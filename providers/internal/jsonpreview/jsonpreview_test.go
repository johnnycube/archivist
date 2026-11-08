package jsonpreview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/bundle"
)

func dec(s string) *bundle.RecordDecoder {
	d := bundle.NewRecordLimitedDecoder(strings.NewReader(s), 0)
	d.UseNumber()
	return d
}

// Render re-renders any value as compact JSON — exactly what json.Compact makes
// of it — and leaves the decoder right behind the value.
func TestRenderIsCompactJSON(t *testing.T) {
	for _, in := range []string{
		`"text"`, `12345678901234567890`, `-1.5e3`, `true`, `null`, `[]`, `{}`,
		`[1, "two", [3, [4]], {"five": 5}, null, false]`,
		`{"a": {"b": [ {"c": "d\n\"e\""}, {} ], "f": null}, "g": "ü"}`,
	} {
		d := dec("[" + in + `, "next"]`)
		d.Token() // [
		got, err := Render(d)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		var want bytes.Buffer
		if err := json.Compact(&want, []byte(in)); err != nil {
			t.Fatal(err)
		}
		compact := want.Bytes()
		whole, ok := got.Complete()
		if !ok || string(whole) != want.String() || got.Preview() != want.String() || got.Size != int64(want.Len()) {
			t.Errorf("%s: rendered %q (complete %v, size %d), want %q", in, whole, ok, got.Size, want.String())
		}
		sum := sha256.Sum256(compact)
		if got.Hash != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: hash is not that of the compact rendering", in)
		}
		if wantScalar := !strings.ContainsAny(in[:1], "[{"); got.Scalar != wantScalar {
			t.Errorf("%s: Scalar = %v", in, got.Scalar)
		}
		var next string
		if err := d.Decode(&next); err != nil || next != "next" {
			t.Errorf("%s: decoder not left behind the value: %q %v", in, next, err)
		}
	}
}

// A long value is hashed and sized in full but only its head is kept, cut to
// valid UTF-8.
func TestRenderKeepsOnlyTheHead(t *testing.T) {
	in := `{"k": "` + strings.Repeat("ü", 5000) + `"}`
	got, err := Render(dec(in))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Complete(); ok || got.Size != int64(len(`{"k":"`)+10000+len(`"}`)) {
		t.Errorf("Complete = %v, Size = %d", ok, got.Size)
	}
	p := got.Preview()
	if !strings.HasPrefix(p, `{"k":"üü`) || !strings.HasSuffix(p, "…") || len(p) > Bytes+len("…") || strings.ContainsRune(p, '�') {
		t.Errorf("Preview = %q", p)
	}
}

func TestRenderAndSkipReportTruncatedInput(t *testing.T) {
	if _, err := Render(dec(`{"a": [1, 2`)); err == nil {
		t.Error("Render: no error on truncated input")
	}
	if err := Skip(dec(`[[1], {"a"`)); err == nil {
		t.Error("Skip: no error on truncated input")
	}
	d := bundle.NewRecordLimitedDecoder(strings.NewReader(`["`+strings.Repeat("x", 5000)+`"]`), 1024)
	if _, err := Render(d); !errors.Is(err, bundle.ErrRecordTooLarge) {
		t.Errorf("Render past the record limit: err = %v", err)
	}
}

func TestSkip(t *testing.T) {
	d := dec(`[{"a": [1, {"b": []}]}, 7, "s", [[]], "next"]`)
	d.Token() // [
	for i := 0; i < 4; i++ {
		if err := Skip(d); err != nil {
			t.Fatal(err)
		}
	}
	var next string
	if err := d.Decode(&next); err != nil || next != "next" {
		t.Fatalf("after 4 skips: %q %v", next, err)
	}

	d = dec(`{"a": 1} "next"`)
	tok, _ := d.Token()
	if err := SkipOpened(d, tok); err != nil {
		t.Fatal(err)
	}
	if err := d.Decode(&next); err != nil || next != "next" {
		t.Fatalf("after SkipOpened: %q %v", next, err)
	}
}

func TestStr(t *testing.T) {
	var v struct{ A, B, C, D, E, F Str }
	err := json.Unmarshal([]byte(`{"A": "user123", "B": 1234567890123456789, "C": -4.5, "D": null, "E": {"x": 1}, "F": true}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.A != "user123" || v.B != "1234567890123456789" || v.C != "-4.5" || v.D != "" || v.E != "" || v.F != "" {
		t.Errorf("decoded %+v", v)
	}
}
