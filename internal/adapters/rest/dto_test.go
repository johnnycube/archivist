package rest

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

var update = flag.Bool("update", false, "rewrite testdata/dto/*.golden.json from the current DTOs")

// The wire format is a contract with the SPA. Every response type is rendered
// from a fully populated value — so no field hides behind omitempty — and from
// its zero value, and compared byte for byte with a golden file. A failure here
// means the API changed: update the frontend (and the golden file, with
// `go test ./internal/adapters/rest -run TestWireFormat -update`) deliberately.
func TestWireFormat(t *testing.T) {
	at := time.Date(2024, 3, 9, 14, 30, 0, 123_000_000, time.UTC)
	job := domain.ImportJob{
		ID: "11111111-1111-4111-8111-111111111111", UserID: "SECRET-USER-ID",
		Source: "whatsapp", Label: "default",
		ZipPaths:  []string{"SECRET-USER-ID/STAGINGREF.zip"},
		PartNames: []string{"WhatsApp Chat - Book Club.zip"}, Parts: 1,
		ArchiveBytes: 2048, Status: domain.JobError, Attempts: 2, Saved: 7,
		BytesDone: 100, BytesTotal: 200,
		Coverage: &domain.Coverage{
			Members: 5, Mapped: 3, Generic: 1, Skipped: 1, Events: 9, Duplicates: 2,
			DedupeTruncated: true, New: 6, Reimported: 1, MediaStored: 4, Truncated: 2, Superseded: 3, Bytes: 4096,
			PerType: map[string]int{"whatsapp.message": 8, "whatsapp.unknown": 1}, GenericMembers: []string{"notes.bin"},
		},
		Error: "boom", CancelRequested: true, CreatedAt: at, UpdatedAt: at.Add(time.Minute),
	}
	event := domain.StoredEvent{
		ID: "22222222-2222-4222-8222-222222222222", UserID: "SECRET-USER-ID", AccountID: "SECRET-ACCOUNT-ID",
		ImportJobID: job.ID, IngestedAt: at.Add(time.Hour),
		Event: domain.Event{
			SourceID: "whatsapp", ExternalID: "msg-1", Type: "whatsapp.message", Category: domain.CategoryMessage,
			OccurredAt: at, Actor: "Anna", Title: "Book Club", Body: "See <you> & \"them\" there",
			ThreadExternalID: "chat:Book Club", ParentExternalID: "msg-0",
			Targets:  []domain.Target{{Relation: domain.RelationRepliedTo, TargetSource: "whatsapp", TargetExternalID: "msg-0", TargetURL: "https://example.org/0"}},
			Media:    []domain.Media{{BlobKey: "IMG-0001.jpg", MIME: "image/jpeg", URL: "https://example.org/i.jpg"}, {BlobKey: "notes.pdf"}},
			Metadata: map[string]any{"edited": true, "reactions": []any{"+1"}, "n": 3},
		},
	}
	page := app.Page{
		Events: []domain.StoredEvent{event, {
			ID: "33333333-3333-4333-8333-333333333333", UserID: "SECRET-USER-ID", AccountID: "SECRET-ACCOUNT-ID",
			Event: domain.Event{SourceID: "reddit", ExternalID: "u1", Type: "reddit.unknown", Category: domain.CategoryOther},
		}},
		NextCursor: "Q1VSU09S",
		Facets: app.Facets{
			Sources:    []app.FacetCount{{Value: "whatsapp", Count: 1}, {Value: "reddit", Count: 1}},
			Categories: []app.FacetCount{{Value: "message", Count: 1}},
			Types:      []app.FacetCount{},
		},
		Total: 2,
	}
	sources := []domain.Descriptor{{
		ID: "whatsapp", DisplayName: "WhatsApp", IngestionModes: []domain.Mode{domain.ModeBundle}, Maturity: domain.MaturityVerified,
		EventTypes: []domain.TypeDef{{Type: "whatsapp.message", Category: domain.CategoryMessage, HasBody: true, NeedsTarget: true}},
	}, {ID: "bare"}}
	cluster := app.Cluster{
		SourceID: "spotify", Type: "spotify.play", Category: domain.CategoryConsumption, Count: 37,
		FirstAt: at, LastAt: at.Add(-time.Hour),
		FirstID: "55555555-5555-4555-8555-555555555555", LastID: "66666666-6666-4666-8666-666666666666",
		SampleTitles: []string{"Track <A>", "Track \"B\"", "Track C"},
	}
	collapsedPage := app.CollapsedPage{
		Items:      []app.CollapsedItem{{Cluster: &cluster}, {Event: &event}, {Event: &page.Events[1]}},
		NextCursor: "Q1VSU09S", Facets: page.Facets, Total: 39,
	}

	for name, v := range map[string]any{
		"user":                toUserDTO(domain.User{ID: "44444444-4444-4444-8444-444444444444", Email: "me@x.com", PasswordHash: "SECRET-HASH", EmailVerified: true}, []string{"gitea"}),
		"oidc_providers":      toOIDCProviderDTOs([]app.OIDCProviderInfo{{ID: "gitea", Name: "Company Gitea"}}),
		"job":                 toJobDTO(job),
		"job_zero":            toJobDTO(domain.ImportJob{}),
		"jobs_none":           toJobDTOs(nil),
		"event":               toEventDTO(event),
		"event_zero":          toEventDTO(domain.StoredEvent{}),
		"page":                toPageDTO(page),
		"page_zero":           toPageDTO(app.Page{}),
		"cluster":             toClusterDTO(cluster),
		"cluster_zero":        toClusterDTO(app.Cluster{}),
		"collapsed_page":      toCollapsedPageDTO(collapsedPage),
		"collapsed_page_zero": toCollapsedPageDTO(app.CollapsedPage{}),
		"histogram":           toBucketDTOs([]app.HistogramBucket{{Day: "2024-03-09", Counts: map[string]int{"reddit": 2, "whatsapp": 5}}}),
		"histogram_none":      toBucketDTOs(nil),
		"histogram_category":  toCategoryBucketDTOs([]app.HistogramBucket{{Day: "2024-03-09", Counts: map[string]int{"consumption": 40, "message": 5}}}),
		"sources":             toSourceDTOs(sources),
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSON(rec, 200, v) // the encoder every handler uses
			got := rec.Body.Bytes()
			if bytes.Contains(got, []byte("SECRET")) || bytes.Contains(got, []byte("STAGINGREF")) {
				t.Errorf("server-side data on the wire:\n%s", got)
			}
			for _, key := range []string{`"user_id"`, `"account_id"`, `"zip_paths"`, `"password_hash"`} {
				if bytes.Contains(got, []byte(key)) {
					t.Errorf("tenancy field %s on the wire:\n%s", key, got)
				}
			}
			golden := filepath.Join("testdata", "dto", name+".golden.json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("wire format of %s changed.\n--- got\n%s--- want (%s)\n%s", name, got, golden, want)
			}
		})
	}
}

// A cluster item names exactly two of its members (first_id, last_id) and
// nothing about the owner; every item of a collapsed page says what it is.
func TestCollapsedItemsExposeOnlyTheirOwnFields(t *testing.T) {
	event := domain.StoredEvent{
		ID: "22222222-2222-4222-8222-222222222222", UserID: "SECRET-USER-ID", AccountID: "SECRET-ACCOUNT-ID",
		Event: domain.Event{SourceID: "spotify", ExternalID: "p1", Type: "spotify.play", Category: domain.CategoryConsumption, Title: "Track"},
	}
	cluster := app.Cluster{
		SourceID: "spotify", Type: "spotify.play", Category: domain.CategoryConsumption, Count: 3,
		FirstID: "55555555-5555-4555-8555-555555555555", LastID: "66666666-6666-4666-8666-666666666666",
	}
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, toCollapsedPageDTO(app.CollapsedPage{Items: []app.CollapsedItem{{Cluster: &cluster}, {Event: &event}}}))

	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatalf("decode collapsed page: %v\n%s", err, rec.Body.String())
	}
	allowed := map[string]bool{
		"kind": true, "source_id": true, "type": true, "category": true, "count": true,
		"first_at": true, "last_at": true, "sample_titles": true, "first_id": true, "last_id": true,
	}
	c := page.Items[0]
	if string(c["kind"]) != `"cluster"` {
		t.Errorf("cluster kind = %s", c["kind"])
	}
	for k := range c {
		if !allowed[k] {
			t.Errorf("cluster item carries %q", k)
		}
	}
	for k := range allowed {
		if _, ok := c[k]; !ok {
			t.Errorf("cluster item lacks %q", k)
		}
	}
	e := page.Items[1]
	if string(e["kind"]) != `"event"` || string(e["id"]) != `"`+event.ID+`"` {
		t.Errorf("event item = %v", e)
	}
	for _, k := range []string{"user_id", "account_id"} {
		if _, ok := e[k]; ok {
			t.Errorf("event item carries %q", k)
		}
	}
}
