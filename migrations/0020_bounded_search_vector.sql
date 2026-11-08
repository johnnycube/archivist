-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- PostgreSQL refuses a tsvector over 1 MiB ("string is too long for
-- tsvector"), and the vector of text full of distinct words is ~1.25 times
-- the text's size: one long message of unique tokens (a pasted log, a base64
-- blob) failed its whole import batch. The vector is now built from at most
-- the first 4,000 characters of the title and 150,000 of the body — at four
-- bytes per character still well under the limit. Search finds words in
-- that part; the event itself keeps its full text.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION events_tsv() RETURNS trigger AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('simple', left(coalesce(NEW.title, ''), 4000)), 'A') ||
        setweight(to_tsvector('simple', left(coalesce(NEW.body, ''), 150000)), 'B');
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd
