package router_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
	"github.com/Yut0Miura/local-inference-router/internal/metrics"
	"github.com/Yut0Miura/local-inference-router/internal/router"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// upstream is a fake inference backend.
type upstream struct {
	server   *httptest.Server
	requests atomic.Int64
	lastBody atomic.Value // []byte
	lastAuth atomic.Value // string
	lastHdr  atomic.Value // http.Header
}

func newUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		u.lastBody.Store(body)
		u.lastAuth.Store(r.Header.Get("Authorization"))
		u.lastHdr.Store(r.Header.Clone())
		handler(w, r)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *upstream) body() string {
	if value, ok := u.lastBody.Load().([]byte); ok {
		return string(value)
	}
	return ""
}

func (u *upstream) header() http.Header {
	if value, ok := u.lastHdr.Load().(http.Header); ok {
		return value
	}
	return http.Header{}
}

func statusHandler(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// buildProxy wires a router over the given upstream URLs, in order, all with
// the same priority unless priorities are given.
func buildProxy(t *testing.T, urls []string, capacities []int, priorities []int) (*router.Proxy, *router.State) {
	t.Helper()

	cfg := &config.Config{Version: 1, Listen: "127.0.0.1:8080"}
	alias := config.AliasConfig{Name: "chat"}
	for i, url := range urls {
		name := string(rune('a' + i))
		capacity := 10
		if i < len(capacities) {
			capacity = capacities[i]
		}
		priority := 10
		if i < len(priorities) {
			priority = priorities[i]
		}
		cfg.Backends = append(cfg.Backends, config.BackendConfig{
			Name:                    name,
			Kind:                    config.KindOllama,
			BaseURL:                 url,
			MaxInflight:             capacity,
			ResponseHeaderTimeoutMS: 1000,
		})
		alias.Routes = append(alias.Routes, config.RouteConfig{
			Backend: name, UpstreamModel: "upstream-" + name, Priority: priority,
		})
	}
	cfg.Aliases = []config.AliasConfig{alias}

	if err := config.Validate(cfg); err != nil {
		t.Fatalf("invalid test configuration: %v", err)
	}
	backends, err := backend.Build(cfg.Backends, backend.EnvSecrets)
	if err != nil {
		t.Fatalf("build backends: %v", err)
	}
	recorder := metrics.New()
	state, err := router.NewState(cfg, backends, recorder)
	if err != nil {
		t.Fatalf("new state: %v", err)
	}
	for _, b := range cfg.Backends {
		state.SetHealth(b.Name, true)
	}
	return router.NewProxy(state, recorder, quietLogger()), state
}

func chatPayload(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return payload
}

func serve(t *testing.T, proxy *router.Proxy, payload map[string]json.RawMessage) (*httptest.ResponseRecorder, int, *router.APIError) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	status, apiErr := proxy.Serve(rec, req, "chat", payload)
	return rec, status, apiErr
}

func TestModelIsRewrittenAndOtherFieldsPreserved(t *testing.T) {
	up := newUpstream(t, statusHandler(http.StatusOK, `{"model":"real-upstream-name","ok":true}`))
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	rec, status, apiErr := serve(t, proxy, chatPayload(t,
		`{"model":"chat","messages":[{"role":"user","content":"hi"}],"temperature":0.2,"custom":{"x":1}}`))
	if apiErr != nil || status != http.StatusOK {
		t.Fatalf("status = %d, err = %+v", status, apiErr)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(up.body()), &sent); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if sent["model"] != "upstream-a" {
		t.Fatalf("upstream model = %v, want the configured upstream_model", sent["model"])
	}
	if sent["temperature"] != 0.2 {
		t.Fatalf("temperature was not preserved: %v", sent["temperature"])
	}
	if _, ok := sent["custom"]; !ok {
		t.Fatal("unknown fields must be passed through")
	}

	// The response model is deliberately not rewritten.
	if !strings.Contains(rec.Body.String(), "real-upstream-name") {
		t.Fatalf("response model must be forwarded untouched: %s", rec.Body.String())
	}
}

func TestFailoverOnRetryableStatuses(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			first := newUpstream(t, statusHandler(status, `{"error":"busy"}`))
			second := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
			proxy, _ := buildProxy(t, []string{first.server.URL, second.server.URL}, nil, []int{10, 20})

			rec, got, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
			if apiErr != nil || got != http.StatusOK {
				t.Fatalf("status = %d, err = %+v", got, apiErr)
			}
			if first.requests.Load() != 1 || second.requests.Load() != 1 {
				t.Fatalf("attempts: first=%d second=%d", first.requests.Load(), second.requests.Load())
			}
			if !strings.Contains(rec.Body.String(), `"ok":true`) {
				t.Fatalf("body = %s, want the second backend's response", rec.Body.String())
			}
		})
	}
}

func TestNoFailoverOnNonRetryableStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusRequestTimeout, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusInternalServerError, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			first := newUpstream(t, statusHandler(status, `{"error":"configuration"}`))
			second := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
			proxy, _ := buildProxy(t, []string{first.server.URL, second.server.URL}, nil, []int{10, 20})

			_, got, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
			if apiErr != nil {
				t.Fatalf("unexpected router error: %+v", apiErr)
			}
			if got != status {
				t.Fatalf("status = %d, want the upstream status %d forwarded", got, status)
			}
			if second.requests.Load() != 0 {
				t.Fatal("a configuration error must not move traffic to another backend")
			}
		})
	}
}

func TestFinalRetryableStatusIsForwarded(t *testing.T) {
	only := newUpstream(t, statusHandler(http.StatusServiceUnavailable, `{"error":"overloaded"}`))
	proxy, _ := buildProxy(t, []string{only.server.URL}, nil, nil)

	rec, status, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr != nil {
		t.Fatalf("the upstream answer must be forwarded, not replaced: %+v", apiErr)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if !strings.Contains(rec.Body.String(), "overloaded") {
		t.Fatalf("body = %s, want the upstream body", rec.Body.String())
	}
}

func TestTransportFailover(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	alive := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
	proxy, _ := buildProxy(t, []string{deadURL, alive.server.URL}, nil, []int{10, 20})

	_, status, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr != nil || status != http.StatusOK {
		t.Fatalf("status = %d, err = %+v", status, apiErr)
	}
	if alive.requests.Load() != 1 {
		t.Fatal("the healthy backend should have been used")
	}
}

func TestFinalTransportFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	proxy, _ := buildProxy(t, []string{deadURL}, nil, nil)
	_, _, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr == nil {
		t.Fatal("expected a router error")
	}
	if apiErr.Status != http.StatusBadGateway || apiErr.Code != "upstream_unavailable" {
		t.Fatalf("error = %+v, want 502 upstream_unavailable", apiErr)
	}
}

func TestFinalTimeoutFailure(t *testing.T) {
	release := make(chan struct{})
	slow := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	})
	defer close(release)

	// response_header_timeout_ms is 1000 in the test configuration.
	proxy, _ := buildProxy(t, []string{slow.server.URL}, nil, nil)
	_, _, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr == nil {
		t.Fatal("expected a router error")
	}
	if apiErr.Status != http.StatusGatewayTimeout || apiErr.Code != "upstream_timeout" {
		t.Fatalf("error = %+v, want 504 upstream_timeout", apiErr)
	}
}

func TestClientCancellationDoesNotFailOver(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	first := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	second := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))

	proxy, state := buildProxy(t, []string{first.server.URL, second.server.URL}, nil, []int{10, 20})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan int, 1)
	go func() {
		status, _ := proxy.Serve(rec, req, "chat", chatPayload(t, `{"model":"chat"}`))
		done <- status
	}()

	<-started
	cancel()

	select {
	case status := <-done:
		if status != router.StatusClientClosed {
			t.Fatalf("status = %d, want the client-closed marker", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the client canceled")
	}

	if second.requests.Load() != 0 {
		t.Fatal("client cancellation must not trigger failover")
	}
	for _, status := range state.Snapshot().Backends {
		if status.Inflight != 0 {
			t.Fatalf("backend %q kept a reservation after cancellation", status.Name)
		}
	}
}

func TestClientHeadersAreNotForwarded(t *testing.T) {
	up := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("Cookie", "session=1")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("Forwarded", "for=10.0.0.1")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	if _, apiErr := proxy.Serve(rec, req, "chat", chatPayload(t, `{"model":"chat"}`)); apiErr != nil {
		t.Fatalf("serve: %+v", apiErr)
	}

	header := up.header()
	if got := header.Get("Authorization"); got != "" {
		t.Fatalf("client Authorization leaked upstream: %q", got)
	}
	for _, name := range []string{"Cookie", "X-Forwarded-For", "Forwarded"} {
		if got := header.Get(name); got != "" {
			t.Fatalf("%s leaked upstream: %q", name, got)
		}
	}
	if got := header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want it forwarded", got)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestResponseHeadersAreFiltered(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Model-Server", "vllm")
		w.Header().Set("Set-Cookie", "session=leak")
		w.Header().Set("Server", "upstream/1.0")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	rec, _, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr != nil {
		t.Fatalf("serve: %+v", apiErr)
	}
	if rec.Header().Get("X-Model-Server") != "vllm" {
		t.Fatal("end-to-end headers must be copied")
	}
	for _, name := range []string{"Set-Cookie", "Server", "Transfer-Encoding", "Connection"} {
		if got := rec.Header().Get(name); got != "" {
			t.Fatalf("%s must not be copied downstream: %q", name, got)
		}
	}
}

func TestRedirectIsForwardedNotFollowed(t *testing.T) {
	target := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
	redirector := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.server.URL)
		w.WriteHeader(http.StatusFound)
	})
	second := newUpstream(t, statusHandler(http.StatusOK, `{"second":true}`))

	proxy, _ := buildProxy(t, []string{redirector.server.URL, second.server.URL}, nil, []int{10, 20})
	_, status, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr != nil {
		t.Fatalf("serve: %+v", apiErr)
	}
	if status != http.StatusFound {
		t.Fatalf("status = %d, want the 302 forwarded", status)
	}
	if target.requests.Load() != 0 {
		t.Fatal("the redirect must not be followed")
	}
	if second.requests.Load() != 0 {
		t.Fatal("a 3xx is not failover-eligible")
	}
}

func TestNoRouteAvailable(t *testing.T) {
	up := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
	proxy, state := buildProxy(t, []string{up.server.URL}, []int{1}, nil)
	state.SetHealth("a", false)

	_, _, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`))
	if apiErr == nil || apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != "no_route_available" {
		t.Fatalf("error = %+v, want 503 no_route_available", apiErr)
	}
}

func TestUnknownAliasIsRejected(t *testing.T) {
	up := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
	proxy, _ := buildProxy(t, []string{up.server.URL}, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	_, apiErr := proxy.Serve(rec, req, "missing", chatPayload(t, `{"model":"missing"}`))
	if apiErr == nil || apiErr.Status != http.StatusNotFound || apiErr.Code != "model_not_found" {
		t.Fatalf("error = %+v, want 404 model_not_found", apiErr)
	}
}

func TestReservationIsReleasedAfterEveryOutcome(t *testing.T) {
	ok := newUpstream(t, statusHandler(http.StatusOK, `{"ok":true}`))
	retryable := newUpstream(t, statusHandler(http.StatusServiceUnavailable, `{}`))
	proxy, state := buildProxy(t, []string{retryable.server.URL, ok.server.URL}, nil, []int{10, 20})

	for i := 0; i < 5; i++ {
		if _, _, apiErr := serve(t, proxy, chatPayload(t, `{"model":"chat"}`)); apiErr != nil {
			t.Fatalf("serve: %+v", apiErr)
		}
	}
	for _, status := range state.Snapshot().Backends {
		if status.Inflight != 0 {
			t.Fatalf("backend %q kept %d reservations", status.Name, status.Inflight)
		}
	}
}
