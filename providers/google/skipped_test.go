package google

import (
	"context"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

// Never drop data: an array the extractor does not stream — here because the
// member is not shaped the way this provider knows it — becomes a
// google.unknown event that says which array, and how many records, were passed
// over.
func TestUnexpectedArrayBecomesUnknownEvent(t *testing.T) {
	const member = "Takeout/YouTube and YouTube Music/history/watch-history.json"
	zip, size, err := archivetest.ZipReader(map[string]string{member: `{"items":[{"header":"YouTube","title":"Watched A","time":"2024-01-01T00:00:00Z"},{"header":"YouTube","title":"Watched B","time":"2024-01-02T00:00:00Z"},{"header":"YouTube","title":"Watched C","time":"2024-01-03T00:00:00Z"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	var unknown []domain.Event
	mapped := 0
	cov, err := bundle.NewImporter(New()).ImportParts(context.Background(), []bundle.Part{{ReaderAt: zip, Size: size}},
		func(ev domain.Event) error {
			if ev.Type == "google.unknown" {
				unknown = append(unknown, ev)
			} else {
				mapped++
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if mapped != 0 {
		t.Errorf("mapped events = %d, want 0", mapped)
	}
	if len(unknown) != 1 {
		t.Fatalf("unknown events = %d (%+v), want 1; coverage %+v", len(unknown), unknown, cov)
	}
	ev := unknown[0]
	if ev.Metadata["member"] != member || ev.Metadata["key"] != "items" || ev.Metadata["count"] != 3 {
		t.Errorf("unknown event metadata = %v, want member %q, key %q, count 3", ev.Metadata, member, "items")
	}
	if ev.Category != domain.CategoryOther || ev.ExternalID == "" || ev.Title == "" {
		t.Errorf("unknown event = %+v", ev)
	}
}
