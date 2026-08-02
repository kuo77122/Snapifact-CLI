#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKFLOW=$ROOT/.github/workflows/release.yml

fail() {
	printf 'workflow contract test failed: %s\n' "$*" >&2
	exit 1
}

require() {
	local text=$1 needle=$2
	[[ "$text" == *"$needle"* ]] || fail "missing: $needle"
}

[[ -f "$WORKFLOW" ]] || fail 'release workflow is missing'
workflow=$(<"$WORKFLOW")

require "$workflow" 'permissions:'
require "$workflow" 'contents: read'
require "$workflow" 'name: Verify (ubuntu-latest)'
require "$workflow" 'canonical_tag:'
require "$workflow" 'canonical_tag=true'
require "$workflow" 'pull_request:'
require "$workflow" 'push:'
require "$workflow" 'refs/tags/v(0|[1-9][0-9]*)'
require "$workflow" 'needs: verify'
require "$workflow" "needs.verify.outputs.canonical_tag == 'true'"
require "$workflow" 'github.event_name == '\''push'\'''
require "$workflow" 'contents: write'
require "$workflow" 'gh release create'
require "$workflow" '--verify-tag'
require "$workflow" '--draft'
require "$workflow" 'snapifact_linux_amd64'
require "$workflow" 'snapifact_linux_arm64'
require "$workflow" 'snapifact_darwin_amd64'
require "$workflow" 'snapifact_darwin_arm64'
require "$workflow" 'SHA256SUMS'
require "$workflow" 'install.sh'
require "$workflow" 'validate-release.sh'

if [[ "$workflow" == *'workflow_dispatch'* ]]; then
	fail 'manual dispatch is present'
fi
blocked_one=$(printf '\143\157\162\145')
blocked_two=$(printf '\122\62')
blocked_three=$(printf '\160\162\157\144\165\143\164\151\157\156')
if [[ "$workflow" == *"$blocked_one"* || "$workflow" == *"$blocked_two"* || "$workflow" == *"$blocked_three"* ]]; then
	fail 'forbidden publication boundary is referenced'
fi

printf 'release workflow contract tests passed\n'
