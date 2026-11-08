-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Idle timeout for sessions: when the session was last used. The application
-- writes it at most once per 5 minutes per session (app.SessionTouchInterval)
-- and always supplies the value from its own clock; the default only serves
-- the rows that exist right now, which count as used at migration time — an
-- upgrade must not log everybody out.
ALTER TABLE sessions ADD COLUMN last_seen_at timestamptz NOT NULL DEFAULT now();
