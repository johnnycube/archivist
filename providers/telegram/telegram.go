// Package telegram ingests a Telegram Desktop "Export Telegram data" export in
// its machine-readable JSON format: a folder (zipped by the user) holding one
// result.json next to the media directories (chats/chat_001/photos/…, files/,
// video_files/, voice_messages/, stickers/, …).
//
// result.json comes in two shapes — a full-account export with every chat under
// chats.list[] (and left_chats.list[]), and a single-chat export whose top-level
// object is the chat itself. It can be gigabytes large, so it is walked token by
// token: each message is emitted on its own, wrapped with the name/type/id of
// the chat it belongs to, which precede the "messages" array in the file.
//
// Messages, service messages and contacts are mapped to Events. Every other
// top-level section (personal_information, profile_pictures, stories,
// frequent_contacts, sessions, web_sessions, other_data, …) becomes one
// telegram.unknown event with a short preview.
package telegram

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

const sourceID = "telegram"

type Provider struct{}

func New() *Provider { return &Provider{} }

var (
	_ provider.BundleProvider    = (*Provider)(nil)
	_ provider.AttachmentMatcher = (*Provider)(nil)
)

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Telegram",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "telegram.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "telegram.service", Category: domain.CategoryOther},
			{Type: "telegram.contact", Category: domain.CategoryConnection},
			{Type: "telegram.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors: result.json is the only structured member. Media members are left
// unmatched so the importer stores them as blobs keyed by their member path.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{resultEx{}}
}

// attachmentDirs are the directories Telegram Desktop exports message files
// into, next to result.json (full export: below chats/chat_NNN/).
var attachmentDirs = map[string]bool{
	"photos": true, "files": true, "video_files": true, "voice_messages": true, "video_messages": true,
	"round_video_messages": true, "stickers": true,
}

// IsAttachment implements provider.AttachmentMatcher: documents, .tgs/.webp
// stickers and the like are no media by extension, but messages point at them
// (Media.BlobKey), so they are stored rather than captured as telegram.unknown.
func (p *Provider) IsAttachment(member string) bool {
	dirs := strings.Split(strings.ToLower(path.Dir(member)), "/")
	for _, d := range dirs {
		if attachmentDirs[d] {
			return true
		}
	}
	return false
}

// ---- extraction ----

type resultEx struct{}

func (resultEx) Match(member string) bool {
	return strings.EqualFold(path.Base(member), "result.json")
}

// chatCtx is what a message needs to know about the chat it was found in.
type chatCtx struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	Left bool   `json:"left,omitempty"` // listed under left_chats
}

func (resultEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	dec := bundle.NewRecordLimitedDecoder(r, 0)
	dec.UseNumber() // chat ids stay exact
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil // empty member
		}
		return jsonErr(err)
	}
	if tok != json.Delim('{') {
		return fmt.Errorf("decode json: expected an object, got %v", tok)
	}

	// The top-level object is either the account (chats, contacts, …) or, in a
	// single-chat export, the chat itself (name, type, id, messages).
	var chat chatCtx
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "name", "type", "id":
			err = readChatField(dec, key, &chat)
		case "messages":
			err = streamMessages(dec, withFallbackID(chat, "single"), emit)
		case "chats", "left_chats":
			err = walkChatList(dec, key, emit)
		case "contacts":
			err = walkList(dec, func() error {
				var el json.RawMessage
				if err := dec.Decode(&el); err != nil {
					return jsonErr(err)
				}
				return emit(rec("contact", el))
			})
		default:
			err = emitSection(dec, key, emit)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// walkChatList reads a {"about": …, "list": [chat, …]} section, streaming the
// messages of each chat.
func walkChatList(dec *bundle.RecordDecoder, section string, emit func(provider.RawRecord) error) error {
	n := 0
	return walkList(dec, func() error {
		n++
		tok, err := dec.Token()
		if err != nil {
			return jsonErr(err)
		}
		if tok != json.Delim('{') {
			return jsonpreview.SkipOpened(dec, tok)
		}
		chat := chatCtx{Left: section == "left_chats"}
		for dec.More() {
			key, err := readKey(dec)
			if err != nil {
				return err
			}
			switch key {
			case "name", "type", "id":
				err = readChatField(dec, key, &chat)
			case "messages":
				err = streamMessages(dec, withFallbackID(chat, section+"-"+strconv.Itoa(n)), emit)
			default:
				err = jsonpreview.Skip(dec)
			}
			if err != nil {
				return err
			}
		}
		_, err = dec.Token() // closing brace
		return jsonErr(err)
	})
}

// withFallbackID covers a chat whose id did not precede its messages (not the
// order Telegram writes): the chat name stays empty rather than being buffered
// for, and the position keeps message ExternalIDs of different chats apart.
func withFallbackID(chat chatCtx, position string) chatCtx {
	if chat.ID == "" {
		chat.ID = "noid-" + position
	}
	return chat
}

// walkList reads the value of a list section. Its usual shape is
// {"about": "…", "list": […]}; a bare array is accepted too. each is called
// with the decoder positioned at the start of every list element and must
// consume exactly that element.
func walkList(dec *bundle.RecordDecoder, each func() error) error {
	tok, err := dec.Token()
	if err != nil {
		return jsonErr(err)
	}
	elements := func() error {
		for dec.More() {
			if err := each(); err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing bracket
		return jsonErr(err)
	}
	switch tok {
	case json.Delim('['):
		return elements()
	case json.Delim('{'):
		for dec.More() {
			key, err := readKey(dec)
			if err != nil {
				return err
			}
			if key != "list" {
				if err := jsonpreview.Skip(dec); err != nil { // "about": export boilerplate
					return err
				}
				continue
			}
			open, err := dec.Token()
			if err != nil {
				return jsonErr(err)
			}
			if open != json.Delim('[') {
				err = jsonpreview.SkipOpened(dec, open)
			} else {
				err = elements()
			}
			if err != nil {
				return err
			}
		}
		_, err := dec.Token() // closing brace
		return jsonErr(err)
	}
	return nil // scalar or null: nothing listed
}

// streamMessages emits each element of a "messages" array together with the
// chat context collected so far.
func streamMessages(dec *bundle.RecordDecoder, chat chatCtx, emit func(provider.RawRecord) error) error {
	tok, err := dec.Token()
	if err != nil {
		return jsonErr(err)
	}
	if tok != json.Delim('[') {
		return jsonpreview.SkipOpened(dec, tok)
	}
	for dec.More() {
		var el json.RawMessage
		if err := dec.Decode(&el); err != nil {
			return jsonErr(err)
		}
		payload, err := json.Marshal(struct {
			Chat    chatCtx         `json:"chat"`
			Message json.RawMessage `json:"message"`
		}{chat, el})
		if err != nil {
			return err
		}
		if err := emit(rec("message", payload)); err != nil {
			return err
		}
	}
	_, err = dec.Token() // closing bracket
	return jsonErr(err)
}

func readChatField(dec *bundle.RecordDecoder, key string, chat *chatCtx) error {
	tok, err := dec.Token()
	if err != nil {
		return jsonErr(err)
	}
	var v string
	switch t := tok.(type) {
	case string:
		v = t
	case json.Number:
		v = t.String()
	case json.Delim:
		return jsonpreview.SkipOpened(dec, tok)
	}
	switch key {
	case "name":
		chat.Name = v
	case "type":
		chat.Type = v
	case "id":
		chat.ID = v
	}
	return nil
}

// emitSection turns one unmapped top-level value into an "unknown" record: the
// value is consumed token by token, hashed in full, and only a short preview
// (re-rendered as compact JSON) is kept.
func emitSection(dec *bundle.RecordDecoder, key string, emit func(provider.RawRecord) error) error {
	v, err := jsonpreview.Render(dec)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(unknownSection{Section: key, Preview: v.Preview(), Bytes: v.Size, Hash: v.Hash})
	if err != nil {
		return err
	}
	return emit(rec("unknown", payload))
}

type unknownSection struct {
	Section string `json:"section"`
	Preview string `json:"preview"`
	Bytes   int64  `json:"bytes"` // of the compact rendering
	Hash    string `json:"content_hash"`
}

func readKey(dec *bundle.RecordDecoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", jsonErr(err)
	}
	key, _ := tok.(string)
	return key, nil
}

func jsonErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("decode json: %w", err)
}

// rec hashes the kind into the dedupe key. Message payloads include their chat
// context, so the same message id and text in two chats stay two records.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	base := domain.Event{SourceID: sourceID, Metadata: map[string]any{}}
	switch raw.Kind {
	case "message":
		return normalizeMessage(base, raw)
	case "contact":
		return normalizeContact(base, raw)
	case "unknown":
		var u unknownSection
		if err := json.Unmarshal(raw.Payload, &u); err != nil {
			return nil, err
		}
		ev := base
		ev.Type, ev.Category = "telegram.unknown", domain.CategoryOther
		ev.ExternalID = "unknown:section:" + u.Section
		ev.Title = "Unmapped export data: " + u.Section
		ev.Metadata["member"] = raw.Member
		ev.Metadata["section"] = u.Section
		ev.Metadata["preview"] = u.Preview
		ev.Metadata["bytes"] = u.Bytes
		ev.Metadata["content_hash"] = u.Hash
		return one(ev), nil
	}
	return unknownRecord(base, raw), nil
}

// entity is one run of a formatted text: {"type":"link","text":"https://…"},
// {"type":"text_link","text":"here","href":"https://…"}, {"type":"plain",…}.
// Other types Telegram Desktop writes: bold, italic, underline, strikethrough,
// spoiler, code, pre (language), blockquote (collapsed), mention ("@name"),
// mention_name (user_id), hashtag, cashtag, bot_command, email, phone,
// bank_card, custom_emoji (document_id).
type entity struct {
	Type   str `json:"type"`
	Text   str `json:"text"`
	Href   str `json:"href"`
	UserID str `json:"user_id"`
}

// mentionsOf collects the accounts a text mentions, "@name" or by user id.
func mentionsOf(entities []entity) []string {
	var out []string
	for _, e := range entities {
		var who string
		switch e.Type {
		case "mention":
			who = string(e.Text)
		case "mention_name":
			who = firstNonEmpty("user"+string(e.UserID), string(e.Text))
			if e.UserID == "" {
				who = string(e.Text)
			}
		}
		if who != "" && !contains(out, who) {
			out = append(out, who)
		}
	}
	return out
}

// str is the permissive scalar: Telegram changed several fields between export
// versions (from_id was a number before it became "user123"; names are null for
// deleted accounts).
type str = jsonpreview.Str

type tgMessage struct {
	ID           str             `json:"id"`
	Type         str             `json:"type"`
	Date         str             `json:"date"`
	DateUnix     str             `json:"date_unixtime"`
	From         str             `json:"from"`
	FromID       str             `json:"from_id"`
	Text         json.RawMessage `json:"text"`
	TextEntities json.RawMessage `json:"text_entities"`
	ReplyTo      str             `json:"reply_to_message_id"`
	ReplyToPeer  str             `json:"reply_to_peer_id"` // the reply points into another chat
	Forwarded    json.RawMessage `json:"forwarded_from"`   // null for a deleted account
	SavedFrom    str             `json:"saved_from"`
	ViaBot       str             `json:"via_bot"`
	Photo        str             `json:"photo"`
	File         str             `json:"file"`
	FileName     str             `json:"file_name"`
	Thumbnail    str             `json:"thumbnail"`
	MediaType    str             `json:"media_type"` // sticker, video_message, voice_message, animation, video_file, audio_file
	MIME         str             `json:"mime_type"`
	StickerEmoji str             `json:"sticker_emoji"`
	Duration     str             `json:"duration_seconds"`
	Edited       str             `json:"edited"`
	EditedUnix   str             `json:"edited_unixtime"`
	SelfDestruct str             `json:"self_destruct_period_seconds"`
	Reactions    json.RawMessage `json:"reactions"` // [{type, count, emoji | document_id, recent: […]}]

	// service messages
	Action  str   `json:"action"`
	Actor   str   `json:"actor"`
	ActorID str   `json:"actor_id"`
	Title   str   `json:"title"`
	Members []str `json:"members"`
	Pinned  str   `json:"message_id"`

	// structured attachments, kept verbatim
	Location json.RawMessage `json:"location_information"` // {latitude, longitude, place_name?, address?}
	Contact  json.RawMessage `json:"contact_information"`  // {first_name, last_name, phone_number, user_id?}
	Poll     json.RawMessage `json:"poll"`                 // {question, closed, total_voters, answers: [{text, voters, chosen}]}
}

// serviceStdKeys are the service-message keys read into tgMessage; every other
// key (phone_call's duration_seconds and discard_reason, score_in_game's
// score, proximity_reached's distance, boost_apply's boosts, gift codes, new
// titles/photos, …) is copied into the metadata as is, so no action loses its
// details.
var serviceStdKeys = map[string]bool{
	"id": true, "type": true, "date": true, "date_unixtime": true, "actor": true, "actor_id": true,
	"action": true, "text": true, "text_entities": true, "title": true, "members": true, "message_id": true,
	"from": true, "from_id": true,
}

func normalizeMessage(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var w struct {
		Chat    chatCtx   `json:"chat"`
		Message tgMessage `json:"message"`
	}
	if err := json.Unmarshal(raw.Payload, &w); err != nil {
		return unknownRecord(base, raw), nil
	}
	m, chat := w.Message, w.Chat

	ev := base
	ev.ExternalID = msgID(chat.ID, string(m.ID))
	ev.ThreadExternalID = chat.ID
	ev.OccurredAt = messageTime(ev, string(m.DateUnix), string(m.Date))
	ev.Title = "Message in " + chatLabel(chat)
	meta(ev, "chat", chat.Name)
	meta(ev, "chat_type", chat.Type)
	if chat.Left {
		ev.Metadata["left_chat"] = true
	}

	var entities []entity
	_ = json.Unmarshal(m.TextEntities, &entities) // optional; absent in older exports
	text, links := flattenText(m.Text)
	links = appendLinks(links, entities)
	mentions := mentionsOf(entities)
	if text == "" { // the text may be given as entities only
		for _, e := range entities {
			text += string(e.Text)
		}
	}

	if m.Type == "service" {
		ev.Type, ev.Category = "telegram.service", domain.CategoryOther
		ev.Actor = firstNonEmpty(string(m.Actor), string(m.ActorID), string(m.From), string(m.FromID), "unknown")
		ev.Title = firstNonEmpty(string(m.Action), "service") + " in " + chatLabel(chat)
		ev.Body = text
		meta(ev, "action", string(m.Action))
		meta(ev, "actor", string(m.Actor))
		meta(ev, "actor_id", string(m.ActorID))
		meta(ev, "title", string(m.Title))
		if len(m.Members) > 0 {
			members := make([]string, 0, len(m.Members))
			for _, mem := range m.Members {
				members = append(members, firstNonEmpty(string(mem), "unknown")) // null = deleted account
			}
			ev.Metadata["members"] = members
		}
		if m.Pinned != "" {
			meta(ev, "message_external_id", msgID(chat.ID, string(m.Pinned)))
		}
		var all struct {
			Message map[string]json.RawMessage `json:"message"`
		}
		if json.Unmarshal(raw.Payload, &all) == nil {
			for k, v := range all.Message {
				var decoded any
				if !serviceStdKeys[k] && json.Unmarshal(v, &decoded) == nil && decoded != nil {
					ev.Metadata[k] = decoded
				}
			}
		}
		return one(ev), nil
	}

	ev.Type, ev.Category = "telegram.message", domain.CategoryMessage
	ev.Actor = firstNonEmpty(string(m.From), string(m.FromID), "unknown") // from is null for deleted accounts
	ev.Body = text
	if m.ReplyTo != "" {
		ev.ParentExternalID = msgID(firstNonEmpty(string(m.ReplyToPeer), chat.ID), string(m.ReplyTo))
	}
	meta(ev, "from_id", string(m.FromID))
	if len(links) > 0 {
		ev.Metadata["links"] = links
	}
	if len(m.Forwarded) > 0 {
		var from str
		_ = from.UnmarshalJSON(m.Forwarded)
		ev.Metadata["forwarded"] = true
		meta(ev, "forwarded_from", string(from))
	}
	meta(ev, "saved_from", string(m.SavedFrom))
	meta(ev, "via_bot", string(m.ViaBot))
	meta(ev, "media_type", string(m.MediaType))
	meta(ev, "sticker_emoji", string(m.StickerEmoji))
	meta(ev, "file_name", string(m.FileName))
	meta(ev, "duration_seconds", string(m.Duration))
	if t := messageTime(domain.Event{}, string(m.EditedUnix), string(m.Edited)); !t.IsZero() {
		meta(ev, "edited_at", t.Format(time.RFC3339))
	}
	meta(ev, "self_destruct_period_seconds", string(m.SelfDestruct))
	for k, v := range map[string]json.RawMessage{"location": m.Location, "contact": m.Contact, "poll": m.Poll, "reactions": m.Reactions} {
		var decoded any
		if len(v) > 0 && json.Unmarshal(v, &decoded) == nil && decoded != nil {
			ev.Metadata[k] = decoded
		}
	}
	if len(mentions) > 0 {
		ev.Metadata["mentions"] = mentions
	}
	if ev.Body == "" {
		ev.Body = attachmentSummary(m)
	}

	dir := path.Dir(raw.Member)
	for _, a := range []struct{ ref, mime string }{{string(m.Photo), ""}, {string(m.File), string(m.MIME)}} {
		if a.ref == "" {
			continue
		}
		if key, ok := blobKey(dir, a.ref); ok {
			ev.Media = append(ev.Media, domain.Media{BlobKey: key, MIME: a.mime})
			continue
		}
		// "(File not included. Change data exporting settings to download.)"
		ev.Metadata["media_not_included"] = true
		meta(ev, "media_note", a.ref)
		meta(ev, "mime_type", a.mime)
	}
	if key, ok := blobKey(dir, string(m.Thumbnail)); ok {
		meta(ev, "thumbnail", key)
	}
	return one(ev), nil
}

// attachmentSummary gives a poll, a location or a shared contact a readable
// body, since such messages carry no text of their own.
func attachmentSummary(m tgMessage) string {
	if len(m.Poll) > 0 {
		var p struct {
			Question str `json:"question"`
			Answers  []struct {
				Text str `json:"text"`
			} `json:"answers"`
		}
		if json.Unmarshal(m.Poll, &p) == nil && p.Question != "" {
			var b strings.Builder
			b.WriteString("Poll: " + string(p.Question))
			for _, a := range p.Answers {
				b.WriteString("\n- " + string(a.Text))
			}
			return b.String()
		}
	}
	if len(m.Location) > 0 {
		var l struct {
			Lat, Lon  json.Number `json:"-"`
			Latitude  json.Number `json:"latitude"`
			Longitude json.Number `json:"longitude"`
			Place     str         `json:"place_name"`
			Address   str         `json:"address"`
		}
		if json.Unmarshal(m.Location, &l) == nil {
			where := strings.TrimSpace(strings.Join([]string{string(l.Place), string(l.Address)}, " "))
			if where == "" && l.Latitude != "" {
				where = l.Latitude.String() + ", " + l.Longitude.String()
			}
			if where != "" {
				return "Location: " + where
			}
		}
	}
	if len(m.Contact) > 0 {
		var c struct {
			First str `json:"first_name"`
			Last  str `json:"last_name"`
			Phone str `json:"phone_number"`
		}
		if json.Unmarshal(m.Contact, &c) == nil {
			who := strings.TrimSpace(string(c.First) + " " + string(c.Last))
			if s := strings.TrimSpace(who + " " + string(c.Phone)); s != "" {
				return "Contact: " + s
			}
		}
	}
	return ""
}

// unknownRecord keeps a record the normalizer could not read.
func unknownRecord(base domain.Event, raw provider.RawRecord) []domain.Event {
	ev := base
	ev.Type, ev.Category = "telegram.unknown", domain.CategoryOther
	ev.ExternalID = "unknown:" + raw.ContentHash
	ev.Title = "Unrecognized Telegram record"
	preview := raw.Payload
	if len(preview) > 1024 {
		preview = preview[:1024]
	}
	ev.Metadata["kind"] = raw.Kind
	ev.Metadata["preview"] = string(preview)
	return one(ev)
}

func normalizeContact(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var c struct {
		FirstName str `json:"first_name"`
		LastName  str `json:"last_name"`
		Phone     str `json:"phone_number"`
		Date      str `json:"date"`
		DateUnix  str `json:"date_unixtime"`
	}
	if err := json.Unmarshal(raw.Payload, &c); err != nil {
		return unknownRecord(base, raw), nil
	}
	name := strings.TrimSpace(string(c.FirstName + " " + c.LastName))
	phone := string(c.Phone)
	ev := base
	ev.Type, ev.Category = "telegram.contact", domain.CategoryConnection
	ev.Actor = "self"
	ev.OccurredAt = messageTime(ev, string(c.DateUnix), string(c.Date))
	ev.Title = "Contact " + firstNonEmpty(name, phone, "without a name")
	sum := sha256.Sum256([]byte(name + "|" + phone))
	ev.ExternalID = "contact:" + hex.EncodeToString(sum[:8])
	meta(ev, "name", name)
	meta(ev, "phone_number", phone)
	return one(ev), nil
}

// ---- helpers ----

func msgID(chatID, messageID string) string { return "msg:" + chatID + ":" + messageID }

func chatLabel(chat chatCtx) string {
	switch {
	case chat.Name != "":
		return chat.Name
	case chat.Type == "saved_messages":
		return "Saved Messages"
	case chat.Type == "replies":
		return "Replies"
	}
	return "chat " + chat.ID
}

// messageTime prefers date_unixtime, which is true UTC. The "date" string is
// the exporting machine's local time without a zone; it is only a fallback
// (older exports) and is then read as if it were UTC and flagged.
func messageTime(ev domain.Event, unix, local string) time.Time {
	if s, err := strconv.ParseInt(unix, 10, 64); err == nil && s > 0 {
		return time.Unix(s, 0).UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05", local); err == nil && t.Year() > 1970 {
		if ev.Metadata != nil {
			ev.Metadata["date_is_local"] = true
		}
		return t.UTC()
	}
	return time.Time{}
}

// flattenText renders Telegram's "text" — a string, or an array mixing strings
// with {"type","text"} runs — as plain text, and collects the links in it.
func flattenText(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return "", nil
	}
	var b strings.Builder
	var entities []entity
	for _, part := range parts {
		var e entity
		if json.Unmarshal(part, &s) == nil {
			b.WriteString(s)
		} else if json.Unmarshal(part, &e) == nil {
			b.WriteString(string(e.Text))
			entities = append(entities, e)
		}
	}
	return b.String(), appendLinks(nil, entities)
}

func appendLinks(links []string, entities []entity) []string {
	for _, e := range entities {
		var link string
		switch e.Type {
		case "link":
			link = string(e.Text)
		case "text_link":
			link = string(e.Href)
		}
		if link != "" && !contains(links, link) {
			links = append(links, link)
		}
	}
	return links
}

// blobKey resolves a media reference, which is relative to result.json, to the
// member path the importer stored the file under. Instead of a path Telegram
// writes a parenthesized note when the file was not exported.
func blobKey(dir, ref string) (string, bool) {
	if ref == "" || strings.HasPrefix(ref, "(") || strings.HasPrefix(ref, "/") || strings.Contains(ref, "://") {
		return "", false
	}
	key := path.Join(dir, strings.ReplaceAll(ref, `\`, "/"))
	if key == ".." || strings.HasPrefix(key, "../") {
		return "", false
	}
	return key, true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
