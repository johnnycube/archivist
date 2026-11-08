package discord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/provider"
)

var kinds = []string{"message", "channel", "channel_label", "server", "guild", "relationship", "unknown", "nonsense"}

// FuzzNormalize: any JSON value under any kind yields events, never an error.
func FuzzNormalize(f *testing.F) {
	seeds := []string{
		`{"ID":1202220000000000001,"Timestamp":"2024-01-31 12:34:56","Contents":"hi","Attachments":""}`,
		`{"ID":"1","Timestamp":"2024-01-31T12:36:00+01:00","Contents":"","Attachments":["https://cdn/x.png"]}`,
		`{"id":"100000000000000001","type":1,"recipients":["3","4"]}`,
		`{"id":"2","type":"GUILD_TEXT","name":"general","guild":{"id":"9","name":"G"}}`,
		`{"id":"1","name":"Direct Message with jane#0"}`,
		`{"id":"3","type":1,"nickname":null,"user":{"id":"3","username":"jane","global_name":"Jane","discriminator":"0"}}`,
		`{"section":"settings","preview":"{}","bytes":2,"content_hash":"ab"}`,
		`"str"`, `42`, `null`, `[]`, `{}`, `{"ID":{"x":1},"Attachments":{"y":2}}`, `{"recipients":"nope","guild":"nope"}`, `{"user":"x","type":{}}`,
	}
	for _, s := range seeds {
		for _, k := range kinds {
			f.Add(k, s)
		}
	}
	p := New()
	f.Fuzz(func(t *testing.T, kind, payload string) {
		if !json.Valid([]byte(payload)) {
			return
		}
		raw := rec(kind, []byte(payload))
		raw.Member = "messages/c100000000000000009/messages.json"
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

// FuzzExtract runs whole members through every matching extractor.
func FuzzExtract(f *testing.F) {
	f.Add("messages/c1/messages.json", `[{"ID":1,"Timestamp":"2024-01-31 12:34:56","Contents":"hi","Attachments":""}]`)
	f.Add("messages/c1/messages.csv", "ID,Timestamp,Contents,Attachments\n1,2023-06-01 08:00:00,\"a\nb\",\n")
	f.Add("messages/c1/channel.json", `{"id":"1","type":1}`)
	f.Add("messages/index.json", `{"1":"DM","2":null,"3":{"nested":true}}`)
	f.Add("servers/index.json", `{"9":"G"}`)
	f.Add("servers/9/guild.json", `{"id":"9","name":"G"}`)
	f.Add("account/user.json", `{"id":"3","username":"j","relationships":[{"id":"4","type":1,"user":{"id":"4"}}],"settings":{"a":1},"payments":[]}`)
	f.Add("account/user.json", `{"relationships":"nope","x":[1,2,3]}`)
	f.Add("messages/c1/messages.json", `{"messages":[{"ID":1}],"threads":[1,2]}`)
	p := New()
	f.Fuzz(func(t *testing.T, member, doc string) {
		for _, ex := range p.Extractors() {
			if !ex.Match(member) {
				continue
			}
			_ = ex.Extract(member, strings.NewReader(doc), func(raw provider.RawRecord) error {
				raw.Member = member
				if _, err := p.Normalize(context.Background(), raw); err != nil {
					t.Fatalf("Normalize failed for %s: %v (%s)", member, err, raw.Payload)
				}
				return nil
			})
		}
	})
}
