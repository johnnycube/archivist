// Command archivist-demo imports a platform export ZIP for a chosen source and
// prints the coverage report plus a sample of normalized Events — a CLI driving
// adapter over the archive engine.
//
//	go run ./cmd/archivist-demo -source reddit -zip export.zip
//	go run ./cmd/archivist-demo -list
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/johnnycube/archivist/internal/adapters/catalog"
	"github.com/johnnycube/archivist/internal/adapters/engine"
	"github.com/johnnycube/archivist/internal/adapters/staging"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

func main() {
	source := flag.String("source", "", "source id")
	zipPath := flag.String("zip", "", "path to the export .zip (comma-separate the parts of a split export)")
	limit := flag.Int("n", 15, "max sample events to print")
	list := flag.Bool("list", false, "list available sources")
	flag.Parse()

	cat := catalog.New()
	if *list {
		for _, d := range cat.Descriptors() {
			types := make([]string, 0, len(d.EventTypes))
			for _, et := range d.EventTypes {
				types = append(types, et.Type)
			}
			fmt.Printf("%-10s %-14s %s\n", d.ID, "("+string(d.IngestionModes[0])+")", strings.Join(types, ", "))
		}
		return
	}

	p, ok := cat.Bundler(*source)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown -source %q\n", *source)
		os.Exit(2)
	}
	if *zipPath == "" {
		fmt.Fprintln(os.Stderr, "error: -zip is required")
		os.Exit(2)
	}

	var sample []domain.Event
	// -zip takes one archive, or the comma-separated parts of a split export.
	var parts []app.ArchivePart
	for _, path := range strings.Split(*zipPath, ",") {
		f, err := staging.OpenFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open archive: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		parts = append(parts, f)
	}
	cov, err := engine.NewWith(cat.Bundler).Import(context.Background(), *source, app.ArchiveInput{
		Parts: parts,
		Emit: func(ev domain.Event) error {
			if len(sample) < *limit {
				sample = append(sample, ev)
			}
			return nil
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("── %s coverage ─────────────────────────────\n", p.Descriptor().DisplayName)
	fmt.Println(domain.CoverageJSON(cov))
	fmt.Printf("\n── Sample events (%d of %d) ────────────────────\n", len(sample), cov.Events)
	for _, ev := range sample {
		when := "     (undated)"
		if !ev.OccurredAt.IsZero() {
			when = ev.OccurredAt.Format("2006-01-02")
		}
		line := fmt.Sprintf("%s  %-18s  %s", when, ev.Type, oneLine(firstNonEmpty(ev.Body, ev.Title)))
		if len(ev.Targets) > 0 {
			t := ev.Targets[0]
			line += fmt.Sprintf("   → [%s %s]", t.Relation, firstNonEmpty(t.TargetURL, t.TargetExternalID))
		}
		fmt.Println(line)
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
