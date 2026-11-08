-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- An event is the same event only within one account (one connection: a
-- user's source under a label). The upsert key was (user_id, source_id,
-- external_id), so two accounts of the same source whose exports share an id
-- — two Reddit accounts that voted on the same post (`vote:<post id>`), or
-- the same file name captured as `<source>.unknown` — overwrote each other:
-- the second import took the event over, and deleting it deleted the first
-- account's event too. See ADR 0015.
--
-- user_id stays in the key so an upsert can only ever meet rows of the user
-- it writes for. Existing rows are unique under the new key already.
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_user_id_source_id_external_id_key;
ALTER TABLE events ADD CONSTRAINT events_upsert_key UNIQUE (user_id, account_id, source_id, external_id);
