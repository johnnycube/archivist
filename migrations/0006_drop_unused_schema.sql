-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Drop the parts of 0001 that no code ever read or wrote.
--
-- 0001 created the schema of the whole design up front. These features were
-- never built (ARCHITECTURE.md lists them as not implemented), so their tables
-- and columns have only ever been empty:
--
--   encryption_keys, secrets      credential vault for API connectors
--   import_bundles, sync_runs     ingestion bookkeeping (import_jobs is what is used)
--   raw_records                   raw-payload store   (+ events.raw_record_id)
--   threads                       normalized threads  (+ events.thread_id;
--                                 events.thread_external_id IS used and stays)
--   tags, event_tags              user tagging
--   users.mfa_secret              TOTP
--
-- A feature that gets built re-adds what it needs, shaped by its real
-- requirements, in its own migration.
--
-- NOTHING THAT HOLDS DATA IS DROPPED. An operator may have used these tables by
-- hand; so each table goes only if it is empty, and users.mfa_secret only if it
-- is NULL everywhere — otherwise a NOTICE says what was kept and why, and the
-- migration still succeeds. A table is also kept while another surviving table
-- references it (FK-safe order: children first, no CASCADE). The two events
-- columns are foreign keys into raw_records / threads, so they are provably
-- all-NULL whenever that table is empty, and go with it.
--
-- Idempotent: objects that are already gone are skipped.

-- +goose StatementBegin
DO $$
DECLARE
    t         text;
    has_rows  boolean;
    referrers text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'event_tags', 'tags',
        'secrets', 'encryption_keys',
        'sync_runs',
        'raw_records', 'import_bundles',
        'threads'
    ] LOOP
        IF to_regclass(t) IS NULL THEN
            CONTINUE;
        END IF;
        EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I)', t) INTO has_rows;
        IF has_rows THEN
            RAISE NOTICE 'migration 0006: table % holds data and is kept', t;
            CONTINUE;
        END IF;
        -- The table is empty, so these FK columns into it are all NULL.
        IF t = 'raw_records' THEN
            ALTER TABLE events DROP COLUMN IF EXISTS raw_record_id;
        ELSIF t = 'threads' THEN
            ALTER TABLE events DROP COLUMN IF EXISTS thread_id;
        END IF;
        SELECT string_agg(DISTINCT c.conrelid::regclass::text, ', ') INTO referrers
        FROM pg_constraint c
        WHERE c.contype = 'f' AND c.confrelid = to_regclass(t) AND c.conrelid <> c.confrelid;
        IF referrers IS NOT NULL THEN
            RAISE NOTICE 'migration 0006: table % is empty but still referenced by % (kept above); it is kept too', t, referrers;
            CONTINUE;
        END IF;
        EXECUTE format('DROP TABLE %I', t);
    END LOOP;

    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'mfa_secret') THEN
        EXECUTE 'SELECT EXISTS (SELECT 1 FROM users WHERE mfa_secret IS NOT NULL)' INTO has_rows;
        IF has_rows THEN
            RAISE NOTICE 'migration 0006: users.mfa_secret holds data and is kept';
        ELSE
            ALTER TABLE users DROP COLUMN mfa_secret;
        END IF;
    END IF;
END $$;
-- +goose StatementEnd
