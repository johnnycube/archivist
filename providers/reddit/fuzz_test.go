package reddit

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
	for _, ex := range New().Extractors() {
		f.Add(ex.(csvExtractor).kind, []byte(`{"id":"abc","permalink":"/r/x/comments/abc/","date":"2023-01-15 10:20:30 UTC","body":"hi"}`))
	}
	f.Add("post_vote", []byte(`{"id":"abc","direction":"none"}`))
	f.Add("comment", []byte(`not json`))
	f.Add("", []byte(``))
	f.Add("chat", []byte(`[1,2]`))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "reddit/x.csv"})
		checkEvents(t, p, evs, err)
	})
}

func FuzzExtract(f *testing.F) {
	f.Add("comments.csv", "id,permalink,date,ip,subreddit,gildings,link,parent,body,media\nk1,https://www.reddit.com/r/a/comments/1/x/k1/,2023-05-01 12:00:00 UTC,1.2.3.4,a,0,https://www.reddit.com/r/a/comments/1/x/,t3_1,\"multi\nline\",\n")
	f.Add("post_votes.csv", "id,permalink,direction\np1,/r/a/comments/p1/,up\np2,/r/a/comments/p2/,down\np3,/r/a/comments/p3/,none\n")
	f.Add("chat_history.csv", "message_id,created_at,updated_at,username,message,thread_parent_message_id,channel_url,subreddit,channel_name,conversation_type\nm1,2023-07-01 08:00:00 UTC,,bob,Hey,,sendbird_group_channel_1,,,direct\n")
	f.Add("messages.csv", "id,permalink,thread_id,date,ip,from,to,subject,body\nm1,/message/messages/m1/,th1,2023-06-01 09:00:00 UTC,1.2.3.4,alice,me,Hi,\"Hello\"\n")
	f.Add("posts.csv", "id\n")
	f.Add("posts.csv", "")
	f.Add("posts.csv", "\"unterminated\nquote,x")
	f.Add("statistics.csv", "statistic,value\nkarma,1\n")

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
