#!/usr/bin/env bash
# Deploy Archivist on this host from the image the release workflow published
# (ghcr.io/johnnycube/archivist by default; any registry it pushed to).
#
#   deploy/deploy.sh [tag]            # default: latest
#   ARCHIVIST_IMAGE=registry/owner/archivist:1.2.3 deploy/deploy.sh
#
# What it does: docker login (when REGISTRY_USER/_TOKEN are set),
# compose pull + up with deploy/compose.registry.yml layered over
# docker-compose.yml, then waits for /readyz and prints the running version.
# Settings (environment or the .env file next to docker-compose.yml):
#   ARCHIVIST_IMAGE    full image reference; overrides REGISTRY/IMAGE_NAME/tag
#   REGISTRY           default ghcr.io
#   IMAGE_NAME         default johnnycube/archivist
#   REGISTRY_USER / REGISTRY_TOKEN   login before pulling, for a private registry
#                      (package:read is enough; DOCKER_REGISTRY_GITEA_USER /
#                      _TOKEN, the older names, are read too)
#   ARCHIVIST_COMPOSE_OVERLAYS  extra compose files, space-separated, e.g.
#                      deploy/compose.s3.yml for the S3 blob store
#   APP_URL            default http://127.0.0.1:8080 (where /readyz answers)
#   COMPOSE            default "docker compose" (set "podman compose" or "docker-compose")
#   WAIT_SECONDS       default 120
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
cd "$root"

# .env next to docker-compose.yml: compose reads it, so does this script.
if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091 # operator-provided, not part of the repository
  . ./.env
  set +a
fi

tag="${1:-latest}"
registry="${REGISTRY:-ghcr.io}"
image_name="${IMAGE_NAME:-johnnycube/archivist}"
registry_user="${REGISTRY_USER:-${DOCKER_REGISTRY_GITEA_USER:-}}"
registry_token="${REGISTRY_TOKEN:-${DOCKER_REGISTRY_GITEA_TOKEN:-}}"
export ARCHIVIST_IMAGE="${ARCHIVIST_IMAGE:-${registry}/${image_name}:${tag}}"
app_url="${APP_URL:-http://127.0.0.1:8080}"
wait_seconds="${WAIT_SECONDS:-120}"
read -r -a compose <<<"${COMPOSE:-docker compose}"
compose+=(-f docker-compose.yml -f deploy/compose.registry.yml)
read -r -a overlays <<<"${ARCHIVIST_COMPOSE_OVERLAYS:-}"
s3_overlay=0
for f in "${overlays[@]}"; do
  compose+=(-f "$f")
  [ "$(basename "$f")" = compose.s3.yml ] && s3_overlay=1
done

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }

[ -f docker-compose.yml ] || { echo "deploy: run from the checkout that holds docker-compose.yml" >&2; exit 1; }

if [ -n "$registry_token" ]; then
  log "logging in to ${ARCHIVIST_IMAGE%%/*} as ${registry_user:?REGISTRY_USER is required with REGISTRY_TOKEN}"
  printf '%s' "$registry_token" | "${compose[0]}" login "${ARCHIVIST_IMAGE%%/*}" -u "$registry_user" --password-stdin
fi

# An S3 data volume from an earlier deployment, but the S3 overlay is off: the
# app would start on an empty filesystem store and every archive and photo
# would seem gone. (Earlier builds ran MinIO in docker-compose.yml.)
if [ "$s3_overlay" = 0 ] && "${compose[0]}" volume ls -q 2>/dev/null | grep -Eq '(^|_)miniodata$'; then
  echo "deploy: an S3 data volume (miniodata) exists but the S3 overlay is not enabled." >&2
  echo "        Set ARCHIVIST_COMPOSE_OVERLAYS=deploy/compose.s3.yml in .env to keep using it," >&2
  echo "        or remove the volume if it is no longer needed." >&2
  exit 1
fi

previous="$("${compose[@]}" images --format '{{.Repository}}:{{.Tag}}' app 2>/dev/null | head -1 || true)"
log "deploying $ARCHIVIST_IMAGE${previous:+ (currently $previous)}"
"${compose[@]}" pull app
"${compose[@]}" up -d --remove-orphans

log "waiting up to ${wait_seconds}s for ${app_url}/readyz"
deadline=$((SECONDS + wait_seconds))
until curl -fsS -o /dev/null "${app_url}/readyz"; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "deploy: ${app_url}/readyz did not answer 200 in time; last log lines:" >&2
    "${compose[@]}" logs --tail=40 app >&2 || true
    exit 1
  fi
  sleep 2
done

log "ready: $(curl -fsS "${app_url}/healthz")"
log "done; roll back with: deploy/deploy.sh <previous tag>"
