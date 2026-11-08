# Own import queue in PostgreSQL instead of River

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

Imports are asynchronous and can run for minutes. The design proposed River
as the job queue. The queue has one job type, must enforce per-user quotas
atomically with enqueueing, must be fair between users, and must also work
against the in-memory store used for development.

## Decision

The queue is the `app.Jobs` port with a PostgreSQL implementation
(`FOR UPDATE SKIP LOCKED`, round-robin claiming per user, quotas enforced in
the same transaction as the insert) and an in-memory one. `Jobs.Recover`
requeues jobs orphaned in `running` on boot and gives up after three
attempts. River is not used.

## Consequences

- No extra dependency or schema outside the migrations; the contract suite
  covers both implementations.
- Features River would give for free (priorities, many job kinds, a UI) are
  not available and would have to be built if needed.
