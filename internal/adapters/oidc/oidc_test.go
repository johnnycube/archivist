package oidc_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/oidc"
)

// fakeIDP is a minimal OpenID provider: discovery, JWKS, a token endpoint
// that mints RS256 ID tokens, and userinfo. It records what the client sent.
type fakeIDP struct {
	srv  *httptest.Server
	key  *rsa.PrivateKey
	seen url.Values // the last token request's form
	// idTokenClaims are added to every ID token; userinfo answers with info.
	idTokenClaims, info map[string]any
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{key: key, idTokenClaims: map[string]any{}, info: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/auth",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
			"userinfo_endpoint":                     f.srv.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.seen = r.PostForm
		if r.PostForm.Get("code") != "good-code" {
			w.WriteHeader(400)
			writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		now := time.Now().Unix()
		c := map[string]any{"iss": f.srv.URL, "aud": "client-1", "sub": "subject-1", "iat": now, "exp": now + 300}
		for k, v := range f.idTokenClaims {
			c[k] = v
		}
		if n := r.PostForm.Get("nonce_override"); n != "" {
			c["nonce"] = n
		}
		writeJSON(w, map[string]any{"access_token": "at-1", "token_type": "Bearer", "id_token": f.sign(t, c)})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, f.info)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// sign mints an RS256 JWT with the fake's key.
func (f *fakeIDP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newProvider(t *testing.T, f *fakeIDP) *oidc.Provider {
	t.Helper()
	p, err := oidc.New(context.Background(), oidc.Config{Issuer: f.srv.URL, ClientID: "client-1", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAuthCodeURLCarriesStateNonceAndChallenge(t *testing.T) {
	p := newProvider(t, newFakeIDP(t))
	u, err := url.Parse(p.AuthCodeURL("state-1", "nonce-1", "verifier-123456789012345678901234567890123", "https://app.example/cb"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	sum := sha256.Sum256([]byte("verifier-123456789012345678901234567890123"))
	want := map[string]string{
		"state": "state-1", "nonce": "nonce-1", "response_type": "code", "client_id": "client-1",
		"redirect_uri": "https://app.example/cb", "scope": "openid email profile",
		"code_challenge_method": "S256", "code_challenge": base64.RawURLEncoding.EncodeToString(sum[:]),
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !strings.HasSuffix(u.Path, "/auth") {
		t.Errorf("path = %q, want the authorization endpoint", u.Path)
	}
}

func TestExchangeVerifiesTokenAndReadsClaims(t *testing.T) {
	f := newFakeIDP(t)
	f.idTokenClaims = map[string]any{"nonce": "nonce-1", "email": "Me@Example.com", "email_verified": true, "name": "Me"}
	p := newProvider(t, f)
	c, err := p.Exchange(context.Background(), "good-code", "verifier-1", "nonce-1", "https://app.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "subject-1" || c.Email != "Me@Example.com" || !c.EmailVerified || c.Name != "Me" {
		t.Errorf("claims = %+v", c)
	}
	if got := f.seen.Get("code_verifier"); got != "verifier-1" {
		t.Errorf("code_verifier sent = %q, want verifier-1", got)
	}
	if got := f.seen.Get("redirect_uri"); got != "https://app.example/cb" {
		t.Errorf("redirect_uri sent = %q", got)
	}
}

func TestExchangeFallsBackToUserinfoAndReadsStringBooleans(t *testing.T) {
	f := newFakeIDP(t)
	f.idTokenClaims = map[string]any{"nonce": "n"}
	f.info = map[string]any{"sub": "subject-1", "email": "me@example.com", "email_verified": "true", "preferred_username": "me"}
	p := newProvider(t, f)
	c, err := p.Exchange(context.Background(), "good-code", "v", "n", "https://app.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "me@example.com" || !c.EmailVerified || c.Name != "me" {
		t.Errorf("claims = %+v", c)
	}
}

// Userinfo about another subject than the ID token's must not be believed.
func TestExchangeIgnoresUserinfoOfAnotherSubject(t *testing.T) {
	f := newFakeIDP(t)
	f.idTokenClaims = map[string]any{"nonce": "n"}
	f.info = map[string]any{"sub": "someone-else", "email": "victim@example.com", "email_verified": true}
	p := newProvider(t, f)
	c, err := p.Exchange(context.Background(), "good-code", "v", "n", "https://app.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "" || c.EmailVerified {
		t.Errorf("claims = %+v; want no email from another subject's userinfo", c)
	}
}

func TestExchangeRefusesBadCodeAndWrongNonce(t *testing.T) {
	f := newFakeIDP(t)
	f.idTokenClaims = map[string]any{"nonce": "right"}
	p := newProvider(t, f)
	if _, err := p.Exchange(context.Background(), "bad-code", "v", "right", "https://app.example/cb"); err == nil {
		t.Error("bad code: want an error")
	}
	if _, err := p.Exchange(context.Background(), "good-code", "v", "other", "https://app.example/cb"); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Errorf("wrong nonce: err = %v, want a nonce mismatch", err)
	}
}

func TestNewRejectsUnreachableIssuer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := oidc.New(ctx, oidc.Config{Issuer: "http://127.0.0.1:1", ClientID: "x"}); err == nil {
		t.Error("want a discovery error")
	}
	if _, err := oidc.New(ctx, oidc.Config{Issuer: "http://127.0.0.1:1"}); err == nil {
		t.Error("want an error for a missing client id")
	}
}
