# Archivist — developer tasks. See docs/development.md for context; `make help` lists targets.
.DEFAULT_GOAL := help
.PHONY: help setup tools web build run run-pg test test-pg test-s3 vet fmt lint cover audit mutate check check-fast \
        tidy docker dev-db dev-db-podman dev-db-stop dev-s3 dev-s3-stop migrate-blobs clean distclean demo export-docs icons

# Task-runner contract (fleetlint taskrunner/targets): fmt, lint, test, cover,
# audit, build, check, check-fast are the targets the git hooks and CI call.
# Tool versions come from mise.toml (`make tools`).
# Mutation testing: fail when fewer than MUTATE_MIN % of the mutants are killed.
MUTATE_MIN ?= 75

DATABASE_URL ?= postgres://archivist:archivist@localhost:5432/archivist?sslmode=disable
# The throwaway S3 store of `make dev-s3` (loopback only: these credentials
# are public). run-pg, test-pg and test-s3 point at it; override to use your own.
BLOB_S3_ENDPOINT ?= http://127.0.0.1:9000
BLOB_S3_ACCESS_KEY ?= archivist
BLOB_S3_SECRET_KEY ?= archivist-secret
S3_TEST_ENV = BLOB_S3_TEST_ENDPOINT="$(BLOB_S3_ENDPOINT)" BLOB_S3_TEST_ACCESS_KEY="$(BLOB_S3_ACCESS_KEY)" BLOB_S3_TEST_SECRET_KEY="$(BLOB_S3_SECRET_KEY)"

# Container engine for dev-db / dev-s3: podman if installed, else docker.
# Override with `make dev-db CONTAINER=docker`.
CONTAINER ?= $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)
# Keep in step with docker-compose.yml and deploy/compose.s3.yml.
PG_IMAGE ?= docker.io/library/postgres:16.15-alpine
# SILO, the maintained MinIO fork (MinIO's community images ended in 2025).
S3_IMAGE ?= docker.io/pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

setup: ## Install frontend dependencies
	cd frontend && npm install

tools: ## Install the pinned toolchain and tools from mise.toml, then the frontend dependencies
	@command -v mise >/dev/null 2>&1 || { echo "tools: mise not found — https://mise.jdx.dev/getting-started.html"; exit 1; }
	mise install
	cd frontend && npm ci

web: ## Build the SvelteKit UI into internal/webui/dist (required before go build)
	cd frontend && npm run build

build: web ## Build UI + Go binaries (-trimpath: reproducible, no build-machine paths)
	go build -trimpath ./...

run: ## Run the server with the in-memory store + demo data (http://localhost:8080)
	DATABASE_URL=memory ALLOW_SIGNUP=1 SEED_DEMO=$${SEED_DEMO:-1} COOKIE_SECURE=0 LOG_FORMAT=text go run ./cmd/server

run-pg: ## Run the server against $(DATABASE_URL) + the dev S3 blob store (make dev-db dev-s3 first)
	DATABASE_URL="$(DATABASE_URL)" MIGRATE=1 ALLOW_SIGNUP=1 COOKIE_SECURE=0 LOG_FORMAT=text \
	  BLOB_S3_ENDPOINT="$(BLOB_S3_ENDPOINT)" BLOB_S3_ACCESS_KEY="$(BLOB_S3_ACCESS_KEY)" BLOB_S3_SECRET_KEY="$(BLOB_S3_SECRET_KEY)" \
	  go run ./cmd/server

test: ## All tests with the race detector (Postgres and S3 integration tests skip without their env)
	go test -race -shuffle=on ./...
	cd frontend && npx vitest run

test-pg: ## Run all Go tests including the Postgres AND S3 integration tests (make dev-db dev-s3)
	DATABASE_URL="$(DATABASE_URL)" $(S3_TEST_ENV) go test ./...

test-s3: ## Run the S3 integration tests only (blob store, media Range, migration CLI)
	$(S3_TEST_ENV) go test -race -count=1 -run 'S3|RealEndpoint' ./internal/adapters/blob/ ./internal/adapters/rest/ ./cmd/archivist-blobs/

vet: ## go vet
	go vet ./...

fmt: ## Format in place: gofumpt + goimports (golangci-lint fmt), prettier (frontend)
	golangci-lint fmt ./...
	cd frontend && npx prettier --write .

lint: ## Format check + linters: golangci-lint (ratcheted, .golangci.yml), prettier, eslint, actionlint, zizmor, shellcheck, toolchain drift
	golangci-lint fmt --diff ./...
	golangci-lint run ./...
	cd frontend && npx prettier --check . && npx eslint --max-warnings=0 .
	actionlint
	zizmor --offline --min-severity medium .github/workflows
	shellcheck $$(git ls-files '*.sh')
	scripts/check-toolchain.sh

cover: ## Tests with coverage; fail below the floors (.testcoverage.yml, frontend/vitest.config.js)
	go test -coverprofile=cover.out -coverpkg=./... ./...
	go-test-coverage --config .testcoverage.yml
	cd frontend && npx vitest run --coverage

audit: ## Vulnerabilities (govulncheck, npm audit), licenses, secrets (gitleaks), unused deps (knip), tidy modules
	govulncheck ./...
	go mod tidy -diff && go mod verify
	go-licenses check ./... --ignore github.com/johnnycube/archivist --allowed_licenses=MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC
	gitleaks git --no-banner .
	cd frontend && npm audit --audit-level=high && npx knip && \
	  npx license-checker-rseidelsohn --production --excludePrivatePackages --onlyAllow "MIT;ISC;Apache-2.0;BSD-2-Clause;BSD-3-Clause;0BSD;CC0-1.0"

mutate: ## Mutation testing of the application core with gremlins (weekly in CI; MUTATE_MIN=75, measured 82.9 % at adoption)
	gremlins unleash --threshold-efficacy $(MUTATE_MIN) ./internal/app

check-fast: ## Before a commit (< 30 s): lint + tests
	$(MAKE) lint
	$(MAKE) test

check: lint test cover audit build ## Definition of green — what CI runs
	fleetlint check --fail-on error

tidy: ## go mod tidy + verify (dependencies are not vendored)
	go mod tidy && go mod verify

docker: ## Build and run the stack (app + postgres; S3: add deploy/compose.s3.yml) via Docker
	docker compose up --build

dev-db: ## Start a throwaway PostgreSQL 16 on 127.0.0.1:5432 (podman or docker)
	@test -n "$(CONTAINER)" || { echo "dev-db: neither podman nor docker found in PATH — install one, or point DATABASE_URL at your own PostgreSQL"; exit 1; }
	@# Loopback only: the password is public (it is in this file), so the port
	@# must not be reachable from the network.
	$(CONTAINER) run -d --name archivist-db -p 127.0.0.1:5432:5432 \
	  -e POSTGRES_USER=archivist -e POSTGRES_PASSWORD=archivist -e POSTGRES_DB=archivist \
	  $(PG_IMAGE)

dev-db-podman: ## Same as dev-db, forcing podman
	@$(MAKE) --no-print-directory dev-db CONTAINER=podman

dev-db-stop: ## Stop and remove the throwaway database
	@test -n "$(CONTAINER)" || { echo "dev-db-stop: neither podman nor docker found in PATH"; exit 1; }
	$(CONTAINER) rm -f archivist-db

dev-s3: ## Start a throwaway S3 store (SILO) on 127.0.0.1:9000 (podman or docker); run-pg/test-pg/test-s3 use it
	@test -n "$(CONTAINER)" || { echo "dev-s3: neither podman nor docker found in PATH — install one, or point BLOB_S3_* at your own S3"; exit 1; }
	@# Loopback only, console not published: the credentials are public (in this file).
	$(CONTAINER) run -d --name archivist-s3 -p 127.0.0.1:9000:9000 \
	  -e MINIO_ROOT_USER="$(BLOB_S3_ACCESS_KEY)" -e MINIO_ROOT_PASSWORD="$(BLOB_S3_SECRET_KEY)" \
	  $(S3_IMAGE) server /data

dev-s3-stop: ## Stop and remove the throwaway S3 store
	@test -n "$(CONTAINER)" || { echo "dev-s3-stop: neither podman nor docker found in PATH"; exit 1; }
	$(CONTAINER) rm -f archivist-s3

migrate-blobs: ## Copy $$DATA_DIR/blobs (filesystem store) into the S3 store named by BLOB_S3_* (idempotent, resumable)
	BLOB_S3_ENDPOINT="$(BLOB_S3_ENDPOINT)" BLOB_S3_ACCESS_KEY="$(BLOB_S3_ACCESS_KEY)" BLOB_S3_SECRET_KEY="$(BLOB_S3_SECRET_KEY)" \
	  go run ./cmd/archivist-blobs migrate-to-s3 -from "$${DATA_DIR:-./data}/blobs"

demo: ## CLI: import a zip and print coverage (make demo SRC=reddit ZIP=export.zip)
	go run ./cmd/archivist-demo -source $(SRC) -zip $(ZIP)

export-docs: ## Regenerate docs/exports.md from frontend/src/lib/exportGuides.js
	node frontend/scripts/gen-export-docs.mjs

icons: ## Render the favicon/app icons in frontend/static from favicon.svg (resvg), then rebuild the UI
	node frontend/scripts/gen-icons.mjs
	$(MAKE) web

clean: ## Remove caches and local binaries (keeps the committed internal/webui/dist)
	rm -rf frontend/.svelte-kit frontend/node_modules/.vite
	rm -f archivist server archivist-demo
	go clean

distclean: clean ## clean + node_modules + the built UI. `go build` FAILS until `make setup web` (or `git checkout internal/webui/dist`)
	rm -rf frontend/node_modules internal/webui/dist
