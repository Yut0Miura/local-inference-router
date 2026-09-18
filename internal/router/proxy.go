package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Yut0Miura/local-inference-router/internal/metrics"
)

// StatusClientClosed is reported when the client went away before the router
// produced a response. Nothing is written downstream in that case.
const StatusClientClosed = 499

// APIError is a router-generated error for the client. The proxy never formats
// JSON itself; the HTTP layer renders this.
type APIError struct {
	Status  int
	Message string
	Type    string
	Code    string
}

// Errors the router can produce on its own.
var (
	errUnknownAlias = &APIError{
		Status:  http.StatusNotFound,
		Message: "model alias not found",
		Type:    "invalid_request_error",
		Code:    "model_not_found",
	}
	errNoRoute = &APIError{
		Status:  http.StatusServiceUnavailable,
		Message: "no healthy backend route available",
		Type:    "router_unavailable",
		Code:    "no_route_available",
	}
	errUpstreamTimeout = &APIError{
		Status:  http.StatusGatewayTimeout,
		Message: "upstream request timed out",
		Type:    "router_upstream_error",
		Code:    "upstream_timeout",
	}
	errUpstreamUnavailable = &APIError{
		Status:  http.StatusBadGateway,
		Message: "upstream backend unavailable",
		Type:    "router_upstream_error",
		Code:    "upstream_unavailable",
	}
	errStreamUnsupported = &APIError{
		Status:  http.StatusInternalServerError,
		Message: "router cannot stream this response",
		Type:    "router_error",
		Code:    "streaming_unsupported",
	}
)

// Proxy performs one client request against the configured routes.
type Proxy struct {
	state   *State
	metrics *metrics.Metrics
	logger  *slog.Logger
}

// NewProxy creates the proxy.
func NewProxy(state *State, m *metrics.Metrics, logger *slog.Logger) *Proxy {
	return &Proxy{state: state, metrics: m, logger: logger}
}

// retryableStatuses are the only upstream statuses that may move a request to
// another route. Everything else, including 401, 404 and 500, is forwarded:
// those usually mean a configuration problem, and hiding them by silently
// trying another backend would make it invisible.
func retryableStatus(status int) (string, bool) {
	switch status {
	case http.StatusTooManyRequests:
		return metrics.ReasonStatus429, true
	case http.StatusBadGateway:
		return metrics.ReasonStatus502, true
	case http.StatusServiceUnavailable:
		return metrics.ReasonStatus503, true
	case http.StatusGatewayTimeout:
		return metrics.ReasonStatus504, true
	default:
		return "", false
	}
}

// Serve routes one chat-completion request.
//
// Failover is bounded: the router may try another route only while nothing has
// been written downstream. Once the response is committed, switching backends
// could only produce a corrupted body, so the attempt is final.
func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request, alias string, payload map[string]json.RawMessage) (int, *APIError) {
	attempted := make(map[*Route]bool)

	route, err := p.state.Reserve(alias, attempted)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownAlias):
			return 0, errUnknownAlias
		default:
			return 0, errNoRoute
		}
	}

	for {
		attempted[route] = true

		resp, elapsed, attemptErr := p.attempt(r, route, payload)

		if attemptErr != nil {
			p.state.Release(route)

			if isCanceled(r.Context(), attemptErr) {
				// The client left. This is not a backend failure and must not
				// trigger failover.
				p.observe(route, metrics.ResultCanceled, elapsed)
				return StatusClientClosed, nil
			}

			reason, final := metrics.ReasonTransportError, errUpstreamUnavailable
			result := metrics.ResultTransportError
			if isTimeout(attemptErr) {
				reason, final, result = metrics.ReasonTimeout, errUpstreamTimeout, metrics.ResultTimeout
			}
			p.observe(route, result, elapsed)
			p.logger.Warn("upstream attempt failed",
				"alias", alias, "backend", route.Backend.Name, "reason", reason)

			next, nextErr := p.state.Reserve(alias, attempted)
			if nextErr != nil {
				return 0, final
			}
			p.metrics.Failover(alias, reason)
			route = next
			continue
		}

		p.observe(route, metrics.ResultResponse, elapsed)

		if reason, ok := retryableStatus(resp.StatusCode); ok {
			// Reserve the next route before releasing the current one, so the
			// capacity just freed cannot be taken by another request first.
			next, nextErr := p.state.Reserve(alias, attempted)
			if nextErr == nil {
				resp.Body.Close()
				p.state.Release(route)
				p.metrics.Failover(alias, reason)
				p.logger.Info("failing over",
					"alias", alias, "backend", route.Backend.Name, "status", resp.StatusCode)
				route = next
				continue
			}
			// No alternative: the upstream status is the honest answer and is
			// forwarded as-is instead of a synthetic router error.
		}

		status, apiErr := p.forward(w, resp, route, alias)
		p.state.Release(route)
		return status, apiErr
	}
}

// attempt performs one upstream request. The returned response body is open.
func (p *Proxy) attempt(r *http.Request, route *Route, payload map[string]json.RawMessage) (*http.Response, time.Duration, error) {
	started := time.Now()

	body, err := encodeWithModel(payload, route.UpstreamModel)
	if err != nil {
		return nil, time.Since(started), err
	}

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		route.Backend.ChatURL().String(), bytes.NewReader(body))
	if err != nil {
		return nil, time.Since(started), err
	}
	setUpstreamHeaders(upstream, r, route)
	upstream.ContentLength = int64(len(body))

	resp, err := route.Backend.Client().Do(upstream)
	return resp, time.Since(started), err
}

// forward sends the upstream response downstream. After WriteHeader the
// response is committed and no failover is possible.
func (p *Proxy) forward(w http.ResponseWriter, resp *http.Response, route *Route, alias string) (int, *APIError) {
	defer resp.Body.Close()

	streaming := isEventStream(resp.Header)
	flusher, flushable := w.(http.Flusher)
	if streaming && !flushable {
		// Nothing has been written yet, so this can still be reported cleanly.
		p.logger.Error("downstream writer cannot flush a stream",
			"alias", alias, "backend", route.Backend.Name)
		return 0, errStreamUnsupported
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	var sink io.Writer = w
	if streaming {
		sink = flushWriter{w: w, f: flusher}
	}

	if err := relay(sink, resp.Body); err != nil {
		// Committed: no retry, no backend switch. The body is never logged.
		p.logger.Error("response relay failed",
			"alias", alias, "backend", route.Backend.Name, "status", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (p *Proxy) observe(route *Route, result string, elapsed time.Duration) {
	if p.metrics != nil {
		p.metrics.ObserveAttempt(route.Backend.Name, route.Alias, result, elapsed.Seconds())
	}
}

// encodeWithModel replaces the public alias with the route's upstream model and
// leaves every other field as the client sent it.
func encodeWithModel(payload map[string]json.RawMessage, upstreamModel string) ([]byte, error) {
	rewritten := make(map[string]json.RawMessage, len(payload))
	for key, value := range payload {
		rewritten[key] = value
	}
	model, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, err
	}
	rewritten["model"] = model
	return json.Marshal(rewritten)
}

func isCanceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || ctx.Err() != nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return true
	}
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}
