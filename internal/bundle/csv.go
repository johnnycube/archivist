package bundle

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
)

// CSV bounds. encoding/csv holds a whole record in memory and grows its
// buffers without limit; with LazyQuotes an unclosed quote even swallows every
// following line into one field. A row of a real export is a few KiB (a long
// Reddit comment is ~40 KiB) and a header a few dozen columns.
const (
	// MaxCSVRecordBytes caps one record (one row, quoted newlines included).
	MaxCSVRecordBytes = 4 << 20
	// MaxCSVColumns caps the header: every row becomes a map of its width.
	MaxCSVColumns = 1024
)

// ErrCSVTooLarge is returned (wrapped) when a CSV record or header exceeds the
// bounds above.
var ErrCSVTooLarge = errors.New("csv record too large")

// ReadCSV reads a CSV with a header row and invokes fn once per data row with a
// header->value map. It tolerates ragged rows (missing trailing fields map to
// ""). This is the shared adapter Reddit's extractors build on. Records are
// bounded by MaxCSVRecordBytes and the header by MaxCSVColumns.
func ReadCSV(r io.Reader, fn func(row map[string]string) error) error {
	budget := &csvBudget{r: r, max: MaxCSVRecordBytes}
	cr := csv.NewReader(budget)
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	cr.LazyQuotes = true
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return nil // empty file
		}
		return err
	}
	if len(header) > MaxCSVColumns {
		return fmt.Errorf("%w: %d columns in the header (at most %d)", ErrCSVTooLarge, len(header), MaxCSVColumns)
	}
	header = append([]string(nil), header...) // ReuseRecord: keep our own copy
	budget.next()

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		budget.next()
		row := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			} else {
				row[h] = ""
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
}

// csvBudget fails a read once the csv.Reader has consumed more than max
// bytes since the last record it returned (next). The reader buffers ahead
// (bufio, 4 KiB), so a record is cut off at max plus that read-ahead — never
// at gigabytes.
type csvBudget struct {
	r        io.Reader
	max      int64
	read     int64
	recordAt int64
}

func (b *csvBudget) next() { b.recordAt = b.read }

func (b *csvBudget) Read(p []byte) (int, error) {
	if b.read-b.recordAt > b.max {
		return 0, fmt.Errorf("%w: a record exceeds %d bytes", ErrCSVTooLarge, b.max)
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	return n, err
}
