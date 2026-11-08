// Command server is the composition root: it wires the adapters to the
// application use cases and starts the HTTP adapter. This is the only place that
// knows about concrete implementations.
//
//	make run                            # in-memory store + demo data (dev)
//	DATABASE_URL=postgres:///archivist?sslmode=disable MIGRATE=1 ./archivist
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	_ "time/tzdata"

	"time" // The timeline filters, buckets and collapses in the VIEWER'S zone

	"github.com/johnnycube/archivist/internal/domain"

	// (?tz=Europe/Berlin), which time.LoadLocation resolves against the
	// zoneinfo database. A scratch/Alpine runtime image has none, so the
	// server carries its own copy: without this every ?tz= would be a 400 in
	// the container and a "works on my machine" bug everywhere else.
	"github.com/johnnycube/archivist/internal/adapters/blob"
	"github.com/johnnycube/archivist/internal/adapters/catalog"
	"github.com/johnnycube/archivist/internal/adapters/crypto"
	"github.com/johnnycube/archivist/internal/adapters/engine"
	"github.com/johnnycube/archivist/internal/adapters/memory"
	"github.com/johnnycube/archivist/internal/adapters/metrics"
	"github.com/johnnycube/archivist/internal/adapters/postgres"
	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/adapters/staging"
	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/seed"
)

// version is stamped at build time via -ldflags "-X main.version=…".
var version = "dev"

const (
	// workerDrain is how long shutdown waits for the in-flight import before
	// interrupting it (an interrupted import is requeued on the next boot).
	workerDrain = 30 * time.Second
	// stagingSweepEvery is how often the janitor looks for orphaned uploads.
	stagingSweepEvery = 6 * time.Hour
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(runAdmin(os.Args[2:]))
	}
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := loadConfig()
	setupLogging(cfg.LogFormat)
	if err != nil {
		fatal("invalid configuration", err)
	}
	for _, w := range cfg.Warnings {
		slog.Warn(w)
	}

	deps, dbReady, blobsReady := wire(cfg)
	stats := metrics.New(version, deps.Jobs.Stats)
	deps.ImportObserver = stats

	maybeSeed(deps, cfg)

	// Identity providers are discovered before the server listens, so a bad
	// issuer or an unreachable provider is a boot failure, not a dead button.
	deps.OIDC = newOIDCProviders(cfg)

	application := app.New(deps)

	// Requeue imports orphaned by a previous crash before the worker starts.
	if req, failed, err := deps.Jobs.Recover(context.Background(), domain.DefaultMaxJobAttempts); err != nil {
		fatal("recover jobs", err)
	} else if req+failed > 0 {
		slog.Info("recovered orphaned import jobs", "requeued", req, "failed", failed)
	}

	// Closing stopWorker is the graceful stop (finish the in-flight import);
	// cancelling workerCtx interrupts it.
	workerCtx, interruptWorker := context.WithCancel(context.Background())
	stopWorker, workerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(workerDone)
		application.Worker.Run(workerCtx, stopWorker, 300*time.Millisecond)
	}()
	slog.Info("import worker started")

	// Janitor: uploads that never became an import (a request or the process
	// died mid-upload) are removed once they are STAGING_MAX_AGE old — at boot, then
	// every stagingSweepEvery, until shutdown.
	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()
	go sweepStaging(janitorCtx, application.Import, cfg.StagingMaxAge)

	srv := rest.New(application, cfg.CookieSecure)
	srv.MaxZip = cfg.MaxUploadBytes
	srv.MaxAge = int(cfg.SessionTTL / time.Second)
	srv.TrustedProxies = cfg.TrustedProxies
	srv.TrustedOrigins = cfg.TrustedOrigins
	srv.Metrics = stats
	srv.MetricsToken = cfg.MetricsToken
	srv.Version = version
	// /readyz: the database ping (a pool round trip) plus the blob store's
	// cached probe (one HEAD per probeTTL at most, however often it is asked).
	srv.Ready = readiness(dbReady, blobsReady)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No ReadTimeout/WriteTimeout: they would cut off multi-GB uploads and
		// media streams. Request bodies get their deadline per route instead
		// (rest.Server.BodyTimeout / UploadIdleTimeout).
	}

	go func() {
		slog.Info("Archivist listening", "addr", cfg.Addr, "version", version,
			"secure_cookies", cfg.CookieSecure, "trusted_proxies", cfg.TrustedProxies,
			"metrics_token", cfg.MetricsToken != "", "max_upload_mb", cfg.MaxUploadBytes>>20,
			"signup", cfg.AllowSignup, "session_ttl", cfg.SessionTTL.String(), "session_idle_ttl", cfg.SessionIdleTTL.String(), "max_active_imports", cfg.MaxActiveImports,
			"max_user_archive_gb", cfg.MaxArchiveBytes>>30, "staging_max_age", cfg.StagingMaxAge.String(),
			"import_timeout", cfg.ImportTimeout.String(), "max_import_events", cfg.MaxImportEvents,
			"oidc_providers", len(cfg.OIDC))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("listen", err)
		}
	}()

	// Graceful shutdown: stop accepting requests, drain in-flight ones, then
	// let the worker finish its current import (bounded).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("shutting down")
	stopJanitor()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
	close(stopWorker)
	select {
	case <-workerDone:
	case <-time.After(workerDrain):
		slog.Warn("import still running; interrupting it (it is requeued on the next start)", "waited", workerDrain)
		interruptWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			slog.Error("import worker did not stop")
		}
	}
	interruptWorker()
	slog.Info("bye")
}

// wire builds the driven adapters the configuration names: staging, the blob
// store (checked at once), and PostgreSQL (migrated when MIGRATE=1) or the
// in-memory store. It returns the ports for app.New and the readiness probes
// of the database and the blob store (nil = nothing remote to probe). The
// server and `archivist admin` share it, so both see the same data.
func wire(cfg config) (deps app.Deps, dbReady, blobsReady func(ctx context.Context) error) {
	stage, err := staging.NewFS(filepath.Join(cfg.DataDir, "uploads"))
	if err != nil {
		fatal("create upload staging dir", err)
	}

	// The blob store is checked before anything else: an unreachable object
	// store or rejected credentials end the boot here with a message that
	// says so, not on the first import.
	var blobs app.Blobs
	blobs, blobsReady = newBlobs(cfg)

	// One catalog: what the application lists is what the engine can parse.
	cat := catalog.New()
	deps = app.Deps{
		Hasher:         crypto.Hasher{},
		Catalog:        cat,
		Engine:         engine.NewWith(cat.Bundler),
		Staging:        stage,
		Blobs:          blobs,
		SignupDisabled: !cfg.AllowSignup,
		Limits: app.Limits{
			MaxActiveImports: cfg.MaxActiveImports,
			MaxArchiveBytes:  cfg.MaxArchiveBytes,
		},
		ImportBudget: app.ImportBudget{Timeout: cfg.ImportTimeout, MaxEvents: cfg.MaxImportEvents},
		Log:          slog.Default(),
	}

	if cfg.DatabaseURL != "" {
		dbReady = postgresStores(cfg, &deps)
	} else {
		memoryStores(cfg, &deps)
	}
	return deps, dbReady, blobsReady
}

// postgresStores opens (and with MIGRATE=1 migrates) the database and puts
// its adapters into deps; it returns the database's readiness probe.
func postgresStores(cfg config, deps *app.Deps) func(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		fatal("postgres", err)
	}
	if cfg.Migrate {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		applied, err := postgres.Migrate(ctx, db, cfg.MigrationsDir)
		cancel()
		if err != nil {
			fatal("migrate", err)
		}
		slog.Info("migrations applied", "count", len(applied), "files", applied)
	}
	deps.Events = postgres.NewEvents(db)
	deps.Users = postgres.NewUsers(db)
	deps.Accounts = postgres.NewAccounts(db)
	deps.Jobs = postgres.NewJobs(db)
	sessions := postgres.NewSessions(db, cfg.SessionTTL)
	sessions.IdleTTL = cfg.SessionIdleTTL
	deps.Sessions = sessions
	deps.Attempts = postgres.NewAttempts(db)
	slog.Info("store: postgres")
	return db.PingContext
}

// memoryStores puts the in-memory adapters into deps (development only).
func memoryStores(cfg config, deps *app.Deps) {
	// One Store, so that deleting a user cascades as it does in PostgreSQL.
	store := memory.NewStore(memory.Options{SessionTTL: cfg.SessionTTL, SessionIdleTTL: cfg.SessionIdleTTL})
	deps.Events = store.Events
	deps.Users = store.Users
	deps.Accounts = store.Accounts
	deps.Jobs = store.Jobs
	deps.Sessions = store.Sessions
	deps.Attempts = store.Attempts
	slog.Info("store: in-memory (set DATABASE_URL for postgres)")
}

// sweepStaging runs the staging janitor now and then periodically until ctx
// is cancelled.
func sweepStaging(ctx context.Context, imports app.ImportService, maxAge time.Duration) {
	t := time.NewTicker(stagingSweepEvery)
	defer t.Stop()
	for {
		removed, err := imports.SweepStaging(ctx, maxAge)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			slog.Warn("staging janitor", "err", err, "removed", removed)
		case removed > 0:
			slog.Info("staging janitor removed orphaned uploads", "removed", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// newBlobs picks the blob store: an S3-compatible object store (MinIO, S3, …)
// when BLOB_S3_ENDPOINT is set — the shipped deployment — otherwise the local
// filesystem under DATA_DIR (single-node fallback and `make run`). Both are
// write-once — ingested artifacts are immutable.
//
// For S3 the boot fails fast: a short connectivity/credentials check (HEAD
// bucket, 5 s) runs before the bucket is created if missing. The second
// return value is the store's readiness probe for /readyz (nil for the
// filesystem, which has nothing remote to fail).
func newBlobs(cfg config) (app.Blobs, func(ctx context.Context) error) {
	if cfg.S3.Endpoint == "" {
		dir := filepath.Join(cfg.DataDir, "blobs")
		slog.Info("blob store: filesystem (set BLOB_S3_ENDPOINT for S3/MinIO)", "dir", dir)
		return blob.NewFS(dir), nil
	}
	s3, err := blob.NewS3(blob.S3Config{
		Endpoint:  cfg.S3.Endpoint,
		Bucket:    cfg.S3.Bucket,
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		Region:    cfg.S3.Region,
		SSE:       cfg.S3.SSE,
	})
	if err != nil {
		fatal("s3 blobs", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s3.Check(ctx); err != nil {
		fatal("s3 blobs: connectivity check failed", err)
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		fatal("s3 blobs: ensure bucket", err)
	}
	slog.Info("blob store: s3", "endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket, "sse", cfg.S3.SSE)
	return s3, newCachedProbe("blobs", probeTTL, s3.Healthy).check
}

// maybeSeed fills the demo account in dev. Against a real database it refuses
// unless explicitly forced: a well-known demo login has no place in prod.
func maybeSeed(deps app.Deps, cfg config) {
	switch cfg.SeedDemo {
	case "1":
		if cfg.DatabaseURL != "" {
			slog.Warn("SEED_DEMO=1 ignored with DATABASE_URL set (use SEED_DEMO=force to override)")
			return
		}
	case "force":
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := seed.Demo(ctx, seed.Deps{Users: deps.Users, Hasher: deps.Hasher, Accounts: deps.Accounts, Events: deps.Events, Blobs: deps.Blobs})
	if err != nil {
		fatal("seed demo data", err)
	}
	slog.Info("seeded demo data", "events", n, "email", seed.DemoEmail, "password", seed.DemoPassword)
}

func setupLogging(format string) {
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(os.Stdout, nil)
	} else {
		h = slog.NewJSONHandler(os.Stdout, nil)
	}
	slog.SetDefault(slog.New(h))
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
