# syntax=docker/dockerfile:1

# Multi-platform (linux/amd64, linux/arm64): the UI and the Go binaries are
# built on the build machine's own platform and cross-compiled for the
# target, and the runtime stage has no RUN step — so building for another
# architecture needs no emulation (no QEMU on the CI runner).

# 1) Build the SvelteKit UI into internal/webui/dist (embedded by the Go binary).
#    Static files: the same for every target platform.
#    Same Node major as mise.toml (what CI builds and tests the UI with);
#    scripts/check-toolchain.sh, run by `make lint`, fails when they drift.
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS web
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./frontend/
RUN cd frontend && npm ci
COPY frontend ./frontend
RUN cd frontend && npm run build   # adapter-static → /src/internal/webui/dist

# 2) Build the Go binaries for the target platform. Modules are downloaded in
#    their own layer (pinned by go.sum), so it is cached until go.mod/go.sum
#    change. Same Go major.minor as mise.toml (scripts/check-toolchain.sh).
#    -trimpath keeps build-machine paths out of the binaries (reproducible builds).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN go build -trimpath -mod=readonly -ldflags "-X main.version=${VERSION}" -o /out/archivist ./cmd/server
# archivist-blobs moves an existing /data/blobs filesystem store into S3:
#   docker compose run --rm app /app/archivist-blobs migrate-to-s3 -from /data/blobs
RUN go build -trimpath -mod=readonly -o /out/archivist-blobs ./cmd/archivist-blobs
# The data directory, owned by the runtime user (the runtime stage cannot RUN).
RUN mkdir -p /out/data

# 3) Minimal runtime image. Runs as uid/gid 10001 (no passwd entry needed:
#    the binaries look up no users).
#    No tzdata package is needed: the server imports time/tzdata, so the IANA
#    zone database (~410 KiB) is embedded in the binary and ?tz=Europe/Berlin
#    resolves here exactly as it does on a developer's machine.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
COPY --from=build --chown=10001:10001 /out/data /data
WORKDIR /app
COPY --from=build /out/archivist /out/archivist-blobs /app/
COPY migrations ./migrations
USER 10001:10001
ENV ADDR=:8080 DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/app/archivist"]
