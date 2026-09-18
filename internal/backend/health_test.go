package backend_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Yut0Miura/local-inference-router/internal/backend"
	"github.com/Yut0Miura/local-inference-router/internal/config"
)

// sink records health results.
type sink struct {
	mu      sync.Mutex
	last    map[string]bool
	updates chan struct{}
}

func newSink() *sink {
	return &sink{last: map[string]bool{}, updates: make(chan struct{}, 64)}
}

func (s *sink) SetHealth(name string, healthy bool) {
	s.mu.Lock()
	s.last[name] = healthy
	s.mu.Unlock()
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

func (s *sink) get(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[name]
}

func (s *sink) waitForUpdate(t *testing.T) {
	t.Helper()
	select {
	case <-s.updates:
	case <-time.After(5 * time.Second):
		t.Fatal("no health update within 5s")
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func buildBackend(t *testing.T, kind, baseURL string) *backend.Backend {
	t.Helper()
	backends, err := backend.Build([]config.BackendConfig{{
		Name:                    "b",
		Kind:                    kind,
		BaseURL:                 baseURL,
		MaxInflight:             1,
		ResponseHeaderTimeoutMS: 1000,
	}}, backend.EnvSecrets)
	if err != nil {
		t.Fatalf("build backend: %v", err)
	}
	return backends[0]
}

func TestHealthPathPerKind(t *testing.T) {
	for _, tc := range []struct {
		kind string
		path string
	}{
		{config.KindOllama, "/api/version"},
		{config.KindLlamaCPP, "/health"},
		{config.KindVLLM, "/health"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var requested string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			recorder := newSink()
			checker := backend.NewChecker([]*backend.Backend{buildBackend(t, tc.kind, server.URL)}, recorder, quietLogger())

			ctx, cancel := context.WithCancel(context.Background())
			go checker.Run(ctx)
			recorder.waitForUpdate(t)
			cancel()

			if requested != tc.path {
				t.Fatalf("checked %q, want %q", requested, tc.path)
			}
			if !recorder.get("b") {
				t.Fatal("backend should be healthy after HTTP 200")
			}
		})
	}
}

func TestHealthNon200IsUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	recorder := newSink()
	checker := backend.NewChecker([]*backend.Backend{buildBackend(t, config.KindVLLM, server.URL)}, recorder, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go checker.Run(ctx)
	recorder.waitForUpdate(t)
	cancel()

	if recorder.get("b") {
		t.Fatal("HTTP 503 must be reported as unhealthy")
	}
}

func TestHealthTransportErrorIsUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening any more

	recorder := newSink()
	checker := backend.NewChecker([]*backend.Backend{buildBackend(t, config.KindOllama, url)}, recorder, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go checker.Run(ctx)
	recorder.waitForUpdate(t)
	cancel()

	if recorder.get("b") {
		t.Fatal("a transport error must be reported as unhealthy")
	}
}

func TestHealthTimeoutIsUnhealthy(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); server.Close() }()

	recorder := newSink()
	checker := backend.NewChecker([]*backend.Backend{buildBackend(t, config.KindVLLM, server.URL)}, recorder, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go checker.Run(ctx)
	recorder.waitForUpdate(t)
	cancel()

	if recorder.get("b") {
		t.Fatal("a health check that never answers must be unhealthy")
	}
}

func TestHealthRecovers(t *testing.T) {
	var mu sync.Mutex
	healthy := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if healthy {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	recorder := newSink()
	b := buildBackend(t, config.KindLlamaCPP, server.URL)
	checker := backend.NewCheckerWithInterval([]*backend.Backend{b}, recorder, quietLogger(), 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go checker.Run(ctx)

	recorder.waitForUpdate(t)
	if recorder.get("b") {
		t.Fatal("backend should start unhealthy")
	}

	mu.Lock()
	healthy = true
	mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for !recorder.get("b") {
		if time.Now().After(deadline) {
			t.Fatal("backend did not recover")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCheckerStopsWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := backend.NewCheckerWithInterval(
		[]*backend.Backend{buildBackend(t, config.KindOllama, server.URL)},
		newSink(), quietLogger(), 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		checker.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("health goroutines did not stop when the context was canceled")
	}
}
