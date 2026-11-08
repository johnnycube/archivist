// Package linkedin ingests a LinkedIn "Get a copy of your data" export — a ZIP
// of CSV files, one per data type (file names are English whatever the UI
// language) — and maps every row to a canonical Event.
//
// Three things make these CSVs awkward, and extraction is built around them:
//
//   - Some members (Connections.csv) open with a "Notes:" preamble before the
//     real header row, so every member is read through skipPreamble, which drops
//     those lines while streaming.
//   - Column names differ in case between files ("CONVERSATION ID", "Connected
//     On", "inviterProfileUrl") and across export vintages, so header names are
//     lower-cased at extraction and looked up with aliases (pick).
//   - Every file has its own date format, so parseTime tries them all. None of
//     them carries a zone except the "… UTC" ones; zoneless values are taken as
//     UTC.
//
// The export has no record ids (not even for messages), so ExternalIDs are built
// from the record's own stable fields: its URL where there is one, otherwise its
// timestamp plus a hash of its content. Files that are not listed in Extractors
// (skills, endorsements, recommendations, ad targeting…) are captured
// generically by the bundle importer, so nothing is dropped.
package linkedin

import (
	"bufio"
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

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const sourceID = "linkedin"

// Provider implements provider.BundleProvider for LinkedIn exports.
type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "LinkedIn",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "linkedin.post", Category: domain.CategoryContent, HasBody: true},
			{Type: "linkedin.comment", Category: domain.CategoryContent, HasBody: true},
			{Type: "linkedin.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "linkedin.reaction", Category: domain.CategoryReaction, NeedsTarget: true},
			{Type: "linkedin.connection", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "linkedin.invitation", Category: domain.CategoryConnection, HasBody: true},
			{Type: "linkedin.follow", Category: domain.CategoryConnection, NeedsTarget: true},
			{Type: "linkedin.save", Category: domain.CategorySave, NeedsTarget: true},
			{Type: "linkedin.search", Category: domain.CategoryConsumption, HasBody: true},
			{Type: "linkedin.position", Category: domain.CategoryProfile, HasBody: true},
			{Type: "linkedin.education", Category: domain.CategoryProfile, HasBody: true},
			{Type: "linkedin.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors maps each known CSV to a provider-local Kind. Unlisted files are
// captured generically by the importer.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{
		csvExtractor{file: "Connections.csv", kind: "connection", header: "first name"},
		csvExtractor{file: "messages.csv", kind: "message"},
		csvExtractor{file: "Shares.csv", kind: "post"},
		csvExtractor{file: "Comments.csv", kind: "comment"},
		csvExtractor{file: "Reactions.csv", kind: "reaction"},
		csvExtractor{file: "Invitations.csv", kind: "invitation"},
		csvExtractor{file: "SearchQueries.csv", kind: "search"},
		csvExtractor{file: "Positions.csv", kind: "position"},
		csvExtractor{file: "Education.csv", kind: "education"},
		csvExtractor{file: "Company Follows.csv", kind: "follow"},
		csvExtractor{file: "Company_Follows.csv", kind: "follow"}, // spelling in newer exports
		// Headers of the next three are not publicly documented; columns are
		// picked by the names LinkedIn uses elsewhere and the whole row is kept.
		csvExtractor{file: "Member_Follows.csv", kind: "follow"},
		csvExtractor{file: "Hashtag_Follows.csv", kind: "follow"},
		csvExtractor{file: "Saved_Items.csv", kind: "save"},
	}
}

// ---- extraction ----

// csvExtractor reads one CSV member. header, if set, is a lower-case fragment
// that identifies the real header row (see skipPreamble).
type csvExtractor struct {
	file   string
	kind   string
	header string
}

// Match accepts the file by name, and the "<name>_<member id>.csv" form the
// complete archive uses for some files (Shares_506219023.csv).
func (e csvExtractor) Match(memberPath string) bool {
	b := strings.ToLower(path.Base(memberPath))
	want := strings.ToLower(e.file)
	if b == want {
		return true
	}
	stem := strings.TrimSuffix(want, ".csv")
	rest, ok := strings.CutPrefix(strings.TrimSuffix(b, ".csv"), stem+"_")
	if !ok || rest == "" || !strings.HasSuffix(b, ".csv") {
		return false
	}
	_, err := strconv.ParseUint(rest, 10, 64)
	return err == nil
}

func (e csvExtractor) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	body, head, err := skipPreamble(r, e.header)
	if err != nil {
		return err
	}
	if body == nil {
		// No header row: the member is not the CSV we know. Keep a trace of it
		// rather than guessing at columns.
		payload, err := json.Marshal(map[string]string{
			"member": member, "reason": "no header row found", "preview": strings.ToValidUTF8(string(head), ""),
		})
		if err != nil {
			return err
		}
		return emit(rec("unknown", payload))
	}

	keys := map[string]string{} // header as written -> normalized, per Extract
	return bundle.ReadCSV(body, func(row map[string]string) error {
		norm := make(map[string]string, len(row))
		empty := true
		for k, v := range row {
			nk, ok := keys[k]
			if !ok {
				nk = normKey(k)
				keys[k] = nk
			}
			if nk == "" {
				continue
			}
			if v = strings.TrimSpace(v); v != "" {
				empty = false
			}
			if norm[nk] == "" {
				norm[nk] = v
			}
		}
		if empty {
			return nil
		}
		payload, err := json.Marshal(norm) // map keys are sorted: deterministic
		if err != nil {
			return err
		}
		return emit(rec(e.kind, payload))
	})
}

// rec builds a RawRecord whose ContentHash covers the kind as well as the
// payload: the importer dedupes on that hash across the whole archive, and two
// files can hold identical rows that mean different things.
func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

// normKey makes a header name comparable: BOM and whitespace stripped,
// lower-cased ("CONVERSATION ID" -> "conversation id").
func normKey(k string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(k, "\ufeff")))
}

const (
	preambleBuf     = 64 << 10 // longest line that can still be a header
	preamblePreview = 240
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// skipPreamble returns a reader positioned at the CSV header row, dropping any
// preamble LinkedIn put before it. Connections.csv, for one, starts with
//
//	Notes:
//	"When exporting your connection data, you may notice that …"
//	<blank line>
//	First Name,Last Name,URL,Email Address,Company,Position,Connected On
//
// It works line by line on a bounded buffer, so the member is never held in
// memory. With a header fragment, lines are dropped until one contains it
// (case-insensitively); a member that starts with its header passes through
// untouched. Without one, only an explicit "Notes:" first line counts as a
// preamble, and the header is the first non-blank line after the next blank one.
//
// If no header row turns up, body is nil and head holds the first bytes of the
// member for diagnostics.
func skipPreamble(r io.Reader, header string) (body io.Reader, head []byte, err error) {
	br := bufio.NewReaderSize(r, preambleBuf)
	if header == "" {
		peek, _ := br.Peek(len(utf8BOM) + len("Notes:") + 1) // a short read surfaces later
		peek = bytes.TrimPrefix(peek, utf8BOM)
		if !bytes.HasPrefix(peek, []byte("Notes:")) || len(peek) <= len("Notes:") ||
			(peek[len("Notes:")] != '\r' && peek[len("Notes:")] != '\n') {
			return br, nil, nil
		}
	}

	afterBlank := false
	for {
		line, err := br.ReadSlice('\n')
		if len(head) < preamblePreview {
			head = append(head, line[:min(len(line), preamblePreview-len(head))]...)
		}
		blank := len(bytes.TrimSpace(line)) == 0
		if err == bufio.ErrBufferFull {
			// Over-long line: not a header. Drop the rest of it (line is invalid
			// from here on).
			for err == bufio.ErrBufferFull {
				_, err = br.ReadSlice('\n')
			}
		} else if !blank {
			found := afterBlank
			if header != "" {
				found = bytes.Contains(bytes.ToLower(line), []byte(header))
			}
			if found {
				return io.MultiReader(bytes.NewReader(bytes.Clone(line)), br), nil, nil
			}
		}
		if err == io.EOF {
			return nil, head, nil
		}
		if err != nil {
			return nil, nil, err
		}
		afterBlank = afterBlank || blank
	}
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	var row map[string]string
	if err := json.Unmarshal(raw.Payload, &row); err != nil {
		// Never drop: a payload that is not a row becomes an unknown event.
		return []domain.Event{{
			SourceID: sourceID, Type: "linkedin.unknown", Category: domain.CategoryOther,
			Title: "Unrecognized LinkedIn record", ExternalID: "unknown:" + digest(raw.Kind, string(raw.Payload)),
			Metadata: map[string]any{"member": raw.Member, "reason": "malformed row: " + err.Error(), "preview": strings.ToValidUTF8(string(raw.Payload), "")},
		}}, nil
	}
	ev := domain.Event{SourceID: sourceID, Actor: "self", Metadata: map[string]any{}}
	if raw.Member != "" {
		ev.Metadata["member"] = raw.Member
	}

	switch raw.Kind {
	case "connection":
		name := fullName(pick(row, "first name", "firstname"), pick(row, "last name", "lastname"))
		profile := pick(row, "url", "profile url")
		when := pick(row, "connected on", "connected at")
		ev.Type, ev.Category = "linkedin.connection", domain.CategoryConnection
		ev.OccurredAt = parseTime(when)
		ev.Title = "Connected with " + firstNonEmpty(name, profile, "a LinkedIn member")
		ev.Targets = []domain.Target{{
			Relation: domain.RelationFollowed, TargetSource: sourceID,
			TargetExternalID: firstNonEmpty(profile, name), TargetURL: profile,
		}}
		// Members who hide their profile from exports have no URL.
		ev.ExternalID = "connection:" + firstNonEmpty(profile, digest(name, pick(row, "company"), when))
		meta(ev, "name", name)
		meta(ev, "email", pick(row, "email address", "email"))
		meta(ev, "company", pick(row, "company"))
		meta(ev, "position", pick(row, "position"))

	case "message":
		conv := pick(row, "conversation id")
		from := pick(row, "from")
		when := pick(row, "date")
		body := pick(row, "content", "message")
		ev.Type, ev.Category = "linkedin.message", domain.CategoryMessage
		ev.OccurredAt = parseTime(when)
		ev.Actor = firstNonEmpty(from, "self")
		ev.Body = body
		ev.ThreadExternalID = conv
		ev.Title = firstNonEmpty(pick(row, "subject"), pick(row, "conversation title"), "Message from "+firstNonEmpty(from, "a LinkedIn member"))
		ev.ExternalID = "message:" + conv + ":" + stamp(ev.OccurredAt, when) + ":" + digest(from, body)
		meta(ev, "to", pick(row, "to"))
		meta(ev, "sender_url", pick(row, "sender profile url"))
		meta(ev, "recipient_urls", pick(row, "recipient profile urls"))
		meta(ev, "conversation_title", pick(row, "conversation title"))
		meta(ev, "folder", pick(row, "folder"))
		meta(ev, "attachments", pick(row, "attachments"))
		if d := strings.ToLower(pick(row, "is message draft")); d == "yes" || d == "true" {
			ev.Metadata["draft"] = true
			ev.Title = "Draft: " + ev.Title
		}

	case "post":
		link := pick(row, "sharelink", "share link")
		when := pick(row, "date")
		shared := pick(row, "sharedurl", "shared url")
		ev.Type, ev.Category = "linkedin.post", domain.CategoryContent
		ev.OccurredAt = parseTime(when)
		ev.Body = pick(row, "sharecommentary", "share commentary")
		ev.Title = "Shared a post"
		ev.ExternalID = "post:" + firstNonEmpty(link, stamp(ev.OccurredAt, when)+":"+digest(ev.Body, shared))
		ev.ThreadExternalID = link
		if shared != "" {
			ev.Targets = []domain.Target{{Relation: domain.RelationShared, TargetURL: shared}}
		}
		meta(ev, "url", link)
		meta(ev, "shared_url", shared)
		meta(ev, "media_url", pick(row, "mediaurl", "media url"))
		meta(ev, "visibility", pick(row, "visibility"))

	case "comment":
		link := pick(row, "link")
		when := pick(row, "date")
		ev.Type, ev.Category = "linkedin.comment", domain.CategoryContent
		ev.OccurredAt = parseTime(when)
		ev.Body = pick(row, "message", "comment")
		ev.Title = "Commented on a post"
		ev.ExternalID = "comment:" + stamp(ev.OccurredAt, when) + ":" + digest(link, ev.Body)
		if link != "" {
			ev.Targets = []domain.Target{{
				Relation: domain.RelationRepliedTo, TargetSource: sourceID,
				TargetExternalID: urn(link), TargetURL: link,
			}}
		}
		meta(ev, "url", link)

	case "reaction":
		link := pick(row, "link")
		when := pick(row, "date")
		kind := strings.ToUpper(pick(row, "type", "reaction"))
		ev.Type, ev.Category = "linkedin.reaction", domain.CategoryReaction
		ev.OccurredAt = parseTime(when)
		ev.Title = "Reacted to a post"
		if kind != "" {
			ev.Title = "Reacted with " + kind
		}
		ev.Targets = []domain.Target{{
			Relation: domain.RelationLiked, TargetSource: sourceID,
			TargetExternalID: urn(link), TargetURL: link,
		}}
		ev.ExternalID = "reaction:" + stamp(ev.OccurredAt, when) + ":" + digest(kind, link)
		meta(ev, "reaction", kind)

	case "invitation":
		from, to := pick(row, "from"), pick(row, "to")
		when := pick(row, "sent at", "date")
		dir := strings.ToUpper(pick(row, "direction"))
		ev.Type, ev.Category = "linkedin.invitation", domain.CategoryConnection
		ev.OccurredAt = parseTime(when)
		ev.Body = pick(row, "message")
		other := pick(row, "inviteeprofileurl", "invitee profile url")
		ev.Title = "Invited " + firstNonEmpty(to, "a LinkedIn member") + " to connect"
		if dir == "INCOMING" {
			ev.Actor = firstNonEmpty(from, "self")
			ev.Title = "Invitation to connect from " + firstNonEmpty(from, "a LinkedIn member")
			other = pick(row, "inviterprofileurl", "inviter profile url")
		}
		if other != "" {
			ev.Targets = []domain.Target{{
				Relation: domain.RelationFollowed, TargetSource: sourceID,
				TargetExternalID: other, TargetURL: other,
			}}
		}
		ev.ExternalID = "invitation:" + stamp(ev.OccurredAt, when) + ":" + digest(dir, from, to)
		meta(ev, "direction", dir)
		meta(ev, "from", from)
		meta(ev, "to", to)

	case "search":
		when := pick(row, "time", "date")
		ev.Type, ev.Category = "linkedin.search", domain.CategoryConsumption
		ev.OccurredAt = parseTime(when)
		ev.Title = "Searched LinkedIn"
		ev.Body = pick(row, "search query", "query")
		ev.ExternalID = "search:" + stamp(ev.OccurredAt, when) + ":" + digest(ev.Body)

	case "position":
		company, title := pick(row, "company name", "company"), pick(row, "title")
		start := pick(row, "started on", "start date")
		ev.Type, ev.Category = "linkedin.position", domain.CategoryProfile
		ev.OccurredAt = parseTime(start)
		ev.Title = join(" at ", title, company)
		ev.Body = pick(row, "description")
		ev.ExternalID = "position:" + digest(company, title, start)
		meta(ev, "company", company)
		meta(ev, "location", pick(row, "location"))
		meta(ev, "started_on", start)
		meta(ev, "finished_on", pick(row, "finished on", "end date"))

	case "education":
		school, degree := pick(row, "school name", "school"), pick(row, "degree name", "degree")
		start := pick(row, "start date", "started on")
		ev.Type, ev.Category = "linkedin.education", domain.CategoryProfile
		ev.OccurredAt = parseTime(start)
		ev.Title = join(" at ", degree, school)
		ev.Body = pick(row, "notes")
		ev.ExternalID = "education:" + digest(school, degree, start)
		meta(ev, "school", school)
		meta(ev, "degree", degree)
		meta(ev, "activities", pick(row, "activities"))
		meta(ev, "start_date", start)
		meta(ev, "end_date", pick(row, "end date", "finished on"))

	case "follow":
		org := pick(row, "organization", "company", "name", "full name", "member", "hashtag", "hashtag name")
		link := pick(row, "url", "profile url", "link")
		when := pick(row, "followed on", "date", "followed at")
		ev.Type, ev.Category = "linkedin.follow", domain.CategoryConnection
		ev.OccurredAt = parseTime(when)
		what := "a company"
		switch b := strings.ToLower(path.Base(raw.Member)); {
		case strings.HasPrefix(b, "member_follows"):
			what, ev.Metadata["kind"] = "a member", "member"
		case strings.HasPrefix(b, "hashtag_follows"):
			what, ev.Metadata["kind"] = "a hashtag", "hashtag"
			if org != "" && !strings.HasPrefix(org, "#") {
				org = "#" + org
			}
		default:
			ev.Metadata["kind"] = "company"
		}
		ev.Title = "Followed " + firstNonEmpty(org, what)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: sourceID, TargetExternalID: firstNonEmpty(org, link), TargetURL: link}}
		ev.ExternalID = "follow:" + ev.Metadata["kind"].(string) + ":" + digest(firstNonEmpty(org, link, string(raw.Payload)))
		if ev.Metadata["kind"] != "company" {
			ev.Metadata["row"] = row // header not publicly documented: keep everything
		}

	case "save":
		link := pick(row, "url", "link", "saved item url", "post url", "item url")
		when := pick(row, "saved at", "saved on", "date", "time")
		ev.Type, ev.Category = "linkedin.save", domain.CategorySave
		ev.OccurredAt = parseTime(when)
		ev.Title = "Saved an item"
		if u := urn(link); u != link && u != "" {
			ev.Title = "Saved " + u
		}
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: sourceID, TargetExternalID: firstNonEmpty(urn(link), link), TargetURL: link}}
		ev.ExternalID = "save:" + firstNonEmpty(link, stamp(ev.OccurredAt, when)+":"+digest(string(raw.Payload)))
		ev.Metadata["row"] = row // header not publicly documented: keep everything

	default:
		ev.Type, ev.Category = "linkedin.unknown", domain.CategoryOther
		ev.Title = "Unrecognized LinkedIn record"
		ev.ExternalID = "unknown:" + digest(string(raw.Payload))
		for k, v := range row {
			meta(ev, k, v)
		}
	}
	if ev.Title == "" {
		ev.Title = "LinkedIn " + raw.Kind
	}
	return []domain.Event{ev}, nil
}

// ---- helpers ----

func meta(ev domain.Event, k, v string) {
	if v != "" {
		ev.Metadata[k] = v
	}
}

// pick returns the first non-empty value among the given (lower-case) columns.
func pick(row map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(row[k]); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fullName(first, last string) string { return join(" ", first, last) }

// join joins the non-empty parts.
func join(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}

// digest is a short stable hash of the fields that tell two records apart.
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// stamp is the timestamp part of an ExternalID: unix seconds, or the raw text
// when it could not be parsed (still stable across re-imports).
func stamp(t time.Time, raw string) string {
	if t.IsZero() {
		return raw
	}
	return strconv.FormatInt(t.Unix(), 10)
}

// urn extracts the LinkedIn URN from an activity link
// (https://www.linkedin.com/feed/update/urn%3Ali%3Aactivity%3A123?x=y ->
// urn:li:activity:123), or returns the link itself.
func urn(link string) string {
	s, err := url.PathUnescape(link)
	if err != nil {
		return link
	}
	i := strings.Index(s, "urn:li:")
	if i < 0 {
		return link
	}
	s = s[i:]
	if j := strings.IndexAny(s, "/?#"); j >= 0 {
		s = s[:j]
	}
	return s
}

var timeLayouts = []string{
	"2006-01-02 15:04:05 MST",      // messages, Shares, Comments, Reactions: 2024-01-31 12:34:56 UTC
	"2 Jan 2006",                   // Connections: 31 Jan 2024
	"1/2/06, 3:04 PM",              // Invitations: 1/31/24, 12:34 PM
	"Jan 2006",                     // Positions, Education: Jan 2020
	"2006/01/02 15:04:05 MST",      // SearchQueries (some vintages)
	"Mon Jan _2 15:04:05 MST 2006", // Company Follows: Wed Jan 31 12:34:56 UTC 2024
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	time.RFC3339,
	"2006-01-02",
	"1/2/2006, 3:04 PM",
	"January 2006",
	"2006-01", // Positions/Education in some vintages: 2021-03
	"2006",    // Education: year only
}

// spaces folds the no-break spaces newer exports put before AM/PM.
var spaces = strings.NewReplacer("\u202f", " ", "\u00a0", " ")

// parseTime parses any of the date formats the export uses, as UTC. Values
// without a zone (everything but the "… UTC" ones) are taken as UTC; partial
// dates resolve to the start of the month or year. Unparseable -> zero time.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(spaces.Replace(s))
	if s == "" || strings.EqualFold(s, "present") {
		return time.Time{}
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
