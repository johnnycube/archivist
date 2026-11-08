# Hexagonal architecture with the dependency rule pointing inward

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

The application has several storage backends (PostgreSQL, an in-memory store
for development), two blob stores (S3, filesystem), two driving adapters (REST
server, CLI) and fourteen providers that parse vendor exports. Use cases such
as import, timeline and auth must not depend on any of them.

## Decision

Ports and adapters: `domain` ← `app` ← `adapters`. Nothing in `internal/domain`
or `internal/app` imports a framework, database driver, transport or `os`. The
application layer defines the driven ports (`app/ports.go`) and their
contracts; `internal/adapters/*` implement them; only the three `cmd/*/main.go`
composition roots know concrete types. The provider SPI lives in
`internal/provider` and imports the domain only.

## Consequences

- Every port has a memory and a PostgreSQL implementation that run the same
  contract suite (`internal/adapters/porttest`), including tenant scoping.
- Use cases take time from `Deps.Clock` and log through `Deps.Log`, so they
  are testable without wall-clock or I/O.
- The REST layer maps domain structs to DTOs; a domain type is never
  serialized directly.
