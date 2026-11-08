package rest

import (
	"encoding/json"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// The wire format of the API. Responses are built from these types only — never
// by serialising a domain or application struct — so that the JSON the SPA
// depends on is decided here, changes to the domain cannot leak onto the wire
// (tenancy ids, staging refs, password hashes), and a wire change is a visible
// diff in this file and in testdata/*.golden.json.

type userDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// HasPassword is false for an account that only logs in through an
	// identity provider: the UI then offers to set a password instead of
	// asking for the current one.
	HasPassword bool `json:"has_password"`
	// LinkedProviders are the identity providers (ids as in GET
	// /api/auth/oidc) the account can log in through; filled by
	// /api/auth/me, empty in the signup and login answers.
	LinkedProviders []string `json:"linked_providers"`
}

func toUserDTO(u domain.User, linked []string) userDTO {
	if linked == nil {
		linked = []string{}
	}
	return userDTO{ID: u.ID, Email: u.Email, HasPassword: u.PasswordHash != "", LinkedProviders: linked}
}

type coverageDTO struct {
	Members         int            `json:"members"`
	Mapped          int            `json:"mapped"`
	Generic         int            `json:"generic"`
	Skipped         int            `json:"skipped"`
	Events          int            `json:"events"`
	Duplicates      int            `json:"duplicates"`
	DedupeTruncated bool           `json:"dedupe_truncated,omitempty"`
	New             int            `json:"new"`
	Reimported      int            `json:"reimported"`
	MediaStored     int            `json:"media_stored,omitempty"`
	Truncated       int            `json:"truncated,omitempty"`
	Superseded      int            `json:"superseded,omitempty"`
	Bytes           int64          `json:"bytes"`
	PerType         map[string]int `json:"per_type"`
	GenericMembers  []string       `json:"generic_members"`
}

func toCoverageDTO(c *domain.Coverage) *coverageDTO {
	if c == nil {
		return nil
	}
	return &coverageDTO{
		Members: c.Members, Mapped: c.Mapped, Generic: c.Generic, Skipped: c.Skipped,
		Events: c.Events, Duplicates: c.Duplicates, DedupeTruncated: c.DedupeTruncated,
		New: c.New, Reimported: c.Reimported, MediaStored: c.MediaStored, Truncated: c.Truncated, Superseded: c.Superseded, Bytes: c.Bytes,
		PerType: c.PerType, GenericMembers: c.GenericMembers,
	}
}

// jobDTO is an import job. The owner's id and the staging refs of the upload
// (domain.ImportJob.ZipPaths) stay on the server.
type jobDTO struct {
	ID           string       `json:"id"`
	Source       string       `json:"source"`
	Label        string       `json:"label"`
	PartNames    []string     `json:"part_names,omitempty"`
	Parts        int          `json:"parts"`
	ArchiveBytes int64        `json:"archive_bytes"`
	Status       string       `json:"status"`
	Attempts     int          `json:"attempts,omitempty"`
	Saved        int          `json:"saved"`
	BytesDone    int64        `json:"bytes_done"`
	BytesTotal   int64        `json:"bytes_total"`
	Coverage     *coverageDTO `json:"coverage,omitempty"`
	Error        string       `json:"error,omitempty"`
	// CancelRequested: the owner cancelled this running import; the worker
	// stops it within moments.
	CancelRequested bool      `json:"cancel_requested,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func toJobDTO(j domain.ImportJob) jobDTO {
	return jobDTO{
		ID: j.ID, Source: j.Source, Label: j.Label, PartNames: j.PartNames, Parts: j.Parts,
		ArchiveBytes: j.ArchiveBytes, Status: string(j.Status), Attempts: j.Attempts,
		Saved: j.Saved, BytesDone: j.BytesDone, BytesTotal: j.BytesTotal,
		Coverage: toCoverageDTO(j.Coverage), Error: j.Error, CancelRequested: j.CancelRequested,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

func toJobDTOs(jobs []domain.ImportJob) []jobDTO {
	out := make([]jobDTO, len(jobs)) // never null on the wire
	for i, j := range jobs {
		out[i] = toJobDTO(j)
	}
	return out
}

type targetDTO struct {
	Relation         string `json:"relation"`
	TargetSource     string `json:"target_source,omitempty"`
	TargetExternalID string `json:"target_external_id,omitempty"`
	TargetURL        string `json:"target_url,omitempty"`
}

// mediaDTO is an attachment. BlobKey is the attachment's path inside the
// user's own export ("" = not stored); the bytes are served by
// GET /api/events/{id}/media/{index}.
type mediaDTO struct {
	BlobKey string `json:"blob_key"`
	MIME    string `json:"mime,omitempty"`
	URL     string `json:"url,omitempty"`
}

// eventDTO is a timeline event. The owner's and the account's ids stay on the
// server. An undated event carries the zero time, as it always has.
type eventDTO struct {
	ID          string    `json:"id"`
	ImportJobID string    `json:"import_job_id,omitempty"`
	IngestedAt  time.Time `json:"ingested_at"`

	SourceID   string `json:"source_id"`
	ExternalID string `json:"external_id"`
	Type       string `json:"type"`
	Category   string `json:"category"`

	OccurredAt time.Time `json:"occurred_at"`
	Actor      string    `json:"actor,omitempty"`

	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`

	ThreadExternalID string      `json:"thread_external_id,omitempty"`
	ParentExternalID string      `json:"parent_external_id,omitempty"`
	Targets          []targetDTO `json:"targets,omitempty"`
	Media            []mediaDTO  `json:"media,omitempty"`

	Metadata map[string]any `json:"metadata,omitempty"`
}

func toEventDTO(e domain.StoredEvent) eventDTO {
	out := eventDTO{
		ID: e.ID, ImportJobID: e.ImportJobID, IngestedAt: e.IngestedAt,
		SourceID: e.SourceID, ExternalID: e.ExternalID, Type: e.Type, Category: string(e.Category),
		OccurredAt: e.OccurredAt, Actor: e.Actor, Title: e.Title, Body: e.Body,
		ThreadExternalID: e.ThreadExternalID, ParentExternalID: e.ParentExternalID,
		Metadata: e.Metadata,
	}
	for _, t := range e.Targets {
		out.Targets = append(out.Targets, targetDTO{
			Relation: string(t.Relation), TargetSource: t.TargetSource,
			TargetExternalID: t.TargetExternalID, TargetURL: t.TargetURL,
		})
	}
	for _, m := range e.Media {
		out.Media = append(out.Media, mediaDTO{BlobKey: m.BlobKey, MIME: m.MIME, URL: m.URL})
	}
	return out
}

type facetCountDTO struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type facetsDTO struct {
	Sources    []facetCountDTO `json:"sources"`
	Categories []facetCountDTO `json:"categories"`
	Types      []facetCountDTO `json:"types"`
}

func toFacetCountDTOs(in []app.FacetCount) []facetCountDTO {
	out := make([]facetCountDTO, len(in))
	for i, f := range in {
		out[i] = facetCountDTO{Value: f.Value, Count: f.Count}
	}
	return out
}

type pageDTO struct {
	Events     []eventDTO `json:"events"`
	NextCursor string     `json:"next_cursor,omitempty"`
	Facets     facetsDTO  `json:"facets"`
	Total      int        `json:"total"`
}

func toPageDTO(p app.Page) pageDTO {
	out := pageDTO{
		Events: make([]eventDTO, len(p.Events)), NextCursor: p.NextCursor, Total: p.Total,
		Facets: facetsDTO{
			Sources:    toFacetCountDTOs(p.Facets.Sources),
			Categories: toFacetCountDTOs(p.Facets.Categories),
			Types:      toFacetCountDTOs(p.Facets.Types),
		},
	}
	for i, e := range p.Events {
		out.Events[i] = toEventDTO(e)
	}
	return out
}

// clusterDTO is one collapsed run of repetitive events on a collapsed page
// (GET /api/events?collapse=1). first_*/last_* are the run's first and last
// members IN PAGE ORDER: first is the newest event, last the oldest; the page's
// cursor continues after last. No other member id leaves the server.
type clusterDTO struct {
	Kind         string    `json:"kind"` // "cluster"
	SourceID     string    `json:"source_id"`
	Type         string    `json:"type"`
	Category     string    `json:"category"`
	Count        int       `json:"count"`
	FirstAt      time.Time `json:"first_at"` // zero for undated runs
	LastAt       time.Time `json:"last_at"`
	SampleTitles []string  `json:"sample_titles"` // never null; up to 3 distinct
	FirstID      string    `json:"first_id"`
	LastID       string    `json:"last_id"`
}

func toClusterDTO(c app.Cluster) clusterDTO {
	out := clusterDTO{
		Kind: "cluster", SourceID: c.SourceID, Type: c.Type, Category: string(c.Category), Count: c.Count,
		FirstAt: c.FirstAt, LastAt: c.LastAt, SampleTitles: c.SampleTitles, FirstID: c.FirstID, LastID: c.LastID,
	}
	if out.SampleTitles == nil {
		out.SampleTitles = []string{}
	}
	return out
}

// collapsedEventDTO is an event item of a collapsed page: the event DTO with
// kind "event" in front.
type collapsedEventDTO struct {
	Kind string `json:"kind"`
	eventDTO
}

// collapsedItemDTO is one item of a collapsed page, serialised as whichever of
// the two it is (a flat object with a "kind" discriminator, no wrapper).
type collapsedItemDTO struct {
	event   *collapsedEventDTO
	cluster *clusterDTO
}

func (it collapsedItemDTO) MarshalJSON() ([]byte, error) {
	if it.cluster != nil {
		return json.Marshal(it.cluster)
	}
	return json.Marshal(it.event)
}

func toCollapsedItemDTO(it app.CollapsedItem) collapsedItemDTO {
	if it.Cluster != nil {
		c := toClusterDTO(*it.Cluster)
		return collapsedItemDTO{cluster: &c}
	}
	var e domain.StoredEvent
	if it.Event != nil {
		e = *it.Event
	}
	return collapsedItemDTO{event: &collapsedEventDTO{Kind: "event", eventDTO: toEventDTO(e)}}
}

// collapsedPageDTO is a page of GET /api/events?collapse=1: items instead of
// events, otherwise the same as pageDTO. limit counts items.
type collapsedPageDTO struct {
	Items      []collapsedItemDTO `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
	Facets     facetsDTO          `json:"facets"`
	Total      int                `json:"total"`
}

func toCollapsedPageDTO(p app.CollapsedPage) collapsedPageDTO {
	out := collapsedPageDTO{
		Items: make([]collapsedItemDTO, len(p.Items)), NextCursor: p.NextCursor, Total: p.Total,
		Facets: facetsDTO{
			Sources:    toFacetCountDTOs(p.Facets.Sources),
			Categories: toFacetCountDTOs(p.Facets.Categories),
			Types:      toFacetCountDTOs(p.Facets.Types),
		},
	}
	for i, it := range p.Items {
		out.Items[i] = toCollapsedItemDTO(it)
	}
	return out
}

type bucketDTO struct {
	Day      string         `json:"day"` // YYYY-MM-DD (UTC)
	BySource map[string]int `json:"by_source"`
}

func toBucketDTOs(in []app.HistogramBucket) []bucketDTO {
	out := make([]bucketDTO, len(in))
	for i, b := range in {
		out[i] = bucketDTO{Day: b.Day, BySource: b.Counts}
	}
	return out
}

// categoryBucketDTO is a histogram bucket of GET /api/events/histogram?by=category.
type categoryBucketDTO struct {
	Day        string         `json:"day"` // YYYY-MM-DD (UTC)
	ByCategory map[string]int `json:"by_category"`
}

func toCategoryBucketDTOs(in []app.HistogramBucket) []categoryBucketDTO {
	out := make([]categoryBucketDTO, len(in))
	for i, b := range in {
		out[i] = categoryBucketDTO{Day: b.Day, ByCategory: b.Counts}
	}
	return out
}

type typeDefDTO struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	HasBody     bool   `json:"has_body"`
	NeedsTarget bool   `json:"needs_target"`
}

type sourceDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// Maturity is "verified" (met a real export) or "experimental".
	Maturity       string       `json:"maturity"`
	IngestionModes []string     `json:"ingestion_modes"`
	EventTypes     []typeDefDTO `json:"event_types"`
}

func toSourceDTOs(in []domain.Descriptor) []sourceDTO {
	out := make([]sourceDTO, len(in))
	for i, d := range in {
		maturity := domain.MaturityExperimental
		if d.Verified() {
			maturity = domain.MaturityVerified
		}
		s := sourceDTO{
			ID: d.ID, DisplayName: d.DisplayName, Maturity: string(maturity),
			IngestionModes: make([]string, len(d.IngestionModes)),
			EventTypes:     make([]typeDefDTO, len(d.EventTypes)),
		}
		for k, m := range d.IngestionModes {
			s.IngestionModes[k] = string(m)
		}
		for k, t := range d.EventTypes {
			s.EventTypes[k] = typeDefDTO{Type: t.Type, Category: string(t.Category), HasBody: t.HasBody, NeedsTarget: t.NeedsTarget}
		}
		out[i] = s
	}
	return out
}
