package instagram

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
)

func TestImportInstagram(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "instagram.zip")
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

	want := map[string]int{
		"instagram.post":           3, // feed post + reel + archived post
		"instagram.thread_post":    2,
		"instagram.comment":        3, // two post comments + one reel comment
		"instagram.story":          2,
		"instagram.message":        4, // inbox thread (text, photo, share) + one message request
		"instagram.like":           5, // post (string_list_data) + post (label_values) + comment + story + thread
		"instagram.save":           1,
		"instagram.follow":         2, // following.json + close_friends.json
		"instagram.follower":       3, // followers_1.json (array) + followers_2.json (relationships_followers)
		"instagram.follow_request": 1,
		"instagram.unfollow":       2, // recently unfollowed + blocked
		"instagram.search":         2,
		"instagram.login":          1,
		"instagram.unknown":        2, // personal_information.json + posts_viewed.json captured generically
	}
	if len(cov.PerType) != len(want) {
		t.Errorf("PerType has %d types, want %d: %v", len(cov.PerType), len(want), cov.PerType)
	}
	for typ, n := range want {
		if cov.PerType[typ] != n {
			t.Errorf("PerType[%q] = %d, want %d", typ, cov.PerType[typ], n)
		}
	}
	if cov.Events != 33 || cov.Duplicates != 0 {
		t.Errorf("Events = %d, Duplicates = %d, want 33 and 0", cov.Events, cov.Duplicates)
	}

	declared := map[string]bool{}
	for _, td := range New().Descriptor().EventTypes {
		declared[td.Type] = true
	}
	ids := map[string]int{}
	for _, ev := range events {
		ids[ev.ExternalID]++
		if !declared[ev.Type] {
			t.Errorf("event type %q is not declared in the descriptor", ev.Type)
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("ExternalID %q used by %d events", id, n)
		}
	}
	// ExternalIDs of the original four types are persisted dedupe keys.
	for _, id := range []string{
		"post:media/posts/202301/photo1.jpg",
		"like:https://www.instagram.com/p/AAA/",
		"save:https://www.instagram.com/p/BBB/",
		"follow:https://www.instagram.com/best.friend/",
	} {
		if ids[id] != 1 {
			t.Errorf("ExternalID %q missing: it must not change across versions", id)
		}
	}

	// The Meta mojibake in the caption must be repaired: "cafÃ©" -> "café".
	post := byType(events, "instagram.post")
	if post == nil {
		t.Fatal("expected an instagram.post event")
	}
	if post.Body != "café vibes this morning" {
		t.Errorf("post body = %q, want %q (mojibake should be repaired)", post.Body, "café vibes this morning")
	}
	// Save and follow carry the right targets.
	if !hasRelation(events, "instagram.save", domain.RelationSaved) {
		t.Error("expected instagram.save with saved target")
	}
	if !hasRelation(events, "instagram.follow", domain.RelationFollowed) {
		t.Error("expected instagram.follow with followed target")
	}

	msg := byBody(events, "instagram.message", "Café at five?")
	if msg == nil {
		t.Fatal("message with repaired mojibake not found")
	}
	if msg.Actor != "Jane Doe" || msg.ThreadExternalID != "inbox/janedoe_1234" || msg.Title != "Message in Jane Doe, Johnny" || msg.Category != domain.CategoryMessage {
		t.Errorf("message not mapped: %+v", msg)
	}
	if got := msg.OccurredAt.UnixMilli(); got != 1650020002500 {
		t.Errorf("message OccurredAt = %d ms, want timestamp_ms 1650020002500", got)
	}
	if rs, _ := msg.Metadata["reactions"].([]map[string]string); len(rs) != 1 || rs[0]["reaction"] != "❤" || rs[0]["actor"] != "Johnny" {
		t.Errorf("message reactions = %v, want one repaired heart by Johnny", msg.Metadata["reactions"])
	}
	if photo := byBody(events, "instagram.message", ""); photo == nil || len(photo.Media) != 1 {
		t.Errorf("photo message should carry its attachment: %+v", photo)
	}
	if share := byBody(events, "instagram.message", "Best pasta in town"); share == nil ||
		share.Metadata["link"] != "https://www.instagram.com/reel/RRR/" || share.Metadata["share_owner"] != "great.recipes" {
		t.Errorf("shared reel not kept: %+v", share)
	}
	if req := byBody(events, "instagram.message", "Hi, is the bike still for sale?"); req == nil ||
		req.ThreadExternalID != "message_requests/maxmustermann_5678" || req.Metadata["folder"] != "message_requests" {
		t.Errorf("message request not mapped: %+v", req)
	}

	c := byBody(events, "instagram.comment", "Looks delicious, crème brûlée next?")
	if c == nil || c.Title != "Commented on great.recipes's post" || c.OccurredAt.Unix() != 1650030000 || c.Metadata["media_owner"] != "great.recipes" {
		t.Errorf("post comment not mapped: %+v", c)
	}
	reel := 0
	for _, ev := range events {
		if ev.Type == "instagram.comment" && ev.Body == "Congrats!" && ev.Metadata["via"] == "reels" {
			reel++
			if ev.Title != "Commented on cool.account's reel" {
				t.Errorf("reel comment title = %q", ev.Title)
			}
		}
	}
	if reel != 1 {
		t.Errorf("got %d reel comments, want 1", reel)
	}

	st := byBody(events, "instagram.story", "Sunset over the café")
	if st == nil || st.Category != domain.CategoryMedia || len(st.Media) != 1 || st.Media[0].BlobKey != "media/stories/202204/story1.jpg" || st.OccurredAt.Unix() != 1650040000 {
		t.Errorf("story not mapped: %+v", st)
	}

	th := byBody(events, "instagram.thread_post", "First thread — hello world")
	if th == nil || len(th.Media) != 0 || th.OccurredAt.Unix() != 1690000000 {
		t.Errorf("text-only thread not mapped: %+v", th)
	}
	if pic := byBody(events, "instagram.thread_post", "With a picture"); pic == nil || len(pic.Media) != 1 {
		t.Errorf("thread with a picture should carry it: %+v", pic)
	}

	likes := map[string]*domain.Event{} // via → like
	for i, ev := range events {
		if ev.Type != "instagram.like" {
			continue
		}
		if len(ev.Targets) != 1 || ev.Targets[0].Relation != domain.RelationLiked {
			t.Errorf("like without a liked target: %+v", ev)
		}
		via, _ := ev.Metadata["via"].(string)
		if via == "" && ev.OccurredAt.Unix() == 1750000000 {
			via = "label_values"
		}
		likes[via] = &events[i]
	}
	if l := likes["threads"]; l == nil || l.Title != "Liked a thread by cool.account" || l.Targets[0].TargetURL != "https://www.threads.net/@cool.account/post/TTT" {
		t.Errorf("liked thread not mapped: %+v", l)
	}
	if l := likes["comment"]; l == nil || l.Title != "Liked a comment by cool.account" {
		t.Errorf("liked comment not mapped: %+v", l)
	}
	if l := likes["story"]; l == nil || l.Title != "Liked a story by best.friend" || l.OccurredAt.Unix() != 1650050000 {
		t.Errorf("story like not mapped: %+v", l)
	}
	if l := likes["label_values"]; l == nil || l.Title != "Liked new.format" || l.ExternalID != "like:https://www.instagram.com/p/CCC/" {
		t.Errorf("label_values like not mapped: %+v", l)
	}

	followers := map[string]bool{}
	for _, ev := range events {
		if ev.Type == "instagram.follower" {
			followers[ev.Actor] = ev.Title == ev.Actor+" followed you" && len(ev.Targets) == 0
		}
	}
	if !followers["best.friend"] || !followers["jane.doe"] || !followers["wrapped.follower"] {
		t.Errorf("followers = %v, want best.friend (also followed back), jane.doe and wrapped.follower", followers)
	}

	// Newly mapped connection lists (paths and keys as documented by the
	// Instagram export guides: relationships_close_friends,
	// relationships_follow_requests_sent, relationships_unfollowed_users,
	// relationships_blocked_users).
	if cf := byID(events, "close_friend:https://www.instagram.com/best.friend/"); cf == nil || cf.Type != "instagram.follow" ||
		cf.Title != "Added best.friend to close friends" || cf.Metadata["list"] != "close_friends" || cf.OccurredAt.Unix() != 1650006000 {
		t.Errorf("close friend not mapped: %+v", cf)
	}
	if fr := byType(events, "instagram.follow_request"); fr == nil || fr.Title != "Requested to follow private.account" || fr.Metadata["direction"] != "sent" {
		t.Errorf("follow request not mapped: %+v", fr)
	}
	unfollows := map[string]string{}
	for _, ev := range events {
		if ev.Type == "instagram.unfollow" {
			unfollows[ev.Metadata["status"].(string)] = ev.Title
		}
	}
	if unfollows["unfollowed"] != "Unfollowed old.acquaintance" || unfollows["blocked"] != "Blocked spam.bot" {
		t.Errorf("unfollow/block not mapped: %v", unfollows)
	}

	// Reels and archived posts share the post shape and id space.
	if reel := byID(events, "post:media/reels/202204/reel1.mp4"); reel == nil || reel.Title != "Instagram reel" || reel.Metadata["via"] != "reels" ||
		reel.Body != "Morning ride — fast" || len(reel.Media) != 1 {
		t.Errorf("reel not mapped: %+v", reel)
	}
	if arch := byID(events, "post:media/archived_posts/202204/old1.jpg"); arch == nil || arch.Metadata["archived"] != true || arch.Body != "Archived memory" {
		t.Errorf("archived post not mapped: %+v", arch)
	}

	// Searches and logins.
	if s := byBody(events, "instagram.search", "jane.doe"); s == nil || s.Category != domain.CategoryConsumption || s.OccurredAt.Unix() != 1650043000 || s.Metadata["kind"] != "profile_searches" {
		t.Errorf("profile search not mapped: %+v", s)
	}
	if s := byBody(events, "instagram.search", "crème brûlée recipe"); s == nil || s.Metadata["kind"] != "word_or_phrase_searches" {
		t.Errorf("keyword search (mojibake repaired) not mapped: %+v", s)
	}
	if l := byType(events, "instagram.login"); l == nil || l.Metadata["ip"] != "203.0.113.7" || l.Metadata["language"] != "de" ||
		l.OccurredAt.Unix() != 1650045000 || l.Title != "Logged in to Instagram" || l.Metadata["action"] != "login" {
		t.Errorf("login not mapped: %+v", l)
	}

	// posts_viewed.json must not be claimed by the posts_N.json extractor: its
	// records would be silently skipped instead of captured.
	generic := map[string]bool{}
	for _, m := range cov.GenericMembers {
		generic[filepath.Base(m)] = true
	}
	if !generic["posts_viewed.json"] || !generic["personal_information.json"] {
		t.Errorf("GenericMembers = %v, want posts_viewed.json and personal_information.json", cov.GenericMembers)
	}
}

func byID(evs []domain.Event, id string) *domain.Event {
	for i := range evs {
		if evs[i].ExternalID == id {
			return &evs[i]
		}
	}
	return nil
}

func byType(evs []domain.Event, typ string) *domain.Event {
	for i := range evs {
		if evs[i].Type == typ {
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

func hasRelation(evs []domain.Event, typ string, rel domain.Relation) bool {
	for _, e := range evs {
		if e.Type != typ {
			continue
		}
		for _, tg := range e.Targets {
			if tg.Relation == rel {
				return true
			}
		}
	}
	return false
}
