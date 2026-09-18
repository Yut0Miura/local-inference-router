package router_test

import (
	"sync"
	"testing"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
	"github.com/Yut0Miura/local-inference-router/internal/metrics"
	"github.com/Yut0Miura/local-inference-router/internal/router"
)

// newState builds routing state from a literal configuration.
func newState(t *testing.T, cfg *config.Config) *router.State {
	t.Helper()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("invalid test configuration: %v", err)
	}
	backends, err := backend.Build(cfg.Backends, backend.EnvSecrets)
	if err != nil {
		t.Fatalf("build backends: %v", err)
	}
	state, err := router.NewState(cfg, backends, metrics.New())
	if err != nil {
		t.Fatalf("new state: %v", err)
	}
	return state
}

func backendCfg(name string, maxInflight int) config.BackendConfig {
	return config.BackendConfig{
		Name:                    name,
		Kind:                    config.KindOllama,
		BaseURL:                 "http://127.0.0.1:11434",
		MaxInflight:             maxInflight,
		ResponseHeaderTimeoutMS: 1000,
	}
}

func threeRouteConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Listen:  "127.0.0.1:8080",
		Backends: []config.BackendConfig{
			backendCfg("a", 10), backendCfg("b", 10), backendCfg("c", 10),
		},
		Aliases: []config.AliasConfig{{
			Name: "chat",
			Routes: []config.RouteConfig{
				{Backend: "a", UpstreamModel: "m", Priority: 10},
				{Backend: "b", UpstreamModel: "m", Priority: 10},
				{Backend: "c", UpstreamModel: "m", Priority: 20},
			},
		}},
	}
}

func healthy(state *router.State, names ...string) {
	for _, name := range names {
		state.SetHealth(name, true)
	}
}

// reserve is a helper that fails the test when no route is available.
func reserve(t *testing.T, state *router.State, attempted map[*router.Route]bool) *router.Route {
	t.Helper()
	route, err := state.Reserve("chat", attempted)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return route
}

func TestPriorityWins(t *testing.T) {
	state := newState(t, threeRouteConfig())
	healthy(state, "a", "b", "c")

	// Load both priority-10 backends, so the priority-20 backend has strictly
	// fewer inflight requests. Priority must still win.
	reserve(t, state, map[*router.Route]bool{})
	reserve(t, state, map[*router.Route]bool{})

	route := reserve(t, state, map[*router.Route]bool{})
	if route.Backend.Name == "c" {
		t.Fatalf("selected the priority-20 backend while priority-10 backends had capacity")
	}
	if route.Priority != 10 {
		t.Fatalf("selected priority %d, want 10", route.Priority)
	}
}

func TestLeastInflightWithinSamePriority(t *testing.T) {
	state := newState(t, threeRouteConfig())
	healthy(state, "a", "b", "c")

	first := reserve(t, state, map[*router.Route]bool{})
	if first.Backend.Name != "a" {
		t.Fatalf("first selection = %q, want the first configured route", first.Backend.Name)
	}
	second := reserve(t, state, map[*router.Route]bool{})
	if second.Backend.Name != "b" {
		t.Fatalf("second selection = %q, want the least loaded same-priority backend", second.Backend.Name)
	}
}

func TestConfigurationOrderBreaksExactTie(t *testing.T) {
	state := newState(t, threeRouteConfig())
	healthy(state, "a", "b", "c")

	route := reserve(t, state, map[*router.Route]bool{})
	if route.Backend.Name != "a" {
		t.Fatalf("selection = %q, want the earlier route on an exact tie", route.Backend.Name)
	}
}

func TestUnhealthyAndUncheckedRoutesAreExcluded(t *testing.T) {
	state := newState(t, threeRouteConfig())

	if _, err := state.Reserve("chat", map[*router.Route]bool{}); err == nil {
		t.Fatal("an unchecked backend must not be selected")
	}

	state.SetHealth("a", false)
	state.SetHealth("b", false)
	state.SetHealth("c", true)
	route := reserve(t, state, map[*router.Route]bool{})
	if route.Backend.Name != "c" {
		t.Fatalf("selection = %q, want the only healthy backend", route.Backend.Name)
	}
}

func TestCapacityExcludesFullBackend(t *testing.T) {
	cfg := &config.Config{
		Version:  1,
		Listen:   "127.0.0.1:8080",
		Backends: []config.BackendConfig{backendCfg("a", 1), backendCfg("b", 1)},
		Aliases: []config.AliasConfig{{
			Name: "chat",
			Routes: []config.RouteConfig{
				{Backend: "a", UpstreamModel: "m", Priority: 10},
				{Backend: "b", UpstreamModel: "m", Priority: 20},
			},
		}},
	}
	state := newState(t, cfg)
	healthy(state, "a", "b")

	first := reserve(t, state, map[*router.Route]bool{})
	second := reserve(t, state, map[*router.Route]bool{})
	if first.Backend.Name != "a" || second.Backend.Name != "b" {
		t.Fatalf("selections = %q, %q", first.Backend.Name, second.Backend.Name)
	}

	if _, err := state.Reserve("chat", map[*router.Route]bool{}); err == nil {
		t.Fatal("a third request must fail: every backend is at capacity")
	}

	state.Release(first)
	third := reserve(t, state, map[*router.Route]bool{})
	if third.Backend.Name != "a" {
		t.Fatalf("after release selection = %q, want a", third.Backend.Name)
	}
}

func TestCapacityIsSharedBetweenAliases(t *testing.T) {
	cfg := &config.Config{
		Version:  1,
		Listen:   "127.0.0.1:8080",
		Backends: []config.BackendConfig{backendCfg("shared", 1)},
		Aliases: []config.AliasConfig{
			{Name: "chat", Routes: []config.RouteConfig{{Backend: "shared", UpstreamModel: "m1", Priority: 1}}},
			{Name: "other", Routes: []config.RouteConfig{{Backend: "shared", UpstreamModel: "m2", Priority: 1}}},
		},
	}
	state := newState(t, cfg)
	healthy(state, "shared")

	if _, err := state.Reserve("chat", map[*router.Route]bool{}); err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if _, err := state.Reserve("other", map[*router.Route]bool{}); err == nil {
		t.Fatal("max_inflight belongs to the backend and must be shared by all aliases")
	}
}

func TestAttemptedRouteIsExcluded(t *testing.T) {
	state := newState(t, threeRouteConfig())
	healthy(state, "a", "b", "c")

	attempted := map[*router.Route]bool{}
	first := reserve(t, state, attempted)
	attempted[first] = true
	second := reserve(t, state, attempted)
	attempted[second] = true
	third := reserve(t, state, attempted)
	attempted[third] = true

	if _, err := state.Reserve("chat", attempted); err == nil {
		t.Fatal("every route was attempted; reservation must fail")
	}
}

func TestUnknownAlias(t *testing.T) {
	state := newState(t, threeRouteConfig())
	healthy(state, "a", "b", "c")
	if _, err := state.Reserve("missing", map[*router.Route]bool{}); err == nil {
		t.Fatal("an unknown alias must be rejected")
	}
}

func TestReleaseNeverGoesNegative(t *testing.T) {
	state := newState(t, threeRouteConfig())
	healthy(state, "a", "b", "c")

	route := reserve(t, state, map[*router.Route]bool{})
	state.Release(route)
	state.Release(route)
	state.Release(route)

	for _, status := range state.Snapshot().Backends {
		if status.Inflight < 0 {
			t.Fatalf("backend %q inflight = %d", status.Name, status.Inflight)
		}
	}
}

// TestConcurrentReservationsRespectCapacity runs many goroutines against a
// finite capacity and proves the selection and reservation are atomic.
func TestConcurrentReservationsRespectCapacity(t *testing.T) {
	const (
		goroutines  = 200
		capacityA   = 3
		capacityB   = 2
		totalCapaci = capacityA + capacityB
	)

	cfg := &config.Config{
		Version:  1,
		Listen:   "127.0.0.1:8080",
		Backends: []config.BackendConfig{backendCfg("a", capacityA), backendCfg("b", capacityB)},
		Aliases: []config.AliasConfig{{
			Name: "chat",
			Routes: []config.RouteConfig{
				{Backend: "a", UpstreamModel: "m", Priority: 10},
				{Backend: "b", UpstreamModel: "m", Priority: 10},
			},
		}},
	}
	state := newState(t, cfg)
	healthy(state, "a", "b")

	var (
		mu       sync.Mutex
		held     int
		maxHeld  int
		granted  int
		rejected int
	)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			route, err := state.Reserve("chat", map[*router.Route]bool{})
			if err != nil {
				mu.Lock()
				rejected++
				mu.Unlock()
				return
			}

			mu.Lock()
			held++
			granted++
			if held > maxHeld {
				maxHeld = held
			}
			mu.Unlock()

			mu.Lock()
			held--
			mu.Unlock()
			state.Release(route)
		}()
	}
	close(start)
	wg.Wait()

	if maxHeld > totalCapaci {
		t.Fatalf("%d reservations were held at once, capacity is %d", maxHeld, totalCapaci)
	}
	if granted+rejected != goroutines {
		t.Fatalf("accounted %d of %d goroutines", granted+rejected, goroutines)
	}
	for _, status := range state.Snapshot().Backends {
		if status.Inflight != 0 {
			t.Fatalf("backend %q ended with inflight %d, want 0", status.Name, status.Inflight)
		}
		if status.Inflight > status.MaxInflight {
			t.Fatalf("backend %q exceeded its capacity", status.Name)
		}
	}
	if granted == 0 {
		t.Fatal("no goroutine ever got a reservation")
	}
}

func TestReadinessAndStatus(t *testing.T) {
	cfg := &config.Config{
		Version:  1,
		Listen:   "127.0.0.1:8080",
		Backends: []config.BackendConfig{backendCfg("a", 1), backendCfg("b", 1)},
		Aliases: []config.AliasConfig{
			{Name: "chat", Routes: []config.RouteConfig{{Backend: "a", UpstreamModel: "m", Priority: 1}}},
			{Name: "other", Routes: []config.RouteConfig{{Backend: "b", UpstreamModel: "m", Priority: 1}}},
		},
	}
	state := newState(t, cfg)

	if state.Ready() {
		t.Fatal("an unchecked router must not be ready")
	}
	state.SetHealth("a", true)
	if state.Ready() {
		t.Fatal("one alias still has no healthy route")
	}
	state.SetHealth("b", true)
	if !state.Ready() {
		t.Fatal("every alias has a healthy route")
	}

	// A saturated backend stays ready.
	route := reserve(t, state, map[*router.Route]bool{})
	if !state.Ready() {
		t.Fatal("a backend at capacity must still count as ready")
	}
	state.Release(route)

	status := state.Snapshot()
	if len(status.Backends) != 2 || len(status.Aliases) != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.Backends[0].Name != "a" || status.Aliases[0].Name != "chat" {
		t.Fatal("status must follow configuration order")
	}
	if !status.Aliases[0].Ready || status.Aliases[0].RouteCount != 1 {
		t.Fatalf("unexpected alias status: %+v", status.Aliases[0])
	}
}
