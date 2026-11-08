# Import export bundles only, streamed under hard limits

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

The design foresaw two ingestion paths, live API/OAuth pulls and data-export
bundles. Vendors rate-limit, deprecate and price their APIs; every vendor
must hand out a data export on request, and that export is the complete
history. Real exports are hundreds of megabytes to gigabytes, and a ZIP is
untrusted input.

## Decision

Only the bundle path is implemented. Extractors stream every member (never
`io.ReadAll`); the engine enforces `bundle.Limits` (entries and central
directory size checked before `archive/zip` opens a part, total and
per-member bytes, record size, nesting depth, dedupe set size); parts are read
as `io.ReaderAt`, never extracted to disk; a provider panic fails only that
import.

## Consequences

- No vendor credentials are stored, so there is no secrets service yet.
- Live pulls, webhooks and scheduling remain open in the roadmap; the
  provider SPI leaves room for them.
- Resource limits are in-process; a sandboxed parser process is a possible
  later hardening.
