// Package provider is the service-provider interface that source adapters under
// /providers implement and the bundle importer drives. It deals in adapter-level
// mechanics (io.Reader, archive member paths), so it lives outside the domain;
// it imports the domain only. What a source *is* (domain.Descriptor, TypeDef,
// Mode) and what it produces (domain.Event) stay in the domain.
//
// Only export bundles are ingested today. A future live-pull SPI (API/OAuth:
// credentials, cursors, a Sync method) belongs here as a sibling interface of
// BundleProvider that embeds Provider — not in internal/domain.
package provider

import (
	"context"
	"io"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// Capabilities declares runtime behavior and security posture.
type Capabilities struct {
	Incremental    bool
	SupportsBundle bool
	NeedsEgress    []string // outbound allowlist (SSRF defense)
}

// RawRecord is the verbatim unit emitted by an Extractor, before normalization.
type RawRecord struct {
	ExternalID string
	Kind       string
	OccurredAt time.Time
	Payload    []byte
	// Member is the archive member path the record came from. The importer
	// fills it in when the extractor leaves it empty.
	Member string
	// Archive is the base name of the uploaded ZIP the member came from, as the
	// user named it ("WhatsApp Chat - Book Club.zip"). Set by the importer; empty
	// when the original upload name is unknown.
	Archive string
	// ContentHash is the in-import dedupe key. Empty means "hash Payload".
	ContentHash string
}

// Provider is implemented by every source.
type Provider interface {
	Descriptor() domain.Descriptor
	Capabilities() Capabilities
	Normalize(ctx context.Context, raw RawRecord) ([]domain.Event, error)
}

// BundleProvider is a source ingested from an export archive.
type BundleProvider interface {
	Provider
	Extractors() []Extractor
}

// Extractor handles one kind of member file within an export bundle.
type Extractor interface {
	Match(memberPath string) bool
	Extract(memberPath string, r io.Reader, emit func(RawRecord) error) error
}

// AttachmentMatcher is an optional interface of a BundleProvider. Members that
// no Extractor matches are stored as blobs (keyed by member path) when they are
// media by extension; a provider whose events reference other files — a chat's
// PDFs, vCards, stickers — implements AttachmentMatcher so those members are
// stored too instead of becoming "<source>.unknown" events, and the BlobKeys
// its events carry resolve.
type AttachmentMatcher interface {
	IsAttachment(memberPath string) bool
}

// Lookup resolves a source id to its bundle provider.
type Lookup func(id string) (BundleProvider, bool)
