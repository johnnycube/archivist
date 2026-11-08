// Package discord ingests a Discord data package ("Request all of my Data").
//
//	messages/index.json                 {"<channel id>": "Direct Message with jane#0", …}
//	messages/c<channel id>/channel.json {"id","type","name"?,"guild":{…}?,"recipients":[…]?}
//	messages/c<channel id>/messages.json  [{"ID","Timestamp","Contents","Attachments"}, …]
//	messages/c<channel id>/messages.csv   older packages: the same four columns
//	servers/index.json                  {"<guild id>": "Server name", …}
//	servers/<guild id>/guild.json       {"id","name"} (plus audit-log.json etc., left generic)
//	account/user.json                   profile, settings, relationships (friends), payments, …
//	activity/**                         analytics logs (left to generic capture)
//
// Evidence for the layout: discord-data-package-explorer (Androz2091), which
// reads the same files; pre-2021 packages name channel folders by the bare id
// and pre-2024 packages write messages.csv instead of messages.json.
//
// The package holds only the user's own messages. Extractors are stateless
// across members, so a message cannot look up its channel's name; it carries the
// channel id (from its member path) as ThreadExternalID, and discord.channel
// events from channel.json and messages/index.json carry the names under the
// same ThreadExternalID.
package discord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/jsonpreview"
)

const sourceID = "discord"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Discord",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "discord.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "discord.channel", Category: domain.CategoryConnection},
			{Type: "discord.server", Category: domain.CategoryConnection},
			{Type: "discord.friend", Category: domain.CategoryConnection},
			{Type: "discord.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors. activity/** (newline-delimited analytics logs), servers/<id>/*,
// and everything else not listed is captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		messagesEx{},
		channelEx{},
		indexEx{dir: "messages", kind: "channel_label"},
		indexEx{dir: "servers", kind: "server"},
		guildEx{},
		userEx{},
	}
}

// under reports whether member is <…/>dir/<…>/base with the given base name,
// compared case-insensitively (newer packages capitalize the folders).
func under(member, dir string, depth int, bases ...string) bool {
	parts := strings.Split(strings.ToLower(member), "/")
	if len(parts) < depth+2 || parts[len(parts)-depth-2] != dir {
		return false
	}
	for _, b := range bases {
		if parts[len(parts)-1] == b {
			return true
		}
	}
	return false
}

// channelOf turns …/messages/c1234/messages.json into 1234. Older packages
// name the folder by the bare id.
func channelOf(member string) string {
	dir := path.Base(path.Dir(member))
	if id := strings.TrimLeft(dir, "cC"); id != "" && strings.Trim(id, "0123456789") == "" {
		return id
	}
	return dir
}

// ---- extraction ----

// messagesEx reads one channel's messages: messages.json (a top-level array) or
// the older messages.csv.
type messagesEx struct{}

func (messagesEx) Match(member string) bool {
	return under(member, "messages", 1, "messages.json", "messages.csv")
}

func (messagesEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	if strings.HasSuffix(strings.ToLower(member), ".csv") {
		return bundle.ReadCSV(r, func(row map[string]string) error {
			if id, ok := row["\ufeffID"]; ok { // byte-order mark glued to the first header
				row["ID"] = id
				delete(row, "\ufeffID")
			}
			payload, err := json.Marshal(row)
			if err != nil {
				return err
			}
			return emit(rec("message", payload))
		})
	}
	return bundle.StreamJSONArray(r, []string{"messages"}, func(el json.RawMessage) error {
		return emit(rec("message", el))
	}, bundle.ReportSkipped(member, emit))
}

// smallLimit bounds the members that are decoded as one value; they are a few
// hundred bytes in practice.
const smallLimit = 1 << 20

// channelEx reads messages/c<id>/channel.json, one small object.
type channelEx struct{}

func (channelEx) Match(member string) bool { return under(member, "messages", 1, "channel.json") }

func (channelEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return smallObject(r, "channel", "channel.json", emit)
}

// guildEx reads servers/<id>/guild.json, one small object.
type guildEx struct{}

func (guildEx) Match(member string) bool { return under(member, "servers", 1, "guild.json") }

func (guildEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return smallObject(r, "guild", "guild.json", emit)
}

// smallObject decodes a member that is one small JSON object into a record of
// the given kind; an oversized or malformed member leaves an unknown record.
func smallObject(r io.Reader, kind, section string, emit func(provider.RawRecord) error) error {
	var el json.RawMessage
	if err := json.NewDecoder(io.LimitReader(r, smallLimit)).Decode(&el); err != nil {
		if _, err := io.Copy(io.Discard, r); err != nil {
			return err
		}
		payload, err := json.Marshal(unknownSection{Section: section, Preview: "unreadable: " + err.Error()})
		if err != nil {
			return err
		}
		return emit(rec("unknown", payload))
	}
	return emit(rec(kind, el))
}

// indexEx streams an index.json, one flat {"<id>": "<name>"} object, emitting a
// record per entry.
type indexEx struct{ dir, kind string }

func (e indexEx) Match(member string) bool { return under(member, e.dir, 0, "index.json") }

type indexEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (e indexEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	dec := bundle.NewRecordLimitedDecoder(r, 0)
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return jsonErr(err)
	}
	if tok != json.Delim('{') {
		return fmt.Errorf("decode json: expected an object, got %v", tok)
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return jsonErr(err)
		}
		val, err := dec.Token()
		if err != nil {
			return jsonErr(err)
		}
		if err := jsonpreview.SkipOpened(dec, val); err != nil {
			return err
		}
		id, _ := key.(string)
		name, _ := val.(string) // null for channels Discord no longer knows
		payload, err := json.Marshal(indexEntry{ID: id, Name: name})
		if err != nil {
			return err
		}
		if err := emit(rec(e.kind, payload)); err != nil {
			return err
		}
	}
	return nil
}

// userEx walks account/user.json. Its "relationships" array is streamed as
// friends. So that matching the member loses nothing compared with generic
// capture, the rest is kept too: the top-level scalar fields together in one
// unknown record, and every other section (settings, connections, payments, …)
// as an unknown record of its own with a preview and a hash.
type userEx struct{}

func (userEx) Match(member string) bool { return under(member, "account", 0, "user.json") }

func (userEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	dec := bundle.NewRecordLimitedDecoder(r, 0)
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return jsonErr(err)
	}
	if tok != json.Delim('{') {
		return fmt.Errorf("decode json: expected an object, got %v", tok)
	}
	fields := map[string]any{}
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			return jsonErr(err)
		}
		key, _ := name.(string)
		if key == "relationships" {
			open, err := dec.Token()
			if err != nil {
				return jsonErr(err)
			}
			if open != json.Delim('[') {
				if err := jsonpreview.SkipOpened(dec, open); err != nil {
					return err
				}
				continue
			}
			for dec.More() {
				var el json.RawMessage
				if err := dec.Decode(&el); err != nil {
					return jsonErr(err)
				}
				if err := emit(rec("relationship", el)); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // closing bracket
				return jsonErr(err)
			}
			continue
		}

		v, err := jsonpreview.Render(dec)
		if err != nil {
			return err
		}
		if v.Scalar {
			if whole, ok := v.Complete(); ok {
				fields[key] = whole
			} else {
				fields[key] = v.Preview()
			}
			continue
		}
		payload, err := json.Marshal(unknownSection{Section: key, Preview: v.Preview(), Bytes: v.Size, Hash: v.Hash})
		if err != nil {
			return err
		}
		if err := emit(rec("unknown", payload)); err != nil {
			return err
		}
	}
	if len(fields) == 0 {
		return nil
	}
	payload, err := json.Marshal(unknownSection{Section: "account fields", Fields: fields})
	if err != nil {
		return err
	}
	return emit(rec("unknown", payload))
}

type unknownSection struct {
	Section string         `json:"section"`
	Preview string         `json:"preview,omitempty"`
	Bytes   int64          `json:"bytes,omitempty"` // of the compact rendering
	Hash    string         `json:"content_hash,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func jsonErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("decode json: %w", err)
}

// rec hashes the kind into the dedupe key: a channel label and a server name
// can be the same bare {id, name} record.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// ---- normalization ----

// str is the permissive scalar: snowflake ids are numbers in messages.json and
// strings elsewhere; channel types were numbers before they became names.
type str = jsonpreview.Str

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	ev := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}

	switch raw.Kind {
	case "message":
		var m struct {
			ID          str             `json:"ID"`
			Timestamp   str             `json:"Timestamp"`
			Contents    str             `json:"Contents"`
			Attachments json.RawMessage `json:"Attachments"`
		}
		if err := json.Unmarshal(raw.Payload, &m); err != nil {
			return unrecognized(ev, raw), nil
		}
		channel := channelOf(raw.Member)
		ev.Type, ev.Category = "discord.message", domain.CategoryMessage
		ev.ExternalID = "msg:" + string(m.ID)
		if m.ID == "" { // not expected; keep the record apart rather than collapsing
			ev.ExternalID = "msg:" + channel + ":" + raw.ContentHash[:16]
		}
		ev.OccurredAt = parseTime(string(m.Timestamp))
		if ev.OccurredAt.IsZero() {
			ev.OccurredAt = snowflakeTime(string(m.ID))
		}
		ev.ThreadExternalID = channel
		ev.Title = "Message in channel " + channel
		ev.Body = string(m.Contents)
		ev.Metadata["channel_id"] = channel
		if urls := attachments(m.Attachments); len(urls) > 0 {
			ev.Metadata["attachments"] = urls // remote CDN links, not archive members
		}
		return one(ev), nil

	case "channel":
		var c struct {
			ID    str `json:"id"`
			Type  str `json:"type"`
			Name  str `json:"name"`
			Guild struct {
				ID   str `json:"id"`
				Name str `json:"name"`
			} `json:"guild"`
			Recipients []str `json:"recipients"`
		}
		if err := json.Unmarshal(raw.Payload, &c); err != nil {
			return unrecognized(ev, raw), nil
		}
		id := firstNonEmpty(string(c.ID), channelOf(raw.Member))
		ev.Type, ev.Category = "discord.channel", domain.CategoryConnection
		ev.ExternalID = "channel:" + id
		ev.ThreadExternalID = id
		switch {
		case c.Name != "" && c.Guild.Name != "":
			ev.Title = "Channel " + string(c.Name) + " in " + string(c.Guild.Name)
		case c.Name != "":
			ev.Title = "Channel " + string(c.Name)
		case c.Guild.Name != "":
			ev.Title = "Channel " + id + " in " + string(c.Guild.Name)
		default:
			ev.Title = "Channel " + id
		}
		ev.Metadata["channel_id"] = id
		meta(ev, "name", string(c.Name))
		meta(ev, "channel_type", string(c.Type))
		meta(ev, "channel_type_name", channelTypeName(string(c.Type)))
		meta(ev, "guild_id", string(c.Guild.ID))
		meta(ev, "guild_name", string(c.Guild.Name))
		if len(c.Recipients) > 0 {
			recipients := make([]string, len(c.Recipients))
			for i, r := range c.Recipients {
				recipients[i] = string(r)
			}
			ev.Metadata["recipients"] = recipients
		}
		return one(ev), nil

	case "guild":
		var g struct {
			ID   str `json:"id"`
			Name str `json:"name"`
		}
		if err := json.Unmarshal(raw.Payload, &g); err != nil {
			return unrecognized(ev, raw), nil
		}
		id := firstNonEmpty(string(g.ID), path.Base(path.Dir(raw.Member)))
		ev.Type, ev.Category = "discord.server", domain.CategoryConnection
		ev.ExternalID = "guild:" + id
		ev.Title = "Server " + firstNonEmpty(string(g.Name), id)
		ev.Metadata["guild_id"] = id
		meta(ev, "name", string(g.Name))
		var fields map[string]any
		if json.Unmarshal(raw.Payload, &fields) == nil {
			delete(fields, "id")
			delete(fields, "name")
			if len(fields) > 0 {
				ev.Metadata["fields"] = fields
			}
		}
		return one(ev), nil

	case "channel_label", "server":
		var e indexEntry
		if err := json.Unmarshal(raw.Payload, &e); err != nil {
			return unrecognized(ev, raw), nil
		}
		if raw.Kind == "server" {
			ev.Type, ev.Category = "discord.server", domain.CategoryConnection
			ev.ExternalID = "server:" + e.ID
			ev.Title = "Server " + firstNonEmpty(e.Name, e.ID)
			ev.Metadata["guild_id"] = e.ID
			meta(ev, "name", e.Name)
			return one(ev), nil
		}
		// messages/index.json is the only place a DM's counterpart is named, so
		// its labels are kept next to the channel.json events.
		ev.Type, ev.Category = "discord.channel", domain.CategoryConnection
		ev.ExternalID = "channel-label:" + e.ID
		ev.ThreadExternalID = e.ID
		ev.Title = firstNonEmpty(e.Name, "Channel "+e.ID)
		ev.Metadata["channel_id"] = e.ID
		meta(ev, "label", e.Name)
		return one(ev), nil

	case "relationship":
		var r struct {
			ID       str `json:"id"`
			Type     str `json:"type"`
			Nickname str `json:"nickname"`
			User     struct {
				ID            str `json:"id"`
				Username      str `json:"username"`
				GlobalName    str `json:"global_name"`
				Discriminator str `json:"discriminator"`
			} `json:"user"`
		}
		if err := json.Unmarshal(raw.Payload, &r); err != nil {
			return unrecognized(ev, raw), nil
		}
		id := firstNonEmpty(string(r.User.ID), string(r.ID))
		name := firstNonEmpty(string(r.User.GlobalName), string(r.User.Username), string(r.Nickname), id)
		relationship, title := relationshipOf(string(r.Type))
		ev.Type, ev.Category = "discord.friend", domain.CategoryConnection
		ev.ExternalID = "relationship:" + firstNonEmpty(id, raw.ContentHash[:16])
		ev.Title = title + name
		meta(ev, "relationship", relationship)
		meta(ev, "user_id", id)
		meta(ev, "username", string(r.User.Username))
		meta(ev, "global_name", string(r.User.GlobalName))
		meta(ev, "nickname", string(r.Nickname))
		if d := string(r.User.Discriminator); d != "" && d != "0" && d != "0000" {
			meta(ev, "discriminator", d)
		}
		return one(ev), nil

	case "unknown":
		var u unknownSection
		if err := json.Unmarshal(raw.Payload, &u); err != nil {
			return unrecognized(ev, raw), nil
		}
		ev.Type, ev.Category = "discord.unknown", domain.CategoryOther
		ev.ExternalID = "unknown:" + strings.ToLower(raw.Member) + "#" + u.Section
		ev.Title = "Unmapped export data: " + raw.Member + " (" + u.Section + ")"
		ev.Metadata["member"] = raw.Member
		ev.Metadata["section"] = u.Section
		meta(ev, "preview", u.Preview)
		meta(ev, "content_hash", u.Hash)
		if u.Bytes > 0 {
			ev.Metadata["bytes"] = u.Bytes
		}
		if len(u.Fields) > 0 {
			ev.Metadata["fields"] = u.Fields
		}
		return one(ev), nil

	case bundle.SkippedArrayKind:
		skipped, err := bundle.SkippedArrayEvent(sourceID, raw)
		return one(skipped), err
	}

	return unrecognized(ev, raw), nil
}

// unrecognized keeps a record of a shape the normalizer could not read.
func unrecognized(ev domain.Event, raw provider.RawRecord) []domain.Event {
	ev.Type, ev.Category = "discord.unknown", domain.CategoryOther
	ev.ExternalID = "unknown:" + raw.ContentHash
	ev.Title = "Unrecognized Discord record"
	ev.Metadata["kind"] = raw.Kind
	ev.Metadata["member"] = raw.Member
	preview := raw.Payload
	if len(preview) > 1024 {
		preview = preview[:1024]
	}
	ev.Metadata["preview"] = string(preview)
	return one(ev)
}

// channelTypeName names Discord's channel types (docs.discord.com, "Channel
// Types"); newer packages already write the name, older ones the number.
func channelTypeName(typ string) string {
	names := map[string]string{
		"0": "GUILD_TEXT", "1": "DM", "2": "GUILD_VOICE", "3": "GROUP_DM", "4": "GUILD_CATEGORY",
		"5": "GUILD_ANNOUNCEMENT", "10": "ANNOUNCEMENT_THREAD", "11": "PUBLIC_THREAD", "12": "PRIVATE_THREAD",
		"13": "GUILD_STAGE_VOICE", "14": "GUILD_DIRECTORY", "15": "GUILD_FORUM", "16": "GUILD_MEDIA",
	}
	if n, ok := names[typ]; ok {
		return n
	}
	if typ != "" && strings.Trim(typ, "0123456789") != "" {
		return strings.ToUpper(typ)
	}
	return ""
}

// relationshipOf names Discord's relationship types.
func relationshipOf(typ string) (relationship, titlePrefix string) {
	switch strings.ToUpper(typ) {
	case "1", "FRIEND":
		return "friend", "Friend "
	case "2", "BLOCKED":
		return "blocked", "Blocked "
	case "3", "PENDING_INCOMING":
		return "incoming_request", "Friend request from "
	case "4", "PENDING_OUTGOING":
		return "outgoing_request", "Friend request to "
	}
	return firstNonEmpty(typ, "unknown"), "Relationship with "
}

// ---- helpers ----

var timeLayouts = []string{
	"2006-01-02 15:04:05Z07:00", // "2024-01-31 12:34:56.789000+00:00"
	"2006-01-02 15:04:05",       // "2024-01-31 12:34:56" — UTC; fractions are accepted by time.Parse
	time.RFC3339,
	"2006-01-02T15:04:05",
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// snowflakeTime reads the creation time out of a Discord id: its upper bits are
// milliseconds since 2015-01-01.
func snowflakeTime(id string) time.Time {
	const discordEpochMS = 1420070400000
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n>>22 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(n>>22) + discordEpochMS).UTC()
}

// attachments reads the Attachments field: URLs separated by spaces (an array
// is accepted too).
func attachments(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.Fields(s)
	}
	var list []string
	_ = json.Unmarshal(raw, &list)
	return list
}

func one(ev domain.Event) []domain.Event { return []domain.Event{ev} }

func meta(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
