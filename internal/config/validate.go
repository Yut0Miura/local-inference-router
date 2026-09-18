package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Validation bounds.
const (
	MinMaxInflight = 1
	MaxMaxInflight = 100000

	MinResponseHeaderTimeoutMS = 1000
	MaxResponseHeaderTimeoutMS = 900000

	MaxPriority = 1000000

	maxBackendNameLen   = 64
	maxAliasNameLen     = 128
	maxUpstreamModelLen = 256
)

// Validate checks the semantics of an already decoded configuration.
// It performs no network access.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: file is empty")
	}
	if cfg.Version != 1 {
		return fmt.Errorf("config.version: must equal 1")
	}
	if err := validateListen(cfg.Listen); err != nil {
		return err
	}
	if err := validateEnvName(cfg.BearerTokenEnv); err != nil {
		return fmt.Errorf("config.bearer_token_env: %w", err)
	}

	backends, err := validateBackends(cfg.Backends)
	if err != nil {
		return err
	}
	return validateAliases(cfg.Aliases, backends)
}

func validateListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("config.listen: invalid listen address")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("config.listen: invalid listen address")
	}
	return nil
}

func validateBackends(backends []BackendConfig) (map[string]struct{}, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("config.backends: at least one backend is required")
	}

	known := make(map[string]struct{}, len(backends))
	for i, backend := range backends {
		where := fmt.Sprintf("config.backends[%d]", i)

		if err := validateName(backend.Name, maxBackendNameLen, isBackendNameByte); err != nil {
			return nil, fmt.Errorf("%s.name: %w", where, err)
		}
		if _, exists := known[backend.Name]; exists {
			return nil, fmt.Errorf("%s.name: duplicate backend name %q", where, backend.Name)
		}
		known[backend.Name] = struct{}{}

		switch backend.Kind {
		case KindOllama, KindLlamaCPP, KindVLLM:
		default:
			return nil, fmt.Errorf("%s.kind: must be one of ollama, llama_cpp, vllm", where)
		}

		if _, err := ParseBaseURL(backend.BaseURL); err != nil {
			return nil, fmt.Errorf("%s.base_url: %w", where, err)
		}
		if backend.MaxInflight < MinMaxInflight || backend.MaxInflight > MaxMaxInflight {
			return nil, fmt.Errorf("%s.max_inflight: must be between %d and %d",
				where, MinMaxInflight, MaxMaxInflight)
		}
		if backend.ResponseHeaderTimeoutMS < MinResponseHeaderTimeoutMS ||
			backend.ResponseHeaderTimeoutMS > MaxResponseHeaderTimeoutMS {
			return nil, fmt.Errorf("%s.response_header_timeout_ms: must be between %d and %d",
				where, MinResponseHeaderTimeoutMS, MaxResponseHeaderTimeoutMS)
		}
		if err := validateEnvName(backend.BearerTokenEnv); err != nil {
			return nil, fmt.Errorf("%s.bearer_token_env: %w", where, err)
		}
	}
	return known, nil
}

func validateAliases(aliases []AliasConfig, backends map[string]struct{}) error {
	if len(aliases) == 0 {
		return fmt.Errorf("config.aliases: at least one alias is required")
	}

	seenAlias := make(map[string]struct{}, len(aliases))
	for i, alias := range aliases {
		where := fmt.Sprintf("config.aliases[%d]", i)

		if err := validateName(alias.Name, maxAliasNameLen, isAliasNameByte); err != nil {
			return fmt.Errorf("%s.name: %w", where, err)
		}
		if _, exists := seenAlias[alias.Name]; exists {
			return fmt.Errorf("%s.name: duplicate alias name %q", where, alias.Name)
		}
		seenAlias[alias.Name] = struct{}{}

		if len(alias.Routes) == 0 {
			return fmt.Errorf("%s.routes: at least one route is required", where)
		}

		seenRoute := make(map[string]struct{}, len(alias.Routes))
		for j, route := range alias.Routes {
			routeWhere := fmt.Sprintf("%s.routes[%d]", where, j)

			if _, exists := backends[route.Backend]; !exists {
				return fmt.Errorf("%s.backend: unknown backend %q", routeWhere, route.Backend)
			}
			model := strings.TrimSpace(route.UpstreamModel)
			if model == "" {
				return fmt.Errorf("%s.upstream_model: must not be empty", routeWhere)
			}
			if len(model) > maxUpstreamModelLen {
				return fmt.Errorf("%s.upstream_model: must be at most %d bytes", routeWhere, maxUpstreamModelLen)
			}
			if hasControlBytes(model) {
				return fmt.Errorf("%s.upstream_model: must not contain control characters", routeWhere)
			}
			if route.Priority < 0 || route.Priority > MaxPriority {
				return fmt.Errorf("%s.priority: must be between 0 and %d", routeWhere, MaxPriority)
			}

			key := route.Backend + "\x00" + model
			if _, exists := seenRoute[key]; exists {
				return fmt.Errorf("%s: duplicate route for backend %q and model %q",
					routeWhere, route.Backend, model)
			}
			seenRoute[key] = struct{}{}
		}
	}
	return nil
}

// validateEnvName accepts an empty value or a valid environment variable name.
func validateEnvName(name string) error {
	if name == "" {
		return nil
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("invalid environment variable name %q", name)
		}
	}
	return nil
}

func validateName(name string, maxLen int, allowed func(byte) bool) error {
	if name == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(name) > maxLen {
		return fmt.Errorf("must be at most %d bytes", maxLen)
	}
	for i := 0; i < len(name); i++ {
		if !allowed(name[i]) {
			return fmt.Errorf("contains an unsupported character")
		}
	}
	return nil
}

func isBackendNameByte(c byte) bool {
	return isAlphanumeric(c) || c == '_' || c == '.' || c == '-'
}

func isAliasNameByte(c byte) bool {
	return isAlphanumeric(c) || c == '_' || c == '.' || c == ':' || c == '/' || c == '-'
}

func isAlphanumeric(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func hasControlBytes(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}
