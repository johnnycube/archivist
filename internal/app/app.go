package app

import (
	"log/slog"
	"time"
)

// DefaultSessionTTL is how long a login lasts unless configured otherwise. It
// is THE definition: the session stores and the cookie lifetime all derive
// from the one value the composition root passes down.
const DefaultSessionTTL = 30 * 24 * time.Hour

// DefaultSessionIdleTTL is how long a session may go unused before it ends,
// whatever is left of its absolute lifetime (see Sessions).
const DefaultSessionIdleTTL = 14 * 24 * time.Hour

// App aggregates the use cases the driving (inbound) adapters talk to.
type App struct {
	Auth     AuthService
	Import   ImportService
	Timeline TimelineService
	Worker   Worker
	// Admin is the operator's use case (the admin CLI); no transport exposes it.
	Admin AdminService
}

// Deps bundles the driven ports needed to construct the App.
type Deps struct {
	Events   EventRepository
	Users    Users
	Accounts Accounts
	Hasher   PasswordHasher
	Sessions Sessions
	// Attempts backs the login throttle (optional; nil = LogInGuarded does not
	// throttle).
	Attempts AttemptLimiter
	Catalog  ProviderCatalog
	Engine   ArchiveEngine
	Jobs     Jobs
	// Staging holds uploaded archive parts until the worker has imported them.
	// Required for imports.
	Staging Staging
	// Blobs stores the original archives and the media extracted from them
	// (optional; nil disables both).
	Blobs Blobs
	// Limits are the per-user import quotas (zero values = unlimited).
	Limits Limits
	// ImportBudget bounds each run of the import worker (zero = unbounded).
	ImportBudget ImportBudget
	// ImportObserver is told how each import run ended (optional; metrics).
	ImportObserver ImportObserver
	// SignupDisabled closes registration.
	SignupDisabled bool
	// OIDC are the identity providers people may log in through (optional).
	OIDC []OIDCProviderConfig
	// Log receives the background worker's failures and non-fatal use-case
	// problems (nil = discard).
	Log *slog.Logger
	// Clock is the application's one source of time (nil = time.Now): session
	// lifetimes, job timestamps and the worker's progress throttle all read it,
	// so tests can drive time instead of sleeping.
	Clock func() time.Time
}

// New wires the use cases from their ports (composition happens in main).
func New(d Deps) *App {
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	d.Log, d.Clock = log, clock
	gate := newUploadGate()
	auth := newAuthService(d)
	auth.uploadsInFlight = gate.count
	imports := ImportService{
		Jobs: d.Jobs, Catalog: d.Catalog, Events: d.Events, Blobs: d.Blobs,
		Staging: d.Staging, Limits: d.Limits, Clock: clock, Gate: gate,
	}
	return &App{
		Auth:     auth,
		Import:   imports,
		Admin:    AdminService{Auth: auth, Import: imports},
		Timeline: TimelineService{Events: d.Events, Catalog: d.Catalog, Blobs: d.Blobs, Jobs: d.Jobs},
		Worker: Worker{
			Jobs: d.Jobs, Catalog: d.Catalog, Engine: d.Engine,
			Events: d.Events, Accounts: d.Accounts, Blobs: d.Blobs,
			Staging: d.Staging, Log: log, Clock: clock, Budget: d.ImportBudget, Observer: d.ImportObserver,
		},
	}
}

// clockOrNow makes a zero-value service (constructed without New) usable.
func clockOrNow(clock func() time.Time) time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}
