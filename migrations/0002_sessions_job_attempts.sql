-- +goose NO TRANSACTION
-- +goose Up
-- (Migration-runner annotations. This file carries its own BEGIN/COMMIT, so the
-- runner must not wrap it in a second transaction. No Down section: migrations
-- are forward-only.)
--
-- Revocable server-side sessions + import-job crash recovery.

BEGIN;

-- Sessions are stored by token hash: a DB leak does not leak usable tokens.
CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- Claim attempts: jobs orphaned by a crash are requeued a bounded number of
-- times on boot, then failed.
ALTER TABLE import_jobs ADD COLUMN attempts int NOT NULL DEFAULT 0;

COMMIT;
