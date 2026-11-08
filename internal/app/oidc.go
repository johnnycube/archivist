package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/johnnycube/archivist/internal/domain"
)

// OIDC login (OpenID Connect, authorization-code flow with PKCE).
//
// The transport drives it in two steps. OIDCStart makes the authorization URL
// and the attempt's secrets (state, nonce, PKCE verifier), which the browser
// carries in a short-lived cookie; OIDCFinish redeems the code the provider
// sent back and maps the asserted identity to a user. The protocol itself is
// behind the OIDCProvider port (adapters/oidc): this package sees claims, not
// tokens.

// OIDCClaims is what a provider asserts about the person who just logged in.
type OIDCClaims struct {
	// Subject is the provider's stable id for the person; (provider, subject)
	// is the identity linked to a user.
	Subject string
	Email   string
	// EmailVerified is the provider's own word that Email belongs to the
	// person. Only a verified email may attach a login to an existing account.
	EmailVerified bool
	Name          string
}

// OIDCProvider is the driven port to one OpenID Connect provider.
type OIDCProvider interface {
	// AuthCodeURL is where the browser is sent to log in. state comes back as
	// a query parameter and nonce inside the ID token; verifier is the PKCE
	// secret whose challenge goes into the URL.
	AuthCodeURL(state, nonce, verifier, redirectURL string) string
	// Exchange redeems the authorization code, verifies the ID token
	// (signature, audience, expiry, nonce) and returns its claims.
	Exchange(ctx context.Context, code, verifier, nonce, redirectURL string) (OIDCClaims, error)
}

// OIDCProviderConfig registers one provider with the AuthService.
type OIDCProviderConfig struct {
	// ID is the provider's short name in URLs and configuration ([a-z0-9-]).
	ID string
	// Name is shown on the login page ("Continue with <Name>").
	Name     string
	Provider OIDCProvider
	// RedirectURL is this server's callback as registered with the provider.
	// Empty = the URL the transport derives from the request.
	RedirectURL string
	// AutoSignup creates an account on a first login that matches nothing.
	AutoSignup bool
	// LinkByEmail attaches a first login to the existing account with the
	// same email, provided the provider says the email is verified AND the
	// account's own email is verified (domain.User.EmailVerified): a password
	// signup proves nothing about its address, so linking to it would hand
	// the victim's login to whoever registered the address first.
	LinkByEmail bool
}

// OIDCProviderInfo is what the login page needs to offer a provider.
type OIDCProviderInfo struct {
	ID, Name string
}

// OIDCState is one login attempt's secrets: made by OIDCStart, handed back
// unchanged to OIDCFinish by the transport (which keeps them in a cookie).
type OIDCState struct {
	Provider string
	State    string
	Nonce    string
	Verifier string
	// LinkUserID is set when the attempt attaches the provider to the
	// signed-in user (OIDCStartLink) instead of logging in. The transport
	// keeps it in an unsigned cookie, so it is only ever COMPARED with the
	// session's user, never trusted on its own.
	LinkUserID string
}

var (
	// ErrOIDCUnknownProvider is returned for a provider id that is not configured.
	ErrOIDCUnknownProvider = errors.New("unknown identity provider")
	// ErrOIDCState is returned when the callback does not belong to the attempt
	// the browser started (missing, expired or mismatching state).
	ErrOIDCState = errors.New("login attempt does not match")
	// ErrOIDCNoAccount is returned when nothing matches the identity and the
	// provider may not create accounts.
	ErrOIDCNoAccount = errors.New("no account for this login")
	// ErrOIDCNoEmail is returned when an account would be created but the
	// provider supplied no email address, which every account needs.
	ErrOIDCNoEmail = errors.New("the identity provider did not supply an email address")
	// ErrOIDCEmailInUse is returned when an account with the identity's email
	// exists but may not be linked automatically: email linking is off, or
	// the account's email was never verified. Its owner logs in with the
	// password and links the provider from the account page (OIDCStartLink).
	ErrOIDCEmailInUse = errors.New("an account with this email already exists")
	// ErrOIDCEmailUnverified is returned when an account would be created
	// from an email the provider has not verified.
	ErrOIDCEmailUnverified = errors.New("the identity provider has not verified this email address")
)

// SignupOpen reports whether password signup is open (ALLOW_SIGNUP).
func (a AuthService) SignupOpen() bool { return !a.SignupDisabled }

// OIDCProviders lists the configured providers in configuration order.
func (a AuthService) OIDCProviders() []OIDCProviderInfo {
	out := make([]OIDCProviderInfo, 0, len(a.OIDC))
	for _, p := range a.OIDC {
		out = append(out, OIDCProviderInfo{ID: p.ID, Name: p.Name})
	}
	return out
}

func (a AuthService) oidcProvider(id string) (OIDCProviderConfig, bool) {
	for _, p := range a.OIDC {
		if p.ID == id {
			return p, true
		}
	}
	return OIDCProviderConfig{}, false
}

// OIDCStart begins a login with provider id: the URL to send the browser to,
// and the attempt's secrets, which the transport must hand back unchanged to
// OIDCFinish. redirectURL is this server's callback as the transport derives
// it; a configured RedirectURL wins over it.
func (a AuthService) OIDCStart(id, redirectURL string) (string, OIDCState, error) {
	p, ok := a.oidcProvider(id)
	if !ok {
		return "", OIDCState{}, ErrOIDCUnknownProvider
	}
	st := OIDCState{Provider: id, State: newSessionToken(), Nonce: newSessionToken(), Verifier: newSessionToken()}
	return p.Provider.AuthCodeURL(st.State, st.Nonce, st.Verifier, p.redirect(redirectURL)), st, nil
}

// OIDCStartLink begins attaching provider id to the signed-in user userID;
// it is OIDCStart with the attempt bound to that user. The transport must
// call it only from a request that proves the session and is not
// cross-site (a POST under the CSRF check): otherwise another site could
// start a link in the victim's browser.
func (a AuthService) OIDCStartLink(id, userID, redirectURL string) (string, OIDCState, error) {
	if userID == "" {
		return "", OIDCState{}, ErrOIDCState
	}
	authURL, st, err := a.OIDCStart(id, redirectURL)
	st.LinkUserID = userID
	return authURL, st, err
}

// OIDCFinish completes a login: st are the secrets OIDCStart made; state and
// code are what the provider sent back. The identity (provider, subject) is
// resolved to a user — an existing link, else the account with the same
// verified email (LinkByEmail), else a new account (AutoSignup) — and a
// session is started, replacing previousToken as logIn does.
func (a AuthService) OIDCFinish(ctx context.Context, st OIDCState, state, code, redirectURL, previousToken string) (domain.User, string, error) {
	if st.LinkUserID != "" {
		return domain.User{}, "", ErrOIDCState // a link attempt never logs in
	}
	p, claims, err := a.oidcExchange(ctx, st, state, code, redirectURL)
	if err != nil {
		return domain.User{}, "", err
	}
	u, err := a.oidcUser(ctx, p, claims)
	if err != nil {
		return domain.User{}, "", err
	}
	token, err := a.startSession(ctx, u.ID, previousToken)
	return u, token, err
}

// OIDCFinishLink completes an attempt OIDCStartLink began: the asserted
// identity is linked to sessionUserID, the user of the session the callback
// arrived with, which must be the user the attempt was started for. An
// identity linked to another user is ErrIdentityTaken; one already linked to
// this user is fine. A verified email equal to the account's marks the
// account's email verified. The session is left as it is.
func (a AuthService) OIDCFinishLink(ctx context.Context, st OIDCState, state, code, redirectURL, sessionUserID string) error {
	if st.LinkUserID == "" || sessionUserID == "" ||
		subtle.ConstantTimeCompare([]byte(st.LinkUserID), []byte(sessionUserID)) != 1 {
		return ErrOIDCState
	}
	p, claims, err := a.oidcExchange(ctx, st, state, code, redirectURL)
	if err != nil {
		return err
	}
	u, ok, err := a.Users.ByID(ctx, sessionUserID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOIDCState
	}
	email := domain.NormalizeEmail(claims.Email)
	if err := a.Users.LinkIdentity(ctx, u.ID, p.ID, claims.Subject, email); err != nil {
		return err
	}
	if claims.EmailVerified && email != "" && email == u.Email && !u.EmailVerified {
		return a.Users.MarkEmailVerified(ctx, u.ID)
	}
	return nil
}

// LinkedProviders lists the configured providers the user has an identity at.
func (a AuthService) LinkedProviders(ctx context.Context, userID string) ([]string, error) {
	linked, err := a.Users.LinkedProviders(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(linked))
	for _, id := range linked {
		if _, ok := a.oidcProvider(id); ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// oidcExchange checks that the callback belongs to the attempt st and
// redeems the code: the provider and the claims it asserts.
func (a AuthService) oidcExchange(ctx context.Context, st OIDCState, state, code, redirectURL string) (OIDCProviderConfig, OIDCClaims, error) {
	p, ok := a.oidcProvider(st.Provider)
	if !ok {
		return OIDCProviderConfig{}, OIDCClaims{}, ErrOIDCUnknownProvider
	}
	if st.State == "" || subtle.ConstantTimeCompare([]byte(state), []byte(st.State)) != 1 {
		return OIDCProviderConfig{}, OIDCClaims{}, ErrOIDCState
	}
	claims, err := p.Provider.Exchange(ctx, code, st.Verifier, st.Nonce, p.redirect(redirectURL))
	if err != nil {
		return OIDCProviderConfig{}, OIDCClaims{}, fmt.Errorf("%s: %w", p.ID, err)
	}
	if claims.Subject == "" {
		return OIDCProviderConfig{}, OIDCClaims{}, fmt.Errorf("%s: ID token without a subject", p.ID)
	}
	return p, claims, nil
}

func (p OIDCProviderConfig) redirect(derived string) string {
	if p.RedirectURL != "" {
		return p.RedirectURL
	}
	return derived
}

// oidcUser maps an asserted identity to a user, linking or creating one as the
// provider's configuration allows:
//
//  1. the user the identity is already linked to;
//  2. with a verified email that an account already has: that account, if
//     LinkByEmail is on and the account's email is verified too — otherwise
//     ErrOIDCEmailInUse (its owner links the provider from the account page);
//  3. a new account (AutoSignup), which needs a verified email: an unverified
//     one would let a provider with self-service signup claim any address.
func (a AuthService) oidcUser(ctx context.Context, p OIDCProviderConfig, c OIDCClaims) (domain.User, error) {
	if u, ok, err := a.Users.ByIdentity(ctx, p.ID, c.Subject); err != nil || ok {
		return u, err
	}
	email := domain.NormalizeEmail(c.Email)
	if email != "" && c.EmailVerified {
		u, ok, err := a.Users.ByEmail(ctx, email)
		if err != nil {
			return domain.User{}, err
		}
		if ok {
			return a.oidcLinkByEmail(ctx, p, c, u, email)
		}
	}
	return a.oidcSignup(ctx, p, c, email)
}

// oidcLinkByEmail attaches the identity to u, the account with the verified
// email the provider asserted — only when the provider allows it and u's own
// email is verified too.
func (a AuthService) oidcLinkByEmail(ctx context.Context, p OIDCProviderConfig, c OIDCClaims, u domain.User, email string) (domain.User, error) {
	if !p.LinkByEmail || !u.EmailVerified {
		return domain.User{}, ErrOIDCEmailInUse
	}
	return u, a.Users.LinkIdentity(ctx, u.ID, p.ID, c.Subject, email)
}

// oidcSignup creates an account for an identity that matches none, if the
// provider may (AutoSignup) and vouches for its email.
func (a AuthService) oidcSignup(ctx context.Context, p OIDCProviderConfig, c OIDCClaims, email string) (domain.User, error) {
	switch {
	case !p.AutoSignup:
		return domain.User{}, ErrOIDCNoAccount
	case email == "":
		return domain.User{}, ErrOIDCNoEmail
	case !c.EmailVerified:
		return domain.User{}, ErrOIDCEmailUnverified
	}
	// No password: the account logs in through the provider until the person
	// sets one (ChangePassword accepts an empty current password then).
	u, err := a.Users.Create(ctx, email, "")
	if errors.Is(err, ErrEmailTaken) {
		return domain.User{}, ErrOIDCEmailInUse
	}
	if err != nil {
		return domain.User{}, err
	}
	if err := a.Users.MarkEmailVerified(ctx, u.ID); err != nil {
		return domain.User{}, err
	}
	u.EmailVerified = true
	return u, a.Users.LinkIdentity(ctx, u.ID, p.ID, c.Subject, email)
}
