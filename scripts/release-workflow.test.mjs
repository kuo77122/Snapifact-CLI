import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const workflow = await readFile(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')

function jobBlock(name) {
  const lines = workflow.split('\n')
  const start = lines.findIndex((line) => line === `  ${name}:`)
  assert.notEqual(start, -1, `missing job: ${name}`)
  const body = []
  for (let i = start; i < lines.length; i += 1) {
    if (i > start && /^  [A-Za-z0-9_-]+:$/.test(lines[i])) break
    body.push(lines[i])
  }
  return body.join('\n')
}

const verify = jobBlock('verify')
const publish = jobBlock('publish')
const preflight = jobBlock('preflight')
const preview = jobBlock('preview-r2-rehearsal')
const backfill = jobBlock('backfill-v023')
const production = jobBlock('publish-production')
const finalizer = jobBlock('finalize-production-release')
const assets = [
  'snapifact_linux_amd64',
  'snapifact_linux_arm64',
  'snapifact_darwin_amd64',
  'snapifact_darwin_arm64',
  'SHA256SUMS',
  'install.sh',
]

test('manual preview is owner-only, main-only, and checks out dispatch HEAD', () => {
  assert.match(workflow, /workflow_dispatch:\n    inputs:\n      version:/)
  assert.match(
    preflight,
    /if: \$\{\{ github\.event_name == 'workflow_dispatch' && github\.ref == 'refs\/heads\/main' && github\.actor == github\.repository_owner && inputs\.operation == 'preview' \}\}/,
  )
  assert.match(
    preview,
    /if: \$\{\{ github\.event_name == 'workflow_dispatch' && github\.ref == 'refs\/heads\/main' && github\.actor == github\.repository_owner && needs\.preflight\.result == 'success' \}\}/,
  )
  assert.match(preflight, /ref: \$\{\{ github\.sha \}\}/)
  assert.match(preview, /ref: \$\{\{ github\.sha \}\}/)
})

test('dispatch version selects release assets and helper arguments, never implementation code', () => {
  assert.match(workflow, /RELEASE_VERSION: \$\{\{ inputs\.version \}\}/)
  assert.doesNotMatch(workflow, /ref: [^\n]*inputs\.version/)
  assert.doesNotMatch(workflow, /refs\/tags\/\$\{\{ inputs\.version \}\}/)
  assert.match(preflight, /gh api "repos\/\$GITHUB_REPOSITORY\/git\/ref\/tags\/\$RELEASE_VERSION"/)
  assert.match(preview, /gh api "repos\/\$GITHUB_REPOSITORY\/git\/ref\/tags\/\$RELEASE_VERSION"/)
})

test('preflight proves exact artifact inventory, tag SHA, installer pin, and six digests', () => {
  assert.match(preflight, /^    needs: verify$/m)
  assert.match(preflight, /permissions:\n      contents: read\n      actions: read/)
  assert.match(preflight, /jq -r '\.object\.sha'/)
  assert.match(preflight, /git\/tags\/\$tag_sha/)
  assert.match(preflight, /actions\/workflows\/release\.yml\/runs\?event=/)
  assert.match(preflight, /expired == false/)
  assert.match(preflight, /artifact_digest/)
  assert.match(preflight, /unzip -q/)
  assert.match(preflight, /await validateAssets\(process\.env\.RELEASE_ASSETS_DIR, process\.env\.RELEASE_VERSION\)/)
  assert.match(preflight, /sha256sum "\$RELEASE_ASSETS_DIR\/\$asset"/)
  assert.match(preflight, /tag_sha: \$\{\{ steps\.provenance\.outputs\.tag_sha \}\}/)
  assert.match(preflight, /digest_set: \$\{\{ steps\.digests\.outputs\.digest_set \}\}/)
  assert.match(preflight, /for asset in snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64 SHA256SUMS install\.sh/)
  assert.doesNotMatch(preflight, /SNAPIFACT_R2_|r2-release-preview/)
})

test('mutation re-downloads into a fresh directory and requires independent provenance convergence', () => {
  assert.match(preview, /^    needs: preflight$/m)
  assert.match(preview, /^    environment: r2-release-preview$/m)
  assert.match(preview, /concurrency:\n      group: snapifact-r2-preview-publication\n      cancel-in-progress: false/)
  assert.match(preview, /permissions:\n      contents: read\n      actions: read/)
  assert.match(preview, /rm -rf "\$RELEASE_ASSETS_DIR"/)
  assert.match(preview, /tag_sha" == "\$PREFLIGHT_TAG_SHA"/)
  assert.match(preview, /actions\/workflows\/release\.yml\/runs\?event=/)
  assert.match(preview, /artifact_digest/)
  assert.match(preview, /unzip -q/)
  assert.match(preview, /digest_set" == "\$PREFLIGHT_DIGEST_SET"/)
  assert.match(preview, /printf 'directory=%s\\n'/)
  assert.match(preview, /for asset in snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64 SHA256SUMS install\.sh/)
  assert.doesNotMatch(preview, /gh release (view|download)/)
})

test('only the final credential-bearing step binds approved Preview values', () => {
  const credentialStart = preview.indexOf('      - name: Publish, verify, rollback, and final verify')
  assert.ok(credentialStart >= 0)
  const beforeCredentials = preview.slice(0, credentialStart)
  const credentialStep = preview.slice(credentialStart)
  for (const reference of [
    'secrets.SNAPIFACT_R2_ACCESS_KEY_ID',
    'secrets.SNAPIFACT_R2_SECRET_ACCESS_KEY',
    'vars.SNAPIFACT_R2_ACCOUNT_ID',
    'vars.SNAPIFACT_R2_BUCKET',
  ]) {
    assert.equal((preview.match(new RegExp(reference.replaceAll('.', '\\.'), 'g')) ?? []).length, 1, reference)
    assert.doesNotMatch(beforeCredentials, new RegExp(reference.replaceAll('.', '\\.')))
    assert.match(credentialStep, new RegExp(reference.replaceAll('.', '\\.')))
  }
  assert.deepEqual(
    [...credentialStep.matchAll(/node scripts\/publish-r2\.mjs (publish|verify|rollback)/g)].map((match) => match[1]),
    ['publish', 'verify', 'rollback', 'verify'],
  )
  assert.equal((credentialStep.match(/--assets "\$RELEASE_ASSETS_DIR"/g) ?? []).length, 1)
})

test('ordinary verification and manual dispatch cannot create releases or resolve R2 credentials', () => {
  assert.match(publish, /if: \$\{\{ github\.event_name == 'push'/)
  assert.doesNotMatch(publish, /workflow_dispatch/)
  assert.doesNotMatch(verify, /SNAPIFACT_R2_|publish-r2\.mjs/)
  const nonProductionJobs = `${verify}\n${publish}\n${preflight}\n${backfill}`
  assert.doesNotMatch(nonProductionJobs, /r2-release-production|production/i)
  assert.doesNotMatch(workflow, /inputs\.(?:environment|bucket|public_origin)/)
  assert.doesNotMatch(nonProductionJobs, /gh release (edit|delete)|\b(delete|list)\b/i)
  assert.doesNotMatch(workflow, /Authorization|signature|private endpoint/i)
})

test('Production publication is active and reachable only from the canonical tag chain', () => {
  assert.match(
    production,
    /if: \$\{\{ github\.event_name == 'push' && github\.ref_type == 'tag' && github\.repository == 'kuo77122\/Snapifact-CLI' && github\.actor == github\.repository_owner && needs\.verify\.result == 'success' && needs\.publish\.result == 'success' && needs\.verify\.outputs\.canonical_tag == 'true' \}\}/,
  )
  assert.doesNotMatch(production, /false &&/)
  assert.match(production, /^    needs: \[verify, publish\]$/m)
  assert.match(production, /^    environment: r2-release-production$/m)
  assert.match(production, /concurrency:\n      group: snapifact-r2-production-publication\n      cancel-in-progress: false/)
  assert.match(production, /permissions:\n      contents: read\n      actions: read/)
  assert.doesNotMatch(production, /always\(\)|workflow_dispatch|inputs\.|github\.ref == 'refs\/heads\//)
})

test('Production revalidates the current-run provenance and six immutable release assets', () => {
  assert.match(publish, /tag_sha: \$\{\{ github\.sha \}\}/)
  assert.match(publish, /artifact_id: \$\{\{ steps\.provenance_artifact\.outputs\.artifact-id \}\}/)
  assert.match(publish, /artifact_digest: sha256:\$\{\{ steps\.provenance_artifact\.outputs\.artifact-digest \}\}/)
  assert.match(publish, /digest_set: \$\{\{ steps\.draft_assets\.outputs\.digest_set \}\}/)
  assert.match(production, /actions\/artifacts\/\$PRODUCTION_ARTIFACT_ID\/zip/)
  assert.match(production, /PRODUCTION_RUN_ID.*needs\.publish\.outputs\.run_id/)
  assert.match(production, /workflow_run\.id.*PRODUCTION_RUN_ID/)
  assert.match(production, /sha256sum.*archive/)
  assert.match(production, /validateAssets/)
  assert.match(production, /for asset in snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64 SHA256SUMS install\.sh/)
  assert.match(production, /digest_set.*PRODUCTION_DIGEST_SET|PRODUCTION_DIGEST_SET.*digest_set/)
  assert.match(production, /default_version=|RELEASE_VERSION/)
})

test('Production preserves exact REST and downloaded archive digest comparisons', () => {
  assert.match(production, /\[\[ "\$\(jq -r '\.digest' <<<"\$artifact"\)" == "\$PRODUCTION_ARTIFACT_DIGEST" \]\]/)
  assert.match(production, /\[\[ "sha256:\$\(sha256sum "\$archive" \| cut -d ' ' -f1\)" == "\$PRODUCTION_ARTIFACT_DIGEST" \]\]/)
})

test('Production installs publication dependencies before provenance revalidation', () => {
  const setupNode = production.indexOf('      - uses: actions/setup-node@')
  const install = production.indexOf('      - name: Install publication dependencies')
  const assets = production.indexOf('      - id: assets')

  assert.match(production, /uses: actions\/setup-node@[0-9a-f]{40}/)
  assert.ok(setupNode >= 0)
  assert.ok(install > setupNode)
  assert.ok(assets > install)
  assert.match(production.slice(install, assets), /- name: Install publication dependencies\n        run: npm ci/)
})

test('Production publishes once through the signed-authoritative helper', () => {
  assert.match(production, /node scripts\/publish-r2\.mjs publish --version "\$RELEASE_VERSION" --assets "\$RELEASE_ASSETS_DIR"/)
  assert.deepEqual(
    [...production.matchAll(/node scripts\/publish-r2\.mjs (publish|verify|rollback)/g)].map((match) => match[1]),
    ['publish'],
  )
  assert.doesNotMatch(production, /channels\/stable\.json|SNAPIFACT_R2_PUBLIC_ORIGIN|mutation_succeeded|Compensate/)
})

test('Production installer smoke is credential-free and non-blocking', () => {
  const smokeStart = production.indexOf('      - name: Canonical installer smoke')
  assert.ok(smokeStart >= 0)
  const smoke = production.slice(smokeStart)
  assert.match(smoke, /https:\/\/snapifact\.dev\/downloads\/\$RELEASE_VERSION\/install\.sh/)
  assert.match(smoke, /SNAPIFACT_INSTALL_DIR/)
  assert.match(smoke, /--version/)
  assert.match(smoke, /continue-on-error: true/)
  assert.doesNotMatch(smoke, /SNAPIFACT_R2_|r2-release-production|go install|Compensate/)
})

test('removed public binding, diagnostic, and workflow compensation stay absent', () => {
  assert.doesNotMatch(workflow, /SNAPIFACT_R2_PUBLIC_ORIGIN|diagnostic|Compensate to previously verified stable release|mutation_succeeded/)
})

test('Production finalization is success-gated, contents-write-only, and identity-only', () => {
  assert.match(finalizer, /^    needs: \[publish-production, publish\]$/m)
  assert.match(finalizer, /if: \$\{\{ needs\.publish-production\.result == 'success' \}\}/)
  assert.match(finalizer, /permissions:\n      contents: write/)
  assert.doesNotMatch(finalizer, /actions: write|always\(\)|SNAPIFACT_R2_|r2-release-production/)
  assert.match(finalizer, /\.draft == true/)
  assert.doesNotMatch(finalizer, /\.isDraft/)
  assert.match(finalizer, /tag.*GITHUB_SHA|GITHUB_SHA.*tag/)
  for (const asset of assets) assert.match(finalizer, new RegExp(`"${asset}"`))
  assert.doesNotMatch(finalizer, /gh release download|sha256sum|validate-release|EXPECTED_DIGEST_SET|RELEASE_ASSETS_DIR/)
  assert.match(finalizer, /gh release edit "\$RELEASE_VERSION".*--draft=false/)
})

test('all third-party release actions use reviewed full commit pins', () => {
  const uses = [...workflow.matchAll(/^\s+- uses: ([^\s]+)$/gm)].map((match) => match[1])
  assert.ok(uses.length > 0)
  for (const action of uses) assert.match(action, /^[^@]+@[0-9a-f]{40}$/)
})

test('tag publication uploads one immutable six-file provenance artifact after draft revalidation', () => {
  assert.match(publish, /permissions:\n      contents: write\n      actions: write/)
  const revalidation = publish.indexOf('Download and revalidate exact draft assets')
  const upload = publish.indexOf('actions\/upload-artifact@')
  assert.ok(revalidation >= 0)
  assert.ok(upload > revalidation)
  assert.match(publish, /id: draft_assets/)
  assert.match(publish, /name: release-provenance-\$\{\{ github\.ref_name \}\}/)
  assert.match(publish, /path: \$\{\{ steps\.draft_assets\.outputs\.directory \}\}/)
  assert.match(publish, /if-no-files-found: error/)
  assert.match(publish, /steps\.provenance_artifact\.outputs\.artifact-id/)
  assert.match(publish, /steps\.provenance_artifact\.outputs\.artifact-digest/)
  assert.match(publish, /tag SHA|tag_sha/i)
  assert.match(publish, /digest_set/)
})

test('legacy backfill is literal, owner/main-only, and actions-write-only for v0.2.3', () => {
  assert.match(backfill, /^    needs: verify$/m)
  assert.match(backfill, /if: \$\{\{ github\.event_name == 'workflow_dispatch' && github\.ref == 'refs\/heads\/main' && github\.actor == github\.repository_owner && inputs\.operation == 'legacy-backfill' && inputs\.version == 'v0\.2\.3' && needs\.verify\.result == 'success' \}\}/)
  assert.match(backfill, /permissions:\n      contents: read\n      actions: write/)
  assert.match(backfill, /ref: \$\{\{ github\.sha \}\}/)
  assert.match(backfill, /ref: refs\/tags\/v0\.2\.3/)
  assert.match(backfill, /path: trusted-source/)
  assert.match(backfill, /LEGACY_RECORD: trusted-source\/release-provenance\/v0\.2\.3\.json/)
  assert.ok(backfill.indexOf('ref: refs/tags/v0.2.3') < backfill.indexOf('ref: ${{ github.sha }}'))
  assert.match(backfill, /test -f "\$LEGACY_RECORD"/)
  assert.match(backfill, /jq -r '\.version' "\$LEGACY_RECORD"/)
  assert.match(backfill, /jq -r '\.tag_sha' "\$LEGACY_RECORD"/)
  assert.match(backfill, /8ece01f324f5d6f37a120b5efcbb3796fa6eab6e/)
  assert.match(backfill, /release-provenance\/v0\.2\.3\.json/)
  assert.match(backfill, /build-release\.sh v0\.2\.3/)
  assert.match(backfill, /actions\/upload-artifact@/)
  assert.match(backfill, /name: release-provenance-v0\.2\.3/)
  assert.match(backfill, /if-no-files-found: error/)
  assert.doesNotMatch(backfill, /contents: write|gh release|SNAPIFACT_R2_|r2-release-preview|\bPAT\b|production/i)
  assert.doesNotMatch(backfill, /inputs\.(?:source|sha|artifact|run|digest)/)
})

test('preview jobs independently select one successful non-expired artifact and bind its digest', () => {
  for (const job of [preflight, preview]) {
    assert.match(job, /actions: read/)
    assert.match(job, /actions\/workflows\/release\.yml\/runs\?event=/)
    assert.match(job, /conclusion.*success|status.*success/)
    assert.match(job, /expired.*false/)
    assert.match(job, /length.*1|length == 1/)
    assert.match(job, /artifact-digest|\.digest/)
    assert.match(job, /sha256sum.*archive|artifact_digest/)
    assert.match(job, /unzip/)
    assert.match(job, /validateAssets/)
    assert.match(job, /release-provenance-\$RELEASE_VERSION/)
    assert.match(job, /for asset in snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64 SHA256SUMS install\.sh/)
    assert.match(job, /installer|install\.sh/)
    assert.match(job, /directory=/)
    assert.doesNotMatch(job, /gh release (view|download)/)
  }
  assert.match(preflight, /run_id:|artifact_id:|artifact_digest:/)
  assert.match(preview, /PREFLIGHT_RUN_ID|PREFLIGHT_ARTIFACT_ID|PREFLIGHT_ARTIFACT_DIGEST/)
  assert.match(preview, /tag_sha.*PREFLIGHT_TAG_SHA|PREFLIGHT_TAG_SHA.*tag_sha/)
  assert.match(preview, /digest_set.*PREFLIGHT_DIGEST_SET|PREFLIGHT_DIGEST_SET.*digest_set/)
})

test('legacy provenance record is sanitized and contains the six reviewed digests', async () => {
  const record = JSON.parse(await readFile(new URL('../release-provenance/v0.2.3.json', import.meta.url), 'utf8'))
  assert.deepEqual(record, {
    version: 'v0.2.3',
    tag_sha: '8ece01f324f5d6f37a120b5efcbb3796fa6eab6e',
    artifact_name: 'release-provenance-v0.2.3',
    asset_digests: {
      snapifact_linux_amd64: '76cc21868e36dc81d53f2ddb2bc10404ef864fbcae36c194076dbf536e63ead6',
      snapifact_linux_arm64: '074e298d8819b78313c44a467a69c2a1903af6f87ec38dffee8e6aab34dbed18',
      snapifact_darwin_amd64: 'e8e10dfb732735b07673445002216254d2508ad959e15377d5276669b8a5407b',
      snapifact_darwin_arm64: '65da2521c2fd4e56dc3032fb4eb0cabc76a14d80ecbe57d163b854440626b87a',
      SHA256SUMS: '7af0c5df06bfb9db6e1b799a3a201489fce71786c98894292ee784e71e2728fc',
      'install.sh': 'b5fe812c2c4109d2869cbc658598a91df703d3f587dfd56be6c4d989d8439995',
    },
  })
  assert.doesNotMatch(JSON.stringify(record), /https?:|secret|token|signature|endpoint|body|bytes/i)
})
