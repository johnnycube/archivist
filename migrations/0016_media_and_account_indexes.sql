-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Two foreign keys without an index on the referencing side:
--
--   media.event_id   — every timeline page loads its events' media by
--                      event_id, every import batch replaces media by
--                      event_id, and deleting an event cascades to media by
--                      it. Without the index each of those scans the media
--                      table of ALL users, so one user with a million media
--                      rows slows everybody's timeline.
--   events.account_id — deleting an account (a user's deletion cascades to
--                      their accounts) looks events up by it.
--
-- Built inside the migration's transaction, so writes to these tables wait
-- while it runs (seconds even for millions of rows).
CREATE INDEX IF NOT EXISTS media_event_idx ON media (event_id);
CREATE INDEX IF NOT EXISTS events_account_idx ON events (account_id);
