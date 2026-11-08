package google

import (
	"encoding/json"
	"io"
	"path"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// Keep exports one JSON file per note ("Keep/<title or id>.json": title,
// textContent, listContent[{text, isChecked}], isTrashed, isArchived,
// isPinned, color, createdTimestampUsec, userEditedTimestampUsec, labels[{name}],
// attachments[{filePath, mimetype}], annotations[{url, title}], sharees) plus an
// .html rendering of the same note, which stays generic (google.unknown) so the
// duplicate is visible without being parsed twice.

type keepEx struct{}

func (keepEx) Match(member string) bool {
	return stem(member, ".json") != "" && parentContains(member, "keep")
}

// Extract reads the note whole: a note is a few KB, and the record limit bounds
// a hostile one.
func (keepEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	var note json.RawMessage
	if err := bundle.ReadJSON(r, &note); err != nil {
		return err
	}
	return emit(rec("keep_note", note))
}

type keepNote struct {
	Title       string `json:"title"`
	TextContent string `json:"textContent"`
	ListContent []struct {
		Text      string `json:"text"`
		IsChecked bool   `json:"isChecked"`
	} `json:"listContent"`
	IsTrashed   bool   `json:"isTrashed"`
	IsArchived  bool   `json:"isArchived"`
	IsPinned    bool   `json:"isPinned"`
	Color       string `json:"color"`
	CreatedUsec flex   `json:"createdTimestampUsec"`
	EditedUsec  flex   `json:"userEditedTimestampUsec"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Attachments []struct {
		FilePath string `json:"filePath"`
		Mimetype string `json:"mimetype"`
	} `json:"attachments"`
	Annotations []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"annotations"`
	Sharees []struct {
		Email string `json:"email"`
	} `json:"sharees"`
}

func normalizeKeepNote(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var n keepNote
	if err := json.Unmarshal(raw.Payload, &n); err != nil {
		return nil, err
	}
	if n.Title == "" && n.TextContent == "" && len(n.ListContent) == 0 && len(n.Attachments) == 0 {
		return one(unknown(base, raw, "", nil)), nil
	}
	ev := base
	ev.Type, ev.Category = "google.keep.note", domain.CategoryContent
	ev.OccurredAt = epoch(n.CreatedUsec, time.Microsecond)
	ev.Title = firstNonEmpty(n.Title, "Untitled note")
	body := n.TextContent
	if len(n.ListContent) > 0 {
		var b strings.Builder
		b.WriteString(body)
		for _, item := range n.ListContent {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			if item.IsChecked {
				b.WriteString("[x] ")
			} else {
				b.WriteString("[ ] ")
			}
			b.WriteString(item.Text)
		}
		body = b.String()
		ev.Metadata["list"] = true
	}
	ev.Body = body
	if t := epoch(n.EditedUsec, time.Microsecond); !t.IsZero() {
		meta(ev, "edited", t.Format(time.RFC3339))
	}
	if n.IsPinned {
		ev.Metadata["pinned"] = true
	}
	if n.IsArchived {
		ev.Metadata["archived"] = true
	}
	if n.IsTrashed {
		ev.Metadata["trashed"] = true
	}
	if n.Color != "" && !strings.EqualFold(n.Color, "DEFAULT") {
		meta(ev, "color", n.Color)
	}
	var labels []string
	for _, l := range n.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	meta(ev, "labels", strings.Join(labels, ", "))
	var links []string
	for _, a := range n.Annotations {
		if a.URL != "" {
			links = append(links, a.URL)
		}
	}
	meta(ev, "links", strings.Join(links, " "))
	if len(n.Sharees) > 0 {
		metaAny(ev, "shared_with", len(n.Sharees))
	}
	for _, a := range n.Attachments {
		if a.FilePath != "" {
			ev.Media = append(ev.Media, domain.Media{BlobKey: path.Dir(raw.Member) + "/" + a.FilePath, MIME: a.Mimetype})
		}
	}
	// createdTimestampUsec is the note's identity: it survives edits and is
	// unique per note in practice.
	key := n.CreatedUsec.String()
	if key == "" {
		key = short(n.Title + "\x00" + body)
	}
	ev.ExternalID = "g.keep.note:" + key
	return one(ev), nil
}
