package rest

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/webui"
)

var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// inlineScriptSources returns the CSP source ('sha256-…') of every inline
// script in the document, computed the way a browser does: over the exact
// bytes between the tags.
func inlineScriptSources(t *testing.T, html []byte) []string {
	t.Helper()
	var out []string
	for _, m := range inlineScriptRe.FindAllSubmatch(html, -1) {
		sum := sha256.Sum256(m[1])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	if len(out) == 0 {
		t.Fatal("no inline script in the document: the test would prove nothing")
	}
	return out
}

func scriptSrc(t *testing.T, policy string) string {
	t.Helper()
	for _, d := range strings.Split(policy, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src ") {
			return d
		}
	}
	t.Fatalf("no script-src in %q", policy)
	return ""
}

// The header allows the embedded shell's inline bootstrap script by its hash —
// the real SHA-256 of the script, computed here — and nothing inline besides.
func TestCSPHashesTheEmbeddedInlineScript(t *testing.T) {
	index, err := webui.Index()
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := contentSecurityPolicy(index)
	if !ok {
		t.Fatal("no script hash found in the embedded index.html: rebuild the frontend (make web)")
	}
	src := scriptSrc(t, policy)
	for _, want := range inlineScriptSources(t, index) {
		if !strings.Contains(src, want) {
			t.Errorf("script-src = %q does not allow the inline script %s", src, want)
		}
	}
	if strings.Contains(src, "unsafe-inline") || strings.Contains(src, "unsafe-eval") {
		t.Errorf("script-src = %q, want hashes only", src)
	}
	for _, want := range []string{"default-src 'self'", "style-src 'self' 'unsafe-inline'", "frame-ancestors 'none'", "base-uri 'self'", "connect-src 'self'", "object-src 'none'", "form-action 'self'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy %q lacks %q", policy, want)
		}
	}
}

// End to end: what is served at / is allowed by the header served with it.
func TestServedShellIsAllowedByServedCSP(t *testing.T) {
	h := (&Server{App: &app.App{}}).Handler()
	for _, path := range []string{"/", "/help", "/some/client/route"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body, _ := io.ReadAll(rec.Body)
		src := scriptSrc(t, rec.Header().Get("Content-Security-Policy"))
		for _, want := range inlineScriptSources(t, body) {
			if !strings.Contains(src, want) {
				t.Errorf("GET %s: header script-src %q blocks the page's own inline script %s", path, src, want)
			}
		}
		if strings.Contains(src, "unsafe-inline") {
			t.Errorf("GET %s: script-src = %q", path, src)
		}
	}
	// API responses carry the same policy.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "'sha256-") {
		t.Errorf("/healthz CSP = %q", got)
	}
}

func TestScriptHashExtraction(t *testing.T) {
	const h1, h2 = "'sha256-DnAbA1nYHsD2zZppvHSHKKr87iVQOgX6bnjnVLYA3ok='", "'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='"
	meta := func(content string) []byte {
		return []byte(`<head><meta http-equiv="content-security-policy" content="` + content + `"></head>`)
	}
	for _, tc := range []struct {
		name string
		html []byte
		want []string
	}{
		{"one hash", meta("default-src 'self'; script-src 'self' " + h1 + "; style-src 'self'"), []string{h1}},
		{"two hashes", meta("script-src 'self' " + h1 + " " + h2), []string{h1, h2}},
		{"entity-encoded quotes", meta("script-src &#39;self&#39; " + strings.ReplaceAll(h1, "'", "&#39;")), []string{h1}},
		{"hashes of other directives are not script hashes", meta("style-src " + h2 + "; script-src 'self' " + h1), []string{h1}},
		{"upper-case attribute", []byte(`<META HTTP-EQUIV="Content-Security-Policy" CONTENT="script-src ` + h1 + `">`), []string{h1}},
		{"truncated hash", meta("script-src 'self' 'sha256-abc='"), nil},
		{"no script-src", meta("default-src 'self'"), nil},
		{"no meta tag", []byte("<html></html>"), nil},
		{"no document", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scriptHashes(tc.html); strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("scriptHashes = %q, want %q", got, tc.want)
			}
		})
	}
}

// Without a hash (a build without the frontend's CSP) the policy fails
// closed — no inline script at all — and the server warns once.
func TestCSPFailsClosedAndWarnsOnce(t *testing.T) {
	policy, ok := contentSecurityPolicy([]byte("<html></html>"))
	if ok || scriptSrc(t, policy) != "script-src 'self'" {
		t.Errorf("fallback = %q, %v; want script-src 'self' only", policy, ok)
	}
	var logs strings.Builder
	s := &Server{App: &app.App{}, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	s.buildCSP(nil)
	s.buildCSP(nil)
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("%d warnings, want exactly 1:\n%s", n, logs.String())
	}
	if strings.Contains(scriptSrc(t, s.csp), "unsafe-inline") {
		t.Errorf("csp = %q", s.csp)
	}
}
