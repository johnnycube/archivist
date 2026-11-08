// Package bundle is the technical engine that turns a platform export archive (a
// ZIP of many heterogeneous files) into a uniform stream of normalized Events.
// It is used by the bundle ArchiveEngine adapter; it depends only on the domain
// and the provider SPI.
package bundle

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// Limits guards against hostile archives (zip bombs, path traversal).
type Limits struct {
	MaxEntries     int
	MaxTotalBytes  int64
	MaxMemberBytes int64
	// MaxRecordBytes caps a single streamed JSON record — the unit the JSON
	// helpers hold in memory (see RecordDecoder). Zero means
	// DefaultMaxRecordBytes. MaxMemberBytes stays large because members are
	// streamed (media, multi-GB message logs); records are what is buffered.
	MaxRecordBytes int64
	// MaxDirectoryBytes caps a part's ZIP central directory, which archive/zip
	// holds in memory whole (names, extras, comments) once the part is opened.
	// Checked before opening; see checkDirectory. Zero disables the check.
	MaxDirectoryBytes int64
	// MaxDedupeEntries caps the in-import dedupe set (16 bytes per record).
	// Once full, new hashes are no longer recorded and Coverage.DedupeTruncated
	// is set: later duplicates reach the store, whose idempotent upsert on
	// (user, source, external_id) is the real guarantee. Zero means unbounded.
	MaxDedupeEntries int
}

// DefaultLimits are conservative defaults suitable for personal exports.
func DefaultLimits() Limits {
	return Limits{
		MaxEntries: 200_000, MaxTotalBytes: 20 << 30, MaxMemberBytes: 4 << 30,
		MaxRecordBytes: DefaultMaxRecordBytes, MaxDirectoryBytes: 256 << 20,
		MaxDedupeEntries: 5_000_000,
	}
}

// MaxGenericMemberNames caps Coverage.GenericMembers, the list of unmapped
// member names (an archive can hold 200k of them). Coverage.Generic stays exact.
const MaxGenericMemberNames = 1000

// Importer imports export bundles for one BundleProvider.
type Importer struct {
	p               provider.BundleProvider
	CaptureUnmapped bool
	Limits          Limits
	// PutBlob, if set, receives unmatched members that are media by extension
	// (images, video, audio) or that the provider declares attachments
	// (provider.AttachmentMatcher), with their archive path and the SHA-256
	// of their content (hex), instead of capturing them as generic events.
	// Both count as Coverage.MediaStored. The content is read twice — once to
	// hash it, once for r — so a store can key it by its content.
	PutBlob func(path, sha256 string, r io.Reader) error
	// Progress, if set, is called as the import advances with the uncompressed
	// bytes consumed so far and the archive total. It fires per record, so
	// callers should throttle.
	Progress func(doneBytes, totalBytes int64)
	seen     map[dedupeKey]struct{}

	total int64        // uncompressed bytes across all members
	cur   *limitReader // member being read (for intra-member progress)
}

var mediaExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".heic": true, ".avif": true, ".bmp": true, ".mp4": true, ".mov": true,
	".m4v": true, ".webm": true, ".mp3": true, ".m4a": true, ".wav": true,
	".ogg": true,
	// Voice notes and clips from messenger exports (WhatsApp PTT, Telegram voice).
	".opus": true, ".oga": true, ".aac": true, ".amr": true, ".3gp": true,
}

func isMediaMember(name string) bool {
	return mediaExts[strings.ToLower(pathExt(name))]
}

func pathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && !strings.ContainsRune(name[i:], '/') {
		return name[i:]
	}
	return ""
}

// NewImporter builds an importer that captures unmapped data as generic Events.
func NewImporter(p provider.BundleProvider) *Importer {
	return &Importer{p: p, CaptureUnmapped: true, Limits: DefaultLimits(), seen: map[dedupeKey]struct{}{}}
}

// Part is one ZIP part of an export, opened for random access.
type Part struct {
	io.ReaderAt
	Size int64
	// Name is the file name the part was uploaded under, when known. Its base
	// name reaches extractors as RawRecord.Archive (some exports carry data only
	// there: "WhatsApp Chat - Book Club.zip").
	Name string
}

// member is one archive member plus the part it came from.
type member struct {
	*zip.File
	archive string
}

// ImportZip opens the ZIP at path and imports it, calling emitEvent per Event.
func (im *Importer) ImportZip(ctx context.Context, path string, emitEvent func(domain.Event) error) (*domain.Coverage, error) {
	return im.ImportZips(ctx, []string{path}, emitEvent)
}

// ImportZips is ImportParts over ZIP files on disk; the file names serve as the
// parts' names.
func (im *Importer) ImportZips(ctx context.Context, paths []string, emitEvent func(domain.Event) error) (*domain.Coverage, error) {
	parts := make([]Part, 0, len(paths))
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		parts = append(parts, Part{ReaderAt: f, Size: fi.Size(), Name: path})
	}
	return im.ImportParts(ctx, parts, emitEvent)
}

// ImportParts imports a (possibly multi-part: platforms split large exports
// into several ZIPs) export as one archive: members of all parts are routed
// together and share the dedupe set and the limits.
func (im *Importer) ImportParts(ctx context.Context, parts []Part, emitEvent func(domain.Event) error) (*domain.Coverage, error) {
	var files []member
	// The directory limits hold for the import, not per part: every part's
	// directory stays in memory until the import ends, so 64 parts each just
	// under the limit would hold 64 times what one may.
	budget := im.Limits
	for _, part := range parts {
		// archive/zip materialises the whole central directory on open, so the
		// directory is vetted first.
		entries, dirBytes, err := checkDirectory(part.ReaderAt, part.Size, budget)
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		budget.MaxEntries = spend(budget.MaxEntries, entries)
		budget.MaxDirectoryBytes = spend(budget.MaxDirectoryBytes, dirBytes)
		zr, err := zip.NewReader(part.ReaderAt, part.Size)
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		archive := baseName(part.Name)
		for _, f := range zr.File {
			files = append(files, member{File: f, archive: archive})
		}
	}
	return im.importFiles(ctx, files, emitEvent)
}

// spend takes used from a limit budget (<= 0 = unlimited) and keeps a spent
// budget at a minimum of 1, so the next part can hold nothing more rather than
// becoming unlimited.
func spend[T int | int64](budget, used T) T {
	if budget <= 0 {
		return budget
	}
	return max(budget-used, 1)
}

// baseName is the last element of an upload name, whichever separator the
// uploading client used.
func baseName(name string) string {
	return name[strings.LastIndexAny(name, `/\`)+1:]
}

func (im *Importer) importFiles(ctx context.Context, files []member, emitEvent func(domain.Event) error) (*domain.Coverage, error) {
	cov := domain.NewCoverage()
	extractors := im.p.Extractors()
	sourceID := im.p.Descriptor().ID
	attachments, _ := im.p.(provider.AttachmentMatcher)

	sort.SliceStable(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	im.total = 0
	for _, f := range files {
		if !f.FileInfo().IsDir() {
			im.total += int64(f.UncompressedSize64)
		}
	}
	// archive/zip refuses a member that inflates beyond its declared size, so
	// the declared total is an upper bound: refuse before reading anything.
	if im.Limits.MaxTotalBytes > 0 && im.total > im.Limits.MaxTotalBytes {
		return cov, &LimitError{Limit: "MaxTotalBytes", Max: im.Limits.MaxTotalBytes}
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return cov, err
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			cov.Skipped++
			continue
		}
		if im.Limits.MaxEntries > 0 && cov.Members >= im.Limits.MaxEntries {
			return cov, &LimitError{Limit: "MaxEntries", Max: int64(im.Limits.MaxEntries)}
		}
		cov.Members++

		ex := matchExtractor(extractors, f.Name)
		if ex == nil && im.PutBlob != nil && (isMediaMember(f.Name) || attachments != nil && attachments.IsAttachment(f.Name)) {
			// Media and attachments survive as stored blobs, not generic
			// "unknown" events.
			sum, err := im.hashMember(f, cov)
			if err != nil {
				return cov, fmt.Errorf("read media %q: %w", f.Name, err)
			}
			rc, err := f.Open()
			if err != nil {
				return cov, fmt.Errorf("open member %q: %w", f.Name, err)
			}
			lr := im.memberReader(rc, cov, 0)
			err = im.PutBlob(f.Name, sum, lr)
			cov.Bytes += lr.read
			cov.MediaStored++
			rc.Close()
			if err != nil {
				return cov, fmt.Errorf("store media %q: %w", f.Name, err)
			}
			if im.Limits.MaxTotalBytes > 0 && cov.Bytes > im.Limits.MaxTotalBytes {
				return cov, &LimitError{Limit: "MaxTotalBytes", Max: im.Limits.MaxTotalBytes}
			}
			im.report(cov)
			continue
		}
		if ex == nil && !im.CaptureUnmapped {
			cov.Skipped++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return cov, fmt.Errorf("open member %q: %w", f.Name, err)
		}
		lr := im.memberReader(rc, cov, im.Limits.MaxRecordBytes)
		im.cur = lr

		if ex != nil {
			cov.Mapped++
			err = ex.Extract(f.Name, lr, func(raw provider.RawRecord) error {
				return im.handleRaw(ctx, raw, f, cov, emitEvent)
			})
		} else {
			cov.Generic++
			if len(cov.GenericMembers) < MaxGenericMemberNames {
				cov.GenericMembers = append(cov.GenericMembers, f.Name)
			}
			err = im.captureGeneric(f.Name, lr, sourceID, cov, emitEvent)
		}
		im.cur = nil
		cov.Bytes += lr.read
		rc.Close()
		if err != nil {
			return cov, fmt.Errorf("member %q: %w", f.Name, err)
		}
		im.report(cov)
		if im.Limits.MaxTotalBytes > 0 && cov.Bytes > im.Limits.MaxTotalBytes {
			return cov, &LimitError{Limit: "MaxTotalBytes", Max: im.Limits.MaxTotalBytes}
		}
	}
	return cov, nil
}

// report publishes progress: finished members plus the bytes read so far from
// the member in flight.
func (im *Importer) report(cov *domain.Coverage) {
	if im.Progress == nil {
		return
	}
	done := cov.Bytes
	if im.cur != nil {
		done += im.cur.read
	}
	im.Progress(done, im.total)
}

// hashMember reads a member once, under the same bounds as any other read of
// it, and returns the SHA-256 of its content (hex). It counts nothing: the
// read that follows does.
func (im *Importer) hashMember(f member, cov *domain.Coverage) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }() // read-only: a close error changes nothing
	h := sha256.New()
	if _, err := io.Copy(h, im.memberReader(rc, cov, 0)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// memberReader bounds a member to MaxMemberBytes and to what is left of
// MaxTotalBytes, so the total cannot be overshot by up to one member.
func (im *Importer) memberReader(r io.Reader, cov *domain.Coverage, maxRecord int64) *limitReader {
	lr := &limitReader{r: r, remaining: -1, maxRecord: maxRecord}
	if m := im.Limits.MaxMemberBytes; m > 0 {
		lr.remaining, lr.over = m, &LimitError{Limit: "MaxMemberBytes", Max: m}
	}
	if t := im.Limits.MaxTotalBytes; t > 0 {
		if left := max(t-cov.Bytes, 0); lr.remaining < 0 || left < lr.remaining {
			lr.remaining, lr.over = left, &LimitError{Limit: "MaxTotalBytes", Max: t}
		}
	}
	return lr
}

func (im *Importer) handleRaw(ctx context.Context, raw provider.RawRecord, m member, cov *domain.Coverage, emitEvent func(domain.Event) error) error {
	// Per record, not only per member: one member can hold millions of
	// records, and a cancelled or timed-out import must stop within moments.
	if err := ctx.Err(); err != nil {
		return err
	}
	if raw.ContentHash == "" {
		raw.ContentHash = hashBytes(raw.Payload)
	}
	if im.duplicate(raw.ContentHash, cov) {
		return nil
	}
	if raw.Member == "" {
		raw.Member = m.Name
	}
	raw.Archive = m.archive
	evs, err := im.p.Normalize(ctx, raw)
	if err != nil {
		return fmt.Errorf("normalize kind=%q: %w", raw.Kind, err)
	}
	for _, ev := range evs {
		if ev.SourceID == "" {
			ev.SourceID = im.p.Descriptor().ID
		}
		if capped, cut := ev.Capped(); cut {
			ev = capped
			cov.Truncated++
		}
		cov.Events++
		cov.PerType[ev.Type]++
		if err := emitEvent(ev); err != nil {
			return err
		}
	}
	im.report(cov)
	return nil
}

func (im *Importer) captureGeneric(member string, r io.Reader, sourceID string, cov *domain.Coverage, emitEvent func(domain.Event) error) error {
	// Stream the member: hash it and keep only a short preview, so a multi-GB
	// unmapped file never sits in memory. (The full bytes stay available in the
	// preserved original archive.)
	hasher := sha256.New()
	head := &headWriter{max: previewBytes}
	size, err := io.Copy(io.MultiWriter(hasher, head), r)
	if err != nil {
		return err
	}
	h := hex.EncodeToString(hasher.Sum(nil))
	if im.duplicate(h, cov) {
		return nil
	}
	ev := domain.Event{
		SourceID:   sourceID,
		ExternalID: sourceID + ":unknown:" + member,
		Type:       sourceID + ".unknown",
		Category:   domain.CategoryOther,
		Title:      "Unmapped export data: " + member,
		Metadata:   map[string]any{"member": member, "bytes": size, "content_hash": h, "preview": preview(head.buf, size)},
	}
	cov.Events++
	cov.PerType[ev.Type]++
	return emitEvent(ev)
}

func matchExtractor(exs []provider.Extractor, name string) provider.Extractor {
	for _, ex := range exs {
		if ex.Match(name) {
			return ex
		}
	}
	return nil
}

// dedupeKey is the fixed-size form of a content hash kept in the dedupe set: a
// 64-hex string per record costs ~5x as much, which is hundreds of MB for a
// multi-million-record listening or watch history.
type dedupeKey [16]byte

// keyOf maps a content hash to its dedupe key. Hashes are hex SHA-256 strings
// (the importer's and every provider's), whose first 16 bytes are the key; any
// other string a provider might set is hashed, so every value has one key.
func keyOf(hash string) (k dedupeKey) {
	if len(hash) >= 2*len(k) {
		if _, err := hex.Decode(k[:], []byte(hash[:2*len(k)])); err == nil {
			return k
		}
	}
	sum := sha256.Sum256([]byte(hash))
	copy(k[:], sum[:])
	return k
}

// duplicate reports whether hash was already seen in this import (counting it)
// and records it otherwise, while the set has room.
func (im *Importer) duplicate(hash string, cov *domain.Coverage) bool {
	k := keyOf(hash)
	if _, dup := im.seen[k]; dup {
		cov.Duplicates++
		return true
	}
	if im.Limits.MaxDedupeEntries > 0 && len(im.seen) >= im.Limits.MaxDedupeEntries {
		cov.DedupeTruncated = true
		return false
	}
	im.seen[k] = struct{}{}
	return false
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

const previewBytes = 240

// preview renders the captured head of a member of the given total size.
func preview(head []byte, size int64) string {
	s := strings.TrimSpace(strings.ToValidUTF8(string(head), ""))
	if size > int64(len(head)) {
		return s + "…"
	}
	return s
}

// headWriter keeps the first max bytes written to it and discards the rest.
type headWriter struct {
	buf []byte
	max int
}

func (h *headWriter) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

type limitReader struct {
	r         io.Reader
	remaining int64 // < 0 = unbounded
	over      error // returned when the member has more than remaining
	read      int64
	maxRecord int64
}

// MaxRecordBytes tells the JSON helpers this import's record limit.
func (l *limitReader) MaxRecordBytes() int64 { return l.maxRecord }

func (l *limitReader) Read(p []byte) (int, error) {
	if l.remaining == 0 {
		// At the limit: a member of exactly the limit is fine, so only fail if
		// there is actually more data.
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		if n > 0 {
			return 0, l.over
		}
		return 0, err
	}
	if l.remaining > 0 && int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.remaining > 0 {
		l.remaining -= int64(n)
	}
	return n, err
}
