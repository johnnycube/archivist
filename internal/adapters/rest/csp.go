package rest

import (
	"html"
	"regexp"
	"strings"
)

// The Content-Security-Policy header. Everything is same-origin; data: images
// cover seeded previews; Svelte sets style attributes, hence 'unsafe-inline'
// for styles. The SPA shell carries a <meta> policy of its own, but a <meta>
// policy cannot express frame-ancestors and covers that one document only; the
// header covers every response.
const (
	cspBeforeScript = "default-src 'self'; img-src 'self' data:; media-src 'self' data:; " +
		"style-src 'self' 'unsafe-inline'; script-src 'self'"
	cspAfterScript = "; connect-src 'self'; object-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'self'"
)

var (
	// cspMetaRe finds the policy the SvelteKit build writes into the SPA shell
	// (kit.csp, mode "hash"): the hash of its one inline bootstrap script.
	cspMetaRe = regexp.MustCompile(`(?i)<meta\s+http-equiv="content-security-policy"\s+content="([^"]*)"`)
	cspHashRe = regexp.MustCompile(`'sha256-[A-Za-z0-9+/]{43}='`)
)

// scriptHashes returns the 'sha256-…' sources of the script-src directive of
// the CSP <meta> tag in indexHTML, in order of appearance.
func scriptHashes(indexHTML []byte) []string {
	m := cspMetaRe.FindSubmatch(indexHTML)
	if m == nil {
		return nil
	}
	for _, directive := range strings.Split(html.UnescapeString(string(m[1])), ";") {
		if name, sources, _ := strings.Cut(strings.TrimSpace(directive), " "); strings.EqualFold(name, "script-src") {
			return cspHashRe.FindAllString(sources, -1)
		}
	}
	return nil
}

// contentSecurityPolicy builds the header value. Inline scripts are allowed by
// hash only; ok is false when the shell carries no hash (a build without the
// frontend's CSP), and the policy then fails closed — no inline script at all,
// so the SPA will not boot — rather than open the door to injected script.
func contentSecurityPolicy(indexHTML []byte) (policy string, ok bool) {
	hashes := scriptHashes(indexHTML)
	if len(hashes) == 0 {
		return cspBeforeScript + cspAfterScript, false
	}
	return cspBeforeScript + " " + strings.Join(hashes, " ") + cspAfterScript, true
}

// buildCSP sets the server's Content-Security-Policy header from the SPA shell
// it serves, warning (once per server) when it has to fall back.
func (s *Server) buildCSP(indexHTML []byte) {
	policy, hashed := contentSecurityPolicy(indexHTML)
	s.csp = policy
	if !hashed {
		s.cspWarned.Do(func() {
			s.logger().Warn("no script hash in the embedded index.html (frontend not built? run make web): " +
				"Content-Security-Policy allows no inline script, so the web UI will not start")
		})
	}
}
