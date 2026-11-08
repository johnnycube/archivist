// Package seed fills a demo account with a plausible, entirely fictional
// personal history: every provider the catalog registers and every event type
// each of them declares appears at least once, at realistic volumes, so that
// someone opening the demo sees what each source and type looks like on the
// timeline, in the modal, in the facets, the histogram and the cluster cards.
//
// It is a dev-only driving adapter: invoked from the composition root
// (SEED_DEMO=1), it talks to the application strictly through the driven
// ports, so it works against both the in-memory and the Postgres stores.
// The data set is a pure function of the code (fixed PRNG seed, fixed
// reference date) and external ids are stable ("seed-…"), so reseeding
// upserts the same events instead of piling up.
package seed

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Demo account credentials (documented in the boot log). Deliberately trivial
// for local use: seeding bypasses the signup password policy, and cmd/server
// refuses to seed a real database unless SEED_DEMO=force.
const (
	DemoEmail    = "demo"
	DemoPassword = "demo"
)

// batchSize is how many events go to the repository per round trip (the
// import worker's size, see docs/development.md).
const batchSize = 500

// Deps are the ports seeding needs. Blobs is optional: without it the media
// events are still seeded, just with nothing behind their attachments.
type Deps struct {
	Users    app.Users
	Hasher   app.PasswordHasher
	Accounts app.Accounts
	Events   app.EventRepository
	Blobs    app.Blobs
}

// Demo ensures the demo user exists and upserts the demo history for every
// source. It returns the number of events written.
func Demo(ctx context.Context, d Deps) (int, error) {
	hash, err := d.Hasher.Hash(DemoPassword)
	if err != nil {
		return 0, err
	}
	u, err := d.Users.Create(ctx, DemoEmail, hash)
	if errors.Is(err, app.ErrEmailTaken) {
		var ok bool
		u, ok, err = d.Users.ByEmail(ctx, DemoEmail)
		if err == nil && !ok {
			err = errors.New("demo user exists but lookup failed")
		}
	}
	if err != nil {
		return 0, err
	}

	g := generate(d.Blobs != nil)

	total := 0
	for _, source := range g.sources() {
		accID, err := d.Accounts.Ensure(ctx, u.ID, source, "demo")
		if err != nil {
			return total, fmt.Errorf("ensure %s account: %w", source, err)
		}
		evs := g.events[source]
		for len(evs) > 0 {
			n := min(batchSize, len(evs))
			if _, err := d.Events.SaveBatch(ctx, u.ID, accID, app.ImportRun{}, evs[:n]); err != nil {
				return total, fmt.Errorf("save %s events: %w", source, err)
			}
			total += n
			evs = evs[n:]
		}
	}

	// Attachments last: the store is write-once, so a re-seed is a no-op here.
	if d.Blobs != nil {
		for _, b := range g.blobs {
			if err := d.Blobs.Put(ctx, u.ID, b.key, bytes.NewReader(b.data)); err != nil {
				return total, fmt.Errorf("store demo blob %s: %w", b.key, err)
			}
		}
	}
	return total, nil
}

// Events returns the generated demo history without writing it anywhere, by
// source — for tests that check the data set itself. withMedia says whether
// attachments are rendered (as Demo does when Deps.Blobs is set).
func Events(withMedia bool) map[string][]domain.Event {
	return generate(withMedia).events
}

// generators build one source each. The order is fixed: it is part of what
// makes the data set deterministic.
var generators = []func(*gen){reddit, twitter, facebook, instagram, linkedin, chatgpt, claude, tiktok, whatsapp, telegram, discord, snapchat, spotify, google}

func generate(withMedia bool) *gen {
	g := newGen()
	g.media = withMedia
	for _, gen := range generators {
		gen(g)
	}
	return g
}
