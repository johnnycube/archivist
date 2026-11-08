package domain

import "encoding/json"

// Coverage is the "respect all data" accounting for one import: what was seen,
// mapped, captured generically, and produced. It is a domain result, not an
// adapter detail.
//
// The JSON tags are the PERSISTED form — the import_jobs.coverage jsonb column
// (postgres adapter) and the CLI's report (CoverageJSON) — so renaming one
// orphans stored data. They are not the API: the REST adapter maps Coverage to
// its own response type.
type Coverage struct {
	Members    int `json:"members"`
	Mapped     int `json:"mapped"`
	Generic    int `json:"generic"`
	Skipped    int `json:"skipped"`
	Events     int `json:"events"`
	Duplicates int `json:"duplicates"`
	// DedupeTruncated reports that the importer's in-memory dedupe set filled up,
	// so Duplicates undercounts; the store's idempotent upsert still dedupes.
	DedupeTruncated bool `json:"dedupe_truncated,omitempty"`
	// Cross-import idempotency accounting: New counts events archived for the
	// first time; Reimported counts events that already existed from an earlier
	// import of the same data and were refreshed in place.
	New        int `json:"new"`
	Reimported int `json:"reimported"`
	// Superseded counts events of this import that an earlier run had
	// written and this one no longer derived (removed after the run).
	Superseded int `json:"superseded,omitempty"`
	// MediaStored counts archive members persisted to the blob store.
	MediaStored int `json:"media_stored,omitempty"`
	// Truncated counts events with a field cut to its size bound
	// (Event.Capped).
	Truncated      int            `json:"truncated,omitempty"`
	Bytes          int64          `json:"bytes"`
	PerType        map[string]int `json:"per_type"`
	GenericMembers []string       `json:"generic_members"`
}

// NewCoverage returns an initialized Coverage.
func NewCoverage() *Coverage { return &Coverage{PerType: map[string]int{}} }

// CoverageJSON renders a compact, indented coverage report.
func CoverageJSON(c *Coverage) string {
	b, _ := json.MarshalIndent(c, "", "  ")
	return string(b)
}
