package backend

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// HealthInterval is the pause between checks of one backend.
const HealthInterval = 5 * time.Second

// HealthSink receives health results. Implementations must be safe for
// concurrent use.
type HealthSink interface {
	SetHealth(backend string, healthy bool)
}

// Checker probes every backend on its own schedule. Health checks never
// consume inference capacity.
type Checker struct {
	backends []*Backend
	sink     HealthSink
	client   *http.Client
	logger   *slog.Logger
	interval time.Duration
}

// NewChecker creates a health checker with the standard interval.
func NewChecker(backends []*Backend, sink HealthSink, logger *slog.Logger) *Checker {
	return NewCheckerWithInterval(backends, sink, logger, HealthInterval)
}

// NewCheckerWithInterval creates a health checker with a custom interval. It
// exists so tests do not have to wait for the production interval.
func NewCheckerWithInterval(backends []*Backend, sink HealthSink, logger *slog.Logger, interval time.Duration) *Checker {
	return &Checker{
		backends: backends,
		sink:     sink,
		client:   NewHealthClient(),
		logger:   logger,
		interval: interval,
	}
}

// Run checks every backend immediately and then every interval until ctx is
// canceled. It returns when all goroutines have stopped, so there is no leak.
func (c *Checker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range c.backends {
		wg.Add(1)
		go func(b *Backend) {
			defer wg.Done()
			c.loop(ctx, b)
		}(b)
	}
	wg.Wait()
}

func (c *Checker) loop(ctx context.Context, b *Backend) {
	previous, known := false, false

	check := func() {
		healthy := c.probe(ctx, b)
		if !known || healthy != previous {
			c.logger.Info("backend health changed",
				"backend", b.Name, "kind", b.Kind, "healthy", healthy)
		}
		previous, known = healthy, true
		c.sink.SetHealth(b.Name, healthy)
	}

	check()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

// probe reports whether the backend answered its health endpoint with 200.
// The body is not inspected.
func (c *Checker) probe(ctx context.Context, b *Backend) bool {
	requestCtx, cancel := context.WithTimeout(ctx, HealthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, b.HealthURL().String(), nil)
	if err != nil {
		return false
	}
	b.Authorize(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
