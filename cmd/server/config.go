package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/rest"
	"github.com/johnnycube/archivist/internal/app"
)

// config is every setting the server reads from the environment, parsed and
// validated once at startup. Nothing else in the program calls os.Getenv.
type config struct {
	Addr          string
	DataDir       string
	DatabaseURL   string // "" = in-memory store (DATABASE_URL=memory)
	Migrate       bool
	MigrationsDir string
	LogFormat     string // json|text
	SeedDemo      string // ""|1|force

	CookieSecure   bool
	TrustedProxies int      // reverse proxies appending to X-Forwarded-For; 0 = ignore the header
	TrustedOrigins []string // extra Origins the CSRF check accepts
	MetricsToken   string   // "" = /metrics is open
	SessionTTL     time.Duration
	SessionIdleTTL time.Duration // 0 = no idle timeout
	AllowSignup    bool

	MaxUploadBytes   int64
	MaxActiveImports int
	MaxArchiveBytes  int64 // per user
	// StagingMaxAge is how old an upload that never became an import must be
	// before the janitor removes it.
	StagingMaxAge time.Duration
	// ImportTimeout ends an import that runs longer (0 = no limit);
	// MaxImportEvents ends one whose archive yields more events.
	ImportTimeout   time.Duration
	MaxImportEvents int

	S3 s3Config

	// OIDC are the identity providers (ARCHIVIST_OIDC_*, see oidc.go).
	OIDC []oidcProviderConfig
	// PublicURL is where users reach the server (PUBLIC_URL, scheme://host
	// [:port], no path): the base of the OIDC callback URLs.
	PublicURL string

	// Warnings are non-fatal findings (deprecated or risky settings), logged
	// at startup once logging is set up.
	Warnings []string
}

type s3Config struct {
	Endpoint, Bucket, AccessKey, SecretKey, Region string
	// SSE requests SSE-S3 (AES256, store-managed keys) on every upload.
	// Off by default: MinIO rejects it unless a KMS is configured.
	SSE bool
}

// loadConfig reads the environment. Every invalid value is reported, not just
// the first, so a broken deployment is fixed in one round.
func loadConfig() (config, error) {
	var errs []error
	str := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	// flag parses the project's 0/1 switches.
	flag := func(key string, def bool) bool {
		switch v := os.Getenv(key); v {
		case "":
			return def
		case "1", "true":
			return true
		case "0", "false":
			return false
		default:
			errs = append(errs, fmt.Errorf("%s=%q: want 1 or 0", key, v))
			return def
		}
	}
	positive := func(key string, def int64) int64 {
		v := os.Getenv(key)
		if v == "" {
			return def
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s=%q: want a positive whole number", key, v))
			return def
		}
		return n
	}

	c := config{
		Addr:          str("ADDR", ":8080"),
		DataDir:       str("DATA_DIR", "./data"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		Migrate:       flag("MIGRATE", false),
		MigrationsDir: str("MIGRATIONS_DIR", "migrations"),
		LogFormat:     str("LOG_FORMAT", "json"),
		SeedDemo:      os.Getenv("SEED_DEMO"),

		CookieSecure: flag("COOKIE_SECURE", true),
		MetricsToken: os.Getenv("METRICS_TOKEN"),
		SessionTTL:   app.DefaultSessionTTL,
		// Closed unless opened: a public instance must not accept strangers
		// by default. The first account comes from `archivist admin`.
		AllowSignup: flag("ALLOW_SIGNUP", false),

		SessionIdleTTL: app.DefaultSessionIdleTTL,

		MaxUploadBytes:   positive("MAX_UPLOAD_MB", rest.DefaultMaxZip>>20) << 20,
		MaxActiveImports: int(positive("MAX_ACTIVE_IMPORTS", 3)),
		MaxArchiveBytes:  positive("MAX_USER_ARCHIVE_GB", 50) << 30,
		StagingMaxAge:    app.DefaultStagingMaxAge,
		ImportTimeout:    defaultImportTimeout,
		MaxImportEvents:  int(positive("MAX_IMPORT_EVENTS", defaultMaxImportEvents)),

		S3: s3Config{
			Endpoint:  os.Getenv("BLOB_S3_ENDPOINT"),
			Bucket:    str("BLOB_S3_BUCKET", "archivist"),
			AccessKey: os.Getenv("BLOB_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("BLOB_S3_SECRET_KEY"),
			Region:    str("BLOB_S3_REGION", "us-east-1"),
			SSE:       flag("BLOB_S3_SSE", false),
		},
	}
	// The store is never chosen by omission: without DATABASE_URL a bare
	// `docker run` would keep every import in memory and lose it on restart.
	switch os.Getenv("DATABASE_URL") {
	case "":
		errs = append(errs, errors.New("DATABASE_URL is not set: point it at PostgreSQL, or set DATABASE_URL=memory for a throwaway in-memory store (development only: nothing persists)"))
	case "memory":
		c.DatabaseURL = ""
		c.Warnings = append(c.Warnings, "DATABASE_URL=memory: the in-memory development store; nothing survives a restart")
	}
	if sampleSecrets[c.S3.SecretKey] {
		errs = append(errs, errors.New("BLOB_S3_SECRET_KEY is the sample value from the compose file: set a secret of your own (and the object store's) in .env"))
	}
	if c.S3.Endpoint == "" {
		for _, k := range []string{"BLOB_S3_ACCESS_KEY", "BLOB_S3_SECRET_KEY", "BLOB_S3_SSE"} {
			if os.Getenv(k) != "" {
				c.Warnings = append(c.Warnings, k+" is set but BLOB_S3_ENDPOINT is not: blobs go to the filesystem under DATA_DIR/blobs")
				break
			}
		}
	}
	// TRUST_PROXY (pre-release builds) was removed: refuse it rather than silently trust no
	// proxy, which would put every client in one rate-limit bucket.
	if os.Getenv("TRUST_PROXY") != "" {
		errs = append(errs, errors.New("TRUST_PROXY was removed: set TRUSTED_PROXIES to the number of reverse proxies in front of the server (TRUST_PROXY=1 meant TRUSTED_PROXIES=1)"))
	}
	if v := os.Getenv("TRUSTED_PROXIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES=%q: want the number of reverse proxies in front of the server (0 = none)", v))
		}
		c.TrustedProxies = n
	}
	for _, o := range strings.Split(os.Getenv("TRUSTED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		// Validated by the consumer of the value itself.
		if err := http.NewCrossOriginProtection().AddTrustedOrigin(o); err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_ORIGINS: %w (want e.g. https://archive.example.com)", err))
			continue
		}
		c.TrustedOrigins = append(c.TrustedOrigins, o)
	}
	if t := c.MetricsToken; t != "" && (len(t) < minMetricsToken || strings.ContainsAny(t, " \t\r\n")) {
		errs = append(errs, fmt.Errorf("METRICS_TOKEN: want at least %d characters without whitespace", minMetricsToken))
	}
	if c.MetricsToken == "" && !isLoopbackAddr(c.Addr) {
		c.Warnings = append(c.Warnings, "/metrics is open to anyone who can reach "+c.Addr+": set METRICS_TOKEN or block the path at the proxy")
	}
	if v := os.Getenv("SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			errs = append(errs, fmt.Errorf("SESSION_TTL=%q: want a duration of at least 1m, e.g. 720h", v))
		} else {
			c.SessionTTL = d
		}
	}
	if v := os.Getenv("SESSION_IDLE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || (d != 0 && d < minSessionIdleTTL) {
			errs = append(errs, fmt.Errorf("SESSION_IDLE_TTL=%q: want 0 (no idle timeout) or a duration of at least %v, e.g. 336h", v, minSessionIdleTTL))
		} else {
			c.SessionIdleTTL = d
		}
	}
	if v := os.Getenv("STAGING_MAX_AGE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minStagingMaxAge {
			errs = append(errs, fmt.Errorf("STAGING_MAX_AGE=%q: want a duration of at least %v, e.g. 24h", v, minStagingMaxAge))
		} else {
			c.StagingMaxAge = d
		}
	}
	if v := os.Getenv("IMPORT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case v == "0":
			c.ImportTimeout = 0
		case err != nil || d < minImportTimeout:
			errs = append(errs, fmt.Errorf("IMPORT_TIMEOUT=%q: want 0 (no limit) or a duration of at least %v, e.g. 12h", v, minImportTimeout))
		default:
			c.ImportTimeout = d
		}
	}
	if c.SessionIdleTTL >= c.SessionTTL {
		// Not an error: the absolute lifetime simply always wins.
		c.SessionIdleTTL = 0
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT=%q: want json or text", c.LogFormat))
	}
	switch c.SeedDemo {
	case "", "0", "1", "force":
	default:
		errs = append(errs, fmt.Errorf("SEED_DEMO=%q: want 1 or force", c.SeedDemo))
	}
	providers, err := parseOIDC(os.Getenv)
	if err != nil {
		errs = append(errs, err)
	}
	c.OIDC = providers
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_URL")), "/"); v != "" {
		if u, err := url.Parse(v); err != nil || !isHTTPURL(v) || u.Path != "" || u.RawQuery != "" {
			errs = append(errs, fmt.Errorf("PUBLIC_URL=%q: want scheme://host[:port], e.g. https://archive.example.com", v))
		} else {
			c.PublicURL = v
		}
	}
	errs = append(errs, pinCallbacks(c.OIDC, c.PublicURL)...)
	// The remaining S3 settings (URL shape, required keys) are validated by
	// the blob adapter when it is constructed.
	return c, errors.Join(errs...)
}

// pinCallbacks gives every provider without its own REDIRECT_URL the one
// under PUBLIC_URL. The callback URL goes to the identity provider with the
// login; taken from the request's Host header, anyone could have it point at
// a host of their choice, and an identity provider that accepts loose
// redirect URLs would send the victim's code there.
func pinCallbacks(providers []oidcProviderConfig, publicURL string) []error {
	var errs []error
	for i, p := range providers {
		if p.RedirectURL != "" {
			continue
		}
		if publicURL == "" {
			errs = append(errs, fmt.Errorf("OIDC provider %q needs its callback URL: set PUBLIC_URL (e.g. https://archive.example.com) or %sREDIRECT_URL",
				p.ID, "ARCHIVIST_OIDC_"+strings.ToUpper(strings.ReplaceAll(p.ID, "-", "_"))+"_"))
			continue
		}
		providers[i].RedirectURL = publicURL + "/api/auth/oidc/" + url.PathEscape(p.ID) + "/callback"
	}
	return errs
}

// sampleSecrets are secrets shipped as examples; a server using one has
// credentials anyone who has read the repository knows.
var sampleSecrets = map[string]bool{"change-me-minio-secret": true}

// The import budget's defaults: generous for the largest real exports (a
// Google Takeout with a decade of location history is a few million events),
// small enough that one archive cannot hold the shared queue for days.
const (
	defaultImportTimeout   = 12 * time.Hour
	defaultMaxImportEvents = 25_000_000
	minImportTimeout       = time.Minute
)

// minSessionIdleTTL keeps the idle timeout well above the granularity at which
// session use is recorded (app.SessionTouchInterval): below that an active
// session could expire between two recordings.
const minSessionIdleTTL = 3 * app.SessionTouchInterval

// minStagingMaxAge keeps the janitor away from uploads that are still being
// received: a part in flight is referenced by no job yet, and a multi-GB upload
// over a slow line takes a while.
const minStagingMaxAge = time.Hour

// minMetricsToken keeps guessable scrape tokens out.
const minMetricsToken = 16

// isLoopbackAddr reports whether a listen address is reachable from this host
// only.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
