// Package catalog implements app.ProviderCatalog. It is the one place that
// imports every concrete provider package, keeping that dependency at the edge.
package catalog

import (
	"sort"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
	"github.com/johnnycube/archivist/providers/chatgpt"
	"github.com/johnnycube/archivist/providers/claude"
	"github.com/johnnycube/archivist/providers/discord"
	"github.com/johnnycube/archivist/providers/facebook"
	"github.com/johnnycube/archivist/providers/google"
	"github.com/johnnycube/archivist/providers/instagram"
	"github.com/johnnycube/archivist/providers/linkedin"
	"github.com/johnnycube/archivist/providers/reddit"
	"github.com/johnnycube/archivist/providers/snapchat"
	"github.com/johnnycube/archivist/providers/spotify"
	"github.com/johnnycube/archivist/providers/telegram"
	"github.com/johnnycube/archivist/providers/tiktok"
	"github.com/johnnycube/archivist/providers/twitter"
	"github.com/johnnycube/archivist/providers/whatsapp"
)

// Catalog is the registry of bundle-capable providers.
type Catalog struct {
	bundlers map[string]provider.BundleProvider
}

// New returns the default catalog with all built-in providers registered.
func New() *Catalog {
	return &Catalog{bundlers: map[string]provider.BundleProvider{
		"reddit":    reddit.New(),
		"chatgpt":   chatgpt.New(),
		"claude":    claude.New(),
		"twitter":   twitter.New(),
		"instagram": instagram.New(),
		"facebook":  facebook.New(),
		"google":    google.New(),
		"whatsapp":  whatsapp.New(),
		"tiktok":    tiktok.New(),
		"telegram":  telegram.New(),
		"snapchat":  snapchat.New(),
		"spotify":   spotify.New(),
		"linkedin":  linkedin.New(),
		"discord":   discord.New(),
	}}
}

var _ app.ProviderCatalog = (*Catalog)(nil)

// Bundler resolves a source id to its provider. It is not part of the app
// port: the composition root hands it to the archive engine (engine.NewWith).
func (c *Catalog) Bundler(id string) (provider.BundleProvider, bool) {
	p, ok := c.bundlers[id]
	return p, ok
}

// Known reports whether a source with this id is registered.
func (c *Catalog) Known(id string) bool {
	_, ok := c.bundlers[id]
	return ok
}

func (c *Catalog) Descriptors() []domain.Descriptor {
	ids := make([]string, 0, len(c.bundlers))
	for id := range c.bundlers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]domain.Descriptor, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.bundlers[id].Descriptor())
	}
	return out
}
