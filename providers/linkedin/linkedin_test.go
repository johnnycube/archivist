package linkedin

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

func importTestdata(t *testing.T) ([]domain.Event, *domain.Coverage) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "linkedin.zip")
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
	return events, cov
}

func TestImportLinkedIn(t *testing.T) {
	events, cov := importTestdata(t)

	want := map[string]int{
		"linkedin.connection": 3, // the Notes preamble is skipped, not read as rows
		"linkedin.message":    4, // incl. one draft
		"linkedin.post":       2,
		"linkedin.comment":    1,
		"linkedin.reaction":   2,
		"linkedin.invitation": 2,
		"linkedin.search":     2,
		"linkedin.position":   2,
		"linkedin.education":  1,
		"linkedin.follow":     3, // Company Follows + Member_Follows + Hashtag_Follows
		"linkedin.save":       1,
		"linkedin.unknown":    2, // Skills.csv + Endorsement_Received_Info.csv captured generically
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	if cov.Mapped != 13 || cov.Generic != 2 {
		t.Errorf("mapped/generic members = %d/%d, want 13/2", cov.Mapped, cov.Generic)
	}

	declared := map[string]bool{}
	for _, d := range New().Descriptor().EventTypes {
		declared[d.Type] = true
	}
	ids := map[string]bool{}
	for _, ev := range events {
		if !declared[ev.Type] {
			t.Errorf("type %q is not declared in the descriptor", ev.Type)
		}
		if ev.ExternalID == "" || ids[ev.ExternalID] {
			t.Errorf("ExternalID %q empty or reused (%s)", ev.ExternalID, ev.Type)
		}
		ids[ev.ExternalID] = true
		if ev.OccurredAt.Location() != time.UTC {
			t.Errorf("%s OccurredAt is not UTC: %v", ev.ExternalID, ev.OccurredAt)
		}
		if ev.Type != "linkedin.unknown" && ev.OccurredAt.IsZero() {
			t.Errorf("%s %q has no date", ev.Type, ev.Title)
		}
	}

	// Connections.csv: header found behind the preamble; "31 Jan 2024".
	jane := byTitle(events, "Connected with Jane Doe")
	if jane == nil {
		t.Fatalf("connection behind the preamble not found")
	}
	if !jane.OccurredAt.Equal(date(2024, 1, 31, 0, 0, 0)) || len(jane.Targets) != 1 ||
		jane.Targets[0].Relation != domain.RelationFollowed ||
		jane.Targets[0].TargetURL != "https://www.linkedin.com/in/jane-doe-example" ||
		jane.ExternalID != "connection:https://www.linkedin.com/in/jane-doe-example" ||
		jane.Metadata["company"] != "Example GmbH" || jane.Metadata["email"] != "jane@example.com" {
		t.Errorf("connection not mapped: %+v", jane)
	}
	if max := byTitle(events, "Connected with Max Mustermann"); max == nil || max.Metadata["company"] != "Muster, Inc." {
		t.Errorf("quoted company not mapped: %+v", max)
	}
	if hidden := byTitle(events, "Connected with Hidden Member"); hidden == nil || !strings.HasPrefix(hidden.ExternalID, "connection:") || hidden.ExternalID == "connection:" {
		t.Errorf("connection without a URL needs a hashed id: %+v", hidden)
	}
	for _, ev := range events {
		if ev.Type == "linkedin.connection" && strings.Contains(ev.Title, "Notes") {
			t.Errorf("preamble leaked into a connection: %+v", ev)
		}
	}

	// messages.csv: upper-case headers, "2024-01-31 12:34:56 UTC", multi-line HTML body.
	var msg *domain.Event
	for i := range events {
		if events[i].Type == "linkedin.message" && strings.HasPrefix(events[i].Body, "<p>Hi Sam,</p>") {
			msg = &events[i]
		}
	}
	if msg == nil {
		t.Fatalf("multi-line message not found")
	}
	if msg.Actor != "Jane Doe" || msg.ThreadExternalID != "2-abc123==" || msg.Category != domain.CategoryMessage ||
		!msg.OccurredAt.Equal(date(2024, 1, 31, 12, 34, 56)) || msg.Metadata["to"] != "Sam Sample" ||
		!strings.Contains(msg.Body, "\n<p>are you at the meetup, too?</p>") {
		t.Errorf("message not mapped: %+v", msg)
	}
	if hiring := byTitle(events, "An exciting opportunity"); hiring == nil || hiring.Actor != "Rita Recruiter" ||
		hiring.ThreadExternalID != "2-def456==" || hiring.Metadata["attachments"] == nil || hiring.Metadata["conversation_title"] != "Hiring" {
		t.Errorf("message with a subject not mapped: %+v", hiring)
	}

	// Shares.csv: "2024-02-01 09:00:00" (no zone).
	post := byBody(events, "linkedin.post", "Shipped a new release today.")
	if post == nil || !post.OccurredAt.Equal(date(2024, 2, 1, 9, 0, 0)) || post.Actor != "self" ||
		post.Metadata["visibility"] != "MEMBER_NETWORK" || len(post.Targets) != 1 || post.Targets[0].TargetURL != "https://example.com/release" ||
		post.ExternalID != "post:https://www.linkedin.com/feed/update/urn%3Ali%3Ashare%3A7000000000000000001" {
		t.Errorf("post not mapped: %+v", post)
	}
	if c := byBody(events, "linkedin.comment", "Congratulations!"); c == nil || len(c.Targets) != 1 ||
		c.Targets[0].TargetExternalID != "urn:li:activity:7000000000000000100" {
		t.Errorf("comment not mapped: %+v", c)
	}

	// Reactions.csv
	praise := byTitle(events, "Reacted with PRAISE")
	if praise == nil || len(praise.Targets) != 1 || praise.Targets[0].Relation != domain.RelationLiked ||
		praise.Targets[0].TargetURL != "https://www.linkedin.com/feed/update/urn%3Ali%3Aactivity%3A7000000000000000201" ||
		praise.Targets[0].TargetExternalID != "urn:li:activity:7000000000000000201" ||
		praise.Metadata["reaction"] != "PRAISE" || !praise.OccurredAt.Equal(date(2024, 2, 5, 7, 16, 0)) {
		t.Errorf("reaction not mapped: %+v", praise)
	}

	// Invitations.csv: "1/31/24, 12:34 PM", and the narrow no-break space variant.
	out := byTitle(events, "Invited Jane Doe to connect")
	if out == nil || out.Actor != "self" || !out.OccurredAt.Equal(date(2024, 1, 31, 12, 34, 0)) ||
		out.Body != "Great talk yesterday!" || out.Targets[0].TargetURL != "https://www.linkedin.com/in/jane-doe-example" {
		t.Errorf("outgoing invitation not mapped: %+v", out)
	}
	in := byTitle(events, "Invitation to connect from Rita Recruiter")
	if in == nil || in.Actor != "Rita Recruiter" || !in.OccurredAt.Equal(date(2023, 11, 2, 7, 5, 0)) ||
		in.Targets[0].TargetURL != "https://www.linkedin.com/in/rita-recruiter-example" {
		t.Errorf("incoming invitation not mapped: %+v", in)
	}

	// SearchQueries.csv: "2024/02/06 18:00:01 UTC".
	if s := byBody(events, "linkedin.search", "golang jobs karlsruhe"); s == nil || !s.OccurredAt.Equal(date(2024, 2, 6, 18, 0, 1)) ||
		s.Category != domain.CategoryConsumption {
		t.Errorf("search not mapped: %+v", s)
	}

	// Positions.csv "Jan 2020", Education.csv "2014".
	if pos := byTitle(events, "Software Engineer at Example GmbH"); pos == nil || !pos.OccurredAt.Equal(date(2020, 1, 1, 0, 0, 0)) ||
		pos.Body != "Backend services in Go." || pos.Metadata["location"] != "Karlsruhe, Germany" || pos.Category != domain.CategoryProfile {
		t.Errorf("position not mapped: %+v", pos)
	}
	if old := byTitle(events, "Working Student at Old Corp"); old == nil || old.Metadata["finished_on"] != "Dec 2019" {
		t.Errorf("finished position not mapped: %+v", old)
	}
	if edu := byTitle(events, "B.Sc. Computer Science at Example University"); edu == nil || !edu.OccurredAt.Equal(date(2014, 1, 1, 0, 0, 0)) ||
		edu.Metadata["end_date"] != "2017" || edu.Metadata["activities"] != "Chess club" {
		t.Errorf("education not mapped: %+v", edu)
	}

	// Company Follows.csv: "Wed Jan 31 12:34:56 UTC 2024".
	if f := byTitle(events, "Followed Example GmbH"); f == nil || !f.OccurredAt.Equal(date(2024, 1, 31, 12, 34, 56)) ||
		f.Targets[0].Relation != domain.RelationFollowed || f.Metadata["kind"] != "company" {
		t.Errorf("company follow not mapped: %+v", f)
	}
	// Member_Follows.csv / Hashtag_Follows.csv / Saved_Items.csv: headers are not
	// publicly documented, so the whole row is kept alongside the best guess.
	if f := byTitle(events, "Followed Jane Doe"); f == nil || f.Metadata["kind"] != "member" || f.Metadata["row"] == nil || f.Targets[0].TargetURL != "https://www.linkedin.com/in/jane-doe-example" {
		t.Errorf("member follow not mapped: %+v", f)
	}
	if f := byTitle(events, "Followed #golang"); f == nil || f.Metadata["kind"] != "hashtag" {
		t.Errorf("hashtag follow not mapped: %+v", f)
	}
	if s := byTitle(events, "Saved urn:li:activity:7000000000000000300"); s == nil || s.Category != domain.CategorySave || s.Targets[0].Relation != domain.RelationSaved ||
		s.Metadata["row"] == nil || s.OccurredAt.IsZero() {
		t.Errorf("saved item not mapped: %+v", s)
	}
	// messages.csv "IS MESSAGE DRAFT" column.
	if d := byBody(events, "linkedin.message", "Unsent thoughts"); d == nil || d.Metadata["draft"] != true || !strings.HasPrefix(d.Title, "Draft: ") {
		t.Errorf("draft message not flagged: %+v", d)
	}
}

func TestMatchVariants(t *testing.T) {
	cases := map[string]bool{
		"Connections.csv": true,
		"Basic_LinkedInDataExport_01-02-2026/Connections.csv": true,
		"Shares.csv":                 true,
		"Shares_506219023.csv":       true,
		"Comments_506219023.csv":     true,
		"Company Follows.csv":        true,
		"Company_Follows.csv":        true,
		"Member_Follows.csv":         true,
		"Hashtag_Follows.csv":        true,
		"Saved_Items.csv":            true,
		"messages.csv":               true,
		"Shares_abc.csv":             false,
		"SharesX.csv":                false,
		"Skills.csv":                 false,
		"__MACOSX/._Connections.csv": false,
		"Rich Media.csv":             false,
	}
	for member, want := range cases {
		got := false
		for _, ex := range New().Extractors() {
			got = got || ex.Match(member)
		}
		if got != want {
			t.Errorf("Match(%q) = %v, want %v", member, got, want)
		}
	}
}

// Re-importing the same archive must produce the same ExternalIDs.
func TestExternalIDsStable(t *testing.T) {
	a, _ := importTestdata(t)
	b, _ := importTestdata(t)
	if len(a) != len(b) {
		t.Fatalf("event counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ExternalID != b[i].ExternalID {
			t.Errorf("event %d: %q vs %q", i, a[i].ExternalID, b[i].ExternalID)
		}
	}
}

func TestSkipPreamble(t *testing.T) {
	const csv = "First Name,Last Name\nJane,Doe\n"
	long := strings.Repeat("x", 3*preambleBuf)
	cases := []struct {
		name, in, header, want string
		missing                bool
	}{
		{name: "notes preamble", in: "Notes:\n\"Some explanation, with commas.\"\n\n" + csv, header: "first name", want: csv},
		{name: "crlf and bom", in: "\ufeffNotes:\r\n\"Explanation\"\r\n\r\n" + csv, header: "first name", want: csv},
		{name: "no preamble", in: csv, header: "first name", want: csv},
		{name: "header case", in: "Notes:\n\nFIRST NAME,LAST NAME\nJane,Doe\n", header: "first name", want: "FIRST NAME,LAST NAME\nJane,Doe\n"},
		{name: "over-long preamble line", in: "Notes:\n" + long + " first name\n\n" + csv, header: "first name", want: csv},
		{name: "header without newline", in: "Notes:\n\nFirst Name,Last Name", header: "first name", want: "First Name,Last Name"},
		{name: "no hint, no preamble", in: "Date,Link\n2024,x\n", want: "Date,Link\n2024,x\n"},
		{name: "no hint, notes column", in: "Notes,Date\nhello,2024\n", want: "Notes,Date\nhello,2024\n"},
		{name: "no hint, notes preamble", in: "Notes:\n\"Explanation\"\n\nDate,Link\n2024,x\n", want: "Date,Link\n2024,x\n"},
		{name: "header never found", in: "Notes:\n\"Explanation\"\n\nA,B\n1,2\n", header: "first name", missing: true},
		{name: "empty", in: "", header: "first name", missing: true},
	}
	for _, c := range cases {
		body, head, err := skipPreamble(strings.NewReader(c.in), c.header)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.missing {
			if body != nil || len(head) > preamblePreview || !strings.HasPrefix(c.in, string(head)) {
				t.Errorf("%s: want no body and a bounded preview, got body=%v head=%q", c.name, body != nil, head)
			}
			continue
		}
		if body == nil {
			t.Errorf("%s: header not found", c.name)
			continue
		}
		got, _ := io.ReadAll(body)
		if string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A Connections.csv without a recognizable header is kept as one unknown
// record instead of being read with the wrong columns or dropped.
func TestHeaderlessMemberIsKept(t *testing.T) {
	var got []provider.RawRecord
	ex := New().Extractors()[0]
	err := ex.Extract("Connections.csv", strings.NewReader("Notes:\nsomething else entirely\n"), func(r provider.RawRecord) error {
		got = append(got, r)
		return nil
	})
	if err != nil || len(got) != 1 || got[0].Kind != "unknown" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	evs, err := New().Normalize(context.Background(), got[0])
	if err != nil || len(evs) != 1 || evs[0].Type != "linkedin.unknown" || evs[0].Metadata["reason"] == nil {
		t.Errorf("got %+v, err %v", evs, err)
	}
}

func TestParseTime(t *testing.T) {
	cases := map[string]time.Time{
		"2024-01-31 12:34:56 UTC":      date(2024, 1, 31, 12, 34, 56),
		"2024/01/31 12:34:56 UTC":      date(2024, 1, 31, 12, 34, 56),
		"2024-01-31 12:34:56":          date(2024, 1, 31, 12, 34, 56),
		"31 Jan 2024":                  date(2024, 1, 31, 0, 0, 0),
		"05 Feb 2023":                  date(2023, 2, 5, 0, 0, 0),
		"1/31/24, 12:34 PM":            date(2024, 1, 31, 12, 34, 0),
		"12/1/23, 9:05\u202fAM":        date(2023, 12, 1, 9, 5, 0),
		"Jan 2020":                     date(2020, 1, 1, 0, 0, 0),
		"2014":                         date(2014, 1, 1, 0, 0, 0),
		"2021-03":                      date(2021, 3, 1, 0, 0, 0),
		"Present":                      {},
		"Wed Jan 31 12:34:56 UTC 2024": date(2024, 1, 31, 12, 34, 56),
		"Fri Feb  2 08:00:00 UTC 2024": date(2024, 2, 2, 8, 0, 0),
		"2024-01-31T12:34:56+01:00":    date(2024, 1, 31, 11, 34, 56),
		"":                             {},
		"not a date":                   {},
	}
	for in, want := range cases {
		got := parseTime(in)
		if !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("parseTime(%q) = %v, want %v", in, got, want)
		}
	}
}

func date(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

func byTitle(evs []domain.Event, title string) *domain.Event {
	for i := range evs {
		if evs[i].Title == title {
			return &evs[i]
		}
	}
	return nil
}

func byBody(evs []domain.Event, typ, body string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ && evs[i].Body == body {
			return &evs[i]
		}
	}
	return nil
}
