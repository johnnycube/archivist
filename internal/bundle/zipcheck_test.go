package bundle

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/domain"
)

// endRecord builds an end-of-central-directory record.
func endRecord(records uint16, size, offset uint32, comment string) []byte {
	b := make([]byte, dirEndLen, dirEndLen+len(comment))
	le := binary.LittleEndian
	le.PutUint32(b, dirEndSig)
	le.PutUint16(b[8:], records)
	le.PutUint16(b[10:], records)
	le.PutUint32(b[12:], size)
	le.PutUint32(b[16:], offset)
	le.PutUint16(b[20:], uint16(len(comment)))
	return append(b, comment...)
}

// end64 builds a ZIP64 end record + locator + saturated classic end record, for
// an archive whose ZIP64 end record sits at offset at.
func end64(at, records, size, offset uint64) []byte {
	le := binary.LittleEndian
	rec := make([]byte, dir64EndLen)
	le.PutUint32(rec, dir64EndSig)
	le.PutUint64(rec[4:], dir64EndLen-12)
	le.PutUint64(rec[24:], records)
	le.PutUint64(rec[32:], records)
	le.PutUint64(rec[40:], size)
	le.PutUint64(rec[48:], offset)
	loc := make([]byte, dir64LocLen)
	le.PutUint32(loc, dir64LocSig)
	le.PutUint64(loc[8:], at)
	le.PutUint32(loc[16:], 1)
	return append(append(rec, loc...), endRecord(0xffff, 0xffffffff, 0xffffffff, "")...)
}

func wantLimit(t *testing.T, err error, limit string) {
	t.Helper()
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != limit {
		t.Fatalf("err = %v, want a LimitError for %s", err, limit)
	}
}

// A 22-byte file can declare 60,000 entries: refused from the end record alone.
func TestCheckDirectoryDeclaredCount(t *testing.T) {
	data := endRecord(60_000, 60_000*dirHeaderLen, 0, "")
	lim := DefaultLimits()
	lim.MaxEntries = 1000
	wantLimit(t, checkDirectoryErr(bytes.NewReader(data), int64(len(data)), lim), "MaxEntries")
}

// ZIP64 lifts the count to 64 bits; 50 million entries in a 98-byte file.
func TestCheckDirectoryZip64DeclaredCount(t *testing.T) {
	data := end64(0, 50_000_000, 50_000_000*dirHeaderLen, 0)
	err := checkDirectoryErr(bytes.NewReader(data), int64(len(data)), DefaultLimits())
	wantLimit(t, err, "MaxEntries")

	// The importer refuses it before archive/zip sees it.
	_, err = NewImporter(lineProvider{}).ImportParts(context.Background(), []Part{memPart(data, "")}, func(domain.Event) error { return nil })
	wantLimit(t, err, "MaxEntries")

	// A modest count with an outsized directory is refused on size.
	data = end64(0, 10, 1<<40, 0)
	wantLimit(t, checkDirectoryErr(bytes.NewReader(data), int64(len(data)), DefaultLimits()), "MaxDirectoryBytes")
}

// archive/zip reads headers until one fails to parse, whatever the end record
// declares, so an understated count must not get an archive through.
func TestCheckDirectoryCountsActualHeaders(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 50; i++ {
		files[fmt.Sprintf("f%02d", i)] = ""
	}
	data := zipBytes(t, files)
	end := len(data) - dirEndLen
	binary.LittleEndian.PutUint16(data[end+8:], 1) // "one entry"
	binary.LittleEndian.PutUint16(data[end+10:], 1)

	lim := DefaultLimits()
	lim.MaxEntries = 10
	wantLimit(t, checkDirectoryErr(bytes.NewReader(data), int64(len(data)), lim), "MaxEntries")

	lim = DefaultLimits()
	lim.MaxDirectoryBytes = 20 * dirHeaderLen
	wantLimit(t, checkDirectoryErr(bytes.NewReader(data), int64(len(data)), lim), "MaxDirectoryBytes")
}

// Ordinary archives pass: plain, with a trailing comment (short, and long
// enough to push the end record out of the first 1 KiB search window), with
// prepended data (self-extractor style), empty, and genuinely ZIP64.
func TestCheckDirectoryAcceptsNormalArchives(t *testing.T) {
	withComment := func(comment string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("data/a.txt")
		w.Write([]byte("one two"))
		if err := zw.SetComment(comment); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	var many bytes.Buffer // > 65,535 entries makes archive/zip write ZIP64 records
	zw := zip.NewWriter(&many)
	for i := 0; i < 66_000; i++ {
		if _, err := zw.Create(fmt.Sprintf("data/%05d.txt", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"plain":         zipBytes(t, map[string]string{"data/a.txt": "one two"}),
		"comment":       withComment("exported 2026-01-01"),
		"long comment":  withComment(strings.Repeat("c", 5000)),
		"prepended":     append(bytes.Repeat([]byte{0xAA}, 3000), zipBytes(t, map[string]string{"data/a.txt": "one two"})...),
		"empty archive": zipBytes(t, nil),
		"zip64":         many.Bytes(),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := checkDirectoryErr(bytes.NewReader(data), int64(len(data)), DefaultLimits()); err != nil {
				t.Fatalf("checkDirectory: %v", err)
			}
			end, err := readDirectoryEnd(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if end.records != uint64(len(zr.File)) {
				t.Errorf("declared records = %d, archive/zip found %d", end.records, len(zr.File))
			}
		})
	}

	// And it still imports.
	data := cases["long comment"]
	cov, err := NewImporter(lineProvider{}).ImportParts(context.Background(), []Part{memPart(data, "")}, func(domain.Event) error { return nil })
	if err != nil || cov.Events != 2 {
		t.Fatalf("import with comment: cov=%+v err=%v", cov, err)
	}
}

// Not a ZIP at all: the stdlib's own error, not a panic.
func TestCheckDirectoryNotAZip(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("hello"), bytes.Repeat([]byte("x"), 100_000)} {
		if err := checkDirectoryErr(bytes.NewReader(data), int64(len(data)), DefaultLimits()); !errors.Is(err, zip.ErrFormat) {
			t.Errorf("len %d: err = %v, want zip.ErrFormat", len(data), err)
		}
	}
}

func FuzzCheckDirectory(f *testing.F) {
	f.Add(endRecord(1, 46, 0, "c"))
	f.Add(end64(0, 5, 230, 0))
	f.Add(zipBytes(f, map[string]string{"a": "b"}))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = checkDirectoryErr(bytes.NewReader(data), int64(len(data)), DefaultLimits()) // must not panic; any verdict is fine
	})
}

// checkDirectoryErr is checkDirectory for the tests that only need its verdict.
func checkDirectoryErr(r io.ReaderAt, size int64, lim Limits) error {
	_, _, err := checkDirectory(r, size, lim)
	return err
}
