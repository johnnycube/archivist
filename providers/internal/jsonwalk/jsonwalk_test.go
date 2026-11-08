package jsonwalk

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWalk(t *testing.T) {
	const doc = `{
		"A": {"List": [{"n": 1}, {"n": 2}], "note": "x", "big": 12345678901234567890},
		"B": {"Chat with jane:": [{"n": 3}], "Deep": {"List": [[1, 2], "s"]}, "empty": [], "none": {}},
		"top": true, "nil": null
	}`
	var got []string
	err := Walk(strings.NewReader(doc), Handler{
		Element: func(path []string, el json.RawMessage) error {
			got = append(got, strings.Join(path, "/")+" = "+string(el))
			return nil
		},
		Fields: func(path []string, f json.RawMessage) error {
			if !json.Valid(f) {
				t.Errorf("fields at %v are not valid JSON: %s", path, f)
			}
			got = append(got, strings.Join(path, "/")+" : "+string(f))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`A/List = {"n": 1}`,
		`A/List = {"n": 2}`,
		`A : {"note":"x","big":12345678901234567890}`,
		`B/Chat with jane: = {"n": 3}`,
		`B/Deep/List = [1, 2]`,
		`B/Deep/List = "s"`,
		` : {"top":true,"nil":null}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWalkTopLevelArrayAndEmpty(t *testing.T) {
	var n int
	h := Handler{Element: func(path []string, _ json.RawMessage) error {
		if len(path) != 0 {
			t.Errorf("top-level array path = %v", path)
		}
		n++
		return nil
	}}
	if err := Walk(strings.NewReader(`[1, {"a": [2]}]`), h); err != nil || n != 2 {
		t.Errorf("n = %d, err = %v", n, err)
	}
	if err := Walk(strings.NewReader(``), h); err != nil {
		t.Errorf("empty member: %v", err)
	}
	if err := Walk(strings.NewReader(`{"a": [1, `), Handler{}); err == nil {
		t.Error("truncated document should fail")
	}
}

func TestWalkFlushesLargeObjectsInBatches(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"m": {`)
	const members = 5000
	for i := 0; i < members; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"key_` + strings.Repeat("x", 20) + `_` + string(rune('a'+i%26)) + `": "value"`)
	}
	sb.WriteString(`}}`)
	calls, total := 0, 0
	err := Walk(strings.NewReader(sb.String()), Handler{Fields: func(_ []string, f json.RawMessage) error {
		calls++
		if len(f) > flushBytes+1024 {
			t.Errorf("batch of %d bytes exceeds the bound", len(f))
		}
		total += strings.Count(string(f), `"value"`)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 || total != members {
		t.Errorf("calls = %d, members seen = %d, want several calls covering %d", calls, total, members)
	}
}

// Unbounded recursion on hostile nesting would overflow the stack, which kills
// the process; the walker must refuse instead.
func TestWalkRejectsHostileNesting(t *testing.T) {
	const depth = 2_000_000
	in := strings.Repeat(`{"a":`, depth) + `1` + strings.Repeat(`}`, depth)
	if err := Walk(strings.NewReader(in), Handler{}); err == nil {
		t.Fatal("want an error for deeply nested objects, got nil")
	}
}
