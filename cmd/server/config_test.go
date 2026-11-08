package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// clearEnv makes the test independent of the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ADDR", "DATA_DIR", "DATABASE_URL", "MIGRATE", "MIGRATIONS_DIR", "LOG_FORMAT",
		"SEED_DEMO", "COOKIE_SECURE", "TRUST_PROXY", "SESSION_TTL", "ALLOW_SIGNUP", "MAX_UPLOAD_MB",
		"MAX_ACTIVE_IMPORTS", "MAX_USER_ARCHIVE_GB", "BLOB_S3_ENDPOINT", "BLOB_S3_BUCKET",
		"BLOB_S3_ACCESS_KEY", "BLOB_S3_SECRET_KEY", "BLOB_S3_REGION", "BLOB_S3_SSE",
		"TRUSTED_PROXIES", "TRUSTED_ORIGINS", "METRICS_TOKEN", "SESSION_IDLE_TTL", "STAGING_MAX_AGE",
		"IMPORT_TIMEOUT", "MAX_IMPORT_EVENTS", "PUBLIC_URL", "ARCHIVIST_OIDC_PROVIDERS",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "memory") // the store must be named; tests use memory
}

func TestConfigDefaults(t *testing.T) {
	clearEnv(t)
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.DataDir != "./data" || !c.CookieSecure || c.Migrate ||
		c.TrustedProxies != 0 || c.TrustedOrigins != nil || c.MetricsToken != "" {
		t.Errorf("defaults = %+v", c)
	}
	if c.AllowSignup || c.MaxActiveImports != 3 || c.MaxArchiveBytes != 50<<30 ||
		c.MaxUploadBytes != 8<<30 || c.SessionTTL != 720*time.Hour {
		t.Errorf("limit defaults = %+v", c)
	}
	if c.ImportTimeout != 12*time.Hour || c.MaxImportEvents != 25_000_000 {
		t.Errorf("import budget defaults = %v, %d", c.ImportTimeout, c.MaxImportEvents)
	}
}

func TestConfigImportBudget(t *testing.T) {
	for _, tc := range []struct {
		timeout, events string
		wantTimeout     time.Duration
		wantEvents      int
		wantErr         string
	}{
		{"30m", "1000", 30 * time.Minute, 1000, ""},
		{"0", "", 0, 25_000_000, ""},
		{"30s", "", 0, 0, "IMPORT_TIMEOUT"},
		{"soon", "", 0, 0, "IMPORT_TIMEOUT"},
		{"", "0", 0, 0, "MAX_IMPORT_EVENTS"},
		{"", "-5", 0, 0, "MAX_IMPORT_EVENTS"},
	} {
		clearEnv(t)
		t.Setenv("IMPORT_TIMEOUT", tc.timeout)
		t.Setenv("MAX_IMPORT_EVENTS", tc.events)
		c, err := loadConfig()
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%q/%q: err = %v, want it to name %s", tc.timeout, tc.events, err, tc.wantErr)
			}
			continue
		}
		if err != nil || c.ImportTimeout != tc.wantTimeout || c.MaxImportEvents != tc.wantEvents {
			t.Errorf("%q/%q: = %v, %d, %v", tc.timeout, tc.events, c.ImportTimeout, c.MaxImportEvents, err)
		}
	}
}

func TestConfigStoreIsNeverChosenByOmission(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("no DATABASE_URL = %v; want it refused by name", err)
	}
	t.Setenv("DATABASE_URL", "memory")
	c, err := loadConfig()
	if err != nil || c.DatabaseURL != "" || !warned(c, "in-memory") {
		t.Errorf("DATABASE_URL=memory = %q, %v, %q; want the memory store with a warning", c.DatabaseURL, err, c.Warnings)
	}
	t.Setenv("DATABASE_URL", "postgres://db/archivist")
	if c, err := loadConfig(); err != nil || c.DatabaseURL != "postgres://db/archivist" {
		t.Errorf("a DSN = %q, %v", c.DatabaseURL, err)
	}
}

func TestConfigRefusesSampleSecrets(t *testing.T) {
	clearEnv(t)
	t.Setenv("BLOB_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("BLOB_S3_SECRET_KEY", "change-me-minio-secret")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "BLOB_S3_SECRET_KEY") {
		t.Errorf("sample secret = %v; want it refused", err)
	}
}

func TestConfigOverridesAndValidation(t *testing.T) {
	clearEnv(t)
	t.Setenv("ALLOW_SIGNUP", "0")
	t.Setenv("MAX_ACTIVE_IMPORTS", "7")
	t.Setenv("MAX_USER_ARCHIVE_GB", "2")
	t.Setenv("SESSION_TTL", "24h")
	t.Setenv("COOKIE_SECURE", "0")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowSignup || c.MaxActiveImports != 7 || c.MaxArchiveBytes != 2<<30 || c.SessionTTL != 24*time.Hour || c.CookieSecure {
		t.Errorf("overrides = %+v", c)
	}

	// Every bad value is reported at once, by name.
	t.Setenv("ALLOW_SIGNUP", "nope")
	t.Setenv("MAX_ACTIVE_IMPORTS", "-1")
	t.Setenv("MAX_UPLOAD_MB", "lots")
	t.Setenv("SESSION_TTL", "soon")
	t.Setenv("LOG_FORMAT", "xml")
	_, err = loadConfig()
	if err == nil {
		t.Fatal("invalid configuration accepted")
	}
	for _, key := range []string{"ALLOW_SIGNUP", "MAX_ACTIVE_IMPORTS", "MAX_UPLOAD_MB", "SESSION_TTL", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestConfigSessionIdleTTL(t *testing.T) {
	for _, tc := range []struct {
		name, ttl, idle string
		want            time.Duration
		wantErr         bool
	}{
		{name: "default is 14 days", want: 336 * time.Hour},
		{name: "override", idle: "48h", want: 48 * time.Hour},
		{name: "0 disables", idle: "0", want: 0},
		{name: "no longer than the session itself: the absolute TTL wins", ttl: "24h", want: 0},
		{name: "below the recording granularity", idle: "1m", wantErr: true},
		{name: "negative", idle: "-1h", wantErr: true},
		{name: "not a duration", idle: "two weeks", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("SESSION_TTL", tc.ttl)
			t.Setenv("SESSION_IDLE_TTL", tc.idle)
			c, err := loadConfig()
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "SESSION_IDLE_TTL") {
					t.Fatalf("err = %v, want one naming SESSION_IDLE_TTL", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.SessionIdleTTL != tc.want {
				t.Errorf("SessionIdleTTL = %v, want %v", c.SessionIdleTTL, tc.want)
			}
		})
	}
}

func warned(c config, substr string) bool {
	return slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, substr) })
}

func TestConfigTrustedProxies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		proxies string
		want    int
	}{
		{"default ignores forwarding headers", "", 0},
		{"explicit count", "2", 2},
		{"explicit zero", "0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("TRUSTED_PROXIES", tc.proxies)
			c, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if c.TrustedProxies != tc.want {
				t.Errorf("TrustedProxies = %d, want %d", c.TrustedProxies, tc.want)
			}
		})
	}
	// The old alias is refused by name, with the replacement in the message,
	// rather than silently ignored.
	for _, legacy := range []string{"1", "0"} {
		clearEnv(t)
		t.Setenv("TRUST_PROXY", legacy)
		if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "TRUST_PROXY was removed") || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
			t.Errorf("TRUST_PROXY=%s: err = %v, want it refused with the replacement named", legacy, err)
		}
	}
	for _, bad := range []string{"-1", "yes", "1.5"} {
		clearEnv(t)
		t.Setenv("TRUSTED_PROXIES", bad)
		if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
			t.Errorf("TRUSTED_PROXIES=%q: err = %v, want it rejected by name", bad, err)
		}
	}
}

func TestConfigTrustedOrigins(t *testing.T) {
	clearEnv(t)
	t.Setenv("TRUSTED_ORIGINS", " https://archive.example.com, http://localhost:5173 ,")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://archive.example.com", "http://localhost:5173"}; !slices.Equal(c.TrustedOrigins, want) {
		t.Errorf("TrustedOrigins = %q, want %q", c.TrustedOrigins, want)
	}
	for _, bad := range []string{"archive.example.com", "https://archive.example.com/app", "https://"} {
		t.Setenv("TRUSTED_ORIGINS", bad)
		if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "TRUSTED_ORIGINS") {
			t.Errorf("TRUSTED_ORIGINS=%q: err = %v, want it rejected by name", bad, err)
		}
	}
}

func TestConfigMetricsToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("METRICS_TOKEN", "0123456789abcdef0123")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.MetricsToken != "0123456789abcdef0123" || warned(c, "/metrics") {
		t.Errorf("token = %q, warnings = %q", c.MetricsToken, c.Warnings)
	}
	for _, bad := range []string{"short", "0123456789 abcdef0123"} {
		t.Setenv("METRICS_TOKEN", bad)
		_, err := loadConfig()
		if err == nil || !strings.Contains(err.Error(), "METRICS_TOKEN") {
			t.Errorf("METRICS_TOKEN=%q: err = %v, want it rejected by name", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), bad) {
			t.Errorf("the error echoes the secret: %v", err)
		}
	}

	// Open metrics are worth a warning unless only this host can reach them.
	for addr, want := range map[string]bool{
		":8080": true, "0.0.0.0:8080": true, "192.168.1.5:8080": true, "[::]:8080": true, "garbage": true,
		"127.0.0.1:8080": false, "localhost:8080": false, "[::1]:8080": false,
	} {
		clearEnv(t)
		t.Setenv("ADDR", addr)
		c, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got := warned(c, "/metrics"); got != want {
			t.Errorf("ADDR=%s: open-metrics warning = %v, want %v", addr, got, want)
		}
	}
}

func TestConfigStagingMaxAge(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        time.Duration
		wantErr     bool
	}{
		{name: "default is a day", want: 24 * time.Hour},
		{name: "override", value: "72h", want: 72 * time.Hour},
		{name: "the minimum", value: "1h", want: time.Hour},
		{name: "minutes", value: "90m", want: 90 * time.Minute},
		{name: "shorter than an upload may take", value: "10m", wantErr: true},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-24h", wantErr: true},
		{name: "a bare number", value: "24", wantErr: true},
		{name: "not a duration", value: "a day", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("STAGING_MAX_AGE", tc.value)
			c, err := loadConfig()
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "STAGING_MAX_AGE") {
					t.Fatalf("err = %v, want one naming STAGING_MAX_AGE", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.StagingMaxAge != tc.want {
				t.Errorf("StagingMaxAge = %v, want %v", c.StagingMaxAge, tc.want)
			}
		})
	}
}

func TestConfigPinsOIDCCallbacks(t *testing.T) {
	clearEnv(t)
	t.Setenv("ARCHIVIST_OIDC_PROVIDERS", "gitea,keycloak")
	t.Setenv("ARCHIVIST_OIDC_GITEA_ISSUER", "https://gitea.example")
	t.Setenv("ARCHIVIST_OIDC_GITEA_CLIENT_ID", "c1")
	t.Setenv("ARCHIVIST_OIDC_KEYCLOAK_ISSUER", "https://kc.example/realms/x")
	t.Setenv("ARCHIVIST_OIDC_KEYCLOAK_CLIENT_ID", "c2")
	t.Setenv("ARCHIVIST_OIDC_KEYCLOAK_REDIRECT_URL", "https://other.example/api/auth/oidc/keycloak/callback")
	t.Setenv("PUBLIC_URL", "")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "PUBLIC_URL") || !strings.Contains(err.Error(), `"gitea"`) {
		t.Errorf("a provider without a callback URL = %v; want it refused, naming PUBLIC_URL and the provider", err)
	}
	t.Setenv("PUBLIC_URL", "https://archive.example/")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.OIDC[0].RedirectURL; got != "https://archive.example/api/auth/oidc/gitea/callback" {
		t.Errorf("derived callback = %q", got)
	}
	if got := c.OIDC[1].RedirectURL; got != "https://other.example/api/auth/oidc/keycloak/callback" {
		t.Errorf("a provider's own REDIRECT_URL was replaced: %q", got)
	}
	for _, bad := range []string{"archive.example", "https://archive.example/sub", "ftp://x", "https://a.example/?q=1"} {
		t.Setenv("PUBLIC_URL", bad)
		if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "PUBLIC_URL") {
			t.Errorf("PUBLIC_URL=%q: err = %v", bad, err)
		}
	}
}
