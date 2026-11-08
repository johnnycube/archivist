package engine_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/johnnycube/archivist/internal/adapters/catalog"
	"github.com/johnnycube/archivist/internal/adapters/engine"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/whatsapp"
)

// namedPart is an archive part that is not file-backed and knows its upload name.
type namedPart struct {
	*bytes.Reader
	name string
}

func (p namedPart) Name() string { return p.name }

func chatZip(t *testing.T) []byte {
	t.Helper()
	data, err := archivetest.Zip(map[string]string{"_chat.txt": "[15/04/2022, 14:03:21] Ann: Hello\n[16/04/2022, 09:00:00] Max: Hi\n"})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func importThreads(t *testing.T, e engine.Engine, part app.ArchivePart) map[string]int {
	t.Helper()
	threads := map[string]int{}
	cov, err := e.Import(context.Background(), "whatsapp", app.ArchiveInput{
		Parts: []app.ArchivePart{part},
		Emit:  func(ev domain.Event) error { threads[ev.ThreadExternalID]++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if cov.Events != 2 {
		t.Fatalf("Events = %d, want 2", cov.Events)
	}
	return threads
}

// Any io.ReaderAt with a Size is an archive part — nothing needs a file path —
// and a part that knows its upload name passes it on to the provider.
func TestImportFromMemoryParts(t *testing.T) {
	data := chatZip(t)
	e := engine.NewWith(catalog.New().Bundler)
	if got := importThreads(t, e, namedPart{bytes.NewReader(data), ""}); got["chat:WhatsApp chat"] != 2 {
		t.Errorf("anonymous part: threads = %v", got)
	}
	named := namedPart{bytes.NewReader(data), "WhatsApp Chat - Book Club.zip"}
	if got := importThreads(t, e, named); got["chat:Book Club"] != 2 {
		t.Errorf("named part: threads = %v, want the archive's chat name", got)
	}
}

func TestImportResolvesSourceThroughLookup(t *testing.T) {
	if _, err := engine.NewWith(catalog.New().Bundler).Import(context.Background(), "nope", app.ArchiveInput{}); err == nil || !strings.Contains(err.Error(), `unknown source "nope"`) {
		t.Errorf("unknown source: err = %v", err)
	}
	only := engine.NewWith(func(id string) (provider.BundleProvider, bool) {
		return whatsapp.New(), id == "wa"
	})
	if _, err := only.Import(context.Background(), "whatsapp", app.ArchiveInput{}); err == nil {
		t.Error("NewWith must resolve through the given lookup only")
	}
	data := chatZip(t)
	cov, err := only.Import(context.Background(), "wa", app.ArchiveInput{
		Parts: []app.ArchivePart{namedPart{bytes.NewReader(data), ""}},
		Emit:  func(domain.Event) error { return nil },
	})
	if err != nil || cov.Events != 2 {
		t.Errorf("custom lookup: cov = %+v, err = %v", cov, err)
	}
}
