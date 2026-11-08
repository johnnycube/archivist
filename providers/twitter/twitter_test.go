package twitter

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
)

func importTestdata(t *testing.T) ([]domain.Event, *domain.Coverage, map[string]int64) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "twitter.zip")
	if err := archivetest.ZipDir("testdata", zipPath); err != nil {
		t.Fatal(err)
	}
	blobs := map[string]int64{}
	im := bundle.NewImporter(New())
	im.PutBlob = func(key, _ string, r io.Reader) error {
		n, err := io.Copy(io.Discard, r)
		blobs[key] = n
		return err
	}
	var events []domain.Event
	cov, err := im.ImportZip(context.Background(), zipPath, func(ev domain.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return events, cov, blobs
}

func TestImportTwitter(t *testing.T) {
	events, cov, blobs := importTestdata(t)

	want := map[string]int{
		"twitter.tweet":        5, // tweets.js 3 + tweets-part1.js 1 + deleted-tweets.js 1
		"twitter.note_tweet":   1,
		"twitter.moment":       1,
		"twitter.dm":           3, // 2 one-to-one + 1 group message
		"twitter.dm_event":     4, // join, rename, participants join, participants leave
		"twitter.like":         1,
		"twitter.follow":       1,
		"twitter.follower":     1,
		"twitter.block":        1,
		"twitter.mute":         1,
		"twitter.list":         3,
		"twitter.saved_search": 1,
		"twitter.account":      3, // account.js, profile.js, verified.js
		"twitter.unknown":      4, // tweetdeck.js, ad-impressions.js, manifest.js, README.txt: generic, not dropped
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType = %v, want %v", cov.PerType, want)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	if cov.MediaStored != 3 || cov.Generic != 4 {
		t.Errorf("media stored / generic = %d/%d, want 3/4", cov.MediaStored, cov.Generic)
	}

	declared := map[string]domain.Category{}
	for _, d := range New().Descriptor().EventTypes {
		declared[d.Type] = d.Category
	}
	ids := map[string]bool{}
	for _, ev := range events {
		if cat, ok := declared[ev.Type]; !ok || cat != ev.Category {
			t.Errorf("type/category %q/%q not declared", ev.Type, ev.Category)
		}
		if ev.ExternalID == "" || ids[ev.ExternalID] {
			t.Errorf("ExternalID %q empty or reused (%s)", ev.ExternalID, ev.Type)
		}
		ids[ev.ExternalID] = true
		// Every Media.BlobKey must point at a member the importer stored.
		for _, m := range ev.Media {
			if m.BlobKey == "" {
				continue
			}
			if _, ok := blobs[m.BlobKey]; !ok && !strings.Contains(m.BlobKey, "profile_media") {
				t.Errorf("%s: media blob %q was not stored (have %v)", ev.ExternalID, m.BlobKey, keys(blobs))
			}
		}
	}

	// tweets.js: text, time, hashtags, expanded urls, source, counts, media.
	tw := byExternalID(events, "tweet:111")
	if tw == nil {
		t.Fatal("tweet 111 missing")
	}
	if !tw.OccurredAt.Equal(time.Date(2018, 10, 10, 20, 19, 24, 0, time.UTC)) || tw.Title != "Tweet" || tw.ThreadExternalID != "tweet:111" ||
		tw.Metadata["source"] != "Twitter Web App" || tw.Metadata["favorite_count"] != "3" || tw.Metadata["lang"] != "en" ||
		tw.Metadata["hashtags"].([]string)[0] != "golang" || tw.Metadata["urls"].([]string)[0] != "https://example.com/archiver" {
		t.Errorf("tweet not mapped: %+v", tw)
	}
	if _, edited := tw.Metadata["edit_tweet_ids"]; edited {
		t.Errorf("single editTweetId must not count as edited: %+v", tw.Metadata)
	}
	if len(tw.Media) != 2 {
		t.Fatalf("tweet media = %+v, want photo + video", tw.Media)
	}
	if tw.Media[0].BlobKey != "data/tweets_media/111-AbCdEf.jpg" || tw.Media[0].MIME != "image/jpeg" || tw.Media[0].URL != "https://pbs.twimg.com/media/AbCdEf.jpg" {
		t.Errorf("photo media = %+v", tw.Media[0])
	}
	if tw.Media[1].BlobKey != "data/tweets_media/111-HighRes.mp4" || tw.Media[1].MIME != "video/mp4" || !strings.Contains(tw.Media[1].URL, "1280x720") {
		t.Errorf("video media (best bitrate variant) = %+v", tw.Media[1])
	}

	// A reply links to its parent and carries the edit chain.
	re := byExternalID(events, "tweet:222")
	if re == nil || re.Title != "Reply to @jane" || re.ParentExternalID != "tweet:100" || re.ThreadExternalID != "tweet:100" ||
		len(re.Targets) != 1 || re.Targets[0].Relation != domain.RelationRepliedTo || re.Targets[0].TargetURL != "https://twitter.com/jane/status/100" ||
		re.Metadata["mentions"].([]string)[0] != "jane" || len(re.Metadata["edit_tweet_ids"].([]string)) != 2 {
		t.Errorf("reply not mapped: %+v", re)
	}
	// A retweet is recognised by its "RT @" prefix (there is no retweeted_status).
	if rt := byExternalID(events, "tweet:333"); rt == nil || rt.Title != "Retweet of @max" || rt.Metadata["retweet"] != true || rt.Metadata["retweeted_text"] != "Borrow checker FTW" {
		t.Errorf("retweet not mapped: %+v", rt)
	}
	// Split parts and deleted tweets are read; deleted ones are flagged.
	if p := byExternalID(events, "tweet:444"); p == nil || !strings.Contains(p.Metadata["member"].(string), "tweets-part1.js") {
		t.Errorf("tweets-part1.js not imported: %+v", p)
	}
	if d := byExternalID(events, "tweet:999"); d == nil || d.Metadata["deleted"] != true || d.Title != "Deleted tweet" {
		t.Errorf("deleted tweet not mapped: %+v", d)
	}
	// note-tweet.js: long-form text, ISO timestamp.
	if n := byExternalID(events, "note_tweet:1700000000000000001"); n == nil || !strings.Contains(n.Body, "long-form text") ||
		!n.OccurredAt.Equal(time.Date(2023, 8, 12, 16, 10, 37, 0, time.UTC)) {
		t.Errorf("note tweet not mapped: %+v", n)
	}

	// Direct messages: thread by conversation, raw sender id as actor, urls,
	// reactions, media blob key <message id>-<file name>.
	dm := byExternalID(events, "dm:1490000000000000001")
	if dm == nil || dm.ThreadExternalID != "dm_conversation:1000-2000" || dm.Actor != "1000" || dm.Body != "Hi! Are you at the meetup? https://t.co/dmlink" ||
		!dm.OccurredAt.Equal(time.Date(2022, 1, 27, 15, 58, 52, 744_000_000, time.UTC)) || dm.Category != domain.CategoryMessage ||
		dm.Metadata["sender_id"] != "1000" || len(dm.Metadata["reactions"].([]map[string]string)) != 1 || dm.Metadata["urls"].([]map[string]string)[0]["expanded"] != "https://example.com/talk" {
		t.Errorf("dm not mapped: %+v", dm)
	}
	if pic := byExternalID(events, "dm:1490000000000000002"); pic == nil || len(pic.Media) != 1 || pic.Media[0].BlobKey != "data/direct_messages_media/1490000000000000002-AbCdEfGh.jpg" || pic.Actor != "2000" {
		t.Errorf("dm media not mapped: %+v", pic)
	}
	if g := byExternalID(events, "dm:1490000000000000101"); g == nil || g.Metadata["group"] != true || g.ThreadExternalID != "dm_conversation:1490000000000000100" || g.Title != "Group direct message" {
		t.Errorf("group dm not mapped: %+v", g)
	}
	evs := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "twitter.dm_event" {
			evs[ev.Metadata["event"].(string)] = ev
		}
	}
	if len(evs) != 4 || evs["conversationNameUpdate"].Metadata["name"] != "Book club" || evs["participantsJoin"].Metadata["user_ids"].([]string)[0] != "4000" ||
		evs["joinConversation"].Actor != "3000" || len(evs["joinConversation"].Metadata["participants"].([]string)) != 3 || evs["participantsLeave"].OccurredAt.IsZero() {
		t.Errorf("dm events not mapped: %+v", evs)
	}

	// Undated connection files.
	if b := byExternalID(events, "block:666"); b == nil || !b.OccurredAt.IsZero() || b.Targets[0].TargetURL != "https://twitter.com/intent/user?user_id=666" {
		t.Errorf("block not mapped: %+v", b)
	}
	if m := byExternalID(events, "mute:667"); m == nil || !m.OccurredAt.IsZero() || m.Type != "twitter.mute" {
		t.Errorf("mute not mapped: %+v", m)
	}
	lists := map[string]domain.Event{}
	for _, ev := range events {
		if ev.Type == "twitter.list" {
			lists[ev.Metadata["relation"].(string)] = ev
		}
	}
	if len(lists) != 3 || lists["created"].Title != "Created list gophers" || lists["member"].Targets[0].TargetURL != "https://twitter.com/jane/lists/1234567890" ||
		lists["subscribed"].ExternalID != "list:subscribed:https://twitter.com/max/lists/rustaceans" {
		t.Errorf("lists not mapped: %+v", lists)
	}
	if s := byExternalID(events, "saved_search:123"); s == nil || s.Body != "golang -filter:retweets" || s.Category != domain.CategoryConsumption {
		t.Errorf("saved search not mapped: %+v", s)
	}
	if mo := byExternalID(events, "moment:900"); mo == nil || mo.Title != "Moment: Conference day" || mo.Body != "Talks I liked" || mo.OccurredAt.Year() != 2019 || mo.Metadata["tweets"] == nil {
		t.Errorf("moment not mapped: %+v", mo)
	}

	// account.js / profile.js / verified.js
	if a := byExternalID(events, "account:1000"); a == nil || a.Title != "Account @johnny" || a.Metadata["username"] != "johnny" || a.Metadata["account_id"] != "1000" ||
		!a.OccurredAt.Equal(time.Date(2010, 3, 1, 9, 0, 0, 0, time.UTC)) || a.Category != domain.CategoryProfile {
		t.Errorf("account not mapped: %+v", a)
	}
	if pr := byExternalID(events, "profile:self"); pr == nil || pr.Body != "Builds archives" || pr.Metadata["location"] != "Karlsruhe" || len(pr.Media) != 2 ||
		pr.Media[0].BlobKey != "data/profile_media/avatar.jpg" {
		t.Errorf("profile not mapped: %+v", pr)
	}
	if v := byExternalID(events, "verified:1000"); v == nil || v.Metadata["verified"] != false {
		t.Errorf("verified not mapped: %+v", v)
	}

	// Files left to generic capture are captured with content, not emptied.
	for _, member := range []string{"data/tweetdeck.js", "data/ad-impressions.js", "data/manifest.js"} {
		u := byExternalID(events, "twitter:unknown:"+member)
		if u == nil {
			t.Errorf("%s not captured generically", member)
			continue
		}
		if u.Metadata["bytes"].(int64) == 0 || !strings.Contains(u.Metadata["preview"].(string), "window.") {
			t.Errorf("%s captured empty: %+v", member, u.Metadata)
		}
	}
	// Likes and follows as before.
	if l := byExternalID(events, "like:999"); l == nil || l.Targets[0].TargetURL != "https://twitter.com/x/status/999" || l.Targets[0].Relation != domain.RelationLiked {
		t.Errorf("like not mapped: %+v", l)
	}
	if f := byExternalID(events, "follow:555"); f == nil || f.Targets[0].Relation != domain.RelationFollowed {
		t.Errorf("follow not mapped: %+v", f)
	}
	if f := byExternalID(events, "follower:777"); f == nil || f.Actor != "777" {
		t.Errorf("follower not mapped: %+v", f)
	}
}

func TestExternalIDsStable(t *testing.T) {
	a, _, _ := importTestdata(t)
	b, _, _ := importTestdata(t)
	if len(a) != len(b) {
		t.Fatalf("event counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ExternalID != b[i].ExternalID {
			t.Errorf("event %d: %q vs %q", i, a[i].ExternalID, b[i].ExternalID)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := map[string]bool{
		"data/tweets.js":                       true,
		"data/tweet.js":                        true,
		"data/tweets-part1.js":                 true,
		"data/tweets-part12.js":                true,
		"data/tweet-headers.js":                false,
		"data/deleted-tweets.js":               true,
		"data/deleted-tweet-headers.js":        false,
		"data/community-tweet.js":              true,
		"data/twitter-circle-tweet.js":         true,
		"data/note-tweet.js":                   true,
		"data/direct-messages.js":              true,
		"data/direct-messages-part1.js":        true,
		"data/direct-message-part1.js":         true,
		"data/direct-messages-group.js":        true,
		"data/direct-message-headers.js":       false,
		"data/direct-message-group-headers.js": false,
		"data/block.js":                        true,
		"data/mute.js":                         true,
		"data/lists-created.js":                true,
		"data/lists-member.js":                 true,
		"data/lists-subscribed.js":             true,
		"data/saved-search.js":                 true,
		"data/moment.js":                       true,
		"data/account.js":                      true,
		"data/account-creation-ip.js":          false,
		"data/profile.js":                      true,
		"data/verified.js":                     true,
		"data/like.js":                         true,
		"data/following.js":                    true,
		"data/follower.js":                     true,
		"data/tweetdeck.js":                    false,
		"data/ad-impressions.js":               false,
		"data/manifest.js":                     false,
		"data/tweets_media/1-abc.jpg":          false,
		"Your archive.html":                    false,
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

func TestRetweetOf(t *testing.T) {
	if u, rest, ok := retweetOf("RT @max: Borrow checker FTW"); !ok || u != "max" || rest != "Borrow checker FTW" {
		t.Errorf("retweetOf = %q %q %v", u, rest, ok)
	}
	for _, s := range []string{"", "RT", "RT @", "RT @no colon", "Hello RT @x: y", "@x: y"} {
		if _, _, ok := retweetOf(s); ok {
			t.Errorf("retweetOf(%q) should be false", s)
		}
	}
}

func byExternalID(evs []domain.Event, id string) *domain.Event {
	for i := range evs {
		if evs[i].ExternalID == id {
			return &evs[i]
		}
	}
	return nil
}

func keys(m map[string]int64) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
