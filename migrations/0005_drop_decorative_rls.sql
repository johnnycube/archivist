-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- Remove the Row-Level Security that 0001 declared but that never did anything.
--
-- 0001 enabled RLS on events, accounts and event_targets with policies keyed on
-- current_setting('app.user_id'). In this application that is decoration:
--
--   * the app connects as the role that OWNS the tables, and owners bypass RLS
--     unless the table is FORCEd (it is not);
--   * nothing ever sets app.user_id, on any query path.
--
-- So the policies protected nothing, while reading as if they did. Worse, they
-- were a trap: connect as a non-owner role one day (a sensible hardening step)
-- and current_setting('app.user_id', true) is NULL, every policy evaluates to
-- NULL, and every query on these tables silently returns zero rows.
--
-- Tenant isolation is enforced where it always was: every repository query is
-- scoped by user_id, and the shared port contract tests
-- (internal/adapters/porttest) check that on every method of every port.
--
-- A REAL implementation, should defense in depth at the database be wanted:
--
--   1. a dedicated non-owner application role (migrations keep running as the
--      owner), with table privileges granted explicitly;
--   2. ALTER TABLE ... FORCE ROW LEVEL SECURITY, and policies with both USING
--      and WITH CHECK, on EVERY tenant table (import_jobs, sessions, media, ...
--      — 0001 covered only three);
--   3. every query path running inside a transaction that first does
--      SET LOCAL app.user_id (set_config(..., true)) — pooled connections must
--      never carry the setting over — including the paths that legitimately
--      work across tenants (the job queue's Claim/Recover, login by email,
--      session lookup), which need their own role or SECURITY DEFINER functions;
--   4. contract tests that connect as that role and prove a missing or foreign
--      app.user_id sees and writes nothing.
--
-- Idempotent: safe on databases where the policies are already gone.
DROP POLICY IF EXISTS targets_tenant ON event_targets;
DROP POLICY IF EXISTS accounts_tenant ON accounts;
DROP POLICY IF EXISTS events_tenant ON events;

ALTER TABLE event_targets DISABLE ROW LEVEL SECURITY;
ALTER TABLE accounts DISABLE ROW LEVEL SECURITY;
ALTER TABLE events DISABLE ROW LEVEL SECURITY;
