package twitter

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
	kinds := []string{"dm", "dm_event", "unknown"}
	for _, ex := range New().Extractors() {
		if js, ok := ex.(jsExtractor); ok {
			kinds = append(kinds, js.kind)
		}
	}
	for _, kind := range kinds {
		f.Add(kind, []byte(`not json`))
		f.Add(kind, []byte(`[1,2]`))
		f.Add(kind, []byte(`{"id_str":5,"accountId":[],"messages":"x","event":"messageCreate","body":{"id":1}}`))
	}
	f.Add("", []byte(``))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "data/x.js"})
		checkEvents(t, p, evs, err)
	})
}

func FuzzExtract(f *testing.F) {
	f.Add("data/tweets.js", `window.YTD.tweets.part0 = [ { "tweet": { "id_str": "1", "full_text": "RT @a: hi", "created_at": "Wed Oct 10 20:19:24 +0000 2018", "in_reply_to_status_id_str": "0", "extended_entities": { "media": [ { "id_str": "9", "type": "photo", "media_url_https": "https://pbs.twimg.com/media/AbC.jpg" } ] } } } ]`)
	f.Add("data/direct-messages.js", `window.YTD.direct_messages.part0 = [ { "dmConversation": { "conversationId": "1-2", "messages": [ { "messageCreate": { "id": "5", "senderId": "1", "recipientId": "2", "createdAt": "2020-01-01T12:00:00.000Z", "text": "hi", "mediaUrls": [], "urls": [], "reactions": [] } } ] } } ]`)
	f.Add("data/direct-messages-group.js", `window.YTD.direct_messages_group.part0 = [ { "dmConversation": { "conversationId": "3", "messages": [ { "participantsJoin": { "initiatingUserId": "1", "userIds": ["2"], "createdAt": "2020-01-01T12:00:00.000Z" } }, { "joinConversation": { "initiatingUserId": "1", "participantsSnapshot": ["1","2"], "createdAt": "2020-01-01T12:00:00.000Z" } } ] } } ]`)
	f.Add("data/account.js", `window.YTD.account.part0 = [ { "account": { "accountId": "1", "username": "u", "createdAt": "2010-01-01T00:00:00.000Z" } } ]`)
	f.Add("data/tweets.js", `window.YTD.tweets.part0 = [ 1, "two", null, { "tweet": 5 } ]`)
	f.Add("data/tweets.js", `window.YTD.tweets.part0 = [`)
	f.Add("data/tweets.js", ``)
	f.Add("data/like.js", `no array here`)

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
