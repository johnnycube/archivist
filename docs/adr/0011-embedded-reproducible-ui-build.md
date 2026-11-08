# The SvelteKit UI is embedded in the binary from a committed, reproducible build

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

The server should ship as one binary and one container image, a fresh clone
should build with `go build` alone, and the content-security policy must not
allow inline scripts, which SvelteKit's bootstrap script would otherwise need.

## Decision

`internal/webui` embeds the static SvelteKit build (`//go:embed all:dist`),
and `dist/` is committed. The build is reproducible: the app version is a
hash of the sources instead of the build time, so an unchanged UI rebuilds
byte-identically and CI fails when `frontend/` changed without `make web`.
SvelteKit's `csp.mode: hash` emits the bootstrap script's hash, which the Go
server reads from the embedded `index.html` into its CSP header.

## Consequences

- Every frontend change comes with a rebuilt `dist/` in the same commit.
- Script CSP has no `'unsafe-inline'`; styles still need it because Svelte
  sets style attributes.
- Dependency updates and test files under `src/` change the version hash and
  therefore `dist/`, even when the rendered app is the same.
