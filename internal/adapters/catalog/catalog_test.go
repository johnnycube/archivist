package catalog

import "testing"

// Every registered provider is listed under the id it is registered with, and
// Known/Bundler agree with the listing.
func TestCatalogIsConsistent(t *testing.T) {
	c := New()
	ds := c.Descriptors()
	if len(ds) != len(c.bundlers) || len(ds) == 0 {
		t.Fatalf("Descriptors lists %d of %d providers", len(ds), len(c.bundlers))
	}
	for i, d := range ds {
		if i > 0 && ds[i-1].ID >= d.ID {
			t.Errorf("descriptors not sorted by id: %q before %q", ds[i-1].ID, d.ID)
		}
		p, ok := c.Bundler(d.ID)
		if !ok || !c.Known(d.ID) || p.Descriptor().ID != d.ID {
			t.Errorf("provider %q is not registered under its own id", d.ID)
		}
	}
	if _, ok := c.Bundler("nope"); ok || c.Known("nope") || c.Known("") {
		t.Error("unknown ids must not resolve")
	}
}
