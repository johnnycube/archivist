package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/oidc"
	"github.com/johnnycube/archivist/internal/app"
)

// OIDC login configuration:
//
//	ARCHIVIST_OIDC_PROVIDERS=gitea,google        provider ids, comma-separated
//	ARCHIVIST_OIDC_<ID>_ISSUER=https://...        required; discovery runs at boot
//	ARCHIVIST_OIDC_<ID>_CLIENT_ID=...             required
//	ARCHIVIST_OIDC_<ID>_CLIENT_SECRET=...         optional (public client: PKCE only)
//	ARCHIVIST_OIDC_<ID>_NAME=Gitea                login button label; default: the id, capitalised
//	ARCHIVIST_OIDC_<ID>_SCOPES="openid email profile"  default; openid is added if missing
//	ARCHIVIST_OIDC_<ID>_REDIRECT_URL=https://host/api/auth/oidc/<id>/callback
//	                                              default: derived from the request
//	ARCHIVIST_OIDC_<ID>_AUTO_SIGNUP=1             create an account on a first login
//	ARCHIVIST_OIDC_<ID>_LINK_BY_EMAIL=1           attach a first login to the account with the same verified email
//
// <ID> is the id upper-cased with "-" as "_" (my-idp → ARCHIVIST_OIDC_MY_IDP_*).

type oidcProviderConfig struct {
	ID, Name, Issuer, ClientID, ClientSecret, RedirectURL string
	Scopes                                                []string
	AutoSignup, LinkByEmail                               bool
}

var oidcIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// parseOIDC reads the ARCHIVIST_OIDC_* variables through getenv. Every
// invalid value is reported.
func parseOIDC(getenv func(string) string) ([]oidcProviderConfig, error) {
	var out []oidcProviderConfig
	var errs []error
	seen := map[string]bool{}
	for _, id := range strings.Split(getenv("ARCHIVIST_OIDC_PROVIDERS"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !oidcIDRe.MatchString(id) {
			errs = append(errs, fmt.Errorf("ARCHIVIST_OIDC_PROVIDERS: %q: want a-z, 0-9 and -, at most 32 characters", id))
			continue
		}
		if seen[id] {
			errs = append(errs, fmt.Errorf("ARCHIVIST_OIDC_PROVIDERS: %q is listed twice", id))
			continue
		}
		seen[id] = true
		p, perrs := parseOIDCProvider(id, getenv)
		errs = append(errs, perrs...)
		out = append(out, p)
	}
	return out, errors.Join(errs...)
}

func parseOIDCProvider(id string, getenv func(string) string) (oidcProviderConfig, []error) {
	prefix := "ARCHIVIST_OIDC_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_"
	get := func(key string) string { return strings.TrimSpace(getenv(prefix + key)) }
	var errs []error
	p := oidcProviderConfig{
		ID: id, Name: get("NAME"), Issuer: get("ISSUER"), ClientID: get("CLIENT_ID"),
		ClientSecret: getenv(prefix + "CLIENT_SECRET"), RedirectURL: get("REDIRECT_URL"),
		AutoSignup: true, LinkByEmail: true,
	}
	if p.Name == "" {
		p.Name = strings.ToUpper(id[:1]) + id[1:]
	}
	for _, key := range []string{"ISSUER", "REDIRECT_URL"} {
		if v := get(key); v != "" && !isHTTPURL(v) {
			errs = append(errs, fmt.Errorf("%s%s=%q: want an http(s) URL", prefix, key, v))
		}
	}
	if p.Issuer == "" {
		errs = append(errs, fmt.Errorf("%sISSUER is required", prefix))
	}
	if p.ClientID == "" {
		errs = append(errs, fmt.Errorf("%sCLIENT_ID is required", prefix))
	}
	for _, sw := range []struct {
		key string
		dst *bool
	}{{"AUTO_SIGNUP", &p.AutoSignup}, {"LINK_BY_EMAIL", &p.LinkByEmail}} {
		switch v := get(sw.key); v {
		case "":
		case "1", "true":
			*sw.dst = true
		case "0", "false":
			*sw.dst = false
		default:
			errs = append(errs, fmt.Errorf("%s%s=%q: want 1 or 0", prefix, sw.key, v))
		}
	}
	if scopes := strings.Fields(strings.ReplaceAll(get("SCOPES"), ",", " ")); len(scopes) > 0 {
		if !slicesContains(scopes, "openid") {
			scopes = append([]string{"openid"}, scopes...)
		}
		p.Scopes = scopes
	}
	return p, errs
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// newOIDCProviders discovers every configured provider. Discovery that fails
// ends the boot: a login button that cannot work is worse than a clear
// message in the log.
func newOIDCProviders(cfg config) []app.OIDCProviderConfig {
	out := make([]app.OIDCProviderConfig, 0, len(cfg.OIDC))
	for _, pc := range cfg.OIDC {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		p, err := oidc.New(ctx, oidc.Config{Issuer: pc.Issuer, ClientID: pc.ClientID, ClientSecret: pc.ClientSecret, Scopes: pc.Scopes})
		cancel()
		if err != nil {
			fatal("oidc provider "+pc.ID, err)
		}
		slog.Info("oidc provider ready", "id", pc.ID, "issuer", pc.Issuer, "name", pc.Name,
			"auto_signup", pc.AutoSignup, "link_by_email", pc.LinkByEmail, "redirect_url", pc.RedirectURL)
		out = append(out, app.OIDCProviderConfig{
			ID: pc.ID, Name: pc.Name, Provider: p, RedirectURL: pc.RedirectURL,
			AutoSignup: pc.AutoSignup, LinkByEmail: pc.LinkByEmail,
		})
	}
	return out
}
