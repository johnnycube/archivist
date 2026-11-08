package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReadJSVarArray(t *testing.T) {
	in := `window.YTD.tweets.part0 = [ {"tweet":{"id_str":"1"}}, {"tweet":{"id_str":"2"}} ];`
	got, err := ReadJSVarArray(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("elements = %d, want 2", len(got))
	}
}

func TestFixMetaMojibake(t *testing.T) {
	// "café" UTF-8 is 0x63 0x61 0x66 0xC3 0xA9. The Meta bug escapes each byte,
	// so JSON parsing yields the runes U+00C3 U+00A9 for the "é".
	broken := "cafÃ©"
	if got := FixMetaMojibake(broken); got != "café" {
		t.Errorf("FixMetaMojibake(%q) = %q, want %q", broken, got, "café")
	}
	// Already-correct text is returned unchanged (idempotent).
	if got := FixMetaMojibake("café"); got != "café" {
		t.Errorf("idempotency failed: got %q", got)
	}
	if got := FixMetaMojibake("plain ascii"); got != "plain ascii" {
		t.Errorf("ascii changed: got %q", got)
	}
}

func TestStreamJSONArrays(t *testing.T) {
	collect := func(in string, keys ...string) []string {
		t.Helper()
		var got []string
		err := StreamJSONArrays(strings.NewReader(in), keys, func(key string, el json.RawMessage) error {
			got = append(got, key+"="+string(el))
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		return got
	}

	// Nested wrapper, several wanted arrays, and unwanted values of every shape
	// before, between and after them.
	in := `{"skip":[1,[2,{"a":[3]}]],"n":1,"wrap":{"a":[{"x":1},{"x":2}],"other":{"deep":[9]},"b":[{"y":3}]},"tail":"s","a":[{"x":4}]}`
	got := strings.Join(collect(in, "a", "b"), " ")
	want := `a={"x":1} a={"x":2} b={"y":3} a={"x":4}`
	if got != want {
		t.Errorf("nested: got %s, want %s", got, want)
	}

	if got := collect(`[{"x":1},{"x":2}]`, "a"); len(got) != 2 || got[0] != `={"x":1}` {
		t.Errorf("top-level array: got %v", got)
	}
	if got := collect(``, "a"); got != nil {
		t.Errorf("empty member: got %v", got)
	}
	if got := collect(`{"a":"not an array","b":{"a":[7]}}`, "a"); len(got) != 1 || got[0] != "a=7" {
		t.Errorf("non-array value under a wanted key: got %v", got)
	}
}

// A hostile member of deeply nested objects must not recurse without bound: a
// Go stack overflow is fatal and would take the whole server down.
func TestStreamJSONArraysDepthBound(t *testing.T) {
	// Deeper than maxObjectDepth, so the array is skipped, but within
	// encoding/json's own nesting limit (10 000 since Go 1.27).
	const depth = 5_000
	in := strings.Repeat(`{"a":`, depth) + `[1]` + strings.Repeat(`}`, depth)
	n := 0
	err := StreamJSONArrays(strings.NewReader(in), []string{"a"}, func(string, json.RawMessage) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("deeply nested input: %v", err)
	}
	if n != 0 {
		t.Errorf("array below maxObjectDepth was streamed %d times, want it skipped", n)
	}

	// Millions deep: refused at MaxTokenDepth on every Go version (it is below
	// Go 1.27's own 10,000), before the decoder's token stack can grow.
	// Nothing is streamed and nothing overflows.
	const hostileDepth = 2_000_000
	hostile := strings.Repeat(`{"a":`, hostileDepth) + `[1]` + strings.Repeat(`}`, hostileDepth)
	if err := StreamJSONArrays(strings.NewReader(hostile), []string{"a"}, func(string, json.RawMessage) error {
		n++
		return nil
	}); !errors.Is(err, ErrTooDeep) || n != 0 {
		t.Errorf("hostile input: n=%d err=%v", n, err)
	}

	shallow := strings.Repeat(`{"w":`, maxObjectDepth-1) + `{"a":[1]}` + strings.Repeat(`}`, maxObjectDepth-1)
	if err := StreamJSONArrays(strings.NewReader(shallow), []string{"a"}, func(string, json.RawMessage) error {
		n++
		return nil
	}); err != nil || n != 1 {
		t.Errorf("array at maxObjectDepth: n=%d err=%v, want 1 element", n, err)
	}
}

// StreamJSONArraysReport names every non-empty array it passes over — with its
// path and element count — and nothing else: not empty arrays, not arrays
// inside streamed elements, not scalars or objects.
func TestStreamJSONArraysReportSkipped(t *testing.T) {
	doc := `{
		"title": "x", "empty": [], "tags": ["a", "b", "c"],
		"rows": [{"id": 1, "nested": [1, 2]}, {"id": 2}],
		"wrapper": {"rows": [{"id": 3}], "other_v2": [{"deep": [[1], {"a": []}]}, 7, "s", null], "inner": {"more": [true]}}
	}`
	var streamed []string
	var skipped []SkippedArray
	err := StreamJSONArraysReport(strings.NewReader(doc), []string{"rows"},
		func(key string, el json.RawMessage) error { streamed = append(streamed, key+string(el)); return nil },
		func(sa SkippedArray) error { skipped = append(skipped, sa); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(streamed) != 3 {
		t.Errorf("streamed = %v, want the 3 rows", streamed)
	}
	want := []SkippedArray{{"tags", 3}, {"wrapper.other_v2", 4}, {"wrapper.inner.more", 1}}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}

	// The callback's error stops the walk; StreamJSONArrays itself is unchanged.
	stop := errors.New("stop")
	err = StreamJSONArraysReport(strings.NewReader(doc), []string{"rows"},
		func(string, json.RawMessage) error { return nil }, func(SkippedArray) error { return stop })
	if !errors.Is(err, stop) {
		t.Errorf("err = %v, want the callback's", err)
	}
	if err := StreamJSONArrays(strings.NewReader(doc), []string{"rows"}, func(string, json.RawMessage) error { return nil }); err != nil {
		t.Error(err)
	}
}

// Skipping counts token by token, so a huge skipped array is never buffered,
// one oversized element in it still trips the record limit, and objects below
// the depth bound are not looked into.
func TestStreamJSONArraysReportBounds(t *testing.T) {
	var doc strings.Builder
	doc.WriteString(`{"big": [`)
	for i := 0; i < 50_000; i++ {
		if i > 0 {
			doc.WriteByte(',')
		}
		doc.WriteString(`{"n": 1, "s": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)
	}
	doc.WriteString(`]}`)
	var got SkippedArray
	r := &limitReader{r: strings.NewReader(doc.String()), remaining: -1, maxRecord: 4096} // far below the array's 2.3 MB
	err := StreamJSONArraysReport(r, nil, nil, func(sa SkippedArray) error { got = sa; return nil })
	if err != nil || got != (SkippedArray{"big", 50_000}) {
		t.Fatalf("got %v, err %v; want big x 50000", got, err)
	}

	r = &limitReader{r: strings.NewReader(`{"big": ["` + strings.Repeat("y", 10_000) + `"]}`), remaining: -1, maxRecord: 4096}
	if err := StreamJSONArraysReport(r, nil, nil, func(SkippedArray) error { return nil }); !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("oversized element in a skipped array: err = %v, want ErrRecordTooLarge", err)
	}

	deep := strings.Repeat(`{"o":`, maxObjectDepth+5) + `{"arr": [1]}` + strings.Repeat(`}`, maxObjectDepth+5)
	called := false
	if err := StreamJSONArraysReport(strings.NewReader(deep), nil, nil, func(SkippedArray) error { called = true; return nil }); err != nil || called {
		t.Errorf("below the depth bound: err = %v, reported = %v; want nil, false", err, called)
	}
}

// A skipped array becomes a "<source>.unknown" event; the member is part of the
// record, so the same key in two members is two records and two events.
func TestSkippedArrayRecordAndEvent(t *testing.T) {
	a := SkippedArrayRecord("dir/a.json", SkippedArray{Key: "w.items_v3", Count: 12})
	b := SkippedArrayRecord("dir/b.json", SkippedArray{Key: "w.items_v3", Count: 12})
	if bytes.Equal(a.Payload, b.Payload) {
		t.Error("records of different members must differ (they are deduped by payload)")
	}
	ev, err := SkippedArrayEvent("facebook", a)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "facebook.unknown" || ev.ExternalID != "facebook:unknown:dir/a.json#w.items_v3" ||
		ev.Metadata["member"] != "dir/a.json" || ev.Metadata["key"] != "w.items_v3" || ev.Metadata["count"] != 12 {
		t.Errorf("event = %+v", ev)
	}
}
