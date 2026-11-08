package meta

import (
	"encoding/json"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// MessageKind is the RawRecord kind MessageExtractor emits.
const MessageKind = "message"

// Media is an attachment as Meta exports write it.
type Media struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Created     int64  `json:"creation_timestamp"`
}

// MessageExtractor reads one Messenger / Instagram Direct thread file
// (messages/<folder>/<thread>/message_N.json). A message alone does not say who
// it was sent to, so each is emitted together with the thread's participants,
// which precede the messages in the file.
type MessageExtractor struct{}

func (MessageExtractor) Match(member string) bool {
	return InDir(member, "messages") && BasePrefix("message_")(member)
}

func (MessageExtractor) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	var participants []string
	return bundle.StreamJSONArraysReport(r, []string{"participants", "messages"}, func(key string, el json.RawMessage) error {
		if key == "participants" {
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(el, &p); err == nil { // anything else is not a participant
				participants = append(participants, p.Name)
			}
			return nil
		}
		if key != "messages" {
			return nil
		}
		payload, err := json.Marshal(struct {
			Participants []string        `json:"participants"`
			Message      json.RawMessage `json:"message"`
		}{participants, el})
		if err != nil {
			return err
		}
		return emit(Rec(MessageKind, payload))
	}, bundle.ReportSkipped(member, emit))
}

// Messages normalizes the records of MessageExtractor.
type Messages struct {
	EventType string // e.g. "facebook.message"
	// Extras keeps reactions and the owner of shared content as metadata.
	Extras bool
}

// Normalize maps one message; base supplies the source and the metadata map,
// raw.Member the thread.
func (o Messages) Normalize(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var w struct {
		Participants []string `json:"participants"`
		Message      struct {
			Sender      string  `json:"sender_name"`
			TimestampMS int64   `json:"timestamp_ms"`
			Content     string  `json:"content"`
			Photos      []Media `json:"photos"`
			Videos      []Media `json:"videos"`
			Gifs        []Media `json:"gifs"`
			Files       []Media `json:"files"`
			Audio       []Media `json:"audio_files"`
			Sticker     *Media  `json:"sticker"`
			Share       struct {
				Link  string `json:"link"`
				Text  string `json:"share_text"`
				Owner string `json:"original_content_owner"`
			} `json:"share"`
			Reactions []struct {
				Reaction string `json:"reaction"`
				Actor    string `json:"actor"`
			} `json:"reactions"`
			Unsent       bool  `json:"is_unsent"`
			CallDuration int64 `json:"call_duration"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw.Payload, &w); err != nil {
		// A record of another shape (the exporter's, or a corrupt file): keep it
		// visible rather than failing the whole import.
		return One(Unknown(base.SourceID, raw)), nil
	}
	m := w.Message
	thread := ThreadOf(raw.Member)

	ev := base
	ev.Type, ev.Category = o.EventType, domain.CategoryMessage
	ev.Actor = FirstNonEmpty(bundle.FixMetaMojibake(m.Sender), "self")
	if m.TimestampMS > 0 {
		ev.OccurredAt = time.UnixMilli(m.TimestampMS).UTC()
	}
	ev.ThreadExternalID = thread
	ev.Title = "Message in " + FirstNonEmpty(ThreadLabel(w.Participants), thread)

	attachments := [][]Media{m.Photos, m.Videos, m.Gifs, m.Files, m.Audio}
	if m.Sticker != nil {
		attachments = append(attachments, []Media{*m.Sticker})
	}
	for _, group := range attachments {
		for _, a := range group {
			if a.URI != "" {
				ev.Media = append(ev.Media, domain.Media{BlobKey: a.URI})
			}
		}
	}
	ev.Body = bundle.FixMetaMojibake(FirstNonEmpty(m.Content, m.Share.Text, m.Share.Link))
	Set(ev, "link", m.Share.Link)
	Set(ev, "folder", path.Dir(thread))
	if m.Unsent {
		ev.Metadata["unsent"] = true
	}
	if m.CallDuration > 0 {
		ev.Metadata["call_duration_seconds"] = m.CallDuration
	}
	if o.Extras {
		Set(ev, "share_owner", bundle.FixMetaMojibake(m.Share.Owner))
		if len(m.Reactions) > 0 {
			reactions := make([]map[string]string, 0, len(m.Reactions))
			for _, r := range m.Reactions {
				reactions = append(reactions, map[string]string{
					"reaction": bundle.FixMetaMojibake(r.Reaction),
					"actor":    bundle.FixMetaMojibake(r.Actor),
				})
			}
			ev.Metadata["reactions"] = reactions
		}
	}
	disc := thread + "|" + m.Sender + "|" + m.Content
	if m.Content == "" && len(ev.Media) > 0 {
		disc += ev.Media[0].BlobKey
	}
	ev.ExternalID = ID(MessageKind, m.TimestampMS, disc)
	return One(ev), nil
}

// ThreadOf turns …/messages/inbox/janedoe_123/message_1.json into inbox/janedoe_123.
func ThreadOf(member string) string {
	dir := path.Dir(member)
	if i := strings.LastIndex("/"+dir, "/messages/"); i >= 0 {
		return ("/" + dir)[i+len("/messages/"):]
	}
	return dir
}

// ThreadLabel names a thread after its first few participants.
func ThreadLabel(participants []string) string {
	const max = 3
	names := make([]string, 0, max)
	for _, p := range participants {
		if len(names) == max {
			break
		}
		names = append(names, bundle.FixMetaMojibake(p))
	}
	label := strings.Join(names, ", ")
	if extra := len(participants) - len(names); extra > 0 {
		label += " +" + strconv.Itoa(extra) + " more"
	}
	return label
}
