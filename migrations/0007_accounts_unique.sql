-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- One account per (user, source, label) — enforced, not hoped for.
--
-- Accounts.Ensure used to SELECT, then INSERT, with no unique constraint behind
-- it: two imports of the same connection starting together could each create
-- "the" account. Ensure is now a single INSERT ... ON CONFLICT on the index
-- created here.
--
-- Databases that already contain such duplicates are repaired first, without
-- losing a row: per (user_id, source_id, label) the OLDEST account survives and
-- every row that references one of the others is repointed to it, then the
-- others are deleted. "Every row" is taken from the catalog — each foreign key
-- that references accounts(id) — so it covers events (the only one the
-- application writes), whichever of 0001's unused tables 0006 had to keep
-- (secrets, import_bundles, sync_runs, raw_records, threads) and anything an
-- operator added. Two of those carry a unique key that includes account_id;
-- rows that would collide after the merge are the same logical row on both
-- accounts, so they are merged first (events repointed to the surviving one):
--
--   threads     UNIQUE (account_id, external_id)   <- events.thread_id
--   raw_records UNIQUE (account_id, content_hash)  <- events.raw_record_id
--
-- events' own unique key is (user_id, source_id, external_id) — no account_id
-- in it — so repointing events can never collide.

-- +goose StatementBegin
DO $$
DECLARE
    fk record;
    n  bigint;
BEGIN
    CREATE TEMP TABLE account_merge ON COMMIT DROP AS
    SELECT id AS dup_id, keep_id
    FROM (SELECT id, first_value(id) OVER (PARTITION BY user_id, source_id, label
                                           ORDER BY created_at, id) AS keep_id
          FROM accounts) a
    WHERE id <> keep_id;

    SELECT count(*) INTO n FROM account_merge;
    IF n = 0 THEN
        RETURN;
    END IF;
    RAISE NOTICE 'migration 0007: merging % duplicate account(s) into the oldest of their kind', n;

    IF to_regclass('threads') IS NOT NULL THEN
        CREATE TEMP TABLE thread_merge ON COMMIT DROP AS
        SELECT id AS dup_id, keep_id
        FROM (SELECT t.id, first_value(t.id) OVER (
                         PARTITION BY coalesce(m.keep_id, t.account_id), t.external_id
                         ORDER BY (m.keep_id IS NOT NULL), t.id) AS keep_id
              FROM threads t LEFT JOIN account_merge m ON m.dup_id = t.account_id) x
        WHERE id <> keep_id;
        UPDATE events e SET thread_id = tm.keep_id FROM thread_merge tm WHERE e.thread_id = tm.dup_id;
        DELETE FROM threads t USING thread_merge tm WHERE t.id = tm.dup_id;
    END IF;

    IF to_regclass('raw_records') IS NOT NULL THEN
        CREATE TEMP TABLE raw_merge ON COMMIT DROP AS
        SELECT id AS dup_id, keep_id
        FROM (SELECT r.id, first_value(r.id) OVER (
                         PARTITION BY coalesce(m.keep_id, r.account_id), r.content_hash
                         ORDER BY (m.keep_id IS NOT NULL), r.id) AS keep_id
              FROM raw_records r LEFT JOIN account_merge m ON m.dup_id = r.account_id) x
        WHERE id <> keep_id;
        UPDATE events e SET raw_record_id = rm.keep_id FROM raw_merge rm WHERE e.raw_record_id = rm.dup_id;
        DELETE FROM raw_records r USING raw_merge rm WHERE r.id = rm.dup_id;
    END IF;

    FOR fk IN
        SELECT c.conrelid::regclass AS tbl, a.attname AS col
        FROM pg_constraint c
        JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
        WHERE c.contype = 'f' AND c.confrelid = 'accounts'::regclass
          AND array_length(c.conkey, 1) = 1
    LOOP
        EXECUTE format('UPDATE %s t SET %I = m.keep_id FROM account_merge m WHERE t.%I = m.dup_id',
                       fk.tbl, fk.col, fk.col);
    END LOOP;

    DELETE FROM accounts a USING account_merge m WHERE a.id = m.dup_id;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX accounts_user_source_label_key ON accounts (user_id, source_id, label);
