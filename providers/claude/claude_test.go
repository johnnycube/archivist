package claude

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func TestImportClaude(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "claude.zip")
	if err := archivetest.ZipDir("testdata", zipPath); err != nil {
		t.Fatal(err)
	}

	var events []domain.Event
	cov, err := bundle.NewImporter(New()).ImportZip(context.Background(), zipPath, func(ev domain.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]int{
		"claude.message":     8, // c1: 2, c2: 2, c3: 4 (one file-only)
		"claude.project":     2,
		"claude.project_doc": 2,
		"claude.unknown":     1, // users.json: no documented schema, captured generically
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType = %v, want %v", cov.PerType, want)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	if cov.Mapped != 2 || cov.Generic != 1 {
		t.Errorf("mapped/generic = %d/%d, want 2/1", cov.Mapped, cov.Generic)
	}

	// The assistant reply parents to the previous human message in the thread;
	// microsecond timestamps parse.
	a := findByBody(events, "set of method signatures")
	if a == nil || a.ParentExternalID != "m1" || a.Actor != "assistant" || a.Metadata["model"] != "claude-3-5-sonnet-20241022" ||
		!a.OccurredAt.Equal(time.Date(2024, 5, 1, 10, 0, 5, 0, time.UTC)) {
		t.Errorf("assistant reply = %+v, want parent m1", a)
	}
	// Empty text falls back to the content[] array; attachments become Media.
	m := findByBody(events, "Body via content array")
	if m == nil {
		t.Fatal("expected content-array fallback body to be ingested")
	}
	if m.Actor != "human" || m.ThreadExternalID != "c2" || len(m.Media) != 1 || m.Media[0].URL != "notes.pdf" || m.Metadata["attachments"] == nil {
		t.Errorf("fallback msg = %+v, want human/c2 with one attachment", m)
	}
	// Tool blocks are noted; the "+00:00" timestamp form parses.
	if r := findByBody(events, "Here is what I found."); r == nil || !r.OccurredAt.Equal(time.Date(2024, 5, 2, 9, 0, 10, 500_000_000, time.UTC)) ||
		len(r.Metadata["block_types"].([]string)) != 3 {
		t.Errorf("tool-using reply = %+v", r)
	}

	// c3 arrives out of order with index/parent_message_uuid: ordering follows
	// index, parents follow the explicit link, the root's nil-uuid parent is dropped.
	first, second, third := findByBody(events, "First"), findByBody(events, "Second"), findByBody(events, "Third")
	if first == nil || second == nil || third == nil {
		t.Fatal("tree-shaped conversation not imported")
	}
	if first.ParentExternalID != "" || second.ParentExternalID != "t1" || third.ParentExternalID != "t2" {
		t.Errorf("parents = %q/%q/%q, want \"\"/t1/t2", first.ParentExternalID, second.ParentExternalID, third.ParentExternalID)
	}
	if idx(events, first) > idx(events, second) || idx(events, second) > idx(events, third) {
		t.Error("messages not ordered by index")
	}
	// A message with no text but a file is kept.
	var fileOnly *domain.Event
	for i := range events {
		if events[i].ExternalID == "msg:t4" {
			fileOnly = &events[i]
		}
	}
	if fileOnly == nil || fileOnly.Metadata["files"] == nil || !strings.HasSuffix(fileOnly.Title, "(attachment)") {
		t.Errorf("file-only message = %+v", fileOnly)
	}

	// projects.json
	pr := byTitle(events, "Project: Archivist")
	if pr == nil || pr.Body != "You are helping with a Go codebase." || pr.Metadata["docs"] != 2 || pr.Metadata["private"] != true ||
		pr.ExternalID != "project:p1" || pr.Category != domain.CategoryContent ||
		!pr.OccurredAt.Equal(time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("project = %+v", pr)
	}
	if empty := byTitle(events, "Project: Empty project"); empty == nil || empty.Metadata["docs"] != 0 || empty.Metadata["starter_project"] != true {
		t.Errorf("empty project = %+v", empty)
	}
	doc := byTitle(events, "ARCHITECTURE.md · Archivist")
	if doc == nil || doc.ThreadExternalID != "project:p1" || doc.ParentExternalID != "project:p1" || !strings.Contains(doc.Body, "Hexagonal") ||
		doc.ExternalID != "project_doc:d1" {
		t.Errorf("project doc = %+v", doc)
	}

	ids := map[string]bool{}
	for _, ev := range events {
		if ids[ev.ExternalID] {
			t.Errorf("ExternalID %q reused", ev.ExternalID)
		}
		ids[ev.ExternalID] = true
	}
}

func idx(evs []domain.Event, e *domain.Event) int {
	for i := range evs {
		if &evs[i] == e {
			return i
		}
	}
	return -1
}

func byTitle(evs []domain.Event, title string) *domain.Event {
	for i := range evs {
		if evs[i].Title == title {
			return &evs[i]
		}
	}
	return nil
}

func findByBody(evs []domain.Event, sub string) *domain.Event {
	for i := range evs {
		if strings.Contains(evs[i].Body, sub) {
			return &evs[i]
		}
	}
	return nil
}
