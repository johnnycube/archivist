// Package chatgpt ingests a ChatGPT data export. The export's conversations.json
// stores each conversation as a tree of message nodes under "mapping" (every
// edit or regeneration is a branch; "current_node" is the leaf of the branch
// shown in the UI); this provider walks the whole tree, orders messages by time,
// and maps each to a chatgpt.message Event grouped by conversation thread.
//
// Shapes follow the public type definitions of chatgpt-exporter
// (github.com/pionxzh/chatgpt-exporter, src/api.ts):
//
//   - message.create_time is a float of unix seconds and may be null or absent
//     (system-generated nodes); such a message is dated by its nearest dated
//     ancestor and flagged time_inferred.
//   - content.content_type is one of text, multimodal_text, code,
//     execution_output, tether_quote, tether_browsing_display, thoughts,
//     reasoning_recap, user_editable_context, model_editable_context, …; only
//     text-like types carry "parts", the others put their text in "text",
//     "result", "content" or their own fields. All are mapped; the type is kept
//     in metadata.
//   - multimodal parts may be objects: image_asset_pointer
//     ("file-service://file-XXXX" or "sediment://…") becomes a Media entry with
//     the pointer as URL (the export names the files file-XXXX-<original>, so the
//     exact member cannot be derived statelessly); audio_transcription
//     contributes its text.
//   - author.role is system|user|assistant|tool; a tool's name (browser, python,
//     dalle.text2im, …) is kept in the Actor as "tool:<name>".
//
// Nodes of branches that are not the current one are kept (never drop data) and
// marked on_current_branch=false. Large exports are split into
// conversations-000.json, conversations-001.json, …; all are matched.
package chatgpt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
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

const sourceID = "chatgpt"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "ChatGPT",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "chatgpt.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "chatgpt.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{convExtractor{}}
}

// ---- extraction ----

type convExtractor struct{}

// Match accepts conversations.json and the numbered parts of a split export
// (conversations-000.json).
func (convExtractor) Match(memberPath string) bool {
	b := strings.ToLower(path.Base(memberPath))
	if !strings.HasSuffix(b, ".json") {
		return false
	}
	b = strings.TrimSuffix(b, ".json")
	if b == "conversations" {
		return true
	}
	n, ok := strings.CutPrefix(b, "conversations-")
	if !ok || n == "" {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// on-disk shapes (the subset we read)
type cgptConversation struct {
	Title          string              `json:"title"`
	ConversationID string              `json:"conversation_id"`
	ID             string              `json:"id"`
	CreateTime     *float64            `json:"create_time"`
	CurrentNode    string              `json:"current_node"`
	Mapping        map[string]cgptNode `json:"mapping"`
	GizmoID        string              `json:"gizmo_id"`
	ModelSlug      string              `json:"default_model_slug"`
}

type cgptNode struct {
	ID      string       `json:"id"`
	Parent  string       `json:"parent"`
	Message *cgptMessage `json:"message"`
}

type cgptMessage struct {
	ID     string `json:"id"`
	Author struct {
		Role string `json:"role"`
		Name string `json:"name"`
	} `json:"author"`
	CreateTime *float64        `json:"create_time"`
	Content    json.RawMessage `json:"content"`
	Status     string          `json:"status"`
	Recipient  string          `json:"recipient"`
	Metadata   struct {
		ModelSlug   string            `json:"model_slug"`
		Hidden      bool              `json:"is_visually_hidden_from_conversation"`
		Attachments []json.RawMessage `json:"attachments"`
	} `json:"metadata"`
}

// cgptContent is the union of the content shapes; which fields are set depends
// on content_type.
type cgptContent struct {
	ContentType string            `json:"content_type"`
	Parts       []json.RawMessage `json:"parts"`
	Text        string            `json:"text"`     // code, execution_output, tether_quote
	Language    string            `json:"language"` // code
	Result      string            `json:"result"`   // tether_browsing_display
	Summary     string            `json:"summary"`  // tether_browsing_display
	Title       string            `json:"title"`    // tether_quote
	URL         string            `json:"url"`      // tether_quote
	Content     string            `json:"content"`  // reasoning_recap
	Thoughts    []struct {
		Summary string `json:"summary"`
		Content string `json:"content"`
	} `json:"thoughts"` // thoughts
	UserProfile      string `json:"user_profile"`      // user_editable_context
	UserInstructions string `json:"user_instructions"` // user_editable_context
	ModelSetContext  string `json:"model_set_context"` // model_editable_context
}

// multimodal part objects (multimodal_text)
type cgptPart struct {
	ContentType  string `json:"content_type"`
	AssetPointer string `json:"asset_pointer"`
	Text         string `json:"text"`      // audio_transcription
	Direction    string `json:"direction"` // audio_transcription: in|out
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	SizeBytes    int64  `json:"size_bytes"`
	Metadata     struct {
		Dalle *struct {
			GenID  string `json:"gen_id"`
			Prompt string `json:"prompt"`
		} `json:"dalle"`
	} `json:"metadata"`
}

// msgPayload is the normalized RawRecord payload we emit per message.
type msgPayload struct {
	ConvID       string            `json:"conv_id"`
	ConvTitle    string            `json:"conv_title"`
	NodeID       string            `json:"node_id"`
	MsgID        string            `json:"msg_id"`
	ParentMsgID  string            `json:"parent_msg_id"`
	Role         string            `json:"role"`
	AuthorName   string            `json:"author_name,omitempty"`
	Recipient    string            `json:"recipient,omitempty"`
	Text         string            `json:"text"`
	ContentType  string            `json:"content_type"`
	Language     string            `json:"language,omitempty"`
	CreateTime   float64           `json:"create_time"`
	TimeInferred bool              `json:"time_inferred,omitempty"`
	OnBranch     bool              `json:"on_current_branch"`
	Hidden       bool              `json:"hidden,omitempty"`
	ModelSlug    string            `json:"model_slug,omitempty"`
	Status       string            `json:"status,omitempty"`
	Assets       []asset           `json:"assets,omitempty"`
	Attachments  []json.RawMessage `json:"attachments,omitempty"`
}

// asset is an image/audio pointer found in a multimodal message.
type asset struct {
	Kind    string `json:"kind"` // image | audio | video | other
	Pointer string `json:"pointer"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Prompt  string `json:"dalle_prompt,omitempty"`
}

func (convExtractor) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	// Stream conversation by conversation: real exports run to hundreds of MB.
	return bundle.StreamJSONArray(r, nil, func(el json.RawMessage) error {
		var c cgptConversation
		if err := json.Unmarshal(el, &c); err != nil {
			// An element that is not a conversation object is kept, not
			// dropped and not fatal to the rest of the member.
			return emit(provider.RawRecord{Kind: "unknown", Payload: el})
		}
		for _, pl := range flatten(c) {
			b, err := json.Marshal(pl)
			if err != nil {
				return err
			}
			if err := emit(provider.RawRecord{
				ExternalID: "msg:" + pl.MsgID,
				Kind:       "message",
				OccurredAt: unixFloat(pl.CreateTime),
				Payload:    b,
			}); err != nil {
				return err
			}
		}
		return nil
	}, bundle.ReportSkipped(member, emit))
}

// flatten turns a conversation's node tree into the ordered list of message
// payloads: every node with content, on any branch, dated by itself or its
// nearest dated ancestor, sorted by time (ties keep tree order).
func flatten(c cgptConversation) []msgPayload {
	convID := firstNonEmpty(c.ConversationID, c.ID, c.Title)

	// The current branch: current_node up through the parents. A visited set
	// guards against exports whose node is its own ancestor.
	onBranch := map[string]bool{}
	for id := c.CurrentNode; id != "" && !onBranch[id]; {
		onBranch[id] = true
		n, ok := c.Mapping[id]
		if !ok {
			break
		}
		id = n.Parent
	}

	// The message id a node's parent chain resolves to (skipping structural
	// nodes without a message), and the nearest dated ancestor's time.
	msgID := func(nid string, n cgptNode) string {
		if n.Message != nil && n.Message.ID != "" {
			return n.Message.ID
		}
		return convID + "/" + nid
	}
	// Every per-node value below is defined over the node's parent chain.
	// Each is resolved once per node (memoised, iteratively, in a fixed
	// order), so a conversation costs O(nodes): walking the chain per node —
	// let alone per comparison inside the sort — is quadratic, and a 1 MiB
	// single-chain conversation then blocks the import worker for hours.
	nodeIDs := make([]string, 0, len(c.Mapping))
	for nid := range c.Mapping {
		nodeIDs = append(nodeIDs, nid)
	}
	sort.Strings(nodeIDs)

	// The nearest dated ancestor's time.
	dated := ancestorValues(c.Mapping, nodeIDs, func(_ string, pn cgptNode) (float64, bool) {
		if pn.Message != nil && pn.Message.CreateTime != nil && *pn.Message.CreateTime > 0 {
			return *pn.Message.CreateTime, true
		}
		return 0, false
	})
	inheritedTime := func(nid string) float64 {
		if v, ok := dated[nid]; ok {
			return v
		}
		if c.CreateTime != nil {
			return *c.CreateTime
		}
		return 0
	}

	// Tree order (root first) as the tiebreaker: depth, then node id.
	depth := chainDepths(c.Mapping, nodeIDs)
	sort.SliceStable(nodeIDs, func(i, j int) bool { return depth[nodeIDs[i]] < depth[nodeIDs[j]] })

	// First pass: which nodes carry content at all. Structurally empty ones
	// (system priming with parts [""]) are not emitted, and parent links skip
	// them so a reply points at a message that exists.
	type rendered struct {
		text, ctype, lang string
		assets            []asset
	}
	content := map[string]rendered{}
	for _, nid := range nodeIDs {
		m := c.Mapping[nid].Message
		if m == nil {
			continue
		}
		text, ctype, lang, assets := render(m.Content)
		if strings.TrimSpace(text) == "" && len(assets) == 0 && len(m.Metadata.Attachments) == 0 {
			continue
		}
		content[nid] = rendered{text, ctype, lang, assets}
	}
	parents := ancestorValues(c.Mapping, nodeIDs, func(pid string, pn cgptNode) (string, bool) {
		if _, emitted := content[pid]; emitted {
			return msgID(pid, pn), true
		}
		return "", false
	})

	var payloads []msgPayload
	for _, nid := range nodeIDs {
		r, ok := content[nid]
		if !ok {
			continue
		}
		n := c.Mapping[nid]
		m := n.Message
		text, ctype, lang, assets := r.text, r.ctype, r.lang, r.assets
		pl := msgPayload{
			ConvID:      convID,
			ConvTitle:   c.Title,
			NodeID:      nid,
			MsgID:       msgID(nid, n),
			ParentMsgID: parents[nid],
			Role:        firstNonEmpty(m.Author.Role, "unknown"),
			AuthorName:  m.Author.Name,
			Recipient:   m.Recipient,
			Text:        text,
			ContentType: ctype,
			Language:    lang,
			OnBranch:    onBranch[nid],
			Hidden:      m.Metadata.Hidden,
			ModelSlug:   firstNonEmpty(m.Metadata.ModelSlug, c.ModelSlug),
			Status:      m.Status,
			Assets:      assets,
			Attachments: m.Metadata.Attachments,
		}
		if m.CreateTime != nil && *m.CreateTime > 0 {
			pl.CreateTime = *m.CreateTime
		} else if t := inheritedTime(nid); t > 0 {
			pl.CreateTime, pl.TimeInferred = t, true
		}
		payloads = append(payloads, pl)
	}
	sort.SliceStable(payloads, func(i, j int) bool { return payloads[i].CreateTime < payloads[j].CreateTime })
	return payloads
}

// ancestorValues maps every node to the value of its nearest proper ancestor
// for which own reports one; nodes without such an ancestor are absent. A
// missing parent or a cycle ends a chain. Each node is resolved once: a walk
// stops at the first ancestor already resolved, and every node it passed
// shares that ancestor's answer (none of them had a value of its own on the
// way). order fixes the walk order, so the result is deterministic.
func ancestorValues[T any](mapping map[string]cgptNode, order []string, own func(id string, n cgptNode) (T, bool)) map[string]T {
	type result struct {
		v  T
		ok bool
	}
	memo := make(map[string]result, len(mapping))
	for _, start := range order {
		if _, done := memo[start]; done {
			continue
		}
		var r result
		path, _ := climb(mapping, start, func(p string) bool {
			if v, ok := own(p, mapping[p]); ok {
				r = result{v, true}
				return true
			}
			m, done := memo[p]
			r = m
			return done
		})
		for _, id := range path {
			memo[id] = r
		}
	}
	out := make(map[string]T, len(memo))
	for id, r := range memo {
		if r.ok {
			out[id] = r.v
		}
	}
	return out
}

// chainDepths maps every node to the number of nodes on its parent chain,
// itself included (a root is 1); a missing parent or a cycle ends a chain.
// Linear, like ancestorValues.
func chainDepths(mapping map[string]cgptNode, order []string) map[string]int {
	depth := make(map[string]int, len(mapping))
	for _, start := range order {
		if _, done := depth[start]; done {
			continue
		}
		path, at := climb(mapping, start, func(p string) bool {
			_, done := depth[p]
			return done
		})
		base := depth[at] // 0 when the chain ended
		for i := len(path) - 1; i >= 0; i-- {
			base++
			depth[path[i]] = base
		}
	}
	return depth
}

// climb walks from start up the parent chain and returns the nodes it passed
// (start first) and the parent it stopped at: one for which stop reported
// true, or "" when the chain ended (no parent, a missing one, or a cycle).
func climb(mapping map[string]cgptNode, start string, stop func(parent string) bool) (path []string, at string) {
	onPath := map[string]bool{}
	for id := start; ; {
		path = append(path, id)
		onPath[id] = true
		p := mapping[id].Parent
		if _, ok := mapping[p]; p == "" || !ok || onPath[p] {
			return path, ""
		}
		if stop(p) {
			return path, p
		}
		id = p
	}
}

// render extracts the readable text of a message content object, whatever its
// content_type, plus the media pointers of a multimodal message.
func render(raw json.RawMessage) (text, ctype, lang string, assets []asset) {
	var ct cgptContent
	if err := json.Unmarshal(raw, &ct); err != nil {
		// Not an object (or garbage): keep whatever text it is.
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, "text", "", nil
		}
		return "", "unknown", "", nil
	}
	ctype = firstNonEmpty(ct.ContentType, "text")
	var out []string
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	for _, p := range ct.Parts {
		var s string
		if json.Unmarshal(p, &s) == nil {
			add(s)
			continue
		}
		var obj cgptPart
		if json.Unmarshal(p, &obj) != nil {
			continue
		}
		switch obj.ContentType {
		case "audio_transcription":
			add(obj.Text)
		case "image_asset_pointer", "audio_asset_pointer", "video_container_asset_pointer", "real_time_user_audio_video_asset_pointer":
			a := asset{
				Kind: strings.TrimSuffix(strings.SplitN(obj.ContentType, "_", 2)[0], "_"), Pointer: obj.AssetPointer,
				Width: obj.Width, Height: obj.Height, Bytes: obj.SizeBytes,
			}
			if obj.ContentType == "real_time_user_audio_video_asset_pointer" {
				a.Kind = "video"
			}
			if obj.Metadata.Dalle != nil {
				a.Prompt = obj.Metadata.Dalle.Prompt
			}
			assets = append(assets, a)
		default:
			if obj.Text != "" {
				add(obj.Text)
			} else if obj.AssetPointer != "" {
				assets = append(assets, asset{Kind: "other", Pointer: obj.AssetPointer})
			}
		}
	}
	switch ct.ContentType {
	case "code", "execution_output", "tether_quote":
		add(ct.Text)
		lang = ct.Language
	case "tether_browsing_display":
		add(firstNonEmpty(ct.Summary, ct.Result))
	case "reasoning_recap":
		add(ct.Content)
	case "thoughts":
		for _, th := range ct.Thoughts {
			add(firstNonEmpty(th.Content, th.Summary))
		}
	case "user_editable_context":
		add(ct.UserProfile)
		add(ct.UserInstructions)
	case "model_editable_context":
		add(ct.ModelSetContext)
	default:
		if len(ct.Parts) == 0 {
			add(ct.Text)
			add(ct.Content)
			add(ct.Result)
		}
	}
	return strings.Join(out, "\n"), ctype, lang, assets
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	if raw.Kind == bundle.SkippedArrayKind {
		ev, err := bundle.SkippedArrayEvent(sourceID, raw)
		if err != nil {
			return unknown(raw, "malformed skipped-array record"), nil
		}
		return []domain.Event{ev}, nil
	}
	if raw.Kind != "message" {
		return unknown(raw, "unrecognized record kind "+strconv.Quote(raw.Kind)), nil
	}
	var pl msgPayload
	if err := json.Unmarshal(raw.Payload, &pl); err != nil {
		return unknown(raw, "malformed message record: "+err.Error()), nil
	}
	actor := pl.Role
	if pl.Role == "tool" && pl.AuthorName != "" {
		actor = "tool:" + pl.AuthorName
	}
	ev := domain.Event{
		SourceID:         sourceID,
		ExternalID:       "msg:" + pl.MsgID,
		Type:             "chatgpt.message",
		Category:         domain.CategoryMessage,
		OccurredAt:       unixFloat(pl.CreateTime),
		Actor:            actor,
		Title:            "ChatGPT · " + firstNonEmpty(pl.ConvTitle, "untitled"),
		Body:             pl.Text,
		ThreadExternalID: pl.ConvID,
		ParentExternalID: pl.ParentMsgID,
		Metadata: map[string]any{
			"conversation":      pl.ConvTitle,
			"content_type":      pl.ContentType,
			"on_current_branch": pl.OnBranch,
			"node_id":           pl.NodeID,
		},
	}
	set := func(k, v string) {
		if v != "" {
			ev.Metadata[k] = v
		}
	}
	set("language", pl.Language)
	set("model", pl.ModelSlug)
	set("author_name", pl.AuthorName)
	set("recipient", pl.Recipient)
	set("status", pl.Status)
	if pl.TimeInferred {
		ev.Metadata["time_inferred"] = true
	}
	if pl.Hidden {
		ev.Metadata["hidden"] = true
	}
	if len(pl.Attachments) > 0 {
		ev.Metadata["attachments"] = pl.Attachments
	}
	for _, a := range pl.Assets {
		m := domain.Media{URL: a.Pointer}
		switch a.Kind {
		case "image":
			m.MIME = "image/*"
		case "audio":
			m.MIME = "audio/*"
		case "video":
			m.MIME = "video/*"
		}
		ev.Media = append(ev.Media, m)
	}
	if len(pl.Assets) > 0 {
		ev.Metadata["assets"] = pl.Assets
		if ev.Body == "" {
			ev.Title += " (attachment)"
		}
	}
	return []domain.Event{ev}, nil
}

// ---- helpers ----

// unknown is the never-drop fallback: a record Normalize cannot make sense of
// becomes a chatgpt.unknown event that keeps a preview of the payload, instead
// of an error that would abort the whole import.
func unknown(raw provider.RawRecord, reason string) []domain.Event {
	sum := sha256.Sum256(raw.Payload)
	id := raw.ExternalID
	if id == "" {
		id = hex.EncodeToString(sum[:8])
	}
	ev := domain.Event{
		SourceID: sourceID, ExternalID: "unknown:" + id,
		Type: "chatgpt.unknown", Category: domain.CategoryOther,
		Title:    "Unrecognized ChatGPT record",
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

// unixFloat converts the export's float unix seconds. The JSON floats carry at
// most microseconds, so rounding there avoids the float64 error that would turn
// .123 into .122999906.
func unixFloat(sec float64) time.Time {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) || sec > 1e12 {
		return time.Time{}
	}
	micros := int64(math.Round(sec * 1e6))
	return time.UnixMicro(micros).UTC()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
