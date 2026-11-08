-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- The timeline sorts and pages on COALESCE(occurred_at, '-infinity') DESC, id
-- DESC — undated events last — and the keyset cursor is a row comparison on
-- that same pair. events_timeline_idx (user_id, occurred_at DESC, id DESC)
-- does not match either expression, so every page was a scan of all of the
-- user's rows plus a top-N sort. The collapsed timeline reads a window of
-- 2000 rows per page (app.CollapseWindow), which made that visible; this
-- expression index turns both into one index range scan of exactly the rows
-- a page needs. events_timeline_idx stays: it still serves the from/to range
-- filters on occurred_at.
CREATE INDEX events_timeline_order_idx
    ON events (user_id, (COALESCE(occurred_at, TIMESTAMPTZ '-infinity')) DESC, id DESC);
