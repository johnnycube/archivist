package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"

	"github.com/lib/pq"
)

// Events is a PostgreSQL app.EventRepository: upsert, keyset pagination
// (undated events last), facets, and tsvector full-text search.
type Events struct{ db *sql.DB }

// NewEvents wraps a pool as an event repository.
func NewEvents(db *sql.DB) *Events { return &Events{db: db} }

var _ app.EventRepository = (*Events)(nil)

// Save upserts one event outside of any import (seeding, tools): same statement
// as SaveBatch, owned by no import afterwards.
func (s *Events) Save(ctx context.Context, userID, accountID string, ev domain.Event) (domain.StoredEvent, bool, error) {
	saved, err := s.upsert(ctx, userID, accountID, app.ImportRun{}, []domain.Event{ev})
	if err != nil {
		return domain.StoredEvent{}, false, err
	}
	if len(saved) != 1 {
		return domain.StoredEvent{}, false, fmt.Errorf("upsert event: %d rows returned, want 1", len(saved))
	}
	r := saved[0]
	return domain.StoredEvent{ID: r.id, UserID: userID, AccountID: accountID, IngestedAt: r.ingestedAt, Event: ev}, r.inserted, nil
}

// batchRow is one event of a SaveBatch, shipped to Postgres as a jsonb array
// element and expanded server-side with jsonb_to_recordset.
type batchRow struct {
	SourceID         string          `json:"source_id"`
	ExternalID       string          `json:"external_id"`
	Type             string          `json:"type"`
	Category         string          `json:"category"`
	OccurredAt       *time.Time      `json:"occurred_at"`
	Actor            string          `json:"actor"`
	Title            string          `json:"title"`
	Body             string          `json:"body"`
	ThreadExternalID *string         `json:"thread_external_id"`
	ParentExternalID *string         `json:"parent_external_id"`
	Metadata         json.RawMessage `json:"metadata"`
}

type batchTarget struct {
	EventID          string  `json:"event_id"`
	Position         int     `json:"position"`
	Relation         string  `json:"relation"`
	TargetSource     *string `json:"target_source"`
	TargetExternalID *string `json:"target_external_id"`
	TargetURL        *string `json:"target_url"`
}

type batchMedia struct {
	EventID  string  `json:"event_id"`
	Position int     `json:"position"`
	BlobKey  string  `json:"blob_key"`
	MIME     *string `json:"mime"`
	URL      *string `json:"url"`
}

// SaveBatch upserts a batch of events in one transaction and three statements
// (events, targets, media) instead of several round trips per event.
func (s *Events) SaveBatch(ctx context.Context, userID, accountID string, run app.ImportRun, evs []domain.Event) (int, error) {
	saved, err := s.upsert(ctx, userID, accountID, run, evs)
	created := 0
	for _, r := range saved {
		if r.inserted {
			created++
		}
	}
	return created, err
}

// upserted is one row written by upsert.
type upserted struct {
	id         string
	ingestedAt time.Time
	inserted   bool // false: an existing event was refreshed
}

// upsert is the one write path for events. An empty run.JobID means "no
// import": the rows' import_job_id (and import_run) become NULL. Events,
// targets and media are written in one transaction, three statements.
func (s *Events) upsert(ctx context.Context, userID, accountID string, run app.ImportRun, evs []domain.Event) ([]upserted, error) {
	if len(evs) == 0 {
		return nil, nil
	}
	rows, kept := batchRows(evs)
	payload, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("encode batch: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit
	saved, ids, err := insertEvents(ctx, tx, userID, accountID, run, payload, len(rows))
	if err != nil {
		return nil, err
	}
	if err := replaceChildren(ctx, tx, rows, kept, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// batchRows turns events into rows. ON CONFLICT cannot touch the same row
// twice in one statement, so repeats of an upsert key collapse — the last one
// wins, as sequential saves would have it. kept are the events of the rows.
//
// The key is the CLEANED (NUL-free) source and external id — what the row
// will hold: ids that differ only by NUL bytes are one row, and keying on the
// raw ids would send both and fail the whole statement.
func batchRows(evs []domain.Event) (rows []batchRow, kept []domain.Event) {
	key := func(ev domain.Event) string { return clean(ev.SourceID) + "\x00" + clean(ev.ExternalID) }
	last := make(map[string]int, len(evs))
	for i, ev := range evs {
		last[key(ev)] = i
	}
	rows = make([]batchRow, 0, len(last))
	kept = make([]domain.Event, 0, len(last))
	for i, ev := range evs {
		if last[key(ev)] != i {
			continue
		}
		row := batchRow{
			SourceID: clean(ev.SourceID), ExternalID: clean(ev.ExternalID),
			Type: clean(ev.Type), Category: clean(string(ev.Category)),
			Actor: clean(ev.Actor), Title: clean(ev.Title), Body: clean(ev.Body),
			ThreadExternalID: optStr(ev.ThreadExternalID), ParentExternalID: optStr(ev.ParentExternalID),
			Metadata: metadataJSON(ev.Metadata),
		}
		if y := ev.OccurredAt.Year(); !ev.OccurredAt.IsZero() && y >= 1 && y <= 9999 {
			t := ev.OccurredAt.UTC()
			row.OccurredAt = &t
		}
		rows = append(rows, row)
		kept = append(kept, ev)
	}
	return rows, kept
}

// insertEvents upserts the rows (one jsonb parameter) and returns what each
// became, and the event id per (source, external id).
func insertEvents(ctx context.Context, tx *sql.Tx, userID, accountID string, run app.ImportRun, payload []byte, n int) ([]upserted, map[string]string, error) {
	res, err := tx.QueryContext(ctx, `
INSERT INTO events
  (user_id, account_id, import_job_id, import_run, source_id, external_id, type, category, occurred_at,
   actor, title, body, thread_external_id, parent_external_id, metadata)
SELECT $1::uuid, $2::uuid, NULLIF($3,'')::uuid, NULLIF($5,''), r.source_id, r.external_id, r.type, r.category,
       r.occurred_at, r.actor, r.title, r.body, r.thread_external_id, r.parent_external_id,
       COALESCE(r.metadata, '{}'::jsonb)
FROM jsonb_to_recordset($4::jsonb) AS r(
  source_id text, external_id text, type text, category text, occurred_at timestamptz,
  actor text, title text, body text, thread_external_id text, parent_external_id text,
  metadata jsonb)
ON CONFLICT (user_id, account_id, source_id, external_id) DO UPDATE SET
  import_job_id=EXCLUDED.import_job_id, import_run=EXCLUDED.import_run,
  type=EXCLUDED.type, category=EXCLUDED.category,
  occurred_at=EXCLUDED.occurred_at, actor=EXCLUDED.actor, title=EXCLUDED.title,
  body=EXCLUDED.body, thread_external_id=EXCLUDED.thread_external_id,
  parent_external_id=EXCLUDED.parent_external_id, metadata=EXCLUDED.metadata
RETURNING id, source_id, external_id, ingested_at, (xmax = 0) AS inserted`,
		userID, accountID, run.JobID, string(payload), run.RunID)
	if err != nil {
		return nil, nil, fmt.Errorf("upsert events: %w", err)
	}
	defer func() { _ = res.Close() }() // read errors surface through res.Err
	ids := make(map[string]string, n)
	saved := make([]upserted, 0, n)
	for res.Next() {
		var src, ext string
		var r upserted
		if err := res.Scan(&r.id, &src, &ext, &r.ingestedAt, &r.inserted); err != nil {
			return nil, nil, err
		}
		ids[src+"\x00"+ext] = r.id
		saved = append(saved, r)
	}
	if err := res.Err(); err != nil {
		return nil, nil, fmt.Errorf("upsert events: %w", err)
	}
	return saved, ids, nil
}

// replaceChildren replaces the targets and media of the upserted events
// wholesale: delete theirs, insert the batch's (one jsonb parameter each).
func replaceChildren(ctx context.Context, tx *sql.Tx, rows []batchRow, kept []domain.Event, ids map[string]string) error {
	var targets []batchTarget
	var media []batchMedia
	all := make([]string, 0, len(ids))
	for _, id := range ids {
		all = append(all, id)
	}
	for i, ev := range kept {
		id := ids[rows[i].SourceID+"\x00"+rows[i].ExternalID]
		for pos, t := range ev.Targets {
			targets = append(targets, batchTarget{
				EventID: id, Position: pos, Relation: clean(string(t.Relation)),
				TargetSource: optStr(t.TargetSource), TargetExternalID: optStr(t.TargetExternalID), TargetURL: optStr(t.TargetURL),
			})
		}
		for pos, m := range ev.Media {
			media = append(media, batchMedia{EventID: id, Position: pos, BlobKey: clean(m.BlobKey), MIME: optStr(m.MIME), URL: optStr(m.URL)})
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM event_targets WHERE event_id = ANY($1::uuid[])`, pq.Array(all)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media WHERE event_id = ANY($1::uuid[])`, pq.Array(all)); err != nil {
		return err
	}
	if err := insertJSON(ctx, tx, targets, "insert targets", `
INSERT INTO event_targets (event_id, position, relation, target_source, target_external_id, target_url)
SELECT r.event_id, r.position, r.relation, r.target_source, r.target_external_id, r.target_url
FROM jsonb_to_recordset($1::jsonb) AS r(
  event_id uuid, position int, relation text, target_source text, target_external_id text, target_url text)`); err != nil {
		return err
	}
	return insertJSON(ctx, tx, media, "insert media", `
INSERT INTO media (event_id, position, blob_key, mime, url)
SELECT r.event_id, r.position, r.blob_key, r.mime, r.url
FROM jsonb_to_recordset($1::jsonb) AS r(event_id uuid, position int, blob_key text, mime text, url text)`)
}

// insertJSON runs query with rows as its one jsonb parameter; no rows, no
// statement.
func insertJSON[T any](ctx context.Context, tx *sql.Tx, rows []T, what, query string) error {
	if len(rows) == 0 {
		return nil
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, query, string(b)); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// SweepImport removes the events the import owns that the run did not write
// (targets and media cascade).
func (s *Events) SweepImport(ctx context.Context, userID string, run app.ImportRun) (int, error) {
	if !isUUID(run.JobID) || run.RunID == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM events WHERE user_id=$1 AND import_job_id=$2 AND import_run IS DISTINCT FROM $3`,
		userID, run.JobID, run.RunID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteByImport removes the events an import owns (targets and media cascade).
func (s *Events) DeleteByImport(ctx context.Context, userID, importJobID string) (int, error) {
	if !isUUID(importJobID) {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM events WHERE user_id=$1 AND import_job_id=$2`, userID, importJobID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// clean drops NUL bytes, which neither text nor jsonb can hold; exports do
// occasionally contain them.
func clean(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	s = clean(s)
	return &s
}

// metadataJSON encodes event metadata for a jsonb column, stripping NULs.
func metadataJSON(m map[string]any) json.RawMessage {
	if m == nil {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	if !bytes.Contains(b, []byte(`\u0000`)) {
		return b
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return json.RawMessage("{}")
	}
	if b, err = json.Marshal(stripNUL(v)); err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func stripNUL(v any) any {
	switch x := v.(type) {
	case string:
		return clean(x)
	case []any:
		for i := range x {
			x[i] = stripNUL(x[i])
		}
	case map[string]any:
		for k, e := range x {
			delete(x, k)
			x[clean(k)] = stripNUL(e)
		}
	}
	return v
}

// Histogram returns per-day, per-source counts of dated events matching q.
func (s *Events) Histogram(ctx context.Context, q app.Query) ([]app.HistogramBucket, error) {
	return s.histogram(ctx, q, "source_id")
}

// HistogramByCategory returns per-day, per-category counts of dated events
// matching q.
func (s *Events) HistogramByCategory(ctx context.Context, q app.Query) ([]app.HistogramBucket, error) {
	return s.histogram(ctx, q, "category")
}

// histogram counts dated events per day of the query's zone and the given
// column (a column name of this package's own, never client input). The zone
// travels as a bind parameter: `occurred_at AT TIME ZONE $n` reads the stored
// instant as wall-clock time there (PostgreSQL ships the IANA database, so a
// DST day is 23 or 25 hours long, as it is for the viewer), and date_trunc
// then cuts it at that zone's midnight. The range predicate of the filters
// still uses occurred_at itself, so the timeline-order index keeps serving it.
func (s *Events) histogram(ctx context.Context, q app.Query, col string) ([]app.HistogramBucket, error) {
	where, args, idx := buildFilter(q)
	args = append(args, q.Zone().String())
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT to_char(date_trunc('day', occurred_at AT TIME ZONE $%d::text), 'YYYY-MM-DD') AS day, %s, count(*)
FROM events WHERE %s AND occurred_at IS NOT NULL
GROUP BY 1, 2 ORDER BY 1 ASC`, idx+1, col, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []app.HistogramBucket{}
	for rows.Next() {
		var day, key string
		var n int
		if err := rows.Scan(&day, &key, &n); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Day != day {
			out = append(out, app.HistogramBucket{Day: day, Counts: map[string]int{}})
		}
		out[len(out)-1].Counts[key] = n
	}
	return out, rows.Err()
}

// timelineOrder is the SQL form of app.CompareTimeline: undated events last.
const timelineOrder = `COALESCE(occurred_at, TIMESTAMPTZ '-infinity') DESC, id DESC`

// pageFilter is the WHERE clause of one timeline page: the query's filters plus
// the SQL form of app.Cursor.Admits. The cursor is decoded before any query is
// spent on the request (ErrBadCursor).
func pageFilter(q app.Query) (rowsWhere string, rowsArgs []any, where string, args []any, err error) {
	var cur *app.Cursor
	if q.Cursor != "" {
		c, err := app.DecodeCursor(q.Cursor)
		if err != nil {
			return "", nil, "", nil, err
		}
		cur = &c
	}
	where, args, idx := buildFilter(q)
	rowsWhere = where
	rowsArgs = append([]any(nil), args...)
	switch {
	case cur == nil:
	case cur.Undated:
		rowsWhere += fmt.Sprintf(" AND occurred_at IS NULL AND id < $%d", idx+1)
		rowsArgs = append(rowsArgs, cur.ID)
	default:
		rowsWhere += fmt.Sprintf(" AND (COALESCE(occurred_at, TIMESTAMPTZ '-infinity'), id) < ($%d, $%d)", idx+1, idx+2)
		rowsArgs = append(rowsArgs, cur.OccurredAt, cur.ID)
	}
	return rowsWhere, rowsArgs, where, args, nil
}

// summary is the total and the facets of the whole filtered set.
func (s *Events) summary(ctx context.Context, where string, args []any) (total int, facets app.Facets, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE `+where, args...).Scan(&total); err != nil {
		return 0, app.Facets{}, err
	}
	if facets.Sources, err = s.facet(ctx, "source_id", where, args); err != nil {
		return 0, app.Facets{}, err
	}
	if facets.Categories, err = s.facet(ctx, "category", where, args); err != nil {
		return 0, app.Facets{}, err
	}
	if facets.Types, err = s.facet(ctx, "type", where, args); err != nil {
		return 0, app.Facets{}, err
	}
	return total, facets, nil
}

func (s *Events) Timeline(ctx context.Context, q app.Query) (app.Page, error) {
	rowsWhere, rowsArgs, where, args, err := pageFilter(q)
	if err != nil {
		return app.Page{}, err
	}
	page := app.Page{Facets: app.EmptyFacets()}
	if !q.SkipSummary {
		if page.Total, page.Facets, err = s.summary(ctx, where, args); err != nil {
			return app.Page{}, err
		}
	}

	limit := app.ClampLimit(q.Limit) // normalized by the use case; capped defensively
	sqlText := `
SELECT id, user_id, account_id, source_id, external_id, type, category, occurred_at,
       actor, title, body, thread_external_id, parent_external_id, metadata, ingested_at, import_job_id
FROM events WHERE ` + rowsWhere + `
ORDER BY ` + timelineOrder + `
LIMIT ` + fmt.Sprintf("%d", limit+1)

	rows, err := s.db.QueryContext(ctx, sqlText, rowsArgs...)
	if err != nil {
		return app.Page{}, err
	}
	defer rows.Close()

	var evs []domain.StoredEvent
	for rows.Next() {
		se, err := scanEvent(rows)
		if err != nil {
			return app.Page{}, err
		}
		evs = append(evs, se)
	}
	if err := rows.Err(); err != nil {
		return app.Page{}, err
	}
	if len(evs) > limit {
		last := evs[limit-1]
		evs = evs[:limit]
		page.NextCursor = app.CursorAfter(last).Encode()
	}
	if err := s.attachChildren(ctx, evs); err != nil {
		return app.Page{}, err
	}
	page.Events = evs
	if page.Events == nil {
		page.Events = []domain.StoredEvent{}
	}
	return page, nil
}

// Collapsed folds runs of repetitive events into clusters, in SQL, over a
// window of app.CollapseWindow events:
//
//  1. win: the next CollapseWindow events after the cursor, in timeline order —
//     the same index range scan as a Timeline page, just longer; this bounds
//     the cost of a page whatever the archive's size.
//  2. flagged: LAG over the window marks a run boundary where source, type,
//     category or the day IN THE QUERY'S ZONE change, or the gap to the previous
//     event exceeds the rule's gap (app.CollapseRule.Continues, in SQL).
//  3. runs: a running SUM of the boundary flags numbers the runs; ranked marks
//     the first occurrence of each sample title within a run.
//  4. One row per run: count, first/last member, first three distinct sample
//     titles — at most limit+1 of them, since every run yields at least one
//     item.
//
// Runs shorter than the rule's threshold are shown as events: their members
// are loaded by one bounded second query (expandRuns), which is simpler than
// re-expanding rows inside the aggregate and touches only what the page shows.
// app.CollapseRule.Page owns the paging rule shared with the memory adapter.
func (s *Events) Collapsed(ctx context.Context, q app.Query, rule app.CollapseRule) (app.CollapsedPage, error) {
	rowsWhere, rowsArgs, where, args, err := pageFilter(q)
	if err != nil {
		return app.CollapsedPage{}, err
	}
	page := app.CollapsedPage{Facets: app.EmptyFacets()}
	if !q.SkipSummary {
		if page.Total, page.Facets, err = s.summary(ctx, where, args); err != nil {
			return app.CollapsedPage{}, err
		}
	}

	limit := app.ClampLimit(q.Limit)
	gapArg := len(rowsArgs) + 1
	rowsArgs = append(rowsArgs, rule.Gap.Seconds())
	tzArg := len(rowsArgs) + 1
	rowsArgs = append(rowsArgs, q.Zone().String())
	sqlText := fmt.Sprintf(`
WITH win AS (
  SELECT id, source_id, type, category, occurred_at,
         left(COALESCE(NULLIF(title, ''), body), %d) AS sample,
         (occurred_at AT TIME ZONE $%d::text)::date AS day
  FROM events
  WHERE %s
  ORDER BY %s
  LIMIT %d
), flagged AS (
  SELECT *,
         CASE WHEN LAG(source_id) OVER o IS DISTINCT FROM source_id
                OR LAG(type) OVER o IS DISTINCT FROM type
                OR LAG(category) OVER o IS DISTINCT FROM category
                OR LAG(day) OVER o IS DISTINCT FROM day
                OR ($%d::float8 > 0 AND LAG(occurred_at) OVER o - occurred_at > make_interval(secs => $%d::float8))
              THEN 1 ELSE 0 END AS brk
  FROM win
  WINDOW o AS (ORDER BY %s)
), runs AS (
  SELECT *, SUM(brk) OVER (ORDER BY %s ROWS UNBOUNDED PRECEDING) AS run
  FROM flagged
), ranked AS (
  SELECT *, ROW_NUMBER() OVER (PARTITION BY run, sample ORDER BY %s) AS srank
  FROM runs
)
SELECT source_id, type, category, count(*)::int,
       (array_agg(id::text ORDER BY %s))[1] AS first_id,
       (array_agg(id::text ORDER BY COALESCE(occurred_at, TIMESTAMPTZ '-infinity') ASC, id ASC))[1] AS last_id,
       max(occurred_at) AS first_at, min(occurred_at) AS last_at,
       COALESCE((array_agg(sample ORDER BY %s) FILTER (WHERE srank = 1 AND sample <> ''))[1:%d], '{}') AS samples,
       (SELECT count(*) FROM win) AS window_rows
FROM ranked
GROUP BY run, source_id, type, category
ORDER BY run
LIMIT %d`,
		app.SampleTitleLen, tzArg, rowsWhere, timelineOrder, app.CollapseWindow,
		gapArg, gapArg, timelineOrder, timelineOrder, timelineOrder,
		timelineOrder, timelineOrder, app.SampleTitles, limit+1)

	rows, err := s.db.QueryContext(ctx, sqlText, rowsArgs...)
	if err != nil {
		return app.CollapsedPage{}, fmt.Errorf("collapsed timeline: %w", err)
	}
	defer rows.Close()
	var runs []app.Run
	windowRows := 0
	for rows.Next() {
		var c app.Cluster
		var firstAt, lastAt sql.NullTime
		var samples pq.StringArray
		if err := rows.Scan(&c.SourceID, &c.Type, &c.Category, &c.Count, &c.FirstID, &c.LastID,
			&firstAt, &lastAt, &samples, &windowRows); err != nil {
			return app.CollapsedPage{}, err
		}
		if firstAt.Valid {
			c.FirstAt = firstAt.Time.UTC()
		}
		if lastAt.Valid {
			c.LastAt = lastAt.Time.UTC()
		}
		c.SampleTitles = []string(samples)
		if c.SampleTitles == nil {
			c.SampleTitles = []string{}
		}
		runs = append(runs, app.Run{Cluster: c})
	}
	if err := rows.Err(); err != nil {
		return app.CollapsedPage{}, err
	}

	expand := func(ctx context.Context, idx []int) ([][]domain.StoredEvent, error) {
		picked := make([]app.Run, len(idx))
		for i, k := range idx {
			picked[i] = runs[k]
		}
		return s.expandRuns(ctx, where, args, picked)
	}
	items, next, err := rule.Page(ctx, runs, windowRows == app.CollapseWindow, limit, expand)
	if err != nil {
		return app.CollapsedPage{}, err
	}
	if items == nil {
		items = []app.CollapsedItem{}
	}
	page.Items, page.NextCursor = items, next
	return page, nil
}

// expandRuns loads the members of the given short runs in one query and hands
// them back per run in timeline order. where/args are the query's filters
// (buildFilter): a run is a closed interval of the FILTERED timeline, so the
// filters must apply again, or events the query excludes would show up in it.
func (s *Events) expandRuns(ctx context.Context, where string, args []any, runs []app.Run) ([][]domain.StoredEvent, error) {
	args = append([]any(nil), args...)
	where += " AND ("
	for i, r := range runs {
		if i > 0 {
			where += " OR "
		}
		n := len(args)
		switch {
		case r.FirstAt.IsZero(): // an undated run
			where += fmt.Sprintf("(occurred_at IS NULL AND id <= $%d AND id >= $%d)", n+1, n+2)
			args = append(args, r.FirstID, r.LastID)
		default:
			where += fmt.Sprintf("((COALESCE(occurred_at, TIMESTAMPTZ '-infinity'), id) <= ($%d, $%d) AND (COALESCE(occurred_at, TIMESTAMPTZ '-infinity'), id) >= ($%d, $%d))",
				n+1, n+2, n+3, n+4)
			args = append(args, r.FirstAt, r.FirstID, r.LastAt, r.LastID)
		}
	}
	where += ")"
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, account_id, source_id, external_id, type, category, occurred_at,
       actor, title, body, thread_external_id, parent_external_id, metadata, ingested_at, import_job_id
FROM events WHERE `+where+`
ORDER BY `+timelineOrder, args...)
	if err != nil {
		return nil, fmt.Errorf("expand runs: %w", err)
	}
	defer rows.Close()
	var evs []domain.StoredEvent
	for rows.Next() {
		se, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		evs = append(evs, se)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachChildren(ctx, evs); err != nil {
		return nil, err
	}
	// The runs are disjoint and in timeline order, so the fetched events split
	// into them by count; Page verifies the boundaries.
	out := make([][]domain.StoredEvent, len(runs))
	want := 0
	for _, r := range runs {
		want += r.Count
	}
	if len(evs) != want {
		return nil, fmt.Errorf("expand runs: got %d events, want %d", len(evs), want)
	}
	pos := 0
	for i, r := range runs {
		out[i] = evs[pos : pos+r.Count]
		pos += r.Count
	}
	return out, nil
}

func (s *Events) Get(ctx context.Context, userID, id string) (domain.StoredEvent, bool, error) {
	if !isUUID(id) {
		return domain.StoredEvent{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, user_id, account_id, source_id, external_id, type, category, occurred_at,
       actor, title, body, thread_external_id, parent_external_id, metadata, ingested_at, import_job_id
FROM events WHERE user_id=$1 AND id=$2`, userID, id)
	se, err := scanEvent(row)
	if err == sql.ErrNoRows {
		return domain.StoredEvent{}, false, nil
	}
	if err != nil {
		return domain.StoredEvent{}, false, err
	}
	list := []domain.StoredEvent{se}
	if err := s.attachChildren(ctx, list); err != nil {
		return domain.StoredEvent{}, false, err
	}
	return list[0], true, nil
}

func (s *Events) facet(ctx context.Context, col, where string, args []any) ([]app.FacetCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+col+`, count(*) FROM events WHERE `+where+` GROUP BY `+col+` ORDER BY count(*) DESC, `+col+` ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.FacetCount{}
	for rows.Next() {
		var fc app.FacetCount
		if err := rows.Scan(&fc.Value, &fc.Count); err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	return out, rows.Err()
}

func buildFilter(q app.Query) (string, []any, int) {
	where := "user_id = $1"
	args := []any{q.UserID}
	idx := 1
	add := func(cond string, val any) {
		idx++
		where += fmt.Sprintf(cond, idx)
		args = append(args, val)
	}
	// One value is written as an equality, which the planner matches with the
	// (user_id, <dimension>, timeline order) indexes of 0022 for an ordered
	// scan; several stay an = ANY.
	in := func(column string, values []string) {
		if len(values) == 1 {
			add(" AND "+column+" = $%d", values[0])
		} else if len(values) > 1 {
			add(" AND "+column+" = ANY($%d)", pq.Array(values))
		}
	}
	in("source_id", q.Sources)
	in("category", q.Categories)
	in("type", q.Types)
	// Date bounds in the timeline order's own expression, so they bound the
	// index scan instead of filtering it; undated events are excluded
	// explicitly (as the contract says once either bound is set).
	if !q.From.IsZero() || !q.To.IsZero() {
		where += " AND occurred_at IS NOT NULL"
	}
	if !q.From.IsZero() {
		add(" AND COALESCE(occurred_at, TIMESTAMPTZ '-infinity') >= $%d", q.From.UTC())
	}
	if !q.To.IsZero() {
		add(" AND COALESCE(occurred_at, TIMESTAMPTZ '-infinity') <= $%d", q.To.UTC())
	}
	if strings.TrimSpace(q.Search) != "" {
		add(" AND search_vector @@ plainto_tsquery('simple', $%d)", q.Search)
	}
	return where, args, idx
}

type scanner interface{ Scan(dest ...any) error }

func scanEvent(sc scanner) (domain.StoredEvent, error) {
	var (
		se       domain.StoredEvent
		occ      sql.NullTime
		actor    sql.NullString
		thread   sql.NullString
		parent   sql.NullString
		job      sql.NullString
		metaJSON []byte
	)
	if err := sc.Scan(
		&se.ID, &se.UserID, &se.AccountID, &se.SourceID, &se.ExternalID, &se.Type, &se.Category,
		&occ, &actor, &se.Title, &se.Body, &thread, &parent, &metaJSON, &se.IngestedAt, &job,
	); err != nil {
		return domain.StoredEvent{}, err
	}
	if occ.Valid {
		se.OccurredAt = occ.Time.UTC()
	}
	se.Actor = actor.String
	se.ThreadExternalID = thread.String
	se.ParentExternalID = parent.String
	se.ImportJobID = job.String
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &se.Metadata)
	}
	return se, nil
}

func (s *Events) attachChildren(ctx context.Context, evs []domain.StoredEvent) error {
	if len(evs) == 0 {
		return nil
	}
	byID := make(map[string]int, len(evs))
	ids := make([]string, len(evs))
	for i := range evs {
		evs[i].Targets = nil
		evs[i].Media = nil
		ids[i] = evs[i].ID
		byID[evs[i].ID] = i
	}
	tr, err := s.db.QueryContext(ctx,
		`SELECT event_id, relation, target_source, target_external_id, target_url
		 FROM event_targets WHERE event_id = ANY($1) ORDER BY event_id, position, id`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer tr.Close()
	for tr.Next() {
		var eid string
		var t domain.Target
		var src, ext, url sql.NullString
		if err := tr.Scan(&eid, &t.Relation, &src, &ext, &url); err != nil {
			return err
		}
		t.TargetSource, t.TargetExternalID, t.TargetURL = src.String, ext.String, url.String
		if i, ok := byID[eid]; ok {
			evs[i].Targets = append(evs[i].Targets, t)
		}
	}
	if err := tr.Err(); err != nil {
		return err
	}
	mr, err := s.db.QueryContext(ctx,
		`SELECT event_id, blob_key, mime, url FROM media WHERE event_id = ANY($1) ORDER BY event_id, position, id`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer mr.Close()
	for mr.Next() {
		var eid string
		var m domain.Media
		var mime, url sql.NullString
		if err := mr.Scan(&eid, &m.BlobKey, &mime, &url); err != nil {
			return err
		}
		m.MIME, m.URL = mime.String, url.String
		if i, ok := byID[eid]; ok {
			evs[i].Media = append(evs[i].Media, m)
		}
	}
	return mr.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
