# Repository policy enforced by fleetlint at tier 1

- Status: accepted
- Date: 2026-10-21

## Context

The repository had CI but no hooks, no linter configuration, no coverage
floor, unpinned actions and images, and tool versions that depended on the
machine. It is a production service for its operator, which fleetlint calls
tier 1.

## Decision

`.fleetlint.yaml` pins tier 1 with the `fleetlint:recommended` preset and
private visibility; `make check` ends with `fleetlint check`, and the
pre-commit hook runs it on every commit. The task runner exposes the contract
targets (`fmt`, `lint`, `test`, `cover`, `audit`, `build`, `check`,
`check-fast`); tools are pinned in `mise.toml` and installed the same way
locally and in CI; hooks run through prek. Existing lint findings are
ratcheted rather than rewritten: golangci-lint with `new-from-rev` at the
adoption commit, ESLint with a bulk-suppressions file. Coverage floors are set
below the measured values at adoption and are only ever raised.

## Consequences

- Commit messages are Conventional Commits; `git-cliff` generates the
  changelog.
- Touching an old line means making it pass the linters; untouched code is
  not rewritten.
- Switch the preset to `fleetlint:oss` before publishing the repository so
  the public-repository rules (license, security policy) apply.
