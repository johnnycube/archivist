package memory_test

import (
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/adapters/porttest"
	"github.com/johnnycube/archivist/internal/app"
)

// The in-memory adapters are held to the same port contract as the PostgreSQL
// ones (internal/adapters/postgres runs these suites when DATABASE_URL is set).

func env() porttest.Env {
	return porttest.EnvFrom(memory.NewUsers(), memory.NewAccounts(), memory.NewJobs())
}

func TestUsersContract(t *testing.T) {
	porttest.RunUsers(t, func(*testing.T) app.Users { return memory.NewUsers() })
}

func TestAccountsContract(t *testing.T) {
	porttest.RunAccounts(t, func(*testing.T) (app.Accounts, porttest.Env) { return memory.NewAccounts(), env() })
}

func TestSessionsContract(t *testing.T) {
	porttest.RunSessions(t, func(_ *testing.T, ttl, idle time.Duration) (app.Sessions, porttest.Env) {
		s := memory.NewSessions(ttl)
		s.IdleTTL = idle
		return s, env()
	})
}

func TestJobsContract(t *testing.T) {
	porttest.RunJobs(t, func(*testing.T) (app.Jobs, porttest.Env) {
		jobs := memory.NewJobs()
		return jobs, porttest.EnvFrom(memory.NewUsers(), memory.NewAccounts(), jobs)
	})
}

func TestEventRepositoryContract(t *testing.T) {
	porttest.RunEventRepository(t, func(*testing.T) (app.EventRepository, porttest.Env) {
		return memory.NewEvents(), env()
	})
}

func TestAttemptLimiterContract(t *testing.T) {
	porttest.RunAttemptLimiter(t, func(*testing.T) app.AttemptLimiter { return memory.NewAttempts() })
}

// A Store behaves like one database: deleting a user cascades.
func TestUserDeletionContract(t *testing.T) {
	porttest.RunUserDeletion(t, func(*testing.T) porttest.Store {
		s := memory.NewStore(memory.Options{SessionTTL: time.Hour})
		return porttest.Store{Users: s.Users, Accounts: s.Accounts, Events: s.Events, Jobs: s.Jobs, Sessions: s.Sessions}
	})
}

// The Store's adapters are the ordinary ones: they pass their own contracts too.
func TestStoreAdaptersContract(t *testing.T) {
	porttest.RunJobs(t, func(*testing.T) (app.Jobs, porttest.Env) {
		s := memory.NewStore(memory.Options{})
		return s.Jobs, porttest.EnvFrom(s.Users, s.Accounts, s.Jobs)
	})
	porttest.RunSessions(t, func(_ *testing.T, ttl, idle time.Duration) (app.Sessions, porttest.Env) {
		s := memory.NewStore(memory.Options{SessionTTL: ttl, SessionIdleTTL: idle})
		return s.Sessions, porttest.EnvFrom(s.Users, s.Accounts, s.Jobs)
	})
}
