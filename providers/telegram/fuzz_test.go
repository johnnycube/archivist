package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/provider"
)

// FuzzResult runs arbitrary documents through the result.json walker and
// Normalize: a document that is not JSON fails as a whole, anything else must
// come out as events without a panic or a Normalize error.
func FuzzResult(f *testing.F) {
	seeds := []string{
		`{"name":"Max","type":"personal_chat","id":5555,"messages":[{"id":1,"type":"message","date_unixtime":"1706868000","from":"Max","from_id":"user5555","photo":"photos/p.jpg","text":"hi","text_entities":[{"type":"plain","text":"hi"}]}]}`,
		`{"chats":{"list":[{"name":"A","type":"private_group","id":1,"messages":[{"id":1,"type":"service","action":"phone_call","duration_seconds":3,"actor":"A","members":["B",null]}]}]}}`,
		`{"contacts":{"list":[{"first_name":"J","last_name":"E","phone_number":"+1","date_unixtime":"1685602800"}]},"sessions":{"list":[{"platform":"x"}]}}`,
		`{"messages":[{"id":1,"poll":{"question":"q","answers":[{"text":"a"}]}},{"id":2,"location_information":{"latitude":1,"longitude":2}},{"id":3,"contact_information":{"first_name":"M"}}]}`,
		`{"messages":["x",42,null,[],{"id":{"nested":true},"text":{"not":"a string"},"text_entities":"x","reactions":7}]}`,
		`{"chats":{"list":[42,"x",null,{"messages":"nope"}]},"left_chats":[]}`,
		`[]`, `{}`, `null`, `{"messages":[`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := New()
	f.Fuzz(func(t *testing.T, doc string) {
		if !json.Valid([]byte(doc)) {
			return
		}
		err := resultEx{}.Extract("result.json", strings.NewReader(doc), func(raw provider.RawRecord) error {
			raw.Member = "result.json"
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
		// Only an object is a Telegram export; anything else may fail as a whole.
		if err != nil && strings.HasPrefix(strings.TrimSpace(doc), "{") {
			t.Fatalf("a JSON object must not fail extraction: %v", err)
		}
	})
}
