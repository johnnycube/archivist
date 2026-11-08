# Media is stored under its content's hash

- Status: accepted
- Date: 2026-11-07

## Context

Blobs are write-once (ADR 0005), and media was stored under
`<source>/<path inside the export>`. Paths are not unique across exports:
WhatsApp names photos `IMG-<date>-WA0001.jpg` on every phone, and two exports
of different chats, phones or years contain different files of the same name.
The first one stored won; every later event with that path showed it.

## Decision

- The engine reads a media member twice: once to hash it (SHA-256), once to
  store it. The worker stores it under `<source>/sha256/<hex>`
  (`app.MediaKey`). Identical files share one blob, whichever exports they
  came in.
- Events keep naming media by path. Each import's ledger (`import_job_blobs`,
  migration 0023) maps `<source>/<path>` to the content key; its primary key is
  `(job_id, path)`, so one export may hold the same file under two names.
- Serving resolves an event's path through the ledger of the import that last
  wrote the event, else of the user's most recent import that recorded the
  path, else the path itself (media stored before this change, seeded media).
- Deleting an import deletes the content keys no other import of the user
  recorded; the storage quota counts each key once.
- Media stored before this change keeps working under its path until its
  import is reprocessed. A reprocess stores the media again, under content
  keys. Migration 0025 flags every import that existed before this change
  (`import_jobs.media_by_path`). After a completed run of a flagged import,
  `Jobs.ReleaseLegacyMedia` returns the paths in its ledger that no event
  still resolves to by path, the worker deletes those path-keyed blobs, and
  the flag is cleared. A path is still in use while an event of an import
  without a ledger entry for that path names it, or while a ledger entry
  uses the path as its key (an entry recorded before migration 0023).

## Consequences

- An export can never show another export's file, and duplicates are stored
  once.
- Every media member is decompressed twice during an import.
- Media is reachable only through the ledger: a blob store copied without the
  database is a set of anonymous hashes.
- A path-keyed copy goes once every import that shows it has been
  reprocessed. If an import from before is deleted without being reprocessed
  first, its path-keyed copies stay in the blob store.
