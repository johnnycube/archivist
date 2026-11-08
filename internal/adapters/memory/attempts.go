package memory

import (
	"context"
	"sync"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// Attempts is an in-memory app.AttemptLimiter: per key, the times of its
// failures, oldest first.
type Attempts struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func NewAttempts() *Attempts { return &Attempts{m: map[string][]time.Time{}} }

var _ app.AttemptLimiter = (*Attempts)(nil)

func (a *Attempts) Failures(_ context.Context, key string, now time.Time, window time.Duration) (int, time.Time, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var (
		n      int
		oldest time.Time
	)
	for _, at := range a.m[key] {
		if at.After(now.Add(-window)) && !at.After(now) {
			if n == 0 || at.Before(oldest) {
				oldest = at
			}
			n++
		}
	}
	return n, oldest, nil
}

func (a *Attempts) RecordFailure(_ context.Context, key string, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := now.Add(-app.AttemptRetention)
	for k, times := range a.m { // prune on write
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(a.m, k)
		} else {
			a.m[k] = kept
		}
	}
	a.m[key] = append(a.m[key], now)
	return nil
}

func (a *Attempts) Reset(_ context.Context, key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, key)
	return nil
}
