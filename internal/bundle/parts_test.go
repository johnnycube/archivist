package bundle

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

func zipBytes(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func memPart(data []byte, name string) Part {
	return Part{ReaderAt: bytes.NewReader(data), Size: int64(len(data)), Name: name}
}

// archiveProvider records the RawRecord.Archive each record arrived with.
type archiveProvider struct {
	lineProvider
	archives map[string]string // payload -> archive
}

func (p archiveProvider) Normalize(ctx context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	p.archives[string(raw.Payload)] = raw.Archive
	return p.lineProvider.Normalize(ctx, raw)
}

// ImportParts needs nothing file-backed, and hands each record the base name of
// the part its member came from.
func TestImportPartsInMemoryWithArchiveNames(t *testing.T) {
	p := archiveProvider{archives: map[string]string{}}
	parts := []Part{
		memPart(zipBytes(t, map[string]string{"data/a.txt": "one"}), `C:\Users\me\WhatsApp Chat - Book Club.zip`),
		memPart(zipBytes(t, map[string]string{"data/b.txt": "two"}), "uploads/part-2.zip"),
		memPart(zipBytes(t, map[string]string{"data/c.txt": "three"}), ""),
	}
	var evs []domain.Event
	cov, err := NewImporter(p).ImportParts(context.Background(), parts, collect(&evs))
	if err != nil {
		t.Fatal(err)
	}
	if cov.Events != 3 {
		t.Fatalf("Events = %d, want 3", cov.Events)
	}
	want := map[string]string{"one": "WhatsApp Chat - Book Club.zip", "two": "part-2.zip", "three": ""}
	for payload, archive := range want {
		if got := p.archives[payload]; got != archive {
			t.Errorf("Archive of %q = %q, want %q", payload, got, archive)
		}
	}
}

// ImportZip names the part after the file.
func TestImportZipSetsArchiveFromPath(t *testing.T) {
	p := archiveProvider{archives: map[string]string{}}
	path := writeZip(t, map[string]string{"data/a.txt": "one"})
	if _, err := NewImporter(p).ImportZip(context.Background(), path, func(domain.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := p.archives["one"]; got != "a.zip" {
		t.Errorf("Archive = %q, want a.zip", got)
	}
}

// attachmentProvider declares everything under files/ an attachment.
type attachmentProvider struct{ lineProvider }

func (attachmentProvider) IsAttachment(member string) bool {
	return strings.HasPrefix(member, "files/")
}

// A provider-declared attachment is stored like media; the same member without
// the declaration is an unknown event, and nothing is stored without PutBlob.
func TestAttachmentMatcherStoresNonMediaMembers(t *testing.T) {
	data := zipBytes(t, map[string]string{"data/a.txt": "one", "files/contract.pdf": "%PDF", "other/contract.pdf": "%PDF-2"})
	run := func(p provider.BundleProvider, store bool) (*domain.Coverage, map[string]string) {
		t.Helper()
		stored := map[string]string{}
		im := NewImporter(p)
		if store {
			im.PutBlob = func(key, _ string, r io.Reader) error {
				b, err := io.ReadAll(r)
				stored[key] = string(b)
				return err
			}
		}
		cov, err := im.ImportParts(context.Background(), []Part{memPart(data, "x.zip")}, func(domain.Event) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return cov, stored
	}

	cov, stored := run(attachmentProvider{}, true)
	if stored["files/contract.pdf"] != "%PDF" || len(stored) != 1 {
		t.Errorf("stored = %v, want only files/contract.pdf", stored)
	}
	if cov.MediaStored != 1 || cov.Generic != 1 || cov.PerType["lines.unknown"] != 1 {
		t.Errorf("MediaStored/Generic/unknown = %d/%d/%d, want 1/1/1", cov.MediaStored, cov.Generic, cov.PerType["lines.unknown"])
	}

	if cov, stored := run(lineProvider{}, true); len(stored) != 0 || cov.Generic != 2 {
		t.Errorf("without AttachmentMatcher: stored %v, Generic %d; want none, 2", stored, cov.Generic)
	}
	if cov, _ := run(attachmentProvider{}, false); cov.MediaStored != 0 || cov.Generic != 2 {
		t.Errorf("without PutBlob: MediaStored %d, Generic %d; want 0, 2", cov.MediaStored, cov.Generic)
	}
}

// Once the dedupe set is full, new hashes are not recorded (later duplicates
// pass through to the store's upsert) and Coverage says so; hashes recorded
// before the cap still dedupe.
func TestDedupeSetIsBounded(t *testing.T) {
	data := zipBytes(t, map[string]string{"data/a.txt": "a b c d a b c d"})
	im := NewImporter(lineProvider{})
	im.Limits.MaxDedupeEntries = 2
	var evs []domain.Event
	cov, err := im.ImportParts(context.Background(), []Part{memPart(data, "")}, collect(&evs))
	if err != nil {
		t.Fatal(err)
	}
	// a, b recorded; c, d not: the second pass drops a and b only.
	if cov.Events != 6 || cov.Duplicates != 2 || !cov.DedupeTruncated {
		t.Errorf("Events/Duplicates/Truncated = %d/%d/%v, want 6/2/true", cov.Events, cov.Duplicates, cov.DedupeTruncated)
	}
	if len(im.seen) != 2 {
		t.Errorf("dedupe set holds %d keys, want 2", len(im.seen))
	}

	im = NewImporter(lineProvider{})
	cov, err = im.ImportParts(context.Background(), []Part{memPart(data, "")}, func(domain.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if cov.Events != 4 || cov.Duplicates != 4 || cov.DedupeTruncated {
		t.Errorf("default limits: Events/Duplicates/Truncated = %d/%d/%v, want 4/4/false", cov.Events, cov.Duplicates, cov.DedupeTruncated)
	}
}

// A provider's hex ContentHash and the importer's own hash of the same bytes
// land on the same key; a non-hex hash still gets a stable key of its own.
func TestKeyOf(t *testing.T) {
	h := hashBytes([]byte("payload"))
	if keyOf(h) != keyOf(strings.ToUpper(h)) {
		t.Error("hex case changes the key")
	}
	if got := fmt.Sprintf("%x", keyOf(h)); got != h[:32] {
		t.Errorf("key = %s, want the hash's first 16 bytes %s", got, h[:32])
	}
	if keyOf("not-hex") != keyOf("not-hex") || keyOf("not-hex") == keyOf("not-hex2") {
		t.Error("non-hex hashes must map to stable, distinct keys")
	}
	if keyOf(strings.Repeat("z", 64)) == keyOf(strings.Repeat("y", 64)) {
		t.Error("long non-hex hashes collide")
	}
}

// The list of unmapped member names is capped; the count is not.
func TestGenericMemberNamesAreCapped(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < MaxGenericMemberNames+25; i++ {
		files[fmt.Sprintf("misc/%04d.bin", i)] = fmt.Sprint(i)
	}
	cov, err := NewImporter(lineProvider{}).ImportParts(context.Background(), []Part{memPart(zipBytes(t, files), "")}, func(domain.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if cov.Generic != MaxGenericMemberNames+25 || len(cov.GenericMembers) != MaxGenericMemberNames {
		t.Errorf("Generic = %d, names = %d; want %d, %d", cov.Generic, len(cov.GenericMembers), MaxGenericMemberNames+25, MaxGenericMemberNames)
	}
}

// retainedMB reports the live heap that build's result keeps alive.
func retainedMB(build func() any) float64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	keep := build()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(keep)
	return float64(after.HeapAlloc-before.HeapAlloc) / (1 << 20)
}

// BenchmarkDedupeSet reports the live heap held by the dedupe set for 1M
// distinct records; BenchmarkDedupeSetHexStrings is the representation it
// replaced (one 64-hex string per record).
func BenchmarkDedupeSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.ReportMetric(retainedMB(func() any {
			seen := map[dedupeKey]struct{}{}
			for n := 0; n < 1_000_000; n++ {
				seen[keyOf(hashBytes([]byte(strconv.Itoa(n))))] = struct{}{}
			}
			return seen
		}), "retained-MB")
	}
}

func BenchmarkDedupeSetHexStrings(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.ReportMetric(retainedMB(func() any {
			seen := map[string]struct{}{}
			for n := 0; n < 1_000_000; n++ {
				seen[hashBytes([]byte(strconv.Itoa(n)))] = struct{}{}
			}
			return seen
		}), "retained-MB")
	}
}
