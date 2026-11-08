# Search reads one user's postings where btree_gin is available

- Status: accepted
- Date: 2026-11-07

## Context

Full-text search (ADR 0007) had one GIN index on `search_vector` over every
user's events. A search found all events that contain the words, for every
user, and only then dropped those of other users. A word that is rare in
your own archive but common in someone else's therefore cost as much as
their matches. On a test with 300,000 matching events of another user and
10 of one's own, counting one's own matches took 44 ms. A plain GIN index
cannot lead with `user_id`. The contrib extension `btree_gin` adds GIN
operator classes for scalar types, so one index can cover
`(user_id, search_vector)`.

Creating an extension needs CREATE on the database. Roles on managed
PostgreSQL often lack it, and since the first public release a fresh install needs no extension
(ADR 0010, amendment).

## Decision

- Migration 0024 tries `CREATE EXTENSION IF NOT EXISTS btree_gin` in a block
  that catches the refusal. If the extension exists afterwards, it builds
  `events_user_fts_idx ON events USING gin (user_id, search_vector)` and drops
  the global `events_fts_idx`. Otherwise nothing changes.
- The queries stay the same. Both indexes serve the same
  `user_id = $1 AND search_vector @@ …` condition, and the planner picks
  whichever exists.
- A migration may ask only for an optional extension, and only inside a block
  that catches the refusal. A static test checks every migration for that, and
  another runs the whole chain as a role without extension privileges.
- An operator who gets the extension later builds the index by hand, without
  blocking writes:

  ```sql
  CREATE EXTENSION btree_gin;   -- needs CREATE on the database
  CREATE INDEX CONCURRENTLY events_user_fts_idx ON events USING gin (user_id, search_vector);
  DROP INDEX CONCURRENTLY events_fts_idx;
  ```

## Consequences

- Where the extension is available, a search reads only the searching user's
  postings. In the test above, counting dropped from 44 ms to 8 ms. Common
  words are unaffected, because there the planner walks the timeline index.
- Installs differ in one index, depending on the role's privileges. Search
  returns the same results either way.
- Upgrading builds the new index inside the migration, which blocks writes to
  `events` while it runs. On a large archive the upgrade takes noticeably
  longer (docs/upgrading.md).
