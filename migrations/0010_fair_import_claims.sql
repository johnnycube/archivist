-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Fair (round-robin) import scheduling. Jobs.Claim prefers the user who was
-- served least recently, so one user's backlog cannot starve everybody else.
-- "Recently" is a position in a sequence, not a timestamp: it is strictly
-- monotonic, also across concurrent workers and clock steps. NULL = never
-- served, which sorts first. It lives on users, not on import_jobs, so that
-- deleting finished imports does not buy a user a better place in the queue.
CREATE SEQUENCE import_claim_seq;
ALTER TABLE users ADD COLUMN last_import_claim bigint;
