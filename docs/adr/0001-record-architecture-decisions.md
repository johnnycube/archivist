# Record architecture decisions

- Status: accepted
- Date: 2026-10-21

## Context

`ARCHITECTURE.md` carries the design and its implementation status, but the
reasons behind the choices were scattered over commit messages, code
comments and the development notes. Without the why, every settled question is open to
re-litigation by the next contributor.

## Decision

Significant decisions are recorded as Markdown ADRs in `docs/adr/`, numbered,
in MADR form (context, decision, consequences). The first records (0002-0011)
capture the decisions already visible in the code.

## Consequences

- A change that reverses a recorded decision adds a new record that supersedes
  the old one; records are never edited into something they did not say.
- `ARCHITECTURE.md` stays the description of the whole; an ADR is the argument
  for one part of it.
