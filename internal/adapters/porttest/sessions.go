package porttest

import (
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// RunSessions checks an app.Sessions implementation. factory returns a fresh,
// empty store whose sessions live for ttl and end after idle without use
// (0 = no idle timeout). The port deals in token HASHES only
// (the application layer generates and hashes tokens), so there is no clear
// token a store could leak; the suite uses arbitrary strings as hashes.
func RunSessions(t *testing.T, newStore func(t *testing.T, ttl, idle time.Duration) (app.Sessions, Env)) {
	const ttl = time.Hour
	// The pre-idle-timeout contract holds unchanged for a store without one.
	factory := func(t *testing.T, ttl time.Duration) (app.Sessions, Env) { return newStore(t, ttl, 0) }

	lookup := func(t *testing.T, s app.Sessions, hash string, now time.Time) (string, bool) {
		t.Helper()
		uid, ok, err := s.Lookup(ctx, hash, now)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", hash, err)
		}
		return uid, ok
	}

	t.Run("create and look up", func(t *testing.T) {
		s, env := factory(t, ttl)
		alice, bob := env.NewUser(t), env.NewUser(t)
		if err := s.Create(ctx, "hash-alice", alice, t0); err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, "hash-bob", bob, t0); err != nil {
			t.Fatal(err)
		}
		if uid, ok := lookup(t, s, "hash-alice", t0); !ok || uid != alice {
			t.Errorf("Lookup(alice's) = %q,%v; want %q,true", uid, ok, alice)
		}
		if uid, ok := lookup(t, s, "hash-bob", t0); !ok || uid != bob {
			t.Errorf("Lookup(bob's) = %q,%v; want %q,true", uid, ok, bob)
		}
		if _, ok := lookup(t, s, "hash-nobody", t0); ok {
			t.Error("an unknown hash resolved to a session")
		}
		if _, ok := lookup(t, s, "", t0); ok {
			t.Error("the empty hash resolved to a session")
		}
	})

	t.Run("sessions expire after the TTL, by the caller's clock", func(t *testing.T) {
		s, env := factory(t, ttl)
		u := env.NewUser(t)
		if err := s.Create(ctx, "h", u, t0); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			at   time.Duration
			want bool
		}{
			{0, true}, {ttl - time.Second, true}, {ttl, false}, {ttl + time.Second, false}, {100 * ttl, false},
		} {
			if _, ok := lookup(t, s, "h", t0.Add(tc.at)); ok != tc.want {
				t.Errorf("Lookup at +%v = %v, want %v", tc.at, ok, tc.want)
			}
		}
	})

	t.Run("creating a session never disturbs live ones", func(t *testing.T) {
		s, env := factory(t, ttl)
		u := env.NewUser(t)
		_ = s.Create(ctx, "old", u, t0)
		_ = s.Create(ctx, "mid", u, t0.Add(ttl/2))
		later := t0.Add(ttl + time.Minute) // "old" has expired, "mid" has not
		if err := s.Create(ctx, "new", u, later); err != nil {
			t.Fatal(err)
		}
		for hash, want := range map[string]bool{"old": false, "mid": true, "new": true} {
			if _, ok := lookup(t, s, hash, later); ok != want {
				t.Errorf("Lookup(%q) after a later Create = %v, want %v", hash, ok, want)
			}
		}
	})

	t.Run("Revoke ends exactly one session", func(t *testing.T) {
		s, env := factory(t, ttl)
		u := env.NewUser(t)
		_ = s.Create(ctx, "h1", u, t0)
		_ = s.Create(ctx, "h2", u, t0)
		if err := s.Revoke(ctx, "h1"); err != nil {
			t.Fatal(err)
		}
		if _, ok := lookup(t, s, "h1", t0); ok {
			t.Error("revoked session still resolves")
		}
		if _, ok := lookup(t, s, "h2", t0); !ok {
			t.Error("Revoke ended another session of the user")
		}
		if err := s.Revoke(ctx, "h1"); err != nil {
			t.Errorf("revoking twice = %v, want a no-op", err)
		}
		if err := s.Revoke(ctx, "never-existed"); err != nil {
			t.Errorf("revoking an unknown hash = %v, want a no-op", err)
		}
	})

	t.Run("RevokeUser ends all of that user's sessions and nobody else's", func(t *testing.T) {
		s, env := factory(t, ttl)
		alice, bob := env.NewUser(t), env.NewUser(t)
		_ = s.Create(ctx, "a1", alice, t0)
		_ = s.Create(ctx, "a2", alice, t0)
		_ = s.Create(ctx, "b1", bob, t0)
		if err := s.RevokeUser(ctx, alice); err != nil {
			t.Fatal(err)
		}
		for _, h := range []string{"a1", "a2"} {
			if _, ok := lookup(t, s, h, t0); ok {
				t.Errorf("RevokeUser left session %q alive", h)
			}
		}
		if uid, ok := lookup(t, s, "b1", t0); !ok || uid != bob {
			t.Error("RevokeUser(alice) ended bob's session")
		}
		if err := s.RevokeUser(ctx, alice); err != nil {
			t.Errorf("RevokeUser without sessions = %v, want a no-op", err)
		}
	})

	t.Run("idle sessions end before their TTL; used ones do not", func(t *testing.T) {
		const ttl, idle = 30 * 24 * time.Hour, 24 * time.Hour
		// (A factory call empties the store, so each store is used up before
		// the next one is made.)
		for _, tc := range []struct {
			at   time.Duration
			want bool
		}{{idle - time.Second, true}, {idle, false}, {idle + time.Hour, false}} {
			s2, env2 := newStore(t, ttl, idle)
			_ = s2.Create(ctx, "h", env2.NewUser(t), t0)
			if _, ok := lookup(t, s2, "h", t0.Add(tc.at)); ok != tc.want {
				t.Errorf("first Lookup at +%v = %v, want %v", tc.at, ok, tc.want)
			}
		}
		s, env := newStore(t, ttl, idle)
		u := env.NewUser(t)
		_ = s.Create(ctx, "used", u, t0)
		_ = s.Create(ctx, "unused", u, t0)
		// "used" is looked up every 20h for 5 days: never idle for 24h.
		for at := 20 * time.Hour; at <= 120*time.Hour; at += 20 * time.Hour {
			if _, ok := lookup(t, s, "used", t0.Add(at)); !ok {
				t.Fatalf("a session used every 20h ended at +%v (idle timeout %v)", at, idle)
			}
		}
		if _, ok := lookup(t, s, "unused", t0.Add(120*time.Hour)); ok {
			t.Error("a session unused for 5 days survived a 24h idle timeout")
		}
		// An idle-expired session stays dead: looking at it must not revive it.
		if _, ok := lookup(t, s, "unused", t0.Add(120*time.Hour+time.Second)); ok {
			t.Error("an idle-expired session came back")
		}
		// Use does not extend the absolute TTL.
		if _, ok := lookup(t, s, "used", t0.Add(120*time.Hour+idle-time.Second)); !ok {
			t.Error("session ended less than the idle timeout after its last use")
		}
		s3, env3 := newStore(t, 2*time.Hour, time.Hour)
		_ = s3.Create(ctx, "h", env3.NewUser(t), t0)
		for at := 30 * time.Minute; at < 2*time.Hour; at += 30 * time.Minute {
			if _, ok := lookup(t, s3, "h", t0.Add(at)); !ok {
				t.Fatalf("in-use session ended at +%v", at)
			}
		}
		if _, ok := lookup(t, s3, "h", t0.Add(2*time.Hour)); ok {
			t.Error("a session in constant use outlived its absolute TTL")
		}
	})

	// Use is recorded at most once per SessionTouchInterval. Observable: a
	// Lookup sooner than that after the recorded use does not count as use.
	t.Run("use is recorded once per touch interval, not per Lookup", func(t *testing.T) {
		const idle = time.Hour
		touch := app.SessionTouchInterval
		s, env := newStore(t, 24*time.Hour, idle)
		u := env.NewUser(t)
		_ = s.Create(ctx, "h", u, t0)
		if _, ok := lookup(t, s, "h", t0.Add(touch-time.Second)); !ok { // not recorded: too soon
			t.Fatal("fresh session not found")
		}
		if _, ok := lookup(t, s, "h", t0.Add(idle)); ok {
			t.Errorf("a Lookup %v after creation was recorded as use (the session outlived created+idle)", touch-time.Second)
		}
		_ = s.Create(ctx, "h2", u, t0)
		if _, ok := lookup(t, s, "h2", t0.Add(touch)); !ok { // recorded
			t.Fatal("fresh session not found")
		}
		if _, ok := lookup(t, s, "h2", t0.Add(idle)); !ok {
			t.Errorf("a Lookup %v after creation was not recorded as use", touch)
		}
		_ = s.Create(ctx, "h3", u, t0)
		lookup(t, s, "h3", t0.Add(touch)) // recorded
		if _, ok := lookup(t, s, "h3", t0.Add(touch+idle)); ok {
			t.Error("session alive a full idle timeout after its last recorded use")
		}
	})

	t.Run("CreateReplacing revokes the user's previous session, and only that", func(t *testing.T) {
		s, env := factory(t, ttl)
		alice, bob := env.NewUser(t), env.NewUser(t)
		_ = s.Create(ctx, "alice-old", alice, t0)
		_ = s.Create(ctx, "alice-other-device", alice, t0)
		_ = s.Create(ctx, "bob-1", bob, t0)
		if err := s.CreateReplacing(ctx, "alice-new", alice, "alice-old", t0); err != nil {
			t.Fatal(err)
		}
		for hash, want := range map[string]bool{"alice-new": true, "alice-old": false, "alice-other-device": true, "bob-1": true} {
			if _, ok := lookup(t, s, hash, t0); ok != want {
				t.Errorf("after replacing alice-old: Lookup(%q) = %v, want %v", hash, ok, want)
			}
		}
		// Somebody else's session is not alice's to revoke; nor is an unknown
		// or empty hash an error.
		for _, previous := range []string{"bob-1", "never-existed", ""} {
			hash := "alice-after-" + previous
			if err := s.CreateReplacing(ctx, hash, alice, previous, t0); err != nil {
				t.Fatalf("CreateReplacing(previous %q): %v", previous, err)
			}
			if uid, ok := lookup(t, s, hash, t0); !ok || uid != alice {
				t.Errorf("CreateReplacing(previous %q) created no session", previous)
			}
		}
		if uid, ok := lookup(t, s, "bob-1", t0); !ok || uid != bob {
			t.Error("alice's login revoked bob's session")
		}
		// The new session expires like any other.
		if _, ok := lookup(t, s, "alice-new", t0.Add(ttl)); ok {
			t.Error("a session made by CreateReplacing outlived the TTL")
		}
	})
}
