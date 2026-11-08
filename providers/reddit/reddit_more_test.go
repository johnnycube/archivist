package reddit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

// A realistic, messy export: quoted multiline bodies, alternate column names
// (link_id/parent_id), an unknown vote direction, and several file types.
var messyExport = map[string]string{
	"reddit/comments.csv": "id,permalink,date,subreddit,link_id,parent_id,body\n" +
		"k1,/r/golang/comments/1/x/k1/,2023-05-01 12:00:00 UTC,golang,post:1,post:1,\"Multi, line\nbody with \"\"quotes\"\"\"\n",
	"reddit/post_votes.csv": "id,permalink,direction\n" +
		"p1,/r/a/comments/p1/,up\n" +
		"p2,/r/a/comments/p2/,down\n" +
		"p3,/r/a/comments/p3/,none\n",
	"reddit/friends.csv":    "username,note\nalice,best\n",
	"reddit/poll_votes.csv": "post_id,user_selection\npoll1,Option B\n",
	"reddit/messages.csv": "id,permalink,date,from,to,subject,body,thread_id\n" +
		"m1,/message/messages/m1/,2023-06-01 09:00:00 UTC,alice,me,Hello,\"Hi there\",th1\n",
	"reddit/chat_history.csv": "message_id,created_at,username,message,channel_url\n" +
		"ch1,2023-07-01 08:00:00 UTC,bob,Hey there,sendbird_group_channel_123\n",
	"reddit/hidden_posts.csv": "id,permalink\nh1,/r/x/comments/h1/\n",
	// a file with no mapper — must be captured generically, not dropped
	"reddit/ip_logs.csv": "date,ip\n2023-01-01,1.2.3.4\n",
}

func importMessy(t *testing.T) ([]domain.Event, *domain.Coverage) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := archivetest.WriteFiles(src, messyExport); err != nil {
		t.Fatal(err)
	}
	zip := filepath.Join(dir, "e.zip")
	if err := archivetest.ZipDir(src, zip); err != nil {
		t.Fatal(err)
	}
	var evs []domain.Event
	cov, err := bundle.NewImporter(New()).ImportZip(context.Background(), zip, func(e domain.Event) error {
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return evs, cov
}

func TestRedditMessyExport(t *testing.T) {
	evs, cov := importMessy(t)

	want := map[string]int{
		"reddit.comment":   1,
		"reddit.vote.up":   1,
		"reddit.vote.down": 1,
		"reddit.vote":      1, // "none" → undirected vote, still respected
		"reddit.friend":    1,
		"reddit.poll_vote": 1,
		"reddit.message":   1,
		"reddit.chat":      1,
		"reddit.hide":      1,
		"reddit.unknown":   1, // ip_logs.csv captured generically
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}

	// Quoted multiline body survived CSV parsing, and alt column names worked.
	c := byType(evs, "reddit.comment")
	if c == nil || !strings.Contains(c.Body, "Multi, line") || !strings.Contains(c.Body, "quotes") {
		t.Errorf("comment body not parsed from quoted multiline field: %q", bodyOf(c))
	}
	if c != nil && c.ParentExternalID != "post:1" {
		t.Errorf("comment parent = %q, want post:1 (from parent_id)", c.ParentExternalID)
	}

	// Private message: sender and thread mapped.
	m := byType(evs, "reddit.message")
	if m == nil || m.Actor != "alice" || m.ThreadExternalID != "th1" || m.Category != domain.CategoryMessage {
		t.Errorf("message not mapped correctly: %+v", m)
	}

	// Chat message body + actor.
	ch := byType(evs, "reddit.chat")
	if ch == nil || ch.Body != "Hey there" || ch.Actor != "bob" {
		t.Errorf("chat not mapped correctly: %+v", ch)
	}

	// Friend connection target.
	f := byType(evs, "reddit.friend")
	if f == nil || len(f.Targets) != 1 || f.Targets[0].TargetExternalID != "u/alice" {
		t.Errorf("friend target wrong: %+v", f)
	}
}

func byType(evs []domain.Event, typ string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ {
			return &evs[i]
		}
	}
	return nil
}

func bodyOf(e *domain.Event) string {
	if e == nil {
		return "<nil>"
	}
	return e.Body
}

func TestRefID(t *testing.T) {
	cases := map[[2]string]string{
		{"https://old.reddit.com/r/golang/comments/aaa/x/", "post"}: "post:aaa",
		{"https://www.reddit.com/r/golang/comments/aaa/x/c1/", ""}:  "comment:c1",
		{"/r/golang/comments/aaa/x/c1/?context=3", ""}:              "comment:c1",
		{"t3_aaa", ""}:                         "post:aaa",
		{"t1_c9", ""}:                          "comment:c9",
		{"aaa", "post"}:                        "post:aaa",
		{"post:1", "post"}:                     "post:1",
		{"aaa", ""}:                            "aaa",
		{"", "post"}:                           "",
		{"https://old.reddit.com/user/x/", ""}: "https://old.reddit.com/user/x/",
		{"https://old.reddit.com/r/golang/comments/", "post"}: "https://old.reddit.com/r/golang/comments/",
	}
	for in, want := range cases {
		if got := refID(in[0], in[1]); got != want {
			t.Errorf("refID(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
