package blob

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 speaks just enough of the S3 REST API for minio-go: bucket
// HEAD/PUT, object HEAD/GET/PUT/DELETE (incl. If-None-Match: * and the
// aws-chunked streaming-signature body minio-go uses over plain HTTP), the
// three multipart calls, paginated ListObjectsV2, and multi-object delete.
type fakeS3 struct {
	t *testing.T

	mu         sync.Mutex
	buckets    map[string]bool
	objects    map[string][]byte         // "/bucket/key" → content
	uploads    map[string]map[int][]byte // uploadID → part number → content
	puts       int                       // completed object writes (single + multipart)
	multiparts int                       // completed multipart uploads
	aborted    int
	pageSize   int // ListObjectsV2 page size (small, to exercise continuation)

	// gets records every object GET as "<Range header>" ("" = whole object);
	// heads counts object HEADs. checksummed counts uploads (single PUTs and
	// parts) that carried a CRC32C checksum; sse records the
	// x-amz-server-side-encryption header of every object PUT.
	gets        []string
	heads       int
	checksummed int
	sse         []string
	// reject, when set, answers every request with this S3 error code
	// (simulates bad credentials).
	reject string
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	f := &fakeS3{t: t, buckets: map[string]bool{}, objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}, pageSize: 2}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// hasChecksum reports whether an upload request carries a CRC32C checksum,
// either as a header or as a trailer announced by X-Amz-Trailer (the
// streaming form minio-go uses with TrailingHeaders enabled).
func hasChecksum(r *http.Request) bool {
	return r.Header.Get("X-Amz-Checksum-Crc32c") != "" ||
		strings.EqualFold(r.Header.Get("X-Amz-Trailer"), "x-amz-checksum-crc32c")
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

// body returns the request payload, decoding aws-chunked framing
// ("<hex-size>;chunk-signature=…\r\n<data>\r\n" … "0;chunk-signature=…\r\n\r\n").
func (f *fakeS3) body(r *http.Request) []byte {
	if !strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		b, _ := io.ReadAll(r.Body)
		return b
	}
	br := bufio.NewReader(r.Body)
	var out bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			f.t.Errorf("aws-chunked: header: %v", err)
			return nil
		}
		hexSize, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(hexSize, 16, 64)
		if err != nil {
			f.t.Errorf("aws-chunked: size %q: %v", line, err)
			return nil
		}
		if size == 0 {
			break
		}
		if _, err := io.CopyN(&out, br, size); err != nil {
			f.t.Errorf("aws-chunked: data: %v", err)
			return nil
		}
		br.Discard(2) // trailing CRLF
	}
	if want := r.Header.Get("X-Amz-Decoded-Content-Length"); want != strconv.Itoa(out.Len()) {
		f.t.Errorf("aws-chunked: decoded %d bytes, header says %s", out.Len(), want)
	}
	return out.Bytes()
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.Header.Get("X-Amz-Date") == "" {
		f.t.Errorf("unsigned request: %s %s", r.Method, r.URL)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.reject != "" {
		s3Error(w, http.StatusForbidden, f.reject)
		return
	}

	path := r.URL.Path
	q := r.URL.Query()
	bucket, key, isObject := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	isObject = isObject && key != ""

	if !isObject {
		switch {
		case r.Method == http.MethodHead:
			if !f.buckets[bucket] {
				w.WriteHeader(http.StatusNotFound)
			}
		case r.Method == http.MethodPut:
			f.buckets[bucket] = true
		case r.Method == http.MethodGet && q.Get("list-type") == "2":
			f.list(w, bucket, q.Get("prefix"), q.Get("continuation-token"))
		case r.Method == http.MethodPost && q.Has("delete"):
			f.multiDelete(w, r, bucket)
		default:
			f.t.Errorf("fake s3: unsupported bucket request %s %s", r.Method, r.URL)
			s3Error(w, http.StatusNotImplemented, "NotImplemented")
		}
		return
	}
	if !f.buckets[bucket] {
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
		return
	}

	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		id := fmt.Sprintf("upload-%d", len(f.uploads)+1)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, bucket, key, id)

	case r.Method == http.MethodPut && q.Has("uploadId"):
		parts, ok := f.uploads[q.Get("uploadId")]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchUpload")
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		if hasChecksum(r) {
			f.checksummed++
		}
		parts[n] = f.body(r)
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, n))

	case r.Method == http.MethodPost && q.Has("uploadId"):
		parts, ok := f.uploads[q.Get("uploadId")]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchUpload")
			return
		}
		var req struct {
			Parts []struct {
				PartNumber int
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("complete multipart: %v", err)
		}
		var whole []byte
		for _, p := range req.Parts {
			whole = append(whole, parts[p.PartNumber]...)
		}
		delete(f.uploads, q.Get("uploadId"))
		f.objects[path] = whole
		f.puts++
		f.multiparts++
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"etag-mp"</ETag></CompleteMultipartUploadResult>`, bucket, key)

	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(f.uploads, q.Get("uploadId"))
		f.aborted++
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPut:
		if _, exists := f.objects[path]; exists && r.Header.Get("If-None-Match") == "*" {
			s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if hasChecksum(r) {
			f.checksummed++
		}
		f.sse = append(f.sse, r.Header.Get("X-Amz-Server-Side-Encryption"))
		f.objects[path] = f.body(r)
		f.puts++
		w.Header().Set("ETag", `"etag"`)

	case r.Method == http.MethodHead, r.Method == http.MethodGet:
		if r.Method == http.MethodHead {
			f.heads++
		}
		b, ok := f.objects[path]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
			} else {
				s3Error(w, http.StatusNotFound, "NoSuchKey")
			}
			return
		}
		w.Header().Set("ETag", `"etag"`)
		w.Header().Set("Last-Modified", time.Unix(0, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			return
		}
		f.gets = append(f.gets, r.Header.Get("Range"))
		// "bytes=<start>-[<end>]": what minio-go's Object sends after a Seek.
		if rng := r.Header.Get("Range"); rng != "" {
			spec := strings.TrimPrefix(rng, "bytes=")
			startS, endS, _ := strings.Cut(spec, "-")
			start, _ := strconv.Atoi(startS)
			end := len(b) - 1
			if endS != "" {
				end, _ = strconv.Atoi(endS)
				end = min(end, len(b)-1)
			}
			if start > end || start >= len(b) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(b)))
				s3Error(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(b)))
			w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(b[start : end+1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)

	case r.Method == http.MethodDelete:
		delete(f.objects, path)
		w.WriteHeader(http.StatusNoContent)

	default:
		f.t.Errorf("fake s3: unsupported object request %s %s", r.Method, r.URL)
		s3Error(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func (f *fakeS3) list(w http.ResponseWriter, bucket, prefix, token string) {
	var keys []string
	for p := range f.objects {
		k, ok := strings.CutPrefix(p, "/"+bucket+"/")
		if ok && strings.HasPrefix(k, prefix) && k > token {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	truncated := len(keys) > f.pageSize
	if truncated {
		keys = keys[:f.pageSize]
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<ListBucketResult><Name>%s</Name><Prefix>%s</Prefix><IsTruncated>%t</IsTruncated>`, bucket, prefix, truncated)
	if truncated {
		fmt.Fprintf(&b, `<NextContinuationToken>%s</NextContinuationToken>`, keys[len(keys)-1])
	}
	for _, k := range keys {
		fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size><ETag>"etag"</ETag><LastModified>1970-01-01T00:00:00.000Z</LastModified></Contents>`, k, len(f.objects["/"+bucket+"/"+k]))
	}
	b.WriteString(`</ListBucketResult>`)
	io.WriteString(w, b.String())
}

func (f *fakeS3) multiDelete(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Header.Get("Content-Md5") == "" {
		f.t.Errorf("multi-delete without Content-MD5")
	}
	var req struct {
		Objects []struct{ Key string } `xml:"Object"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("multi-delete: %v", err)
	}
	var b strings.Builder
	b.WriteString(`<DeleteResult>`)
	for _, o := range req.Objects {
		delete(f.objects, "/"+bucket+"/"+o.Key)
		fmt.Fprintf(&b, `<Deleted><Key>%s</Key></Deleted>`, o.Key)
	}
	b.WriteString(`</DeleteResult>`)
	io.WriteString(w, b.String())
}

func (f *fakeS3) snapshot() (puts, multiparts, objects int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts, f.multiparts, len(f.objects)
}

func newTestS3(t *testing.T) (*S3, *fakeS3) {
	t.Helper()
	fake, srv := newFakeS3(t)
	s3, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "test-bucket", AccessKey: "ak", SecretKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s3.EnsureBucket(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	return s3, fake
}

func readAll(t *testing.T, s3 *S3, userID, key string) []byte {
	t.Helper()
	rc, err := s3.Open(context.Background(), userID, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestS3ConfigValidation(t *testing.T) {
	ok := S3Config{Endpoint: "http://minio:9000", Bucket: "b", AccessKey: "ak", SecretKey: "sk"}
	if _, err := NewS3(ok); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for name, mutate := range map[string]func(*S3Config){
		"no endpoint":   func(c *S3Config) { c.Endpoint = "" },
		"no scheme":     func(c *S3Config) { c.Endpoint = "minio:9000" },
		"bad scheme":    func(c *S3Config) { c.Endpoint = "ftp://minio:9000" },
		"endpoint path": func(c *S3Config) { c.Endpoint = "http://minio:9000/prefix" },
		"no bucket":     func(c *S3Config) { c.Bucket = "" },
		"no secret":     func(c *S3Config) { c.SecretKey = "" },
	} {
		cfg := ok
		mutate(&cfg)
		if _, err := NewS3(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestS3ObjectLayout pins the object key layout byte-for-byte: buckets written
// by the previous (hand-rolled) client must stay readable.
func TestS3ObjectLayout(t *testing.T) {
	// sha256("media/photo.jpg"), computed independently with sha256sum.
	const want = "user-1/75/752db625f12712fda2f76592d861ba2253aa94e3c0f663f6d8fd389ee71d919d"
	if got := objectName("user-1", "media/photo.jpg"); got != want {
		t.Errorf("objectName = %q, want %q", got, want)
	}
	if got := objectName("../evil", "k"); !strings.HasPrefix(got, "___evil/") {
		t.Errorf("user id not sanitized: %q", got)
	}
}

// TestS3WriteOnce drives the adapter against the fake endpoint and asserts the
// immutability contract, 404 → fs.ErrNotExist, tenant isolation and Delete.
func TestS3WriteOnce(t *testing.T) {
	s3, fake := newTestS3(t)
	ctx := context.Background()

	if err := s3.Put(ctx, "user-1", "media/photo.jpg", strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	// Same key again with different content: must be a no-op.
	if err := s3.Put(ctx, "user-1", "media/photo.jpg", strings.NewReader("MUTATED")); err != nil {
		t.Fatal(err)
	}
	if puts, multiparts, _ := fake.snapshot(); puts != 1 || multiparts != 0 {
		t.Errorf("puts = %d (multipart %d), want 1 single PUT (write-once)", puts, multiparts)
	}
	if got := readAll(t, s3, "user-1", "media/photo.jpg"); string(got) != "original" {
		t.Errorf("content = %q, want the original (immutable)", got)
	}
	if _, ok := fake.objects["/test-bucket/"+objectName("user-1", "media/photo.jpg")]; !ok {
		t.Errorf("object not stored under the documented layout; have %v", fake.objects)
	}

	if _, err := s3.Open(ctx, "user-1", "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing object err = %v, want fs.ErrNotExist", err)
	}
	// Tenant isolation: same key, different user → different object.
	if _, err := s3.Open(ctx, "user-2", "media/photo.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cross-user open err = %v, want fs.ErrNotExist", err)
	}

	// Empty objects are legal.
	if err := s3.Put(ctx, "user-1", "empty", strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s3, "user-1", "empty"); len(got) != 0 {
		t.Errorf("empty object read back %d bytes", len(got))
	}

	if err := s3.Delete(ctx, "user-1", "media/photo.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Open(ctx, "user-1", "media/photo.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after delete err = %v, want fs.ErrNotExist", err)
	}
	if err := s3.Delete(ctx, "user-1", "media/photo.jpg"); err != nil {
		t.Errorf("deleting a missing key must be a no-op, got %v", err)
	}
}

// TestS3PutRace covers the stat→put window: the object appears after the
// adapter's existence check, and the conditional PUT must not overwrite it.
func TestS3PutRace(t *testing.T) {
	s3, fake := newTestS3(t)
	racer := readerFunc(func(p []byte) (int, error) {
		fake.mu.Lock()
		fake.objects["/test-bucket/"+objectName("user-1", "k")] = []byte("winner")
		fake.mu.Unlock()
		return 0, io.EOF
	})
	if err := s3.Put(context.Background(), "user-1", "k", io.MultiReader(strings.NewReader("loser"), racer)); err != nil {
		t.Fatalf("losing the race must be a no-op, got %v", err)
	}
	if got := readAll(t, s3, "user-1", "k"); string(got) != "winner" {
		t.Errorf("content = %q, want the first writer's", got)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// TestS3MultipartStreaming pushes an object larger than one part through Put
// from a non-seekable reader of unknown length and checks it round-trips via
// the multipart calls, and that a failing source aborts the upload.
func TestS3MultipartStreaming(t *testing.T) {
	s3, fake := newTestS3(t)
	s3.partSize = 5 << 20 // S3's minimum part size keeps the test small
	ctx := context.Background()

	want := make([]byte, 11<<20+123) // 3 parts: 5 + 5 + 1.0001 MiB
	rand.Read(want)
	// io.LimitReader hides Seek/Len so the adapter cannot learn the size.
	if err := s3.Put(ctx, "user-1", "archives/big.zip", io.LimitReader(bytes.NewReader(want), int64(len(want)))); err != nil {
		t.Fatal(err)
	}
	if puts, multiparts, _ := fake.snapshot(); puts != 1 || multiparts != 1 {
		t.Errorf("puts = %d, multipart = %d; want one multipart upload", puts, multiparts)
	}
	if got := readAll(t, s3, "user-1", "archives/big.zip"); !bytes.Equal(got, want) {
		t.Errorf("multipart round-trip mismatch: got %d bytes, want %d", len(got), len(want))
	}

	// Exactly one part's worth of data stays a single PUT.
	if err := s3.Put(ctx, "user-1", "exact", bytes.NewReader(want[:5<<20])); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s3, "user-1", "exact"); !bytes.Equal(got, want[:5<<20]) {
		t.Errorf("exact-part round-trip mismatch")
	}

	// A source that fails mid-stream must not leave an object behind.
	boom := errors.New("boom")
	failing := io.MultiReader(bytes.NewReader(want[:6<<20]), readerFunc(func([]byte) (int, error) { return 0, boom }))
	if err := s3.Put(ctx, "user-1", "broken", failing); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the source error", err)
	}
	if _, err := s3.Open(ctx, "user-1", "broken"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("failed upload left an object: err = %v", err)
	}
	fake.mu.Lock()
	if fake.aborted != 1 || len(fake.uploads) != 0 {
		t.Errorf("aborted = %d, dangling uploads = %d; want the failed upload aborted", fake.aborted, len(fake.uploads))
	}
	fake.mu.Unlock()
}

func TestS3DeleteUser(t *testing.T) {
	s3, fake := newTestS3(t)
	ctx := context.Background()

	for i := range 5 { // > 2 list pages
		if err := s3.Put(ctx, "user-1", fmt.Sprintf("k%d", i), strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	// "user-10" shares the string prefix "user-1" and must survive.
	if err := s3.Put(ctx, "user-10", "keep", strings.NewReader("mine")); err != nil {
		t.Fatal(err)
	}

	if err := s3.DeleteUser(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, n := fake.snapshot(); n != 1 {
		t.Errorf("%d objects left, want only user-10's", n)
	}
	if got := readAll(t, s3, "user-10", "keep"); string(got) != "mine" {
		t.Errorf("other tenant's object damaged: %q", got)
	}
	if err := s3.DeleteUser(ctx, "user-1"); err != nil {
		t.Errorf("deleting a user without objects must be a no-op, got %v", err)
	}
	if err := s3.DeleteUser(ctx, ""); err == nil {
		t.Error("empty user id must be rejected")
	}
}

// TestS3OpenIsSeekable pins what http.ServeContent relies on: Open hands out
// an io.ReadSeeker whose size is known from Open's HEAD alone, whose Seek+Read
// turns into ONE ranged GET, and whose sequential read is ONE plain GET — no
// download ahead of the caller either way.
func TestS3OpenIsSeekable(t *testing.T) {
	s3, fake := newTestS3(t)
	ctx := context.Background()
	content := make([]byte, 1000)
	rand.Read(content)
	if err := s3.Put(ctx, "user-1", "clip.mp4", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	reset := func() {
		fake.mu.Lock()
		fake.gets, fake.heads = nil, 0
		fake.mu.Unlock()
	}
	requests := func() (gets []string, heads int) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return append([]string(nil), fake.gets...), fake.heads
	}

	reset()
	rc, err := s3.Open(ctx, "user-1", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		t.Fatalf("Open returned %T, want an io.ReadSeeker", rc)
	}
	if _, ok := rc.(io.ReaderAt); !ok {
		t.Errorf("Open returned %T, want an io.ReaderAt too", rc)
	}
	// ServeContent's opening move: size via Seek to the end, then back.
	if size, err := rs.Seek(0, io.SeekEnd); err != nil || size != int64(len(content)) {
		t.Fatalf("Seek(0, End) = %d, %v; want %d", size, err, len(content))
	}
	if gets, heads := requests(); len(gets) != 0 || heads != 1 {
		t.Errorf("after Open + Seek(End): GETs %v, HEADs %d; want no GET and the single HEAD from Open", gets, heads)
	}
	if _, err := rs.Seek(600, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 100)
	if _, err := io.ReadFull(rs, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content[600:700]) {
		t.Error("ranged read returned the wrong bytes")
	}
	if gets, _ := requests(); len(gets) != 1 || gets[0] != "bytes=600-" {
		t.Errorf("Seek+Read issued GETs %v, want exactly one ranged GET from 600", gets)
	}
	// Reading on to the end is served from the same response.
	rest, err := io.ReadAll(rs)
	if err != nil || !bytes.Equal(rest, content[700:]) {
		t.Errorf("tail read: %d bytes, %v", len(rest), err)
	}
	if gets, _ := requests(); len(gets) != 1 {
		t.Errorf("continuing the read issued extra GETs: %v", gets)
	}

	// Plain streaming (the import worker): one HEAD, one whole-object GET.
	reset()
	if got := readAll(t, s3, "user-1", "clip.mp4"); !bytes.Equal(got, content) {
		t.Error("sequential read mismatch")
	}
	if gets, heads := requests(); len(gets) != 1 || gets[0] != "" || heads != 1 {
		t.Errorf("sequential read: GETs %v, HEADs %d; want one HEAD and one unranged GET", gets, heads)
	}

	// The missing-key check costs exactly one round trip (the HEAD).
	reset()
	if _, err := s3.Open(ctx, "user-1", "nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing key err = %v", err)
	}
	if gets, heads := requests(); len(gets) != 0 || heads != 1 {
		t.Errorf("missing key: GETs %v, HEADs %d; want one HEAD only", gets, heads)
	}
}

// TestS3Integrity checks that every upload — single PUT and each multipart
// part — carries a CRC32C checksum the server verifies.
func TestS3Integrity(t *testing.T) {
	s3, fake := newTestS3(t)
	s3.partSize = 5 << 20
	ctx := context.Background()
	if err := s3.Put(ctx, "user-1", "small", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 10<<20+1) // 3 parts
	if err := s3.Put(ctx, "user-1", "big", io.LimitReader(bytes.NewReader(big), int64(len(big)))); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.checksummed != 4 {
		t.Errorf("checksummed uploads = %d, want 4 (1 PUT + 3 parts)", fake.checksummed)
	}
	if len(fake.sse) != 1 || fake.sse[0] != "" {
		t.Errorf("SSE headers without SSE configured = %q, want none", fake.sse)
	}
}

// TestS3SSE checks the opt-in SSE-S3 header on uploads.
func TestS3SSE(t *testing.T) {
	fake, srv := newFakeS3(t)
	s3, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "sse-bucket", AccessKey: "ak", SecretKey: "sk", SSE: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s3.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s3.Put(ctx, "user-1", "k", strings.NewReader("secret")); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sse) != 1 || fake.sse[0] != "AES256" {
		t.Errorf("x-amz-server-side-encryption = %q, want [AES256]", fake.sse)
	}
}

// TestS3CheckAndHealthy covers the boot-time check and the readiness probe:
// an unreachable endpoint and rejected credentials are reported as such, and
// a bucket that vanished makes the store unhealthy.
func TestS3CheckAndHealthy(t *testing.T) {
	ctx := context.Background()

	s3, fake := newTestS3(t)
	if err := s3.Check(ctx); err != nil {
		t.Errorf("Check on a working store: %v", err)
	}
	if err := s3.Healthy(ctx); err != nil {
		t.Errorf("Healthy on a working store: %v", err)
	}
	fake.mu.Lock()
	fake.reject = "InvalidAccessKeyId"
	fake.mu.Unlock()
	if err := s3.Check(ctx); err == nil || !strings.Contains(err.Error(), "credentials rejected") {
		t.Errorf("Check with bad credentials = %v, want a credentials error", err)
	}
	fake.mu.Lock()
	fake.reject = ""
	delete(fake.buckets, "test-bucket")
	fake.mu.Unlock()
	if err := s3.Check(ctx); err != nil {
		t.Errorf("Check without the bucket = %v; a missing bucket is EnsureBucket's job", err)
	}
	if err := s3.Healthy(ctx); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Healthy without the bucket = %v, want a missing-bucket error", err)
	}

	// Nothing listens here: a refused connection must fail fast and say so.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	dead, err := NewS3(S3Config{Endpoint: url, Bucket: "dead-bucket", AccessKey: "ak", SecretKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := dead.Check(ctx); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("Check against a closed port = %v, want an unreachable error", err)
	}
	if d := time.Since(start); d > s3CheckTimeout+2*time.Second {
		t.Errorf("Check took %v, want it bounded by %v", d, s3CheckTimeout)
	}
}

// realS3 connects to the S3/MinIO named by BLOB_S3_TEST_ENDPOINT (plus
// BLOB_S3_TEST_ACCESS_KEY / _SECRET_KEY, optional BLOB_S3_TEST_BUCKET) with
// the bucket ensured, or skips the test when the variable is unset.
func realS3(t *testing.T) *S3 {
	t.Helper()
	endpoint := os.Getenv("BLOB_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BLOB_S3_TEST_ENDPOINT not set")
	}
	bucket := os.Getenv("BLOB_S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "archivist-test"
	}
	s3, err := NewS3(S3Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		AccessKey: os.Getenv("BLOB_S3_TEST_ACCESS_KEY"),
		SecretKey: os.Getenv("BLOB_S3_TEST_SECRET_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s3.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	return s3
}

// TestS3RealEndpoint runs the same contract against a real S3/MinIO when
// BLOB_S3_TEST_ENDPOINT is set (plus BLOB_S3_TEST_ACCESS_KEY / _SECRET_KEY,
// optional BLOB_S3_TEST_BUCKET), e.g. a local `minio server`.
func TestS3RealEndpoint(t *testing.T) {
	s3 := realS3(t)
	endpoint, bucket := os.Getenv("BLOB_S3_TEST_ENDPOINT"), s3.Bucket()
	ctx := context.Background()
	user := fmt.Sprintf("it-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = s3.DeleteUser(ctx, user) })

	big := make([]byte, s3PartSize*2+77)
	rand.Read(big)
	if err := s3.Put(ctx, user, "big", io.LimitReader(bytes.NewReader(big), int64(len(big)))); err != nil {
		t.Fatal(err)
	}
	if err := s3.Put(ctx, user, "small", strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	if err := s3.Put(ctx, user, "small", strings.NewReader("MUTATED")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s3, user, "small"); string(got) != "original" {
		t.Errorf("small = %q, want original", got)
	}
	if got := readAll(t, s3, user, "big"); !bytes.Equal(got, big) {
		t.Errorf("big round-trip mismatch (%d vs %d bytes)", len(got), len(big))
	}
	// Seek + read across a part boundary: what a browser's Range request does.
	rc, err := s3.Open(ctx, user, "big")
	if err != nil {
		t.Fatal(err)
	}
	rs := rc.(io.ReadSeeker)
	if size, err := rs.Seek(0, io.SeekEnd); err != nil || size != int64(len(big)) {
		t.Errorf("Seek(End) = %d, %v; want %d", size, err, len(big))
	}
	off := int64(s3PartSize) - 10
	if _, err := rs.Seek(off, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 20)
	if _, err := io.ReadFull(rs, got); err != nil || !bytes.Equal(got, big[off:off+20]) {
		t.Errorf("ranged read at %d: %v, mismatch=%t", off, err, !bytes.Equal(got, big[off:off+20]))
	}
	rc.Close()
	if err := s3.Check(ctx); err != nil {
		t.Errorf("Check: %v", err)
	}
	if err := s3.Healthy(ctx); err != nil {
		t.Errorf("Healthy: %v", err)
	}
	bad, _ := NewS3(S3Config{Endpoint: endpoint, Bucket: bucket, AccessKey: "wrong", SecretKey: "wrong-secret"})
	if err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "credentials rejected") {
		t.Errorf("Check with wrong credentials = %v, want a credentials error", err)
	}
	if err := s3.DeleteUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Open(ctx, user, "big"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after DeleteUser err = %v, want fs.ErrNotExist", err)
	}
}
