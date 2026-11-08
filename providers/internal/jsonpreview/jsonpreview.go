// Package jsonpreview holds what the providers of "one big JSON document"
// exports (Telegram's result.json, Discord's account files) share for the parts
// of a document they do not map: consuming a value of any size token by token
// while keeping only a hash and a short compact-JSON preview of it, skipping a
// value, and a scalar type that tolerates an export format's drift.
package jsonpreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"

	"github.com/johnnycube/archivist/internal/bundle"
)

// Bytes is how much of a value's compact rendering a Rendering keeps.
const Bytes = 240

// Rendering describes one consumed JSON value.
type Rendering struct {
	Scalar bool
	Size   int64  // bytes of the compact rendering
	Hash   string // hex SHA-256 of the compact rendering
	head   []byte // its first Bytes bytes
}

// Preview is the head of the compact rendering as valid UTF-8, with an
// ellipsis when the value was longer.
func (r Rendering) Preview() string {
	s := strings.ToValidUTF8(string(r.head), "")
	if r.Size > int64(len(r.head)) {
		s += "…"
	}
	return s
}

// Complete returns the whole compact rendering when it fit within Bytes.
func (r Rendering) Complete() (json.RawMessage, bool) {
	return json.RawMessage(r.head), r.Size == int64(len(r.head))
}

// writer hashes everything written to it and keeps the first Bytes.
type writer struct {
	h    hash.Hash
	head []byte
	size int64
}

func (w *writer) WriteString(s string) {
	w.h.Write([]byte(s))
	w.size += int64(len(s))
	if room := Bytes - len(w.head); room > 0 {
		w.head = append(w.head, s[:min(room, len(s))]...)
	}
}

// Render consumes the next JSON value from dec, re-rendering it as compact JSON
// into a hash and a preview; it holds no more than the nesting stack in memory.
func Render(dec *bundle.RecordDecoder) (Rendering, error) {
	type frame struct {
		object  bool
		wantKey bool
		n       int
	}
	var stack []frame
	w := &writer{h: sha256.New()}
	scalar := true
	for {
		tok, err := dec.Token()
		if err != nil {
			return Rendering{}, fmt.Errorf("decode json: %w", err)
		}
		top := len(stack) - 1
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			w.WriteString(string(rune(d)))
			stack = stack[:top]
		} else if top >= 0 && stack[top].object && stack[top].wantKey {
			if stack[top].n > 0 {
				w.WriteString(",")
			}
			w.WriteString(quote(tok) + ":")
			stack[top].wantKey = false
			continue
		} else {
			if top >= 0 && !stack[top].object && stack[top].n > 0 {
				w.WriteString(",")
			}
			if top >= 0 {
				stack[top].n++
				stack[top].wantKey = true
			}
			switch t := tok.(type) {
			case json.Delim:
				scalar = false
				w.WriteString(string(rune(t)))
				stack = append(stack, frame{object: t == '{', wantKey: true})
				continue
			case json.Number:
				w.WriteString(t.String())
			case nil:
				w.WriteString("null")
			default: // string, bool
				w.WriteString(quote(t))
			}
		}
		if len(stack) == 0 {
			return Rendering{Scalar: scalar, Size: w.size, Hash: hex.EncodeToString(w.h.Sum(nil)), head: w.head}, nil
		}
	}
}

func quote(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Skip consumes the next JSON value token by token, without buffering it.
func Skip(dec *bundle.RecordDecoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return SkipOpened(dec, tok)
}

// SkipOpened finishes skipping a value whose first token has been read.
func SkipOpened(dec *bundle.RecordDecoder, first json.Token) error {
	if d, ok := first.(json.Delim); !ok || (d != '{' && d != '[') {
		return nil // scalar
	}
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode json: %w", err)
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '[' || d == '{' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// Str is a permissive scalar: a JSON string or number decodes to its text,
// anything else (null, bool, object, array) to "". Export formats drift —
// Telegram's from_id was a number before it became "user123", Discord's
// snowflake ids are numbers in one file and strings in the next (and exceed
// float64 precision) — and one odd field must never fail a whole import.
type Str string

func (s *Str) UnmarshalJSON(b []byte) error {
	var v string
	switch {
	case json.Unmarshal(b, &v) == nil:
		*s = Str(v)
	case len(b) > 0 && (b[0] == '-' || (b[0] >= '0' && b[0] <= '9')):
		*s = Str(b)
	default:
		*s = ""
	}
	return nil
}
