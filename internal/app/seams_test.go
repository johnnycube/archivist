package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/crypto"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// fakeClock is a settable app.Deps.Clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---- rehash on login ----

// Argon2id of "password" (salt "somesaltsomesalt") with 4 MiB instead of the
// current 19 MiB: a hash made with outdated parameters, which a login replaces.
const legacyHash = "$argon2id$v=19$m=4096,t=2,p=1$c29tZXNhbHRzb21lc2FsdA$H5mZn8QsFFwntPktdbjGNtqB+DDJ+nixq94uJAZoqIk"

func TestLoginUpgradesLegacyPasswordHash(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, nil)
	deps.Hasher = crypto.Hasher{}
	a := app.New(deps)
	legacy, err := deps.Users.Create(ctx, "old@x.com", legacyHash)
	if err != nil {
		t.Fatal(err)
	}

	// A failed login must not touch the hash.
	if _, _, err := a.Auth.LogInGuarded(ctx, "old@x.com", "not-the-password", "", ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v", err)
	}
	if u, _, _ := deps.Users.ByID(ctx, legacy.ID); u.PasswordHash != legacyHash {
		t.Fatalf("a failed login changed the stored hash to %q", u.PasswordHash)
	}

	u, tok, err := a.Auth.LogInGuarded(ctx, "old@x.com", "password", "", "")
	if err != nil || tok == "" {
		t.Fatalf("login with a legacy hash = %v", err)
	}
	stored, _, _ := deps.Users.ByID(ctx, legacy.ID)
	if !strings.HasPrefix(stored.PasswordHash, "$argon2id$v=19$m=19456,") {
		t.Fatalf("stored hash after login = %q, want one with the current parameters", stored.PasswordHash)
	}
	if u.PasswordHash != stored.PasswordHash {
		t.Errorf("LogIn returned the stale hash")
	}
	if deps.Hasher.NeedsRehash(stored.PasswordHash) {
		t.Error("the upgraded hash still needs a rehash")
	}
	// The upgraded hash is for the same password, and is not rewritten again.
	if _, _, err := a.Auth.LogInGuarded(ctx, "old@x.com", "password", "", ""); err != nil {
		t.Fatalf("login after the upgrade: %v", err)
	}
	if again, _, _ := deps.Users.ByID(ctx, legacy.ID); again.PasswordHash != stored.PasswordHash {
		t.Error("a current hash was rewritten on login")
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "old@x.com", "not-the-password", "", ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("wrong password after the upgrade err = %v", err)
	}
}

// readOnlyUsers fails every password update.
type readOnlyUsers struct{ app.Users }

func (readOnlyUsers) UpdatePassword(context.Context, string, string) error {
	return errors.New("database is read-only")
}

func TestLoginSurvivesFailedHashUpgrade(t *testing.T) {
	ctx := context.Background()
	var logs syncBuffer
	deps := importDeps(t, nil)
	deps.Hasher = crypto.Hasher{}
	deps.Users = readOnlyUsers{deps.Users}
	deps.Log = slog.New(slog.NewTextHandler(&logs, nil))
	a := app.New(deps)
	legacy, _ := deps.Users.Create(ctx, "old@x.com", legacyHash)

	u, tok, err := a.Auth.LogInGuarded(ctx, "old@x.com", "password", "", "")
	if err != nil {
		t.Fatalf("login must not fail because the hash upgrade could not be stored: %v", err)
	}
	if uid, ok, _ := a.Auth.Authenticate(ctx, tok); !ok || uid != legacy.ID {
		t.Error("the session of that login does not authenticate")
	}
	if u.PasswordHash != legacyHash {
		t.Errorf("returned hash = %q, want the one still stored", u.PasswordHash)
	}
	if out := logs.String(); !strings.Contains(out, "password hash upgrade failed") || !strings.Contains(out, "read-only") {
		t.Errorf("the failure was not logged: %q", out)
	}
}

// ---- session tokens ----

// recordingSessions notes what the store is given.
type recordingSessions struct {
	app.Sessions
	mu       sync.Mutex
	hashes   []string
	previous []string // the hash each creation was asked to replace
}

func (r *recordingSessions) Create(ctx context.Context, tokenHash, userID string, now time.Time) error {
	return r.CreateReplacing(ctx, tokenHash, userID, "", now)
}

func (r *recordingSessions) CreateReplacing(ctx context.Context, tokenHash, userID, previousHash string, now time.Time) error {
	r.mu.Lock()
	r.hashes = append(r.hashes, tokenHash)
	r.previous = append(r.previous, previousHash)
	r.mu.Unlock()
	return r.Sessions.CreateReplacing(ctx, tokenHash, userID, previousHash, now)
}

// The session store only ever sees SHA-256(token): a leaked store (or backup)
// holds nothing that authenticates.
func TestSessionStoreNeverSeesTheToken(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSessions{Sessions: memory.NewSessions(time.Hour)}
	deps := importDeps(t, nil)
	deps.Sessions = rec
	a := app.New(deps)

	_, tok1, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, tok2, _ := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "")
	if len(tok1) < 43 || tok1 == tok2 { // 32 random bytes, base64url
		t.Fatalf("tokens = %q, %q; want two distinct 256-bit tokens", tok1, tok2)
	}
	if len(rec.hashes) != 2 {
		t.Fatalf("store saw %d creates, want 2", len(rec.hashes))
	}
	for i, tok := range []string{tok1, tok2} {
		sum := sha256.Sum256([]byte(tok))
		if rec.hashes[i] != hex.EncodeToString(sum[:]) {
			t.Errorf("store was given %q, want the SHA-256 of the token", rec.hashes[i])
		}
		// What the store holds does not authenticate.
		if _, ok, _ := a.Auth.Authenticate(ctx, rec.hashes[i]); ok {
			t.Error("a stored hash works as a token")
		}
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, ""); ok {
		t.Error("the empty token authenticates")
	}
}

// ---- clock ----

func TestSessionsExpireByTheApplicationClock(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	deps := importDeps(t, nil)
	deps.Sessions = memory.NewSessions(time.Hour)
	deps.Clock = clock.Now
	a := app.New(deps)
	u, tok, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Minute)
	if uid, ok, _ := a.Auth.Authenticate(ctx, tok); !ok || uid != u.ID {
		t.Error("session ended before its TTL")
	}
	clock.Advance(time.Minute)
	if _, ok, _ := a.Auth.Authenticate(ctx, tok); ok {
		t.Error("session outlived its TTL")
	}
	if _, ok, _ := a.Auth.CurrentUser(ctx, tok); ok {
		t.Error("CurrentUser accepted an expired session")
	}
}

func TestEnqueueStampsJobsWithTheApplicationClock(t *testing.T) {
	clock := newFakeClock()
	deps := importDeps(t, nil)
	deps.Clock = clock.Now
	a := app.New(deps)

	first := enqueue(t, a, "u1")
	clock.Advance(-time.Hour) // a clock may even step back; the queue follows it
	second := enqueue(t, a, "u1")
	if !first.CreatedAt.Equal(clock.Now().Add(time.Hour)) || !second.CreatedAt.Equal(clock.Now()) {
		t.Errorf("CreatedAt = %v, %v; want the application clock's readings", first.CreatedAt, second.CreatedAt)
	}
	if got, ok, err := a.Worker.Jobs.Claim(context.Background()); err != nil || !ok || got.ID != second.ID {
		t.Errorf("first claim = %s, want the job with the older CreatedAt (%s)", got.ID, second.ID)
	}
}

// chattyEngine reports progress per record, as the real engine does, and lets
// the test move time between reports.
type chattyEngine struct {
	steps []func(progress func(done, total int64))
}

func (e chattyEngine) Import(_ context.Context, _ string, in app.ArchiveInput) (*domain.Coverage, error) {
	for _, step := range e.steps {
		step(in.Progress)
	}
	cov := domain.NewCoverage()
	cov.Bytes = 100
	return cov, nil
}

type progressLog struct {
	app.Jobs
	mu   sync.Mutex
	seen []int64
}

func (p *progressLog) Progress(ctx context.Context, id string, saved int, done, total int64) error {
	p.mu.Lock()
	p.seen = append(p.seen, done)
	p.mu.Unlock()
	return p.Jobs.Progress(ctx, id, saved, done, total)
}

// Progress fires per record but is written through at most once a second —
// measured on the application clock, so this needs no sleeping.
func TestWorkerThrottlesProgressByTheApplicationClock(t *testing.T) {
	clock := newFakeClock()
	deps := importDeps(t, nil)
	jobs := &progressLog{Jobs: deps.Jobs}
	deps.Jobs = jobs
	deps.Clock = clock.Now
	report := func(done int64) func(func(int64, int64)) {
		return func(progress func(int64, int64)) { progress(done, 100) }
	}
	wait := func(d time.Duration) func(func(int64, int64)) {
		return func(func(int64, int64)) { clock.Advance(d) }
	}
	deps.Engine = chattyEngine{steps: []func(func(int64, int64)){
		report(1), report(2), report(3), // same instant: only the first is written
		wait(999 * time.Millisecond), report(4),
		wait(time.Millisecond), report(5), // a full second since the last write
		report(6),
	}}
	a := app.New(deps)
	job := enqueue(t, a, "u1")
	runJob(t, a, "u1", job.ID)

	want := []int64{1, 5, 100} // …plus the final 100% the worker always records
	if len(jobs.seen) != len(want) {
		t.Fatalf("progress writes = %v, want %v", jobs.seen, want)
	}
	for i := range want {
		if jobs.seen[i] != want[i] {
			t.Fatalf("progress writes = %v, want %v", jobs.seen, want)
		}
	}
}

// ---- timeline query normalization ----

// spyEvents records the query the repository receives.
type spyEvents struct {
	app.EventRepository
	got   []app.Query
	calls int
}

func (s *spyEvents) Timeline(ctx context.Context, q app.Query) (app.Page, error) {
	s.calls++
	s.got = append(s.got, q)
	return s.EventRepository.Timeline(ctx, q)
}

func TestTimelineNormalizesTheQueryOnce(t *testing.T) {
	ctx := context.Background()
	spy := &spyEvents{EventRepository: memory.NewEvents()}
	svc := app.TimelineService{Events: spy}

	for in, want := range map[int]int{
		0: app.DefaultPageLimit, -1: app.DefaultPageLimit, 20: 20,
		app.MaxPageLimit: app.MaxPageLimit, app.MaxPageLimit + 1: app.MaxPageLimit,
	} {
		spy.got = nil
		if _, err := svc.Query(ctx, app.Query{UserID: "u", Limit: in, Search: "  hi "}); err != nil {
			t.Fatal(err)
		}
		if got := spy.got[0]; got.Limit != want || got.Search != "hi" {
			t.Errorf("limit %d reached the repository as %+v; want limit %d, search trimmed", in, got, want)
		}
	}

	// A malformed cursor is the caller's fault and never reaches the store.
	spy.calls = 0
	for _, cur := range []string{"garbage!", "AAAA", "VXxub3QtYS11dWlk" /* U|not-a-uuid */} {
		_, err := svc.Query(ctx, app.Query{UserID: "u", Cursor: cur})
		if !errors.Is(err, app.ErrBadCursor) {
			t.Errorf("cursor %q err = %v, want ErrBadCursor", cur, err)
		}
	}
	if spy.calls != 0 {
		t.Errorf("the repository was queried %d times with a malformed cursor", spy.calls)
	}

	// A cursor the application issued round-trips.
	ev := domain.StoredEvent{ID: "0a0b0c0d-0000-4000-8000-000000000001"}
	ev.OccurredAt = time.Date(2023, 1, 2, 3, 4, 5, 6000, time.UTC)
	for _, e := range []domain.StoredEvent{ev, {ID: ev.ID}} {
		c, err := app.DecodeCursor(app.CursorAfter(e).Encode())
		if err != nil || c != app.CursorAfter(e) {
			t.Errorf("cursor round trip = %+v, %v; want %+v", c, err, app.CursorAfter(e))
		}
		if _, err := svc.Query(ctx, app.Query{UserID: "u", Cursor: app.CursorAfter(e).Encode()}); err != nil {
			t.Errorf("a valid cursor was refused: %v", err)
		}
	}
}
