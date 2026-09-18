package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
	"github.com/Yut0Miura/local-inference-router/internal/httpapi"
	"github.com/Yut0Miura/local-inference-router/internal/metrics"
	"github.com/Yut0Miura/local-inference-router/internal/router"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type harness struct {
	handler http.Handler
	state   *router.State
	metrics *metrics.Metrics
}

// newHarness builds a router in front of one upstream, with optional inbound
// authentication.
func newHarness(t *testing.T, upstreamURL, inboundToken string) *harness {
	t.Helper()

	cfg := &config.Config{
		Version: 1,
		Listen:  "127.0.0.1:8080",
		Backends: []config.BackendConfig{{
			Name:                    "a",
			Kind:                    config.KindOllama,
			BaseURL:                 upstreamURL,
			MaxInflight:             4,
			ResponseHeaderTimeoutMS: 1000,
		}},
		Aliases: []config.AliasConfig{{
			Name:   "local-chat",
			Routes: []config.RouteConfig{{Backend: "a", UpstreamModel: "qwen3:0.6b", Priority: 10}},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("configuration: %v", err)
	}
	backends, err := backend.Build(cfg.Backends, backend.EnvSecrets)
	if err != nil {
		t.Fatalf("backends: %v", err)
	}
	recorder := metrics.New()
	state, err := router.NewState(cfg, backends, recorder)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	proxy := router.NewProxy(state, recorder, quietLogger())
	server := httpapi.NewServer(state, proxy, recorder, inboundToken, quietLogger())
	return &harness{handler: server.Handler(), state: state, metrics: recorder}
}

func (h *harness) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func chatRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func okUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestUnknownPathIs404(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	for _, path := range []string{"/", "/v1/completions", "/v1/embeddings", "/v1/responses", "/unknown"} {
		rec := h.do(t, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestChatRequiresJSONContentType(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	req := chatRequest(`{"model":"local-chat"}`)
	req.Header.Set("Content-Type", "text/plain")
	if rec := h.do(t, req); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}

	withParams := chatRequest(`{"model":"local-chat"}`)
	withParams.Header.Set("Content-Type", "application/json; charset=utf-8")
	if rec := h.do(t, withParams); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a parameterised media type", rec.Code)
	}
}

func TestChatRejectsContentEncoding(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	req := chatRequest(`{"model":"local-chat"}`)
	req.Header.Set("Content-Encoding", "gzip")
	if rec := h.do(t, req); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 for a compressed body", rec.Code)
	}
}

func TestChatBodyLimit(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	padding := strings.Repeat("x", httpapi.MaxChatRequestBytes)
	body := `{"model":"local-chat","padding":"` + padding + `"}`
	if rec := h.do(t, chatRequest(body)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestChatRequiresModel(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	for _, body := range []string{`{}`, `{"model":""}`, `{"model":123}`, `[]`, `"text"`, `{`} {
		if rec := h.do(t, chatRequest(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestUnknownAliasReturns404(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	rec := h.do(t, chatRequest(`{"model":"absent"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body["error"]["code"] != "model_not_found" {
		t.Fatalf("code = %q", body["error"]["code"])
	}
}

func TestModelsListsConfiguredAliases(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	// No health check has run: aliases are configuration, not discovery.
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("body: %v", err)
	}
	if list.Object != "list" || len(list.Data) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
	entry := list.Data[0]
	if entry.ID != "local-chat" || entry.Object != "model" || entry.Created != 0 || entry.OwnedBy != "local-inference-router" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestHealthzAndReadyz(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}

	rec = h.do(t, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "not ready\n" {
		t.Fatalf("readyz before health = %d %q", rec.Code, rec.Body.String())
	}

	h.state.SetHealth("a", true)
	rec = h.do(t, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ready\n" {
		t.Fatalf("readyz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestRouterStatusHidesTopology(t *testing.T) {
	upstream := okUpstream(t)
	h := newHarness(t, upstream.URL, "")
	h.state.SetHealth("a", true)

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/router/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range []string{upstream.URL, "qwen3:0.6b", "bearer", "Bearer"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status exposes %q:\n%s", secret, body)
		}
	}
	if !strings.Contains(body, `"max_inflight": 4`) || !strings.Contains(body, `"route_count": 1`) {
		t.Fatalf("unexpected status body:\n%s", body)
	}
}

func TestMetricsAreRecorded(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	if rec := h.do(t, chatRequest(`{"model":"local-chat"}`)); rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d", rec.Code)
	}
	h.do(t, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`local_inference_router_http_requests_total{route="chat_completions",status="200"} 1`,
		`local_inference_router_http_requests_total{route="healthz",status="200"} 1`,
		`local_inference_router_backend_attempts_total{alias="local-chat",backend="a",result="response"} 1`,
		`local_inference_router_backend_inflight{backend="a"} 0`,
		`local_inference_router_backend_healthy{backend="a"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output lacks %q", want)
		}
	}
	if strings.Contains(body, `route="metrics"`) {
		t.Error("/metrics must not instrument itself")
	}
}

func TestMethodPolicy(t *testing.T) {
	h := newHarness(t, okUpstream(t).URL, "")
	h.state.SetHealth("a", true)

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET chat = %d, Allow = %q", rec.Code, rec.Header().Get("Allow"))
	}

	rec = h.do(t, httptest.NewRequest(http.MethodPost, "/v1/models", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST models = %d, Allow = %q", rec.Code, rec.Header().Get("Allow"))
	}
}
