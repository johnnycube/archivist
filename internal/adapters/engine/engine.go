// Package engine implements app.ArchiveEngine over the bundle import library.
package engine

import (
	"context"
	"fmt"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// Engine parses export archives using the bundle importer.
type Engine struct {
	lookup provider.Lookup
}

// NewWith returns a bundle-backed ArchiveEngine that resolves source ids with
// lookup: catalog.Catalog.Bundler of the composition root's one catalog — so
// the sources the application lists are exactly the ones it can parse — or a
// test's own providers.
func NewWith(lookup provider.Lookup) Engine { return Engine{lookup: lookup} }

var _ app.ArchiveEngine = Engine{}

// Import runs the source's extractors over the archive's ZIP part(s), emitting
// each Event and storing media and attachment members via in.PutBlob (nil
// disables that).
func (e Engine) Import(ctx context.Context, sourceID string, in app.ArchiveInput) (*domain.Coverage, error) {
	p, ok := e.lookup(sourceID)
	if !ok {
		return nil, fmt.Errorf("unknown source %q", sourceID)
	}
	parts := make([]bundle.Part, len(in.Parts))
	for i, part := range in.Parts {
		parts[i] = bundle.Part{ReaderAt: part, Size: part.Size(), Name: part.Name()}
	}
	im := bundle.NewImporter(p)
	im.PutBlob = in.PutBlob
	im.Progress = in.Progress
	return im.ImportParts(ctx, parts, in.Emit)
}
