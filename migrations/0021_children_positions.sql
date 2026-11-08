-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- An event's attachments are addressed by their index
-- (/api/events/{id}/media/{idx}), but media and targets were read back
-- without an ORDER BY: heap order is not guaranteed, so a page could label or
-- serve another attachment of the same event than the one meant. Each row
-- now keeps its position within its event. Rows written before this keep 0
-- and fall back to their id for a stable order; a reprocess of their import
-- writes the positions.
ALTER TABLE media ADD COLUMN position int NOT NULL DEFAULT 0;
ALTER TABLE event_targets ADD COLUMN position int NOT NULL DEFAULT 0;
