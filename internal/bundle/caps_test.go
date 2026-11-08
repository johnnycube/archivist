package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/domain"
)

// repeatReader yields prefix, then s repeated until n bytes have been
// produced, then suffix — a large hostile member without holding it in memory.
func repeatReader(prefix, s string, n int64, suffix string) io.Reader {
	return io.MultiReader(strings.NewReader(prefix),
		io.LimitReader(&cycle{s: s}, n), strings.NewReader(suffix))
}

type cycle struct {
	s string
	i int
}

func (c *cycle) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.s[c.i%len(c.s)]
		c.i++
	}
	return len(p), nil
}

func heapAfter(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A member that is nothing but "[" must stop at MaxTokenDepth, not grow the
// decoder's token stack by one entry per byte (1.37 GiB for 64 MiB on Go 1.26).
func TestDeeplyNestedJSONStops(t *testing.T) {
	var err error
	alloc := heapAfter(func() {
		err = StreamJSONArray(repeatReader(`{"other":`, "[", 64<<20, ""), []string{"items"},
			func(json.RawMessage) error { return nil }, nil)
	})
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err = %v; want ErrTooDeep", err)
	}
	if alloc > 64<<20 {
		t.Errorf("allocated %d MiB before refusing", alloc>>20)
	}
	// Honest nesting is fine.
	ok := `{"items":[{"a":[[[[1]]]]}]}`
	if err := StreamJSONArray(strings.NewReader(ok), []string{"items"}, func(json.RawMessage) error { return nil }, nil); err != nil {
		t.Errorf("modest nesting = %v", err)
	}
}

func TestReadCSVBoundsRecordsAndHeader(t *testing.T) {
	noop := func(map[string]string) error { return nil }
	for name, r := range map[string]io.Reader{
		"one huge line":  repeatReader("id,body\n1,", "x", 64<<20, "\n"),
		"unclosed quote": repeatReader("id,body\n1,\"", "short line\n", 64<<20, ""),
		"wide header":    strings.NewReader(strings.Repeat("c,", 2000) + "c\n" + strings.Repeat("x\n", 1000)),
	} {
		var err error
		alloc := heapAfter(func() { err = ReadCSV(r, noop) })
		if !errors.Is(err, ErrCSVTooLarge) {
			t.Errorf("%s: err = %v; want ErrCSVTooLarge", name, err)
		}
		if alloc > 64<<20 {
			t.Errorf("%s: allocated %d MiB before refusing", name, alloc>>20)
		}
	}

	var rows []map[string]string
	in := "id,body\n1,\"multi\nline\"\n2,plain\n3\n"
	if err := ReadCSV(strings.NewReader(in), func(r map[string]string) error {
		rows = append(rows, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0]["body"] != "multi\nline" || rows[1]["body"] != "plain" || rows[2]["body"] != "" {
		t.Errorf("rows = %v", rows)
	}
	// Records up to the limit pass, one after another.
	big := strings.Repeat("y", MaxCSVRecordBytes-64)
	n := 0
	if err := ReadCSV(strings.NewReader("id,body\n1,"+big+"\n2,"+big+"\n"), func(map[string]string) error {
		n++
		return nil
	}); err != nil || n != 2 {
		t.Errorf("two records just under the limit = %d rows, %v", n, err)
	}
}

// The directory limits hold for the whole import: two parts that each fit,
// but together exceed MaxEntries, are refused before either is opened.
func TestDirectoryBudgetSpansParts(t *testing.T) {
	part := func(prefix string, n int) Part {
		files := map[string]string{}
		for i := range n {
			files[fmt.Sprintf("%s/%d.txt", prefix, i)] = "x"
		}
		return memPart(zipBytes(t, files), prefix+".zip")
	}
	im := NewImporter(lineProvider{})
	im.Limits.MaxEntries = 10
	var evs []domain.Event
	_, err := im.ImportParts(context.Background(), []Part{part("a", 6), part("b", 6)}, collect(&evs))
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "MaxEntries" || len(evs) != 0 {
		t.Errorf("two parts over the budget = %v (%d events); want MaxEntries before any event", err, len(evs))
	}
	im = NewImporter(lineProvider{})
	im.Limits.MaxEntries = 12
	if _, err := im.ImportParts(context.Background(), []Part{part("a", 6), part("b", 6)}, collect(&evs)); err != nil {
		t.Errorf("two parts exactly at the budget = %v", err)
	}

	im = NewImporter(lineProvider{})
	im.Limits.MaxEntries = 0
	one := part("a", 3)
	im.Limits.MaxDirectoryBytes = one.Size // a directory is smaller than its archive
	if _, err := im.ImportParts(context.Background(), []Part{one, part("b", 3), part("c", 3)}, collect(&evs)); !errors.As(err, &le) || le.Limit != "MaxDirectoryBytes" {
		t.Errorf("directories over the byte budget together = %v; want MaxDirectoryBytes", err)
	}
}

// What is left of MaxTotalBytes bounds every member, so the total cannot be
// overshot by a member's worth; and a declared total over the limit is
// refused before anything is read.
func TestTotalBytesBoundEachMember(t *testing.T) {
	path := writeZip(t, map[string]string{"data/a.txt": strings.Repeat("a", 60), "data/b.txt": strings.Repeat("b", 60)})
	im := NewImporter(lineProvider{})
	im.Limits.MaxTotalBytes = 100
	var evs []domain.Event
	cov, err := im.ImportZip(context.Background(), path, collect(&evs))
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "MaxTotalBytes" {
		t.Fatalf("err = %v; want MaxTotalBytes", err)
	}
	if len(evs) != 0 || cov.Bytes != 0 {
		t.Errorf("read %d bytes, %d events; the declared total should refuse it up front", cov.Bytes, len(evs))
	}
}

func TestOversizedEventsAreCappedAndCounted(t *testing.T) {
	big := strings.Repeat("x", domain.MaxBodyBytes+1)
	path := writeZip(t, map[string]string{"data/a.txt": big + " small"})
	var evs []domain.Event
	cov, err := NewImporter(lineProvider{}).ImportZip(context.Background(), path, collect(&evs))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || cov.Truncated != 1 {
		t.Fatalf("events = %d, truncated = %d; want 2, 1", len(evs), cov.Truncated)
	}
	for _, ev := range evs {
		if len(ev.Body) > domain.MaxBodyBytes {
			t.Errorf("event body of %d bytes reached the sink", len(ev.Body))
		}
	}
}
