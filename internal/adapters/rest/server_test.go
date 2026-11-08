package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/catalog"
	"github.com/johnnycube/archivist/internal/adapters/crypto"
	"github.com/johnnycube/archivist/internal/adapters/engine"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/adapters/staging"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/domain"
)

var reddit = map[string]string{
	"reddit/comments.csv":   "id,permalink,date,subreddit,link,parent,body\nx1,http://p/1,2023-01-01 00:00:00 UTC,golang,post:a,post:a,Hello there\n",
	"reddit/posts.csv":      "id,permalink,date,subreddit,title,url,body\na,http://p/a,2023-01-02 00:00:00 UTC,golang,Title A,,Body A\n",
	"reddit/post_votes.csv": "id,permalink,direction\na,http://p/a,up\n",
}

// newDepsIn wires the in-memory adapters plus a filesystem staging area in
// uploadDir (free-space margin off: tests must not depend on the disk's state).
func newDepsIn(t *testing.T, uploadDir string) app.Deps {
	t.Helper()
	st, err := staging.NewFS(uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	st.MinFree = 0
	cat := catalog.New()
	return app.Deps{
		Events: memory.NewEvents(), Users: memory.NewUsers(), Accounts: memory.NewAccounts(),
		Hasher: crypto.Hasher{}, Sessions: memory.NewSessions(time.Hour),
		Catalog: cat, Engine: engine.NewWith(cat.Bundler), Jobs: memory.NewJobs(),
		Staging: st, Attempts: memory.NewAttempts(),
	}
}

func newDeps(t *testing.T) app.Deps { return newDepsIn(t, filepath.Join(t.TempDir(), "uploads")) }

func newApp(t *testing.T) *app.App { return app.New(newDeps(t)) }

func newHandler(t *testing.T) http.Handler { return rest.New(newApp(t), false).Handler() }

// stagedFiles counts the uploads currently held under a staging dir.
func stagedFiles(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func signup(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/auth/signup", bytes.NewBufferString(`{"email":"me@x.com","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("signup = %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == rest.CookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func zipBytes(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	archivetest.WriteFiles(src, reddit)
	out := filepath.Join(dir, "e.zip")
	if err := archivetest.ZipDir(src, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	return b
}

func TestUnauthenticated(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(t).ServeHTTP(rec, httptest.NewRequest("GET", "/api/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestSignupImportQuery(t *testing.T) {
	a := newApp(t)
	h := rest.New(a, false).Handler()
	c := signup(t, h)

	// Async import: enqueue returns 202 with a queued job.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("archive", "reddit.zip")
	fw.Write(zipBytes(t))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/import?source=reddit", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	json.Unmarshal(rec.Body.Bytes(), &job)
	if job.Status != "queued" {
		t.Fatalf("status = %q, want queued", job.Status)
	}

	// Drain the worker (in-process; the server would do this in a goroutine).
	for {
		did, err := a.Worker.ProcessNext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			break
		}
	}

	// Job status endpoint reflects completion.
	sreq := httptest.NewRequest("GET", "/api/imports/"+job.ID, nil)
	sreq.AddCookie(c)
	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, sreq)
	var st struct {
		Status string `json:"status"`
		Saved  int    `json:"saved"`
	}
	json.Unmarshal(srec.Body.Bytes(), &st)
	if st.Status != "done" || st.Saved != 3 {
		t.Fatalf("job status = %+v, want done/3", st)
	}

	get := func(url string) int {
		req := httptest.NewRequest("GET", url, nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", url, rec.Code)
		}
		var p struct {
			Total int `json:"total"`
		}
		json.Unmarshal(rec.Body.Bytes(), &p)
		return p.Total
	}
	if n := get("/api/events"); n != 3 {
		t.Errorf("all = %d, want 3", n)
	}
	if n := get("/api/events?category=reaction"); n != 1 {
		t.Errorf("reaction = %d, want 1", n)
	}
	if n := get("/api/events?q=Body%20A"); n != 1 {
		t.Errorf("search = %d, want 1", n)
	}
}

// upload posts the given ZIPs as the "archive" parts of one import.
func upload(t *testing.T, h http.Handler, c *http.Cookie, source string, zips ...[]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, z := range zips {
		fw, _ := mw.CreateFormFile("archive", "part.zip")
		fw.Write(z)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/import?source="+source, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMultiPartUploadReprocessDelete(t *testing.T) {
	uploadDir := t.TempDir()
	a := app.New(newDepsIn(t, uploadDir))
	h := rest.New(a, false).Handler()
	c := signup(t, h)

	call := func(method, url string) (int, map[string]any) {
		req := httptest.NewRequest(method, url, nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	drain := func() {
		for {
			if did, err := a.Worker.ProcessNext(context.Background()); err != nil || !did {
				return
			}
		}
	}

	rec := upload(t, h, c, "reddit", zipBytes(t), zipBytes(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	var job struct {
		ID    string `json:"id"`
		Parts int    `json:"parts"`
	}
	json.Unmarshal(rec.Body.Bytes(), &job)
	if job.Parts != 2 {
		t.Errorf("parts = %d, want 2", job.Parts)
	}
	if n := stagedFiles(uploadDir); n != 2 {
		t.Errorf("staged files = %d, want 2", n)
	}
	// The client's own file names come back; where the server staged the parts
	// does not.
	var named struct {
		PartNames []string `json:"part_names"`
	}
	json.Unmarshal(rec.Body.Bytes(), &named)
	if len(named.PartNames) != 2 || named.PartNames[0] != "part.zip" || named.PartNames[1] != "part.zip" {
		t.Errorf("part_names = %q, want the two upload names", named.PartNames)
	}
	leaks := []string{uploadDir, "user_id", "zip_paths", "ZipPaths"}
	filepath.WalkDir(uploadDir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			leaks = append(leaks, d.Name()) // the staged file's random name = its ref
		}
		return nil
	})
	for _, leak := range leaks {
		if bytes.Contains(rec.Body.Bytes(), []byte(leak)) {
			t.Errorf("import response leaks %q: %s", leak, rec.Body.String())
		}
	}

	if code, _ := call("POST", "/api/imports/"+job.ID+"/reprocess"); code != http.StatusConflict {
		t.Errorf("reprocess while queued = %d, want 409", code)
	}
	drain()
	// No blob store in this app, so the uploads are disposable once processed.
	if n := stagedFiles(uploadDir); n != 0 {
		t.Errorf("staged files after the run = %d, want 0", n)
	}
	if code, _ := call("POST", "/api/imports/nope/reprocess"); code != http.StatusNotFound {
		t.Errorf("reprocess of an unknown job = %d, want 404", code)
	}
	if code, _ := call("POST", "/api/imports/"+job.ID+"/cancel"); code != http.StatusConflict {
		t.Errorf("cancel of a finished import = %d, want 409", code)
	}
	if code, _ := call("POST", "/api/imports/nope/cancel"); code != http.StatusNotFound {
		t.Errorf("cancel of an unknown job = %d, want 404", code)
	}

	code, out := call("DELETE", "/api/imports/"+job.ID)
	if code != http.StatusOK || out["deleted_events"] != float64(3) {
		t.Errorf("delete = %d %v, want 200 with 3 deleted events", code, out)
	}
	if code, _ := call("GET", "/api/imports/"+job.ID); code != http.StatusNotFound {
		t.Errorf("deleted job status = %d, want 404", code)
	}
	if _, out := call("GET", "/api/events"); out["total"] != float64(0) {
		t.Errorf("events after delete = %v, want 0", out["total"])
	}
}

func TestUploadRejections(t *testing.T) {
	uploadDir := t.TempDir()
	srv := rest.New(app.New(newDepsIn(t, uploadDir)), false)
	srv.MaxZip = 1 << 10
	h := srv.Handler()
	c := signup(t, h)

	if rec := upload(t, h, c, "reddit", make([]byte, 4<<10)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload = %d, want 413", rec.Code)
	}
	if rec := upload(t, h, c, "nope", []byte("x")); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown source = %d, want 400", rec.Code)
	}
	if rec := upload(t, h, c, "reddit"); rec.Code != http.StatusBadRequest {
		t.Errorf("upload without parts = %d, want 400", rec.Code)
	}
	if n := stagedFiles(uploadDir); n != 0 {
		t.Errorf("rejected uploads left %d staged files behind", n)
	}
}

// drain runs the import worker until the queue is empty.
func drain(t *testing.T, a *app.App) {
	t.Helper()
	for {
		did, err := a.Worker.ProcessNext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
}

// getJSON GETs url as the signed-in user and decodes the 200 response.
func getJSON(t *testing.T, h http.Handler, c *http.Cookie, url string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", url, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return out
}

// seedEvents writes events straight into the store for the signed-in user.
func seedEvents(t *testing.T, h http.Handler, c *http.Cookie, events app.EventRepository, evs ...domain.Event) {
	t.Helper()
	uid, _ := getJSON(t, h, c, "/api/auth/me")["id"].(string)
	if uid == "" {
		t.Fatal("no user id")
	}
	if _, err := events.SaveBatch(context.Background(), uid, "11111111-1111-4111-8111-111111111111", app.ImportRun{}, evs); err != nil {
		t.Fatal(err)
	}
}

// GET /api/events?collapse=1 answers a page of mixed items — clusters and
// events, flat objects told apart by "kind" — whose cursor continues the
// timeline after the last item's last event; without collapse the response is
// the plain page it always was.
func TestCollapsedEventsPage(t *testing.T) {
	deps := newDeps(t)
	a := app.New(deps)
	h := rest.New(a, false).Handler()
	c := signup(t, h)

	at := time.Date(2023, 5, 1, 12, 0, 0, 0, time.UTC)
	var evs []domain.Event
	for i := 0; i < 5; i++ { // five plays a minute apart, newest first
		evs = append(evs, domain.Event{
			SourceID: "spotify", ExternalID: fmt.Sprintf("p%d", i), Type: "spotify.play",
			Category: domain.CategoryConsumption, OccurredAt: at.Add(-time.Duration(i) * time.Minute), Title: fmt.Sprintf("track %d", i%2),
		})
	}
	evs = append(evs, domain.Event{
		SourceID: "reddit", ExternalID: "post", Type: "reddit.post", Category: domain.CategoryContent,
		OccurredAt: at.Add(-time.Hour), Title: "A post",
	})
	evs = append(evs, domain.Event{
		SourceID: "spotify", ExternalID: "late", Type: "spotify.play", Category: domain.CategoryConsumption,
		OccurredAt: at.Add(-2 * time.Hour), Title: "lone track",
	})
	seedEvents(t, h, c, deps.Events, evs...)

	page := getJSON(t, h, c, "/api/events?collapse=1&limit=2")
	items, _ := page["items"].([]any)
	if len(items) != 2 || page["total"] != float64(7) || page["next_cursor"] == "" || page["events"] != nil {
		t.Fatalf("collapsed page = %v", page)
	}
	cluster, _ := items[0].(map[string]any)
	if cluster["kind"] != "cluster" || cluster["count"] != float64(5) || cluster["source_id"] != "spotify" ||
		cluster["type"] != "spotify.play" || cluster["category"] != "consumption" {
		t.Errorf("first item = %v, want the cluster of five plays", cluster)
	}
	if titles, _ := cluster["sample_titles"].([]any); len(titles) != 2 || titles[0] != "track 0" || titles[1] != "track 1" {
		t.Errorf("sample_titles = %v", cluster["sample_titles"])
	}
	if cluster["first_at"].(string) <= cluster["last_at"].(string) || cluster["first_id"] == cluster["last_id"] {
		t.Errorf("cluster bounds = %v..%v (%v..%v): first is the newest member", cluster["first_at"], cluster["last_at"], cluster["first_id"], cluster["last_id"])
	}
	for _, k := range []string{"user_id", "account_id", "id", "events"} {
		if _, ok := cluster[k]; ok {
			t.Errorf("cluster item carries %q", k)
		}
	}
	post, _ := items[1].(map[string]any)
	if post["kind"] != "event" || post["external_id"] != "post" || post["title"] != "A post" {
		t.Errorf("second item = %v, want the post as an event", post)
	}

	// The cursor continues after the post: one more item, then the end.
	next := getJSON(t, h, c, "/api/events?collapse=1&limit=2&cursor="+page["next_cursor"].(string))
	rest, _ := next["items"].([]any)
	if len(rest) != 1 || rest[0].(map[string]any)["external_id"] != "late" || next["next_cursor"] != nil {
		t.Errorf("next page = %v, want only the lone play and no cursor", next)
	}

	// A gap smaller than a minute leaves nothing to cluster.
	loose := getJSON(t, h, c, "/api/events?collapse=1&collapse_gap=30s")
	if items, _ := loose["items"].([]any); len(items) != 7 || items[0].(map[string]any)["kind"] != "event" {
		t.Errorf("gap 30s: %d items, first %v; want 7 events", len(items), items[0])
	}

	// Without collapse: the plain page, events only, unchanged shape.
	plain := getJSON(t, h, c, "/api/events?limit=2")
	if evs, _ := plain["events"].([]any); len(evs) != 2 || plain["items"] != nil || evs[0].(map[string]any)["kind"] != nil {
		t.Errorf("plain page = %v", plain)
	}

	// Histogram by category vs by source.
	bySrc := getJSON(t, h, c, "/api/events/histogram")["buckets"].([]any)[0].(map[string]any)
	byCat := getJSON(t, h, c, "/api/events/histogram?by=category")["buckets"].([]any)[0].(map[string]any)
	if bySrc["by_source"].(map[string]any)["spotify"] != float64(6) || bySrc["by_category"] != nil {
		t.Errorf("histogram by source = %v", bySrc)
	}
	if byCat["by_category"].(map[string]any)["consumption"] != float64(6) || byCat["by_category"].(map[string]any)["content"] != float64(1) || byCat["by_source"] != nil {
		t.Errorf("histogram by category = %v", byCat)
	}
}

func TestCancelQueuedImport(t *testing.T) {
	a := newApp(t)
	h := rest.New(a, false).Handler()
	c := signup(t, h)
	rec := upload(t, h, c, "reddit", zipBytes(t))
	var job struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/imports/"+job.ID+"/cancel", nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got struct {
		Status, Error string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Status != "error" || !strings.Contains(got.Error, "Cancelled") {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if did, err := a.Worker.ProcessNext(context.Background()); did || err != nil {
		t.Errorf("worker claimed a cancelled import: %v %v", did, err)
	}
}
