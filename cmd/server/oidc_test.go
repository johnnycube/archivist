package main

import (
	"strings"
	"testing"
)

func TestParseOIDC(t *testing.T) {
	env := map[string]string{
		"ARCHIVIST_OIDC_PROVIDERS":            "gitea, my-idp",
		"ARCHIVIST_OIDC_GITEA_ISSUER":         "https://gitea.example",
		"ARCHIVIST_OIDC_GITEA_CLIENT_ID":      "id-1",
		"ARCHIVIST_OIDC_GITEA_CLIENT_SECRET":  " s3cret ",
		"ARCHIVIST_OIDC_GITEA_NAME":           "Company Gitea",
		"ARCHIVIST_OIDC_GITEA_AUTO_SIGNUP":    "0",
		"ARCHIVIST_OIDC_MY_IDP_ISSUER":        "http://idp.internal:8080/realms/x",
		"ARCHIVIST_OIDC_MY_IDP_CLIENT_ID":     "id-2",
		"ARCHIVIST_OIDC_MY_IDP_SCOPES":        "email, groups",
		"ARCHIVIST_OIDC_MY_IDP_REDIRECT_URL":  "https://archive.example/api/auth/oidc/my-idp/callback",
		"ARCHIVIST_OIDC_MY_IDP_LINK_BY_EMAIL": "false",
	}
	got, err := parseOIDC(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d providers", len(got))
	}
	g, m := got[0], got[1]
	if g.ID != "gitea" || g.Name != "Company Gitea" || g.ClientSecret != " s3cret " || g.AutoSignup || !g.LinkByEmail || g.Scopes != nil {
		t.Errorf("gitea = %+v", g)
	}
	if m.ID != "my-idp" || m.Name != "My-idp" || !m.AutoSignup || m.LinkByEmail || strings.Join(m.Scopes, " ") != "openid email groups" || m.RedirectURL == "" {
		t.Errorf("my-idp = %+v", m)
	}
}

func TestParseOIDCReportsEveryProblem(t *testing.T) {
	env := map[string]string{
		"ARCHIVIST_OIDC_PROVIDERS":          "Bad id,ok,ok,nourl",
		"ARCHIVIST_OIDC_OK_ISSUER":          "https://idp.example",
		"ARCHIVIST_OIDC_OK_CLIENT_ID":       "x",
		"ARCHIVIST_OIDC_OK_AUTO_SIGNUP":     "maybe",
		"ARCHIVIST_OIDC_NOURL_ISSUER":       "idp.example",
		"ARCHIVIST_OIDC_NOURL_CLIENT_ID":    "x",
		"ARCHIVIST_OIDC_NOURL_REDIRECT_URL": "ftp://x",
	}
	_, err := parseOIDC(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{`"Bad id"`, "listed twice", "AUTO_SIGNUP", "NOURL_ISSUER", "NOURL_REDIRECT_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
	if _, err := parseOIDC(func(string) string { return "" }); err != nil {
		t.Errorf("no providers = %v; want nil", err)
	}
}
