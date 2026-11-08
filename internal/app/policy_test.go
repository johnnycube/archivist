package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/adapters/staging"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// ---- password policy (audit L5) ----

func TestPasswordPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, email, password, reason string
	}{
		{"ok", "me@x.com", "password123", ""},
		{"exactly the minimum", "me@x.com", "abcdefg1", ""},
		{"exactly the maximum", "me@x.com", strings.Repeat("ab", 512), ""},
		{"multi-byte characters count as their bytes", "me@x.com", "pässwörd", ""},
		{"too short", "me@x.com", "abcdefg", "at least 8"},
		{"empty", "me@x.com", "", "at least 8"},
		{"too long", "me@x.com", strings.Repeat("ab", 512) + "c", "at most 1024"},
		{"the email", "someone@example.com", "someone@example.com", "email"},
		{"the email, other case and padding", "someone@example.com", " Someone@Example.COM ", "email"},
		{"one repeated character", "me@x.com", "aaaaaaaaaaaa", "repeated"},
		{"one repeated multi-byte character", "me@x.com", "ääääääää", "repeated"},
		{"two characters are enough to not be 'repeated'", "me@x.com", "aaaaaaab", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := app.CheckPassword(tc.email, tc.password)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("CheckPassword = %v, want nil", err)
				}
				return
			}
			var weak *app.WeakPasswordError
			if !errors.Is(err, app.ErrWeakPassword) || !errors.As(err, &weak) {
				t.Fatalf("CheckPassword = %v, want a *WeakPasswordError matching ErrWeakPassword", err)
			}
			if !strings.Contains(weak.Reason, tc.reason) || err.Error() != weak.Reason {
				t.Errorf("reason = %q (Error %q), want one mentioning %q", weak.Reason, err.Error(), tc.reason)
			}
		})
	}
}

func TestPasswordPolicyIsEnforcedAtSignupAndPasswordChange(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil)
	for _, bad := range []string{"short", "me@x.com", "zzzzzzzzzz", strings.Repeat("x1", 600)} {
		_, _, err := a.Auth.SignUp(ctx, "me@x.com", bad)
		if !errors.Is(err, app.ErrWeakPassword) {
			t.Errorf("SignUp(%.12q…) err = %v, want ErrWeakPassword", bad, err)
		}
		if errors.Is(err, app.ErrInvalidEmail) {
			t.Errorf("SignUp(%.12q…) err = %v: a weak password is not an invalid email", bad, err)
		}
	}
	if _, _, err := a.Auth.SignUp(ctx, "not-an-email", "password123"); !errors.Is(err, app.ErrInvalidEmail) || errors.Is(err, app.ErrWeakPassword) {
		t.Errorf("SignUp(bad email) err = %v, want ErrInvalidEmail and not ErrWeakPassword", err)
	}
	if _, ok, _ := a.Auth.Users.ByEmail(ctx, "me@x.com"); ok {
		t.Fatal("a refused signup created the user")
	}

	u, _, err := a.Auth.SignUp(ctx, "me@x.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"short", "ME@x.com", "zzzzzzzzzz", strings.Repeat("x1", 600)} {
		_, err := a.Auth.ChangePassword(ctx, u.ID, "password123", bad)
		if !errors.Is(err, app.ErrWeakPassword) {
			t.Errorf("ChangePassword(%.12q…) err = %v, want ErrWeakPassword", bad, err)
		}
	}
	if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", ""); err != nil {
		t.Errorf("the old password stopped working after refused changes: %v", err)
	}
	if _, err := a.Auth.ChangePassword(ctx, u.ID, "password123", "password456"); err != nil {
		t.Errorf("a good new password: %v", err)
	}
}

// ---- session replacement on login (8b) ----

func TestLogInRevokesOnlyTheSameUsersPreviousSession(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil)
	alice, aliceTok, _ := a.Auth.SignUp(ctx, "alice@x.com", "password123")
	_, aliceOther, _ := a.Auth.LogInGuarded(ctx, "alice@x.com", "password123", "", "") // another device
	_, bobTok, _ := a.Auth.SignUp(ctx, "bob@x.com", "password123")
	alive := func(tok string) bool {
		_, ok, err := a.Auth.Authenticate(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// A failed login revokes nothing.
	if _, _, err := a.Auth.LogInGuarded(ctx, "alice@x.com", "wrong-password", aliceTok, ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if !alive(aliceTok) {
		t.Fatal("a FAILED login revoked the previous session")
	}

	u, fresh, err := a.Auth.LogInGuarded(ctx, "alice@x.com", "password123", aliceTok, "")
	if err != nil || u.ID != alice.ID || fresh == aliceTok {
		t.Fatalf("LogInGuarded = %+v, %q, %v", u, fresh, err)
	}
	if alive(aliceTok) {
		t.Error("the previous session survived the login that replaced it")
	}
	if !alive(fresh) || !alive(aliceOther) || !alive(bobTok) {
		t.Error("LogInGuarded ended a session other than the previous one")
	}

	// alice logs in from a browser holding BOB's session: not hers to revoke.
	if _, _, err := a.Auth.LogInGuarded(ctx, "alice@x.com", "password123", bobTok, ""); err != nil {
		t.Fatal(err)
	}
	if !alive(bobTok) {
		t.Error("logging in with somebody else's token as previous revoked THEIR session")
	}
	for _, previous := range []string{"", "garbage"} {
		if _, tok, err := a.Auth.LogInGuarded(ctx, "alice@x.com", "password123", previous, ""); err != nil || !alive(tok) {
			t.Errorf("LogInGuarded(previous %q) = %v", previous, err)
		}
	}
}

// ---- login throttle (10) ----

type throttleEnv struct {
	a     *app.App
	clock *fakeClock
	store *memory.Attempts
}

func newThrottleEnv(t *testing.T) throttleEnv {
	t.Helper()
	deps := importDeps(t, nil)
	clock := newFakeClock()
	store := memory.NewAttempts()
	deps.Clock, deps.Attempts = clock.Now, store
	a := app.New(deps)
	if _, _, err := a.Auth.SignUp(context.Background(), "me@x.com", "password123"); err != nil {
		t.Fatal(err)
	}
	return throttleEnv{a: a, clock: clock, store: store}
}

// fail makes n failed logins from client and reports the last error.
func (e throttleEnv) fail(email, client string, n int) (err error) {
	for i := 0; i < n; i++ {
		_, _, err = e.a.Auth.LogInGuarded(context.Background(), email, "wrong-password", "", client)
	}
	return err
}

func TestLoginThrottlePerClient(t *testing.T) {
	ctx := context.Background()
	e := newThrottleEnv(t)
	if err := e.fail("me@x.com", "10.0.0.1", app.MaxLoginFailuresPair); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Fatalf("failure %d err = %v, want ErrInvalidCredentials still", app.MaxLoginFailuresPair, err)
	}
	e.clock.Advance(time.Minute)
	// Now even the RIGHT password is refused from that client…
	_, _, err := e.a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "10.0.0.1")
	var tooMany *app.TooManyAttemptsError
	if !errors.Is(err, app.ErrTooManyAttempts) || !errors.As(err, &tooMany) {
		t.Fatalf("attempt %d err = %v, want ErrTooManyAttempts", app.MaxLoginFailuresPair+1, err)
	}
	if want := app.LoginAttemptWindow - time.Minute; tooMany.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v (when the oldest failure leaves the window)", tooMany.RetryAfter, want)
	}
	// …in whatever spelling of the email…
	if _, _, err := e.a.Auth.LogInGuarded(ctx, "  ME@X.com ", "password123", "", "10.0.0.1"); !errors.Is(err, app.ErrTooManyAttempts) {
		t.Errorf("other spelling of the email: err = %v, want ErrTooManyAttempts", err)
	}
	// …(ii) but the owner gets in from elsewhere: the attacker locked out only
	// themselves.
	if _, tok, err := e.a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "192.168.1.9"); err != nil || tok == "" {
		t.Fatalf("owner's login from another address = %v; one client's failures locked the account", err)
	}
	// The window slides: once the failures are older than it, the client may
	// try again.
	e.clock.Advance(app.LoginAttemptWindow)
	if _, _, err := e.a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "10.0.0.1"); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

func TestLoginThrottleRefusedAttemptsAreNotCounted(t *testing.T) {
	e := newThrottleEnv(t)
	e.fail("me@x.com", "10.0.0.1", app.MaxLoginFailuresPair)
	// Hammering on while refused must not push the retry time out.
	e.clock.Advance(14 * time.Minute)
	if err := e.fail("me@x.com", "10.0.0.1", 50); !errors.Is(err, app.ErrTooManyAttempts) {
		t.Fatalf("err = %v, want ErrTooManyAttempts", err)
	}
	e.clock.Advance(time.Minute)
	if _, _, err := e.a.Auth.LogInGuarded(context.Background(), "me@x.com", "password123", "", "10.0.0.1"); err != nil {
		t.Errorf("15m after the counted failures: %v; refused attempts extended the lockout", err)
	}
}

func TestLoginThrottleSuccessResetsOnlyThePair(t *testing.T) {
	ctx := context.Background()
	e := newThrottleEnv(t)
	e.fail("me@x.com", "home", app.MaxLoginFailuresPair-1)
	if _, _, err := e.a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "home"); err != nil {
		t.Fatal(err)
	}
	// (iii) the pair starts over: a full budget of failures again.
	if err := e.fail("me@x.com", "home", app.MaxLoginFailuresPair); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("after a success the pair counter was not reset: %v", err)
	}
	if err := e.fail("me@x.com", "home", 1); !errors.Is(err, app.ErrTooManyAttempts) {
		t.Errorf("err = %v, want ErrTooManyAttempts", err)
	}
}

func TestLoginThrottlePerAccountAcrossClients(t *testing.T) {
	ctx := context.Background()
	e := newThrottleEnv(t)
	// A distributed guesser: every client stays under its own limit. The owner
	// logs in successfully in the middle — which must not reset the ACCOUNT
	// counter, or every success would hand the guessers a fresh budget.
	per := app.MaxLoginFailuresPair - 1
	clients := 0
	for total := 0; total < app.MaxLoginFailuresAccount; clients++ {
		n := min(per, app.MaxLoginFailuresAccount-total)
		if err := e.fail("me@x.com", "bot-"+string(rune('a'+clients)), n); !errors.Is(err, app.ErrInvalidCredentials) {
			t.Fatalf("client %d: err = %v, want ErrInvalidCredentials (under its own limit)", clients, err)
		}
		total += n
		if clients == 3 {
			if _, _, err := e.a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", "owner"); err != nil {
				t.Fatal(err)
			}
		}
	}
	// (ii) the account-wide limit now refuses everybody, fresh clients too.
	for _, client := range []string{"bot-fresh", "owner"} {
		_, _, err := e.a.Auth.LogInGuarded(ctx, "me@x.com", "password123", "", client)
		var tooMany *app.TooManyAttemptsError
		if !errors.As(err, &tooMany) || tooMany.RetryAfter <= 0 || tooMany.RetryAfter > app.LoginAttemptWindow {
			t.Errorf("client %q after %d failures on the account: err = %v, want TooManyAttempts with a sane RetryAfter", client, app.MaxLoginFailuresAccount, err)
		}
	}
	// Other accounts are unaffected.
	if _, _, err := e.a.Auth.SignUp(ctx, "other@x.com", "password123"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.a.Auth.LogInGuarded(ctx, "other@x.com", "password123", "", "bot-a"); err != nil {
		t.Errorf("another account, from a client that failed elsewhere: %v", err)
	}
}

// (iv) the throttle must not tell a registered email from an unregistered one:
// same errors, same limits, same RetryAfter — and the same records in the store.
func TestLoginThrottleIsNoExistenceOracle(t *testing.T) {
	type observation struct {
		errs  []string
		after time.Duration
		n     int
	}
	observe := func(email string) observation {
		e := newThrottleEnv(t)
		var o observation
		for i := 0; i < app.MaxLoginFailuresPair+2; i++ {
			_, _, err := e.a.Auth.LogInGuarded(context.Background(), email, "wrong-password", "", "10.0.0.1")
			o.errs = append(o.errs, err.Error())
			var tooMany *app.TooManyAttemptsError
			if errors.As(err, &tooMany) {
				o.after = tooMany.RetryAfter
			}
			e.clock.Advance(time.Second)
		}
		// What the store holds for the account key: failures, counted the same.
		o.n, _, _ = e.store.Failures(context.Background(), accountKeyOf(email), e.clock.Now(), app.LoginAttemptWindow)
		return o
	}
	known, unknown := observe("me@x.com"), observe("nobody@x.com")
	if strings.Join(known.errs, "|") != strings.Join(unknown.errs, "|") || known.after != unknown.after {
		t.Errorf("known email:   %v (retry %v)\nunknown email: %v (retry %v)\nwant identical behaviour", known.errs, known.after, unknown.errs, unknown.after)
	}
	if known.n != app.MaxLoginFailuresPair || unknown.n != known.n {
		t.Errorf("failures recorded: known %d, unknown %d; want %d for both", known.n, unknown.n, app.MaxLoginFailuresPair)
	}
}

// accountKeyOf mirrors AuthService's key derivation (length-prefixed parts,
// SHA-256, hex) — asserting the format also asserts that it IS a hash.
func accountKeyOf(email string) string {
	parts := []string{"login-account", domain.NormalizeEmail(email)}
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p)) + ":" + p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// keyRecorder notes every key the limiter is given.
type keyRecorder struct {
	app.AttemptLimiter
	mu   sync.Mutex
	keys []string
}

func (k *keyRecorder) RecordFailure(ctx context.Context, key string, now time.Time) error {
	k.mu.Lock()
	k.keys = append(k.keys, key)
	k.mu.Unlock()
	return k.AttemptLimiter.RecordFailure(ctx, key, now)
}

func TestLoginThrottleStoresOnlyHashes(t *testing.T) {
	deps := importDeps(t, nil)
	rec := &keyRecorder{AttemptLimiter: memory.NewAttempts()}
	deps.Attempts = rec
	a := app.New(deps)
	_, _, _ = a.Auth.LogInGuarded(context.Background(), "Victim@Example.com", "wrong-password", "", "203.0.113.7")
	if len(rec.keys) != 2 || rec.keys[0] == rec.keys[1] {
		t.Fatalf("keys recorded = %q, want two distinct ones (pair, account)", rec.keys)
	}
	for _, k := range rec.keys {
		if _, err := hex.DecodeString(k); err != nil || len(k) != 64 ||
			strings.Contains(strings.ToLower(k), "victim") || strings.Contains(k, "203.0.113.7") {
			t.Errorf("stored key %q is not a SHA-256", k)
		}
	}
	if rec.keys[1] != accountKeyOf("victim@example.com") {
		t.Errorf("account key = %q, want the hash of the NORMALISED email", rec.keys[1])
	}
}

func TestLogInGuardedWithoutLimiterAndSessionReplacement(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil) // no Attempts port
	_, old, _ := a.Auth.SignUp(ctx, "me@x.com", "password123")
	for i := 0; i < app.MaxLoginFailuresPair+5; i++ {
		if _, _, err := a.Auth.LogInGuarded(ctx, "me@x.com", "wrong-password", "", "c"); !errors.Is(err, app.ErrInvalidCredentials) {
			t.Fatalf("without a limiter: err = %v, want ErrInvalidCredentials every time", err)
		}
	}
	_, tok, err := a.Auth.LogInGuarded(ctx, "me@x.com", "password123", old, "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, old); ok {
		t.Error("LogInGuarded did not replace the previous session")
	}
	if _, ok, _ := a.Auth.Authenticate(ctx, tok); !ok {
		t.Error("new session not valid")
	}
}

// ---- typed not-found (11) ----

func TestUseCasesReportNotFoundAsAnError(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	a := newImportApp(t, blobs)
	const missing = "00000000-0000-4000-8000-000000000000"

	if _, err := a.Import.GetJob(ctx, "u1", missing); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("GetJob err = %v, want ErrNotFound", err)
	}
	if _, err := a.Import.ReprocessJob(ctx, "u1", missing); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("ReprocessJob err = %v, want ErrNotFound", err)
	}
	if _, err := a.Import.DeleteJob(ctx, "u1", missing); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("DeleteJob err = %v, want ErrNotFound", err)
	}
	if _, err := a.Timeline.Event(ctx, "u1", missing); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("Event err = %v, want ErrNotFound", err)
	}
	if _, _, err := a.Timeline.Media(ctx, "u1", missing, 0); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("Media of a missing event err = %v, want ErrNotFound", err)
	}

	// Found, tenant-scoped, busy.
	job := enqueue(t, a, "u1")
	if got, err := a.Import.GetJob(ctx, "u1", job.ID); err != nil || got.ID != job.ID {
		t.Errorf("GetJob = %+v, %v", got, err)
	}
	if _, err := a.Import.GetJob(ctx, "u2", job.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another user's GetJob err = %v, want ErrNotFound", err)
	}
	if _, err := a.Import.ReprocessJob(ctx, "u1", job.ID); !errors.Is(err, app.ErrJobBusy) {
		t.Errorf("ReprocessJob(queued) err = %v, want ErrJobBusy", err)
	}
	runJob(t, a, "u1", job.ID)
	page, _ := a.Timeline.Query(ctx, app.Query{UserID: "u1"})
	ev := page.Events[0]
	if got, err := a.Timeline.Event(ctx, "u1", ev.ID); err != nil || got.ID != ev.ID {
		t.Errorf("Event = %+v, %v", got, err)
	}
	if _, err := a.Timeline.Event(ctx, "u2", ev.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another user's Event err = %v, want ErrNotFound", err)
	}
	// An event that exists but has no media there: ErrNoMedia, not ErrNotFound.
	if _, _, err := a.Timeline.Media(ctx, "u1", ev.ID, 7); !errors.Is(err, app.ErrNoMedia) || errors.Is(err, app.ErrNotFound) {
		t.Errorf("Media(idx out of range) err = %v, want ErrNoMedia", err)
	}
	if n, err := a.Import.DeleteJob(ctx, "u1", job.ID); err != nil || n != 3 {
		t.Errorf("DeleteJob = %d, %v; want its 3 events deleted", n, err)
	}
	if _, err := a.Import.DeleteJob(ctx, "u1", job.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("second DeleteJob err = %v, want ErrNotFound", err)
	}
}

// A media reference whose blob is gone is "no media" for a transport, which
// should not need to know that blobs are files.
func TestMediaWithMissingBlobIsNoMedia(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, newMemBlobs())
	a := app.New(deps)
	se, _, err := deps.Events.Save(ctx, "u1", "acct", domain.Event{
		SourceID: "reddit", ExternalID: "post:1", Type: "reddit.post", Category: domain.CategoryContent,
		Media: []domain.Media{{BlobKey: "gone.jpg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.Timeline.Media(ctx, "u1", se.ID, 0)
	if !errors.Is(err, app.ErrNoMedia) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Media with a missing blob err = %v, want ErrNoMedia (still wrapping fs.ErrNotExist)", err)
	}
}

// ---- original upload names (5) ----

func TestSanitizeUploadName(t *testing.T) {
	long := strings.Repeat("ä", 200) // 400 bytes
	for in, want := range map[string]string{
		"WhatsApp Chat - Anna.zip":         "WhatsApp Chat - Anna.zip",
		"":                                 "",
		"   ":                              "",
		".":                                "",
		"..":                               "",
		"/":                                "",
		"../../etc/passwd":                 "passwd",
		"/abs/path/export.zip":             "export.zip",
		`C:\Users\me\Downloads\export.zip`: "export.zip",
		`mixed/sep\export.zip`:             "export.zip",
		"dir/":                             "dir",
		"new\nline\x00nul\ttab\x7f.zip":    "newlinenultab.zip",
		"bad\xffutf8.zip":                  "badutf8.zip",
		"Ünïcödé 📦.zip":                    "Ünïcödé 📦.zip",
		long + ".zip":                      strings.Repeat("ä", 127),
	} {
		got := app.SanitizeUploadName(in)
		if got != want {
			t.Errorf("SanitizeUploadName(%q) = %q, want %q", in, got, want)
		}
		if len(got) > 255 {
			t.Errorf("SanitizeUploadName(%q) is %d bytes", in, len(got))
		}
	}
}

// nameSpy is an engine that notes the Name of every part it is handed — by
// type assertion, exactly what a real engine has available.
type nameSpy struct{ names *[]string }

func (e nameSpy) Import(_ context.Context, _ string, in app.ArchiveInput) (*domain.Coverage, error) {
	for _, p := range in.Parts {
		n, ok := p.(interface{ Name() string })
		if !ok {
			*e.names = append(*e.names, "<no Name method>")
			continue
		}
		*e.names = append(*e.names, n.Name())
	}
	return &domain.Coverage{}, nil
}

// The client's file names travel Stage → job → the parts the engine opens, on
// the first run and — restored from the blob store — on a reprocess.
func TestUploadNamesReachTheEngine(t *testing.T) {
	ctx := context.Background()
	var seen []string
	deps := importDeps(t, newMemBlobs())
	deps.Engine = nameSpy{&seen}
	a := app.New(deps)

	zip := buildZip(t)
	var parts []app.StagedPart
	for _, name := range []string{`C:\fakepath\WhatsApp Chat - Anna.zip`, ""} {
		f, err := os.Open(zip)
		if err != nil {
			t.Fatal(err)
		}
		p, err := a.Import.StageNamed(ctx, "u1", name, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, p)
	}
	if parts[0].Name != "WhatsApp Chat - Anna.zip" || parts[1].Name != "" {
		t.Fatalf("staged names = %q, %q", parts[0].Name, parts[1].Name)
	}
	// Enqueue sanitises again: ImportSpec is caller-built.
	parts[1].Name = "../sneaky\x00.zip"
	job, err := a.Import.Enqueue(ctx, app.ImportSpec{UserID: "u1", Source: "reddit", Parts: parts})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"WhatsApp Chat - Anna.zip", "sneaky.zip"}
	if strings.Join(job.PartNames, "|") != strings.Join(want, "|") {
		t.Fatalf("job.PartNames = %q, want %q", job.PartNames, want)
	}
	runJob(t, a, "u1", job.ID)
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Errorf("engine saw part names %q, want %q", seen, want)
	}
	// The staged name is not the staging file's own (random) name.
	seen = nil
	if _, err := a.Import.ReprocessJob(ctx, "u1", job.ID); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, "u1", job.ID)
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Errorf("on reprocess the engine saw %q, want %q (restored parts keep their names)", seen, want)
	}
}

// ---- staging janitor (audit L8) ----

// stagedPath is where the filesystem staging area keeps ref.
func stagedPath(a *app.App, ref string) string {
	return filepath.Join(a.Import.Staging.(*staging.FS).Dir(), ref)
}

// age backdates a staged file.
func age(t *testing.T, a *app.App, ref string, by time.Duration) {
	t.Helper()
	path := stagedPath(a, ref)
	old := time.Now().Add(-by)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func stagedExists(t *testing.T, a *app.App, ref string) bool {
	t.Helper()
	f, err := a.Import.Staging.Open(context.Background(), ref, "")
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return true
}

func stageBytes(t *testing.T, a *app.App, userID string) string {
	t.Helper()
	p, err := a.Import.StageNamed(context.Background(), userID, "x.zip", strings.NewReader("orphaned upload"))
	if err != nil {
		t.Fatal(err)
	}
	return p.Ref
}

func TestSweepStagingRemovesOnlyOldUnreferencedUploads(t *testing.T) {
	ctx := context.Background()
	a := newImportApp(t, nil) // no blob store: uploads of finished jobs would go anyway; see below
	const old = 48 * time.Hour

	orphanOld := stageBytes(t, a, "u1")
	orphanYoung := stageBytes(t, a, "u1")
	orphanBorderline := stageBytes(t, a, "u1")
	age(t, a, orphanOld, old)
	age(t, a, orphanBorderline, 23*time.Hour)

	// u2: an old upload whose job is still QUEUED (the worker was down for two
	// days, say). Referenced → must stay, or the import is lost.
	queued := enqueue(t, a, "u2")
	age(t, a, queued.ZipPaths[0], old)
	// …and an old orphan next to it, which stays too while u2 has an import
	// in flight (it could be a restored copy a running reprocess reads).
	besideActive := stageBytes(t, a, "u2")
	age(t, a, besideActive, old)

	removed, err := a.Import.SweepStaging(ctx, 0) // 0 = the 24h default
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || stagedExists(t, a, orphanOld) {
		t.Errorf("removed = %d, old orphan exists = %v; want exactly the old orphan removed", removed, stagedExists(t, a, orphanOld))
	}
	for name, ref := range map[string]string{
		"young orphan": orphanYoung, "23h-old orphan": orphanBorderline,
		"queued job's upload": queued.ZipPaths[0], "orphan of a user with an active import": besideActive,
	} {
		if !stagedExists(t, a, ref) {
			t.Errorf("the janitor removed the %s", name)
		}
	}
	// A shorter threshold takes the borderline one as well.
	if removed, err := a.Import.SweepStaging(ctx, time.Hour); err != nil || removed != 1 || stagedExists(t, a, orphanBorderline) {
		t.Errorf("SweepStaging(1h) = %d, %v; want the 23h-old orphan removed", removed, err)
	}
	if removed, err := a.Import.SweepStaging(ctx, time.Hour); err != nil || removed != 0 {
		t.Errorf("second sweep = %d, %v; want nothing left to do", removed, err)
	}
}

// stuckRunning is a Jobs whose Complete fails: the worker then keeps the staged
// upload although the job ran, which is how an upload outlives a RUNNING job.
type stuckRunning struct{ app.Jobs }

func (stuckRunning) Complete(context.Context, string, domain.JobStatus, int, *domain.Coverage, string) error {
	return errors.New("outcome not recorded")
}

func TestSweepStagingNeverTouchesWhatAJobPointsAt(t *testing.T) {
	ctx := context.Background()
	const old = 72 * time.Hour

	t.Run("running", func(t *testing.T) {
		deps := importDeps(t, newMemBlobs())
		jobs := deps.Jobs
		deps.Jobs = stuckRunning{jobs}
		a := app.New(deps)
		job := enqueue(t, a, "u1")
		_, _ = a.Worker.ProcessNext(ctx) // runs; outcome not recorded → still 'running', upload kept
		if got, _ := a.Import.GetJob(ctx, "u1", job.ID); got.Status != domain.JobRunning {
			t.Fatalf("job status = %q, want running", got.Status)
		}
		age(t, a, job.ZipPaths[0], old)
		if removed, err := a.Import.SweepStaging(ctx, 0); err != nil || removed != 0 || !stagedExists(t, a, job.ZipPaths[0]) {
			t.Errorf("sweep with a running job = %d, %v; its upload must stay", removed, err)
		}
	})

	// A FINISHED job's upload is normally gone already. When it is not (the
	// blob store was down, the outcome write failed and the job was finished
	// by hand, …) the job row still points at it, and ImportService.DeleteJob
	// is what removes it — not the janitor. This is also the case that a
	// "skip users with active imports" rule alone would NOT cover.
	t.Run("finished", func(t *testing.T) {
		deps := importDeps(t, newMemBlobs())
		a := app.New(deps)
		ref := stageBytes(t, a, "u1")
		job, err := deps.Jobs.Create(ctx, domain.ImportJob{
			UserID: "u1", Source: "reddit", Label: "default",
			ZipPaths: []string{ref}, Status: domain.JobError,
		})
		if err != nil {
			t.Fatal(err)
		}
		orphan := stageBytes(t, a, "u1")
		age(t, a, ref, old)
		age(t, a, orphan, old)
		if removed, err := a.Import.SweepStaging(ctx, 0); err != nil || removed != 1 {
			t.Errorf("sweep = %d, %v; want only the orphan removed", removed, err)
		}
		if !stagedExists(t, a, ref) || stagedExists(t, a, orphan) {
			t.Errorf("finished job's upload exists = %v (want true), orphan exists = %v (want false)", stagedExists(t, a, ref), stagedExists(t, a, orphan))
		}
		if _, err := a.Import.DeleteJob(ctx, "u1", job.ID); err != nil || stagedExists(t, a, ref) {
			t.Errorf("DeleteJob = %v; it is what removes a finished job's upload", err)
		}
	})

	// Jobs queued by old versions carry the staged file's PATH instead of a ref.
	t.Run("referenced by path", func(t *testing.T) {
		deps := importDeps(t, newMemBlobs())
		a := app.New(deps)
		ref := stageBytes(t, a, "u1")
		abs := stagedPath(a, ref)
		for _, form := range []string{abs, filepath.Join("data", "uploads", ref), strings.ReplaceAll(abs, "/", `\`)} {
			job, err := deps.Jobs.Create(ctx, domain.ImportJob{
				UserID: "u1", Source: "reddit", Label: "default",
				ZipPaths: []string{form}, Status: domain.JobDone,
			})
			if err != nil {
				t.Fatal(err)
			}
			age(t, a, ref, old)
			if removed, err := a.Import.SweepStaging(ctx, 0); err != nil || removed != 0 || !stagedExists(t, a, ref) {
				t.Errorf("job ref %q: sweep = %d, %v; the upload it points at must stay", form, removed, err)
			}
			_, _ = deps.Jobs.Delete(ctx, "u1", job.ID)
		}
		if removed, _ := a.Import.SweepStaging(ctx, 0); removed != 1 {
			t.Errorf("after the jobs are gone: removed = %d, want 1", removed)
		}
	})
}

// failingList is a Staging that cannot enumerate.
type failingList struct{ app.Staging }

func (failingList) List(context.Context) ([]app.StagedRef, error) { return nil, io.ErrUnexpectedEOF }

func TestSweepStagingReportsErrorsAndRemovesNothing(t *testing.T) {
	deps := importDeps(t, nil)
	deps.Staging = failingList{deps.Staging}
	a := app.New(deps)
	if removed, err := a.Import.SweepStaging(context.Background(), 0); removed != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("SweepStaging = %d, %v; want 0 and the listing error", removed, err)
	}
}
