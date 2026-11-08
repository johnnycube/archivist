-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Media is stored under the SHA-256 of its content (<source>/sha256/<hex>)
-- instead of its path inside the export: blobs are write-once, so two
-- different exports with a file of the same name (two phones' WhatsApp
-- exports, say) shared whichever was stored first. Events still refer to media
-- by path; each import's ledger now maps its paths to their content keys, and
-- serving resolves a path through the ledger of the event's import (ADR 0017).
--
-- Entries recorded before content addressing have key = path, which is what
-- the new column takes; a reprocess moves them to content keys. The primary
-- key becomes (job_id, path): one export can hold the same file under two
-- names, which is one blob and two paths.
ALTER TABLE import_job_blobs ADD COLUMN path text;
UPDATE import_job_blobs SET path = key;
ALTER TABLE import_job_blobs ALTER COLUMN path SET NOT NULL;
ALTER TABLE import_job_blobs DROP CONSTRAINT import_job_blobs_pkey;
ALTER TABLE import_job_blobs ADD PRIMARY KEY (job_id, path);
CREATE INDEX import_job_blobs_user_path_idx ON import_job_blobs (user_id, path);
