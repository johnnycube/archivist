-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- The ledger of blobs (extracted media) each import stored. Before it, media
-- counted against no quota and outlived the import that brought it, so
-- upload, delete, re-upload filled the blob store without bound. Now the
-- storage quota counts each distinct key of a user once, and deleting an
-- import removes the blobs no other import of the user recorded.
--
-- Media stored before this migration has no entries: it stays uncounted and
-- is kept on delete, as before, until a reprocess of its import records it.
CREATE TABLE import_job_blobs (
    job_id  uuid   NOT NULL REFERENCES import_jobs(id) ON DELETE CASCADE,
    user_id uuid   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key     text   NOT NULL,
    bytes   bigint NOT NULL,
    PRIMARY KEY (job_id, key)
);
CREATE INDEX import_job_blobs_user_key_idx ON import_job_blobs (user_id, key);
