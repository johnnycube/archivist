-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Shared login throttle (app.AttemptLimiter): one row per FAILED attempt, in a
-- sliding window. key is a SHA-256 made by the application — of the normalised
-- email, or of email + client address — so neither is stored here, and rows
-- exist for unknown emails just as for known ones (no foreign key to users, on
-- purpose: that would be an account-existence oracle). Rows are short-lived:
-- every write prunes what is older than the retention (app.AttemptRetention).
CREATE TABLE auth_failures (
    key       text        NOT NULL,
    failed_at timestamptz NOT NULL
);
CREATE INDEX auth_failures_key_idx ON auth_failures (key, failed_at);
CREATE INDEX auth_failures_age_idx ON auth_failures (failed_at);
