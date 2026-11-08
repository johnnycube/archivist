package chatgpt

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

func TestImportChatGPT(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "chatgpt.zip")
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

	if cov.Mapped != 2 { // conversations.json + conversations-000.json
		t.Errorf("Mapped = %d, want 2", cov.Mapped)
	}
	// conv1: user + assistant + regenerated assistant (system priming is empty);
	// conv2: 1; conv3: image message + code + tool output; conv4: 1.
	if cov.Events != 8 || cov.PerType["chatgpt.message"] != 8 {
		t.Errorf("Events = %d, PerType = %v, want 8 chatgpt.message", cov.Events, cov.PerType)
	}

	// The user's trip message comes first in conv1, threaded, dated with sub-second precision.
	first := findByBody(events, "Plan a trip")
	if first == nil {
		t.Fatal("user trip message missing")
	}
	if first.Actor != "user" || first.ThreadExternalID != "conv1" || first.ExternalID != "msg:m1" ||
		!first.OccurredAt.Equal(time.Unix(1700000001, 123000000).UTC()) || first.Metadata["on_current_branch"] != true {
		t.Errorf("user message not mapped: %+v", first)
	}
	// The assistant reply links back to the user message as its parent and knows its model.
	if a := findByBody(events, "7-day plan"); a == nil || a.ParentExternalID != "m1" || a.Metadata["model"] != "gpt-4o" || a.Metadata["on_current_branch"] != true {
		t.Errorf("assistant reply = %+v, want parent m1 on the current branch", a)
	}
	// The regenerated answer on the abandoned branch is kept, flagged off-branch,
	// and sorted by its own time (before the final answer).
	if b := findByBody(events, "regenerated answer"); b == nil || b.Metadata["on_current_branch"] != false || b.ParentExternalID != "m1" {
		t.Errorf("off-branch message = %+v", b)
	} else if idx(events, b) > idx(events, findByBody(events, "7-day plan")) {
		t.Errorf("messages not ordered by create_time")
	}

	// conv3: an image-only part becomes Media; the attachment list is kept.
	img := findByBody(events, "What is in this picture?")
	if img == nil || len(img.Media) != 1 || img.Media[0].URL != "file-service://file-AbC123" || img.Media[0].MIME != "image/*" ||
		img.Metadata["content_type"] != "multimodal_text" || img.Metadata["attachments"] == nil {
		t.Errorf("multimodal message not mapped: %+v", img)
	}
	// A code message with create_time null inherits its parent's time and says so.
	code := findByBody(events, "print('hello')")
	if code == nil || code.Metadata["content_type"] != "code" || code.Metadata["language"] != "python" ||
		!code.OccurredAt.Equal(time.Unix(1700200001, 0).UTC()) || code.Metadata["time_inferred"] != true || code.Metadata["recipient"] != "python" {
		t.Errorf("code message not mapped: %+v", code)
	}
	// A tool message names the tool in its actor.
	if tool := findByBody(events, "hello"); tool == nil {
		t.Error("tool output missing")
	} else {
		var out *domain.Event
		for i := range events {
			if events[i].Actor == "tool:python" {
				out = &events[i]
			}
		}
		if out == nil || out.Body != "hello" || out.ParentExternalID != "m5" || out.Metadata["content_type"] != "execution_output" {
			t.Errorf("tool message not mapped: %+v", out)
		}
	}
	// The numbered part of a split export is read too.
	if p := findByBody(events, "numbered part"); p == nil || p.ThreadExternalID != "conv4" {
		t.Errorf("conversations-000.json not imported: %+v", p)
	}

	ids := map[string]bool{}
	for _, ev := range events {
		if ids[ev.ExternalID] {
			t.Errorf("ExternalID %q reused", ev.ExternalID)
		}
		ids[ev.ExternalID] = true
		if ev.OccurredAt.IsZero() {
			t.Errorf("%s has no date", ev.ExternalID)
		}
	}
}

func TestMatch(t *testing.T) {
	ex := convExtractor{}
	for member, want := range map[string]bool{
		"conversations.json":            true,
		"export/conversations.json":     true,
		"conversations-000.json":        true,
		"conversations-12.json":         true,
		"shared_conversations.json":     false,
		"conversations-abc.json":        false,
		"conversations.html":            false,
		"chat.html":                     false,
		"file-AbC123-photo.png":         false,
		"dalle-generations/file-1.webp": false,
	} {
		if got := ex.Match(member); got != want {
			t.Errorf("Match(%q) = %v, want %v", member, got, want)
		}
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

func findByBody(evs []domain.Event, sub string) *domain.Event {
	for i := range evs {
		if strings.Contains(evs[i].Body, sub) {
			return &evs[i]
		}
	}
	return nil
}
