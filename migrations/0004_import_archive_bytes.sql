-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Size of an import's uploaded archive (all parts), in bytes. The per-user
-- storage quota is the sum of this column over the user's imports. Imports
-- that predate the column count as 0.
ALTER TABLE import_jobs ADD COLUMN archive_bytes bigint NOT NULL DEFAULT 0;
