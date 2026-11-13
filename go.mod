module github.com/johnnycube/archivist

go 1.26.0

// npm packages may ship Go sources (flatted does); keep them out of ./...
ignore ./frontend/node_modules

// Dependency policy: prefer a well-known, maintained library over hand-rolled
// protocol or crypto code. Direct dependencies and what they are for:
//   - lib/pq            PostgreSQL driver (database/sql), internal/adapters/postgres
//   - pressly/goose     schema migrations (migrations/*.sql)
//   - minio/minio-go    S3-compatible blob store, internal/adapters/blob
//   - x/crypto/argon2   password hashing, internal/adapters/crypto
//   - x/time/rate       request rate limiting
//   - coreos/go-oidc    OpenID Connect discovery + ID-token verification, internal/adapters/oidc
//   - x/oauth2          the authorization-code flow (PKCE) behind go-oidc
//   - prometheus/client_golang  /metrics: HTTP, import and queue metrics, Go runtime, internal/adapters/metrics
// Dependencies are NOT vendored: go.sum pins them and builds fetch through the
// module proxy/cache. After changing this file run `go mod tidy` (`make tidy`).
//
// Possible future seams: riverqueue/river (Postgres job queue, behind the
// app.Jobs port), meilisearch-go (search), hashicorp/go-plugin (subprocess
// provider isolation).

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/google/uuid v1.6.0
	github.com/lib/pq v1.12.3
	github.com/minio/minio-go/v7 v7.4.1
	github.com/pressly/goose/v3 v3.29.0
	github.com/prometheus/client_golang v1.25.0
	golang.org/x/crypto v0.58.0
	golang.org/x/oauth2 v0.38.0
	golang.org/x/sys v0.50.0
	golang.org/x/time v0.16.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/philhofer/fwd v1.2.1 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/tinylib/msgp v1.6.5 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/ini.v1 v1.67.3 // indirect
)
