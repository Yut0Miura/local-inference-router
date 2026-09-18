package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/cli"
)

func run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"unknown"},
		{"serve"},
		{"validate"},
		{"validate", "-config"},
		{"validate", "-config", "x.yaml", "extra"},
	}
	for _, args := range cases {
		code, _, stderr := run(args...)
		if code != cli.ExitUsage {
			t.Errorf("args %v: exit %d, want %d", args, code, cli.ExitUsage)
		}
		if args != nil && len(args) <= 1 && !strings.Contains(stderr, "usage: local-inference-router") {
			t.Errorf("args %v: usage line missing from stderr: %q", args, stderr)
		}
	}
}

func TestValidateSuccess(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "config.yaml")
	code, stdout, stderr := run("validate", "-config", path)
	if code != cli.ExitSuccess {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if stdout != "config valid\n" {
		t.Fatalf("stdout = %q, want \"config valid\\n\"", stdout)
	}
}

func TestValidateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("version: 2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	code, stdout, stderr := run("validate", "-config", path)
	if code != cli.ExitError {
		t.Fatalf("exit %d, want %d", code, cli.ExitError)
	}
	if stdout != "" {
		t.Fatalf("stdout must stay empty on failure: %q", stdout)
	}
	if !strings.Contains(stderr, "config.version") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// TestValidateDoesNotNetwork points the configuration at an address nothing
// listens on. Validation must still succeed: it never contacts a backend.
func TestValidateDoesNotNetwork(t *testing.T) {
	content := `version: 1
listen: "127.0.0.1:8080"
backends:
  - name: "offline"
    kind: "vllm"
    base_url: "http://127.0.0.1:1"
    max_inflight: 1
    response_header_timeout_ms: 1000
aliases:
  - name: "chat"
    routes:
      - backend: "offline"
        upstream_model: "m"
        priority: 1
`
	path := filepath.Join(t.TempDir(), "offline.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code, stdout, stderr := run("validate", "-config", path); code != cli.ExitSuccess || stdout != "config valid\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

// TestValidateIgnoresMissingSecrets: validate checks names, serve requires
// values.
func TestValidateIgnoresMissingSecrets(t *testing.T) {
	content := `version: 1
listen: "127.0.0.1:8080"
bearer_token_env: "ROUTER_TOKEN_THAT_IS_NOT_SET"
backends:
  - name: "b"
    kind: "ollama"
    base_url: "http://127.0.0.1:11434"
    max_inflight: 1
    response_header_timeout_ms: 1000
    bearer_token_env: "BACKEND_TOKEN_THAT_IS_NOT_SET"
aliases:
  - name: "chat"
    routes:
      - backend: "b"
        upstream_model: "m"
        priority: 1
`
	path := filepath.Join(t.TempDir(), "secrets.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if code, _, stderr := run("validate", "-config", path); code != cli.ExitSuccess {
		t.Fatalf("validate must not require secret values: exit %d, %s", code, stderr)
	}

	code, _, stderr := run("serve", "-config", path)
	if code != cli.ExitError {
		t.Fatalf("serve must fail without the configured secret: exit %d", code)
	}
	if !strings.Contains(stderr, "ROUTER_TOKEN_THAT_IS_NOT_SET") {
		t.Fatalf("stderr = %q", stderr)
	}
}
