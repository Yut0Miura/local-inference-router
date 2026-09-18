// Package router holds the shared routing state and the upstream proxy.
package router

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
	"github.com/Yut0Miura/local-inference-router/internal/metrics"
)

// ErrNoRoute means no configured route is currently healthy and below
// capacity. The router never queues: the request fails immediately.
var ErrNoRoute = errors.New("no healthy backend route available")

// ErrUnknownAlias means the requested model alias is not configured.
var ErrUnknownAlias = errors.New("model alias not found")

// Route is one candidate upstream for an alias.
type Route struct {
	Alias         string
	Backend       *backend.Backend
	UpstreamModel string
	Priority      int
	Order         int // position in the configuration, breaks exact ties
}

type backendState struct {
	backend  *backend.Backend
	checked  bool
	healthy  bool
	inflight int
}

type aliasState struct {
	name   string
	routes []*Route
}

// State holds health, capacity and reservations behind one mutex. Selection
// and the reservation that follows it happen under the same lock, so two
// concurrent requests can never overshoot a backend's capacity.
type State struct {
	mu         sync.Mutex
	backends   map[string]*backendState
	order      []*backendState
	aliases    map[string]*aliasState
	aliasOrder []*aliasState

	metrics *metrics.Metrics
}

// NewState builds routing state from validated configuration.
func NewState(cfg *config.Config, backends []*backend.Backend, m *metrics.Metrics) (*State, error) {
	state := &State{
		backends: make(map[string]*backendState, len(backends)),
		aliases:  make(map[string]*aliasState, len(cfg.Aliases)),
		metrics:  m,
	}

	for _, b := range backends {
		bs := &backendState{backend: b}
		state.backends[b.Name] = bs
		state.order = append(state.order, bs)
		if m != nil {
			m.InitBackend(b.Name)
		}
	}

	for _, aliasCfg := range cfg.Aliases {
		alias := &aliasState{name: aliasCfg.Name}
		for i, routeCfg := range aliasCfg.Routes {
			bs, ok := state.backends[routeCfg.Backend]
			if !ok {
				return nil, fmt.Errorf("alias %q: unknown backend %q", aliasCfg.Name, routeCfg.Backend)
			}
			alias.routes = append(alias.routes, &Route{
				Alias:         aliasCfg.Name,
				Backend:       bs.backend,
				UpstreamModel: routeCfg.UpstreamModel,
				Priority:      routeCfg.Priority,
				Order:         i,
			})
		}
		state.aliases[aliasCfg.Name] = alias
		state.aliasOrder = append(state.aliasOrder, alias)
	}
	return state, nil
}

// SetHealth records the result of a health check.
func (s *State) SetHealth(name string, healthy bool) {
	s.mu.Lock()
	bs, ok := s.backends[name]
	if ok {
		bs.checked = true
		bs.healthy = healthy
	}
	s.mu.Unlock()

	if ok && s.metrics != nil {
		s.metrics.SetHealthy(name, healthy)
	}
}

// HasAlias reports whether an alias is configured. Matching is exact and
// case-sensitive.
func (s *State) HasAlias(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.aliases[name]
	return ok
}

// RouteCount returns the number of routes configured for an alias.
func (s *State) RouteCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias, ok := s.aliases[name]; ok {
		return len(alias.routes)
	}
	return 0
}

// Reserve selects a route for an alias and reserves capacity on its backend in
// one atomic step. Routes in attempted are skipped, so a route is tried at most
// once per client request.
func (s *State) Reserve(aliasName string, attempted map[*Route]bool) (*Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	alias, ok := s.aliases[aliasName]
	if !ok {
		return nil, ErrUnknownAlias
	}

	selected := s.selectLocked(alias, attempted)
	if selected == nil {
		return nil, ErrNoRoute
	}

	bs := s.backends[selected.Backend.Name]
	bs.inflight++
	if s.metrics != nil {
		s.metrics.SetInflight(bs.backend.Name, bs.inflight)
	}
	return selected, nil
}

// Release returns the capacity reserved for a route. It is called exactly once
// per successful Reserve, whatever ends the attempt.
func (s *State) Release(route *Route) {
	if route == nil {
		return
	}
	s.mu.Lock()
	bs, ok := s.backends[route.Backend.Name]
	if ok && bs.inflight > 0 {
		bs.inflight--
	}
	value := 0
	if ok {
		value = bs.inflight
	}
	s.mu.Unlock()

	if ok && s.metrics != nil {
		s.metrics.SetInflight(route.Backend.Name, value)
	}
}

// Ready reports whether every configured alias has at least one checked and
// healthy backend route. Saturated capacity does not make the router unready.
func (s *State) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, alias := range s.aliasOrder {
		if !s.aliasReadyLocked(alias) {
			return false
		}
	}
	return true
}

func (s *State) aliasReadyLocked(alias *aliasState) bool {
	for _, route := range alias.routes {
		bs := s.backends[route.Backend.Name]
		if bs.checked && bs.healthy {
			return true
		}
	}
	return false
}

// BackendStatus is the public view of one backend. It deliberately carries no
// base URL, model name or secret.
type BackendStatus struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Checked     bool   `json:"checked"`
	Healthy     bool   `json:"healthy"`
	Inflight    int    `json:"inflight"`
	MaxInflight int    `json:"max_inflight"`
}

// AliasStatus is the public view of one alias.
type AliasStatus struct {
	Name       string `json:"name"`
	Ready      bool   `json:"ready"`
	RouteCount int    `json:"route_count"`
}

// Status is the body of /router/status.
type Status struct {
	Backends []BackendStatus `json:"backends"`
	Aliases  []AliasStatus   `json:"aliases"`
}

// Snapshot returns the current state in configuration order, so the output is
// deterministic.
func (s *State) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := Status{
		Backends: make([]BackendStatus, 0, len(s.order)),
		Aliases:  make([]AliasStatus, 0, len(s.aliasOrder)),
	}
	for _, bs := range s.order {
		status.Backends = append(status.Backends, BackendStatus{
			Name:        bs.backend.Name,
			Kind:        bs.backend.Kind,
			Checked:     bs.checked,
			Healthy:     bs.healthy,
			Inflight:    bs.inflight,
			MaxInflight: bs.backend.MaxInflight,
		})
	}
	for _, alias := range s.aliasOrder {
		status.Aliases = append(status.Aliases, AliasStatus{
			Name:       alias.name,
			Ready:      s.aliasReadyLocked(alias),
			RouteCount: len(alias.routes),
		})
	}
	return status
}

// AliasNames returns configured alias names in configuration order.
func (s *State) AliasNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.aliasOrder))
	for _, alias := range s.aliasOrder {
		names = append(names, alias.name)
	}
	return names
}
