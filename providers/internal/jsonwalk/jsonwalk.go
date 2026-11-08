// Package jsonwalk streams a JSON document whose record arrays sit under
// dynamic or repeated member names (TikTok's "Chat History with jane:" or its
// two different "VideoList"s, Snapchat's per-conversation chat arrays), where
// bundle.StreamJSONArrays' fixed key list does not fit. The walker reports the
// full member-name path of everything it finds, so a provider can tell
// same-named arrays apart and can account for every part of the document.
package jsonwalk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/johnnycube/archivist/internal/bundle"
)

// flushBytes bounds the scalar members buffered per object before they are
// handed to Fields, so an object with a huge number of members stays streamed.
const flushBytes = 32 << 10

// Handler receives the content of a document. Objects are descended into; the
// path is the chain of member names from the root to the reported value and
// must not be retained (it is reused). A nil callback ignores its values.
type Handler struct {
	// Element is called for each element of every array reached through objects
	// alone; the element is decoded whole, including anything nested in it. A
	// top-level array has an empty path.
	Element func(path []string, el json.RawMessage) error
	// Fields is called with the scalar members of the object at path, gathered
	// into one JSON object in document order. Objects with very many scalar
	// members are delivered in several calls.
	Fields func(path []string, fields json.RawMessage) error
}

// Walk streams the document in r to h without ever holding more than one array
// element (or one batch of scalar members) in memory.
func Walk(r io.Reader, h Handler) error {
	// r is the member reader as handed to Extract, so the importer's
	// Limits.MaxRecordBytes applies; one oversized element fails the import
	// instead of being held in memory.
	dec := bundle.NewRecordLimitedDecoder(r, 0)
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil // empty member
		}
		return fmt.Errorf("decode json: %w", err)
	}
	w := walker{dec: dec, h: h}
	switch tok {
	case json.Delim('['):
		return w.array()
	case json.Delim('{'):
		return w.object()
	}
	return fmt.Errorf("decode json: expected an array or object, got %v", tok)
}

// maxDepth bounds object nesting. Exports nest a handful of levels; a hostile
// member of millions of nested objects would otherwise recurse until the stack
// overflows, which is fatal to the whole process and cannot be recovered.
const maxDepth = 64

type walker struct {
	dec  *bundle.RecordDecoder
	h    Handler
	path []string
}

// object walks one object whose opening brace has been consumed, through to its
// closing brace.
func (w *walker) object() error {
	if len(w.path) > maxDepth {
		return fmt.Errorf("decode json: objects nested deeper than %d", maxDepth)
	}
	var fields bytes.Buffer
	flush := func() error {
		if fields.Len() == 0 || w.h.Fields == nil {
			fields.Reset()
			return nil
		}
		fields.WriteByte('}')
		out := append(json.RawMessage(nil), fields.Bytes()...)
		fields.Reset()
		return w.h.Fields(w.path, out)
	}

	for w.dec.More() {
		name, err := w.dec.Token()
		if err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		key, _ := name.(string)
		val, err := w.dec.Token()
		if err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		switch val {
		case json.Delim('{'):
			w.path = append(w.path, key)
			err = w.object()
			w.path = w.path[:len(w.path)-1]
		case json.Delim('['):
			w.path = append(w.path, key)
			err = w.array()
			w.path = w.path[:len(w.path)-1]
		default:
			if w.h.Fields == nil {
				continue
			}
			k, _ := json.Marshal(key)
			v, merr := json.Marshal(val)
			if merr != nil {
				return fmt.Errorf("decode json: %w", merr)
			}
			if fields.Len() == 0 {
				fields.WriteByte('{')
			} else {
				fields.WriteByte(',')
			}
			fields.Write(k)
			fields.WriteByte(':')
			fields.Write(v)
			if fields.Len() >= flushBytes {
				err = flush()
			}
		}
		if err != nil {
			return err
		}
	}
	if _, err := w.dec.Token(); err != nil { // closing brace
		return fmt.Errorf("decode json: %w", err)
	}
	return flush()
}

// array streams the elements of one array whose opening bracket has been
// consumed, through to its closing bracket.
func (w *walker) array() error {
	for w.dec.More() {
		var el json.RawMessage
		if err := w.dec.Decode(&el); err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		if w.h.Element == nil {
			continue
		}
		if err := w.h.Element(w.path, el); err != nil {
			return err
		}
	}
	if _, err := w.dec.Token(); err != nil { // closing bracket
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}
