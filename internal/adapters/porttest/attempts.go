package porttest

import (
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// RunAttemptLimiter checks an app.AttemptLimiter implementation. factory
// returns a fresh, empty store. Keys are opaque to a store (the application
// hashes them); the suite uses arbitrary strings.
func RunAttemptLimiter(t *testing.T, factory func(t *testing.T) app.AttemptLimiter) {
	const window = 15 * time.Minute

	failures := func(t *testing.T, l app.AttemptLimiter, key string, now time.Time) (int, time.Time) {
		t.Helper()
		n, oldest, err := l.Failures(ctx, key, now, window)
		if err != nil {
			t.Fatalf("Failures(%q): %v", key, err)
		}
		return n, oldest
	}
	record := func(t *testing.T, l app.AttemptLimiter, key string, at time.Time) {
		t.Helper()
		if err := l.RecordFailure(ctx, key, at); err != nil {
			t.Fatalf("RecordFailure(%q): %v", key, err)
		}
	}

	t.Run("failures are counted per key", func(t *testing.T) {
		l := factory(t)
		if n, oldest := failures(t, l, "k1", t0); n != 0 || !oldest.IsZero() {
			t.Errorf("Failures of an unknown key = %d, %v; want 0 and the zero time", n, oldest)
		}
		record(t, l, "k1", t0)
		record(t, l, "k1", t0.Add(time.Minute))
		record(t, l, "k1", t0.Add(time.Minute)) // same instant twice: two failures
		record(t, l, "k2", t0.Add(2*time.Minute))
		now := t0.Add(3 * time.Minute)
		if n, oldest := failures(t, l, "k1", now); n != 3 || !oldest.Equal(t0) {
			t.Errorf("Failures(k1) = %d, oldest %v; want 3, %v", n, oldest, t0)
		}
		if n, oldest := failures(t, l, "k2", now); n != 1 || !oldest.Equal(t0.Add(2*time.Minute)) {
			t.Errorf("Failures(k2) = %d, oldest %v; want 1", n, oldest)
		}
		if n, _ := failures(t, l, "", now); n != 0 {
			t.Errorf("Failures of the empty key = %d, want 0", n)
		}
	})

	t.Run("the window slides, by the caller's clock", func(t *testing.T) {
		l := factory(t)
		record(t, l, "k", t0)
		record(t, l, "k", t0.Add(10*time.Minute))
		for _, tc := range []struct {
			at     time.Duration
			want   int
			oldest time.Duration
		}{
			{10 * time.Minute, 2, 0},
			{window - time.Second, 2, 0},
			{window, 1, 10 * time.Minute}, // the first failure is exactly one window old: out
			{10*time.Minute + window - time.Second, 1, 10 * time.Minute},
			{10*time.Minute + window, 0, -1},
		} {
			n, oldest := failures(t, l, "k", t0.Add(tc.at))
			if n != tc.want || (tc.want > 0 && !oldest.Equal(t0.Add(tc.oldest))) || (tc.want == 0 && !oldest.IsZero()) {
				t.Errorf("at +%v: Failures = %d, oldest %v; want %d, oldest +%v", tc.at, n, oldest, tc.want, tc.oldest)
			}
		}
		// A shorter window over the same data.
		if n, _, _ := l.Failures(ctx, "k", t0.Add(10*time.Minute), time.Minute); n != 1 {
			t.Errorf("Failures with a 1m window = %d, want 1", n)
		}
	})

	t.Run("Reset forgets exactly one key", func(t *testing.T) {
		l := factory(t)
		record(t, l, "k1", t0)
		record(t, l, "k1", t0)
		record(t, l, "k2", t0)
		if err := l.Reset(ctx, "k1"); err != nil {
			t.Fatal(err)
		}
		if n, oldest := failures(t, l, "k1", t0); n != 0 || !oldest.IsZero() {
			t.Errorf("Failures after Reset = %d, want 0", n)
		}
		if n, _ := failures(t, l, "k2", t0); n != 1 {
			t.Errorf("Reset(k1) changed k2: %d failures, want 1", n)
		}
		if err := l.Reset(ctx, "never-seen"); err != nil {
			t.Errorf("Reset of an unknown key = %v, want a no-op", err)
		}
		record(t, l, "k1", t0.Add(time.Second))
		if n, _ := failures(t, l, "k1", t0.Add(time.Second)); n != 1 {
			t.Errorf("Failures after Reset and one more failure = %d, want 1", n)
		}
	})

	// Pruning is allowed, not required, beyond AttemptRetention — but it must
	// never eat into it.
	t.Run("writes never prune inside the retention", func(t *testing.T) {
		l := factory(t)
		record(t, l, "old", t0)
		record(t, l, "other", t0.Add(app.AttemptRetention-time.Second))
		if n, _, _ := l.Failures(ctx, "old", t0.Add(app.AttemptRetention-time.Second), app.AttemptRetention); n != 1 {
			t.Errorf("a failure younger than AttemptRetention was pruned by a write to another key (%d left)", n)
		}
		record(t, l, "other", t0.Add(3*app.AttemptRetention))
		if n, _, _ := l.Failures(ctx, "other", t0.Add(3*app.AttemptRetention), window); n != 1 {
			t.Errorf("Failures after a prune = %d, want the 1 recent failure", n)
		}
	})
}
