-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- A running import's owner may cancel it: the request is this flag, which the
-- worker polls while the job runs (app.Jobs.RequestCancel). Claim, Requeue and
-- Complete clear it; crash recovery fails a running job that has it set
-- instead of requeueing it.
ALTER TABLE import_jobs ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false;
