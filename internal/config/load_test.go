package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/config"
)

const validConfig = `version: 1
listen: "127.0.0.1:8080"
bearer_token_env: ""
backends:
  - name: "ollama-local"
    kind: "ollama"
    base_url: "http://127.0.0.1:11434"
    max_inflight: 4
    response_header_timeout_ms: 120000
    bearer_token_env: ""
aliases:
  - name: "local-chat"
    routes:
      - backend: "ollama-local"
        upstream_model: "qwen3:0.6b"
        priority: 10
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "examples", "config.yaml"))
	if err != nil {
		t.Fatalf("examples/config.yaml must load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8080" || len(cfg.Backends) != 1 || len(cfg.Aliases) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Aliases[0].Routes[0].UpstreamModel != "qwen3:0.6b" {
		t.Fatalf("unexpected upstream model: %+v", cfg.Aliases[0].Routes[0])
	}
}

func TestLoadComposeConfig(t *testing.T) {
	if _, err := config.Load(filepath.Join("..", "..", "examples", "compose-config.yaml")); err != nil {
		t.Fatalf("examples/compose-config.yaml must load: %v", err)
	}
}

func TestEmptyFile(t *testing.T) {
	for name, content := range map[string]string{
		"empty":      "",
		"whitespace": "   \n\t\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, content))
			requireErrorContains(t, err, "config: file is empty")
		})
	}
}

func TestOversizedConfig(t *testing.T) {
	padding := strings.Repeat("#", config.MaxConfigBytes+1)
	_, err := config.Load(writeConfig(t, validConfig+"\n"+padding))
	requireErrorContains(t, err, "larger than")
}

func TestUnknownField(t *testing.T) {
	_, err := config.Load(writeConfig(t, validConfig+"unexpected_field: 1\n"))
	requireErrorContains(t, err, "field unexpected_field not found")
}

func TestSecondDocument(t *testing.T) {
	_, err := config.Load(writeConfig(t, validConfig+"---\nversion: 1\n"))
	requireErrorContains(t, err, "multiple YAML documents are not allowed")
}

func TestListenDefaultsToLoopback(t *testing.T) {
	content := strings.Replace(validConfig, `listen: "127.0.0.1:8080"`, `listen: ""`, 1)
	cfg, err := config.Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != config.DefaultListen {
		t.Fatalf("listen = %q, want the loopback default %q", cfg.Listen, config.DefaultListen)
	}
}

func TestResponseHeaderTimeoutDefault(t *testing.T) {
	content := strings.Replace(validConfig, "    response_header_timeout_ms: 120000\n", "", 1)
	cfg, err := config.Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Backends[0].ResponseHeaderTimeoutMS; got != config.DefaultResponseHeaderTimeoutMS {
		t.Fatalf("response_header_timeout_ms = %d, want %d", got, config.DefaultResponseHeaderTimeoutMS)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	requireErrorContains(t, err, "config:")
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}
