#!/bin/sh
# run-patcher.sh — launch the patch builder from the repository root.
#
# It builds the tool once (offline, using the vendored dependencies) and then
# runs it, forwarding every argument and the exit code. It is a launcher, not an
# installer: it writes only the tool binary inside the repository.
#
# The binary is rebuilt whenever the module's content changes — every Go source,
# the module files and the whole vendored tree are hashed into a stamp next to
# the binary, so a changed file is detected even when its timestamp is not newer
# (for example after a checkout or a patch). When hashing is unavailable it
# falls back to a timestamp comparison.
#
# Works when invoked from any shell (sh, bash, fish, zsh) and from any working
# directory.

set -eu

# Resolve this script's directory, tolerating spaces in the path.
script=$0
case $script in
	*/*) ;;
	*) script=./$script ;;
esac
root=$(CDPATH= cd -- "$(dirname -- "$script")" && pwd -P)

module="$root/patcher"
bin="$module/bin/13s-patcher"
stamp="$module/bin/13s-patcher.stamp"

if ! command -v go >/dev/null 2>&1; then
	echo "run-patcher: the Go toolchain is required but 'go' was not found on PATH" >&2
	echo "            install Go (1.26 or newer) and try again" >&2
	exit 127
fi

# source_hash prints a stable content hash over every input that affects the
# binary: Go sources, the module files and the vendored dependencies. It prints
# nothing (and the caller falls back to timestamps) when no hashing tool exists.
source_hash() {
	command -v sha256sum >/dev/null 2>&1 || return 1
	(
		cd "$module"
		find . -type f \
			\( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' -o -path './vendor/*' \) \
			-not -path './bin/*' -print | LC_ALL=C sort
	) | while IFS= read -r f; do
		printf '%s\0' "$f"
		cat "$module/$f"
	done | sha256sum | cut -d' ' -f1
}

newer_source() {
	find "$module" -type f \
		\( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' -o -path '*/vendor/*' \) \
		-not -path '*/bin/*' -newer "$bin" -print -quit 2>/dev/null || true
}

needs_build=0
if [ ! -x "$bin" ]; then
	needs_build=1
else
	current=$(source_hash 2>/dev/null || true)
	if [ -n "$current" ]; then
		recorded=""
		if [ -f "$stamp" ]; then
			recorded=$(cat "$stamp" 2>/dev/null || true)
		fi
		if [ "$current" != "$recorded" ]; then
			needs_build=1
		fi
	else
		# No hashing available: fall back to the timestamp comparison.
		if [ -n "$(newer_source)" ]; then
			needs_build=1
		fi
	fi
fi

if [ "${PATCHER_FORCE_BUILD:-0}" = "1" ]; then
	needs_build=1
fi

if [ "$needs_build" = "1" ]; then
	echo "run-patcher: building 13s-patcher" >&2
	(
		cd "$module"
		mkdir -p bin
		GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=0 \
			go build -trimpath -mod=vendor -o "$bin" ./cmd/13s-patcher
	)
	# Record the content hash so the next launch skips the rebuild. A failed
	# build never reaches here, so a broken tree rebuilds again next time.
	if h=$(source_hash 2>/dev/null); then
		printf '%s\n' "$h" > "$stamp"
	fi
fi

# exec preserves the terminal (so the interactive dashboard works) and the
# exit code.
exec "$bin" "$@"
