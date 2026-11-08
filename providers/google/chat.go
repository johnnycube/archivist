package google

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

// Google Chat exports one folder per conversation, "Google Chat/Groups/<Space
// or DM id>/", holding group_info.json (name, members[{name, email}]),
// messages.json ({"messages":[{creator{name, email, user_type}, created_date,
// text, topic_id, message_id, annotations[], attached_files[{original_name,
// export_name}], reactions[]}]}) and the attached files themselves.
// created_date is an English sentence, "Thursday, July 14, 2016 at 5:04:34 PM
// UTC"; other account languages render it differently and stay undated.
// group_info.json is left to generic capture (it is a member list, not an
// event). Classic Hangouts is one file, Hangouts/Hangouts.json, with
// conversations[].events[] and microsecond timestamps.

var chatGroupDirs = []string{"groups", "gruppen", "groupes", "grupos", "gruppi"}

// inChatGroup reports whether member sits in a conversation folder of the
// Google Chat export.
func inChatGroup(member string) bool {
	return dirContains(member, "chat") && anyDirIs(member, chatGroupDirs)
}

// ---- Google Chat ----

type chatEx struct{}

func (chatEx) Match(member string) bool {
	return stem(member, ".json") == "messages" && inChatGroup(member)
}

type chatWrapper struct {
	Group   string          `json:"group"`
	Message json.RawMessage `json:"message"`
}

func (chatEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	group := path.Base(path.Dir(member))
	return bundle.StreamJSONArray(r, []string{"messages"}, func(el json.RawMessage) error {
		payload, err := json.Marshal(chatWrapper{Group: group, Message: el})
		if err != nil {
			return err
		}
		return emit(rec("chat_message", payload))
	}, bundle.ReportSkipped(member, emit))
}

type chatMessage struct {
	Creator struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		UserType string `json:"user_type"`
	} `json:"creator"`
	CreatedDate   string `json:"created_date"`
	Text          string `json:"text"`
	TopicID       string `json:"topic_id"`
	MessageID     string `json:"message_id"`
	MessageState  string `json:"message_state"`
	AttachedFiles []struct {
		OriginalName string `json:"original_name"`
		ExportName   string `json:"export_name"`
	} `json:"attached_files"`
	Annotations []struct {
		URLMetadata *struct {
			URL struct {
				PrivateDoNotAccessOrElseSafeURLWrappedValue string `json:"private_do_not_access_or_else_safe_url_wrapped_value"`
			} `json:"url"`
			Title string `json:"title"`
		} `json:"url_metadata"`
		DriveMetadata *struct {
			Title string `json:"title"`
			ID    string `json:"id"`
		} `json:"drive_metadata"`
	} `json:"annotations"`
	Reactions []struct {
		Emoji struct {
			Unicode string `json:"unicode"`
		} `json:"emoji"`
		ReactorEmails []string `json:"reactor_emails"`
	} `json:"reactions"`
}

// chatDateLayout is Google Chat's created_date: "Thursday, July 14, 2016 at
// 5:04:34 PM UTC".
const chatDateLayout = "Monday, January 2, 2006 at 3:04:05 PM MST"

func parseChatDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(chatDateLayout, s); err == nil {
		return t.UTC()
	}
	return parseTime(s)
}

func normalizeChatMessage(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var w chatWrapper
	if err := json.Unmarshal(raw.Payload, &w); err != nil {
		return nil, err
	}
	var m chatMessage
	if err := json.Unmarshal(w.Message, &m); err != nil {
		return nil, err
	}
	actor := firstNonEmpty(m.Creator.Name, m.Creator.Email)
	if actor == "" && m.Text == "" && len(m.AttachedFiles) == 0 {
		return one(unknown(base, raw, "", nil)), nil
	}
	ev := base
	ev.Type, ev.Category = "google.chat.message", domain.CategoryMessage
	ev.Actor = firstNonEmpty(actor, "unknown")
	ev.OccurredAt = parseChatDate(m.CreatedDate)
	if ev.OccurredAt.IsZero() {
		meta(ev, "created_date", m.CreatedDate)
	}
	ev.Body = m.Text
	ev.Title = "Message in " + firstNonEmpty(w.Group, "Google Chat")
	if strings.HasPrefix(strings.ToLower(w.Group), "dm") {
		ev.Title = "Direct message"
	}
	ev.ThreadExternalID = "g.chat:" + w.Group
	meta(ev, "group", w.Group)
	meta(ev, "email", m.Creator.Email)
	meta(ev, "topic_id", m.TopicID)
	meta(ev, "state", m.MessageState)
	dir := path.Dir(raw.Member)
	var files []string
	for _, f := range m.AttachedFiles {
		name := firstNonEmpty(f.ExportName, f.OriginalName)
		if name == "" {
			continue
		}
		files = append(files, f.OriginalName)
		ev.Media = append(ev.Media, domain.Media{BlobKey: dir + "/" + name})
	}
	meta(ev, "attachments", strings.Join(files, ", "))
	var links []string
	for _, a := range m.Annotations {
		if a.URLMetadata != nil && a.URLMetadata.URL.PrivateDoNotAccessOrElseSafeURLWrappedValue != "" {
			links = append(links, a.URLMetadata.URL.PrivateDoNotAccessOrElseSafeURLWrappedValue)
		}
		if a.DriveMetadata != nil && a.DriveMetadata.Title != "" {
			meta(ev, "drive_file", a.DriveMetadata.Title)
		}
	}
	meta(ev, "links", strings.Join(links, " "))
	if len(m.Reactions) > 0 {
		var emoji []string
		for _, r := range m.Reactions {
			if r.Emoji.Unicode != "" {
				emoji = append(emoji, r.Emoji.Unicode)
			}
		}
		meta(ev, "reactions", strings.Join(emoji, " "))
	}
	if m.MessageID != "" {
		ev.ExternalID = "g.chat.message:" + m.MessageID
	} else {
		ev.ExternalID = "g.chat.message:" + short(w.Group+"\x00"+when(ev.OccurredAt, m.CreatedDate)+"\x00"+actor+"\x00"+m.Text)
	}
	return one(ev), nil
}

// ---- classic Hangouts ----

type hangoutsEx struct{}

func (hangoutsEx) Match(member string) bool {
	return stem(member, ".json") == "hangouts"
}

// Extract streams one conversation at a time (a conversation with all its
// events is one array element, so a very long one is bounded by the record
// limit).
func (hangoutsEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, []string{"conversations"}, func(el json.RawMessage) error {
		return emit(rec("hangouts_conversation", el))
	}, bundle.ReportSkipped(member, emit))
}

type hangoutsParticipant struct {
	ID struct {
		ChatID string `json:"chat_id"`
		GaiaID string `json:"gaia_id"`
	} `json:"id"`
	FallbackName string `json:"fallback_name"`
}

type hangoutsConversationInfo struct {
	ID struct {
		ID string `json:"id"`
	} `json:"id"`
	Name            string                `json:"name"`
	Type            string                `json:"type"`
	ParticipantData []hangoutsParticipant `json:"participant_data"`
}

type hangoutsEvent struct {
	EventID  string `json:"event_id"`
	SenderID struct {
		ChatID string `json:"chat_id"`
	} `json:"sender_id"`
	Timestamp   flex   `json:"timestamp"`
	EventType   string `json:"event_type"`
	ChatMessage *struct {
		MessageContent struct {
			Segment []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				LinkData struct {
					LinkTarget string `json:"link_target"`
				} `json:"link_data"`
			} `json:"segment"`
			Attachment []struct {
				EmbedItem struct {
					PlusPhoto struct {
						URL string `json:"url"`
					} `json:"plus_photo"`
				} `json:"embed_item"`
			} `json:"attachment"`
		} `json:"message_content"`
	} `json:"chat_message"`
}

// hangoutsConversation accepts both layouts: the current
// {"conversation":{"conversation":{…}}, "events":[…]} and the older
// {"conversation_state":{"conversation":{…}, "event":[…]}}.
type hangoutsConversation struct {
	Conversation struct {
		Conversation hangoutsConversationInfo `json:"conversation"`
	} `json:"conversation"`
	Events            []hangoutsEvent `json:"events"`
	ConversationState struct {
		Conversation hangoutsConversationInfo `json:"conversation"`
		Event        []hangoutsEvent          `json:"event"`
	} `json:"conversation_state"`
}

func normalizeHangoutsConversation(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var c hangoutsConversation
	if err := json.Unmarshal(raw.Payload, &c); err != nil {
		return nil, err
	}
	info := c.Conversation.Conversation
	events := c.Events
	if info.ID.ID == "" && len(events) == 0 {
		info, events = c.ConversationState.Conversation, c.ConversationState.Event
	}
	names := map[string]string{}
	var participants []string
	for _, p := range info.ParticipantData {
		if p.FallbackName != "" {
			names[p.ID.ChatID] = p.FallbackName
			participants = append(participants, p.FallbackName)
		}
	}
	thread := info.ID.ID
	title := "Hangouts message"
	if info.Name != "" {
		title = "Message in " + info.Name
	} else if len(participants) > 0 {
		title = "Hangouts with " + strings.Join(participants, ", ")
	}

	var out []domain.Event
	for i, e := range events {
		ev := fresh(base)
		ev.OccurredAt = epoch(e.Timestamp, time.Microsecond)
		ev.ThreadExternalID = "g.hangouts:" + thread
		ev.Actor = firstNonEmpty(names[e.SenderID.ChatID], e.SenderID.ChatID, "unknown")
		meta(ev, "conversation", info.Name)
		meta(ev, "conversation_id", thread)
		key := firstNonEmpty(e.EventID, thread+":"+e.Timestamp.String()+":"+e.SenderID.ChatID+"#"+strconv.Itoa(i))
		if e.ChatMessage == nil {
			// Calls, renames, membership changes: kept visible, not modelled.
			ev.Type, ev.Category = "google.unknown", domain.CategoryOther
			ev.Title = "Hangouts event " + firstNonEmpty(e.EventType, "(untyped)")
			meta(ev, "member", raw.Member)
			meta(ev, "event_type", e.EventType)
			ev.ExternalID = "g.hangouts.event:" + key
			out = append(out, ev)
			continue
		}
		var b strings.Builder
		var links []string
		for _, s := range e.ChatMessage.MessageContent.Segment {
			switch s.Type {
			case "LINE_BREAK":
				b.WriteByte('\n')
			default:
				b.WriteString(s.Text)
				if s.LinkData.LinkTarget != "" {
					links = append(links, s.LinkData.LinkTarget)
				}
			}
		}
		for _, a := range e.ChatMessage.MessageContent.Attachment {
			if u := a.EmbedItem.PlusPhoto.URL; u != "" {
				ev.Media = append(ev.Media, domain.Media{URL: u})
			}
		}
		ev.Type, ev.Category = "google.hangouts.message", domain.CategoryMessage
		ev.Title = title
		ev.Body = b.String()
		meta(ev, "links", strings.Join(links, " "))
		ev.ExternalID = "g.hangouts.message:" + key
		out = append(out, ev)
	}
	if len(out) == 0 {
		return one(unknown(base, raw, "", nil)), nil
	}
	return out, nil
}
