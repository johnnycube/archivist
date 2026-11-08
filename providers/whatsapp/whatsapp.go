// Package whatsapp ingests a WhatsApp "Export chat" ZIP. WhatsApp has no
// account-wide export: each ZIP holds ONE chat as a plain-text transcript
// (_chat.txt on iOS, "WhatsApp Chat with <name>.txt" on Android) plus the
// optional media files it refers to.
//
// The transcript format is undocumented and locale dependent, so parsing is
// tolerant but deterministic: a line opening with a timestamp header starts a
// message, every other line continues the previous one, and the day/month
// order is decided once per file (see parse.go). The transcript carries no
// timezone; timestamps are treated as UTC and marked as such in the metadata.
//
// The chat's name comes from the transcript's name (Android) or its directory.
// An iOS export has neither — _chat.txt sits at the ZIP root — and names the
// chat only in the ZIP's own file name ("WhatsApp Chat - Book Club.zip"), which
// is used when the upload name is known (RawRecord.Archive). The chat name is
// part of every ExternalID and of the ThreadExternalID, so such a chat imported
// under its original file name and again under a name that says nothing
// ("export.zip", or no name at all) yields two sets of events.
package whatsapp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

const (
	sourceID = "whatsapp"

	// maxLineBytes bounds one transcript line and maxBodyBytes one (multi-line)
	// message; WhatsApp caps a message at 65,536 characters, so longer input is
	// not a transcript. The excess is cut and the message marked "truncated".
	maxLineBytes = 1 << 20
	maxBodyBytes = 4 << 20

	// Messages are held back until the file's day/month order is known, but
	// never more than this; then the order is guessed and streaming resumes.
	maxPendingMsgs  = 5000
	maxPendingBytes = 4 << 20

	previewBytes = 240
	unknownTZ    = "unknown (treated as UTC)"

	// defaultChat names a chat whose transcript path says nothing about it.
	defaultChat = "WhatsApp chat"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

var (
	_ provider.BundleProvider    = (*Provider)(nil)
	_ provider.AttachmentMatcher = (*Provider)(nil)
)

func (p *Provider) Descriptor() domain.Descriptor {
	return domain.Descriptor{
		ID:             sourceID,
		DisplayName:    "WhatsApp",
		IngestionModes: []domain.Mode{domain.ModeBundle},
		EventTypes: []domain.TypeDef{
			{Type: "whatsapp.message", Category: domain.CategoryMessage, HasBody: true},
			{Type: "whatsapp.system", Category: domain.CategoryOther, HasBody: true},
			{Type: "whatsapp.unknown", Category: domain.CategoryOther},
		},
	}
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{SupportsBundle: true}
}

// Extractors: every .txt member is read as a transcript. Transcript names are
// localized (even into non-Latin scripts), so the name cannot be relied on; a
// .txt without any timestamp header (a shared document, say) still ends up as
// whatsapp.unknown, exactly as the importer's generic capture would make it.
func (p *Provider) Extractors() []provider.Extractor {
	return []provider.Extractor{chatEx{}}
}

// IsAttachment implements provider.AttachmentMatcher: an export holds one
// transcript and the files its messages refer to — photos and voice notes, but
// also PDFs, vCards, stickers — so every member that is not a transcript is
// stored under the key the message's Media.BlobKey carries.
func (p *Provider) IsAttachment(member string) bool { return !chatEx{}.Match(member) }

// ---- extraction ----

// rawMsg is the payload of one transcript entry. Seq keeps two identical
// same-minute messages apart for the importer's content dedupe; RawDate keeps
// the timestamp exactly as written.
type rawMsg struct {
	Chat      string `json:"chat"`
	Seq       int    `json:"seq"`  // entry index within the file
	Line      int    `json:"line"` // line of the header
	Format    string `json:"format"`
	RawDate   string `json:"raw_date"`
	Unix      int64  `json:"unix"`
	DateOrder string `json:"date_order"`
	Guessed   bool   `json:"date_order_guessed,omitempty"`
	Basis     string `json:"date_order_basis,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type rawUnknown struct {
	Member  string `json:"member"`
	Bytes   int64  `json:"bytes"`
	Hash    string `json:"content_hash"`
	Preview string `json:"preview"`
}

func rec(kind string, payload []byte) provider.RawRecord {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(payload)
	return provider.RawRecord{Kind: kind, Payload: payload, ContentHash: hex.EncodeToString(h.Sum(nil))}
}

type chatEx struct{}

func (chatEx) Match(member string) bool {
	return strings.EqualFold(path.Ext(member), ".txt")
}

func (chatEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	s := &scan{member: member, chat: chatName(member), emit: emit, occ: newOccurrences(), preHash: sha256.New()}
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, truncated, err := readLine(br)
		if err != nil && err != io.EOF {
			return err
		}
		if len(line) > 0 || err == nil {
			if lerr := s.line(line, truncated); lerr != nil {
				return lerr
			}
		}
		if err == io.EOF {
			return s.finish()
		}
	}
}

// readLine returns the next line without its line ending. Bytes beyond
// maxLineBytes are read and discarded, so a hostile member cannot balloon memory.
func readLine(br *bufio.Reader) (line []byte, truncated bool, err error) {
	for {
		chunk, e := br.ReadSlice('\n')
		if room := maxLineBytes - len(line); len(chunk) > room {
			chunk, truncated = chunk[:room], true
		}
		line = append(line, chunk...)
		if e == bufio.ErrBufferFull {
			continue
		}
		if !truncated {
			line = bytes.TrimRight(line, "\r\n")
		}
		return line, truncated, e
	}
}

type entry struct {
	hdr       header
	line      int
	sender    string
	text      []byte
	truncated bool
}

// scan is the per-Extract state (the provider itself stays stateless).
type scan struct {
	member, chat string
	emit         func(provider.RawRecord) error

	lineNo int
	seq    int
	cur    *entry
	occ    *occurrences

	// Day/month order of the file: "" while undecided (entries wait in
	// pending), then a guess until a header proves it.
	order, basis string
	proven       bool
	pending      []*entry
	pendingBytes int

	// Lines before the first header are not a message; they are hashed and
	// previewed into one whatsapp.unknown record.
	preHash  hash.Hash
	preHead  []byte
	preBytes int64
	preText  bool
}

func (s *scan) line(b []byte, truncated bool) error {
	s.lineNo++
	text := strings.ToValidUTF8(string(b), "\uFFFD")
	if h, ok := parseHeader(text); ok {
		if err := s.closeCur(); err != nil {
			return err
		}
		e := &entry{hdr: h, line: s.lineNo, truncated: truncated}
		e.sender, e.text = splitSender(h.rest)
		s.cur = e
		if o := h.order(); o != "" && !s.proven {
			s.order, s.basis, s.proven = o, "", true
			return s.flush()
		}
		return nil
	}
	if s.cur == nil {
		s.preHash.Write(b)
		s.preHash.Write([]byte{'\n'})
		s.preBytes += int64(len(b)) + 1
		if room := previewBytes - len(s.preHead); room > 0 {
			s.preHead = append(s.preHead, (text + "\n")[:min(room, len(text)+1)]...)
		}
		s.preText = s.preText || strings.TrimSpace(stripMarks(text)) != ""
		return nil
	}
	if room := maxBodyBytes - len(s.cur.text); room > len(text) {
		s.cur.text = append(append(s.cur.text, '\n'), text...)
		s.cur.truncated = s.cur.truncated || truncated
	} else {
		s.cur.truncated = true
	}
	return nil
}

func (s *scan) closeCur() error {
	e := s.cur
	s.cur = nil
	if e == nil {
		return nil
	}
	if s.order != "" {
		return s.emitEntry(e)
	}
	s.pending = append(s.pending, e)
	s.pendingBytes += len(e.text)
	if len(s.pending) >= maxPendingMsgs || s.pendingBytes >= maxPendingBytes {
		return s.guessAndFlush()
	}
	return nil
}

func (s *scan) guessAndFlush() error {
	hs := make([]header, len(s.pending))
	for i, e := range s.pending {
		hs[i] = e.hdr
	}
	s.order, s.basis = guessOrder(hs)
	return s.flush()
}

func (s *scan) flush() error {
	for _, e := range s.pending {
		if err := s.emitEntry(e); err != nil {
			return err
		}
	}
	s.pending, s.pendingBytes = nil, 0
	return nil
}

func (s *scan) finish() error {
	if err := s.closeCur(); err != nil {
		return err
	}
	if len(s.pending) > 0 {
		if err := s.guessAndFlush(); err != nil {
			return err
		}
	}
	if !s.preText {
		return nil
	}
	size := s.preBytes
	pv := strings.TrimSpace(strings.ToValidUTF8(string(s.preHead), ""))
	if size > int64(len(s.preHead)) {
		pv += "…"
	}
	payload, err := json.Marshal(rawUnknown{Member: s.member, Bytes: size, Hash: hex.EncodeToString(s.preHash.Sum(nil)), Preview: pv})
	if err != nil {
		return err
	}
	raw := rec("unknown", payload)
	raw.Member = s.member
	raw.ExternalID = sourceID + ":unknown:" + s.member
	return s.emit(raw)
}

func (s *scan) emitEntry(e *entry) error {
	order, guessed, basis := e.hdr.order(), false, ""
	if order == "" {
		switch {
		case s.order == orderYMD: // a day/month line in a year-first file
			order, guessed, basis = separatorOrder([]header{e.hdr}), true, basisSeparator
		default:
			order, guessed, basis = s.order, !s.proven, s.basis
		}
	}
	t := e.hdr.time(order)
	s.seq++
	m := rawMsg{
		Chat: s.chat, Seq: s.seq, Line: e.line, Format: e.hdr.format(), RawDate: e.hdr.raw,
		Unix: t.Unix(), DateOrder: order, Guessed: guessed, Basis: basis,
		Sender: e.sender, Text: string(e.text), Truncated: e.truncated,
	}
	kind := "message"
	if m.Sender == "" || isSystemText(m.Text) {
		kind = "system"
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	raw := rec(kind, payload)
	raw.Member = s.member
	raw.OccurredAt = t
	raw.ExternalID = s.occ.externalID(kind, m)
	return s.emit(raw)
}

// occurrences numbers identical (chat, timestamp, sender, text) tuples so two
// real "ok"s in one minute get distinct ExternalIDs, while the ids stay the
// same when a later, longer export of the chat is imported (they do not depend
// on line numbers). Transcripts are chronological apart from small delivery
// jitter, so tuples older than a day behind the newest timestamp are pruned to
// keep the map small on huge chats.
type occurrences struct {
	seen    map[[10]byte]occurrence
	newest  int64
	pruneAt int
}

type occurrence struct {
	n    int
	unix int64
}

func newOccurrences() *occurrences {
	return &occurrences{seen: map[[10]byte]occurrence{}, pruneAt: 4096}
}

func (o *occurrences) externalID(kind string, m rawMsg) string {
	body := sha256.Sum256([]byte(m.Text))
	h := sha256.New()
	for _, part := range []string{m.Chat, strconv.FormatInt(m.Unix, 10), m.Sender} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(body[:])
	var key [10]byte
	copy(key[:], h.Sum(nil))

	occ := o.seen[key]
	occ.n, occ.unix = occ.n+1, m.Unix
	o.seen[key] = occ
	o.newest = max(o.newest, m.Unix)
	if len(o.seen) >= o.pruneAt {
		for k, v := range o.seen {
			if v.unix < o.newest-86400 {
				delete(o.seen, k)
			}
		}
		o.pruneAt = max(4096, 2*len(o.seen))
	}
	prefix := "msg:"
	if kind == "system" {
		prefix = "sys:"
	}
	return prefix + hex.EncodeToString(key[:]) + ":" + strconv.Itoa(occ.n)
}

// ---- normalization ----

func (p *Provider) Normalize(_ context.Context, raw provider.RawRecord) ([]domain.Event, error) {
	switch raw.Kind {
	case "unknown":
		var u rawUnknown
		if err := json.Unmarshal(raw.Payload, &u); err != nil {
			return nil, err
		}
		return []domain.Event{{
			SourceID: sourceID, ExternalID: raw.ExternalID, Type: "whatsapp.unknown", Category: domain.CategoryOther,
			Title:    "Unmapped export data: " + u.Member,
			Metadata: map[string]any{"member": u.Member, "bytes": u.Bytes, "content_hash": u.Hash, "preview": u.Preview},
		}}, nil
	case "message", "system":
	default:
		return nil, nil
	}

	var m rawMsg
	if err := json.Unmarshal(raw.Payload, &m); err != nil {
		return nil, err
	}
	if name := archiveChatName(raw.Archive); m.Chat == defaultChat && name != "" {
		// The extractor sees only the member path and built the id without the
		// name; fold it in so two unnamed-inside chats do not share ids.
		m.Chat, raw.ExternalID = name, rekey(raw.ExternalID, name)
	}
	ev := domain.Event{
		SourceID:         sourceID,
		ExternalID:       raw.ExternalID,
		OccurredAt:       time.Unix(m.Unix, 0).UTC(),
		ThreadExternalID: "chat:" + m.Chat,
		Metadata: map[string]any{
			"chat": m.Chat, "timezone": unknownTZ, "raw_date": m.RawDate, "export_format": m.Format,
		},
	}
	if m.Guessed {
		ev.Metadata["date_order"] = m.DateOrder
		ev.Metadata["date_order_guessed"] = true
		ev.Metadata["date_order_basis"] = m.Basis
	}
	if m.Truncated {
		ev.Metadata["truncated"] = true
	}

	text := strings.TrimSpace(stripMarks(m.Text))
	if raw.Kind == "system" {
		ev.Type, ev.Category = "whatsapp.system", domain.CategoryOther
		ev.Title = "System message in " + m.Chat
		ev.Body = text
		if m.Sender != "" { // iOS attributes some notices to the chat or contact
			ev.Metadata["attributed_to"] = m.Sender
		}
		return []domain.Event{ev}, nil
	}

	ev.Type, ev.Category = "whatsapp.message", domain.CategoryMessage
	ev.Title = "Message in " + m.Chat
	ev.Actor = m.Sender

	if loc := editedRe.FindStringIndex(text); loc != nil {
		text = strings.TrimSpace(text[:loc[0]])
		ev.Metadata["edited"] = true
	}
	file, rest := attachment(text)
	switch {
	case file != "":
		text = rest
		ev.Metadata["attachment"] = file
		ev.Media = []domain.Media{{BlobKey: path.Join(path.Dir(raw.Member), file)}}
	case isMediaOmitted(text, strings.HasPrefix(m.Text, "‎")):
		ev.Metadata["media_omitted"] = true
	case isDeleted(text):
		ev.Metadata["deleted"] = true
	}
	ev.Body = text
	return []domain.Event{ev}, nil
}

// Localized WhatsApp UI strings. Evidence: whatsapp-chat-parser's fixtures
// document "(file attached)", "(Datei angehängt)", "<attached: …>",
// "<‎attached>" and "<Media omitted>"; the other spellings are WhatsApp's own
// translations of those strings (en, de, es, fr, it, pt-BR, pt-PT, nl, tr) and
// are matched loosely (key words, not whole sentences) so a variant off by a
// word still counts.
var (
	// "<This message was edited>", "<Diese Nachricht wurde bearbeitet.>",
	// "<Se editó este mensaje.>", "<Ce message a été modifié.>", …: a trailing
	// bracketed note that says "edited" in one of the known languages.
	editedRe = regexp.MustCompile(`\s*<[^<>\n]{0,60}(?i:edited|bearbeitet|editó|editado|editada|modifié|modificato|bewerkt|düzenlendi|отредактировано)[^<>\n]{0,40}>$`)

	// "This message was deleted", "You deleted this message" and WhatsApp's
	// translations (de, es, fr, it, pt-BR, pt-PT, nl, tr), with or without the
	// final period iOS adds.
	deletedRe = regexp.MustCompile(`(?i)^(?:this message was deleted|you deleted this message|diese nachricht wurde gelöscht|du hast diese nachricht gelöscht|se eliminó este mensaje|eliminaste este mensaje|ce message a été supprimé|vous avez supprimé ce message|questo messaggio è stato eliminato|hai eliminato questo messaggio|mensagem apagada|você apagou esta mensagem|esta mensagem foi eliminada|eliminou esta mensagem|dit bericht is verwijderd|je hebt dit bericht verwijderd|bu mesaj silindi|bu mesajı sildiniz)\.?$`)

	// iOS: "<attached: 00000012-PHOTO-2022-04-15-14-03-21.jpg>", "<Anhang: …>",
	// "<pièce jointe : …>", also written "< attached: … >" with inner spaces.
	iosAttachRe = regexp.MustCompile(`^<\s*[^<>:\n]{1,40}:\s*([^<>\n/\\]+\.[A-Za-z0-9]{1,8})\s*>|<\s*[^<>:\n]{1,40}:\s*([^<>\n/\\]+\.[A-Za-z0-9]{1,8})\s*>$`)
	// Android: "IMG-20220415-WA0001.jpg (file attached)" on the first line, an
	// optional caption on the following ones; old iOS:
	// "2015-08-04-PHOTO-00004762.jpg <attached>".
	androidAttachRe = regexp.MustCompile(`^([^\n/\\<>()]+\.[A-Za-z0-9]{1,8}) (?:\(([^()\n]+)\)|<([^<>\n]+)>)(?:\n|$)`)
	waFileRe        = regexp.MustCompile(`^[A-Z]{3}-\d{8}-WA\d{4,}\.`)
	iosOldFileRe    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-[A-Z]+-\d+\.`)
	// Android: "<Media omitted>", "<Medien ausgeschlossen>", "<Multimedia
	// omitido>", "<Médias omis>", "<Media omessi>", "<Mídia omitida>",
	// "<Multimédia omitida>", "<Media weggelaten>", "<Medya dahil edilmedi>" — a
	// whole message that is two to four bracketed words.
	androidOmittedRe = regexp.MustCompile(`^<\pL[^<>:\n ]*(?: [^<>:\n ]+){1,3}>$`)
	// iOS: "image omitted", "Contact card omitted", "Bild weggelassen", "imagen
	// omitida", "vidéo absente", "audio omis", "immagine omessa", "figurinha
	// omitida", "afbeelding weggelaten", … — one or two words and the verb.
	omittedWords     = `(?:omitted|weggelassen|omitid[oa]|omis|omise|absente|omess[oai]|ocultad[oa]|weggelaten|dahil edilmedi|não incluíd[oa])`
	iosOmittedRe     = regexp.MustCompile(`(?i)^\pL+(?: \pL+)? ` + omittedWords + `$`)
	iosOmittedTailRe = regexp.MustCompile(`(?i)^[^\n]* ` + omittedWords + `$`)

	// The end-to-end encryption notice, which iOS attributes to the chat:
	// "Messages and calls are end-to-end encrypted. …", "Nachrichten und Anrufe
	// sind Ende-zu-Ende-verschlüsselt.", "Los mensajes y las llamadas están
	// cifrados de extremo a extremo.", "Les messages et les appels sont chiffrés
	// de bout en bout.", "I messaggi e le chiamate sono crittografati
	// end-to-end.", "As mensagens e as chamadas são protegidas com a
	// criptografia de ponta a ponta.", "Berichten en gesprekken worden
	// end-to-end versleuteld.", "Mesajlar ve aramalar uçtan uca şifrelidir."
	e2eWords = `(?:end-to-end|ende-zu-ende|extremo a extremo|bout en bout|ponta a ponta|uçtan uca)`
	encWords = `(?:encrypt|verschlüsselt|cifrad|chiffr|crittograf|criptograf|versleuteld|şifreli)`
	e2eRe    = regexp.MustCompile(`(?i)` + e2eWords + `.{0,40}` + encWords + `|` + encWords + `.{0,40}` + e2eWords)
)

// Known spellings of Android's "(file attached)" marker. Files named the
// WhatsApp way (IMG-20220415-WA0001.jpg, 2015-08-04-PHOTO-00004762.jpg) count
// in any language, and so does any "<…>" marker after a file name.
var attachedLabels = map[string]bool{
	"file attached": true, "datei angehängt": true, "archivo adjunto": true, "fichier joint": true,
	"file allegato": true, "arquivo anexado": true, "ficheiro anexado": true, "bestand bijgevoegd": true,
	"dosya ekli": true, "attached": true,
}

// attachment returns the attached file's name and the text without the marker.
func attachment(text string) (file, rest string) {
	if loc := iosAttachRe.FindStringSubmatchIndex(text); loc != nil {
		g := 2
		if loc[g] < 0 {
			g = 4
		}
		file = strings.TrimSpace(text[loc[g]:loc[g+1]])
		return file, strings.TrimSpace(text[:loc[0]] + text[loc[1]:])
	}
	if mm := androidAttachRe.FindStringSubmatch(text); mm != nil {
		label := strings.ToLower(strings.TrimSpace(mm[2] + mm[3]))
		if attachedLabels[label] || mm[3] != "" || waFileRe.MatchString(mm[1]) || iosOldFileRe.MatchString(mm[1]) {
			return strings.TrimSpace(mm[1]), strings.TrimSpace(text[len(mm[0]):])
		}
	}
	return "", text
}

// isMediaOmitted recognizes the placeholder of an export made "without media".
// marked reports whether WhatsApp's own left-to-right mark preceded the text.
func isMediaOmitted(text string, marked bool) bool {
	return androidOmittedRe.MatchString(text) || iosOmittedRe.MatchString(text) ||
		(marked && iosOmittedTailRe.MatchString(text))
}

// isDeleted recognizes the placeholder left by a deleted message.
func isDeleted(text string) bool { return deletedRe.MatchString(text) }

// isSystemText catches the notice that iOS writes with a sender in front.
func isSystemText(text string) bool {
	text = strings.TrimSpace(stripMarks(text))
	return len(text) < 400 && e2eRe.MatchString(text)
}

// ---- chat name ----

// Android names the transcript after the chat, in the phone's language:
// "WhatsApp Chat with Jane.txt", "WhatsApp Chat mit …", "Chat de WhatsApp con …".
var androidNameRe = regexp.MustCompile(`(?i)^.*?whatsapp\S*(?:\s+chat)?\s+(?:with|mit|con|avec|com|met|med)\s+(.+)$`)

// chatName derives the chat's name from the Android file name when possible,
// else from the directory inside the ZIP (iOS: _chat.txt), else a fixed label.
func chatName(member string) string {
	base := strings.TrimSpace(strings.TrimSuffix(path.Base(member), path.Ext(member)))
	if mm := androidNameRe.FindStringSubmatch(base); mm != nil {
		return strings.TrimSpace(mm[1])
	}
	if strings.Contains(strings.ToLower(base), "whatsapp") {
		return base
	}
	if dir := path.Base(path.Dir(member)); dir != "." && dir != "/" {
		if name := strings.TrimSpace(strings.TrimPrefix(dir, "WhatsApp Chat - ")); name != "" {
			return name
		}
	}
	return defaultChat
}

var (
	iosArchiveRe = regexp.MustCompile(`(?i)^whatsapp\s+chat\s+-\s+(.+)$`)
	copySuffixRe = regexp.MustCompile(`\s*\(\d+\)$`) // a browser's "name (1).zip"
)

// archiveChatName derives the chat's name from the uploaded ZIP's file name,
// or returns "" when the name does not look like WhatsApp's own
// ("WhatsApp Chat - Book Club.zip"; Android: "WhatsApp Chat with Jane.zip").
func archiveChatName(archive string) string {
	base := strings.TrimSpace(strings.TrimSuffix(archive, path.Ext(archive)))
	base = copySuffixRe.ReplaceAllString(base, "")
	for _, re := range []*regexp.Regexp{iosArchiveRe, androidNameRe} {
		if mm := re.FindStringSubmatch(base); mm != nil {
			return strings.TrimSpace(mm[1])
		}
	}
	return ""
}

// rekey replaces the key of an "msg:<key>:<n>" id with one that covers chat.
func rekey(id, chat string) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 {
		return id
	}
	sum := sha256.Sum256([]byte(chat + "\x00" + parts[1]))
	return parts[0] + ":" + hex.EncodeToString(sum[:10]) + ":" + parts[2]
}
