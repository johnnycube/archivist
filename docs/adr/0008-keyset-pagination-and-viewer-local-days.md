# Keyset pagination; a "day" is the viewer's

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

A timeline over millions of events cannot use offset pagination, and
events from many sources arrive with instants in UTC while people think in
calendar days of their own zone. Daylight-saving days are 23 or 25 hours
long.

## Decision

The timeline is keyset-paginated on `(occurred_at desc, id desc)`, undated
events last, with opaque cursors encoded in `app/query.go` for both stores.
Instants are stored in UTC; `app.Query.Loc` carries the viewer's IANA zone
inward and `app.LocalDay` is the single definition of "which day", used by the
histogram buckets and by the collapse rule. REST reads the zone from `?tz=`,
which also decides what a date-only `from`/`to` means. PostgreSQL receives the
zone name as a bind parameter, so `Loc` must be an IANA location, never a
fixed offset.

## Consequences

- Pages are stable under concurrent imports and cost the same at any depth.
- `cmd/server` embeds `time/tzdata` so a scratch container resolves zones.
- Collapsing repetitive runs into clusters is done server-side with one rule
  shared by both stores.
