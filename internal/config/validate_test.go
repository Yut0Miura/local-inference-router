package config_test

import (
	"strings"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/config"
)

// decodeAndValidate mirrors what Load does, without touching the filesystem.
func decodeAndValidate(t *testing.T, content string) error {
	t.Helper()
	cfg, err := config.Decode([]byte(content))
	if err != nil {
		return err
	}
	return config.Validate(cfg)
}

func replace(old, new string) string { return strings.Replace(validConfig, old, new, 1) }

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"invalid version", replace("version: 1", "version: 2"), "config.version: must equal 1"},
		{"invalid listen", replace(`listen: "127.0.0.1:8080"`, `listen: "not-an-address"`), "config.listen"},
		{"listen port zero", replace(`listen: "127.0.0.1:8080"`, `listen: "127.0.0.1:0"`), "config.listen"},
		{"listen without host", replace(`listen: "127.0.0.1:8080"`, `listen: ":8080"`), "config.listen"},
		{"invalid backend kind", replace(`kind: "ollama"`, `kind: "Ollama"`), "kind: must be one of"},
		{"unknown backend kind", replace(`kind: "ollama"`, `kind: "openai"`), "kind: must be one of"},
		{"backend url with path", replace(`base_url: "http://127.0.0.1:11434"`, `base_url: "http://127.0.0.1:11434/v1"`), "must not contain a path"},
		{"backend url with credentials", replace(`base_url: "http://127.0.0.1:11434"`, `base_url: "http://user:pass@127.0.0.1:11434"`), "must not contain credentials"},
		{"backend url with query", replace(`base_url: "http://127.0.0.1:11434"`, `base_url: "http://127.0.0.1:11434?x=1"`), "must not contain a query"},
		{"backend url with fragment", replace(`base_url: "http://127.0.0.1:11434"`, `base_url: "http://127.0.0.1:11434#frag"`), "must not contain a fragment"},
		{"backend url scheme", replace(`base_url: "http://127.0.0.1:11434"`, `base_url: "ftp://127.0.0.1:11434"`), "must use http or https"},
		{"backend url empty", replace(`base_url: "http://127.0.0.1:11434"`, `base_url: ""`), "must not be empty"},
		{"backend url without hostname",
			replace(
				`base_url: "http://127.0.0.1:11434"`,
				`base_url: "http://:80"`,
			),
			"must contain a host",
		},

		{"backend url with zero port",
			replace(
				`base_url: "http://127.0.0.1:11434"`,
				`base_url: "http://example.com:0"`,
			),
			"port must be between 1 and 65535",
		},

		{"backend url with oversized port",
			replace(
				`base_url: "http://127.0.0.1:11434"`,
				`base_url: "http://example.com:99999"`,
			),
			"port must be between 1 and 65535",
		},

		{"backend url with empty port",
			replace(
				`base_url: "http://127.0.0.1:11434"`,
				`base_url: "http://example.com:"`,
			),
			"must contain a valid port",
		},

		{"backend url with unbracketed ipv6",
			replace(
				`base_url: "http://127.0.0.1:11434"`,
				`base_url: "http://::1"`,
			),
			"is not a valid URL",
		},
		{"max_inflight zero", replace("max_inflight: 4", "max_inflight: 0"), "max_inflight: must be between"},
		{"max_inflight too large", replace("max_inflight: 4", "max_inflight: 100001"), "max_inflight: must be between"},
		{"timeout too small", replace("response_header_timeout_ms: 120000", "response_header_timeout_ms: 999"), "response_header_timeout_ms"},
		{"timeout too large", replace("response_header_timeout_ms: 120000", "response_header_timeout_ms: 900001"), "response_header_timeout_ms"},
		{"idle timeout too small",
			replace("response_idle_timeout_ms: 120000", "response_idle_timeout_ms: 999"),
			"response_idle_timeout_ms",
		},
		{"idle timeout too large",
			replace("response_idle_timeout_ms: 120000", "response_idle_timeout_ms: 900001"),
			"response_idle_timeout_ms",
		},
		{"invalid backend name", replace(`name: "ollama-local"`, `name: "ollama local"`), "name: contains an unsupported character"},
		{"invalid alias name", replace(`name: "local-chat"`, `name: "local chat"`), "name: contains an unsupported character"},
		{"empty upstream model", replace(`upstream_model: "qwen3:0.6b"`, `upstream_model: "   "`), "upstream_model: must not be empty"},
		{"unknown backend reference", replace(`backend: "ollama-local"`, `backend: "missing"`), "unknown backend"},
		{"invalid priority", replace("priority: 10", "priority: -1"), "priority: must be between"},
		{"invalid env name", replace(`bearer_token_env: ""`, `bearer_token_env: "1BAD"`), "invalid environment variable name"},
		{"no backends", "version: 1\nlisten: \"127.0.0.1:8080\"\nbackends: []\naliases: []\n", "config.backends"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := decodeAndValidate(t, tc.content)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidateRejectsDuplicateBackendName(t *testing.T) {
	content := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "b"
    kind: "ollama"
    base_url: "http://127.0.0.1:11434"
    max_inflight: 1
  - name: "b"
    kind: "vllm"
    base_url: "http://127.0.0.1:8000"
    max_inflight: 1
aliases:
  - name: "a"
    routes:
      - backend: "b"
        upstream_model: "m"
        priority: 1
`
	err := decodeAndValidate(t, content)
	if err == nil || !strings.Contains(err.Error(), "duplicate backend name") {
		t.Fatalf("expected a duplicate backend error, got %v", err)
	}
}

func TestValidateRejectsDuplicateBackendOrigin(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
	}{
		{
			name:   "exact same origin",
			first:  "http://model.internal:8000",
			second: "http://model.internal:8000",
		},
		{
			name:   "host casing and default http port",
			first:  "http://MODEL.INTERNAL",
			second: "http://model.internal:80/",
		},
		{
			name:   "host casing and default https port",
			first:  "https://MODEL.INTERNAL",
			second: "https://model.internal:443/",
		},
		{
			name:   "equivalent ipv6 literals",
			first:  "http://[::1]",
			second: "http://[0:0:0:0:0:0:0:1]:80",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "primary"
    kind: "ollama"
    base_url: "` + tc.first + `"
    max_inflight: 2
    response_header_timeout_ms: 120000

  - name: "backup"
    kind: "vllm"
    base_url: "` + tc.second + `"
    max_inflight: 2
    response_header_timeout_ms: 120000

aliases:
  - name: "chat"
    routes:
      - backend: "primary"
        upstream_model: "model-a"
        priority: 10
`

			err := decodeAndValidate(t, content)
			if err == nil {
				t.Fatal("expected duplicate backend origin error")
			}

			if !strings.Contains(err.Error(), "duplicate backend origin") {
				t.Fatalf(
					"error %q does not contain duplicate backend origin",
					err.Error(),
				)
			}

			if !strings.Contains(err.Error(), `backend "primary"`) {
				t.Fatalf(
					"error %q does not identify the first backend",
					err.Error(),
				)
			}
		})
	}
}

func TestValidateAllowsDistinctBackendOrigins(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
	}{
		{
			name:   "different hosts",
			first:  "http://model-a.internal:8000",
			second: "http://model-b.internal:8000",
		},
		{
			name:   "different ports",
			first:  "http://model.internal:8000",
			second: "http://model.internal:8001",
		},
		{
			name:   "different schemes",
			first:  "http://model.internal",
			second: "https://model.internal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "primary"
    kind: "ollama"
    base_url: "` + tc.first + `"
    max_inflight: 2
    response_header_timeout_ms: 120000

  - name: "backup"
    kind: "vllm"
    base_url: "` + tc.second + `"
    max_inflight: 2
    response_header_timeout_ms: 120000

aliases:
  - name: "chat"
    routes:
      - backend: "primary"
        upstream_model: "model-a"
        priority: 10
      - backend: "backup"
        upstream_model: "model-b"
        priority: 20
`

			if err := decodeAndValidate(t, content); err != nil {
				t.Fatalf(
					"distinct backend origins must be allowed: %v",
					err,
				)
			}
		})
	}
}

func TestValidateRejectsDuplicateAliasAndRoute(t *testing.T) {
	base := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "b"
    kind: "ollama"
    base_url: "http://127.0.0.1:11434"
    max_inflight: 1
aliases:
`
	duplicateAlias := base + `  - name: "a"
    routes:
      - backend: "b"
        upstream_model: "m"
        priority: 1
  - name: "a"
    routes:
      - backend: "b"
        upstream_model: "m2"
        priority: 1
`
	if err := decodeAndValidate(t, duplicateAlias); err == nil || !strings.Contains(err.Error(), "duplicate alias name") {
		t.Fatalf("expected a duplicate alias error, got %v", err)
	}

	duplicateRoute := base + `  - name: "a"
    routes:
      - backend: "b"
        upstream_model: "m"
        priority: 1
      - backend: "b"
        upstream_model: "m"
        priority: 2
`
	if err := decodeAndValidate(t, duplicateRoute); err == nil || !strings.Contains(err.Error(), "duplicate route") {
		t.Fatalf("expected a duplicate route error, got %v", err)
	}
}

func TestSameBackendDifferentModelIsAllowed(t *testing.T) {
	content := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "b"
    kind: "llama_cpp"
    base_url: "http://127.0.0.1:8080"
    max_inflight: 2
aliases:
  - name: "a"
    routes:
      - backend: "b"
        upstream_model: "m1"
        priority: 1
      - backend: "b"
        upstream_model: "m2"
        priority: 2
`
	if err := decodeAndValidate(t, content); err != nil {
		t.Fatalf("same backend with a different model must be allowed: %v", err)
	}
}

func TestAliasRequiresRoute(t *testing.T) {
	content := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "b"
    kind: "ollama"
    base_url: "http://127.0.0.1:11434"
    max_inflight: 1
aliases:
  - name: "a"
    routes: []
`
	if err := decodeAndValidate(t, content); err == nil || !strings.Contains(err.Error(), "at least one route") {
		t.Fatalf("expected a missing route error, got %v", err)
	}
}

func TestBaseURLWithTrailingSlashIsAllowed(t *testing.T) {
	content := replace(`base_url: "http://127.0.0.1:11434"`, `base_url: "http://127.0.0.1:11434/"`)
	if err := decodeAndValidate(t, content); err != nil {
		t.Fatalf("a trailing slash origin must be accepted: %v", err)
	}
}

func TestBaseURLAcceptsValidHostAndPortForms(t *testing.T) {
	cases := []string{
		"http://example.com",
		"http://example.com:8080",
		"https://example.com",
		"https://example.com:443",
		"http://[::1]",
		"http://[::1]:8080",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := config.ParseBaseURL(raw); err != nil {
				t.Fatalf("ParseBaseURL(%q): %v", raw, err)
			}
		})
	}
}

func TestBaseURLPortBoundaries(t *testing.T) {
	for _, raw := range []string{
		"http://example.com:1",
		"http://example.com:65535",
	} {
		if _, err := config.ParseBaseURL(raw); err != nil {
			t.Fatalf("ParseBaseURL(%q): %v", raw, err)
		}
	}

	for _, raw := range []string{
		"http://example.com:0",
		"http://example.com:65536",
	} {
		if _, err := config.ParseBaseURL(raw); err == nil {
			t.Fatalf("ParseBaseURL(%q) succeeded, want invalid port", raw)
		}
	}
}

func TestEndpointURLUsesStaticPath(t *testing.T) {
	base, err := config.ParseBaseURL("https://inference.internal:8443/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := config.EndpointURL(base, config.ChatCompletionsPath).String()
	if got != "https://inference.internal:8443/v1/chat/completions" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestHealthPathsByKind(t *testing.T) {
	for kind, want := range map[string]string{
		config.KindOllama:   "/api/version",
		config.KindLlamaCPP: "/health",
		config.KindVLLM:     "/health",
	} {
		if got := config.HealthPath(kind); got != want {
			t.Errorf("HealthPath(%q) = %q, want %q", kind, got, want)
		}
	}
}
