#!/usr/bin/env bash
# check_build_isolation.sh — prove the public module builds and tests from a
# clean copy of itself, with no dependency on any parent/private directory.
#
# It copies the module (including the vendored third-party tree) into a fresh
# temporary directory that has no parent project, then builds and tests offline.
# Private-path leakage in the source is caught separately by the hygiene scan.
set -euo pipefail

src="$(cd "$(dirname "$0")/.." && pwd)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
dest="$work/copy"
mkdir -p "$dest"
# Copy everything except VCS metadata and build outputs.
( cd "$src" && tar --exclude=.git --exclude=bin --exclude=dist --exclude=reports -cf - . ) | ( cd "$dest" && tar -xf - )

cd "$dest"
export GOFLAGS=-mod=vendor
export GOPROXY=off
go build ./...
go test ./...
echo "build isolation check PASSED (built and tested a clean copy with no parent project)"
