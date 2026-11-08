package memory

import "time"

// Options configures a Store.
type Options struct {
	// SessionTTL is the absolute session lifetime (<= 0: app.DefaultSessionTTL).
	SessionTTL time.Duration
	// SessionIdleTTL ends sessions unused for that long (<= 0: no idle timeout).
	SessionIdleTTL time.Duration
}

// Store is the in-memory counterpart of one PostgreSQL database: the adapters
// of every port, wired so that they behave like tables with foreign keys —
// deleting a user takes the user's accounts, events, import jobs and sessions
// with it. The adapters' own constructors still work; stores built from them
// are independent of each other and cascade nothing.
type Store struct {
	Users    *Users
	Accounts *Accounts
	Events   *Events
	Jobs     *Jobs
	Sessions *Sessions
	Attempts *Attempts
}

// NewStore returns an empty store.
func NewStore(o Options) *Store {
	s := &Store{
		Users:    NewUsers(),
		Accounts: NewAccounts(),
		Events:   NewEvents(),
		Jobs:     NewJobs(),
		Sessions: NewSessions(o.SessionTTL),
		Attempts: NewAttempts(),
	}
	s.Sessions.IdleTTL = o.SessionIdleTTL
	s.Users.onDelete = []func(string){
		s.Sessions.deleteUser,
		s.Events.deleteUser,
		s.Jobs.deleteUser,
		s.Accounts.deleteUser,
	}
	return s
}
