# Contributing

Thanks for helping with Archivist. This page is the short version; the
layout, conventions and testing notes are in
[`docs/development.md`](./docs/development.md), the design in
[`ARCHITECTURE.md`](./ARCHITECTURE.md) and the reasons for it in
[`docs/adr/`](./docs/adr/).

## Set up

```bash
make tools      # pinned Go, Node and tools from mise.toml (needs https://mise.jdx.dev), npm ci
prek install    # git hooks: hygiene, lint on changed lines, commit-message format
make run        # the server with the in-memory store and the demo account
```

`make help` lists every target. `make test` runs the hermetic tests;
`make dev-db && make test-pg` adds the PostgreSQL contract tests.

## Before you push

`make check` is the definition of green and what CI runs: format check,
linters, tests, coverage floors, dependency audit, build, fleetlint. The
pre-push hook runs it for you.

- Linters are ratcheted: a line you touch has to pass, untouched code is not
  rewritten. Do not widen exclusions or lower a coverage floor.
- After changing anything in `frontend/`, run `make web` and commit the
  rebuilt `internal/webui/dist` with it.
- A new provider needs fixtures and a golden test; a new port needs a memory
  and a PostgreSQL implementation plus a contract suite.
- A provider's `ExternalID`s are user data identities: deterministic per
  export record, unique within the account. Changing how one is derived is a
  user-visible change — say so in the commit body, so the release notes can
  tell users to reprocess those imports
  ([ADR 0016](./docs/adr/0016-reprocess-replaces-and-external-id-stability.md)).

## Commits and pull requests

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat:`, `fix:`, `docs:`, `refactor:`, ...); the commit-msg hook enforces it
and the changelog is generated from them. One topic per pull request, with
`make check` green and a note on what you tested by hand when the change is
hard to cover with tests. Design decisions get a record in `docs/adr/`.

## Reporting problems

Bugs and feature requests go to the issue tracker. For a security problem,
follow [`SECURITY.md`](./SECURITY.md) instead of opening an issue.
