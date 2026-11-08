# Developing Archivist

How the code is laid out, the conventions it follows and how it is tested.
The *why* behind the design is in [`adr/`](./adr/); the design as a whole and
what of it is built in [`../ARCHITECTURE.md`](../ARCHITECTURE.md). The code is
the source of truth: where this file disagrees with it, fix this file.

## Architecture: hexagonal (ports and adapters)

The dependency rule points inward — **domain ← application ← adapters**.
Nothing in `domain` or `app` imports a framework, a database, a transport or
`os`.

```
internal/domain/      entities, no dependencies: Event (+ Capped size bounds), User,
                        Coverage, ImportJob, Descriptor (what a source is, its maturity)
internal/provider/    provider SPI (imports domain only): Provider, BundleProvider,
                        Extractor, RawRecord, optional AttachmentMatcher, Lookup
internal/app/         APPLICATION LAYER
  ports.go              driven ports: EventRepository, Users, Accounts, Jobs,
                          PasswordHasher, Sessions, AttemptLimiter, Blobs, Staging,
                          ProviderCatalog, ArchiveEngine, ImportObserver; ErrNotFound
  auth.go oidc.go       AuthService (passwords, sessions, throttles, OIDC login/linking)
  import.go uploads.go  ImportService (admission, upload gate, quotas, reprocess,
                          delete, cancel) and the Worker (budget, ledger, sweep)
  timeline.go query.go  TimelineService, page size, keyset cursors
  admin.go app.go       AdminService (the operator's commands), App + Deps
internal/adapters/    implementations of the ports
  memory/               in-memory adapters; NewStore wires them (cascading delete)
  postgres/             PostgreSQL repositories, jobs, sessions, attempts (lib/pq) + goose
  crypto/               Argon2id password hasher
  blob/                 write-once blob store: filesystem, S3 (minio-go)
  oidc/                 app.OIDCProvider on go-oidc + x/oauth2
  staging/              uploaded archive parts on disk (os.Root), free-disk check
  catalog/              provider catalog (imports the provider packages)
  engine/               app.ArchiveEngine over internal/bundle
  metrics/              Prometheus registry: HTTP, import and queue metrics
  rest/                 DRIVING adapter: net/http, DTOs, CSP, media, rate limits, SPA
  porttest/             shared port contract suites (see Testing)
internal/bundle/      the archive engine: ZIP pre-check, routing, CSV/JSON/JS
                        readers with bounds, dedupe, Limits, skipped arrays
internal/webui/       embeds the built SvelteKit app (dist/ via //go:embed)
internal/{seed,archivetest}/   demo data (SEED_DEMO); test ZIP helper
providers/<name>/     one package per source (+ testdata + golden test)
providers/internal/   shared: meta (Facebook + Instagram), jsonwalk, jsonpreview
migrations/           PostgreSQL schema, goose format, forward-only
cmd/server/           composition root: main.go (wire, worker, janitor, shutdown),
                        admin.go (`archivist admin …`); config.go is the ONLY os.Getenv caller
cmd/archivist-demo/   CLI: import a ZIP, print the coverage report
cmd/archivist-blobs/  CLI: migrate-to-s3 copies a filesystem blob store into S3
frontend/             SvelteKit 3 / Svelte 5 / Vite 8, adapter-static → ../internal/webui/dist
deploy/               deploy.sh, compose.registry.yml (published image), compose.s3.yml (S3 store)
```

Only the `cmd/*/main.go` files know concrete implementations. Adapters assert
conformance with `var _ app.Port = (*Impl)(nil)`. `cmd/server` shares one
provider catalog between the application and the engine.

## Commands

Tool versions are pinned in `mise.toml` (Go, Node, golangci-lint, gitleaks,
fleetlint, …); `make tools` runs `mise install` and `npm ci`, and CI installs
the same file. Git hooks run through [prek](https://github.com/j178/prek)
(`prek install` once): hygiene, gitleaks, fleetlint, golangci-lint on changed
lines, prettier/eslint on changed frontend files, Conventional Commits
messages, no commits on `main`, `make check` before a push.

```
make tools        # mise install + npm ci
make web          # build the UI into internal/webui/dist (required before go build)
make build        # web + go build ./...
make run          # server on the in-memory store, demo login demo/demo, signup open, plain HTTP
make run-pg       # server against $DATABASE_URL (MIGRATE=1) and the dev S3 store
make test         # go test -race -shuffle=on ./... + vitest (PostgreSQL and S3 tests skip)
make test-pg      # everything, with DATABASE_URL and the S3 test settings (make dev-db dev-s3)
make test-s3      # the S3 integration tests only
make fmt          # gofumpt + goimports, prettier
make lint         # golangci-lint (ratcheted), prettier, eslint, actionlint, zizmor,
                  #   shellcheck, toolchain drift (scripts/check-toolchain.sh)
make cover        # coverage floors (.testcoverage.yml, frontend/vitest.config.js)
make audit        # govulncheck, go mod tidy -diff, go-licenses, gitleaks, npm audit, knip
make check        # lint test cover audit build + fleetlint — what CI runs
make mutate       # gremlins mutation testing of internal/app
make export-docs  # regenerate docs/exports.md from frontend/src/lib/exportGuides.js
make icons        # render the icon set from frontend/static/favicon.svg
make docker       # docker compose up --build
make dev-db       # throwaway PostgreSQL on 127.0.0.1:5432 (podman or docker)
make dev-s3       # throwaway S3 store (SILO) on 127.0.0.1:9000
make demo SRC=reddit ZIP=export.zip   # CLI import + coverage report
```

**Build order:** `internal/webui` embeds `dist/`, so it must exist before
`go build`. A prebuilt `dist/` is committed so a fresh clone builds; after
changing anything in `frontend/` run `make web` and commit the result. The
build is reproducible, so an unchanged UI gives no diff (CI checks it).

## Conventions

- **Dependencies**: do not hand-roll what the standard library or a
  well-known, maintained library does (check its health first); do not vendor
  (`go.sum` pins, `make tidy`). `go.mod` says what each direct dependency is for.
- **Add a provider**: create `providers/<name>/` implementing
  `provider.BundleProvider` (`Descriptor`, `Capabilities`, `Extractors`,
  `Normalize`; optionally `AttachmentMatcher`). Reuse `bundle.ReadCSV`,
  `StreamJSONArray`, `StreamJSONArraysReport`, `StreamJSVarArray`,
  `FixMetaMojibake`. Extractors must **stream** (never `io.ReadAll` a member —
  real exports are hundreds of MB) and pass `bundle.ReportSkipped(member, emit)`
  as the `skipped` callback. Register it in `internal/adapters/catalog`. Add
  `testdata/` and a golden test. Unknown files are captured as
  `<source>.unknown` automatically — never drop data. Update
  `docs/platforms.md` and `frontend/src/lib/exportGuides.js`. A new provider
  is `experimental` until it has met a real export.
- **ExternalIDs** are user data identities: deterministic per export record,
  unique within the account. Changing how one is derived is a user-visible
  change; say so in the commit body so the release notes can ask users to
  reprocess ([ADR 0016](./adr/0016-reprocess-replaces-and-external-id-stability.md)).
- **Add a use case**: put it in `internal/app`, depending only on ports; wire
  new ports in `app.Deps`/`app.New`. Take time from `Deps.Clock`, log through
  `Deps.Log`. Ports report absence as `(…, false, nil)`; use cases translate
  to `app.ErrNotFound`.
- **Add a driven port**: define the interface *and its contract* in
  `app/ports.go`, implement it in `adapters/memory` and `adapters/postgres`,
  and add a suite to `adapters/porttest` that both run. There is no row-level
  security: every method touching user data takes a `userID` and must scope
  by it; the suites check that.
- **REST**: never serialize a domain struct — map to a DTO in `rest/dto.go`
  (golden JSON in `rest/testdata/dto`; no `user_id`/`account_id` on the wire).
  Malformed query parameters are a 400, not ignored.
- **Persistence**: events upsert on `(user_id, account_id, source_id,
  external_id)`. Imports write through `EventRepository.SaveBatch` (500 events
  or ~16 MiB per batch) stamped with the import job and its run; after a
  completed run, `SweepImport` removes the import's events that run did not
  write. Media is stored under its content hash (`app.MediaKey`); each
  import's ledger maps the member paths events use to those keys
  ([ADR 0017](./adr/0017-content-addressed-media.md)). After a completed run,
  an import from before that (flagged by 0025) releases its path-keyed copies
  (`Jobs.ReleaseLegacyMedia`). Timeline pages are keyset-paginated `(occurred_at desc, id desc)`,
  undated last; cursors are encoded in `app/query.go`, not by the stores.
- **Time zones**: instants are stored in UTC, but a *day* is the viewer's.
  `app.Query.Loc` carries the zone inward (`?tz=`); `app.LocalDay` is the one
  definition of "which day". `Loc` must be an IANA location.
- **Lint ratchets**: golangci-lint reports only lines changed since
  `issues.new-from-rev` in `.golangci.yml`; ESLint's older violations are frozen
  in `frontend/eslint-suppressions.json`. A line you touch must pass; do not
  widen exclusions or lower a coverage floor. Semgrep false positives get a
  `nosemgrep: <rule> -- <reason>` comment.
- **Migrations**: goose SQL files (`-- +goose Up`), forward-only, no Down.
  0001–0003 carry their own `BEGIN/COMMIT` (`-- +goose NO TRANSACTION`); new
  files have neither (goose wraps them). Add a numbered file; never edit an
  applied one. A fresh install needs no extension: a migration may only ask
  for an optional one, in a block that catches the refusal (0024; a test
  checks it, another runs the chain as a role without the privilege).
- **Commits**: Conventional Commits (`feat:`, `fix:`, …; `!` and a
  `BREAKING CHANGE:` footer for breaking ones); `git-cliff` builds
  `CHANGELOG.md` from them. Design decisions get a record in `docs/adr/`.

## Testing

- `go test ./...` runs everything that needs no services: provider golden
  tests, use-case tests, REST end-to-end tests, the memory adapters' contract
  suites. `internal/adapters/porttest` is the ports' executable contract;
  PostgreSQL adapters run it when `DATABASE_URL` is set (they skip otherwise):
  `make dev-db && make test-pg`. **The tests drop and recreate the `public`
  schema — never point them at a real database.**
- `BLOB_S3_TEST_ENDPOINT` + `BLOB_S3_TEST_ACCESS_KEY` / `_SECRET_KEY` enable
  the tests against a real S3 store (`make dev-s3 && make test-s3`); an
  in-process fake covers the rest.
- The frontend has Vitest unit tests (`frontend/src/lib/*.test.js`) with
  coverage floors on the tested modules.
- Known memory-vs-PostgreSQL differences: search tokenization (memory returns a
  superset), timestamp precision (PostgreSQL: microseconds).
