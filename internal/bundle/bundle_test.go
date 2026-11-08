package bundle

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// lineProvider maps data/*.txt members: one RawRecord, and one Event, per line.
type lineProvider struct{}

func (lineProvider) Descriptor() domain.Descriptor { return domain.Descriptor{ID: "lines"} }
func (lineProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

func (lineProvider) Extractors() []provider.Extractor { return []provider.Extractor{lineExtractor{}} }

func (lineProvider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	return []domain.Event{{ExternalID: string(raw.Payload), Type: "lines.line", Body: string(raw.Payload)}}, nil
}

type lineExtractor struct{}

func (lineExtractor) Match(member string) bool { return strings.HasSuffix(member, ".txt") }
func (lineExtractor) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	for _, l := range strings.Fields(string(data)) {
		if err := emit(provider.RawRecord{Kind: "line", Payload: []byte(l)}); err != nil {
			return err
		}
	}
	return nil
}

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
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
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func collect(evs *[]domain.Event) func(domain.Event) error {
	return func(ev domain.Event) error { *evs = append(*evs, ev); return nil }
}

// The parts of a split export import as one archive: members from every part,
// one shared dedupe set, and progress that ends at the archive total.
func TestImportZipsMultiPart(t *testing.T) {
	part1 := writeZip(t, map[string]string{"data/a.txt": "one two"})
	part2 := writeZip(t, map[string]string{"data/b.txt": "two three", "photo.jpg": "JPEGDATA"})

	im := NewImporter(lineProvider{})
	var media []string
	im.PutBlob = func(key, _ string, r io.Reader) error {
		media = append(media, key)
		_, err := io.Copy(io.Discard, r)
		return err
	}
	var done, total int64
	im.Progress = func(d, tot int64) {
		if d < done {
			t.Errorf("progress went backwards: %d after %d", d, done)
		}
		done, total = d, tot
	}
	var evs []domain.Event
	cov, err := im.ImportZips(context.Background(), []string{part1, part2}, collect(&evs))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || cov.Duplicates != 1 { // "two" appears in both parts
		t.Errorf("events = %d, duplicates = %d; want 3 and 1", len(evs), cov.Duplicates)
	}
	if cov.Members != 3 || cov.MediaStored != 1 || len(media) != 1 {
		t.Errorf("members = %d, media = %d; want 3 and 1", cov.Members, cov.MediaStored)
	}
	if want := int64(len("one two") + len("two three") + len("JPEGDATA")); total != want || done != want {
		t.Errorf("progress = %d/%d, want %d/%d", done, total, want, want)
	}
}

// Unmapped members are captured by streaming: size, hash and a short preview,
// never the whole content.
func TestGenericCaptureStreams(t *testing.T) {
	big := strings.Repeat("x", 10_000)
	path := writeZip(t, map[string]string{"mystery.bin": big, "small.dat": " hi "})

	var evs []domain.Event
	cov, err := NewImporter(lineProvider{}).ImportZip(context.Background(), path, collect(&evs))
	if err != nil {
		t.Fatal(err)
	}
	if cov.Generic != 2 || len(evs) != 2 {
		t.Fatalf("generic = %d, events = %d; want 2 and 2", cov.Generic, len(evs))
	}
	for _, ev := range evs {
		if ev.Type != "lines.unknown" {
			t.Errorf("type = %q, want lines.unknown", ev.Type)
		}
		preview := ev.Metadata["preview"].(string)
		switch ev.Metadata["member"] {
		case "mystery.bin":
			if ev.Metadata["bytes"] != int64(10_000) || preview != strings.Repeat("x", previewBytes)+"…" {
				t.Errorf("big member: bytes = %v, preview len = %d", ev.Metadata["bytes"], len(preview))
			}
		case "small.dat":
			if preview != "hi" {
				t.Errorf("small preview = %q, want %q", preview, "hi")
			}
		}
	}
}

func TestMemberLimit(t *testing.T) {
	path := writeZip(t, map[string]string{"data/a.txt": "12345678"})

	im := NewImporter(lineProvider{})
	im.Limits.MaxMemberBytes = 8 // exactly the member's size: allowed
	var evs []domain.Event
	if _, err := im.ImportZip(context.Background(), path, collect(&evs)); err != nil || len(evs) != 1 {
		t.Errorf("member at the limit: events = %d, err = %v; want 1, nil", len(evs), err)
	}

	im = NewImporter(lineProvider{})
	im.Limits.MaxMemberBytes = 7
	if _, err := im.ImportZip(context.Background(), path, collect(&evs)); err == nil {
		t.Error("member over the limit was accepted")
	}

	im = NewImporter(lineProvider{})
	im.Limits.MaxEntries = 0
	im.Limits.MaxTotalBytes = 4
	if _, err := im.ImportZip(context.Background(), path, collect(&evs)); err == nil {
		t.Error("archive over MaxTotalBytes was accepted")
	}
}

func TestStreamJSONArray(t *testing.T) {
	cases := []struct {
		name, in string
		keys     []string
		want     int
		skipped  string // "key=count" of every reported array, in document order
		wantErr  bool
	}{
		{"top-level array", `[{"a":1},{"a":2}]`, nil, 2, "", false},
		{"top-level array ignores keys", `[1,2,3]`, []string{"x"}, 3, "", false},
		{
			"keyed array, the other arrays are reported", `{"meta":{"deep":[1,{"x":[2]}]},"n":1,"items":[{"a":1},{"a":2}],"after":[9]}`,
			[]string{"items"},
			2, "meta.deep=2 after=1", false,
		},
		{"second of the accepted keys", `{"comments":[1]}`, []string{"comments_v2", "comments"}, 1, "", false},
		{"only the first accepted key is streamed", `{"comments_v2":[1,2],"comments":[3]}`, []string{"comments_v2", "comments"}, 2, "comments=1", false},
		{"no known key", `{"other":[1,2]}`, []string{"items"}, 0, "other=2", false},
		{"an object where an array was expected", `{"conversations":[{"id":1},{"id":2},{"id":3}],"version":2}`, nil, 0, "conversations=3", false},
		{"empty arrays are nothing to report", `{"items":[1],"other":[],"more":{"x":[]}}`, []string{"items"}, 1, "", false},
		{"arrays inside streamed elements are the caller's", `{"items":[{"tags":[1,2,3]}]}`, []string{"items"}, 1, "", false},
		{"a nested wanted key is not streamed, but reported", `{"wrap":{"items":[1,2]}}`, []string{"items"}, 0, "wrap.items=2", false},
		{"empty member", ``, nil, 0, "", false},
		{"key is not an array", `{"items":{"a":1}}`, []string{"items"}, 0, "", true},
		{"scalar document", `42`, nil, 0, "", true},
		{"truncated", `[{"a":1},{"a"`, nil, 1, "", true},
		{"truncated after the streamed array", `{"items":[1],"after":[1,`, []string{"items"}, 1, "", true},
	}
	for _, c := range cases {
		n := 0
		var skipped []string
		err := StreamJSONArray(strings.NewReader(c.in), c.keys, func(el json.RawMessage) error {
			if !json.Valid(el) {
				t.Errorf("%s: invalid element %q", c.name, el)
			}
			n++
			return nil
		}, func(sa SkippedArray) error {
			skipped = append(skipped, fmt.Sprintf("%s=%d", sa.Key, sa.Count))
			return nil
		})
		if (err != nil) != c.wantErr || n != c.want {
			t.Errorf("%s: n = %d, err = %v; want n = %d, wantErr = %v", c.name, n, err, c.want, c.wantErr)
		}
		if got := strings.Join(skipped, " "); got != c.skipped {
			t.Errorf("%s: skipped = %q, want %q", c.name, got, c.skipped)
		}
	}
	// No callback = not interested; the document is still read to its end.
	if err := StreamJSONArray(strings.NewReader(`{"other":[1,2],"items":[1]}`), []string{"items"},
		func(json.RawMessage) error { return nil }, nil); err != nil {
		t.Errorf("nil skipped: %v", err)
	}
	// The callback's error stops the walk.
	stop := errors.New("stop")
	if err := StreamJSONArray(strings.NewReader(`{"other":[1,2]}`), nil, nil, func(SkippedArray) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("skipped error: err = %v, want it passed through", err)
	}
}

func TestStreamJSVarArrayRejectsNonArray(t *testing.T) {
	err := StreamJSVarArray(strings.NewReader(`window.YTD.x.part0 = {}`), func(json.RawMessage) error { return nil })
	if err == nil {
		t.Error("expected an error for a file with no array")
	}
}
