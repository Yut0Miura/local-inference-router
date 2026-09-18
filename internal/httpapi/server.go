package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Yut0Miura/local-inference-router/internal/metrics"
	"github.com/Yut0Miura/local-inference-router/internal/router"
)

// Server settings. WriteTimeout is deliberately zero: a fixed write deadline
// would cut off a legitimate long streamed inference response. Streaming
// lifetime is bounded by the client connection, the backend and shutdown.
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 0
	IdleTimeout       = 120 * time.Second
	MaxHeaderBytes    = 1 << 20
	ShutdownTimeout   = 30 * time.Second
)

// Server wires the HTTP endpoints to the router state.
type Server struct {
	state   *router.State
	proxy   *router.Proxy
	metrics *metrics.Metrics
	auth    authenticator
	logger  *slog.Logger
}

// NewServer creates the HTTP API. An empty inboundToken disables inbound
// authentication.
func NewServer(state *router.State, proxy *router.Proxy, m *metrics.Metrics, inboundToken string, logger *slog.Logger) *Server {
	return &Server{
		state:   state,
		proxy:   proxy,
		metrics: m,
		auth:    authenticator{token: inboundToken},
		logger:  logger,
	}
}

// handler is one endpoint that reports the status it wrote.
type handler func(http.ResponseWriter, *http.Request) int

// Handler builds the mux. Only the documented endpoints exist; anything else
// is 404.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/v1/chat/completions", s.instrumented(metrics.RouteChatCompletions, true, s.handleChat))
	mux.Handle("/v1/models", s.instrumented(metrics.RouteModels, true, s.handleModels))
	mux.Handle("/router/status", s.instrumented(metrics.RouteRouterStatus, true, s.handleRouterStatus))

	// Probes never require authentication: they reveal no topology.
	mux.Handle("/healthz", s.instrumented(metrics.RouteHealthz, false, s.handleHealthz))
	mux.Handle("/readyz", s.instrumented(metrics.RouteReadyz, false, s.handleReadyz))

	// /metrics is protected when inbound auth is configured, and is not itself
	// counted in the HTTP request metrics.
	mux.Handle("/metrics", s.protectedRaw(s.metrics.Handler()))

	return mux
}

// instrumented applies authentication where required and records the request.
func (s *Server) instrumented(route string, protected bool, next handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()

		status := http.StatusUnauthorized
		if !protected || s.auth.authorized(r) {
			status = next(w, r)
		} else {
			status = writeUnauthorized(w)
		}

		elapsed := time.Since(started)
		if status == router.StatusClientClosed {
			// The client disconnected before a response was produced.
			s.logger.Info("client canceled request", "route", route, "duration", elapsed)
		}
		s.metrics.ObserveHTTP(route, status, elapsed.Seconds())
	})
}

func (s *Server) protectedRaw(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.authorized(r) {
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewHTTPServer builds the configured net/http server.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}
