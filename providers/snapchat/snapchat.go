// Package snapchat ingests Snapchat's "My Data" export: a ZIP with a json/
// folder (and an html/ twin of the same data, which is left to the importer's
// generic capture). Chats, snaps, memories, friends, searches, logins, calls
// (talk_history), stories and subscriptions are mapped to Events.
//
// Evidence for the shapes: the community parsers of the export
// (DF-OMNIUS/Snapchat-Chat-History-Location-parser, fekuxle/snapchat-log-parser,
// verdie-g/snap-data-analyzer, BitwiseSang/SnapVault, Tikolu/SnapchatExportTools)
// document From/To, "Media Type" (TEXT, MEDIA, STICKER, SHARE, NOTE, LOCATION;
// snaps IMAGE/VIDEO), "Created" as "2022-07-22 19:28:21 UTC", the legacy
// "Received Saved Chat History" / "Sent Saved Chat History" and "Received Snap
// History" / "Sent Snap History" arrays, memories' "Saved Media" with Date /
// Media Type / Download Link (or "Media Download Url"), and that talk_history
// holds calls with timestamps, duration and type but no user name. The exact
// keys of talk_history.json, story_history.json and subscriptions.json are not
// documented anywhere public, so those are read permissively and every field
// is kept in the metadata.
//
// Chat and snap histories come in two shapes — one array per conversation, keyed
// by the conversation's id or username, or the older "Received … History" /
// "Sent … History" pair — so those member names cannot be listed up front and
// the files are streamed with a path-tracking walker. Field names vary between
// export versions and are read permissively. Whatever a matched file holds
// besides the mapped arrays becomes snapchat.unknown; nothing is dropped.
package snapchat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/internal/jsonwalk"
)

const sourceID = "snapchat"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Snapchat",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "snapchat.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "snapchat.snap", Category: domain.CategoryMessage},
			{Type: "snapchat.memory", Category: domain.CategoryMedia},
			{Type: "snapchat.friend", Category: domain.CategoryConnection},
			{Type: "snapchat.friend_request", Category: domain.CategoryConnection},
			{Type: "snapchat.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "snapchat.login", Category: domain.CategoryOther},
			{Type: "snapchat.call", Category: domain.CategoryMessage},
			{Type: "snapchat.story", Category: domain.CategoryContent},
			{Type: "snapchat.subscription", Category: domain.CategoryConnection},
			{Type: "snapchat.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors maps the known json/ members to record kinds. Unlisted members —
// all of html/ included — are captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		ex{match: baseIs("chat_history.json"), each: "message"},
		ex{match: baseIs("snap_history.json"), each: "snap"},
		ex{match: baseIs("memories_history.json"), kinds: map[string]string{"saved media": "memory"}},
		ex{match: baseIs("friends.json"), kinds: map[string]string{
			"friends":              "friend",
			"friend requests sent": "friend_request",
			"blocked users":        "friend_blocked",
			"deleted friends":      "friend_deleted",
		}},
		ex{match: baseIs("search_history.json"), kinds: map[string]string{"": "search", "search history": "search"}},
		ex{match: baseIs("account.json", "account_history.json", "login_history.json"), kinds: map[string]string{"login history": "login"}},
		ex{match: baseIs("talk_history.json"), each: "call"},
		ex{match: baseIs("story_history.json"), each: "story"},
		ex{match: baseIs("subscriptions.json"), each: "subscription"},
	}
}

func baseIs(names ...string) func(string) bool {
	return func(member string) bool {
		b := path.Base(member)
		for _, n := range names {
			if strings.EqualFold(b, n) {
				return true
			}
		}
		return false
	}
}

// ---- extraction ----

// ex streams one member. Top-level arrays are record lists: their kind is each
// (every array, whatever its name — the per-conversation files) or is looked up
// by lower-cased name in kinds ("" is a document that is itself an array).
// Everything else in the member is emitted as an unknown record.
type ex struct {
	match func(member string) bool
	each  string
	kinds map[string]string
}

func (e ex) Match(member string) bool { return e.match(member) }

// keyed is a record together with the name of the array it was listed in, which
// for chats and snaps is the only place the conversation or direction is given.
type keyed struct {
	Key    string          `json:"key"`
	Record json.RawMessage `json:"record"`
}

type unknownRecord struct {
	Path  []string        `json:"path"`
	Value json.RawMessage `json:"value"`
}

func (e ex) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	file := path.Base(member)
	unknown := func(p []string, value json.RawMessage) error {
		payload, err := json.Marshal(unknownRecord{Path: append([]string{file}, p...), Value: value})
		if err != nil {
			return err
		}
		return emit(rec("unknown", payload))
	}
	return jsonwalk.Walk(r, jsonwalk.Handler{
		Element: func(p []string, el json.RawMessage) error {
			kind, key := "", ""
			if len(p) <= 1 {
				if len(p) == 1 {
					key = p[0]
				}
				if kind = e.each; kind == "" {
					kind = e.kinds[strings.ToLower(strings.TrimSpace(key))]
				}
			}
			if kind == "" || !isObject(el) {
				return unknown(p, el)
			}
			payload, err := json.Marshal(keyed{Key: key, Record: el})
			if err != nil {
				return err
			}
			return emit(rec(kind, payload))
		},
		Fields: unknown,
	})
}

func isObject(el json.RawMessage) bool {
	t := bytes.TrimLeft(el, " \t\r\n")
	return len(t) > 0 && t[0] == '{'
}

// rec hashes the kind into the dedupe key, so equal bare records of different
// meaning (a friend that is also listed as blocked) stay apart.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}
	if raw.Kind == "unknown" {
		return normalizeUnknown(base, raw)
	}

	var k keyed
	if err := json.Unmarshal(raw.Payload, &k); err != nil {
		return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: raw.Payload})
	}
	f, err := decodeFields(k.Record)
	if err != nil {
		return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: raw.Payload})
	}

	switch raw.Kind {
	case "message", "snap":
		return normalizeChat(base, raw.Kind, k.Key, f), nil

	case "memory":
		at, ts := parseDate(f.str("Date", "Created", "Timestamp"))
		mediaType := f.str("Media Type", "MediaType")
		link := f.str("Download Link", "Media Download Url", "Media Download URL")
		ev := base
		ev.Type, ev.Category = "snapchat.memory", domain.CategoryMedia
		ev.OccurredAt = at
		ev.Title = "Saved a memory"
		if mediaType != "" {
			ev.Title += " (" + strings.ToLower(mediaType) + ")"
		}
		// The link is signed and expires, so identity comes from the media id in
		// it; only a link without one has to stand in for itself.
		ev.ExternalID = id("memory", ts, mediaType+"|"+firstNonEmpty(memoryID(link), link))
		meta(ev, "media_type", mediaType)
		meta(ev, "location", f.str("Location"))
		meta(ev, "download_link", link)
		return one(ev), nil

	case "friend", "friend_request", "friend_blocked", "friend_deleted":
		user := f.str("Username", "User Name")
		display := f.str("Display Name", "DisplayName")
		name := firstNonEmpty(display, user)
		created, modified := f.str("Creation Timestamp", "Created"), f.str("Last Modified Timestamp", "Last Modified")
		at, ts := parseDate(created)

		ev := base
		ev.Type, ev.Category = "snapchat.friend", domain.CategoryConnection
		switch raw.Kind {
		case "friend":
			ev.Title = "Added " + name + " as a friend"
			ev.Metadata["status"] = "friend"
			ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: firstNonEmpty(user, display)}}
		case "friend_request":
			ev.Type = "snapchat.friend_request"
			ev.Title = "Sent a friend request to " + name
			ev.Metadata["direction"] = "sent"
		case "friend_blocked", "friend_deleted":
			status := strings.TrimPrefix(raw.Kind, "friend_")
			ev.Title = map[string]string{"blocked": "Blocked ", "deleted": "Removed the friend "}[status] + name
			ev.Metadata["status"] = status
			// The change itself is what happened; creation is when they were added.
			if mAt, mTs := parseDate(modified); !mAt.IsZero() {
				at, ts = mAt, mTs
			}
		}
		ev.OccurredAt = at
		ev.ExternalID = id(raw.Kind, ts, firstNonEmpty(user, display))
		meta(ev, "username", user)
		meta(ev, "display_name", display)
		meta(ev, "created", created)
		meta(ev, "last_modified", modified)
		meta(ev, "added_via", f.str("Source"))
		return one(ev), nil

	case "search":
		at, ts := parseDate(f.str("Date", "Date and time (hourly)", "Date and time", "Created", "Timestamp"))
		ev := base
		ev.Type, ev.Category = "snapchat.search", domain.CategoryConsumption
		ev.OccurredAt = at
		ev.Title = "Searched Snapchat"
		ev.Body = f.str("Search Term", "SearchTerm", "Search Query", "Query", "Term")
		ev.ExternalID = id("search", ts, ev.Body)
		return one(ev), nil

	case "login":
		at, ts := parseDate(f.str("Created", "Date", "Timestamp"))
		ip, device, status := f.str("IP", "IP Address"), f.str("Device", "Device Model"), f.str("Status")
		ev := base
		ev.Type, ev.Category = "snapchat.login", domain.CategoryOther
		ev.OccurredAt = at
		ev.Title = "Logged in to Snapchat"
		if status != "" && !strings.EqualFold(status, "success") {
			ev.Title = "Snapchat login: " + status
		}
		ev.ExternalID = id("login", ts, ip+"|"+device+"|"+status)
		meta(ev, "ip", ip)
		meta(ev, "country", f.str("Country"))
		meta(ev, "status", status)
		meta(ev, "device", device)
		return one(ev), nil

	case "call":
		at, ts := parseDate(f.str("Created", "Date", "Start Time", "Timestamp", "Start"))
		if at.IsZero() {
			if us, err := strconv.ParseInt(f.str("Created(microseconds)", "Created (microseconds)"), 10, 64); err == nil && us > 0 {
				at = time.UnixMicro(us).UTC()
				ts = at.Unix()
			}
		}
		callType := f.str("Call Type", "Media Type", "Type", "Talk Type")
		duration := f.str("Duration", "Call Duration", "Duration (seconds)", "Length")
		from, to := f.str("From"), f.str("To")
		sent := f.truthy("IsSender") || strings.EqualFold(f.str("Direction"), "outgoing") || strings.EqualFold(f.str("Direction"), "sent")
		with := firstNonEmpty(f.str("Conversation Title"), strings.TrimSpace(strings.Trim(k.Key, " ")), to, from)
		ev := base
		ev.Type, ev.Category = "snapchat.call", domain.CategoryMessage
		ev.OccurredAt = at
		ev.ThreadExternalID = firstNonEmpty(k.Key, with)
		ev.Title = "Call"
		if callType != "" {
			ev.Title = strings.ToUpper(callType[:1]) + strings.ToLower(callType[1:]) + " call"
		}
		if with != "" && !isLegacyKey(k.Key) {
			ev.Title += " with " + with
		}
		if !sent {
			ev.Actor = firstNonEmpty(from, "self")
		}
		direction := "received"
		if sent {
			direction = "sent"
		}
		ev.ExternalID = id("call", ts, strings.Join([]string{k.Key, direction, callType, duration, from, to, f.str("Created(microseconds)")}, "|"))
		meta(ev, "direction", direction)
		meta(ev, "call_type", callType)
		meta(ev, "duration", duration)
		f.rest(ev, "Created", "Date", "Start Time", "Timestamp", "Start", "Call Type", "Media Type", "Type", "Talk Type", "Duration", "Call Duration", "Duration (seconds)", "Length", "IsSender", "Direction")
		return one(ev), nil

	case "story":
		at, ts := parseDate(f.str("Date", "Created", "Timestamp", "Posted"))
		mediaType := f.str("Media Type", "MediaType", "Type")
		link := f.str("Download Link", "Media Download Url", "Media Download URL")
		ev := base
		ev.Type, ev.Category = "snapchat.story", domain.CategoryContent
		ev.OccurredAt = at
		ev.Title = "Snapchat story"
		if mediaType != "" {
			ev.Title += " (" + strings.ToLower(mediaType) + ")"
		}
		ev.Body = f.str("Caption", "Text", "Content")
		ev.ExternalID = id("story", ts, strings.Join([]string{k.Key, mediaType, firstNonEmpty(memoryID(link), link), ev.Body}, "|"))
		meta(ev, "list", k.Key)
		meta(ev, "media_type", mediaType)
		meta(ev, "download_link", link)
		f.rest(ev, "Date", "Created", "Timestamp", "Posted", "Media Type", "MediaType", "Type", "Download Link", "Media Download Url", "Media Download URL", "Caption", "Text", "Content")
		return one(ev), nil

	case "subscription":
		name := firstNonEmpty(f.str("Display Name", "Name", "Title", "Publisher", "Channel"), f.str("Username", "User Name"))
		at, ts := parseDate(f.str("Created", "Date", "Timestamp", "Subscribed", "Subscription Date"))
		ev := base
		ev.Type, ev.Category = "snapchat.subscription", domain.CategoryConnection
		ev.OccurredAt = at
		ev.Title = "Subscribed to " + firstNonEmpty(name, "an account")
		ev.ExternalID = id("subscription", ts, k.Key+"|"+name+"|"+f.str("Username", "User Name"))
		if name != "" {
			ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: firstNonEmpty(f.str("Username", "User Name"), name)}}
		}
		meta(ev, "list", k.Key)
		meta(ev, "username", f.str("Username", "User Name"))
		f.rest(ev, "Created", "Date", "Timestamp", "Subscribed", "Subscription Date", "Display Name", "Name", "Title", "Publisher", "Channel", "Username", "User Name")
		return one(ev), nil
	}

	return normalizeUnknown(base, provider.RawRecord{Kind: "unknown", Payload: raw.Payload})
}

// isLegacyKey reports the "Received … History" / "Sent … History" array names
// of older exports, which name a direction rather than a conversation.
func isLegacyKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return strings.HasSuffix(lower, " history") && (strings.HasPrefix(lower, "sent ") || strings.HasPrefix(lower, "received "))
}

// rest copies every scalar field not named in used into the metadata, under
// its lower-cased name, so a record of a shape only partly known loses nothing.
func (f fields) rest(ev domain.Event, used ...string) {
	skip := map[string]bool{}
	for _, u := range used {
		skip[strings.ToLower(u)] = true
	}
	for k, v := range f {
		if skip[k] {
			continue
		}
		switch v := v.(type) {
		case string:
			if v != "" {
				ev.Metadata[k] = v
			}
		case json.Number, bool:
			ev.Metadata[k] = v
		}
	}
}

// normalizeChat maps a chat message or a snap. In the per-conversation shape the
// array name is the conversation and IsSender gives the direction; in the older
// shape the array name gives the direction and From/To the other party.
func normalizeChat(base domain.Event, kind, key string, f fields) []domain.Event {
	from, to := f.str("From"), f.str("To")
	lower := strings.ToLower(strings.TrimSpace(key))
	legacy := isLegacyKey(key)

	sent := f.truthy("IsSender")
	thread := key
	if legacy {
		sent = strings.HasPrefix(lower, "sent ")
		thread = from
		if sent {
			thread = firstNonEmpty(to, from)
		}
	}
	created := f.str("Created", "Date", "Timestamp")
	at, ts := parseDate(created)
	micros := f.str("Created(microseconds)", "Created (microseconds)")
	if at.IsZero() {
		if us, err := strconv.ParseInt(micros, 10, 64); err == nil && us > 0 {
			at = time.UnixMicro(us).UTC()
			ts = at.Unix()
		}
	}
	mediaType := f.str("Media Type", "MediaType")
	convTitle := f.str("Conversation Title")
	with := firstNonEmpty(convTitle, thread, "unknown")

	ev := base
	ev.OccurredAt = at
	ev.ThreadExternalID = thread
	if !sent {
		ev.Actor = firstNonEmpty(from, "self")
	}
	if kind == "snap" {
		ev.Type, ev.Category = "snapchat.snap", domain.CategoryMessage
		ev.Title = "Snap from " + with
		if sent {
			ev.Title = "Snap to " + with
		}
	} else {
		ev.Type, ev.Category = "snapchat.message", domain.CategoryMessage
		ev.Title = "Chat with " + with
		ev.Body = f.str("Content", "Text")
	}
	direction := "received"
	if sent {
		direction = "sent"
	}
	ev.ExternalID = id(kind, ts, strings.Join([]string{thread, direction, from, ev.Body, mediaType, micros, f.str("Media IDs")}, "|"))
	meta(ev, "direction", direction)
	meta(ev, "media_type", mediaType)
	meta(ev, "conversation_title", convTitle)
	meta(ev, "from", from)
	meta(ev, "to", to)
	meta(ev, "media_ids", f.str("Media IDs"))
	if f.truthy("IsSaved") {
		ev.Metadata["saved"] = true
	}
	return one(ev)
}

const previewRunes = 240

func normalizeUnknown(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var u unknownRecord
	if err := json.Unmarshal(raw.Payload, &u); err != nil {
		u = unknownRecord{Value: raw.Payload} // not our own envelope: keep the bytes
	}
	if u.Path == nil && u.Value == nil { // a keyed record of an unhandled kind
		u.Value = raw.Payload
	}
	section := strings.Join(u.Path, " / ")
	sum := sha256.Sum256(raw.Payload)

	ev := base
	ev.Type, ev.Category = "snapchat.unknown", domain.CategoryOther
	ev.Title = "Unmapped Snapchat data: " + firstNonEmpty(section, "(unrecognized record)")
	ev.ExternalID = "unknown:" + hex.EncodeToString(sum[:8])
	if f, err := decodeFields(u.Value); err == nil {
		ev.OccurredAt, _ = parseDate(f.str("Date", "Created", "Timestamp"))
	}
	meta(ev, "section", section)
	meta(ev, "preview", preview(u.Value))
	ev.Metadata["bytes"] = len(u.Value)
	return one(ev), nil
}

// ---- helpers ----

// fields is one export record read permissively: member names are matched
// case-insensitively and values of any scalar type are read as text.
type fields map[string]any

func decodeFields(b []byte) (fields, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep Created(microseconds) exact
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	f := make(fields, len(m))
	for k, v := range m {
		f[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return f, nil
}

func (f fields) str(names ...string) string {
	for _, n := range names {
		switch v := f[strings.ToLower(n)].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case json.Number:
			return v.String()
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ""
}

func (f fields) truthy(name string) bool {
	switch v := f[strings.ToLower(name)].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

// parseDate reads Snapchat's "2024-01-31 12:34:56 UTC".
func parseDate(s string) (time.Time, int64) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05", time.RFC3339, "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			t = t.UTC()
			return t, t.Unix()
		}
	}
	return time.Time{}, 0
}

// memoryID picks the media id out of a memories download link
// (…/memories?uid=…&sid=…&mid=…&sig=…); the signature around it changes with
// every export.
func memoryID(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	q := u.Query()
	return firstNonEmpty(q.Get("mid"), q.Get("sid"))
}

func preview(b []byte) string {
	s := string(b)
	if utf8.RuneCountInString(s) <= previewRunes {
		return s
	}
	return string([]rune(s)[:previewRunes]) + "…"
}

func id(kind string, ts int64, disc string) string {
	sum := sha256.Sum256([]byte(disc))
	return kind + ":" + strconv.FormatInt(ts, 10) + ":" + hex.EncodeToString(sum[:4])
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
