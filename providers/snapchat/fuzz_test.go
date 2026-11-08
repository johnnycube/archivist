package snapchat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/provider"
)

// FuzzMember runs arbitrary JSON documents through every extractor and
// Normalize: a member that is not JSON fails as a whole, anything else must
// come out as events (unrecognized shapes as snapchat.unknown) without a panic.
func FuzzMember(f *testing.F) {
	members := []string{
		"json/chat_history.json", "json/snap_history.json", "json/memories_history.json", "json/friends.json",
		"json/search_history.json", "json/account.json", "json/talk_history.json", "json/story_history.json", "json/subscriptions.json",
	}
	seeds := []string{
		`{"jane_doe":[{"From":"jane_doe","Media Type":"TEXT","Created":"2024-01-31 12:34:56 UTC","Content":"hi","IsSender":false,"Created(microseconds)":1706704496123456,"IsSaved":true,"Media IDs":""}]}`,
		`{"Received Saved Chat History":[{"From":"a","Media Type":"TEXT","Created":"2020-05-01 10:00:00 UTC","Text":"x"}],"Sent Saved Chat History":[{"To":"a","Media Type":"MEDIA","Created":"2020-05-01 10:01:00 UTC","Text":""}]}`,
		`{"Saved Media":[{"Date":"2023-08-15 14:20:00 UTC","Media Type":"Image","Download Link":"https://x/dmd/memories?mid=m&sig=s"}]}`,
		`{"Friends":[{"Username":"a","Display Name":"A","Creation Timestamp":"2022-05-01 10:00:00 UTC"}],"Blocked Users":[{"Username":"b"}],"Shortcuts":[]}`,
		`{"Search History":[{"Date":"2024-01-15 20:00:00 UTC","Search Term":"jane"}]}`,
		`{"Basic Information":{"Username":"j"},"Login History":[{"IP":"203.0.113.7","Created":"2024-01-31 12:00:00 UTC","Status":"success"}]}`,
		`{"jane_doe":[{"Media Type":"VIDEO","Created":"2024-01-29 20:00:00 UTC","Duration":"125","IsSender":false}]}`,
		`{"Subscriptions":[{"Display Name":"N","Username":"n","Created":"2023-05-05 05:05:05 UTC"}]}`,
		`{"x":[1,"two",null,[],{"Created":42,"IsSender":"true","Media Type":7}]}`,
		`[{"From":"a"},"b",3]`, `{}`, `[]`, `null`, `"s"`, `{"a":{"b":{"c":[{"Created":"bad date"}]}}}`,
	}
	for _, m := range members {
		for _, s := range seeds {
			f.Add(m, s)
		}
	}
	p := New()
	f.Fuzz(func(t *testing.T, member, doc string) {
		if !json.Valid([]byte(doc)) {
			return
		}
		for _, ex := range p.Extractors() {
			if !ex.Match(member) {
				continue
			}
			err := ex.Extract(member, strings.NewReader(doc), func(raw provider.RawRecord) error {
				raw.Member = member
				evs, err := p.Normalize(context.Background(), raw)
				if err != nil {
					t.Fatalf("Normalize failed: %v (%s)", err, raw.Payload)
				}
				if len(evs) == 0 {
					t.Fatalf("record dropped: %s", raw.Payload)
				}
				for _, ev := range evs {
					if ev.ExternalID == "" || ev.Type == "" {
						t.Errorf("event without id/type: %+v", ev)
					}
				}
				return nil
			})
			trimmed := strings.TrimSpace(doc)
			if err != nil && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
				t.Fatalf("a JSON object or array must not fail extraction: %v", err)
			}
		}
	})
}
