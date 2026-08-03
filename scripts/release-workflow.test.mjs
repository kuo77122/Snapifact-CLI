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
    /if: \$\{\{ github\.event_name == 'workflow_dispatch' && github\.ref == 'refs\/heads\/main' && github\.actor == github\.repository_owner \}\}/,
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

test('preflight proves exact draft inventory, tag SHA, installer pin, and six digests', () => {
  assert.match(preflight, /^    needs: verify$/m)
  assert.match(preflight, /permissions:\n      contents: read/)
  assert.match(preflight, /jq -r '\.object\.sha'/)
  assert.match(preflight, /git\/tags\/\$tag_sha/)
  assert.match(preflight, /\.isDraft/)
  assert.match(preflight, /expected=\$'SHA256SUMS\\ninstall\.sh/)
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
  assert.match(preview, /permissions:\n      contents: read/)
  assert.match(preview, /rm -rf "\$RELEASE_ASSETS_DIR"/)
  assert.match(preview, /tag_sha" == "\$PREFLIGHT_TAG_SHA"/)
  assert.match(preview, /digest_set" == "\$PREFLIGHT_DIGEST_SET"/)
  assert.match(preview, /printf 'directory=%s\\n'/)
  assert.match(preview, /for asset in snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64 SHA256SUMS install\.sh/)
  assert.doesNotMatch(preview, /gh release download[^\n]*--dir dist/)
})

test('only the final credential-bearing step binds approved Preview values', () => {
  const credentialStart = preview.indexOf('      - name: Publish, verify, matching retry, rollback, and final verify')
  assert.ok(credentialStart >= 0)
  const beforeCredentials = workflow.slice(0, workflow.indexOf('  preview-r2-rehearsal:') + credentialStart)
  const credentialStep = preview.slice(credentialStart)
  for (const reference of [
    'secrets.SNAPIFACT_R2_ACCESS_KEY_ID',
    'secrets.SNAPIFACT_R2_SECRET_ACCESS_KEY',
    'vars.SNAPIFACT_R2_ACCOUNT_ID',
    'vars.SNAPIFACT_R2_BUCKET',
    'vars.SNAPIFACT_R2_PUBLIC_ORIGIN',
  ]) {
    assert.equal((workflow.match(new RegExp(reference.replaceAll('.', '\\.'), 'g')) ?? []).length, 1, reference)
    assert.doesNotMatch(beforeCredentials, new RegExp(reference.replaceAll('.', '\\.')))
    assert.match(credentialStep, new RegExp(reference.replaceAll('.', '\\.')))
  }
  assert.deepEqual(
    [...credentialStep.matchAll(/node scripts\/publish-r2\.mjs (publish|verify|rollback)/g)].map((match) => match[1]),
    ['publish', 'verify', 'publish', 'verify', 'rollback', 'verify'],
  )
  assert.equal((credentialStep.match(/--assets "\$RELEASE_ASSETS_DIR"/g) ?? []).length, 2)
})

test('ordinary verification and manual dispatch cannot create releases or resolve R2 credentials', () => {
  assert.match(publish, /if: \$\{\{ github\.event_name == 'push'/)
  assert.doesNotMatch(publish, /workflow_dispatch/)
  assert.doesNotMatch(verify, /SNAPIFACT_R2_|publish-r2\.mjs/)
  assert.doesNotMatch(workflow, /r2-release-production|production/i)
  assert.doesNotMatch(workflow, /inputs\.(?:environment|bucket|public_origin)/)
  assert.doesNotMatch(workflow, /gh release (edit|delete)|\b(delete|list)\b/i)
  assert.doesNotMatch(workflow, /Authorization|signature|private endpoint/i)
})

test('all third-party release actions use reviewed full commit pins', () => {
  const uses = [...workflow.matchAll(/^\s+- uses: ([^\s]+)$/gm)].map((match) => match[1])
  assert.ok(uses.length > 0)
  for (const action of uses) assert.match(action, /^[^@]+@[0-9a-f]{40}$/)
})
