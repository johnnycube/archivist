-- +goose NO TRANSACTION
-- +goose Up
-- (Migration-runner annotations. This file carries its own BEGIN/COMMIT, so the
-- runner must not wrap it in a second transaction. No Down section: migrations
-- are forward-only.)
--
-- Multi-part imports, live progress, and event → import provenance.

BEGIN;

-- An import is one or more ZIP parts (large exports are split by the platform).
ALTER TABLE import_jobs ADD COLUMN zip_paths jsonb NOT NULL DEFAULT '[]';
UPDATE import_jobs SET zip_paths = jsonb_build_array(zip_path) WHERE zip_path <> '';
ALTER TABLE import_jobs DROP COLUMN zip_path;

-- Progress of a running job, in uncompressed archive bytes.
ALTER TABLE import_jobs
    ADD COLUMN bytes_done  bigint NOT NULL DEFAULT 0,
    ADD COLUMN bytes_total bigint NOT NULL DEFAULT 0;

-- The import that last wrote each event. Deleting an import removes the events
-- it still owns; events refreshed by a later import belong to that one.
ALTER TABLE events
    ADD COLUMN import_job_id uuid REFERENCES import_jobs(id) ON DELETE SET NULL;
CREATE INDEX events_import_job_idx ON events (import_job_id) WHERE import_job_id IS NOT NULL;

COMMIT;
