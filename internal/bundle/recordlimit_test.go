package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// syntheticJSON generates prefix + fill×'x' + suffix on the fly, so a test can
// offer a 100 MiB record without ever allocating it, and counts what was read.
type syntheticJSON struct {
	prefix, suffix string
	fill           int64
	served         int64
	maxRecord      int64 // advertised via MaxRecordBytes when > 0
}

func (s *syntheticJSON) Read(p []byte) (int, error) {
	total := int64(len(s.prefix)) + s.fill + int64(len(s.suffix))
	if s.served >= total {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && s.served < total {
		switch off := s.served; {
		case off < int64(len(s.prefix)):
			c := copy(p[n:], s.prefix[off:])
			n, s.served = n+c, s.served+int64(c)
		case off < int64(len(s.prefix))+s.fill:
			c := int64(len(p) - n)
			if left := int64(len(s.prefix)) + s.fill - off; c > left {
				c = left
			}
			for i := int64(0); i < c; i++ {
				p[n+int(i)] = 'x'
			}
			n, s.served = n+int(c), s.served+c
		default:
			c := copy(p[n:], s.suffix[off-int64(len(s.prefix))-s.fill:])
			n, s.served = n+c, s.served+int64(c)
		}
	}
	return n, nil
}

// limited hides nothing but adds the importer-style limit advertisement.
type limited struct{ *syntheticJSON }

func (l limited) MaxRecordBytes() int64 { return l.maxRecord }

const (
	hugeRecord = 100 << 20
	testLimit  = 1 << 20
)

// allocatedDuring reports the bytes allocated (cumulative, not live) by fn.
func allocatedDuring(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestRecordLimitStopsBeforeAllocating is the H2 regression test: a single
// 100 MiB element must be refused after ~limit bytes were read and buffered,
// not after the decoder swallowed all of it.
func TestRecordLimitStopsBeforeAllocating(t *testing.T) {
	type run func(src io.Reader, onRecord func()) error
	cases := map[string]struct {
		prefix, suffix string
		run            run
	}{
		"StreamJSONArray": {`[{"id":1},{"big":"`, `"},{"id":3}]`, func(r io.Reader, rec func()) error {
			return StreamJSONArray(r, nil, func(json.RawMessage) error { rec(); return nil }, nil)
		}},
		"StreamJSONArray keyed": {`{"skip":[1,2],"rows":[{"id":1},{"big":"`, `"}]}`, func(r io.Reader, rec func()) error {
			return StreamJSONArray(r, []string{"rows"}, func(json.RawMessage) error { rec(); return nil }, nil)
		}},
		"StreamJSONArray skipped string": {`{"skip":"`, `","rows":[{"id":1}]}`, func(r io.Reader, rec func()) error {
			return StreamJSONArray(r, []string{"rows"}, func(json.RawMessage) error { rec(); return nil }, nil)
		}},
		"StreamJSONArrays": {`{"w":{"rows":[{"id":1},{"big":"`, `"}]}}`, func(r io.Reader, rec func()) error {
			return StreamJSONArrays(r, []string{"rows"}, func(string, json.RawMessage) error { rec(); return nil })
		}},
		"StreamJSVarArray": {`window.YTD.tweets.part0 = [{"id":1},{"big":"`, `"}]`, func(r io.Reader, rec func()) error {
			return StreamJSVarArray(r, func(json.RawMessage) error { rec(); return nil })
		}},
		"ReadJSON": {`{"big":"`, `"}`, func(r io.Reader, _ func()) error {
			var v map[string]any
			return ReadJSON(r, &v)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := &syntheticJSON{prefix: tc.prefix, suffix: tc.suffix, fill: hugeRecord, maxRecord: testLimit}
			var err error
			records := 0
			allocated := allocatedDuring(func() { err = tc.run(limited{src}, func() { records++ }) })

			if !errors.Is(err, ErrRecordTooLarge) {
				t.Fatalf("err = %v, want ErrRecordTooLarge", err)
			}
			if wantRecords := strings.Count(tc.prefix, `{"id":1}`); records != wantRecords {
				t.Errorf("records before the oversized one = %d, want %d", records, wantRecords)
			}
			// The source must not be drained past the window (+ bufio/prefix slack).
			if max := int64(testLimit + 64<<10); src.served > max {
				t.Errorf("read %d bytes from the source, want <= %d", src.served, max)
			}
			// json.Decoder grows its buffer by doubling, so the total allocated
			// on the way to a 1 MiB window is a small multiple of it — nowhere
			// near the 100 MiB record.
			if allocated > 8*testLimit {
				t.Errorf("allocated %d bytes, want <= %d (record is %d)", allocated, 8*testLimit, hugeRecord)
			}
		})
	}
}

// TestRecordLimitDefault: without an advertised limit the default applies.
func TestRecordLimitDefault(t *testing.T) {
	src := &syntheticJSON{prefix: `[{"big":"`, suffix: `"}]`, fill: DefaultMaxRecordBytes + 1<<20}
	err := StreamJSONArray(src, nil, func(json.RawMessage) error { return nil }, nil)
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("err = %v, want ErrRecordTooLarge", err)
	}
	if src.served > DefaultMaxRecordBytes+64<<10 {
		t.Errorf("read %d bytes, want about DefaultMaxRecordBytes", src.served)
	}
}

// TestRecordLimitIsPerRecord: the window moves with every record, so a member
// far larger than the limit streams fine as long as each record is small, and
// skipped regions of any total size are fine too.
func TestRecordLimitIsPerRecord(t *testing.T) {
	const limit = 4 << 10
	record := `{"body":"` + strings.Repeat("y", 1000) + `"}`
	var doc strings.Builder
	doc.WriteString(`{"ignored":[`)
	for i := range 200 { // ~200 KiB skipped token by token
		if i > 0 {
			doc.WriteByte(',')
		}
		doc.WriteString(record)
	}
	doc.WriteString(`],"rows":[`)
	for i := range 500 { // ~500 KiB streamed
		if i > 0 {
			doc.WriteString(" ,\n ")
		}
		doc.WriteString(record)
	}
	doc.WriteString(`]}`)

	n := 0
	dec := NewRecordLimitedDecoder(strings.NewReader(doc.String()), limit)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("open: %v %v", tok, err)
	}
	err := streamObjectArrays(dec, []string{"rows"}, func(_ string, el json.RawMessage) error {
		if string(el) != record {
			t.Fatalf("record %d corrupted", n)
		}
		n++
		return nil
	}, nil, "", 1)
	if err != nil || n != 500 {
		t.Fatalf("records = %d, err = %v; want 500, nil", n, err)
	}

	// A record just over the limit is refused, one comfortably under passes.
	for size, wantErr := range map[int]bool{limit / 2: false, limit + 1: true} {
		one := `[{"body":"` + strings.Repeat("y", size) + `"}]`
		dec := NewRecordLimitedDecoder(strings.NewReader(one), limit)
		dec.Token()
		err := streamElements(dec, func(json.RawMessage) error { return nil })
		if errors.Is(err, ErrRecordTooLarge) != wantErr {
			t.Errorf("record of %d bytes (limit %d): err = %v, want too-large = %v", size, limit, err, wantErr)
		}
	}
}

// jsonProvider streams data/*.json members with StreamJSONArray.
type jsonProvider struct{ lineProvider }

func (jsonProvider) Extractors() []provider.Extractor { return []provider.Extractor{jsonExtractor{}} }

type jsonExtractor struct{}

func (jsonExtractor) Match(member string) bool { return strings.HasSuffix(member, ".json") }
func (jsonExtractor) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	return StreamJSONArray(r, nil, func(el json.RawMessage) error {
		return emit(provider.RawRecord{Kind: "row", Payload: el})
	}, nil)
}

// TestImporterRecordLimit: Limits.MaxRecordBytes reaches the JSON helpers
// through the member reader, with no change to the Extractor signature.
func TestImporterRecordLimit(t *testing.T) {
	path := writeZip(t, map[string]string{
		"data/rows.json": `[{"id":1},{"body":"` + strings.Repeat("z", 5000) + `"}]`,
	})

	im := NewImporter(jsonProvider{})
	if im.Limits.MaxRecordBytes != DefaultMaxRecordBytes {
		t.Errorf("default MaxRecordBytes = %d, want %d", im.Limits.MaxRecordBytes, DefaultMaxRecordBytes)
	}
	var evs []domain.Event
	if _, err := im.ImportZip(context.Background(), path, collect(&evs)); err != nil || len(evs) != 2 {
		t.Fatalf("default limit: events = %d, err = %v; want 2, nil", len(evs), err)
	}

	im = NewImporter(jsonProvider{})
	im.Limits.MaxRecordBytes = 1024
	evs = nil
	_, err := im.ImportZip(context.Background(), path, collect(&evs))
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("err = %v, want ErrRecordTooLarge", err)
	}
	if len(evs) != 1 {
		t.Errorf("events before the oversized record = %d, want 1", len(evs))
	}
}
