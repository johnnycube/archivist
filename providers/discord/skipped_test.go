package discord

import (
	"context"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

// Never drop data: an array the extractor does not stream — here because the
// member is not shaped the way this provider knows it — becomes a
// discord.unknown event that says which array, and how many records, were passed
// over.
func TestUnexpectedArrayBecomesUnknownEvent(t *testing.T) {
	const member = "messages/c100000000000000009/messages.json"
	zip, size, err := archivetest.ZipReader(map[string]string{member: `{"messages":[{"ID":"900000000000000001","Timestamp":"2024-01-01T10:00:00+00:00","Contents":"hello","Attachments":""}],"threads":[{"id":1},{"id":2}]}`})
	if err != nil {
		t.Fatal(err)
	}
	var unknown []domain.Event
	mapped := 0
	cov, err := bundle.NewImporter(New()).ImportParts(context.Background(), []bundle.Part{{ReaderAt: zip, Size: size}},
		func(ev domain.Event) error {
			if ev.Type == "discord.unknown" {
				unknown = append(unknown, ev)
			} else {
				mapped++
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if mapped != 1 {
		t.Errorf("mapped events = %d, want 1", mapped)
	}
	if len(unknown) != 1 {
		t.Fatalf("unknown events = %d (%+v), want 1; coverage %+v", len(unknown), unknown, cov)
	}
	ev := unknown[0]
	if ev.Metadata["member"] != member || ev.Metadata["key"] != "threads" || ev.Metadata["count"] != 2 {
		t.Errorf("unknown event metadata = %v, want member %q, key %q, count 2", ev.Metadata, member, "threads")
	}
	if ev.Category != domain.CategoryOther || ev.ExternalID == "" || ev.Title == "" {
		t.Errorf("unknown event = %+v", ev)
	}
}
