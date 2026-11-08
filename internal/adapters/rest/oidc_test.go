package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/app"
)

// fakeIDP stands in for an identity provider: it accepts the code "good" when
// the verifier and nonce are the ones it saw in the authorization URL.
type fakeIDP struct {
	nonce, verifier string
	claims          app.OIDCClaims
}

func (f *fakeIDP) AuthCodeURL(state, nonce, verifier, redirectURL string) string {
	f.nonce, f.verifier = nonce, verifier
	return "https://idp.example/auth?" + url.Values{"state": {state}, "redirect_uri": {redirectURL}}.Encode()
}

func (f *fakeIDP) Exchange(_ context.Context, code, verifier, nonce, _ string) (app.OIDCClaims, error) {
	if code != "good" || verifier != f.verifier || nonce != f.nonce {
		return app.OIDCClaims{}, context.DeadlineExceeded // any provider error
	}
	return f.claims, nil
}

func oidcHandler(t *testing.T, idp *fakeIDP, autoSignup bool) http.Handler {
	t.Helper()
	deps := newDeps(t)
	deps.OIDC = []app.OIDCProviderConfig{{ID: "idp", Name: "Example IdP", Provider: idp, AutoSignup: autoSignup, LinkByEmail: true}}
	return rest.New(app.New(deps), false).Handler()
}

// startLogin hits /start and returns the attempt cookie and the state the
// provider would echo back.
func startLogin(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/idp/start", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Host != "idp.example" {
		t.Fatalf("Location = %q, %v", rec.Header().Get("Location"), err)
	}
	if got := loc.Query().Get("redirect_uri"); got != "http://example.com/api/auth/oidc/idp/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	var attempt *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "archivist_oidc" {
			attempt = c
		}
	}
	if attempt == nil || attempt.Value == "" || !attempt.HttpOnly || attempt.SameSite != http.SameSiteLaxMode || attempt.MaxAge <= 0 {
		t.Fatalf("attempt cookie = %+v", attempt)
	}
	return attempt, loc.Query().Get("state")
}

func callback(h http.Handler, attempt *http.Cookie, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/idp/callback?"+query, nil)
	if attempt != nil {
		req.AddCookie(attempt)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestOIDCLoginRoundTrip(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "me@example.com", EmailVerified: true}}
	h := oidcHandler(t, idp, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc", nil))
	var list struct{ Providers []struct{ ID, Name string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Providers) != 1 || list.Providers[0].ID != "idp" || list.Providers[0].Name != "Example IdP" {
		t.Fatalf("providers = %s, %v", rec.Body.String(), err)
	}

	attempt, state := startLogin(t, h)
	rec = callback(h, attempt, url.Values{"state": {state}, "code": {"good"}}.Encode())
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case rest.CookieName:
			session = c
		case "archivist_oidc":
			if c.MaxAge >= 0 {
				t.Error("attempt cookie not cleared after the callback")
			}
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("no session cookie after the callback")
	}
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/me", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var me struct {
		Email       string `json:"email"`
		HasPassword bool   `json:"has_password"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); rec.Code != 200 || err != nil || me.Email != "me@example.com" || me.HasPassword {
		t.Errorf("me = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
}

func TestOIDCCallbackRefusals(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "me@example.com", EmailVerified: true}}
	h := oidcHandler(t, idp, false) // no auto-signup
	attempt, state := startLogin(t, h)
	for _, tc := range []struct {
		name    string
		cookie  *http.Cookie
		query   string
		wantErr string
	}{
		{"no attempt cookie", nil, "state=" + state + "&code=good", "expired"},
		{"wrong state", attempt, "state=other&code=good", "expired"},
		{"provider refused", attempt, "error=access_denied&error_description=nope", "denied"},
		{"bad code", attempt, "state=" + state + "&code=bad", "failed"},
		{"no account", attempt, "state=" + state + "&code=good", "no_account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callback(h, tc.cookie, tc.query)
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/?auth_error="+tc.wantErr {
				t.Errorf("callback = %d %q, want a redirect with auth_error=%s", rec.Code, rec.Header().Get("Location"), tc.wantErr)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == rest.CookieName && c.Value != "" {
					t.Error("a session cookie was set on a failed login")
				}
			}
		})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/nope/start", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("start with an unknown provider = %d", rec.Code)
	}
}

func TestOIDCRedirectURLFollowsTrustedProxy(t *testing.T) {
	idp := &fakeIDP{}
	deps := newDeps(t)
	deps.OIDC = []app.OIDCProviderConfig{{ID: "idp", Name: "IdP", Provider: idp}}
	srv := rest.New(app.New(deps), false)
	srv.TrustedProxies = 1
	h := srv.Handler()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/idp/start", nil)
	req.Host = "archive.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if got := loc.Query().Get("redirect_uri"); got != "https://archive.example/api/auth/oidc/idp/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
}

// startLink POSTs /link with the session and returns the attempt cookie and
// the state the provider would echo back.
func startLink(t *testing.T, h http.Handler, session *http.Cookie) (*http.Cookie, string) {
	t.Helper()
	rec := request(h, "POST", "/api/auth/oidc/idp/link", "", nil, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("link start = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct{ URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	loc, err := url.Parse(body.URL)
	if err != nil || loc.Host != "idp.example" {
		t.Fatalf("url = %q, %v", body.URL, err)
	}
	attempt := cookieNamed(rec, "archivist_oidc")
	if attempt == nil || attempt.Value == "" {
		t.Fatal("no attempt cookie")
	}
	return attempt, loc.Query().Get("state")
}

func TestOIDCLinkFromAccountPage(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "me@x.com", EmailVerified: true}}
	h := oidcHandler(t, idp, false)
	session := signup(t, h) // me@x.com, a password account

	if rec := request(h, "POST", "/api/auth/oidc/idp/link", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("link start without a session = %d; want 401", rec.Code)
	}
	if rec := request(h, "POST", "/api/auth/oidc/idp/link", "", map[string]string{"Sec-Fetch-Site": "cross-site"}, session); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site link start = %d; want 403", rec.Code)
	}
	if rec := request(h, "POST", "/api/auth/oidc/nope/link", "", nil, session); rec.Code != http.StatusNotFound {
		t.Errorf("link start with an unknown provider = %d; want 404", rec.Code)
	}

	// Before linking, the provider login is refused: the password account's
	// email was never verified, so it is not linked by email.
	attempt, state := startLogin(t, h)
	if rec := callback(h, attempt, url.Values{"state": {state}, "code": {"good"}}.Encode()); rec.Header().Get("Location") != "/?auth_error=email_in_use" {
		t.Fatalf("provider login before linking = %q; want email_in_use", rec.Header().Get("Location"))
	}

	// A link callback without the session that started it does not link.
	attempt, state = startLink(t, h, session)
	if rec := callback(h, attempt, url.Values{"state": {state}, "code": {"good"}}.Encode()); rec.Header().Get("Location") != "/account?link_error=expired" {
		t.Errorf("link callback without a session = %q; want link_error=expired", rec.Header().Get("Location"))
	}
	attempt, state = startLink(t, h, session)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/idp/callback?"+url.Values{"state": {state}, "code": {"good"}}.Encode(), nil)
	req.AddCookie(attempt)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/account?linked=idp" {
		t.Fatalf("link callback = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := cookieNamed(rec, rest.CookieName); c != nil && c.Value != "" {
		t.Error("linking replaced the session")
	}
	var me struct {
		Linked []string `json:"linked_providers"`
	}
	if rec := request(h, "GET", "/api/auth/me", "", nil, session); json.Unmarshal(rec.Body.Bytes(), &me) != nil || len(me.Linked) != 1 || me.Linked[0] != "idp" {
		t.Errorf("me after linking = %s", rec.Body.String())
	}
	// Now the provider logs in to this account.
	attempt, state = startLogin(t, h)
	if rec := callback(h, attempt, url.Values{"state": {state}, "code": {"good"}}.Encode()); rec.Header().Get("Location") != "/" {
		t.Errorf("provider login after linking = %q; want /", rec.Header().Get("Location"))
	}
}

func TestOIDCLinkIdentityOfAnotherAccount(t *testing.T) {
	idp := &fakeIDP{claims: app.OIDCClaims{Subject: "s1", Email: "owner@x.com", EmailVerified: true}}
	h := oidcHandler(t, idp, true)
	// The identity's owner: created by a provider login.
	attempt, state := startLogin(t, h)
	if rec := callback(h, attempt, url.Values{"state": {state}, "code": {"good"}}.Encode()); rec.Header().Get("Location") != "/" {
		t.Fatalf("owner login = %q", rec.Header().Get("Location"))
	}
	session := signup(t, h) // me@x.com
	attempt, state = startLink(t, h, session)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/idp/callback?"+url.Values{"state": {state}, "code": {"good"}}.Encode(), nil)
	req.AddCookie(attempt)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Location") != "/account?link_error=identity_taken" {
		t.Errorf("linking another account's identity = %q; want link_error=identity_taken", rec.Header().Get("Location"))
	}
}

func TestOIDCLinkRefusedByProvider(t *testing.T) {
	h := oidcHandler(t, &fakeIDP{}, false)
	session := signup(t, h)
	attempt, _ := startLink(t, h, session)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/auth/oidc/idp/callback?error=access_denied", nil)
	req.AddCookie(attempt)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Location") != "/account?link_error=denied" {
		t.Errorf("refused link = %q; want link_error=denied", rec.Header().Get("Location"))
	}
}

func TestAuthOptionsTellTheLoginPageWhatToOffer(t *testing.T) {
	for _, closed := range []bool{false, true} {
		deps := newDeps(t)
		deps.SignupDisabled = closed
		deps.OIDC = []app.OIDCProviderConfig{{ID: "idp", Name: "Example IdP", Provider: &fakeIDP{}}}
		h := rest.New(app.New(deps), false).Handler()
		rec := request(h, "GET", "/api/auth/options", "", nil)
		var got struct {
			Signup    bool
			Providers []struct{ ID, Name string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil {
			t.Fatalf("options = %d %s", rec.Code, rec.Body.String())
		}
		if got.Signup == closed || len(got.Providers) != 1 || got.Providers[0].ID != "idp" {
			t.Errorf("signup closed %v: options = %+v", closed, got)
		}
	}
}
