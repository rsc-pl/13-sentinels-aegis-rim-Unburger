#!/usr/bin/env bash
# make-demo.sh — regenerate the synthetic demo tree under examples/demo.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
go run -mod=vendor ./tools/make-demo -out examples/demo
