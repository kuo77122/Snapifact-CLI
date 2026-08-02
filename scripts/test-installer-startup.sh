#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/snapifact-installer-startup-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
cat >"$TMP/failing-go" <<'EOF'
#!/bin/sh
printf 'simulated build startup failure\n' >&2
exit 17
EOF
chmod 755 "$TMP/failing-go"
if output=$(GO="$TMP/failing-go" "$ROOT/scripts/test-installer.sh" 2>&1); then
	printf 'installer startup test failed: early build failure was accepted\n' >&2
	exit 1
fi
[[ "$output" == *'simulated build startup failure'* ]] || { printf '%s\n' "$output" >&2; exit 1; }
printf 'installer startup tests passed\n'
