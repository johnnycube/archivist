#!/usr/bin/env sh
# Fail when the container image would be built with another Go or Node than
# the one CI tests with (mise.toml). The two drifted once: Renovate bumped
# mise.toml to Go 1.27 while the image kept building with Go 1.26, whose
# encoding/json lacks the depth limit 1.27 has, so the tests passed on a
# compiler the release did not ship. Compared: Go major.minor, Node major.
set -eu
root="$(cd "$(dirname "$0")/.." && pwd)"
mise="$root/mise.toml"
dockerfile="$root/Dockerfile"

go_mise="$(sed -nE 's/^go = "([0-9]+\.[0-9]+).*/\1/p' "$mise")"
go_image="$(sed -nE 's/^FROM (--platform=[^ ]+ )?golang:([0-9]+\.[0-9]+).*/\2/p' "$dockerfile")"
node_mise="$(sed -nE 's/^node = "([0-9]+)\..*/\1/p' "$mise")"
node_image="$(sed -nE 's/^FROM (--platform=[^ ]+ )?node:([0-9]+).*/\2/p' "$dockerfile")"

status=0
if [ -z "$go_mise" ] || [ "$go_mise" != "$go_image" ]; then
	echo "toolchain: Dockerfile builds with Go ${go_image:-?}, mise.toml pins Go ${go_mise:-?}" >&2
	status=1
fi
if [ -z "$node_mise" ] || [ "$node_mise" != "$node_image" ]; then
	echo "toolchain: Dockerfile builds the UI with Node ${node_image:-?}, mise.toml pins Node ${node_mise:-?}" >&2
	status=1
fi
exit "$status"
