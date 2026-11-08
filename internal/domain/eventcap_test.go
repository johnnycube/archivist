package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCappedLeavesOrdinaryEventsAlone(t *testing.T) {
	ev := Event{Title: "hello", Body: strings.Repeat("b", 1000), Metadata: map[string]any{"k": "v"}}
	got, cut := ev.Capped()
	if cut || got.Title != ev.Title || got.Body != ev.Body || len(got.Metadata) != 1 {
		t.Errorf("Capped changed an ordinary event: %+v, %v", got, cut)
	}
}

func TestCappedCutsAndRecords(t *testing.T) {
	ev := Event{
		Title:    strings.Repeat("ü", MaxTitleBytes), // 2 bytes per rune: twice the bound
		Body:     strings.Repeat("x", MaxBodyBytes+10),
		Metadata: map[string]any{"blob": strings.Repeat("m", MaxMetadataBytes)},
	}
	got, cut := ev.Capped()
	if !cut {
		t.Fatal("nothing was cut")
	}
	if len(got.Title) > MaxTitleBytes || !utf8.ValidString(got.Title) || !strings.HasSuffix(got.Title, "…") {
		t.Errorf("title: %d bytes, valid %v", len(got.Title), utf8.ValidString(got.Title))
	}
	if len(got.Body) > MaxBodyBytes || !strings.HasSuffix(got.Body, "…") {
		t.Errorf("body: %d bytes", len(got.Body))
	}
	rec, ok := got.Metadata[TruncatedKey].(map[string]any)
	if !ok || rec["title"] != 2*MaxTitleBytes || rec["body"] != MaxBodyBytes+10 || rec["metadata"] == nil {
		t.Errorf("truncation record = %#v", got.Metadata)
	}
	if _, kept := got.Metadata["blob"]; kept {
		t.Error("oversized metadata kept")
	}
	if len(ev.Title) != 2*MaxTitleBytes || ev.Metadata["blob"] == nil {
		t.Error("Capped modified its receiver's fields")
	}
}

func TestCappedKeepsSmallMetadataWhenCuttingText(t *testing.T) {
	got, _ := Event{Body: strings.Repeat("x", MaxBodyBytes+1), Metadata: map[string]any{"k": "v"}}.Capped()
	if got.Metadata["k"] != "v" || got.Metadata[TruncatedKey] == nil {
		t.Errorf("metadata = %#v", got.Metadata)
	}
}

func TestCappedCompactsLongIDsAndTheirReferences(t *testing.T) {
	long := "like:https://example.com/" + strings.Repeat("p", 3000)
	ev := Event{
		ExternalID: long, ThreadExternalID: long, ParentExternalID: "short",
		Targets: []Target{{TargetExternalID: long}, {TargetExternalID: "post:1"}},
	}
	got, cut := ev.Capped()
	if !cut || !strings.HasPrefix(got.ExternalID, "sha256:") || len(got.ExternalID) > MaxExternalIDBytes {
		t.Fatalf("ExternalID = %q (cut %v)", got.ExternalID, cut)
	}
	if got.ThreadExternalID != got.ExternalID || got.Targets[0].TargetExternalID != got.ExternalID {
		t.Error("references to the long id no longer match it")
	}
	if got.ParentExternalID != "short" || got.Targets[1].TargetExternalID != "post:1" {
		t.Error("short ids changed")
	}
	if ev.Targets[0].TargetExternalID != long {
		t.Error("Capped wrote through to the caller's targets")
	}
	if again, _ := ev.Capped(); again.ExternalID != got.ExternalID {
		t.Error("the digest form is not deterministic")
	}
	if rec := got.Metadata[TruncatedKey].(map[string]any); rec["external_id"] != len(long) {
		t.Errorf("truncation record = %v", rec)
	}
}
