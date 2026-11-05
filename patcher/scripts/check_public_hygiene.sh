#!/usr/bin/env bash
# check_public_hygiene.sh — fail if the public boundary contains private or
# internal markers.
#
# The scan covers the repository root (not just the module): file contents,
# file and directory names, shipped binary metadata, the vendored tree for
# project-specific markers, and read-only Git checks for accidentally tracked
# private files. The single implementation lives in the Go internal/hygiene
# package behind tools/public-hygiene.
#
# Usage:
#   ./scripts/check_public_hygiene.sh              # full repository scan
#   ./scripts/check_public_hygiene.sh --changed    # Git changed/untracked set
#   ./scripts/check_public_hygiene.sh --changed FILE...  # explicit files
#   ./scripts/check_public_hygiene.sh --changed --fix    # safe formatting + rescan
#
# Exit status is non-zero on any blocking finding.
set -u

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
root=$(CDPATH= cd -- "$script_dir/../.." && pwd -P)
module="$root/patcher"

mode=full
fix=0
json=0
files=()

while [ "$#" -gt 0 ]; do
	case "$1" in
		--full) mode=full; shift ;;
		--changed) mode=changed; shift ;;
		--fix) fix=1; shift ;;
		--json) json=1; shift ;;
		--) shift; while [ "$#" -gt 0 ]; do files+=("$1"); shift; done ;;
		-h|--help)
			sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
			exit 0
			;;
		-*)
			echo "check_public_hygiene: unknown option: $1" >&2
			exit 2
			;;
		*) files+=("$1"); shift ;;
	esac
done

args=(-root "$root")
[ "$mode" = changed ] && args+=(-changed)
[ "$fix" = 1 ] && args+=(-fix)
[ "$json" = 1 ] && args+=(-json)
if [ "${#files[@]}" -gt 0 ]; then
	# Accept either repository-relative paths or paths relative to the module.
	for f in "${files[@]}"; do
		if [ -e "$root/$f" ]; then
			args+=("$f")
		elif [ -e "$module/$f" ]; then
			args+=("patcher/$f")
		else
			args+=("$f")
		fi
	done
fi

cd "$module"
exec go run -mod=vendor ./tools/public-hygiene "${args[@]}"
