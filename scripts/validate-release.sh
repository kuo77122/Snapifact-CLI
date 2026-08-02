#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[[ $# -eq 1 ]] || { printf 'usage: %s RELEASE_DIR\n' "$0" >&2; exit 2; }
"$ROOT/scripts/build-release.sh" validate "$1"
