# Releasing

For maintainers: how a version gets out, and the repository settings it relies
on. Versions follow [Semantic Versioning](https://semver.org/); while the
major version is 0, a minor release may break things, and its release notes
and [`upgrading.md`](./upgrading.md) say how.

## Cutting a release

1. **Green on `main`**: CI passed, and locally with the services
   `make dev-db dev-s3 && make test-pg` (PostgreSQL contract suites, S3).
2. **Smoke test of the image** — the thing users run:
   ```bash
   docker compose up -d --build
   docker compose exec app /app/archivist admin create-user -email smoke@example.com
   # log in on http://localhost:8080, import a small export, open an event
   docker compose down -v
   ```
3. **Release commit** on a branch, merged like any other change:
   ```bash
   git cliff --unreleased --tag v0.2.0 --prepend CHANGELOG.md   # the new section, from the commits
   # frontend/package.json "version": "0.2.0", then: make web
   git commit -am "chore(release): v0.2.0"
   ```
   Read the generated changelog: every `BREAKING` entry needs a line in
   `docs/upgrading.md`, and a change to how a provider derives its
   `ExternalID`s needs "reprocess your <source> imports" in the notes
   ([ADR 0016](./adr/0016-reprocess-replaces-and-external-id-stability.md)).
4. **Signed tag on the merged commit**, pushed:
   ```bash
   git tag -s v0.2.0 -m v0.2.0 && git push origin v0.2.0
   ```
   The release workflow builds `linux/amd64` + `linux/arm64`, pushes
   `:0.2.0`, `:latest` and `:sha-…`, signs the image and creates the release
   with notes from `git-cliff`.
5. **Check the result**: the release notes, and the signature:
   ```bash
   cosign verify ghcr.io/johnnycube/archivist:0.2.0 \
     --certificate-identity-regexp '^https://github.com/johnnycube/archivist/\.github/workflows/release\.yml@' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   docker run --rm ghcr.io/johnnycube/archivist:0.2.0 -version
   ```

## Repository settings (GitHub, once)

- **Security** → enable *Private vulnerability reporting* (`SECURITY.md`
  promises it) and *Dependabot alerts*.
- **Rules** for `main`: changes through pull requests, the `check`, `sast` and
  `docker` jobs of CI required, no force pushes; tags `v*` restricted to
  maintainers.
- **Actions** → *Workflow permissions*: read-only by default (each job asks
  for what it needs).
- **Packages**: after the first release, make `archivist` public and link it to
  the repository (package settings).
- **Dependency updates**: the Renovate GitHub app (it reads `renovate.json`),
  unless another instance already updates the repository.
- About: description, website, topics (`self-hosted`, `data-export`,
  `gdpr`, `timeline`, `archive`, `golang`, `sveltekit`).

## Gitea (when releasing there too)

The release workflow also runs on Gitea Actions and publishes to Gitea's
registry. It needs the repository variable `REGISTRY` (the registry's public
host), the secrets `REGISTRY_USER` / `REGISTRY_TOKEN` (package read/write;
the older `DOCKER_REGISTRY_GITEA_*` names are read too), and for signatures
`COSIGN_PRIVATE_KEY` / `COSIGN_PASSWORD` (commit the matching `cosign.pub`).
A GitHub push mirror from Gitea keeps both forges on the same history; the tag
pushed to Gitea then reaches GitHub and releases there as well.
