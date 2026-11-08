package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DefaultMaxRecordBytes caps one streamed JSON record (array element, skipped
// token, or a whole document read with ReadJSON). Real export records are a
// few KiB; 64 MiB leaves room for pathological-but-honest ones (a ChatGPT
// conversation with years of turns) while keeping a hostile member from
// making the decoder buffer gigabytes.
const DefaultMaxRecordBytes = 64 << 20

// ErrRecordTooLarge is returned (wrapped) when one JSON record exceeds the
// record byte limit.
var ErrRecordTooLarge = errors.New("json record exceeds the per-record byte limit")

// MaxTokenDepth caps how deeply the values a RecordDecoder walks token by
// token may nest. Real exports nest a dozen levels; json.Decoder.Token keeps
// one stack entry per open container, so without a cap a member of nothing
// but "[" (a few MB deflated, gigabytes inflated) grows that stack without
// bound — on Go 1.26 nothing else stops it (1.27's decoder stops at 10,000,
// later than this cap, which therefore decides on every Go version).
const MaxTokenDepth = 8_192

// ErrTooDeep is returned (wrapped) when token-by-token decoding nests deeper
// than MaxTokenDepth.
var ErrTooDeep = errors.New("json nests too deeply")

// recordLimiter is implemented by readers that know the record limit of the
// import they belong to (the Importer's member reader), so the package-level
// Stream*/Read* helpers honor Limits.MaxRecordBytes without a signature change.
type recordLimiter interface{ MaxRecordBytes() int64 }

// RecordDecoder is a json.Decoder whose memory use per operation is bounded.
//
// encoding/json buffers a complete value before Decode (or Token, for a long
// string) returns, so a single huge element is held in memory whatever the
// caller does with it. Measuring after the fact (InputOffset deltas) is too
// late: the allocation already happened. Instead the source is wrapped so the
// decoder can never pull more than max bytes past the point where the current
// operation started; the read fails with ErrRecordTooLarge before the buffer
// can grow beyond that window.
//
// Token, More and Decode shadow the embedded decoder's methods (each moves the
// window forward first); every other json.Decoder method is promoted as is.
// Token also bounds the nesting depth (MaxTokenDepth).
type RecordDecoder struct {
	*json.Decoder
	lr    *recordLimitReader
	depth int // containers opened by Token and not yet closed
}

// NewRecordLimitedDecoder returns a decoder over r that fails with
// ErrRecordTooLarge instead of buffering more than max bytes for one record.
// With max <= 0 the limit is the one r advertises (members handed out by an
// Importer carry Limits.MaxRecordBytes), else DefaultMaxRecordBytes.
func NewRecordLimitedDecoder(r io.Reader, max int64) *RecordDecoder {
	if max <= 0 {
		max = DefaultMaxRecordBytes
		if rl, ok := r.(recordLimiter); ok && rl.MaxRecordBytes() > 0 {
			max = rl.MaxRecordBytes()
		}
	}
	lr := &recordLimitReader{r: r, max: max}
	return &RecordDecoder{Decoder: json.NewDecoder(lr), lr: lr}
}

// mark starts a new window at the decoder's current position. Bytes the
// decoder has read ahead but not consumed stay counted against the window —
// they are part of the upcoming record (or already in memory either way).
func (d *RecordDecoder) mark() { d.lr.start = d.Decoder.InputOffset() }

// Token is json.Decoder.Token, bounded to the record limit and MaxTokenDepth.
func (d *RecordDecoder) Token() (json.Token, error) {
	d.mark()
	tok, err := d.Decoder.Token()
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '[', '{':
			if d.depth++; d.depth > MaxTokenDepth {
				return nil, fmt.Errorf("%w (more than %d levels)", ErrTooDeep, MaxTokenDepth)
			}
		case ']', '}':
			d.depth--
		}
	}
	return tok, err
}

// More is json.Decoder.More, bounded to the record limit.
func (d *RecordDecoder) More() bool {
	d.mark()
	return d.Decoder.More()
}

// Decode is json.Decoder.Decode, bounded to the record limit.
func (d *RecordDecoder) Decode(v any) error {
	d.mark()
	return d.Decoder.Decode(v)
}

// recordLimitReader lets its consumer read at most max bytes past start.
type recordLimitReader struct {
	r     io.Reader
	max   int64
	read  int64 // bytes handed to the decoder so far (absolute stream offset)
	start int64 // stream offset where the current record began
}

func (l *recordLimitReader) Read(p []byte) (int, error) {
	room := l.start + l.max - l.read
	if room <= 0 {
		// The decoder only asks for more when what it holds is not yet a
		// complete value, so the record in flight really is over the limit.
		return 0, fmt.Errorf("%w (%d bytes)", ErrRecordTooLarge, l.max)
	}
	if int64(len(p)) > room {
		p = p[:room]
	}
	n, err := l.r.Read(p)
	l.read += int64(n)
	return n, err
}
