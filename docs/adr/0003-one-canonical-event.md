# One canonical Event; unknown data is captured, never dropped

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

Fourteen sources export dozens of data types in different shapes. A per-type
schema would make the timeline, filters and search a union of special cases,
and anything a provider does not yet understand would be lost on import.

## Decision

Every data type normalizes into one `Event` with a namespaced `Type`
(`reddit.comment`, `spotify.play`, ...), a category, targets and media. A
member of an archive that no extractor claims is still imported as a
`<source>.unknown` event, and arrays inside mapped members that are not mapped
are reported as skipped. `Event.ExternalID` must be stable across imports so
events upsert on `(user_id, source_id, external_id)`.

## Consequences

- The timeline, histogram, filters and search are written once for all
  sources.
- Nothing in an archive is silently dropped; the coverage report says what was
  mapped and what was captured generically.
- A provider that later learns a type re-maps the data by reprocessing the
  preserved archive (ADR 0005).
