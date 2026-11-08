package instagram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/meta"
)

// kinds is every record kind the extractors emit.
var kinds = []string{
	"post", "reel", "archived_post", "igtv", "story", "thread_post", "comment", "reel_comment",
	"like", "comment_like", "story_like", "thread_like", "save", "follow", "follower", "close_friend",
	"follow_request_sent", "follow_request_received", "unfollow", "block", "search", "login", "logout",
	meta.MessageKind, "nonsense",
}

// FuzzNormalize: whatever JSON value an array element is, Normalize must
// return events (a malformed record becomes instagram.unknown) and never panic
// or fail the import.
func FuzzNormalize(f *testing.F) {
	seeds := []string{
		`{"media":[{"uri":"media/posts/1.jpg","creation_timestamp":1650000000,"title":""}],"creation_timestamp":1650000000,"title":"cafÃ©"}`,
		`{"uri":"media/stories/1.jpg","creation_timestamp":1650040000,"title":"x"}`,
		`{"string_map_data":{"Comment":{"value":"hi"},"Media Owner":{"value":"a"},"Time":{"timestamp":1650030000}}}`,
		`{"title":"a","string_list_data":[{"href":"https://www.instagram.com/p/AAA/","value":"â\u009d¤","timestamp":1650001000}]}`,
		`{"title":"a","string_map_data":{"Saved on":{"href":"https://www.instagram.com/p/BBB/","timestamp":1650002000}}}`,
		`{"timestamp":1750000000,"media":[],"label_values":[{"label":"URL","value":"https://www.instagram.com/p/CCC/"}]}`,
		`{"string_map_data":{"Search":{"value":"jane"},"Time":{"timestamp":1650043000}}}`,
		`{"string_map_data":{"IP Address":{"value":"203.0.113.7"},"Time":{"timestamp":1650045000},"User Agent":{"value":"UA"}}}`,
		`{"participants":["Jane","Johnny"],"message":{"sender_name":"Jane","timestamp_ms":1650020002500,"content":"hi","reactions":[{"reaction":"x","actor":"Johnny"}]}}`,
		`{"participants":null,"message":{"photos":"not-a-list"}}`,
		`"just a string"`, `42`, `null`, `[]`, `{}`, `{"media":"x"}`, `{"string_map_data":[]}`, `{"string_list_data":{}}`,
	}
	for _, s := range seeds {
		for _, k := range kinds {
			f.Add(k, s)
		}
	}
	p := New()
	f.Fuzz(func(t *testing.T, kind, payload string) {
		if !json.Valid([]byte(payload)) {
			return // extractors only ever hand over valid JSON
		}
		raw := meta.Rec(kind, []byte(payload))
		raw.Member = "your_instagram_activity/fuzz/fuzz.json"
		evs, err := p.Normalize(context.Background(), raw)
		if err != nil {
			t.Fatalf("Normalize(%q, %s) failed: %v", kind, payload, err)
		}
		if len(evs) == 0 {
			t.Fatalf("Normalize(%q, %s) dropped the record", kind, payload)
		}
		for _, ev := range evs {
			if ev.ExternalID == "" || ev.Type == "" {
				t.Errorf("event without id/type: %+v", ev)
			}
		}
	})
}

// FuzzExtract runs whole members through the extractors: a member that is not
// JSON fails as a whole, anything else yields records without panicking.
func FuzzExtract(f *testing.F) {
	f.Add("your_instagram_activity/messages/inbox/a_1/message_1.json", `{"participants":[{"name":"A"},{"name":"B"}],"messages":[{"sender_name":"A","timestamp_ms":1,"content":"x"}],"title":"A"}`)
	f.Add("your_instagram_activity/likes/liked_posts.json", `{"likes_media_likes":[{"title":"a","string_list_data":[{"href":"h","timestamp":1}]}]}`)
	f.Add("connections/followers_and_following/followers_1.json", `[{"string_list_data":[{"href":"h","value":"v","timestamp":1}]}]`)
	f.Add("your_instagram_activity/messages/inbox/a_1/message_1.json", `{"participants":[1,2],"messages":[null,"x",{"reactions":{}}]}`)
	f.Add("posts_1.json", `[`)
	p := New()
	f.Fuzz(func(t *testing.T, member, doc string) {
		for _, ex := range p.Extractors() {
			if !ex.Match(member) {
				continue
			}
			_ = ex.Extract(member, strings.NewReader(doc), func(raw provider.RawRecord) error {
				raw.Member = member
				_, err := p.Normalize(context.Background(), raw)
				if err != nil {
					t.Fatalf("Normalize failed for %s: %v (%s)", member, err, raw.Payload)
				}
				return nil
			})
		}
	})
}
