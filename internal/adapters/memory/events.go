// Package memory holds in-memory implementations of the app's driven ports, used
// for dev and tests. They are held to the same port contract as the PostgreSQL
// adapters by the shared suites in internal/adapters/porttest.
package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Events is an in-memory app.EventRepository.
type Events struct {
	mu     sync.RWMutex
	byUser map[string]map[string]domain.StoredEvent
	runs   map[string]string // event id → the import run that last wrote it
	clock  func() time.Time
}

// NewEvents returns an empty in-memory event repository.
func NewEvents() *Events {
	return &Events{byUser: map[string]map[string]domain.StoredEvent{}, runs: map[string]string{}, clock: time.Now}
}

var _ app.EventRepository = (*Events)(nil)

// eventID derives a stable, uuid-shaped id from the upsert key (user,
// account, source, external id), so re-saving an event keeps its id (like the
// Postgres row does).
func eventID(userID, accountID, sourceID, externalID string) string {
	b := sha256.Sum256([]byte(userID + "\x00" + accountID + "\x00" + sourceID + "\x00" + externalID))
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (m *Events) Save(_ context.Context, userID, accountID string, ev domain.Event) (domain.StoredEvent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	se, created := m.save(userID, accountID, app.ImportRun{}, ev)
	return se, created, nil
}

// SaveBatch upserts the events one by one under one lock.
func (m *Events) SaveBatch(_ context.Context, userID, accountID string, run app.ImportRun, evs []domain.Event) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	created := 0
	for _, ev := range evs {
		if _, isNew := m.save(userID, accountID, run, ev); isNew {
			created++
		}
	}
	return created, nil
}

// SweepImport removes the events the import owns that the run did not write.
func (m *Events) SweepImport(_ context.Context, userID string, run app.ImportRun) (int, error) {
	if run.JobID == "" || run.RunID == "" {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted := 0
	for id, se := range m.byUser[userID] {
		if se.ImportJobID == run.JobID && m.runs[id] != run.RunID {
			delete(m.byUser[userID], id)
			delete(m.runs, id)
			deleted++
		}
	}
	return deleted, nil
}

func (m *Events) DeleteByImport(_ context.Context, userID, importJobID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted := 0
	for id, se := range m.byUser[userID] {
		if importJobID != "" && se.ImportJobID == importJobID {
			delete(m.byUser[userID], id)
			delete(m.runs, id)
			deleted++
		}
	}
	return deleted, nil
}

// save upserts one event; the caller holds the lock.
func (m *Events) save(userID, accountID string, run app.ImportRun, ev domain.Event) (domain.StoredEvent, bool) {
	if m.byUser[userID] == nil {
		m.byUser[userID] = map[string]domain.StoredEvent{}
	}
	se := domain.StoredEvent{
		ID:          eventID(userID, accountID, ev.SourceID, ev.ExternalID),
		UserID:      userID,
		AccountID:   accountID,
		ImportJobID: run.JobID,
		IngestedAt:  m.clock().UTC(),
		Event:       ev,
	}
	prev, existed := m.byUser[userID][se.ID]
	if existed {
		se.IngestedAt = prev.IngestedAt
	}
	m.byUser[userID][se.ID] = se
	m.runs[se.ID] = run.RunID
	return se, !existed
}

func (m *Events) Histogram(_ context.Context, q app.Query) ([]app.HistogramBucket, error) {
	return m.histogram(q, func(se domain.StoredEvent) string { return se.SourceID }), nil
}

func (m *Events) HistogramByCategory(_ context.Context, q app.Query) ([]app.HistogramBucket, error) {
	return m.histogram(q, func(se domain.StoredEvent) string { return string(se.Category) }), nil
}

// histogram counts the dated events matching q per day of the query's zone
// (app.Query.Loc, UTC when unset) and key.
func (m *Events) histogram(q app.Query, key func(domain.StoredEvent) string) []app.HistogramBucket {
	m.mu.RLock()
	defer m.mu.RUnlock()

	loc := q.Zone()
	byDay := map[string]map[string]int{}
	for _, se := range m.byUser[q.UserID] {
		if se.OccurredAt.IsZero() || !matches(se, q) {
			continue
		}
		day := app.LocalDay(se.OccurredAt, loc)
		if byDay[day] == nil {
			byDay[day] = map[string]int{}
		}
		byDay[day][key(se)]++
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	out := make([]app.HistogramBucket, 0, len(days))
	for _, d := range days {
		out = append(out, app.HistogramBucket{Day: d, Counts: byDay[d]})
	}
	return out
}

func (m *Events) Get(_ context.Context, userID, id string) (domain.StoredEvent, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	se, ok := m.byUser[userID][id]
	return se, ok, nil
}

func (m *Events) Timeline(_ context.Context, q app.Query) (app.Page, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	matched, total, facets, err := m.filtered(q)
	if err != nil {
		return app.Page{}, err
	}
	page := app.Page{Total: total, Facets: facets}
	limit := app.ClampLimit(q.Limit) // normalized by the use case; capped defensively
	if len(matched) > limit {
		page.Events = matched[:limit]
		page.NextCursor = app.CursorAfter(page.Events[limit-1]).Encode()
	} else {
		page.Events = matched
	}
	if page.Events == nil {
		page.Events = []domain.StoredEvent{}
	}
	return page, nil
}

// filtered returns the user's events matching q in timeline order, those after
// the cursor only, plus the total and facets of the whole filtered set. The
// caller holds the lock.
func (m *Events) filtered(q app.Query) (after []domain.StoredEvent, total int, facets app.Facets, err error) {
	var matched []domain.StoredEvent
	for _, se := range m.byUser[q.UserID] {
		if matches(se, q) {
			matched = append(matched, se)
		}
	}
	if q.SkipSummary {
		facets = app.EmptyFacets()
	} else {
		total, facets = len(matched), computeFacets(matched)
	}
	sort.Slice(matched, func(i, j int) bool { return less(matched[i], matched[j]) })

	if q.Cursor != "" {
		c, err := app.DecodeCursor(q.Cursor)
		if err != nil {
			return nil, 0, app.Facets{}, err
		}
		kept := matched[:0:0]
		for _, se := range matched {
			if c.Admits(se) {
				kept = append(kept, se)
			}
		}
		matched = kept
	}
	return matched, total, facets, nil
}

// Collapsed is the reference implementation of the collapsed timeline: the
// same filtered, ordered events as Timeline, cut into a window of
// app.CollapseWindow events whose maximal runs (app.CollapseRule.Continues)
// app.CollapseRule.Page turns into the page.
func (m *Events) Collapsed(ctx context.Context, q app.Query, rule app.CollapseRule) (app.CollapsedPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	matched, total, facets, err := m.filtered(q)
	if err != nil {
		return app.CollapsedPage{}, err
	}
	window := matched
	if len(window) > app.CollapseWindow {
		window = window[:app.CollapseWindow]
	}

	// Maximal runs of the window, with their members.
	var runs []app.Run
	var members [][]domain.StoredEvent
	for i, se := range window {
		if i > 0 && rule.Continues(window[i-1], se, q.Loc) {
			r := &runs[len(runs)-1]
			r.Count++
			r.LastAt, r.LastID = se.OccurredAt, se.ID
			if s := app.SampleTitle(se.Event); s != "" && len(r.SampleTitles) < app.SampleTitles && !contains(r.SampleTitles, s) {
				r.SampleTitles = append(r.SampleTitles, s)
			}
			members[len(members)-1] = append(members[len(members)-1], se)
			continue
		}
		c := app.Cluster{
			SourceID: se.SourceID, Type: se.Type, Category: se.Category, Count: 1,
			FirstAt: se.OccurredAt, LastAt: se.OccurredAt, FirstID: se.ID, LastID: se.ID, SampleTitles: []string{},
		}
		if s := app.SampleTitle(se.Event); s != "" {
			c.SampleTitles = append(c.SampleTitles, s)
		}
		runs = append(runs, app.Run{Cluster: c})
		members = append(members, []domain.StoredEvent{se})
	}

	expand := func(_ context.Context, idx []int) ([][]domain.StoredEvent, error) {
		out := make([][]domain.StoredEvent, len(idx))
		for i, k := range idx {
			out[i] = members[k]
		}
		return out, nil
	}
	items, next, err := rule.Page(ctx, runs, len(window) == app.CollapseWindow, app.ClampLimit(q.Limit), expand)
	if err != nil {
		return app.CollapsedPage{}, err
	}
	if items == nil {
		items = []app.CollapsedItem{}
	}
	return app.CollapsedPage{Items: items, NextCursor: next, Total: total, Facets: facets}, nil
}

// --- filtering / facets ---

func matches(se domain.StoredEvent, q app.Query) bool {
	if len(q.Sources) > 0 && !contains(q.Sources, se.SourceID) {
		return false
	}
	if len(q.Categories) > 0 && !contains(q.Categories, string(se.Category)) {
		return false
	}
	if len(q.Types) > 0 && !contains(q.Types, se.Type) {
		return false
	}
	if !q.From.IsZero() || !q.To.IsZero() {
		// A date range only ever matches dated events.
		if se.OccurredAt.IsZero() ||
			(!q.From.IsZero() && se.OccurredAt.Before(q.From)) ||
			(!q.To.IsZero() && se.OccurredAt.After(q.To)) {
			return false
		}
	}
	if strings.TrimSpace(q.Search) != "" && !matchesSearch(se, q.Search) {
		return false
	}
	return true
}

func computeFacets(evs []domain.StoredEvent) app.Facets {
	src, cat, typ := map[string]int{}, map[string]int{}, map[string]int{}
	for _, e := range evs {
		src[e.SourceID]++
		cat[string(e.Category)]++
		typ[e.Type]++
	}
	return app.Facets{Sources: toFacet(src), Categories: toFacet(cat), Types: toFacet(typ)}
}

func toFacet(m map[string]int) []app.FacetCount {
	out := make([]app.FacetCount, 0, len(m))
	for v, c := range m {
		out = append(out, app.FacetCount{Value: v, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// --- search ---

// words splits text the way Postgres' 'simple' text-search configuration
// roughly does: lowercased runs of letters and digits.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// matchesSearch approximates the Postgres adapter's
// search_vector @@ plainto_tsquery('simple', q): every word of the query must
// occur as a whole word in the title or the body, case-insensitively; a query
// without a single word matches nothing.
//
// Remaining gap (checked against PostgreSQL 16): its parser keeps host names
// ("example.com"), URLs and their paths ("go.dev/doc"), e-mail addresses, file
// paths and decimal numbers ("3.14") as ONE lexeme, where this splits at every
// non-alphanumeric rune. Hyphenated and underscored words agree (Postgres
// indexes the parts too). So this finds a superset: "example" matches
// "example.com" and "doc" matches "https://go.dev/doc" here but not in
// Postgres; anything Postgres finds, this finds.
func matchesSearch(se domain.StoredEvent, query string) bool {
	terms := words(query)
	if len(terms) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range words(se.Title) {
		have[w] = true
	}
	for _, w := range words(se.Body) {
		have[w] = true
	}
	for _, t := range terms {
		if !have[t] {
			return false
		}
	}
	return true
}

// less is the timeline order: occurred_at desc, id desc, undated events last.
func less(a, b domain.StoredEvent) bool { return app.CompareTimeline(a, b) < 0 }

func (m *Events) deleteUser(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.byUser[userID] {
		delete(m.runs, id)
	}
	delete(m.byUser, userID)
}
