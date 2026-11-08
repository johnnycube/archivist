# Immutable ingestion: write-once blob store, archive preserved before parsing

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

Providers improve over time and were mostly developed against synthetic
fixtures. An import that only kept the normalized events could never be
re-run against the original data, and a failed or interrupted import must not
lose the upload.

## Decision

`app.Blobs` is write-once. The worker preserves the uploaded ZIP at
`archives/<job-id>.zip` (`.partN.zip` for further parts) before parsing and
removes staged files only once preserved *and* the outcome is recorded.
Reprocess re-runs the providers over the preserved archive. The shipped
deployment uses an S3-compatible store (MinIO in compose); the filesystem is
the single-node fallback. Both share the object layout
`<user>/<hh>/<sha256(key)>` so `archivist-blobs migrate-to-s3` copies a store
file by file.

## Consequences

- Storage holds every upload in full; delete-import removes events, archives
  and the job row, in that order, and is retryable.
- Media is served from the blob store with `Range` support because both
  readers seek.
- Stored raw records (re-normalizing without re-parsing the ZIP) are not
  needed and were dropped from the schema.
