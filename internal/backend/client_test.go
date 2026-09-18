package backend_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
)

func TestClientDoesNotFollowRedirects(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := backend.NewClient(0).Get(redirector.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the 302 itself, not the followed target", resp.StatusCode)
	}
}

func TestClientHasNoOverallTimeout(t *testing.T) {
	// A whole-response timeout would cut off long streamed answers.
	if timeout := backend.NewClient(1000).Timeout; timeout != 0 {
		t.Fatalf("inference client timeout = %v, want 0", timeout)
	}
	if timeout := backend.NewHealthClient().Timeout; timeout != backend.HealthTimeout {
		t.Fatalf("health client timeout = %v, want %v", timeout, backend.HealthTimeout)
	}
}

func TestBuildRequiresConfiguredSecret(t *testing.T) {
	cfgs := []config.BackendConfig{{
		Name:                    "b",
		Kind:                    config.KindOllama,
		BaseURL:                 "http://127.0.0.1:11434",
		MaxInflight:             1,
		ResponseHeaderTimeoutMS: 1000,
		BearerTokenEnv:          "MISSING_TOKEN_ENV",
	}}

	if _, err := backend.Build(cfgs, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("serve must fail when a configured secret is absent")
	}
	if _, err := backend.Build(cfgs, func(string) (string, bool) { return "", true }); err == nil {
		t.Fatal("serve must fail when a configured secret is empty")
	}
	if _, err := backend.Build(cfgs, func(string) (string, bool) { return "value", true }); err != nil {
		t.Fatalf("build with a present secret: %v", err)
	}
}

func TestAuthorizeUsesBackendToken(t *testing.T) {
	backends, err := backend.Build([]config.BackendConfig{{
		Name:                    "b",
		Kind:                    config.KindVLLM,
		BaseURL:                 "https://example.invalid",
		MaxInflight:             1,
		ResponseHeaderTimeoutMS: 1000,
		BearerTokenEnv:          "TOKEN_ENV",
	}}, func(string) (string, bool) { return "backend-secret", true })
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "https://example.invalid/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	backends[0].Authorize(req)

	if got := req.Header.Get("Authorization"); got != "Bearer backend-secret" {
		t.Fatalf("Authorization = %q, want the backend token", got)
	}
}

func TestURLsAreBuiltFromConfig(t *testing.T) {
	backends, err := backend.Build([]config.BackendConfig{{
		Name:                    "b",
		Kind:                    config.KindOllama,
		BaseURL:                 "http://127.0.0.1:11434",
		MaxInflight:             1,
		ResponseHeaderTimeoutMS: 1000,
	}}, backend.EnvSecrets)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := backends[0].ChatURL().String(); got != "http://127.0.0.1:11434/v1/chat/completions" {
		t.Fatalf("chat URL = %q", got)
	}
	if got := backends[0].HealthURL().String(); got != "http://127.0.0.1:11434/api/version" {
		t.Fatalf("health URL = %q", got)
	}
}
