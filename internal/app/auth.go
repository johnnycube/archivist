package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/domain"
)

// ErrInvalidCredentials is returned on a failed login or password check.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrInvalidEmail is returned by SignUp when the email is not one.
var ErrInvalidEmail = errors.New("a valid email address is required")

// maxEmailBytes is the longest address SMTP allows (RFC 5321 path limit).
const maxEmailBytes = 254

// validEmail reports whether email (normalized) is a bare address: one that
// net/mail parses to itself, at most 254 bytes. Anything longer is no
// address, and would only meet the database's index size limit.
func validEmail(email string) bool {
	if len(email) > maxEmailBytes || !strings.Contains(email, "@") {
		return false
	}
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && a.Name == ""
}

// ErrWeakPassword is the class of password-policy refusals (signup and
// password change). The concrete error is a *WeakPasswordError, whose message
// is the reason, fit to show to the user.
var ErrWeakPassword = errors.New("password does not meet the policy")

// Password policy. Bytes, not characters: that is what the limits protect.
// The maximum is not a hashing-cost concern (Argon2id's cost does not grow
// with its input) — a megabyte-sized "password" is simply never legitimate.
// There is deliberately no breached-password lookup: this is a self-hosted
// server that must work offline.
const (
	MinPasswordBytes = 8
	MaxPasswordBytes = 1024
)

// WeakPasswordError says why a password was refused. It matches
// ErrWeakPassword with errors.Is.
type WeakPasswordError struct {
	// Reason is a complete, user-presentable sentence fragment.
	Reason string
}

func (e *WeakPasswordError) Error() string { return e.Reason }

func (e *WeakPasswordError) Is(target error) bool { return target == ErrWeakPassword }

// CheckPassword is THE password policy: MinPasswordBytes..MaxPasswordBytes,
// not the account's email, not one character repeated. It returns nil or a
// *WeakPasswordError.
func CheckPassword(email, password string) error {
	var reason string
	switch {
	case len(password) < MinPasswordBytes:
		reason = fmt.Sprintf("password must be at least %d characters", MinPasswordBytes)
	case len(password) > MaxPasswordBytes:
		reason = fmt.Sprintf("password must be at most %d bytes", MaxPasswordBytes)
	case email != "" && domain.NormalizeEmail(password) == domain.NormalizeEmail(email):
		reason = "password must not be your email address"
	case strings.Trim(password, password[:firstRuneLen(password)]) == "":
		reason = "password must not be a single repeated character"
	default:
		return nil
	}
	return &WeakPasswordError{Reason: reason}
}

func firstRuneLen(s string) int {
	_, n := utf8.DecodeRuneInString(s)
	return n
}

// ErrTooManyAttempts is the class of login-throttle refusals; the concrete
// error is a *TooManyAttemptsError carrying how long to wait.
var ErrTooManyAttempts = errors.New("too many failed login attempts")

// TooManyAttemptsError is returned by LogInGuarded when the throttle refuses an
// attempt. It matches ErrTooManyAttempts with errors.Is.
type TooManyAttemptsError struct {
	// RetryAfter is when the oldest counted failure leaves the window.
	RetryAfter time.Duration
}

func (e *TooManyAttemptsError) Error() string {
	return fmt.Sprintf("%v; retry in %v", ErrTooManyAttempts, e.RetryAfter.Round(time.Second))
}

func (e *TooManyAttemptsError) Is(target error) bool { return target == ErrTooManyAttempts }

// The login throttle (LogInGuarded). Failures are counted in a sliding window:
//
//   - per (account, client) pair: what one client may guess at one account;
//   - per account over all clients: bounds distributed guessing. It is
//     deliberately much higher than the pair limit, so that one client burning
//     through its own budget cannot lock the owner out from elsewhere.
const (
	LoginAttemptWindow      = 15 * time.Minute
	MaxLoginFailuresPair    = 10
	MaxLoginFailuresAccount = 100
	// MaxReauthFailures bounds wrong passwords given to confirm a password
	// change or an account deletion, per account in LoginAttemptWindow: a
	// stolen session must not be a way to guess the account's password.
	MaxReauthFailures = 10
)

// ErrSignupDisabled is returned by SignUp when the operator closed registration.
var ErrSignupDisabled = errors.New("signup is disabled on this server")

// AuthService is the use case for authentication and account lifecycle.
// Sessions are server-side and revocable: logout, password changes, and
// account deletion all invalidate tokens immediately.
type AuthService struct {
	Users    Users
	Hasher   PasswordHasher
	Sessions Sessions
	// Attempts is the shared state of the login throttle (optional; nil makes
	// LogInGuarded unthrottled).
	Attempts AttemptLimiter
	// Jobs, Blobs and Staging (each optional) are what account deletion has to
	// clean up besides the user row.
	Jobs    Jobs
	Blobs   Blobs
	Staging Staging
	// SignupDisabled closes registration (ALLOW_SIGNUP=0).
	SignupDisabled bool
	// uploadsInFlight counts the user's uploads streaming right now (New
	// wires it to the upload gate); account deletion waits for them.
	uploadsInFlight func(userID string) int
	// OIDC are the identity providers people may log in through, in the
	// order the login page offers them (see oidc.go).
	OIDC []OIDCProviderConfig
	// Clock is the time sessions are created and checked at (nil = time.Now).
	Clock func() time.Time
	// Log receives non-fatal problems, e.g. a password-hash upgrade that could
	// not be stored (nil = discard).
	Log *slog.Logger
	// dummyHash is a valid hash of a throwaway password. logIn verifies against
	// it when the email is unknown, so both paths cost the same hash work.
	dummyHash string
}

// newAuthService derives the dummy hash from the hasher once, at construction.
func newAuthService(d Deps) AuthService {
	a := AuthService{
		Users: d.Users, Hasher: d.Hasher, Sessions: d.Sessions, Attempts: d.Attempts,
		Jobs: d.Jobs, Blobs: d.Blobs, Staging: d.Staging,
		SignupDisabled: d.SignupDisabled, OIDC: d.OIDC, Clock: d.Clock, Log: d.Log,
	}
	if d.Hasher != nil {
		// On error the hash stays empty: Verify then fails fast for unknown
		// emails, which is still correct, only not timing-equalized.
		a.dummyHash, _ = d.Hasher.Hash("archivist-dummy-password-for-unknown-emails")
	}
	return a
}

// SignUp creates a user and returns a session token.
func (a AuthService) SignUp(ctx context.Context, email, password string) (domain.User, string, error) {
	if a.SignupDisabled {
		return domain.User{}, "", ErrSignupDisabled
	}
	email = domain.NormalizeEmail(email)
	if !validEmail(email) {
		return domain.User{}, "", ErrInvalidEmail
	}
	if err := CheckPassword(email, password); err != nil {
		return domain.User{}, "", err
	}
	hash, err := a.Hasher.Hash(password)
	if err != nil {
		return domain.User{}, "", err
	}
	u, err := a.Users.Create(ctx, email, hash)
	if err != nil {
		return domain.User{}, "", err
	}
	token, err := a.startSession(ctx, u.ID, "")
	return u, token, err
}

// logIn verifies credentials and returns a session token; LogInGuarded is its
// exported form. An unknown email costs the same password-hash work as a known
// one (verified against a dummy hash), so response timing does not reveal
// which emails are registered.
//
// A successful login is also when an outdated password hash gets upgraded: the
// plaintext is at hand, so if the hasher says the stored hash NeedsRehash it is
// replaced with a current one. That is best effort — a failure is logged and
// the login succeeds with the old hash still in place.
//
// previousToken ("" = none) is the session the client already holds: the new
// session is created and that one revoked in one atomic step
// (Sessions.CreateReplacing), so a login never leaves the browser's old
// session behind. It is revoked only if it belongs to the user logging in; an
// empty, unknown or foreign token is left alone — a login must not be a way to
// end somebody else's session. A failed login revokes nothing.
func (a AuthService) logIn(ctx context.Context, email, password, previousToken string) (domain.User, string, error) {
	u, ok, err := a.Users.ByEmail(ctx, domain.NormalizeEmail(email))
	if err != nil {
		return domain.User{}, "", err
	}
	hash := u.PasswordHash
	if !ok || hash == "" { // unknown email, or an account that logs in through a provider only
		hash, ok = a.dummyHash, false
	}
	if valid := a.Hasher.Verify(hash, password); !ok || !valid {
		return domain.User{}, "", ErrInvalidCredentials
	}
	if a.Hasher.NeedsRehash(u.PasswordHash) {
		u.PasswordHash = a.upgradeHash(ctx, u, password)
	}
	token, err := a.startSession(ctx, u.ID, previousToken)
	return u, token, err
}

// LogInGuarded is THE login: credentials in, session token out (see logIn for
// the credential check and for previousToken), behind the login throttle.
// clientKey identifies where the attempt comes from (the transport passes the
// client's IP address); it may be empty, which is then one shared client.
// Without an AttemptLimiter (Deps.Attempts nil) nothing is throttled.
//
//  1. At most MaxLoginFailuresPair failures per (account, client) in
//     LoginAttemptWindow, and at most MaxLoginFailuresAccount per account over
//     all clients. Beyond either, the attempt is refused with a
//     *TooManyAttemptsError WITHOUT the password being looked at — so waiting
//     is the only way on, and a refused attempt is not counted again.
//  2. Only failures count. A success resets the pair's counter, not the
//     account's: the owner logging in must not hand a distributed guesser a
//     fresh budget.
//  3. One client can thus lock out only itself; the owner, coming from another
//     address, is refused once the account-wide limit is reached — which
//     clients from many addresses together can do, for as long as they keep
//     at it (a lockout, not a way in).
//  4. An unknown email behaves exactly like a known one — failures are recorded
//     under the hash of the normalised email whether or not a user has it, so
//     the throttle is no account-existence oracle.
//
// Keys are SHA-256 hashes: no email or address is stored. The count and the
// later record are separate steps, so parallel attempts can overshoot a limit
// by their number; the transport's per-IP rate limit bounds that.
func (a AuthService) LogInGuarded(ctx context.Context, email, password, previousToken, clientKey string) (domain.User, string, error) {
	if a.Attempts == nil {
		return a.logIn(ctx, email, password, previousToken)
	}
	normalized := domain.NormalizeEmail(email)
	account := attemptKey("login-account", normalized)
	pair := attemptKey("login-pair", normalized, clientKey)
	now := clockOrNow(a.Clock)
	for _, limit := range []struct {
		key string
		max int
	}{{pair, MaxLoginFailuresPair}, {account, MaxLoginFailuresAccount}} {
		n, oldest, err := a.Attempts.Failures(ctx, limit.key, now, LoginAttemptWindow)
		if err != nil {
			return domain.User{}, "", err
		}
		if n >= limit.max {
			return domain.User{}, "", &TooManyAttemptsError{RetryAfter: max(oldest.Add(LoginAttemptWindow).Sub(now), time.Second)}
		}
	}
	u, token, err := a.logIn(ctx, email, password, previousToken)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		for _, key := range []string{pair, account} {
			if rerr := a.Attempts.RecordFailure(ctx, key, now); rerr != nil {
				return domain.User{}, "", rerr
			}
		}
	case err == nil:
		// Best effort: the login has succeeded and must not fail over this.
		if rerr := a.Attempts.Reset(ctx, pair); rerr != nil {
			a.log().Warn("login throttle not reset", "err", rerr)
		}
	}
	return u, token, err
}

// attemptKey hashes the parts of a throttle key (length-prefixed, so that no
// two different part lists collide) into what the AttemptLimiter stores.
func attemptKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// upgradeHash re-hashes the just-verified password with the hasher's current
// algorithm and stores it. It returns the hash the user has afterwards; on any
// failure that is the old one.
func (a AuthService) upgradeHash(ctx context.Context, u domain.User, password string) string {
	hash, err := a.Hasher.Hash(password)
	if err == nil {
		err = a.Users.UpdatePassword(ctx, u.ID, hash)
	}
	if err != nil {
		a.log().Warn("password hash upgrade failed; keeping the old hash", "user", u.ID, "err", err)
		return u.PasswordHash
	}
	return hash
}

func (a AuthService) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.New(slog.DiscardHandler)
}

// newSessionToken returns an opaque 256-bit token. (crypto/rand.Read never
// fails; it aborts the program if the platform has no entropy source.)
func newSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashSessionToken is what the session store is given instead of the token.
// A fast hash is the right tool here: the token is 256 bits of entropy, not a
// password, so there is nothing to brute-force.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// startSession creates a session for the user and returns its token, which
// exists in the clear only in this return value. previousToken ("" = none) is
// the session it replaces, see logIn.
func (a AuthService) startSession(ctx context.Context, userID, previousToken string) (string, error) {
	token := newSessionToken()
	previous := ""
	if previousToken != "" {
		previous = hashSessionToken(previousToken)
	}
	if err := a.Sessions.CreateReplacing(ctx, hashSessionToken(token), userID, previous, clockOrNow(a.Clock)); err != nil {
		return "", err
	}
	return token, nil
}

// LogOut revokes the session server-side; the token is unusable afterwards.
func (a AuthService) LogOut(ctx context.Context, token string) error {
	return a.Sessions.Revoke(ctx, hashSessionToken(token))
}

// Authenticate resolves a token to a user id.
func (a AuthService) Authenticate(ctx context.Context, token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	return a.Sessions.Lookup(ctx, hashSessionToken(token), clockOrNow(a.Clock))
}

// CurrentUser resolves a token to the full user.
func (a AuthService) CurrentUser(ctx context.Context, token string) (domain.User, bool, error) {
	uid, ok, err := a.Authenticate(ctx, token)
	if err != nil || !ok {
		return domain.User{}, false, err
	}
	return a.Users.ByID(ctx, uid)
}

// ChangePassword verifies the current password, stores the new hash, revokes
// every existing session (stolen tokens die with the old password), and
// returns a fresh token so the caller's session continues. An account that
// has no password yet (created by an OIDC login) sets its first one without a
// current password: the session is the proof.
func (a AuthService) ChangePassword(ctx context.Context, userID, current, next string) (string, error) {
	if err := CheckPassword("", next); err != nil { // before any store access
		return "", err
	}
	u, ok, err := a.Users.ByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrInvalidCredentials
	}
	if err := a.reauthenticate(ctx, u, current); err != nil {
		return "", err
	}
	if err := CheckPassword(u.Email, next); err != nil {
		return "", err
	}
	hash, err := a.Hasher.Hash(next)
	if err != nil {
		return "", err
	}
	if err := a.Users.UpdatePassword(ctx, userID, hash); err != nil {
		return "", err
	}
	if err := a.Sessions.RevokeUser(ctx, userID); err != nil {
		return "", err
	}
	return a.startSession(ctx, userID, "")
}

// DeleteAccount verifies the password, then removes the user and ALL their
// data: staged uploads, blobs (preserved archives + media), and the user row
// (events, accounts, jobs, sessions cascade in the store).
//
// Order, and why:
//
//  1. Refuse with ErrJobBusy while an import is queued or running: a worker
//     would keep writing blobs for the user after they were wiped, leaving
//     private data behind that nothing references any more. Checked first so
//     a refusal has no side effects.
//  2. Revoke sessions — nothing new can be uploaded or reprocessed for the
//     account from here on — and check for active imports once more to close
//     the window between 1 and 2.
//  3. Staged uploads, then blobs. Both are idempotent.
//  4. The user row LAST. It is the only handle to everything above (blob and
//     staging namespaces are keyed by the user id) and what the password is
//     checked against. If any earlier step fails the account still exists, the
//     user can log in again and retry, and the retry redoes the idempotent
//     steps. Deleting the row first would strand undeletable private data on a
//     blob-store failure.
func (a AuthService) DeleteAccount(ctx context.Context, userID, password string) error {
	u, ok, err := a.Users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if err := a.reauthenticate(ctx, u, password); err != nil {
		return err
	}
	return a.deleteUser(ctx, userID)
}

// reauthenticate checks the password a signed-in user gives to confirm a
// sensitive change, under its own throttle (MaxReauthFailures per account
// in LoginAttemptWindow, in the shared store): the session proves who asks,
// not that they know the password, and an unthrottled check would let a
// stolen session guess it. ErrInvalidCredentials or a TooManyAttemptsError.
func (a AuthService) reauthenticate(ctx context.Context, u domain.User, password string) error {
	if a.Attempts == nil {
		if !a.passwordMatches(u, password) {
			return ErrInvalidCredentials
		}
		return nil
	}
	key := attemptKey("reauth", u.ID)
	now := clockOrNow(a.Clock)
	n, oldest, err := a.Attempts.Failures(ctx, key, now, LoginAttemptWindow)
	if err != nil {
		return err
	}
	if n >= MaxReauthFailures {
		return &TooManyAttemptsError{RetryAfter: max(oldest.Add(LoginAttemptWindow).Sub(now), time.Second)}
	}
	if !a.passwordMatches(u, password) {
		if err := a.Attempts.RecordFailure(ctx, key, now); err != nil {
			return err
		}
		return ErrInvalidCredentials
	}
	if err := a.Attempts.Reset(ctx, key); err != nil {
		a.log().Warn("re-authentication throttle not reset", "err", err)
	}
	return nil
}

// deleteUser is DeleteAccount after the password check: steps 1–4 above.
func (a AuthService) deleteUser(ctx context.Context, userID string) error {
	if err := a.refuseWhileImporting(ctx, userID); err != nil {
		return err
	}
	if err := a.Sessions.RevokeUser(ctx, userID); err != nil {
		return err
	}
	if err := a.refuseWhileImporting(ctx, userID); err != nil {
		return err
	}
	if a.Staging != nil {
		if err := a.Staging.RemoveUser(ctx, userID); err != nil {
			return fmt.Errorf("delete staged uploads: %w", err)
		}
	}
	if a.Blobs != nil {
		if err := a.Blobs.DeleteUser(ctx, userID); err != nil {
			return fmt.Errorf("delete blobs: %w", err)
		}
	}
	return a.Users.Delete(ctx, userID)
}

// passwordMatches reports whether password is the user's. An account without
// a password (OIDC login only) has nothing to check against: the caller's
// session stands in for it.
func (a AuthService) passwordMatches(u domain.User, password string) bool {
	return u.PasswordHash == "" || a.Hasher.Verify(u.PasswordHash, password)
}

func (a AuthService) refuseWhileImporting(ctx context.Context, userID string) error {
	// An upload still streaming would become a job — and the worker would
	// write blobs — after the account's data is gone.
	if a.uploadsInFlight != nil && a.uploadsInFlight(userID) > 0 {
		return ErrJobBusy
	}
	if a.Jobs == nil {
		return nil
	}
	usage, err := a.Jobs.Usage(ctx, userID)
	if err != nil {
		return err
	}
	if usage.Active > 0 {
		return ErrJobBusy
	}
	return nil
}
