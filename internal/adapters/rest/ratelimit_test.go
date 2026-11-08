package rest

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock drives a limiterStore deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestStore(maxKeys int) (*limiterStore, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ls := newLimiterStore(maxKeys)
	ls.now = clock.now
	return ls, clock
}

func TestLimiterBurstThenRetryAfter(t *testing.T) {
	ls, clock := newTestStore(10)
	for i := range 10 {
		if _, ok := ls.reserve("k", 10, time.Minute); !ok {
			t.Fatalf("hit %d refused, want 10 allowed", i+1)
		}
	}
	wait, ok := ls.reserve("k", 10, time.Minute)
	if ok || wait != 6*time.Second {
		t.Fatalf("11th hit: ok=%v wait=%v, want refused with 6s (one token per window/n)", ok, wait)
	}
	// A refused hit takes nothing: the wait shrinks with time instead of growing.
	clock.advance(2 * time.Second)
	if wait, ok := ls.reserve("k", 10, time.Minute); ok || wait != 4*time.Second {
		t.Errorf("after 2s: ok=%v wait=%v, want refused with 4s", ok, wait)
	}
	clock.advance(4 * time.Second)
	if _, ok := ls.reserve("k", 10, time.Minute); !ok {
		t.Error("hit after the advertised wait was refused")
	}
	if _, ok := ls.reserve("k", 10, time.Minute); ok {
		t.Error("only one token should have refilled")
	}
	if _, ok := ls.reserve("other", 10, time.Minute); !ok {
		t.Error("keys must not share a bucket")
	}
}

func TestLimiterIdleKeysExpire(t *testing.T) {
	ls, clock := newTestStore(1000)
	for i := range 100 {
		ls.reserve(fmt.Sprint("idle", i), 10, time.Minute)
	}
	for range 10 {
		ls.reserve("busy", 10, time.Hour)
	}
	// After the short window the idle buckets are full again and get swept by
	// the next call; the drained hour-bucket must survive with its state.
	clock.advance(2 * time.Minute)
	ls.reserve("new", 10, time.Minute)
	if n := ls.len(); n != 2 {
		t.Errorf("keys after sweep = %d, want 2 (busy + new)", n)
	}
	if _, ok := ls.reserve("busy", 10, time.Hour); ok {
		t.Error("the sweep reset a bucket that was not idle")
	}
}

func TestLimiterCapEvictsOldestNeverAll(t *testing.T) {
	const maxKeys = 100
	ls, clock := newTestStore(maxKeys)
	// The victim of a brute-force run: drained, and recently used.
	drain := func() {
		for range 10 {
			ls.reserve("victim", 10, time.Hour)
		}
	}
	drain()
	// A flood of fresh keys, none of them idle long enough to expire.
	for i := range 10 * maxKeys {
		clock.advance(time.Millisecond)
		ls.reserve(fmt.Sprint("flood", i), 10, time.Hour)
		if i%50 == 0 {
			ls.reserve("victim", 10, time.Hour) // still being attacked → stays recent
		}
		if n := ls.len(); n > maxKeys {
			t.Fatalf("store grew to %d keys, cap is %d", n, maxKeys)
		}
	}
	if n := ls.len(); n != maxKeys {
		t.Errorf("keys = %d, want the store to stay full (%d), not be wiped", n, maxKeys)
	}
	if _, ok := ls.reserve("victim", 10, time.Hour); ok {
		t.Error("a key flood reset the limiter of a key that is in use")
	}
	// The most recent flood keys are still tracked, the oldest are gone.
	ls.mu.Lock()
	_, newest := ls.entries[fmt.Sprint("flood", 10*maxKeys-1)]
	_, oldest := ls.entries["flood0"]
	ls.mu.Unlock()
	if !newest || oldest {
		t.Errorf("eviction order: newest kept=%v, oldest kept=%v; want true/false", newest, oldest)
	}
}

func TestLimiterConcurrentUse(t *testing.T) {
	ls := newLimiterStore(50)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				ls.reserve(fmt.Sprint("spray", g, i), 5, time.Hour)
				if _, ok := ls.reserve("shared", 20, time.Hour); ok {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if allowed != 20 {
		t.Errorf("allowed = %d, want exactly the burst of 20", allowed)
	}
	if n := ls.len(); n > 50 {
		t.Errorf("keys = %d, cap is 50", n)
	}
}
