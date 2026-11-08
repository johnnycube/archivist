# An event belongs to one account

- Status: accepted
- Date: 2026-11-07

## Context

Events were upserted on `(user_id, source_id, external_id)`. A user can
connect several accounts of one source (two Reddit logins, a personal and a
work Google account), each import naming its account by a label. Provider ids
are unique within an export, not across exports: a Reddit vote is keyed by the
post's id, an unrecognised file by its path. So the second account's import
of an overlapping id took the first account's event over (its `account_id`
and `import_job_id` changed), and deleting that import deleted the first
account's event as well.

## Decision

The upsert key is `(user_id, account_id, source_id, external_id)`
(migration 0018, `events_upsert_key`). An event is the same event only within
one account; `user_id` stays in the key so that an upsert can only ever meet
rows of the user it writes for. The memory store derives its event ids from
the same four parts.

## Consequences

- Two accounts of a source keep separate events, and deleting one account's
  import never touches the other's.
- The same thing seen from two accounts (a group chat both exported) is two
  events. Merging them is a cross-account de-duplication feature, not a
  storage key.
- Re-importing an export under a different label than before creates a second
  set of events; the label is part of the account's identity.
