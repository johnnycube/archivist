// Package oidc is the app.OIDCProvider adapter: OpenID Connect discovery, the
// authorization-code flow with PKCE and ID-token verification, on
// github.com/coreos/go-oidc and golang.org/x/oauth2.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/johnnycube/archivist/internal/app"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config describes one provider as registered with it.
type Config struct {
	// Issuer is the provider's issuer URL; its discovery document is fetched
	// from <Issuer>/.well-known/openid-configuration.
	Issuer       string
	ClientID     string
	ClientSecret string // "" = a public client (PKCE only)
	// Scopes to request; nil = openid, email, profile.
	Scopes []string
}

// Provider is a discovered OpenID Connect provider.
type Provider struct {
	provider *gooidc.Provider
	verifier *gooidc.IDTokenVerifier
	oauth    oauth2.Config
}

var _ app.OIDCProvider = (*Provider)(nil)

// New discovers the provider's endpoints and signing keys (one request to the
// issuer; ctx bounds it).
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, errors.New("oidc: issuer and client id are required")
	}
	p, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover %s: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "email", "profile"}
	}
	return &Provider{
		provider: p,
		verifier: p.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			Endpoint: p.Endpoint(), Scopes: scopes,
		},
	}, nil
}

// config is the oauth2 configuration for one callback URL.
func (p *Provider) config(redirectURL string) *oauth2.Config {
	c := p.oauth
	c.RedirectURL = redirectURL
	return &c
}

// AuthCodeURL implements app.OIDCProvider: the authorization request carries
// state, the nonce, and the S256 challenge of verifier.
func (p *Provider) AuthCodeURL(state, nonce, verifier, redirectURL string) string {
	return p.config(redirectURL).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce))
}

// claims are the ID-token (or userinfo) fields the application uses.
type claims struct {
	Email             string   `json:"email"`
	EmailVerified     flexBool `json:"email_verified"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
}

// flexBool reads a JSON boolean that some providers send as the string "true".
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*b = flexBool(x)
	case string:
		parsed, err := strconv.ParseBool(x)
		*b = flexBool(err == nil && parsed)
	default:
		*b = false
	}
	return nil
}

// Exchange implements app.OIDCProvider: it redeems the code with the PKCE
// verifier, verifies the ID token (signature against the discovered keys,
// issuer, audience, expiry) and checks that it carries nonce. Providers that
// put the email into userinfo rather than the ID token are asked there.
func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce, redirectURL string) (app.OIDCClaims, error) {
	tok, err := p.config(redirectURL).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return app.OIDCClaims{}, fmt.Errorf("oidc: exchange code: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return app.OIDCClaims{}, errors.New("oidc: token response without an id_token")
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return app.OIDCClaims{}, fmt.Errorf("oidc: verify id token: %w", err)
	}
	if idt.Nonce != nonce {
		return app.OIDCClaims{}, errors.New("oidc: id token nonce mismatch")
	}
	var c claims
	if err := idt.Claims(&c); err != nil {
		return app.OIDCClaims{}, fmt.Errorf("oidc: read claims: %w", err)
	}
	if c.Email == "" {
		c = p.userInfo(ctx, tok, idt.Subject, c)
	}
	name := c.Name
	if name == "" {
		name = c.PreferredUsername
	}
	return app.OIDCClaims{Subject: idt.Subject, Email: c.Email, EmailVerified: bool(c.EmailVerified), Name: name}, nil
}

// userInfo fills the email (and a missing name) from the userinfo endpoint;
// any failure leaves the claims as they were — the caller decides what an
// identity without an email means. Userinfo about another subject than the
// ID token's (OpenID Connect Core 5.3.2: it MUST be verified) is ignored.
func (p *Provider) userInfo(ctx context.Context, tok *oauth2.Token, subject string, c claims) claims {
	info, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil || info.Subject != subject {
		return c
	}
	var extra claims
	_ = info.Claims(&extra) // best effort: the typed fields below are what matter
	c.Email, c.EmailVerified = info.Email, flexBool(info.EmailVerified)
	if c.Name == "" {
		c.Name = extra.Name
	}
	if c.PreferredUsername == "" {
		c.PreferredUsername = extra.PreferredUsername
	}
	return c
}
