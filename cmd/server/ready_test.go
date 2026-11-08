package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedProbeRateLimits(t *testing.T) {
	var calls atomic.Int32
	verdict := errors.New("down")
	now := time.Unix(1000, 0)
	p := newCachedProbe("blobs", 10*time.Second, func(context.Context) error {
		calls.Add(1)
		return verdict
	})
	p.now = func() time.Time { return now }
	ctx := context.Background()

	// A burst of readiness requests costs one probe and all see its verdict.
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.check(ctx); err == nil || !strings.HasPrefix(err.Error(), "blobs: down") {
				t.Errorf("check = %v, want the named verdict", err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("probe ran %d times for 20 concurrent checks, want 1", n)
	}

	// Within the TTL the cache answers, even after the dependency recovered.
	verdict = nil
	now = now.Add(9 * time.Second)
	if err := p.check(ctx); err == nil || calls.Load() != 1 {
		t.Errorf("inside TTL: err = %v, probes = %d; want the cached failure and no new probe", err, calls.Load())
	}
	// Once the TTL is over the probe runs again and the recovery shows.
	now = now.Add(2 * time.Second)
	if err := p.check(ctx); err != nil || calls.Load() != 2 {
		t.Errorf("after TTL: err = %v, probes = %d; want healthy after one new probe", err, calls.Load())
	}
}

func TestReadinessJoinsProbes(t *testing.T) {
	if readiness(nil, nil) != nil {
		t.Error("no probes must mean no readiness function (always ready)")
	}
	ok := func(context.Context) error { return nil }
	dbDown := func(context.Context) error { return errors.New("db: ping") }
	blobsDown := func(context.Context) error { return errors.New("blobs: 503") }
	if err := readiness(ok, nil, ok)(context.Background()); err != nil {
		t.Errorf("all healthy: %v", err)
	}
	err := readiness(dbDown, ok, blobsDown)(context.Background())
	if err == nil || !strings.Contains(err.Error(), "db: ping") || !strings.Contains(err.Error(), "blobs: 503") {
		t.Errorf("err = %v, want both failures reported", err)
	}
}
