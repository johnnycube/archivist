package bundle

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// LimitError reports an archive refused because it exceeds one of the Limits.
type LimitError struct {
	Limit string // the Limits field: "MaxEntries", "MaxDirectoryBytes", …
	Max   int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("archive exceeds %s=%d", e.Limit, e.Max)
}

// ZIP structure constants (APPNOTE 4.3.12–4.3.16), as in archive/zip/struct.go.
const (
	dirHeaderSig = 0x02014b50
	dirEndSig    = 0x06054b50
	dir64LocSig  = 0x07064b50
	dir64EndSig  = 0x06064b50
	dirHeaderLen = 46 // + name + extra + comment
	dirEndLen    = 22 // + comment
	dir64LocLen  = 20
	dir64EndLen  = 56
)

// checkDirectory vets a ZIP's central directory against lim before the part is
// handed to archive/zip.
//
// zip.NewReader (Reader.init) materialises one *zip.File — with its name, extra
// field and comment — per central-directory header before returning, so the
// importer's own MaxEntries check runs too late: an 8 GiB upload can carry
// well over a hundred million 46-byte headers. Worse, init does not stop at the
// declared entry count (it only compares the low 16 bits afterwards) nor at the
// declared directory size: it reads headers until one fails to parse. So the
// declared figures are only a fast path here; the directory is then walked
// exactly as init walks it, counting headers and bytes without keeping any.
//
// It returns what the directory holds (the larger walk, when there are two
// candidates), so a caller can carry one budget across the parts of an import.
func checkDirectory(r io.ReaderAt, size int64, lim Limits) (entries int, bytes int64, err error) {
	if lim.MaxEntries <= 0 && lim.MaxDirectoryBytes <= 0 {
		return 0, 0, nil
	}
	end, err := readDirectoryEnd(r, size)
	if err != nil {
		return 0, 0, err
	}
	if lim.MaxEntries > 0 && end.records > uint64(lim.MaxEntries) {
		return 0, 0, &LimitError{Limit: "MaxEntries", Max: int64(lim.MaxEntries)}
	}
	if lim.MaxDirectoryBytes > 0 && end.size > uint64(lim.MaxDirectoryBytes) {
		return 0, 0, &LimitError{Limit: "MaxDirectoryBytes", Max: lim.MaxDirectoryBytes}
	}
	if end.size > uint64(size) || end.offset > uint64(size) {
		return 0, 0, zip.ErrFormat
	}
	// Reader.init starts reading at baseOffset+offset, which is the end record's
	// position minus the declared size — or, when that implies prepended data
	// (baseOffset > 0) but a header also parses at the raw offset, at the raw
	// offset. Which of the two it picks depends on a full header parse, so both
	// candidates are walked.
	return walkCandidates(r, size, end, lim)
}

// walkCandidates walks the directory from each place Reader.init may start
// reading it and returns the larger count.
func walkCandidates(r io.ReaderAt, size int64, end directoryEnd, lim Limits) (entries int, bytes int64, err error) {
	starts := []int64{end.at - int64(end.size)}
	if base := starts[0] - int64(end.offset); base > 0 {
		starts = append(starts, int64(end.offset))
	}
	for _, start := range starts {
		if start < 0 || start >= size {
			continue
		}
		n, b, err := walkDirectory(io.NewSectionReader(r, start, size-start), lim)
		if err != nil {
			return 0, 0, err
		}
		entries, bytes = max(entries, n), max(bytes, b)
	}
	return entries, bytes, nil
}

// walkDirectory reads central-directory headers the way Reader.init's loop over
// readDirectoryHeader does — until something that is not a header — and fails
// once they outnumber MaxEntries or outweigh MaxDirectoryBytes. It returns
// the headers and bytes it walked.
func walkDirectory(r io.Reader, lim Limits) (entries int, bytes int64, err error) {
	br := bufio.NewReader(r)
	for {
		variable, ok := nextDirectoryHeader(br)
		if !ok {
			return entries, bytes, nil // the directory ends where a header does not parse
		}
		entries++
		bytes += dirHeaderLen + int64(variable)
		if lim.MaxEntries > 0 && entries > lim.MaxEntries {
			return entries, bytes, &LimitError{Limit: "MaxEntries", Max: int64(lim.MaxEntries)}
		}
		if lim.MaxDirectoryBytes > 0 && bytes > lim.MaxDirectoryBytes {
			return entries, bytes, &LimitError{Limit: "MaxDirectoryBytes", Max: lim.MaxDirectoryBytes}
		}
	}
}

// nextDirectoryHeader consumes one central-directory header and reports the
// length of its variable part, or ok = false at the first thing that is not a
// complete header — where Reader.init stops too, so that is not an error.
func nextDirectoryHeader(br *bufio.Reader) (variable int, ok bool) {
	var hdr [dirHeaderLen]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil || binary.LittleEndian.Uint32(hdr[:]) != dirHeaderSig {
		return 0, false
	}
	// file name, extra field and file comment lengths
	variable = int(binary.LittleEndian.Uint16(hdr[28:])) + int(binary.LittleEndian.Uint16(hdr[30:])) + int(binary.LittleEndian.Uint16(hdr[32:]))
	n, err := br.Discard(variable)
	return variable, err == nil && n == variable
}

// directoryEnd is what the end-of-central-directory record(s) declare.
type directoryEnd struct {
	records uint64 // total entries in the central directory
	size    uint64 // central directory size in bytes
	offset  uint64 // central directory offset, relative to the archive start
	at      int64  // where the (ZIP64, if present) end record sits in the file
}

// readDirectoryEnd mirrors archive/zip's readDirectoryEnd (reader.go), with
// its helpers findSignatureInBlock, findDirectory64End and readDirectory64End:
// the end record is searched backwards in the last 1 KiB, then the last 65 KiB
// (it is followed by a comment of up to 64 KiB), and the ZIP64 record replaces
// its figures when any of them is saturated.
func readDirectoryEnd(r io.ReaderAt, size int64) (directoryEnd, error) {
	var buf []byte
	var at int64 = -1
	for _, n := range []int64{1024, 65 * 1024} {
		n = min(n, size)
		buf = make([]byte, n)
		if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return directoryEnd{}, err
		}
		if p := findEndSignature(buf); p >= 0 {
			buf, at = buf[p:], size-n+int64(p)
			break
		}
		if n == size {
			break
		}
	}
	if at < 0 {
		return directoryEnd{}, zip.ErrFormat
	}
	le := binary.LittleEndian
	end := directoryEnd{
		records: uint64(le.Uint16(buf[10:])),
		size:    uint64(le.Uint32(buf[12:])),
		offset:  uint64(le.Uint32(buf[16:])),
		at:      at,
	}
	if end.records != 0xffff && end.size != 0xffff && end.offset != 0xffffffff {
		return end, nil
	}
	// ZIP64: the locator sits right before the end record and points at the
	// ZIP64 end record. Absent or malformed locator: not ZIP64 after all.
	if at < dir64LocLen {
		return end, nil
	}
	loc := make([]byte, dir64LocLen)
	if _, err := r.ReadAt(loc, at-dir64LocLen); err != nil {
		return directoryEnd{}, err
	}
	if le.Uint32(loc) != dir64LocSig || le.Uint32(loc[4:]) != 0 || le.Uint32(loc[16:]) != 1 {
		return end, nil
	}
	at64 := int64(le.Uint64(loc[8:]))
	if at64 < 0 || at64 > size-dir64EndLen {
		return directoryEnd{}, zip.ErrFormat
	}
	rec := make([]byte, dir64EndLen)
	if _, err := r.ReadAt(rec, at64); err != nil {
		return directoryEnd{}, err
	}
	if le.Uint32(rec) != dir64EndSig {
		return directoryEnd{}, zip.ErrFormat
	}
	return directoryEnd{
		records: le.Uint64(rec[32:]),
		size:    le.Uint64(rec[40:]),
		offset:  le.Uint64(rec[48:]),
		at:      at64,
	}, nil
}

// findEndSignature returns the position of the last end-of-central-directory
// record in b whose comment fits in b, or -1 (archive/zip findSignatureInBlock).
func findEndSignature(b []byte) int {
	for i := len(b) - dirEndLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(b[i:]) == dirEndSig {
			if comment := int(binary.LittleEndian.Uint16(b[i+dirEndLen-2:])); i+dirEndLen+comment > len(b) {
				return -1 // truncated comment
			}
			return i
		}
	}
	return -1
}
