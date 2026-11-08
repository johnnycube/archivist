package tiktok

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	rec := &recorder{Provider: New()}
	for _, dir := range []string{"export", "aliases"} {
		zipPath := filepath.Join(f.TempDir(), dir+".zip")
		if err := archivetest.ZipDir(filepath.Join("testdata", dir), zipPath); err != nil {
			f.Fatal(err)
		}
		if _, err := bundle.NewImporter(rec).ImportZip(context.Background(), zipPath, func(domain.Event) error { return nil }); err != nil {
			f.Fatal(err)
		}
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
	for _, kind := range []string{"watch", "like", "save", "post", "comment", "search", "follow", "follower", "share", "login", "message", "unknown", ""} {
		f.Add(kind, []byte(`not json`))
		f.Add(kind, []byte(`[1,2]`))
		f.Add(kind, []byte(`{"thread":"x","message":5,"path":"notalist","value":1}`))
	}
	f.Add("post", []byte(`{"Date":"N/A","Link":"https://www.tiktokv.com/share/video/1/","Likes":{"a":1}}`))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "user_data_tiktok.json"})
		checkEvents(t, p, evs, err)
	})
}

func FuzzExtract(f *testing.F) {
	f.Add("user_data_tiktok.json", `{"Your Activity":{"Watch History":{"VideoList":[{"Date":"2025-03-01 08:00:00","Link":"https://www.tiktokv.com/share/video/1/"}]}}}`)
	f.Add("user_data_tiktok.json", `{"Direct Message":{"Direct Messages":{"ChatHistory":{"Chat History with jane:":[{"Date":"2024-02-01 20:06:00","From":"jane","Content":"hi"}]}}}}`)
	f.Add("user_data_tiktok.json", `{"Activity":{"Like List":{"ItemFavoriteList":[1,"two",null,[]]}},"Profile":{"Profile Information":{"ProfileMap":{"userName":"x","likesReceived":42}}}}`)
	f.Add("user_data_tiktok.json", `[{"Date":"2024-01-01 00:00:00"}]`)
	f.Add("user_data_tiktok.json", ``)
	f.Add("user_data_tiktok.json", `{`)
	f.Add("Your Activity/Watch History.txt", "Date: 2025-03-01 08:00:00\nLink: https://www.tiktokv.com/share/video/1/\n\nDate: 2025-03-01 08:01:00\nLink: https://www.tiktokv.com/share/video/2/\n\n")
	f.Add("Direct Messages/Direct Messages.txt", ">>> Chat History with jane:\nDate: 2024-02-01 20:06:00\nFrom: jane\nContent: hi\n\n")

	p := New()
	f.Fuzz(func(t *testing.T, member, data string) {
		start := time.Now()
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
		// A member of a few KB must never take seconds: that is a hang.
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("Extract took %v on %d bytes", d, len(data))
		}
	})
}
