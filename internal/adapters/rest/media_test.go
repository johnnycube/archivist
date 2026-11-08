package rest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/blob"
	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// streamOnly hides the seekability of the blob store's readers: what an
// object store (S3) hands out.
type streamOnly struct{ app.Blobs }

func (s streamOnly) Open(ctx context.Context, userID, key string) (io.ReadCloser, error) {
	rc, err := s.Blobs.Open(ctx, userID, key)
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{rc, rc}, nil
}

const (
	svgPayload  = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.cookie)</script></svg>`
	htmlPayload = `<!doctype html><script>fetch('/api/auth/delete',{method:'POST'})</script>`
)

// mediaFixture is a signed-up user with one event whose attachments are the
// given blob keys (stored with the key's own text as content, or a payload).
func mediaFixture(t *testing.T, blobs app.Blobs, keys ...string) (h http.Handler, c *http.Cookie, eventID string) {
	t.Helper()
	ctx := context.Background()
	deps := newDeps(t)
	deps.Blobs = blobs
	h = rest.New(app.New(deps), false).Handler()
	c = signup(t, h)
	var me struct{ ID string }
	json.Unmarshal(request(h, "GET", "/api/auth/me", "", nil, c).Body.Bytes(), &me)

	ev := domain.Event{
		SourceID: "whatsapp", ExternalID: "m1", Type: "whatsapp.message",
		Category: domain.CategoryMessage, OccurredAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, key := range keys {
		ev.Media = append(ev.Media, domain.Media{BlobKey: key})
		content := "content of " + key
		switch {
		case strings.HasSuffix(strings.ToLower(key), ".svg"):
			content = svgPayload
		case strings.HasSuffix(strings.ToLower(key), "html"):
			content = htmlPayload
		}
		if err := blobs.Put(ctx, me.ID, "whatsapp/"+key, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	ev.Media = append(ev.Media, domain.Media{URL: "https://example.org/remote.jpg"}) // never stored
	stored, _, err := deps.Events.Save(ctx, me.ID, "acc", ev)
	if err != nil {
		t.Fatal(err)
	}
	return h, c, stored.ID
}

func TestMediaIsSafeByConstruction(t *testing.T) {
	cases := []struct {
		key         string
		contentType string
		disposition string
	}{
		// Passive types render in place, with a type from the fixed map.
		{"IMG-0001.jpg", "image/jpeg", "inline"},
		{"photo.JPEG", "image/jpeg", "inline"},
		{"a.png", "image/png", "inline"},
		{"a.gif", "image/gif", "inline"},
		{"a.webp", "image/webp", "inline"},
		{"a.avif", "image/avif", "inline"},
		{"a.bmp", "image/bmp", "inline"},
		{"a.heic", "image/heic", "inline"},
		{"VID-0001.mp4", "video/mp4", "inline"},
		{"a.webm", "video/webm", "inline"},
		{"a.mov", "video/quicktime", "inline"},
		{"PTT-0001.opus", "audio/opus", "inline"},
		{"a.mp3", "audio/mpeg", "inline"},
		{"a.m4a", "audio/mp4", "inline"},
		{"a.ogg", "audio/ogg", "inline"},
		{"a.wav", "audio/wav", "inline"},
		{"a.aac", "audio/aac", "inline"},
		{"a.amr", "audio/amr", "inline"},
		{"a.3gp", "audio/3gpp", "inline"},
		// Everything else is an opaque download — above all anything that can
		// carry script.
		{"drawing.svg", "application/octet-stream", `attachment; filename=drawing.svg`},
		{"DRAWING.SVG", "application/octet-stream", `attachment; filename=DRAWING.SVG`},
		{"page.html", "application/octet-stream", `attachment; filename=page.html`},
		{"page.xhtml", "application/octet-stream", `attachment; filename=page.xhtml`},
		{"feed.xml", "application/octet-stream", `attachment; filename=feed.xml`},
		{"DOC-0002.pdf", "application/octet-stream", `attachment; filename=DOC-0002.pdf`},
		{"contact.vcf", "application/octet-stream", `attachment; filename=contact.vcf`},
		{"app.apk", "application/octet-stream", `attachment; filename=app.apk`},
		{"setup.exe", "application/octet-stream", `attachment; filename=setup.exe`},
		{"archive.zip", "application/octet-stream", `attachment; filename=archive.zip`},
		{"noextension", "application/octet-stream", `attachment; filename=noextension`},
		{"evil.jpg.html", "application/octet-stream", `attachment; filename=evil.jpg.html`},
		// Only the base name is offered, correctly encoded.
		{"chats/Book Club/Jahresbericht 2023.pdf", "application/octet-stream", `attachment; filename="Jahresbericht 2023.pdf"`},
		{"Übersicht ✓.pdf", "application/octet-stream", `attachment; filename*=utf-8''%C3%9Cbersicht%20%E2%9C%93.pdf`},
		{`we"ird;name.docx`, "application/octet-stream", `attachment; filename="we\"ird;name.docx"`},
	}
	keys := make([]string, len(cases))
	for i, tc := range cases {
		keys[i] = tc.key
	}
	for name, blobs := range map[string]app.Blobs{
		"seekable store":    blob.NewFS(t.TempDir()),
		"stream-only store": streamOnly{blob.NewFS(t.TempDir())},
	} {
		h, c, id := mediaFixture(t, blobs, keys...)
		for i, tc := range cases {
			t.Run(name+"/"+tc.key, func(t *testing.T) {
				rec := request(h, "GET", fmt.Sprintf("/api/events/%s/media/%d", id, i), "", nil, c)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
				for header, want := range map[string]string{
					"Content-Type":                 tc.contentType,
					"Content-Disposition":          tc.disposition,
					"X-Content-Type-Options":       "nosniff",
					"Content-Security-Policy":      "sandbox; default-src 'none'",
					"Cross-Origin-Resource-Policy": "same-origin",
					"Cache-Control":                "private, no-store",
				} {
					if got := rec.Header().Get(header); got != want {
						t.Errorf("%s = %q, want %q", header, got, want)
					}
				}
				if n := len(rec.Header().Values("Content-Security-Policy")); n != 1 {
					t.Errorf("%d CSP headers, want the sandbox policy alone", n)
				}
				if rec.Body.Len() == 0 {
					t.Error("empty body")
				}
			})
		}
	}
}

// The point of the exercise, spelled out: markup from an export is never
// rendered in the app's origin.
func TestActiveContentIsNeverServedInline(t *testing.T) {
	h, c, id := mediaFixture(t, blob.NewFS(t.TempDir()), "drawing.svg", "page.html")
	for i, payload := range []string{svgPayload, htmlPayload} {
		rec := request(h, "GET", fmt.Sprintf("/api/events/%s/media/%d", id, i), "", nil, c)
		if rec.Body.String() != payload {
			t.Fatalf("media %d: body = %q (the download must be the original bytes)", i, rec.Body.String())
		}
		ct, cd := rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition")
		if ct != "application/octet-stream" || !strings.HasPrefix(cd, "attachment") {
			t.Errorf("media %d served as %q / %q, want an octet-stream attachment", i, ct, cd)
		}
		for _, bad := range []string{"svg", "html", "xml", "inline"} {
			if strings.Contains(ct, bad) || strings.HasPrefix(cd, bad) {
				t.Errorf("media %d: %q / %q would let a browser render it", i, ct, cd)
			}
		}
	}
}

func TestMediaNotFound(t *testing.T) {
	h, c, id := mediaFixture(t, blob.NewFS(t.TempDir()), "a.jpg")
	for url, want := range map[string]int{
		"/api/events/" + id + "/media/0":                           200,
		"/api/events/" + id + "/media/1":                           404, // remote-only media: nothing stored
		"/api/events/" + id + "/media/2":                           404, // out of range
		"/api/events/" + id + "/media/-1":                          404,
		"/api/events/" + id + "/media/x":                           400,
		"/api/events/99999999-9999-4999-8999-999999999999/media/0": 404, // no such event
		"/api/events/not-an-id/media/0":                            404,
		"/api/events/99999999-9999-4999-8999-999999999999":         404,
	} {
		if rec := request(h, "GET", url, "", nil, c); rec.Code != want {
			t.Errorf("GET %s = %d, want %d: %s", url, rec.Code, want, rec.Body.String())
		}
	}
	// Another user cannot reach it.
	other := cookieNamed(request(h, "POST", "/api/auth/signup", `{"email":"other@x.com","password":"password123"}`, nil), rest.CookieName)
	if rec := request(h, "GET", "/api/events/"+id+"/media/0", "", nil, other); rec.Code != http.StatusNotFound {
		t.Errorf("another user's media = %d, want 404", rec.Code)
	}
}

// Seeking in audio and video needs Range requests; a seekable blob store gets
// them from net/http. A stream-only store answers with the whole file.
func TestMediaRangeRequests(t *testing.T) {
	const content = "content of clip.mp4"
	rng := map[string]string{"Range": "bytes=8-9"}

	h, c, id := mediaFixture(t, blob.NewFS(t.TempDir()), "clip.mp4", "notes.pdf")
	rec := request(h, "GET", "/api/events/"+id+"/media/0", "", rng, c)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "of" {
		t.Fatalf("range = %d %q, want 206 %q", rec.Code, rec.Body.String(), "of")
	}
	if got, want := rec.Header().Get("Content-Range"), fmt.Sprintf("bytes 8-9/%d", len(content)); got != want {
		t.Errorf("Content-Range = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type of a range = %q", got)
	}
	full := request(h, "GET", "/api/events/"+id+"/media/0", "", nil, c)
	if full.Code != 200 || full.Body.String() != content || full.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("full = %d %q, Accept-Ranges %q", full.Code, full.Body.String(), full.Header().Get("Accept-Ranges"))
	}
	// A range of a download stays a download.
	att := request(h, "GET", "/api/events/"+id+"/media/1", "", rng, c)
	if att.Code != http.StatusPartialContent || att.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(att.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("range of an attachment = %d %v", att.Code, att.Header())
	}

	h, c, id = mediaFixture(t, streamOnly{blob.NewFS(t.TempDir())}, "clip.mp4")
	rec = request(h, "GET", "/api/events/"+id+"/media/0", "", rng, c)
	if rec.Code != 200 || rec.Body.String() != content || rec.Header().Get("Accept-Ranges") != "" {
		t.Errorf("stream-only store: %d %q (Accept-Ranges %q), want the whole file", rec.Code, rec.Body.String(), rec.Header().Get("Accept-Ranges"))
	}
}

// TestMediaRangeRequestsFromS3 is TestMediaRangeRequests against a real
// S3/MinIO (BLOB_S3_TEST_ENDPOINT etc., skipped otherwise): the object
// store's reader seeks, so the handler answers Range requests with 206 —
// the fix for "video from S3 does not seek / does not play in Safari".
func TestMediaRangeRequestsFromS3(t *testing.T) {
	endpoint := os.Getenv("BLOB_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BLOB_S3_TEST_ENDPOINT not set")
	}
	bucket := os.Getenv("BLOB_S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "archivist-test"
	}
	s3, err := blob.NewS3(blob.S3Config{
		Endpoint: endpoint, Bucket: bucket,
		AccessKey: os.Getenv("BLOB_S3_TEST_ACCESS_KEY"), SecretKey: os.Getenv("BLOB_S3_TEST_SECRET_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s3.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	// mediaFixture stores under the signed-up user's id; remove it afterwards.
	var users []string
	blobs := recordUsers{Blobs: s3, users: &users}
	t.Cleanup(func() {
		for _, u := range users {
			_ = s3.DeleteUser(ctx, u)
		}
	})

	const content = "content of clip.mp4"
	h, c, id := mediaFixture(t, blobs, "clip.mp4", "notes.pdf")
	url := "/api/events/" + id + "/media/0"

	rec := request(h, "GET", url, "", map[string]string{"Range": "bytes=8-9"}, c)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "of" {
		t.Fatalf("range = %d %q, want 206 %q", rec.Code, rec.Body.String(), "of")
	}
	for header, want := range map[string]string{
		"Content-Range":  fmt.Sprintf("bytes 8-9/%d", len(content)),
		"Content-Length": "2",
		"Content-Type":   "video/mp4",
		"Accept-Ranges":  "bytes",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	// An open-ended suffix range and a first-bytes range, as players send.
	if rec := request(h, "GET", url, "", map[string]string{"Range": "bytes=11-"}, c); rec.Code != 206 || rec.Body.String() != "clip.mp4" {
		t.Errorf("open range = %d %q", rec.Code, rec.Body.String())
	}
	if rec := request(h, "GET", url, "", map[string]string{"Range": "bytes=0-9"}, c); rec.Code != 206 || rec.Body.String() != content[:10] {
		t.Errorf("bytes=0-9 = %d %q", rec.Code, rec.Body.String())
	}
	// Beyond the end: 416, not a 500 from the object store's InvalidRange.
	if rec := request(h, "GET", url, "", map[string]string{"Range": "bytes=100-200"}, c); rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("unsatisfiable range = %d %q", rec.Code, rec.Body.String())
	}
	full := request(h, "GET", url, "", nil, c)
	if full.Code != 200 || full.Body.String() != content || full.Header().Get("Accept-Ranges") != "bytes" ||
		full.Header().Get("Content-Length") != strconv.Itoa(len(content)) {
		t.Errorf("full = %d %q, Accept-Ranges %q, Content-Length %q", full.Code, full.Body.String(), full.Header().Get("Accept-Ranges"), full.Header().Get("Content-Length"))
	}
	// A range of a download stays a download, with the hardening headers.
	att := request(h, "GET", "/api/events/"+id+"/media/1", "", map[string]string{"Range": "bytes=0-3"}, c)
	if att.Code != 206 || att.Body.String() != "cont" || att.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(att.Header().Get("Content-Disposition"), "attachment") ||
		att.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Errorf("range of an attachment = %d %q %v", att.Code, att.Body.String(), att.Header())
	}
}

// recordUsers notes which users a store was written for (for cleanup).
type recordUsers struct {
	app.Blobs
	users *[]string
}

func (r recordUsers) Put(ctx context.Context, userID, key string, body io.Reader) error {
	if !slices.Contains(*r.users, userID) {
		*r.users = append(*r.users, userID)
	}
	return r.Blobs.Put(ctx, userID, key, body)
}
