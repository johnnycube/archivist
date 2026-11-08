-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- A filtered timeline page walked the timeline index in date order and
-- discarded every row that did not match: with 1.5 million events, the 500k
-- undated votes (which sort last) took 3.4 s to reach for a reaction filter.
-- Each filter dimension gets an index in timeline order, so a page of one
-- source, one category or one type is read in order and stops after it. They
-- replace the plain (user_id, <dimension>) indexes of 0001, which they cover
-- as a prefix (the facet counts still use them).
CREATE INDEX events_source_order_idx
    ON events (user_id, source_id, (COALESCE(occurred_at, TIMESTAMPTZ '-infinity')) DESC, id DESC);
CREATE INDEX events_category_order_idx
    ON events (user_id, category, (COALESCE(occurred_at, TIMESTAMPTZ '-infinity')) DESC, id DESC);
CREATE INDEX events_type_order_idx
    ON events (user_id, type, (COALESCE(occurred_at, TIMESTAMPTZ '-infinity')) DESC, id DESC);
DROP INDEX IF EXISTS events_source_idx;
DROP INDEX IF EXISTS events_category_idx;
DROP INDEX IF EXISTS events_type_idx;
