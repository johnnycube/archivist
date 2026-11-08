# Architecture decision records

One file per decision that shaped the code, in [MADR](https://adr.github.io/madr/)
form: context, decision, consequences. Numbers never change; a superseded
record says which one replaced it. `ARCHITECTURE.md` describes the design as a
whole and what of it is built; the records here say *why* the choices were made.

| ADR | Decision |
|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions |
| [0002](0002-hexagonal-architecture.md) | Hexagonal architecture with the dependency rule pointing inward |
| [0003](0003-one-canonical-event.md) | One canonical Event; unknown data is captured, never dropped |
| [0004](0004-export-bundles-streamed-with-limits.md) | Import export bundles only, streamed under hard limits |
| [0005](0005-immutable-ingestion.md) | Immutable ingestion: write-once blob store, archive preserved before parsing |
| [0006](0006-own-job-queue-in-postgres.md) | Own import queue in PostgreSQL instead of River |
| [0007](0007-postgres-full-text-search.md) | PostgreSQL full-text search instead of Meilisearch |
| [0008](0008-keyset-pagination-and-viewer-local-days.md) | Keyset pagination; a "day" is the viewer's |
| [0009](0009-passwords-sessions-and-login-throttle.md) | Argon2id passwords, opaque hashed sessions, throttle in the shared store |
| [0010](0010-forward-only-migrations.md) | Forward-only goose migrations |
| [0011](0011-embedded-reproducible-ui-build.md) | The SvelteKit UI is embedded in the binary from a committed, reproducible build |
| [0012](0012-repository-policy-with-fleetlint.md) | Repository policy enforced by fleetlint at tier 1 |
| [0013](0013-oidc-login-behind-a-provider-port.md) | OpenID Connect login behind a provider port, identities linked to users |
| [0014](0014-oidc-email-linking-needs-a-verified-account.md) | Linking a provider login by email needs a verified account email |
| [0015](0015-an-event-belongs-to-one-account.md) | An event belongs to one account: the upsert key includes the account |
| [0016](0016-reprocess-replaces-and-external-id-stability.md) | A reprocess replaces an import's events; ExternalIDs are stable per provider version |
| [0017](0017-content-addressed-media.md) | Media is stored under its content's hash; each import maps its paths to it |
| [0018](0018-per-user-search-index-with-optional-btree-gin.md) | Search reads one user's postings where btree_gin is available |
