// Package backend describes the already-running inference runtimes the router
// talks to, and checks whether they are reachable.
package backend

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Yut0Miura/local-inference-router/internal/config"
)

// Backend is one validated inference runtime.
type Backend struct {
	Name                  string
	Kind                  string
	BaseURL               *url.URL
	MaxInflight           int
	ResponseHeaderTimeout time.Duration

	// token is the backend's own credential. It is never logged and never
	// taken from a client request.
	token string

	client *http.Client
}

// ChatURL is the upstream inference endpoint for this backend.
func (b *Backend) ChatURL() *url.URL {
	return config.EndpointURL(b.BaseURL, config.ChatCompletionsPath)
}

// HealthURL is the fixed health endpoint for this backend kind.
func (b *Backend) HealthURL() *url.URL {
	return config.EndpointURL(b.BaseURL, config.HealthPath(b.Kind))
}

// Client is the HTTP client used for inference requests.
func (b *Backend) Client() *http.Client { return b.client }

// Authorize sets the backend's own Authorization header, if configured.
func (b *Backend) Authorize(req *http.Request) {
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
}

// SecretResolver reports the value of an environment variable.
type SecretResolver func(name string) (string, bool)

// EnvSecrets reads secrets from the process environment.
func EnvSecrets(name string) (string, bool) { return os.LookupEnv(name) }

// Build turns validated configuration into backends. Configured secrets must
// exist and be non-empty; serve fails to start otherwise.
func Build(cfgs []config.BackendConfig, secrets SecretResolver) ([]*Backend, error) {
	backends := make([]*Backend, 0, len(cfgs))
	for _, cfg := range cfgs {
		base, err := config.ParseBaseURL(cfg.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("backend %q: base_url: %w", cfg.Name, err)
		}

		token := ""
		if cfg.BearerTokenEnv != "" {
			value, ok := secrets(cfg.BearerTokenEnv)
			if !ok || value == "" {
				return nil, fmt.Errorf("backend %q: environment variable %s is not set",
					cfg.Name, cfg.BearerTokenEnv)
			}
			token = value
		}

		timeout := time.Duration(cfg.ResponseHeaderTimeoutMS) * time.Millisecond
		backends = append(backends, &Backend{
			Name:                  cfg.Name,
			Kind:                  cfg.Kind,
			BaseURL:               base,
			MaxInflight:           cfg.MaxInflight,
			ResponseHeaderTimeout: timeout,
			token:                 token,
			client:                NewClient(timeout),
		})
	}
	return backends, nil
}
