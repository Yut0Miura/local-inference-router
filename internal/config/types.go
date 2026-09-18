// Package config loads and validates the router's operator configuration.
//
// The configuration is trusted operator input, not an untrusted policy
// language. Validation exists to catch operator mistakes early and to keep
// every upstream destination under the operator's control.
package config

// Defaults applied when a field is omitted.
const (
	DefaultListen                  = "127.0.0.1:8080"
	DefaultResponseHeaderTimeoutMS = 120000

	MaxConfigBytes = 1048576
)

// Config is the whole configuration file.
type Config struct {
	Version        int             `yaml:"version"`
	Listen         string          `yaml:"listen"`
	BearerTokenEnv string          `yaml:"bearer_token_env"`
	Backends       []BackendConfig `yaml:"backends"`
	Aliases        []AliasConfig   `yaml:"aliases"`
}

// BackendConfig describes one already-running inference runtime.
type BackendConfig struct {
	Name                    string `yaml:"name"`
	Kind                    string `yaml:"kind"`
	BaseURL                 string `yaml:"base_url"`
	MaxInflight             int    `yaml:"max_inflight"`
	ResponseHeaderTimeoutMS int    `yaml:"response_header_timeout_ms"`
	BearerTokenEnv          string `yaml:"bearer_token_env"`
}

// AliasConfig maps one public model name to backend routes.
type AliasConfig struct {
	Name   string        `yaml:"name"`
	Routes []RouteConfig `yaml:"routes"`
}

// RouteConfig is one candidate backend for an alias. A smaller Priority value
// is preferred.
type RouteConfig struct {
	Backend       string `yaml:"backend"`
	UpstreamModel string `yaml:"upstream_model"`
	Priority      int    `yaml:"priority"`
}

// Backend kinds, case-sensitive.
const (
	KindOllama   = "ollama"
	KindLlamaCPP = "llama_cpp"
	KindVLLM     = "vllm"
)

// HealthPath returns the fixed health endpoint of a backend kind.
func HealthPath(kind string) string {
	switch kind {
	case KindOllama:
		return "/api/version"
	case KindLlamaCPP, KindVLLM:
		return "/health"
	default:
		return ""
	}
}
