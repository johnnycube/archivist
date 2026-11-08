package rest

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/johnnycube/archivist/internal/app"
)

// OIDC login over HTTP. The browser is sent to the provider from
// /api/auth/oidc/{id}/start and comes back to /api/auth/oidc/{id}/callback;
// in between, the attempt's secrets (state, nonce, PKCE verifier) live in a
// short-lived cookie, so a callback is accepted only by the browser that
// started it. Failures end on the login page with ?auth_error=<code>.
//
// Linking a provider to the signed-in account starts with POST
// /api/auth/oidc/{id}/link (a session and the CSRF check, so no other site can
// start a link in the victim's browser), which answers {"url": ...} for the
// SPA to navigate to; the same callback finishes it for the session's user and
// returns to /account?linked=<id> or /account?link_error=<code>.

// oidcCookieName holds one login attempt; __Host- when Secure, like the
// session cookie, so no subdomain can plant one.
const oidcCookieName = "archivist_oidc"

// oidcAttemptTTL is how long a login attempt may take at the provider.
const oidcAttemptTTL = 10 * time.Minute

// oidcAttempt is the cookie's content.
type oidcAttempt struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	// Link is the user a link attempt was started for ("" = a login). The
	// cookie is not signed: the callback only compares it with the session.
	Link string `json:"l,omitempty"`
}

type oidcProviderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toOIDCProviderDTOs(in []app.OIDCProviderInfo) []oidcProviderDTO {
	out := make([]oidcProviderDTO, 0, len(in))
	for _, p := range in {
		out = append(out, oidcProviderDTO{ID: p.ID, Name: p.Name})
	}
	return out
}

// authOptionsDTO is what the login page offers: password signup (when the
// operator opened it) and the identity providers.
type authOptionsDTO struct {
	Signup    bool              `json:"signup"`
	Providers []oidcProviderDTO `json:"providers"`
}

func (s *Server) handleAuthOptions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, authOptionsDTO{Signup: s.App.Auth.SignupOpen(), Providers: toOIDCProviderDTOs(s.App.Auth.OIDCProviders())})
}

func (s *Server) handleOIDCProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": toOIDCProviderDTOs(s.App.Auth.OIDCProviders())})
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	authURL, st, err := s.App.Auth.OIDCStart(id, s.oidcRedirectURL(r, id))
	if errors.Is(err, app.ErrOIDCUnknownProvider) {
		httpError(w, http.StatusNotFound, "unknown identity provider")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !s.setOIDCAttempt(w, r, st) {
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// setOIDCAttempt stores the attempt's secrets in the attempt cookie; false
// means the response has been written (an error).
func (s *Server) setOIDCAttempt(w http.ResponseWriter, r *http.Request, st app.OIDCState) bool {
	raw, err := json.Marshal(oidcAttempt{Provider: st.Provider, State: st.State, Nonce: st.Nonce, Verifier: st.Verifier, Link: st.LinkUserID})
	if err != nil {
		s.serverError(w, r, err)
		return false
	}
	http.SetCookie(w, s.oidcCookie(base64.RawURLEncoding.EncodeToString(raw), int(oidcAttemptTTL/time.Second)))
	return true
}

// finishOIDCLink completes a link attempt for the session's user and returns
// to the account page.
func (s *Server) finishOIDCLink(w http.ResponseWriter, r *http.Request, st app.OIDCState) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.oidcLinkFail(w, r, "denied", e+": "+q.Get("error_description"))
		return
	}
	uid := ""
	if token := s.sessionToken(r); token != "" {
		id, ok, err := s.App.Auth.Authenticate(r.Context(), token)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if ok {
			uid = id
		}
	}
	err := s.App.Auth.OIDCFinishLink(r.Context(), st, q.Get("state"), q.Get("code"), s.oidcRedirectURL(r, st.Provider), uid)
	switch {
	case err == nil:
		s.logger().Info("oidc identity linked", "provider", st.Provider, "user", uid)
		http.Redirect(w, r, "/account?linked="+url.QueryEscape(st.Provider), http.StatusFound)
	case errors.Is(err, app.ErrIdentityTaken):
		s.oidcLinkFail(w, r, "identity_taken", err.Error())
	case errors.Is(err, app.ErrOIDCState), errors.Is(err, app.ErrOIDCUnknownProvider):
		s.oidcLinkFail(w, r, "expired", err.Error())
	case errors.Is(err, app.ErrNotFound):
		s.serverError(w, r, err)
	default:
		s.oidcLinkFail(w, r, "failed", err.Error())
	}
}

// oidcLinkFail returns to the account page with a reason code; the detail is
// logged, never shown.
func (s *Server) oidcLinkFail(w http.ResponseWriter, r *http.Request, code, detail string) {
	s.logger().Info("oidc link failed", "provider", r.PathValue("id"), "reason", code, "detail", detail)
	http.Redirect(w, r, "/account?link_error="+url.QueryEscape(code), http.StatusFound)
}

// handleOIDCLinkStart begins attaching provider id to the signed-in account:
// it sets the attempt cookie and answers the provider URL to navigate to.
func (s *Server) handleOIDCLinkStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	uid := r.Context().Value(userKey).(string)
	authURL, st, err := s.App.Auth.OIDCStartLink(id, uid, s.oidcRedirectURL(r, id))
	if errors.Is(err, app.ErrOIDCUnknownProvider) {
		httpError(w, http.StatusNotFound, "unknown identity provider")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !s.setOIDCAttempt(w, r, st) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, ok := s.oidcAttempt(r)
	http.SetCookie(w, s.oidcCookie("", -1)) // one callback per attempt
	if !ok || st.Provider != id {
		s.oidcFail(w, r, "expired", "no matching login attempt")
		return
	}
	q := r.URL.Query()
	if st.LinkUserID != "" {
		s.finishOIDCLink(w, r, st)
		return
	}
	if e := q.Get("error"); e != "" {
		s.oidcFail(w, r, "denied", e+": "+q.Get("error_description"))
		return
	}
	previous := s.sessionToken(r)
	u, token, err := s.App.Auth.OIDCFinish(r.Context(), st, q.Get("state"), q.Get("code"), s.oidcRedirectURL(r, id), previous)
	if err != nil {
		if code, known := oidcErrorCode(err); known {
			s.oidcFail(w, r, code, err.Error())
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.logger().Info("oidc login", "provider", id, "user", u.ID)
	s.setSession(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcErrorCode maps a failed login to the code the login page explains. A
// provider or network failure is "failed"; only unexpected errors (store
// failures) are not known here and become a 500.
func oidcErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, app.ErrOIDCState):
		return "expired", true
	case errors.Is(err, app.ErrOIDCNoAccount):
		return "no_account", true
	case errors.Is(err, app.ErrOIDCNoEmail):
		return "no_email", true
	case errors.Is(err, app.ErrOIDCEmailInUse):
		return "email_in_use", true
	case errors.Is(err, app.ErrOIDCEmailUnverified):
		return "email_unverified", true
	case errors.Is(err, app.ErrOIDCUnknownProvider):
		return "expired", true
	case errors.Is(err, app.ErrNotFound), errors.Is(err, app.ErrIdentityTaken):
		return "", false
	}
	return "failed", true
}

// oidcFail sends the browser back to the login page with a reason code; the
// detail is logged, never shown (it may quote the provider).
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, code, detail string) {
	s.logger().Info("oidc login failed", "provider", r.PathValue("id"), "reason", code, "detail", detail)
	http.Redirect(w, r, "/?auth_error="+url.QueryEscape(code), http.StatusFound)
}

// oidcAttempt reads the attempt cookie; any defect counts as no attempt.
func (s *Server) oidcAttempt(r *http.Request) (app.OIDCState, bool) {
	c, err := r.Cookie(s.oidcCookieName())
	if err != nil || c.Value == "" {
		return app.OIDCState{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return app.OIDCState{}, false
	}
	var a oidcAttempt
	if err := json.Unmarshal(raw, &a); err != nil || a.State == "" || a.Verifier == "" || a.Nonce == "" {
		return app.OIDCState{}, false
	}
	return app.OIDCState{Provider: a.Provider, State: a.State, Nonce: a.Nonce, Verifier: a.Verifier, LinkUserID: a.Link}, true
}

func (s *Server) oidcCookieName() string {
	if s.Secure {
		return "__Host-" + oidcCookieName
	}
	return oidcCookieName
}

// oidcCookie is the attempt cookie: HttpOnly, and SameSite=Lax so that the
// provider's redirect back — a top-level navigation — still carries it.
func (s *Server) oidcCookie(value string, maxAge int) *http.Cookie {
	// Secure follows COOKIE_SECURE exactly as the session cookie does.
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure
	return &http.Cookie{ //nolint:gosec // Secure is COOKIE_SECURE (off only for plain-HTTP dev); HttpOnly and SameSite are set
		Name: s.oidcCookieName(), Value: value, Path: "/", HttpOnly: true,
		Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	}
}

// oidcRedirectURL is this server's callback for provider id as the request
// reached it. Behind a reverse proxy the scheme comes from X-Forwarded-Proto
// when the proxy is trusted (TRUSTED_PROXIES); otherwise Secure cookies imply
// https. A provider's configured RedirectURL overrides this in the use case.
func (s *Server) oidcRedirectURL(r *http.Request, id string) string {
	scheme := "http"
	if r.TLS != nil || s.Secure {
		scheme = "https"
	}
	if s.TrustedProxies > 0 {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host + "/api/auth/oidc/" + url.PathEscape(id) + "/callback"
}
