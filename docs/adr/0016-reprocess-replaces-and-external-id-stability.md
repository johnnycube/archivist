# A reprocess replaces an import's events; ExternalIDs are stable per provider version

- Status: accepted
- Date: 2026-11-07

## Context

Reprocess is how a provider improvement reaches data imported earlier: the
preserved archive is parsed again by the current providers and its events
upserted on `(user, account, source, external_id)` (ADR 0015). Upserting only
adds and refreshes. Whatever the new run no longer derives stayed: when a
member that an older provider captured as `<source>.unknown` gets a mapper,
the timeline showed the old generic event next to the new typed ones. And an
`external_id` is the identity of user data: a provider that changes how it
derives one creates a second copy of every affected event in every archive
already imported.

## Decision

- Every run of an import has a run id; `SaveBatch` stamps each event it
  writes with it (`events.import_run`, migration 0019).
- After a run that **completed** — no error, not cancelled, not stopped by
  the import budget — the import's events that this run did not write are
  removed (`EventRepository.SweepImport`). Events a later import has taken
  over belong to that import and are not touched. The coverage report counts
  them (`superseded`).
- `external_id` policy for providers:
  - It is a deterministic function of the export record (never of time,
    order of processing or randomness), unique within the account. The
    golden tests pin it.
  - Changing how a provider derives it is allowed, but it is a
    user-visible change: the release notes name the provider and tell users
    to **reprocess** its imports, which writes the new ids and sweeps the old
    ones. An import that is not reprocessed keeps the old ids.

## Consequences

- Reprocessing is idempotent and convergent: an archive's events after a
  reprocess are exactly what the current providers derive from it.
- A failed or cancelled reprocess removes nothing; the next successful one
  does.
- Events from before migration 0019 carry no run id; the first reprocess of
  their import treats them as not written by it, which is what it should do.
