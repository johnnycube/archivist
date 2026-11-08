package rest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/adapters/staging"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/seed"
)

func postJSON(h http.Handler, url, body string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSignupDisabled(t *testing.T) {
	deps := newDeps(t)
	deps.SignupDisabled = true
	h := rest.New(app.New(deps), false).Handler()
	if rec := postJSON(h, "/api/auth/signup", `{"email":"me@x.com","password":"password123"}`, nil); rec.Code != http.StatusForbidden {
		t.Errorf("signup on a closed server = %d, want 403", rec.Code)
	}
}

// The 409 on a taken email is an enumeration oracle by design (UX); the per-IP
// rate limit on the route is what bounds it.
func TestSignupEnumerationIsRateLimited(t *testing.T) {
	h := newHandler(t)
	signup(t, h)
	var last int
	for i := 0; i < 12; i++ {
		last = postJSON(h, "/api/auth/signup", `{"email":"me@x.com","password":"password123"}`, nil).Code
		if last == http.StatusTooManyRequests {
			break
		}
		if last != http.StatusConflict {
			t.Fatalf("duplicate signup = %d, want 409", last)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("probing for registered emails was never rate limited (last status %d)", last)
	}
}

func TestImportQuotaStatuses(t *testing.T) {
	uploadDir := t.TempDir()
	deps := newDepsIn(t, uploadDir)
	deps.Limits = app.Limits{MaxActiveImports: 1}
	h := rest.New(app.New(deps), false).Handler()
	c := signup(t, h)
	if rec := upload(t, h, c, "reddit", zipBytes(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("first import = %d: %s", rec.Code, rec.Body.String())
	}
	rec := upload(t, h, c, "reddit", zipBytes(t))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("import over the active-imports quota = %d (Retry-After %q), want 429", rec.Code, rec.Header().Get("Retry-After"))
	}
	if n := stagedFiles(uploadDir); n != 1 {
		t.Errorf("staged files = %d, want only the accepted import's", n)
	}

	uploadDir = t.TempDir()
	deps = newDepsIn(t, uploadDir)
	deps.Limits = app.Limits{MaxArchiveBytes: 16}
	h = rest.New(app.New(deps), false).Handler()
	c = signup(t, h)
	if rec := upload(t, h, c, "reddit", zipBytes(t)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("import over the storage quota = %d, want 413", rec.Code)
	}
	if n := stagedFiles(uploadDir); n != 0 {
		t.Errorf("refused import left %d staged files behind", n)
	}
}

func TestImportRefusedWhenDiskIsFull(t *testing.T) {
	deps := newDeps(t)
	deps.Staging.(*staging.FS).MinFree = math.MaxInt64
	if err := deps.Staging.CheckSpace(context.Background(), 0); err == nil {
		t.Skip("no statfs on this platform")
	}
	h := rest.New(app.New(deps), false).Handler()
	c := signup(t, h)
	if rec := upload(t, h, c, "reddit", zipBytes(t)); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("import onto a full disk = %d, want 507", rec.Code)
	}
}

// An upload without a declared length is still capped while streaming.
func TestChunkedUploadIsCapped(t *testing.T) {
	uploadDir := t.TempDir()
	srv := rest.New(app.New(newDepsIn(t, uploadDir)), false)
	srv.MaxZip = 1 << 10
	h := srv.Handler()
	c := signup(t, h)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("archive", "part.zip")
	fw.Write(make([]byte, 4<<10))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/import?source=reddit", io.NopCloser(&body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized chunked upload = %d, want 413", rec.Code)
	}
	if n := stagedFiles(uploadDir); n != 0 {
		t.Errorf("capped upload left %d staged files behind", n)
	}
}

type brokenJobs struct{ app.Jobs }

var errSecret = errors.New(`pq: connection to "db.internal:5432" refused (password=hunter2)`)

func (brokenJobs) CreateWithin(context.Context, domain.ImportJob, app.Limits) (domain.ImportJob, error) {
	return domain.ImportJob{}, errSecret
}

func (brokenJobs) List(context.Context, string, int) ([]domain.ImportJob, error) {
	return nil, errSecret
}

// Infrastructure failures are 500s (not 400s), are logged, and never echo the
// cause to the client.
func TestServerErrorsAreLoggedNotLeaked(t *testing.T) {
	uploadDir := t.TempDir()
	deps := newDepsIn(t, uploadDir)
	deps.Jobs = brokenJobs{deps.Jobs}
	var logs bytes.Buffer
	srv := rest.New(app.New(deps), false)
	srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	h := srv.Handler()
	c := signup(t, h)

	rec := upload(t, h, c, "reddit", zipBytes(t))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("import during a store outage = %d, want 500", rec.Code)
	}
	req := httptest.NewRequest("GET", "/api/imports", nil)
	req.AddCookie(c)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusInternalServerError {
		t.Errorf("list during a store outage = %d, want 500", rec2.Code)
	}
	for _, body := range []string{rec.Body.String(), rec2.Body.String()} {
		if strings.Contains(body, "hunter2") || strings.Contains(body, "db.internal") {
			t.Errorf("500 response leaks the cause: %s", body)
		}
	}
	if !strings.Contains(logs.String(), "hunter2") {
		t.Errorf("the cause was not logged; log:\n%s", logs.String())
	}
	if n := stagedFiles(uploadDir); n != 0 {
		t.Errorf("failed enqueue left %d staged files behind", n)
	}
}

func TestAccountDeleteRefusedWhileImporting(t *testing.T) {
	a := newApp(t)
	h := rest.New(a, false).Handler()
	c := signup(t, h)
	if rec := upload(t, h, c, "reddit", zipBytes(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d", rec.Code)
	}
	if rec := postJSON(h, "/api/auth/delete", `{"password":"password123"}`, c); rec.Code != http.StatusConflict {
		t.Errorf("account delete with a queued import = %d, want 409", rec.Code)
	}
	if did, err := a.Worker.ProcessNext(context.Background()); err != nil || !did {
		t.Fatalf("ProcessNext did=%v err=%v", did, err)
	}
	if rec := postJSON(h, "/api/auth/delete", `{"password":"password123"}`, c); rec.Code != http.StatusOK {
		t.Errorf("account delete = %d: %s", rec.Code, rec.Body.String())
	}
}

// A cursor is client input: a malformed one is the client's mistake (400), not
// a server fault, and must never reach the store.
func TestBadCursorIsABadRequest(t *testing.T) {
	h := newHandler(t)
	c := signup(t, h)
	for _, cursor := range []string{"garbage", "bm90LWEtY3Vyc29y", "%00"} {
		req := httptest.NewRequest(http.MethodGet, "/api/events?cursor="+cursor, nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("cursor %q: status = %d, want 400 (body %s)", cursor, rec.Code, rec.Body)
		}
	}
}

// The demo seed uses a deliberately trivial login ("test"/"test"): not a valid
// email and shorter than the signup minimum. The password policy guards signup
// and password changes only; it must never lock an existing account out.
func TestSeededDemoAccountCanLogIn(t *testing.T) {
	deps := newDeps(t)
	if _, err := seed.Demo(context.Background(), seed.Deps{Users: deps.Users, Hasher: deps.Hasher, Accounts: deps.Accounts, Events: deps.Events}); err != nil {
		t.Fatal(err)
	}
	h := rest.New(app.New(deps), false).Handler()

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	creds := `{"email":"` + seed.DemoEmail + `","password":"` + seed.DemoPassword + `"}`
	if rec := post("/api/auth/login", creds); rec.Code != http.StatusOK {
		t.Fatalf("demo login = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	// …while the same credentials are still refused for a NEW account.
	if rec := post("/api/auth/signup", `{"email":"someone@example.com","password":"`+seed.DemoPassword+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("signup with the demo password = %d, want 400", rec.Code)
	}
}
