package claude

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

// checkEvents enforces the never-drop contract: a record of any shape yields at
// least one event with a stable id and a declared type, and nothing panics.
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

func FuzzNormalize(f *testing.F) {
	for _, raw := range seedRecords(f) {
		f.Add(raw.Kind, raw.Payload)
	}
	f.Add("message", []byte(`{"conv_id":"c","msg_id":"m","role":"human","text":"hi","created_at":"2024-05-01T10:00:00Z"}`))
	f.Add("message", []byte(`not json`))
	f.Add(bundle.SkippedArrayKind, []byte(`[]`))
	f.Add("", []byte(``))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "conversations.json"})
		checkEvents(t, p, evs, err)
	})
}

func FuzzExtract(f *testing.F) {
	f.Add("conversations.json", `[{"uuid":"c","name":"n","created_at":"2024-05-01T10:00:00.123456Z","chat_messages":[{"uuid":"m","sender":"human","text":"hi","created_at":"2024-05-01T10:00:00.123456+00:00","content":[{"type":"text","text":"hi"}]}]}]`)
	f.Add("conversations.json", `[{"uuid":"c","chat_messages":[{"uuid":"m","sender":"assistant","text":"","content":[{"type":"tool_use","name":"x","input":{}},{"type":"text","text":"done"}],"attachments":[{"file_name":"a.pdf","extracted_content":"body"}]}]}]`)
	f.Add("conversations.json", `[1,"two",null,{"chat_messages":5}]`)
	f.Add("users.json", `[{"uuid":"u","full_name":"Jane","email_address":"j@example.com"}]`)
	f.Add("projects.json", `[{"uuid":"p","name":"Proj","created_at":"2024-05-01T10:00:00Z","docs":[{"uuid":"d","filename":"f.md","content":"x"}]}]`)
	f.Add("conversations.json", ``)
	f.Add("conversations.json", `[`)

	p := New()
	f.Fuzz(func(t *testing.T, member, data string) {
		for _, ex := range p.Extractors() {
			if !ex.Match(member) {
				continue
			}
			_ = ex.Extract(member, strings.NewReader(data), func(raw provider.RawRecord) error {
				raw.Member = member
				evs, err := p.Normalize(context.Background(), raw)
				checkEvents(t, p, evs, err)
				return nil
			})
		}
	})
}
