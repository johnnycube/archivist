package seed

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"math/rand" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used -- seeded, reproducible demo data; nothing security-relevant

	"github.com/johnnycube/archivist/internal/domain"
)

// referenceNow is the demo's "today". It is fixed so that the data set — every
// timestamp, every external id — is identical on every boot and in tests; it
// is refreshed by hand from time to time so the history keeps reading as
// recent.
var referenceNow = time.Date(2026, time.September, 22, 17, 40, 0, 0, time.UTC)

// gen builds the demo history. Everything it draws comes from one seeded PRNG
// and the sources are generated in a fixed order, so the output is a pure
// function of the code.
type gen struct {
	rng    *rand.Rand
	now    time.Time
	events map[string][]domain.Event // by source
	counts map[string]int            // per-kind external-id counters
	blobs  []blob                    // attachments to store, in creation order
	media  bool                      // render attachment bytes (a blob store is available)
	nblobs int                       // attachments issued, rendered or not (keeps ids stable)
}

// blob is one attachment's bytes under the blob-store key the timeline's Media
// use case expects: source id + "/" + Event.Media.BlobKey.
type blob struct {
	key  string
	data []byte
}

func newGen() *gen {
	return &gen{
		rng:    rand.New(rand.NewSource(20260922)),
		now:    referenceNow,
		events: map[string][]domain.Event{},
		counts: map[string]int{},
	}
}

// add records an event under its source.
func (g *gen) add(ev domain.Event) {
	if ev.Metadata == nil {
		ev.Metadata = map[string]any{}
	}
	g.events[ev.SourceID] = append(g.events[ev.SourceID], ev)
}

// sources lists the seeded source ids in a stable order.
func (g *gen) sources() []string {
	ids := make([]string, 0, len(g.events))
	for id := range g.events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// id issues the next external id of a kind: "seed-<source>-<kind>-<n>". The
// prefix marks the data as synthetic; the counter keeps ids unique per source
// and stable across boots.
func (g *gen) id(source, kind string) string {
	k := source + "/" + kind
	g.counts[k]++
	return fmt.Sprintf("seed-%s-%s-%d", source, kind, g.counts[k])
}

func (g *gen) pick(ss []string) string { return ss[g.rng.Intn(len(ss))] }

// between returns an integer in [lo, hi].
func (g *gen) between(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + g.rng.Intn(hi-lo+1)
}

func (g *gen) chance(p float64) bool { return g.rng.Float64() < p }

// ---- time ----

// yearlyGrowth is how much busier each year of the history is than the one
// before. A personal archive grows over the years; the skew is also what
// lets the overview's auto log-scale show — the busiest year holds far more
// than the median one.
const yearlyGrowth = 1.6

// since draws an instant between January 1 of year and now: a calendar year
// is picked with weight yearlyGrowth^(years since the start), scaled by how
// much of it lies before now, and the instant is uniform within that year —
// so recent years are busy without the last few days piling up.
func (g *gen) since(year int) time.Time {
	if year > g.now.Year() {
		return g.now
	}
	weights := make([]float64, 0, g.now.Year()-year+1)
	total := 0.0
	for y := year; y <= g.now.Year(); y++ {
		w := math.Pow(yearlyGrowth, float64(y-year))
		if y == g.now.Year() {
			w *= float64(g.now.YearDay()) / 365
		}
		weights = append(weights, w)
		total += w
	}
	pick := g.rng.Float64() * total
	y := year
	for i, w := range weights {
		if pick < w || i == len(weights)-1 {
			y = year + i
			break
		}
		pick -= w
	}
	start := time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	if end.After(g.now) {
		end = g.now
	}
	return g.uniform(start, end)
}

// uniform draws an instant uniformly between a and b.
func (g *gen) uniform(a, b time.Time) time.Time {
	if !b.After(a) {
		return a
	}
	return a.Add(time.Duration(g.rng.Int63n(int64(b.Sub(a))))).Truncate(time.Second)
}

// clock returns t moved to a random time of day between the hours [from, to).
func (g *gen) clock(t time.Time, from, to int) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	secs := from*3600 + g.rng.Intn((to-from)*3600)
	return day.Add(time.Duration(secs) * time.Second)
}

// minutes is a duration of lo..hi minutes, with some seconds' jitter.
func (g *gen) minutes(lo, hi int) time.Duration {
	return time.Duration(g.between(lo, hi))*time.Minute + time.Duration(g.rng.Intn(60))*time.Second
}

// ---- conversations ----

// line is one message of a generated chat: who said what, when. Self is true
// for the persona's own messages (Actor is then left to the caller, since
// every platform names her differently).
type line struct {
	At   time.Time
	Who  person
	Self bool
	Body string
}

// conversation lays out n messages between the persona and people, starting
// at start: short volleys a minute or two apart with the occasional pause of
// hours, so a thread reads like a real chat rather than a metronome.
func (g *gen) conversation(start time.Time, people []person, n int) []line {
	out := make([]line, 0, n)
	t := start
	self := g.chance(0.5)
	for i := 0; i < n; i++ {
		if i > 0 {
			switch {
			case g.chance(0.08):
				t = t.Add(time.Duration(g.between(2, 14)) * time.Hour)
			case g.chance(0.3):
				t = t.Add(g.minutes(3, 25))
			default:
				t = t.Add(time.Duration(g.between(20, 150)) * time.Second)
			}
		}
		l := line{At: t, Self: self, Body: g.chatLine(people)}
		if !self {
			l.Who = people[g.rng.Intn(len(people))]
		}
		out = append(out, l)
		if g.chance(0.6) {
			self = !self
		}
	}
	return out
}

// chatLine draws a casual message, naming one of people when the line asks.
func (g *gen) chatLine(people []person) string {
	s := g.pick(chatLines)
	if strings.Contains(s, "{name}") {
		s = strings.ReplaceAll(s, "{name}", people[g.rng.Intn(len(people))].Name)
	}
	return s
}

// some returns k distinct people from the cast, in cast order.
func (g *gen) some(k int) []person {
	idx := g.rng.Perm(len(cast))[:k]
	sort.Ints(idx)
	out := make([]person, k)
	for i, j := range idx {
		out[i] = cast[j]
	}
	return out
}

// ---- media ----

// picture renders and registers a PNG attachment for source and returns the
// Media entry to hang on an event. blobKey is the member path a real export
// would have had (the media endpoint picks the content type from its
// extension, so it must end in .png).
//
// Without a blob store (g.media false) the Media entry is still attached —
// the event shape is the same — but no bytes are rendered.
func (g *gen) picture(source, blobKey string, style pictureStyle) domain.Media {
	g.nblobs++
	if g.media {
		g.blobs = append(g.blobs, blob{key: source + "/" + blobKey, data: picture(source, style, g.nblobs)})
	}
	return domain.Media{BlobKey: blobKey, MIME: "image/png"}
}

// document registers a small PDF attachment: served as a download, not inline.
func (g *gen) document(source, blobKey, title string, lines []string) domain.Media {
	g.nblobs++
	if g.media {
		g.blobs = append(g.blobs, blob{key: source + "/" + blobKey, data: pdfDocument(title, lines)})
	}
	return domain.Media{BlobKey: blobKey, MIME: "application/pdf"}
}

// ---- shared shapes ----

// unknown is what the importer's generic capture produces for an export
// member no extractor claimed (internal/bundle captureGeneric): undated, with
// the member path, its size, a content hash and a short preview.
func (g *gen) unknown(source, member, preview string, size int) domain.Event {
	// the hash need only look like one; it is stable per member
	h := fmt.Sprintf("%016x%016x%016x%016x", hash64(member), hash64(member+"1"), hash64(member+"2"), hash64(member+"3"))
	p := preview
	if size > len(preview) {
		p += "…"
	}
	return domain.Event{
		SourceID:   source,
		ExternalID: source + ":unknown:" + member,
		Type:       source + ".unknown",
		Category:   domain.CategoryOther,
		Title:      "Unmapped export data: " + member,
		Metadata:   map[string]any{"member": member, "bytes": int64(size), "content_hash": h, "preview": p},
	}
}

// hash64 is FNV-1a: a stable, dependency-free 64-bit digest for fake hashes.
func hash64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// short imitates the providers' short() helper: a 16-hex-char digest.
func short(s string) string { return fmt.Sprintf("%016x", hash64(s)) }

// rfc3339 is the providers' timestamp format for external ids and metadata.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }
