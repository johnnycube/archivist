package chatgpt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// recorder wraps the provider so the importer hands us every RawRecord the
// extractors produce from the fixtures: those are the fuzz seeds.
type recorder struct {
	*Provider
	raws []provider.RawRecord
}

func (r *recorder) Normalize(ctx context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	r.raws = append(r.raws, raw)
	return r.Provider.Normalize(ctx, raw)
}

func seedRecords(f *testing.F) []provider.RawRecord {
	f.Helper()
	zipPath := filepath.Join(f.TempDir(), "seed.zip")
	if err := archivetest.ZipDir("testdata", zipPath); err != nil {
		f.Fatal(err)
	}
	rec := &recorder{Provider: New()}
	if _, err := bundle.NewImporter(rec).ImportZip(context.Background(), zipPath, func(domain.Event) error { return nil }); err != nil {
		f.Fatal(err)
	}
	return rec.raws
}

// checkEvents enforces the never-drop contract on Normalize's output: a record
// of any shape yields at least one event, every event has a stable id and a
// type the descriptor declares, and nothing panics.
func checkEvents(t *testing.T, p *Provider, evs []domain.Event, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Normalize returned an error (import would abort): %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("Normalize dropped the record")
	}
	declared := map[string]domain.Category{}
	for _, d := range p.Descriptor().EventTypes {
		declared[d.Type] = d.Category
	}
	for _, ev := range evs {
		if ev.ExternalID == "" || ev.SourceID != sourceID {
			t.Errorf("event without id/source: %+v", ev)
		}
		if cat, ok := declared[ev.Type]; !ok || cat != ev.Category {
			t.Errorf("undeclared type/category %q/%q", ev.Type, ev.Category)
		}
	}
}

// FuzzNormalize feeds arbitrary payloads under every record kind to Normalize.
func FuzzNormalize(f *testing.F) {
	for _, raw := range seedRecords(f) {
		f.Add(raw.Kind, raw.Payload)
	}
	f.Add("message", []byte(`{"conv_id":"c","msg_id":"m","role":"user","text":"hi","create_time":1.5}`))
	f.Add("message", []byte(`not json`))
	f.Add(bundle.SkippedArrayKind, []byte(`{"member":"conversations.json","key":"x","count":2}`))
	f.Add(bundle.SkippedArrayKind, []byte(`[]`))
	f.Add("", []byte(``))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "conversations.json"})
		checkEvents(t, p, evs, err)
	})
}

// FuzzExtract runs arbitrary member bytes through the extractor and Normalize.
// A member that is not JSON at all may fail as a whole; a member that parses
// must never make Normalize fail or panic.
func FuzzExtract(f *testing.F) {
	f.Add(`[{"title":"t","conversation_id":"c","mapping":{"n":{"id":"n","parent":null,"message":{"id":"m","author":{"role":"user"},"create_time":1.0,"content":{"content_type":"text","parts":["hi"]}}}}}]`)
	f.Add(`[{"title":"t","mapping":{"n":{"message":{"id":"m","author":{"role":"user"},"create_time":null,"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"file-service://file-1"},"text"]}}}}}]`)
	f.Add(`[{"mapping":{"n":{"message":{"author":{"role":"tool","name":"dalle.text2im"},"content":{"content_type":"code","language":"python","text":"print(1)"}}}}}]`)
	f.Add(`{"conversations":[{"title":"w","mapping":{}}]}`)
	f.Add(`[1, "two", null, {"mapping": 5}]`)
	f.Add(``)
	f.Add(`[`)

	p := New()
	f.Fuzz(func(t *testing.T, data string) {
		for _, ex := range p.Extractors() {
			if !ex.Match("conversations.json") {
				continue
			}
			_ = ex.Extract("conversations.json", strings.NewReader(data), func(raw provider.RawRecord) error {
				evs, err := p.Normalize(context.Background(), raw)
				checkEvents(t, p, evs, err)
				return nil
			})
		}
	})
}
