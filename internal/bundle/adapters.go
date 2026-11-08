package bundle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// ReadJSON decodes an entire member as a single JSON value into v. The whole
// document counts as one record, so it is for small members only: anything
// over the record limit fails with ErrRecordTooLarge — stream it instead.
func ReadJSON(r io.Reader, v any) error {
	dec := NewRecordLimitedDecoder(r, 0)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

// StreamJSONArray streams the elements of a JSON array to fn one at a time, so
// a multi-hundred-MB export member is never held in memory whole.
//
// If the document is a top-level array, its elements are streamed. If it is an
// object, the first top-level member whose name is in keys is streamed; it must
// be an array. A top-level array is always accepted, whatever keys is.
//
// Nothing else in the object is dropped silently: every other non-empty array
// — beside the streamed one or nested in wrapper objects, including a wanted
// key that occurs a second time — is reported through skipped (nil = not
// interested), exactly as StreamJSONArraysReport does; extractors turn that
// into a SkippedArrayRecord (see ReportSkipped).
//
// Memory is bounded per element: one larger than the record limit
// (Limits.MaxRecordBytes, default DefaultMaxRecordBytes) fails with
// ErrRecordTooLarge before it is buffered. The same holds for
// StreamJSONArrays and StreamJSVarArray.
func StreamJSONArray(r io.Reader, keys []string, fn func(json.RawMessage) error, skipped func(SkippedArray) error) error {
	dec := NewRecordLimitedDecoder(r, 0)
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil // empty member
		}
		return fmt.Errorf("decode json: %w", err)
	}
	switch tok {
	case json.Delim('['):
		return streamElements(dec, fn)
	case json.Delim('{'):
		streamed := false
		for dec.More() {
			name, err := dec.Token()
			if err != nil {
				return fmt.Errorf("decode json: %w", err)
			}
			key, _ := name.(string)
			open, err := dec.Token()
			if err != nil {
				return fmt.Errorf("decode json: %w", err)
			}
			if streamed || !contains(keys, key) {
				// Not ours: walk it like the multi-array reader does, with no
				// key to stream, so its arrays are counted and reported.
				if err := walkValue(dec, open, nil, nil, skipped, "", key, 1); err != nil {
					return err
				}
				continue
			}
			if open != json.Delim('[') {
				return fmt.Errorf("decode json: %q is not an array", name)
			}
			if err := streamElements(dec, fn); err != nil {
				return err
			}
			if _, err := dec.Token(); err != nil { // closing bracket
				return fmt.Errorf("decode json: %w", err)
			}
			streamed = true
		}
		if _, err := dec.Token(); err != nil { // closing brace
			return fmt.Errorf("decode json: %w", err)
		}
		return nil
	}
	return fmt.Errorf("decode json: expected an array or object, got %v", tok)
}

// StreamJSONArrays is StreamJSONArray for documents that hold several record
// arrays, possibly nested in wrapper objects (Facebook's
// {"event_responses_v2": {"events_joined": [...], "events_declined": [...]}}).
// Every array whose member name is in keys is streamed to fn along with that
// name; objects are descended into and everything else is skipped. A top-level
// array is streamed with key "".
//
// Arrays under other names are dropped silently: extractors should prefer
// StreamJSONArraysReport, which says what was skipped.
func StreamJSONArrays(r io.Reader, keys []string, fn func(key string, el json.RawMessage) error) error {
	return StreamJSONArraysReport(r, keys, fn, nil)
}

// SkippedArray is a non-empty array that StreamJSONArraysReport did not stream
// because its name is not one of the keys asked for.
type SkippedArray struct {
	Key   string // dotted path from the document root: "event_responses_v2.events_hidden"
	Count int    // number of elements
}

// StreamJSONArraysReport is StreamJSONArrays that also reports, through skipped,
// every non-empty array it passes over (counted token by token, never
// buffered), so a matched member whose data sits under a name the extractor
// does not know still leaves a trace — see SkippedArrayRecord. Arrays inside
// streamed elements are the caller's; objects nested deeper than the depth
// bound are skipped unseen, as in StreamJSONArrays.
func StreamJSONArraysReport(r io.Reader, keys []string, fn func(key string, el json.RawMessage) error, skipped func(SkippedArray) error) error {
	dec := NewRecordLimitedDecoder(r, 0)
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil // empty member
		}
		return fmt.Errorf("decode json: %w", err)
	}
	switch tok {
	case json.Delim('['):
		return streamElements(dec, func(el json.RawMessage) error { return fn("", el) })
	case json.Delim('{'):
		return streamObjectArrays(dec, keys, fn, skipped, "", 1)
	}
	return fmt.Errorf("decode json: expected an array or object, got %v", tok)
}

// maxObjectDepth bounds how deep StreamJSONArrays follows nested objects. Real
// exports nest record arrays two or three wrappers deep; without a bound a
// hostile member of millions of nested objects would recurse until the stack
// overflows, which kills the process (it cannot be recovered).
const maxObjectDepth = 32

// streamObjectArrays walks one object whose opening brace has been consumed,
// through to its closing brace. Objects nested deeper than maxObjectDepth are
// skipped rather than descended into. prefix is the object's own key path.
func streamObjectArrays(dec *RecordDecoder, keys []string, fn func(key string, el json.RawMessage) error, skipped func(SkippedArray) error, prefix string, depth int) error {
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		key, _ := name.(string)
		open, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		if err := walkValue(dec, open, keys, fn, skipped, prefix, key, depth); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

// walkValue handles one object member named key (of the object at prefix and
// depth) whose value's first token, open, has been consumed: a wanted array is
// streamed, any other non-empty array counted and reported, an object
// descended into (up to maxObjectDepth), a scalar passed over.
func walkValue(dec *RecordDecoder, open json.Token, keys []string, fn func(key string, el json.RawMessage) error, skipped func(SkippedArray) error, prefix, key string, depth int) (err error) {
	switch {
	case open == json.Delim('{') && depth >= maxObjectDepth:
		_, err = skipRest(dec)
	case open == json.Delim('{'):
		err = streamObjectArrays(dec, keys, fn, skipped, prefix+key+".", depth+1)
	case open == json.Delim('[') && contains(keys, key):
		err = streamElements(dec, func(el json.RawMessage) error { return fn(key, el) })
		if err == nil {
			_, err = dec.Token() // closing bracket
		}
	case open == json.Delim('['):
		var n int
		if n, err = skipRest(dec); err == nil && n > 0 && skipped != nil {
			err = skipped(SkippedArray{Key: prefix + key, Count: n})
		}
	}
	return err
}

// skipRest consumes the remainder of a container whose opening delimiter has
// already been read, and returns how many values it held directly (an array's
// element count).
func skipRest(dec *RecordDecoder) (int, error) {
	n := 0
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return n, fmt.Errorf("decode json: %w", err)
		}
		d, isDelim := tok.(json.Delim)
		switch {
		case isDelim && (d == ']' || d == '}'):
			depth--
			continue
		case depth == 1:
			n++
		}
		if isDelim {
			depth++
		}
	}
	return n, nil
}

// streamElements decodes array elements until the closing bracket; the opening
// bracket has already been consumed.
func streamElements(dec *RecordDecoder, fn func(json.RawMessage) error) error {
	for dec.More() {
		var el json.RawMessage
		if err := dec.Decode(&el); err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		if err := fn(el); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// StreamJSVarArray parses a Twitter/X archive file, whose members look like:
//
//	window.YTD.tweets.part0 = [ { ... }, { ... } ]
//
// It skips the leading `window.YTD.<name>.partN = ` assignment and streams the
// top-level JSON array's elements to fn (anything after the array is ignored).
func StreamJSVarArray(r io.Reader, fn func(json.RawMessage) error) error {
	var max int64 // resolve before bufio hides the member reader's limit
	if rl, ok := r.(recordLimiter); ok {
		max = rl.MaxRecordBytes()
	}
	br := bufio.NewReader(r)
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return fmt.Errorf("no JSON array found in JS-wrapped file")
		}
		if err != nil {
			return err
		}
		if b == '[' {
			break
		}
	}
	// Hand the bracket back: the decoder must open the array itself.
	if err := br.UnreadByte(); err != nil {
		return err
	}
	dec := NewRecordLimitedDecoder(br, max)
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("parse JS-wrapped array: %w", err)
	}
	if err := streamElements(dec, fn); err != nil {
		return fmt.Errorf("parse JS-wrapped array: %w", err)
	}
	return nil
}

// ReadJSVarArray is StreamJSVarArray collected into a slice (small files/tests).
func ReadJSVarArray(r io.Reader) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := StreamJSVarArray(r, func(el json.RawMessage) error {
		out = append(out, el)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FixMetaMojibake repairs the encoding bug in Facebook/Instagram JSON exports,
// where UTF-8 bytes were escaped individually as \u00XX. When parsed, each
// original byte becomes a separate rune in [0,255]. Re-collapsing those runes to
// bytes and reinterpreting as UTF-8 restores the real text.
//
// If the string contains any rune > 255 it is already valid UTF-8 and is
// returned unchanged (idempotent, safe to call on clean data).
func FixMetaMojibake(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		if r > 0xFF {
			return s // already proper UTF-8
		}
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	if !utf8.Valid(b) {
		return s // repair would produce invalid UTF-8; leave as-is
	}
	return string(b)
}
