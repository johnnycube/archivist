package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/crypto"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/app"
)

// fakeIDP is an app.OIDCProvider that hands out fixed claims and records what
// it was asked.
type fakeIDP struct {
	claims   app.OIDCClaims
	err      error
	exchange struct{ code, verifier, nonce, redirect string }
}

func (f *fakeIDP) AuthCodeURL(state, nonce, verifier, redirectURL string) string {
	return "https://idp.example/auth?state=" + state + "&nonce=" + nonce + "&v=" + verifier + "&r=" + redirectURL
}

func (f *fakeIDP) Exchange(_ context.Context, code, verifier, nonce, redirectURL string) (app.OIDCClaims, error) {
	f.exchange.code, f.exchange.verifier, f.exchange.nonce, f.exchange.redirect = code, verifier, nonce, redirectURL
	return f.claims, f.err
}

func newOIDCApp(t *testing.T, idp *fakeIDP, cfg app.OIDCProviderConfig) (*app.App, *memory.Store) {
	t.Helper()
	store := memory.NewStore(memory.Options{SessionTTL: time.Hour})
	cfg.ID, cfg.Name, cfg.Provider = "idp", "Example IdP", idp
	return app.New(app.Deps{
		Users: store.Users, Sessions: store.Sessions, Hasher: crypto.Hasher{}, OIDC: []app.OIDCProviderConfig{cfg},
	}), store
}

// start runs OIDCStart and returns the state the browser would carry.
func start(t *testing.T, a *app.App) app.OIDCState {
	t.Helper()
	u, st, err := a.Auth.OIDCStart("idp", "https://app.example/cb")
	if err != nil || u == "" || st.State == "" || st.Nonce == "" || st.Verifier == "" {
		t.Fatalf("OIDCStart = %q, %+v, %v", u, st, err)
	}
	return st
}

func TestOIDCFirstLoginCreatesAccountAndSession(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "New@Example.com", EmailVerified: true}}
	a, store := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: true, LinkByEmail: true})
	st := start(t, a)
	u, token, err := a.Auth.OIDCFinish(context.Background(), st, st.State, "code-1", "https://app.example/cb", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "new@example.com" || u.PasswordHash != "" || !u.EmailVerified {
		t.Errorf("user = %+v", u)
	}
	if idp.exchange.code != "code-1" || idp.exchange.verifier != st.Verifier || idp.exchange.nonce != st.Nonce || idp.exchange.redirect != "https://app.example/cb" {
		t.Errorf("exchange got %+v", idp.exchange)
	}
	if got, ok, _ := a.Auth.CurrentUser(context.Background(), token); !ok || got.ID != u.ID {
		t.Errorf("session does not resolve to the user: %+v, %v", got, ok)
	}
	if linked, ok, _ := store.Users.ByIdentity(context.Background(), "idp", "s1"); !ok || linked.ID != u.ID {
		t.Error("identity not linked to the new user")
	}
	// Second login: the link is used, no new account.
	again, _, err := a.Auth.OIDCFinish(context.Background(), st, st.State, "code-2", "https://app.example/cb", "")
	if err != nil || again.ID != u.ID {
		t.Errorf("second login = %+v, %v; want the same user", again, err)
	}
	// The account has no password: a password login must fail, and setting a
	// first password needs no current one.
	if _, _, err := a.Auth.LogInGuarded(context.Background(), u.Email, "", "", ""); !errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("password login to a password-less account = %v; want ErrInvalidCredentials", err)
	}
	if _, err := a.Auth.ChangePassword(context.Background(), u.ID, "", "a-first-password"); err != nil {
		t.Errorf("setting a first password = %v", err)
	}
	if _, _, err := a.Auth.LogInGuarded(context.Background(), u.Email, "a-first-password", "", ""); err != nil {
		t.Errorf("password login after setting one = %v", err)
	}
}

// An OIDC login is attached to an existing account by email only when the
// provider verified the address AND the account's own email is verified.
// A password signup proves nothing about its address: linking to it would
// hand the victim's first provider login to whoever registered the address
// (account pre-hijacking).
func TestOIDCLinksByEmailOnlyToVerifiedAccounts(t *testing.T) {
	for _, tc := range []struct {
		name            string
		claimVerified   bool
		accountVerified bool
		link            bool
		wantErr         error
	}{
		{"verified claim, verified account: links", true, true, true, nil},
		{"password signup is never linked by email", true, false, true, app.ErrOIDCEmailInUse},
		{"unverified claim does not link", false, true, true, app.ErrOIDCEmailUnverified},
		{"linking disabled", true, true, false, app.ErrOIDCEmailInUse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "me@example.com", EmailVerified: tc.claimVerified}}
			a, store := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: true, LinkByEmail: tc.link})
			existing, _, err := a.Auth.SignUp(ctx, "me@example.com", "password123")
			if err != nil {
				t.Fatal(err)
			}
			if tc.accountVerified {
				if err := store.Users.MarkEmailVerified(ctx, existing.ID); err != nil {
					t.Fatal(err)
				}
			}
			st := start(t, a)
			u, _, err := a.Auth.OIDCFinish(ctx, st, st.State, "c", "https://app.example/cb", "")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			_, linked, _ := store.Users.ByIdentity(ctx, "idp", "s1")
			if tc.wantErr == nil && (u.ID != existing.ID || !linked) {
				t.Errorf("logged in as %s (linked %v), want the existing account %s", u.ID, linked, existing.ID)
			}
			if tc.wantErr != nil && linked {
				t.Error("identity linked although the login was refused")
			}
		})
	}
}

func TestOIDCAutoSignupNeedsAVerifiedEmail(t *testing.T) {
	ctx := context.Background()
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "new@example.com"}}
	a, store := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: true, LinkByEmail: true})
	st := start(t, a)
	if _, _, err := a.Auth.OIDCFinish(ctx, st, st.State, "c", "r", ""); !errors.Is(err, app.ErrOIDCEmailUnverified) {
		t.Fatalf("unverified email = %v; want ErrOIDCEmailUnverified", err)
	}
	if _, ok, _ := store.Users.ByEmail(ctx, "new@example.com"); ok {
		t.Error("an account was created from an unverified email")
	}
	idp.claims.EmailVerified = true
	u, _, err := a.Auth.OIDCFinish(ctx, st, st.State, "c", "r", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.Users.ByID(ctx, u.ID); !got.EmailVerified || !u.EmailVerified {
		t.Errorf("account created from a verified email is not marked verified: %+v", got)
	}
}

// startLink runs OIDCStartLink for userID.
func startLink(t *testing.T, a *app.App, userID string) app.OIDCState {
	t.Helper()
	u, st, err := a.Auth.OIDCStartLink("idp", userID, "https://app.example/cb")
	if err != nil || u == "" || st.LinkUserID != userID || st.State == "" {
		t.Fatalf("OIDCStartLink = %q, %+v, %v", u, st, err)
	}
	return st
}

func TestOIDCLinkFromSession(t *testing.T) {
	ctx := context.Background()
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "ME@example.com", EmailVerified: true}}
	a, store := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: false, LinkByEmail: false})
	me, _, err := a.Auth.SignUp(ctx, "me@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := a.Auth.SignUp(ctx, "other@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}

	st := startLink(t, a, me.ID)
	// The attempt is bound to the user it was started for.
	if err := a.Auth.OIDCFinishLink(ctx, st, st.State, "c", "r", other.ID); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("finishing as another user = %v; want ErrOIDCState", err)
	}
	if err := a.Auth.OIDCFinishLink(ctx, st, st.State, "c", "r", ""); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("finishing without a session = %v; want ErrOIDCState", err)
	}
	if err := a.Auth.OIDCFinishLink(ctx, st, "forged", "c", "r", me.ID); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("wrong state = %v; want ErrOIDCState", err)
	}
	// A link attempt never logs in; a login attempt never links.
	if _, _, err := a.Auth.OIDCFinish(ctx, st, st.State, "c", "r", ""); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("link attempt finished as a login = %v; want ErrOIDCState", err)
	}
	login := start(t, a)
	if err := a.Auth.OIDCFinishLink(ctx, login, login.State, "c", "r", me.ID); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("login attempt finished as a link = %v; want ErrOIDCState", err)
	}
	if _, _, err := a.Auth.OIDCStartLink("idp", "", "r"); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("OIDCStartLink without a user = %v; want ErrOIDCState", err)
	}

	if err := a.Auth.OIDCFinishLink(ctx, st, st.State, "c", "r", me.ID); err != nil {
		t.Fatal(err)
	}
	if u, ok, _ := store.Users.ByIdentity(ctx, "idp", "s1"); !ok || u.ID != me.ID || !u.EmailVerified {
		t.Errorf("after linking: ByIdentity = %+v, %v; want me, email verified", u, ok)
	}
	if got, err := a.Auth.LinkedProviders(ctx, me.ID); err != nil || len(got) != 1 || got[0] != "idp" {
		t.Errorf("LinkedProviders = %v, %v", got, err)
	}
	// The provider now logs me in, although email linking and signup are off.
	login = start(t, a)
	if u, _, err := a.Auth.OIDCFinish(ctx, login, login.State, "c", "r", ""); err != nil || u.ID != me.ID {
		t.Errorf("login through the linked provider = %+v, %v", u, err)
	}
	// Linking the same identity again is fine; to another user it is not.
	if err := a.Auth.OIDCFinishLink(ctx, st, st.State, "c", "r", me.ID); err != nil {
		t.Errorf("relinking = %v", err)
	}
	st2 := startLink(t, a, other.ID)
	if err := a.Auth.OIDCFinishLink(ctx, st2, st2.State, "c", "r", other.ID); !errors.Is(err, app.ErrIdentityTaken) {
		t.Errorf("linking my identity to another user = %v; want ErrIdentityTaken", err)
	}
	// A provider email that is not the account's does not verify the account.
	idp.claims = app.OIDCClaims{Subject: "s2", Email: "someone-else@example.com", EmailVerified: true}
	st2 = startLink(t, a, other.ID)
	if err := a.Auth.OIDCFinishLink(ctx, st2, st2.State, "c", "r", other.ID); err != nil {
		t.Fatal(err)
	}
	if u, _, _ := store.Users.ByID(ctx, other.ID); u.EmailVerified {
		t.Error("a different provider email verified the account's email")
	}
}

func TestOIDCRefusals(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "me@example.com", EmailVerified: true}}
	a, _ := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: false})
	st := start(t, a)
	ctx := context.Background()
	if _, _, err := a.Auth.OIDCFinish(ctx, st, st.State, "c", "r", ""); !errors.Is(err, app.ErrOIDCNoAccount) {
		t.Errorf("auto-signup off = %v; want ErrOIDCNoAccount", err)
	}
	if _, _, err := a.Auth.OIDCFinish(ctx, st, "other-state", "c", "r", ""); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("wrong state = %v; want ErrOIDCState", err)
	}
	if _, _, err := a.Auth.OIDCFinish(ctx, app.OIDCState{Provider: "idp"}, "", "c", "r", ""); !errors.Is(err, app.ErrOIDCState) {
		t.Errorf("empty state = %v; want ErrOIDCState", err)
	}
	if _, _, err := a.Auth.OIDCStart("nope", "r"); !errors.Is(err, app.ErrOIDCUnknownProvider) {
		t.Errorf("unknown provider = %v", err)
	}
	idp.err = errors.New("invalid_grant")
	if _, _, err := a.Auth.OIDCFinish(ctx, st, st.State, "c", "r", ""); err == nil || errors.Is(err, app.ErrOIDCNoAccount) {
		t.Errorf("provider failure = %v; want it reported", err)
	}
	idp.err = nil
	idp.claims = app.OIDCClaims{Subject: "s2"}
	b, _ := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: true})
	st = start(t, b)
	if _, _, err := b.Auth.OIDCFinish(ctx, st, st.State, "c", "r", ""); !errors.Is(err, app.ErrOIDCNoEmail) {
		t.Errorf("no email = %v; want ErrOIDCNoEmail", err)
	}
}

func TestOIDCConfiguredRedirectWinsAndProvidersAreListed(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "me@example.com", EmailVerified: true}}
	a, _ := newOIDCApp(t, idp, app.OIDCProviderConfig{AutoSignup: true, RedirectURL: "https://public.example/api/auth/oidc/idp/callback"})
	u, st, err := a.Auth.OIDCStart("idp", "http://internal/cb")
	if err != nil || !contains(u, "r=https://public.example/api/auth/oidc/idp/callback") {
		t.Errorf("auth url = %q, %v; want the configured redirect", u, err)
	}
	if _, _, err := a.Auth.OIDCFinish(context.Background(), st, st.State, "c", "http://internal/cb", ""); err != nil {
		t.Fatal(err)
	}
	if idp.exchange.redirect != "https://public.example/api/auth/oidc/idp/callback" {
		t.Errorf("exchange redirect = %q", idp.exchange.redirect)
	}
	if got := a.Auth.OIDCProviders(); len(got) != 1 || got[0].ID != "idp" || got[0].Name != "Example IdP" {
		t.Errorf("OIDCProviders = %+v", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
