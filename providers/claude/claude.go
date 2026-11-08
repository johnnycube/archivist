// Package claude ingests a Claude.ai data export (Settings → Privacy → Export
// data): a ZIP of conversations.json, projects.json, users.json and
// memories.json.
//
// conversations.json is an array of conversations {uuid, name, summary,
// created_at, updated_at, chat_messages[]}; each message has {uuid, sender
// human|assistant, text, content[] blocks, created_at, updated_at,
// attachments[], files[]}. Newer exports may also carry index and
// parent_message_uuid per message (the shape of Claude's own API); when
// present the parent link is used, otherwise a message parents to the one
// before it. Timestamps are ISO 8601 UTC ("2025-01-15T10:00:00.000Z"; the
// "+00:00" form is accepted too). Messages are sorted by time defensively.
//
// projects.json is an array of projects {uuid, name, description,
// prompt_template, created_at, updated_at, is_private, docs[]{uuid, filename,
// content, created_at}}; a project becomes claude.project and each knowledge
// doc a claude.project_doc threaded under it. Conversations carry no project
// id, so the two are not linked.
//
// users.json and memories.json have no publicly documented schema and are
// captured generically by the importer (claude.unknown), never dropped.
package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const sourceID = "claude"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "Claude",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "claude.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "claude.project", Category: domain.CategoryContent, HasBody: true},
			{Type: "claude.project_doc", Category: domain.CategoryContent, HasBody: true},
			{Type: "claude.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{convExtractor{}, projectExtractor{}}
}

// ---- extraction: conversations ----

type convExtractor struct{}

func (convExtractor) Match(memberPath string) bool {
	return strings.EqualFold(path.Base(memberPath), "conversations.json")
}

type claudeConversation struct {
	UUID         string          `json:"uuid"`
	Name         string          `json:"name"`
	Summary      string          `json:"summary"`
	Model        string          `json:"model"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	ChatMessages []claudeMessage `json:"chat_messages"`
}

type claudeMessage struct {
	UUID        string            `json:"uuid"`
	Text        string            `json:"text"`
	Sender      string            `json:"sender"`
	Index       *int              `json:"index"`
	ParentUUID  string            `json:"parent_message_uuid"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	Content     []json.RawMessage `json:"content"`
	Attachments []json.RawMessage `json:"attachments"`
	Files       []json.RawMessage `json:"files"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type attachment struct {
	FileName         string `json:"file_name"`
	FileType         string `json:"file_type"`
	FileSize         int64  `json:"file_size"`
	ExtractedContent string `json:"extracted_content"`
}

type msgPayload struct {
	ConvID      string            `json:"conv_id"`
	ConvTitle   string            `json:"conv_title"`
	Model       string            `json:"model,omitempty"`
	MsgID       string            `json:"msg_id"`
	ParentMsgID string            `json:"parent_msg_id"`
	Role        string            `json:"role"`
	Text        string            `json:"text"`
	BlockTypes  []string          `json:"block_types,omitempty"`
	CreatedAt   string            `json:"created_at"`
	Attachments []json.RawMessage `json:"attachments,omitempty"`
	Files       []json.RawMessage `json:"files,omitempty"`
}

func (convExtractor) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	// Stream conversation by conversation: real exports run to hundreds of MB.
	return bundle.StreamJSONArray(r, nil, func(el json.RawMessage) error {
		var c claudeConversation
		if err := json.Unmarshal(el, &c); err != nil {
			// Not a conversation object: keep it rather than fail the member.
			return emit(provider.RawRecord{Kind: "unknown", Payload: el})
		}
		msgs := c.ChatMessages
		// Defensive ordering: by index when the export has one, else by time;
		// ties keep the file order.
		sort.SliceStable(msgs, func(i, j int) bool {
			if msgs[i].Index != nil && msgs[j].Index != nil {
				return *msgs[i].Index < *msgs[j].Index
			}
			ti, tj := parseTime(msgs[i].CreatedAt), parseTime(msgs[j].CreatedAt)
			if ti.IsZero() || tj.IsZero() {
				return false
			}
			return ti.Before(tj)
		})
		prev := ""
		for i, m := range msgs {
			text, blockTypes := body(m)
			if strings.TrimSpace(text) == "" && len(m.Attachments) == 0 && len(m.Files) == 0 {
				continue // nothing to keep
			}
			id := m.UUID
			if id == "" {
				id = c.UUID + "/" + strconv.Itoa(i)
			}
			parent := prev
			if m.ParentUUID != "" && !strings.HasPrefix(m.ParentUUID, "00000000-") {
				parent = m.ParentUUID
			}
			pl := msgPayload{
				ConvID:      firstNonEmpty(c.UUID, c.Name),
				ConvTitle:   c.Name,
				Model:       c.Model,
				MsgID:       id,
				ParentMsgID: parent,
				Role:        firstNonEmpty(m.Sender, "unknown"),
				Text:        text,
				BlockTypes:  blockTypes,
				CreatedAt:   m.CreatedAt,
				Attachments: m.Attachments,
				Files:       m.Files,
			}
			prev = id
			b, err := json.Marshal(pl)
			if err != nil {
				return err
			}
			if err := emit(provider.RawRecord{
				ExternalID: "msg:" + id,
				Kind:       "message",
				OccurredAt: parseTime(m.CreatedAt),
				Payload:    b,
			}); err != nil {
				return err
			}
		}
		return nil
	}, bundle.ReportSkipped(member, emit))
}

// body is the message text: "text" when set, else the text blocks of
// "content" joined; the block types seen are reported alongside.
func body(m claudeMessage) (string, []string) {
	var types []string
	var parts []string
	for _, raw := range m.Content {
		var b contentBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		if b.Type != "" {
			types = append(types, b.Type)
		}
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	if strings.TrimSpace(m.Text) != "" {
		return m.Text, types
	}
	return strings.Join(parts, "\n"), types
}

// ---- extraction: projects ----

type projectExtractor struct{}

func (projectExtractor) Match(memberPath string) bool {
	return strings.EqualFold(path.Base(memberPath), "projects.json")
}

type claudeProject struct {
	UUID           string       `json:"uuid"`
	Name           string       `json:"name"`
	Description    string       `json:"description"`
	PromptTemplate string       `json:"prompt_template"`
	IsPrivate      *bool        `json:"is_private"`
	IsStarter      *bool        `json:"is_starter_project"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
	Docs           []projectDoc `json:"docs"`
}

type projectDoc struct {
	UUID      string `json:"uuid"`
	Filename  string `json:"filename"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type projectPayload struct {
	claudeProject
	DocCount int `json:"doc_count"`
}

type docPayload struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Index       int    `json:"index"`
	projectDoc
}

func (projectExtractor) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, []string{"projects"}, func(el json.RawMessage) error {
		var pr claudeProject
		if err := json.Unmarshal(el, &pr); err != nil {
			return emit(provider.RawRecord{Kind: "unknown", Payload: el})
		}
		docs := pr.Docs
		pr.Docs = nil
		b, err := json.Marshal(projectPayload{claudeProject: pr, DocCount: len(docs)})
		if err != nil {
			return err
		}
		if err := emit(provider.RawRecord{Kind: "project", Payload: b, OccurredAt: parseTime(pr.CreatedAt)}); err != nil {
			return err
		}
		for i, d := range docs {
			b, err := json.Marshal(docPayload{ProjectID: pr.UUID, ProjectName: pr.Name, Index: i, projectDoc: d})
			if err != nil {
				return err
			}
			if err := emit(provider.RawRecord{Kind: "project_doc", Payload: b, OccurredAt: parseTime(d.CreatedAt)}); err != nil {
				return err
			}
		}
		return nil
	}, bundle.ReportSkipped(member, emit))
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	switch raw.Kind {
	case bundle.SkippedArrayKind:
		ev, err := bundle.SkippedArrayEvent(sourceID, raw)
		if err != nil {
			return unknown(raw, "malformed skipped-array record"), nil
		}
		return []domain.Event{ev}, nil

	case "message":
		var pl msgPayload
		if err := json.Unmarshal(raw.Payload, &pl); err != nil {
			return unknown(raw, "malformed message record: "+err.Error()), nil
		}
		ev := domain.Event{
			SourceID:         sourceID,
			ExternalID:       "msg:" + pl.MsgID,
			Type:             "claude.message",
			Category:         domain.CategoryMessage,
			OccurredAt:       parseTime(pl.CreatedAt),
			Actor:            pl.Role,
			Title:            "Claude · " + firstNonEmpty(pl.ConvTitle, "untitled"),
			Body:             pl.Text,
			ThreadExternalID: pl.ConvID,
			ParentExternalID: pl.ParentMsgID,
			Metadata:         map[string]any{"conversation": pl.ConvTitle},
		}
		if pl.Model != "" {
			ev.Metadata["model"] = pl.Model
		}
		if len(pl.BlockTypes) > 0 {
			ev.Metadata["block_types"] = pl.BlockTypes
		}
		if len(pl.Attachments) > 0 {
			ev.Metadata["attachments"] = pl.Attachments
			for _, raw := range pl.Attachments {
				var a attachment
				if json.Unmarshal(raw, &a) == nil && a.FileName != "" {
					ev.Media = append(ev.Media, domain.Media{URL: a.FileName, MIME: a.FileType})
				}
			}
		}
		if len(pl.Files) > 0 {
			ev.Metadata["files"] = pl.Files
		}
		if ev.Body == "" {
			ev.Title += " (attachment)"
		}
		return []domain.Event{ev}, nil

	case "project":
		var pr projectPayload
		if err := json.Unmarshal(raw.Payload, &pr); err != nil {
			return unknown(raw, "malformed project record: "+err.Error()), nil
		}
		id := firstNonEmpty(pr.UUID, digest(pr.Name, pr.CreatedAt))
		ev := domain.Event{
			SourceID:         sourceID,
			ExternalID:       "project:" + id,
			Type:             "claude.project",
			Category:         domain.CategoryContent,
			OccurredAt:       parseTime(pr.CreatedAt),
			Actor:            "self",
			Title:            "Project: " + firstNonEmpty(pr.Name, "untitled"),
			Body:             firstNonEmpty(pr.PromptTemplate, pr.Description),
			ThreadExternalID: "project:" + id,
			Metadata:         map[string]any{"docs": pr.DocCount},
		}
		set(ev, "description", pr.Description)
		set(ev, "updated_at", pr.UpdatedAt)
		if pr.IsPrivate != nil {
			ev.Metadata["private"] = *pr.IsPrivate
		}
		if pr.IsStarter != nil {
			ev.Metadata["starter_project"] = *pr.IsStarter
		}
		return []domain.Event{ev}, nil

	case "project_doc":
		var d docPayload
		if err := json.Unmarshal(raw.Payload, &d); err != nil {
			return unknown(raw, "malformed project doc record: "+err.Error()), nil
		}
		pid := firstNonEmpty(d.ProjectID, digest(d.ProjectName))
		id := firstNonEmpty(d.UUID, pid+"/"+strconv.Itoa(d.Index))
		ev := domain.Event{
			SourceID:         sourceID,
			ExternalID:       "project_doc:" + id,
			Type:             "claude.project_doc",
			Category:         domain.CategoryContent,
			OccurredAt:       parseTime(d.CreatedAt),
			Actor:            "self",
			Title:            firstNonEmpty(d.Filename, "Project document") + " · " + firstNonEmpty(d.ProjectName, "project"),
			Body:             d.Content,
			ThreadExternalID: "project:" + pid,
			ParentExternalID: "project:" + pid,
			Metadata:         map[string]any{"project": d.ProjectName},
		}
		set(ev, "filename", d.Filename)
		return []domain.Event{ev}, nil
	}
	return unknown(raw, "unrecognized record kind "+strconv.Quote(raw.Kind)), nil
}

// ---- helpers ----

// unknown is the never-drop fallback: a record Normalize cannot make sense of
// becomes a claude.unknown event with a preview of the payload, instead of an
// error that would abort the whole import.
func unknown(raw provider.RawRecord, reason string) []domain.Event {
	sum := sha256.Sum256(raw.Payload)
	id := raw.ExternalID
	if id == "" {
		id = hex.EncodeToString(sum[:8])
	}
	ev := domain.Event{
		SourceID: sourceID, ExternalID: "unknown:" + id,
		Type: "claude.unknown", Category: domain.CategoryOther,
		Title:    "Unrecognized Claude record",
		Metadata: map[string]any{"reason": reason, "preview": preview(raw.Payload), "bytes": len(raw.Payload)},
	}
	if raw.Member != "" {
		ev.Metadata["member"] = raw.Member
	}
	return []domain.Event{ev}
}

const previewRunes = 240

func preview(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	if utf8.RuneCountInString(s) <= previewRunes {
		return s
	}
	return string([]rune(s)[:previewRunes]) + "…"
}

func set(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:6])
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
