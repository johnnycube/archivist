-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- The file name the client gave each uploaded archive part, index-aligned with
-- zip_paths ('' = none). Some exports carry information only there (WhatsApp on
-- iOS: the chat's name is nowhere but in the ZIP's file name). Sanitised by the
-- application (base name, no control characters, <= 255 bytes). Imports that
-- predate the column have no names.
ALTER TABLE import_jobs ADD COLUMN part_names jsonb NOT NULL DEFAULT '[]';
