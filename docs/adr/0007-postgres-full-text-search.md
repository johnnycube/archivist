# PostgreSQL full-text search instead of Meilisearch

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

The design planned Meilisearch (or Typesense) for search, with PostgreSQL
full-text search as the zero-dependency fallback. A self-hosted app with one
operator gains little from a second stateful service to run, back up and keep
in sync.

## Decision

Search is PostgreSQL full-text search with the `simple` configuration,
filtered by the same source, category, type and date predicates as the
timeline. The in-memory store tokenizes more loosely (a superset of the
`simple` parser), which the contract tests allow for.

## Consequences

- One stateful service to operate; search is transactional with the data.
- Typo tolerance, ranking tuning and facets on participants or tags are not
  available; a dedicated engine remains an option behind the
  `EventRepository` port.
