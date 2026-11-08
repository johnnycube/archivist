package google

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnnycube/archivist/internal/archivetest"
	"github.com/johnnycube/archivist/internal/provider"
)

// seedRecords runs the extractors over both fixture trees and hands every raw
// record (kind, member, payload) to add.
func seedRecords(f *testing.F, add func(kind, member string, payload []byte)) {
	for _, dir := range []string{"en", "de"} {
		zipPath := filepath.Join(f.TempDir(), dir+".zip")
		if err := archivetest.ZipDir(filepath.Join("testdata", dir), zipPath); err != nil {
			f.Fatal(err)
		}
		zr, err := zip.OpenReader(zipPath)
		if err != nil {
			f.Fatal(err)
		}
		for _, m := range zr.File {
			if m.FileInfo().IsDir() {
				continue
			}
			var ex provider.Extractor
			for _, e := range New().Extractors() {
				if e.Match(m.Name) {
					ex = e
					break
				}
			}
			if ex == nil {
				continue
			}
			rc, err := m.Open()
			if err != nil {
				f.Fatal(err)
			}
			err = ex.Extract(m.Name, rc, func(raw provider.RawRecord) error {
				add(raw.Kind, m.Name, raw.Payload)
				return nil
			})
			rc.Close()
			if err != nil {
				f.Fatal(err)
			}
		}
		zr.Close()
	}
}

// FuzzNormalize: no payload may make Normalize panic or fail (an error aborts
// the whole import), and whatever it returns must be a declared type with a
// stable identity.
func FuzzNormalize(f *testing.F) {
	seedRecords(f, func(kind, member string, payload []byte) { f.Add(kind, member, payload) })
	declared := map[string]bool{}
	for _, d := range New().Descriptor().EventTypes {
		declared[d.Type] = true
	}
	p := New()
	f.Fuzz(func(t *testing.T, kind, member string, payload []byte) {
		evs, err := p.Normalize(context.Background(), provider.RawRecord{Kind: kind, Member: member, Payload: payload})
		if err != nil {
			t.Fatalf("Normalize returned an error (import would abort): %v", err)
		}
		if len(evs) == 0 {
			t.Errorf("record of kind %q vanished: no event, no error", kind)
		}
		for _, ev := range evs {
			if !declared[ev.Type] {
				t.Errorf("undeclared type %q from kind %q", ev.Type, kind)
			}
			if ev.ExternalID == "" || ev.SourceID != sourceID {
				t.Errorf("event without identity: %+v", ev)
			}
			if !ev.OccurredAt.IsZero() && ev.OccurredAt.Location().String() != "UTC" {
				t.Errorf("non-UTC time: %v", ev.OccurredAt)
			}
		}
	})
}

// FuzzExtract feeds mutated member contents through the matching extractor and
// then Normalize: the streaming decoders, the bookmark scanner and the CSV
// reader must fail cleanly, never panic.
func FuzzExtract(f *testing.F) {
	for _, dir := range []string{"en", "de"} {
		root := filepath.Join("testdata", dir)
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			member := filepath.ToSlash(rel)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f.Add(member, data)
			return nil
		})
	}
	p := New()
	f.Fuzz(func(t *testing.T, member string, data []byte) {
		var ex provider.Extractor
		for _, e := range p.Extractors() {
			if e.Match(member) {
				ex = e
				break
			}
		}
		if ex == nil {
			return
		}
		_ = ex.Extract(member, io.LimitReader(bytes.NewReader(data), 1<<20), func(raw provider.RawRecord) error {
			raw.Member = member
			_, err := p.Normalize(context.Background(), raw)
			return err
		})
	})
}
