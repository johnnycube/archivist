-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Runs used to store media under its path ("<source>/<member path>"); since
-- 0023 they store it under its content (ADR 0017). The next completed run of
-- an import from before stores its media again, under content keys, and the
-- copies under paths would stay in the blob store, counted nowhere. Every
-- import that exists now stored by path (one from a development build after
-- 0023 has no such copies; deleting a missing blob does nothing). After a
-- completed run of a flagged import, Jobs.ReleaseLegacyMedia hands back the
-- path keys nothing shows any more, the worker deletes them, and the flag goes.
-- New imports get false.
ALTER TABLE import_jobs ADD COLUMN media_by_path boolean NOT NULL DEFAULT true;
ALTER TABLE import_jobs ALTER COLUMN media_by_path SET DEFAULT false;
