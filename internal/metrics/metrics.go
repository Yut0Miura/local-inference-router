// Package metrics holds the router's Prometheus instruments.
//
// Everything is registered on a dedicated registry, never on the global
// default one. Labels are limited to fixed, low-cardinality values: no prompt,
// request id, upstream URL, token or arbitrary error text ever becomes a label.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Route labels for inbound HTTP metrics.
const (
	RouteChatCompletions = "chat_completions"
	RouteModels          = "models"
	RouteHealthz         = "healthz"
	RouteReadyz          = "readyz"
	RouteRouterStatus    = "router_status"
)

// Results of one upstream attempt.
const (
	ResultResponse       = "response"
	ResultTransportError = "transport_error"
	ResultTimeout        = "timeout"
	ResultCanceled       = "canceled"
)

// Failover reasons.
const (
	ReasonTransportError = "transport_error"
	ReasonTimeout        = "timeout"
	ReasonStatus429      = "status_429"
	ReasonStatus502      = "status_502"
	ReasonStatus503      = "status_503"
	ReasonStatus504      = "status_504"
)

// Metrics owns the registry and the router's instruments.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests   *prometheus.CounterVec
	httpDuration   *prometheus.HistogramVec
	attempts       *prometheus.CounterVec
	attemptSeconds *prometheus.HistogramVec
	inflight       *prometheus.GaugeVec
	healthy        *prometheus.GaugeVec
	failovers      *prometheus.CounterVec
}

// New creates the instruments on their own registry.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "local_inference_router_http_requests_total",
			Help: "Inbound HTTP requests handled by the router.",
		}, []string{"route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "local_inference_router_http_request_duration_seconds",
			Help:    "Inbound HTTP request duration, including the full streamed response.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "local_inference_router_backend_attempts_total",
			Help: "Upstream attempts by backend, alias and result.",
		}, []string{"backend", "alias", "result"}),
		attemptSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "local_inference_router_backend_attempt_duration_seconds",
			Help:    "Upstream attempt duration, including the streamed body of a final attempt.",
			Buckets: prometheus.DefBuckets,
		}, []string{"backend", "alias"}),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "local_inference_router_backend_inflight",
			Help: "Inference requests currently reserved on a backend. Health checks are excluded.",
		}, []string{"backend"}),
		healthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "local_inference_router_backend_healthy",
			Help: "1 when the backend passed its last health check, 0 otherwise or when not yet checked.",
		}, []string{"backend"}),
		failovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "local_inference_router_failovers_total",
			Help: "Failovers to another route, by alias and reason.",
		}, []string{"alias", "reason"}),
	}

	m.registry.MustRegister(
		m.httpRequests, m.httpDuration,
		m.attempts, m.attemptSeconds,
		m.inflight, m.healthy, m.failovers,
	)
	return m
}

// Registry exposes the registry for tests.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler serves the Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// ObserveHTTP records one inbound request. The /metrics endpoint itself is not
// instrumented.
func (m *Metrics) ObserveHTTP(route string, status int, seconds float64) {
	m.httpRequests.WithLabelValues(route, statusLabel(status)).Inc()
	m.httpDuration.WithLabelValues(route).Observe(seconds)
}

// ObserveAttempt records one upstream attempt and its duration.
func (m *Metrics) ObserveAttempt(backend, alias, result string, seconds float64) {
	m.attempts.WithLabelValues(backend, alias, result).Inc()
	m.attemptSeconds.WithLabelValues(backend, alias).Observe(seconds)
}

// SetInflight publishes the current reservation count of a backend.
func (m *Metrics) SetInflight(backend string, value int) {
	m.inflight.WithLabelValues(backend).Set(float64(value))
}

// SetHealthy publishes the current health of a backend.
func (m *Metrics) SetHealthy(backend string, healthy bool) {
	value := 0.0
	if healthy {
		value = 1
	}
	m.healthy.WithLabelValues(backend).Set(value)
}

// Failover records a failover to another route.
func (m *Metrics) Failover(alias, reason string) {
	m.failovers.WithLabelValues(alias, reason).Inc()
}

// InitBackend creates the per-backend series so they exist before traffic.
func (m *Metrics) InitBackend(backend string) {
	m.inflight.WithLabelValues(backend).Set(0)
	m.healthy.WithLabelValues(backend).Set(0)
}

func statusLabel(status int) string {
	switch status {
	case 200:
		return "200"
	case 400:
		return "400"
	case 401:
		return "401"
	case 404:
		return "404"
	case 413:
		return "413"
	case 415:
		return "415"
	case 429:
		return "429"
	case 500:
		return "500"
	case 502:
		return "502"
	case 503:
		return "503"
	case 504:
		return "504"
	default:
		// Bounded label set: anything else is grouped by class.
		switch {
		case status >= 200 && status < 300:
			return "2xx"
		case status >= 300 && status < 400:
			return "3xx"
		case status >= 400 && status < 500:
			return "4xx"
		default:
			return "5xx"
		}
	}
}
