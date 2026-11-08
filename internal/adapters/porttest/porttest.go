// Package porttest holds the executable contract of the application's driven
// ports. Every adapter of a port runs the same suite — the in-memory ones
// always, the PostgreSQL ones when DATABASE_URL is set — so "memory and
// postgres behave the same" is checked instead of assumed.
//
// The suites test the port CONTRACT (internal/app/ports.go), never an
// implementation detail: they only talk to the port and to Env, the hooks a
// store needs to create the rows a test depends on (a user to own an event, …).
// A factory must return a fresh, EMPTY store on every call; suites call it once
// per subtest.
//
// This package imports app, domain, the standard library and testing only —
// never an adapter.
package porttest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	// The event suite checks that a store buckets and collapses days in the
	// query's zone, which needs the IANA database. Test hosts that have none
	// (a container built FROM scratch or alpine) would otherwise skip the
	// interesting half of the contract; the server embeds it for the same
	// reason.
	_ "time/tzdata"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// Env creates the prerequisite rows a suite needs. Stores with referential
// integrity (PostgreSQL) need them to be real; EnvFrom builds an Env from the
// sibling ports of the same store.
type Env struct {
	// NewUser creates a user and returns its id.
	NewUser func(t *testing.T) string
	// NewAccount creates (or finds) the user's connection to source.
	NewAccount func(t *testing.T, userID, source string) string
	// NewLabeledAccount is NewAccount for a connection with its own label
	// (a second account of the same source).
	NewLabeledAccount func(t *testing.T, userID, source, label string) string
	// NewJob creates an import job events can be attributed to. Suites that
	// use it never claim jobs, so its status is irrelevant.
	NewJob func(t *testing.T, userID string) string
}

var seq atomic.Int64

// EnvFrom derives an Env from the store's own ports. Ports a suite does not
// need may be nil (see the Run functions).
func EnvFrom(users app.Users, accounts app.Accounts, jobs app.Jobs) Env {
	ctx := context.Background()
	return Env{
		NewUser: func(t *testing.T) string {
			t.Helper()
			u, err := users.Create(ctx, fmt.Sprintf("user-%d@porttest.example", seq.Add(1)), "hash")
			if err != nil {
				t.Fatalf("create prerequisite user: %v", err)
			}
			return u.ID
		},
		NewAccount: func(t *testing.T, userID, source string) string {
			t.Helper()
			id, err := accounts.Ensure(ctx, userID, source, "porttest")
			if err != nil {
				t.Fatalf("create prerequisite account: %v", err)
			}
			return id
		},
		NewLabeledAccount: func(t *testing.T, userID, source, label string) string {
			t.Helper()
			id, err := accounts.Ensure(ctx, userID, source, label)
			if err != nil {
				t.Fatalf("create prerequisite account: %v", err)
			}
			return id
		},
		NewJob: func(t *testing.T, userID string) string {
			t.Helper()
			j, err := jobs.Create(ctx, domain.ImportJob{UserID: userID, Source: "reddit", Label: "porttest", Status: domain.JobDone})
			if err != nil {
				t.Fatalf("create prerequisite job: %v", err)
			}
			return j.ID
		},
	}
}

// unknownID is a well-formed id no store has issued; malformedID is what a
// hand-edited URL delivers. Both must read as "not found", never as an error.
const (
	unknownID   = "00000000-0000-4000-8000-000000000000"
	malformedID = "not-a-uuid"
)

// t0 is the suites' fixed point in time. Whole seconds only: stores may keep
// less than nanosecond precision (PostgreSQL: microseconds).
var t0 = time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)

var ctx = context.Background()
