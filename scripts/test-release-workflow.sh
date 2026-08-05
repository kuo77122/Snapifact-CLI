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
require "$workflow" 'workflow_dispatch:'
require "$workflow" 'github.actor == github.repository_owner'
require "$workflow" 'refs/heads/main'
require "$workflow" 'ref: ${{ github.sha }}'
require "$workflow" 'needs: preflight'
require "$workflow" 'environment: r2-release-preview'
require "$workflow" 'cancel-in-progress: false'
require "$workflow" 'gh api'
require "$workflow" 'validateAssets'
require "$workflow" 'sha256sum'
require "$workflow" 'gh release download'
require "$workflow" 'gh release create'
require "$workflow" '--verify-tag'
require "$workflow" '--draft'
require "$workflow" 'snapifact_linux_amd64'
require "$workflow" 'snapifact_linux_arm64'
require "$workflow" 'snapifact_darwin_amd64'
require "$workflow" 'snapifact_darwin_arm64'
require "$workflow" 'SHA256SUMS'
require "$workflow" 'install.sh'
require "$workflow" 'publish-r2.mjs publish'
require "$workflow" 'publish-r2.mjs verify'
require "$workflow" 'publish-r2.mjs rollback'
require "$workflow" 'stable-diagnostic'
require "$workflow" 'node scripts/publish-r2.mjs stable-diagnostic'
require "$workflow" 'environment: r2-release-production'
require "$workflow" "artifact_digest: sha256:\${{ steps.provenance_artifact.outputs.artifact-digest }}"
require "$workflow" 'jq -r '\''.digest'\'' <<<"$artifact")" == "$PRODUCTION_ARTIFACT_DIGEST" ]]'
require "$workflow" 'sha256:$(sha256sum "$archive" | cut -d '\'' '\'' -f1)" == "$PRODUCTION_ARTIFACT_DIGEST" ]]'
require "$workflow" "if: \${{ github.event_name == 'push' && github.ref_type == 'tag' && github.repository == 'kuo77122/Snapifact-CLI' && github.actor == github.repository_owner && needs.verify.result == 'success' && needs.publish.result == 'success' && needs.verify.outputs.canonical_tag == 'true' }}"
if [[ "$workflow" == *'false &&'* ]]; then
	fail 'Production publication remains inert'
fi
require "$workflow" 'needs: [verify, publish]'
require "$workflow" 'needs: [publish-production, publish]'
require "$workflow" 'gh release edit "$RELEASE_VERSION" --repo "$GITHUB_REPOSITORY" --draft=false'
require "$workflow" 'mutation_succeeded=true'

if [[ "$workflow" == *'inputs.environment'* || "$workflow" == *'inputs.bucket'* || "$workflow" == *'inputs.public_origin'* ]]; then
	fail 'generic environment selector is present'
fi
if [[ "$workflow" == *'gh release delete'* || "$workflow" == *' list '* || "$workflow" == *' delete '* ]]; then
	fail 'release mutation or arbitrary object interface is present'
fi

node --test "$ROOT/scripts/release-workflow.test.mjs"
printf 'release workflow contract tests passed\n'
