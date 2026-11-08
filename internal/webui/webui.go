// Package webui embeds and serves the built SvelteKit SPA (frontend/ → dist/).
// Run `npm --prefix frontend run build` to regenerate dist before `go build`.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed all:dist
var files embed.FS

// Index returns the SPA shell (dist/index.html). The HTTP adapter derives its
// Content-Security-Policy from the script hashes the build wrote into it.
func Index() ([]byte, error) { return files.ReadFile("dist/index.html") }

// Handler serves the embedded SPA. Unknown paths fall back to index.html so
// client-side routing works.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve the file if it exists; otherwise the SPA shell.
		if _, err := fs.Stat(sub, cleanPath(r.URL.Path)); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		} else if path.Ext(r.URL.Path) == ".webmanifest" {
			// Go's MIME table has no entry for the web app manifest and the
			// runtime image has no /etc/mime.types, so it would go out as
			// text/plain; FileServer keeps a Content-Type that is already set.
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func cleanPath(p string) string {
	if p == "" || p == "/" {
		return "index.html"
	}
	return p[1:] // strip leading slash for fs.Stat
}
