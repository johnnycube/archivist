package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

// assertAsset fails unless path is served as a file of its own with a
// Content-Type starting with wantType (and not the SPA fallback).
func assertAsset(t *testing.T, path, wantType string) {
	t.Helper()
	rec := get(t, path)
	if rec.Code != http.StatusOK {
		t.Errorf("GET %s: status %d, want 200", path, rec.Code)
		return
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, wantType) {
		t.Errorf("GET %s: Content-Type %q, want %s…", path, got, wantType)
	}
}

// Every icon and the manifest the SPA shell links must exist in the embedded
// build, with a type browsers accept (a missing file would silently come back
// as index.html through the SPA fallback).
func TestShellIconLinksResolve(t *testing.T) {
	index, err := Index()
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`<link rel="(icon|apple-touch-icon|manifest)" href="([^"]+)"`).
		FindAllStringSubmatch(string(index), -1)
	if len(links) < 4 {
		t.Fatalf("index.html links %d icons/manifest, want ≥ 4 (favicon.ico, favicon.svg, apple-touch-icon, manifest): %q", len(links), links)
	}
	for _, l := range links {
		want := "image/"
		if l[1] == "manifest" {
			want = "application/manifest+json"
		}
		assertAsset(t, l[2], want)
	}
}

func TestManifestIconsResolve(t *testing.T) {
	rec := get(t, "/manifest.webmanifest")
	var m struct {
		Icons []struct {
			Src     string `json:"src"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	maskable := false
	for _, ic := range m.Icons {
		assertAsset(t, ic.Src, ic.Type)
		maskable = maskable || ic.Purpose == "maskable"
	}
	if len(m.Icons) == 0 || !maskable {
		t.Errorf("manifest icons %+v: want at least one, including a maskable one", m.Icons)
	}
}

func TestUnknownPathFallsBackToShell(t *testing.T) {
	for _, path := range []string{"/", "/help/exports", "/nope.webmanifest"} {
		assertAsset(t, path, "text/html")
	}
}
