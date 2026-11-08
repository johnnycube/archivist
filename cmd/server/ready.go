package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// probeTTL is how long a dependency's health verdict is reused by /readyz.
// Orchestrators poll readiness every few seconds from several places; the
// blob store must not see a HEAD request for each of them.
const probeTTL = 10 * time.Second

// cachedProbe rate-limits a health check: the underlying probe runs at most
// once per ttl, its verdict (nil or the error) is served from the cache in
// between, and concurrent callers share one run instead of stampeding.
type cachedProbe struct {
	name  string
	probe func(ctx context.Context) error
	ttl   time.Duration
	now   func() time.Time // for tests

	mu        sync.Mutex
	checkedAt time.Time
	err       error
}

func newCachedProbe(name string, ttl time.Duration, probe func(ctx context.Context) error) *cachedProbe {
	return &cachedProbe{name: name, probe: probe, ttl: ttl, now: time.Now}
}

// check returns the cached verdict, refreshing it when it is older than ttl.
// The lock is held across the probe on purpose: it is bounded by the probe's
// own timeout and it is what makes N simultaneous /readyz hits one request.
func (p *cachedProbe) check(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkedAt.IsZero() || p.now().Sub(p.checkedAt) >= p.ttl {
		p.err = p.probe(ctx)
		p.checkedAt = p.now()
	}
	if p.err != nil {
		return fmt.Errorf("%s: %w", p.name, p.err)
	}
	return nil
}

// readiness combines probes into one rest.Server.Ready function: every probe
// runs (so the log names every unhealthy dependency at once) and their errors
// are joined. Nil probes are skipped.
func readiness(probes ...func(ctx context.Context) error) func(ctx context.Context) error {
	var live []func(ctx context.Context) error
	for _, p := range probes {
		if p != nil {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return func(ctx context.Context) error {
		var errs []error
		for _, p := range live {
			if err := p(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}
