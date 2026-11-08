-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Full-text search used one GIN index over every user's events: a search for
-- a common word read the postings of all users before the user_id condition
-- discarded them, so its cost grew with everybody's archives. With btree_gin
-- (a contrib extension, trusted since PostgreSQL 13) one GIN index can lead
-- with user_id, and a search reads the searching user's postings only.
--
-- The extension is optional. A role that may not create it (no CREATE on the
-- database, common on managed PostgreSQL) keeps the global index. Search
-- returns the same results with it, only more slowly on large multi-user
-- installs. An operator who gets the extension later builds the new index by
-- hand (ADR 0018).
-- +goose StatementBegin
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS btree_gin;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'btree_gin is not available (%); search keeps its index over all users', SQLERRM;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'btree_gin') THEN
        CREATE INDEX IF NOT EXISTS events_user_fts_idx ON events USING gin (user_id, search_vector);
        DROP INDEX IF EXISTS events_fts_idx;
    END IF;
END $$;
-- +goose StatementEnd
