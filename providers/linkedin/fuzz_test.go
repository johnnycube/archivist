package linkedin

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
		f.Add(ex.(csvExtractor).kind, []byte(`{"date":"2024-01-31 12:34:56 UTC","link":"https://www.linkedin.com/feed/update/urn%3Ali%3Aactivity%3A1","from":"A","to":"B","content":"hi"}`))
	}
	f.Add("message", []byte(`not json`))
	f.Add("unknown", []byte(`{"member":"x","reason":"r"}`))
	f.Add("", []byte(``))
	f.Add("post", []byte(`[1,2]`))

	p := New()
	f.Fuzz(func(t *testing.T, kind string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Payload: payload, Member: "x.csv"})
		checkEvents(t, p, evs, err)
	})
}

func FuzzExtract(f *testing.F) {
	f.Add("Connections.csv", "Notes:\n\"When exporting your connection data, you may notice that some of the email addresses are missing.\"\n\nFirst Name,Last Name,URL,Email Address,Company,Position,Connected On\nJane,Doe,https://www.linkedin.com/in/jane,,Ex,Eng,31 Jan 2024\n")
	f.Add("messages.csv", "CONVERSATION ID,CONVERSATION TITLE,FROM,SENDER PROFILE URL,TO,RECIPIENT PROFILE URLS,DATE,SUBJECT,CONTENT,FOLDER,ATTACHMENTS,IS MESSAGE DRAFT\n2-a==,,Jane,https://www.linkedin.com/in/jane,Sam,https://www.linkedin.com/in/sam,2024-01-31 12:34:56 UTC,,\"<p>Hi</p>\n<p>there</p>\",INBOX,,No\n")
	f.Add("Invitations.csv", "From,To,Sent At,Message,Direction,inviterProfileUrl,inviteeProfileUrl\nA,B,\"1/31/24, 12:34 PM\",,OUTGOING,https://x/a,https://x/b\n")
	f.Add("Shares.csv", "Date,ShareLink,ShareCommentary,SharedUrl,MediaUrl,Visibility\n2024-02-01 09:00:00,https://www.linkedin.com/feed/update/urn%3Ali%3Ashare%3A1,hello,,,MEMBER_NETWORK\n")
	f.Add("Reactions.csv", "Date,Type,Link\n2024-02-05 07:15:00 UTC,LIKE,https://www.linkedin.com/feed/update/urn%3Ali%3Aactivity%3A2\n")
	f.Add("Positions.csv", "Company Name,Title,Description,Location,Started On,Finished On\nEx,Eng,,,Jan 2020,\n")
	f.Add("Education.csv", "School Name,Start Date,End Date,Notes,Degree Name,Activities\nU,2014,2017,,BSc,\n")
	f.Add("Company Follows.csv", "Organization,Followed On\nEx,Wed Jan 31 12:34:56 UTC 2024\n")
	f.Add("SearchQueries.csv", "Time,Search Query\n2024/02/06 18:00:01 UTC,go\n")
	f.Add("Connections.csv", "Notes:\nsomething else\n")
	f.Add("Comments.csv", "")
	f.Add("Comments.csv", "\"unterminated\nquote,x")

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
