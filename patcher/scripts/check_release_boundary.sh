#!/usr/bin/env bash
# check_release_boundary.sh — read-only release privacy boundary guard.
#
# It (1) runs the full public-boundary scan, which includes read-only Git checks
# for accidentally tracked private instruction files, the private data directory,
# build output and game artifacts; (2) stages the real release in an isolated
# temporary tree (never touching an existing release); and (3) scans the staged
# release contents, names and binary metadata for private instructions, agent
# configuration, private data, reports, caches, keys/containers and debug files.
#
# It never writes to the repository and never modifies Git state. Set
# HYGIENE_STAGE_DIR to inspect a staging directory instead of a fresh temporary
# one; the default temporary directory is removed on exit.
set -u

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
root=$(CDPATH= cd -- "$script_dir/../.." && pwd -P)
module="$root/patcher"
status=0

note() { echo "$*"; }
fail() { echo "RELEASE BOUNDARY FAIL: $*" >&2; status=1; }

# 1. Full public boundary, including read-only Git checks.
if ! ( cd "$module" && go run -mod=vendor ./tools/public-hygiene -root "$root" ); then
	fail "public boundary scan failed"
fi

if [ -d "$root/.local" ] && [ ! -e "$root/.git/info/exclude" ]; then
	note "note: .local exists without a Git exclude file; the full scan reports the ignore boundary"
fi

# 2. Stage the real release in isolation. Only the module and the root version
# file are inputs; build output and private state are excluded from the copy.
created=0
if [ -n "${HYGIENE_STAGE_DIR:-}" ]; then
	stage="$HYGIENE_STAGE_DIR"
	mkdir -p "$stage" || { fail "cannot create HYGIENE_STAGE_DIR"; exit 1; }
else
	stage=$(mktemp -d "${TMPDIR:-/tmp}/13s-release-boundary.XXXXXX") || {
		fail "cannot create a temporary staging directory"
		exit 1
	}
	created=1
fi
cleanup() { if [ "$created" -eq 1 ]; then rm -rf "$stage"; fi; }
trap cleanup EXIT

note "staging release under $stage"
rm -rf "$stage/patcher" "$stage/version.txt"
if ! tar -C "$root" \
	--exclude='patcher/bin' --exclude='patcher/dist' --exclude='patcher/reports' \
	--exclude='patcher/build' -cf - patcher version.txt 2>/dev/null |
	tar -C "$stage" -xf -; then
	fail "could not stage the module and version file"
fi

if [ "$status" -eq 0 ]; then
	if ! ( cd "$stage/patcher" && CGO_ENABLED=0 GOFLAGS=-mod=vendor GOPROXY=off \
		make release >/dev/null 2>&1 ); then
		fail "make release failed in the isolated staging tree"
	fi
fi

release_dir=""
if [ "$status" -eq 0 ]; then
	release_dir=$(find "$stage/patcher/dist" -maxdepth 1 -mindepth 1 -type d -name '13s-patcher-*' 2>/dev/null | head -n1)
	[ -n "$release_dir" ] || fail "release directory was not produced"
fi

# 3. Scan the actual distributable tree: names, contents and binary metadata.
if [ -n "$release_dir" ] && [ "$status" -eq 0 ]; then
	if ! ( cd "$module" && go run -mod=vendor ./tools/public-hygiene -root "$release_dir" -release ); then
		fail "staged release content/name/metadata scan failed"
	fi

	# Absolute personal paths and private workspace names can be embedded in a
	# binary even when the source tree is clean. Fragment the patterns so this
	# script does not contain them itself.
	home_pat="/ho""me/"
	users_pat="/Us""ers/"
	workspace_pat="Hermes""Workspace"
	private_pat="Da""ne2"
	hits=$(find "$release_dir" -type f -print0 |
		xargs -0 grep -alE "$home_pat|$users_pat|$workspace_pat|$private_pat" 2>/dev/null || true)
	if [ -n "$hits" ]; then
		fail "absolute/private path string in release: $hits"
	fi

	count=$(find "$release_dir" -type f | wc -l | tr -d ' ')
	note "release inspected: $count file(s) under $release_dir"
fi

if [ "$status" -ne 0 ]; then
	echo "release boundary check FAILED" >&2
	exit 1
fi
echo "release boundary check PASSED"
