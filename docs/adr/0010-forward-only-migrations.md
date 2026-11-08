# Forward-only goose migrations

- Status: accepted; amended 2026-11-07 (see the end)
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

Schema changes must be applied in order by an operator who runs the
container with `MIGRATE=1`, and the project does not have the testing budget
to keep reversible migrations correct.

## Decision

Migrations are goose SQL files, numbered, forward-only, with no `Down`
sections. A new change is a new numbered file; an applied file is never
edited, not even its comments. Files 0001-0003 carry their own transactions
(`NO TRANSACTION`); later files let goose wrap them.

## Amendment (2026-11-07): one edit of 0001

0001 created the `pgcrypto` extension for `gen_random_uuid()`, which
PostgreSQL has built in since version 13 (the project requires 16). Creating
an extension needs CREATE on the database, which roles on managed PostgreSQL
often lack, so a fresh install as such a role failed in its first migration.
The first public release removes that one statement from 0001. This is safe only because:
goose records versions, not checksums; a database that ran the old 0001 keeps
the extension, which nothing uses; and no migration depends on pgcrypto. A
test runs the whole chain as a role without extension privileges, and a
static check refuses a `CREATE EXTENSION` outside a block that tolerates its
refusal. The rule stands for every other change.

## Consequences

- Rolling back means a new migration that undoes the change, or restoring a
  backup.
- Tables that turned out unused (raw records, tags, secrets) were dropped by a
  later migration rather than by editing the one that created them.
