import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createHash } from 'node:crypto'

import {
  BINARY_ASSETS,
  CACHE_CONTROL,
  COMMANDS,
  CONTENT_TYPES,
  createPublisherFromEnv,
  RELEASE_ASSETS,
  compareVersions,
  isAllowedKey,
  manifestDigest,
  objectMetadata,
  parseStable,
  parseVersion,
  R2Publisher,
  run,
  parseArguments,
  sanitizeError,
  stableBytes,
  STABLE_CONTENT_TYPE,
  validateAssets,
} from './publish-r2.mjs'

test('accepts only canonical core SemVer and compares numeric components', () => {
  const valid = ['v0.0.0', 'v0.2.2', 'v12.34.56']
  const invalid = ['0.2.2', 'v01.2.3', 'v1.2', 'v1.2.3-beta.1', 'v1.2.3+build']

  for (const version of valid) assert.deepEqual(parseVersion(version), version.slice(1).split('.').map(Number))
  for (const version of invalid) assert.throws(() => parseVersion(version))
  assert.equal(compareVersions('v1.2.3', 'v1.2.4'), -1)
  assert.equal(compareVersions('v1.2.3', 'v1.2.3'), 0)
  assert.equal(compareVersions('v2.0.0', 'v1.99.99'), 1)
})

test('uses canonical stable bytes and validates the stable schema', () => {
  const digest = 'a'.repeat(64)
  const bytes = stableBytes('v0.2.2', digest)

  assert.equal(new TextDecoder().decode(bytes), `{"version":"v0.2.2","manifest_sha256":"${digest}"}\n`)
  assert.deepEqual(parseStable(bytes), { version: 'v0.2.2', manifest_sha256: digest })
  assert.equal(manifestDigest(new TextEncoder().encode('manifest\n')), '7021e610a5f62eefd01830fea68e5fa180e8cf017c08ea0890c326b2854ebc96')
})

test('publishes only fixed object names with fixed metadata', () => {
  assert.deepEqual(RELEASE_ASSETS, [
    'snapifact_linux_amd64',
    'snapifact_linux_arm64',
    'snapifact_darwin_amd64',
    'snapifact_darwin_arm64',
    'SHA256SUMS',
    'install.sh',
  ])
  assert.deepEqual(CONTENT_TYPES, {
    snapifact_linux_amd64: 'application/octet-stream',
    snapifact_linux_arm64: 'application/octet-stream',
    snapifact_darwin_amd64: 'application/octet-stream',
    snapifact_darwin_arm64: 'application/octet-stream',
    SHA256SUMS: 'text/plain; charset=utf-8',
    'install.sh': 'text/x-shellscript; charset=utf-8',
  })
  assert.equal(CACHE_CONTROL.immutable, 'public, max-age=31536000, immutable')
  assert.equal(CACHE_CONTROL.mutable, 'no-store')
  assert.equal(isAllowedKey('downloads/v1.2.3/install.sh'), true)
  assert.equal(isAllowedKey('downloads/v1.2.3/secret.txt'), false)
  assert.equal(isAllowedKey('downloads/v1.2.3/../secret.txt'), false)
  assert.equal(isAllowedKey('channels/stable.json'), true)
})

test('exposes only the three supported commands', () => {
  assert.deepEqual(COMMANDS, ['publish', 'verify', 'rollback'])
  assert.deepEqual(parseArguments(['publish', '--version', 'v0.2.2', '--assets', 'dist']), {
    command: 'publish', version: 'v0.2.2', assets: 'dist',
  })
})

test('constructs the signed publisher without public-origin configuration', () => {
  assert.doesNotThrow(() => createPublisherFromEnv({
    SNAPIFACT_R2_ACCESS_KEY_ID: 'key',
    SNAPIFACT_R2_SECRET_ACCESS_KEY: 'secret',
    SNAPIFACT_R2_ACCOUNT_ID: 'account',
    SNAPIFACT_R2_BUCKET: 'bucket',
  }))
})

test('validates the exact six-file release inventory before constructing a client', async () => {
  const assetsDir = await mkdtemp(join(tmpdir(), 'snapifact-r2-assets-'))
  const contents = {
    snapifact_linux_amd64: 'linux amd64',
    snapifact_linux_arm64: 'linux arm64',
    snapifact_darwin_amd64: 'darwin amd64',
    snapifact_darwin_arm64: 'darwin arm64',
    'install.sh': "default_download_base='https://snapifact.dev/downloads'\ndefault_version='v0.2.2'\n",
  }
  const lines = []
  for (const name of Object.keys(contents)) {
    await writeFile(join(assetsDir, name), contents[name])
    lines.push(`${'0'.repeat(64)}  ${name}`)
  }
  await writeFile(join(assetsDir, 'SHA256SUMS'), `${lines.join('\n')}\n`)

  await assert.rejects(validateAssets(assetsDir, 'v0.2.2'), /checksum mismatch/)
  await mkdir(join(assetsDir, 'unexpected'))
})

async function createReleaseAssets(version) {
  const directory = await mkdtemp(join(tmpdir(), 'snapifact-r2-release-'))
  const assets = {}
  for (const name of RELEASE_ASSETS.filter((asset) => asset !== 'SHA256SUMS')) {
    assets[name] = name === 'install.sh'
      ? `default_download_base='https://snapifact.dev/downloads'\ndefault_version='${version}'\n`
      : `${version}:${name}\n`
    await writeFile(join(directory, name), assets[name])
  }
  const manifest = RELEASE_ASSETS
    .filter((asset) => asset !== 'SHA256SUMS')
    .map((asset) => `${createHash('sha256').update(assets[asset]).digest('hex')}  ${asset}`)
    .join('\n') + '\n'
  await writeFile(join(directory, 'SHA256SUMS'), manifest)
  return directory
}

async function publishDirectory(publisher, version, directory) {
  return publisher.publish(version, await validateAssets(directory, version))
}

function fakeR2() {
  const objects = new Map()
  const signedCalls = []
  let etagNumber = 0
  let stablePutHook = null
  let stableProofHook = null
  const putFailures = new Map()

  function keyFrom(url) {
    const pathname = new URL(url).pathname.replace(/^\/+/, '')
    return decodeURIComponent(pathname.replace(/^bucket\//, ''))
  }

  function response(status, object, includeBody) {
    return new Response(includeBody && object ? object.bytes : null, {
      status,
      headers: object ? { ...object.headers, etag: object.etag } : undefined,
    })
  }

  async function request(url, init, signed) {
    const method = init?.method ?? 'GET'
    const key = keyFrom(url)
    if (signed) signedCalls.push({ method, key, headers: new Headers(init?.headers), body: init?.body })
    const object = objects.get(key)
    if (method === 'PUT') {
      if (key === 'channels/stable.json' && stablePutHook) return stablePutHook({ key, init, object })
      const failure = putFailures.get(key)
      if (failure && !failure.persist) {
        putFailures.delete(key)
        return response(failure.status)
      }
      const headers = new Headers(init?.headers)
      if (headers.get('if-none-match') === '*' && object) return response(412)
      const candidate = headers.get('if-match')
      if (candidate && (!object || (candidate !== object.etag && !(key === 'channels/stable.json' && `W/${candidate}` === object.etag)))) return response(412)
      const body = new Uint8Array(init.body)
      const stored = {
        bytes: body,
        headers: {
          'content-type': headers.get('content-type'),
          'cache-control': headers.get('cache-control'),
        },
        etag: `"fake-${++etagNumber}"`,
      }
      objects.set(key, stored)
      if (failure) return response(failure.status, stored)
      return response(200, stored)
    }
    if (method === 'GET' && key === 'channels/stable.json' && new Headers(init?.headers).has('if-match')) {
      if (stableProofHook) return stableProofHook({ key, init, object })
      const headers = new Headers(init?.headers)
      const candidate = headers.get('if-match')
      if (!object || (candidate !== object.etag && `W/${candidate}` !== object.etag)) return response(412)
    }
    if (!object) return response(404)
    return response(200, object, method === 'GET')
  }

  return {
    objects,
    signedCalls,
    setStablePutHook(hook) { stablePutHook = hook },
    setStableProofHook(hook) { stableProofHook = hook },
    setPutFailure(key, status, { persist = false } = {}) { putFailures.set(key, { status, persist }) },
    publisher: new R2Publisher({
      s3Origin: 'https://account.example/bucket',
      signedFetch: (url, init) => request(url, init, true),
    }),
  }
}

test('publishes exact objects, refreshes latest in order, and commits stable last', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  const result = await publishDirectory(fake.publisher, 'v0.2.2', directory)

  assert.equal(result.version, 'v0.2.2')
  const puts = fake.signedCalls.filter(({ method }) => method === 'PUT')
  assert.deepEqual(puts.slice(0, 6).map(({ key }) => key), RELEASE_ASSETS.map((asset) => `downloads/v0.2.2/${asset}`))
  assert.deepEqual(puts.slice(6, 12).map(({ key }) => key), [
    ...BINARY_ASSETS.map((asset) => `downloads/latest/${asset}`),
    'downloads/latest/SHA256SUMS',
    'downloads/latest/install.sh',
  ])
  assert.equal(puts[12].key, 'channels/stable.json')
  assert.equal(puts[0].headers.get('if-none-match'), '*')
  assert.equal(puts[0].headers.get('cache-control'), CACHE_CONTROL.immutable)
  assert.equal(puts[6].headers.get('cache-control'), CACHE_CONTROL.mutable)
  assert.equal(puts[12].headers.get('if-none-match'), '*')
})

test('proves a weak stable ETag before mutation and reuses its exact candidate', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const newDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.2', old.manifest_sha256),
    headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
    etag: 'W/"old-stable"',
  })

  await publishDirectory(fake.publisher, 'v0.2.3', newDirectory)

  const stableGets = fake.signedCalls.filter(({ method, key }) => method === 'GET' && key === 'channels/stable.json')
  const firstMutation = fake.signedCalls.findIndex(({ method }) => method === 'PUT')
  assert.equal(stableGets.length >= 2, true)
  assert.equal(stableGets[1].headers.get('if-match'), '"old-stable"')
  assert.equal(fake.signedCalls.indexOf(stableGets[1]) < firstMutation, true)
  const stablePut = fake.signedCalls.find(({ method, key }) => method === 'PUT' && key === 'channels/stable.json')
  assert.equal(stablePut.headers.get('if-match'), '"old-stable"')
})

test('stops before mutation when weak stable ETag proof fails', async () => {
  const scenarios = [
    ['404', () => new Response(null, { status: 404 }), 'not-found'],
    ['412', () => new Response(null, { status: 412 }), 'precondition-failed'],
    ['error', () => { throw new Error('private transport detail') }, 'request-failed'],
    ['metadata', ({ object }) => new Response(object.bytes, {
      status: 200,
      headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': 'public, max-age=1' },
    }), 'metadata-mismatch'],
    ['schema', ({ object }) => new Response(new TextEncoder().encode('{"version":"v0.2.2"}\n'), {
      status: 200,
      headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
    }), 'schema-mismatch'],
    ['bytes', ({ object }) => new Response(stableBytes('v0.2.3', 'b'.repeat(64)), {
      status: 200,
      headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
    }), 'body-mismatch'],
  ]

  for (const [name, proof, classification] of scenarios) {
    const oldDirectory = await createReleaseAssets('v0.2.2')
    const newDirectory = await createReleaseAssets('v0.2.3')
    const fake = fakeR2()
    const old = await validateAssets(oldDirectory, 'v0.2.2')
    fake.objects.set('channels/stable.json', {
      bytes: stableBytes('v0.2.2', old.manifest_sha256),
      headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
      etag: 'W/"old-stable"',
    })
    fake.setStableProofHook(proof)

    await assert.rejects(
      publishDirectory(fake.publisher, 'v0.2.3', newDirectory),
      (error) => {
        assert.equal(error.code, 'invalid-precondition', name)
        assert.equal(error.diagnostics.proof, classification, name)
        assert.doesNotMatch(sanitizeError(error), /private transport detail|old-stable/)
        return true
      },
    )
    assert.equal(fake.signedCalls.some(({ method }) => method === 'PUT'), false, name)
    assert.deepEqual(
      fake.signedCalls.filter(({ method, key }) => method === 'GET' && key === 'channels/stable.json').map(({ headers }) => headers.get('if-match')),
      [null, '"old-stable"'],
      name,
    )
  }
})

test('rejects unsupported stable ETag shapes before any publication mutation', async () => {
  for (const etag of ['bare-value', '"a", "b"', 'W/bare-value', 'W/""', '']) {
    const oldDirectory = await createReleaseAssets('v0.2.2')
    const newDirectory = await createReleaseAssets('v0.2.3')
    const fake = fakeR2()
    const old = await validateAssets(oldDirectory, 'v0.2.2')
    fake.objects.set('channels/stable.json', {
      bytes: stableBytes('v0.2.2', old.manifest_sha256),
      headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
      etag,
    })

    await assert.rejects(publishDirectory(fake.publisher, 'v0.2.3', newDirectory), (error) => {
      assert.ok(error.code === 'invalid-precondition' || error.code === 'invalid-stable')
      assert.doesNotMatch(sanitizeError(error), /bare-value|private/)
      return true
    })
    assert.equal(fake.signedCalls.some(({ method }) => method === 'PUT'), false, etag)
    assert.equal(fake.signedCalls.filter(({ method, key }) => method === 'GET' && key === 'channels/stable.json').length, 1, etag)
  }
})

test('routes a post-proof weak stable 412 through compensation without retrying stable PUT', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const newDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  for (const asset of RELEASE_ASSETS) {
    fake.objects.set(`downloads/v0.2.2/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
      etag: `"old-${asset}"`,
    })
    fake.objects.set(`downloads/latest/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.mutable },
      etag: `"latest-${asset}"`,
    })
  }
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.2', old.manifest_sha256),
    headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
    etag: 'W/"old-stable"',
  })
  fake.setStablePutHook(() => {
    fake.objects.set('channels/stable.json', {
      bytes: stableBytes('v0.2.2', old.manifest_sha256),
      headers: { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable },
      etag: '"racing-publisher"',
    })
    return new Response(null, { status: 412 })
  })

  await assert.rejects(publishDirectory(fake.publisher, 'v0.2.3', newDirectory), /stable index changed/)
  const stableCalls = fake.signedCalls.filter(({ key }) => key === 'channels/stable.json')
  assert.equal(stableCalls.filter(({ method, headers }) => method === 'GET' && headers.get('if-match') === '"old-stable"').length, 1)
  assert.equal(stableCalls.filter(({ method }) => method === 'PUT').length, 1)
  assert.equal(stableCalls.find(({ method }) => method === 'PUT').headers.get('if-match'), '"old-stable"')
  assert.deepEqual(fake.objects.get('channels/stable.json').bytes, stableBytes('v0.2.2', old.manifest_sha256))
})

test('publishes from signed GET state', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()

  const result = await publishDirectory(fake.publisher, 'v0.2.2', directory)

  assert.equal(result.version, 'v0.2.2')
  assert.ok(fake.signedCalls.some(({ method }) => method === 'GET'))
})

test('accepts a matching exact-object retry after a conditional conflict without overwriting it', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  const prepared = await validateAssets(directory, 'v0.2.2')
  fake.objects.set('downloads/v0.2.2/snapifact_linux_amd64', {
    bytes: prepared.assets.snapifact_linux_amd64,
    headers: { 'content-type': CONTENT_TYPES.snapifact_linux_amd64, 'cache-control': CACHE_CONTROL.immutable },
    etag: '"existing"',
  })

  await publishDirectory(fake.publisher, 'v0.2.2', directory)
  const firstPut = fake.signedCalls.find(({ method, key }) => method === 'PUT' && key.endsWith('/snapifact_linux_amd64'))
  assert.equal(firstPut.headers.get('if-none-match'), '*')
  assert.deepEqual(fake.objects.get('downloads/v0.2.2/snapifact_linux_amd64').bytes, prepared.assets.snapifact_linux_amd64)
})

test('rejects a lower version before any write', async () => {
  const directory = await createReleaseAssets('v0.2.1')
  const fake = fakeR2()
  const stable = stableBytes('v0.2.2', 'a'.repeat(64))
  fake.objects.set('channels/stable.json', {
    bytes: stable,
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' },
    etag: '"stable"',
  })

  await assert.rejects(publishDirectory(fake.publisher, 'v0.2.1', directory), /older than stable/)
  assert.equal(fake.signedCalls.some(({ method }) => method === 'PUT'), false)
})

test('compensates latest to the observed stable release after a stale stable conflict', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const newDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  for (const asset of RELEASE_ASSETS) {
    fake.objects.set(`downloads/v0.2.2/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
      etag: `"old-${asset}"`,
    })
    fake.objects.set(`downloads/latest/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.mutable },
      etag: `"latest-${asset}"`,
    })
  }
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.2', old.manifest_sha256),
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' },
    etag: '"old-stable"',
  })
  fake.setStablePutHook(() => new Response(null, { status: 412 }))

  await assert.rejects(
    publishDirectory(fake.publisher, 'v0.2.3', newDirectory),
    (error) => {
      assert.match(error.message, /stable index changed/)
      assert.equal(error.diagnostics.classification, 'state-changed')
      assert.doesNotMatch(sanitizeError(error), /old-stable/)
      return true
    },
  )
  for (const asset of RELEASE_ASSETS) {
    assert.deepEqual(fake.objects.get(`downloads/latest/${asset}`).bytes, old.assets[asset])
  }
})

test('restores latest from trusted prior state when stable 412 readback is invalid', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const newDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  for (const asset of RELEASE_ASSETS) {
    fake.objects.set(`downloads/v0.2.2/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
      etag: `"old-${asset}"`,
    })
    fake.objects.set(`downloads/latest/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.mutable },
      etag: `"latest-${asset}"`,
    })
  }
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.2', old.manifest_sha256),
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': CACHE_CONTROL.mutable },
    etag: '"old-stable"',
  })
  fake.setStablePutHook(() => {
    fake.objects.set('channels/stable.json', {
      bytes: new TextEncoder().encode('{not-json}\n'),
      headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': CACHE_CONTROL.mutable },
      etag: '"unreadable-stable"',
    })
    return new Response(null, { status: 412 })
  })

  await assert.rejects(
    publishDirectory(fake.publisher, 'v0.2.3', newDirectory),
    (error) => {
      assert.match(error.message, /compensation did not converge/)
      assert.equal(error.diagnostics.classification, 'readback-unavailable')
      assert.doesNotMatch(sanitizeError(error), /unreadable-stable/)
      return true
    },
  )
  for (const asset of RELEASE_ASSETS) {
    assert.deepEqual(fake.objects.get(`downloads/latest/${asset}`).bytes, old.assets[asset])
  }
})

test('accepts a stable 412 when a concurrent publisher committed the same canonical state', async () => {
  const directory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const inventory = await validateAssets(directory, 'v0.2.3')
  fake.setStablePutHook(({ init }) => {
    const bytes = new Uint8Array(init.body)
    fake.objects.set('channels/stable.json', {
      bytes,
      headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': CACHE_CONTROL.mutable },
      etag: '"racing-publisher"',
    })
    return new Response(null, { status: 412 })
  })

  const result = await publishDirectory(fake.publisher, 'v0.2.3', directory)
  assert.deepEqual(result, { version: 'v0.2.3', manifest_sha256: inventory.manifest_sha256 })
})

test('preserves the opaque quoted ETag for same-version conditional promotion', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  await publishDirectory(fake.publisher, 'v0.2.2', directory)
  fake.signedCalls.length = 0

  await publishDirectory(fake.publisher, 'v0.2.2', directory)
  const stablePut = fake.signedCalls.find(({ method, key }) => method === 'PUT' && key === 'channels/stable.json')
  assert.match(stablePut.headers.get('if-match'), /^"fake-/)
})

test('rejects malformed stable ETags before sending a conditional write', async () => {
  let signedPuts = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    signedFetch: async (_url, init) => {
      if (init.method === 'PUT') signedPuts += 1
      return new Response(null, { status: 200 })
    },
  })
  const current = { version: 'v0.2.2', manifest_sha256: 'a'.repeat(64), etag: 'unquoted-secret' }
  const stable = { version: 'v0.2.3', manifest_sha256: 'b'.repeat(64), bytes: stableBytes('v0.2.3', 'b'.repeat(64)) }

  await assert.rejects(
    publisher.writeStable(stable, current),
    (error) => {
      assert.equal(error.code, 'invalid-precondition')
      assert.equal(error.diagnostics.precondition.quoted, false)
      assert.doesNotMatch(sanitizeError(error), /unquoted-secret/)
      return true
    },
  )
  assert.equal(signedPuts, 0)
})

test('resolves a conditional exact-write 5xx by matching signed metadata and bytes', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  fake.setPutFailure('downloads/v0.2.2/snapifact_linux_amd64', 503, { persist: true })

  await publishDirectory(fake.publisher, 'v0.2.2', directory)
  const writes = fake.signedCalls.filter(({ method, key }) => method === 'PUT' && key === 'downloads/v0.2.2/snapifact_linux_amd64')
  assert.equal(writes.length, 1)
})

test('fails mutable write ambiguity closed when signed read-back is missing', async () => {
  let signedCalls = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    signedFetch: async (_url, init) => {
      signedCalls += 1
      if (init.method === 'PUT') throw new Error('transport')
      return new Response(null, { status: 404 })
    },
  })

  await assert.rejects(
    publisher.putLatest('snapifact_linux_amd64', new Uint8Array([1])),
    /mutable object write outcome is unknown/,
  )
  assert.equal(signedCalls, 2)
})

test('fails mutable ambiguity closed on signed authorization failure', async () => {
  let signedCalls = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    signedFetch: async (_url, init) => {
      signedCalls += 1
      if (init.method === 'PUT') throw new Error('transport')
      return new Response(null, { status: 403 })
    },
  })

  await assert.rejects(
    publisher.putLatest('snapifact_linux_amd64', new Uint8Array([1])),
    /signed GET request failed/,
  )
  assert.equal(signedCalls, 2)
})

test('resolves an exact conditional 412 from one matching signed GET', async () => {
  const bytes = new TextEncoder().encode('exact bytes\n')
  const metadata = objectMetadata('install.sh')
  let signedPuts = 0
  const signedMethods = []
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    signedFetch: async (_url, init) => {
      signedMethods.push(init.method)
      if (init.method === 'PUT') {
        signedPuts += 1
        return new Response(null, { status: 412 })
      }
      return new Response(bytes, { status: 200, headers: { ...metadata, etag: '"existing"' } })
    },
  })

  const result = await publisher.putExact('downloads/v0.2.2/install.sh', bytes, 'install.sh')

  assert.deepEqual(result.bytes, bytes)
  assert.equal(signedPuts, 1)
  assert.deepEqual(signedMethods, ['PUT', 'GET'])
})

test('fails mutable ambiguity closed on a signed read-back 5xx', async () => {
  const bytes = new TextEncoder().encode('latest bytes\n')
  const metadata = objectMetadata('install.sh', { immutable: false })
  let signedPuts = 0
  let signedGets = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    signedFetch: async (_url, init) => {
      if (init.method === 'PUT') {
        signedPuts += 1
        return new Response(null, { status: 503 })
      }
      signedGets += 1
      return new Response(null, { status: 503 })
    },
  })

  await assert.rejects(publisher.putLatest('install.sh', bytes), /signed GET request failed/)
  assert.equal(signedPuts, 1)
  assert.equal(signedGets, 1)
})

test('compensates a partial latest failure to the previous stable release', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const newDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  for (const asset of RELEASE_ASSETS) {
    fake.objects.set(`downloads/v0.2.2/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
      etag: `"old-${asset}"`,
    })
    fake.objects.set(`downloads/latest/${asset}`, {
      bytes: old.assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.mutable },
      etag: `"latest-${asset}"`,
    })
  }
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.2', old.manifest_sha256),
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' },
    etag: '"old-stable"',
  })
  fake.setPutFailure('downloads/latest/install.sh', 503)

  await assert.rejects(publishDirectory(fake.publisher, 'v0.2.3', newDirectory), /signed object bytes do not match/)
  for (const asset of RELEASE_ASSETS) {
    assert.deepEqual(fake.objects.get(`downloads/latest/${asset}`).bytes, old.assets[asset])
  }
})

test('rollback promotes an already verified immutable release without replacing exact history', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const currentDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  const current = await validateAssets(currentDirectory, 'v0.2.3')
  for (const [version, inventory] of [['v0.2.2', old], ['v0.2.3', current]]) {
    for (const asset of RELEASE_ASSETS) {
      fake.objects.set(`downloads/${version}/${asset}`, {
        bytes: inventory.assets[asset],
        headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
        etag: `"${version}-${asset}"`,
      })
    }
  }
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.3', current.manifest_sha256),
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' },
    etag: '"current-stable"',
  })

  await fake.publisher.rollback('v0.2.2')
  assert.deepEqual(fake.objects.get('channels/stable.json').bytes, stableBytes('v0.2.2', old.manifest_sha256))
  assert.deepEqual(fake.objects.get('downloads/v0.2.3/install.sh').bytes, current.assets['install.sh'])
})

test('rollback shares stable 412 compensation for a different trusted target', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const currentDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  const old = await validateAssets(oldDirectory, 'v0.2.2')
  const current = await validateAssets(currentDirectory, 'v0.2.3')
  for (const [version, inventory] of [['v0.2.2', old], ['v0.2.3', current]]) {
    for (const asset of RELEASE_ASSETS) {
      fake.objects.set(`downloads/${version}/${asset}`, {
        bytes: inventory.assets[asset],
        headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
        etag: `"${version}-${asset}"`,
      })
    }
  }
  fake.objects.set('channels/stable.json', {
    bytes: stableBytes('v0.2.3', current.manifest_sha256),
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': CACHE_CONTROL.mutable },
    etag: '"current-stable"',
  })
  fake.setStablePutHook(() => new Response(null, { status: 412 }))

  await assert.rejects(
    fake.publisher.rollback('v0.2.2'),
    (error) => {
      assert.match(error.message, /stable index changed/)
      assert.equal(error.diagnostics.classification, 'state-changed')
      return true
    },
  )
  for (const asset of RELEASE_ASSETS) {
    assert.deepEqual(fake.objects.get(`downloads/latest/${asset}`).bytes, current.assets[asset])
  }
})

test('signed immutable read-back fails closed on metadata, byte, and missing-object mismatches', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const inventory = await validateAssets(directory, 'v0.2.2')

  for (const scenario of ['metadata', 'bytes', 'missing']) {
    const fake = fakeR2()
    for (const asset of RELEASE_ASSETS) {
      fake.objects.set(`downloads/v0.2.2/${asset}`, {
        bytes: inventory.assets[asset],
        headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
        etag: `"${asset}"`,
      })
    }
    const key = 'downloads/v0.2.2/install.sh'
    if (scenario === 'metadata') fake.objects.get(key).headers['cache-control'] = CACHE_CONTROL.mutable
    if (scenario === 'bytes') fake.objects.get(key).bytes = new TextEncoder().encode('tampered\n')
    if (scenario === 'missing') fake.objects.delete(key)

    await assert.rejects(
      fake.publisher.verifySignedRelease('v0.2.2'),
      scenario === 'metadata'
        ? /signed object metadata does not match/
        : scenario === 'bytes'
          ? /immutable release checksum mismatch/
          : /immutable release object is missing/,
    )
  }
})

test('explicit verify checks signed exact, latest, and stable state', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  await publishDirectory(fake.publisher, 'v0.2.2', directory)

  await fake.publisher.verify('v0.2.2')

  assert.ok(fake.signedCalls.some(({ method, key }) => method === 'GET' && key === 'channels/stable.json'))
})

test('rejects an anonymously verified installer whose assignment is not pinned', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  const inventory = await validateAssets(directory, 'v0.2.2')
  const badInstaller = "# default_version='v0.2.2'\ndefault_version='v9.9.9'\n"
  const assets = { ...inventory.assets, 'install.sh': new TextEncoder().encode(badInstaller) }
  const manifest = RELEASE_ASSETS
    .filter((asset) => asset !== 'SHA256SUMS')
    .map((asset) => `${manifestDigest(assets[asset])}  ${asset}`)
    .join('\n') + '\n'
  assets.SHA256SUMS = new TextEncoder().encode(manifest)
  for (const asset of RELEASE_ASSETS) {
    fake.objects.set(`downloads/v0.2.2/${asset}`, {
      bytes: assets[asset],
      headers: { 'content-type': CONTENT_TYPES[asset], 'cache-control': CACHE_CONTROL.immutable },
      etag: `"bad-${asset}"`,
    })
  }

  await assert.rejects(fake.publisher.verifySignedRelease('v0.2.2'), /installer pin does not match/)
})

test('local publish validation fails before a publisher or credential lookup', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'snapifact-r2-invalid-'))
  let factoryCalled = false
  const status = await run([
    'publish', '--version', 'v0.2.2', '--assets', directory,
  ], {
    publisherFactory: () => {
      factoryCalled = true
      throw new Error('must not be called')
    },
  })
  assert.equal(status, 1)
  assert.equal(factoryCalled, false)
})

test('passes the one validated inventory into publication after factory construction', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  let received
  const status = await run([
    'publish', '--version', 'v0.2.2', '--assets', directory,
  ], {
    publisherFactory: () => ({
      publish: async (_version, inventory) => { received = inventory },
    }),
  })

  assert.equal(status, 0)
  assert.equal(received.version, 'v0.2.2')
  assert.equal(received.assets['install.sh'] instanceof Uint8Array, true)
})

test('direct publication rejects an inventory that was not validated by the helper', async () => {
  const fake = fakeR2()
  await assert.rejects(
    fake.publisher.publish('v0.2.2', { version: 'v0.2.2', assets: {} }),
    /validated release inventory/,
  )
  assert.equal(fake.signedCalls.length, 0)
})

test('rejects a coherently mutated validated inventory before any signed request', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const inventory = await validateAssets(directory, 'v0.2.2')
  const mutatedInstaller = new Uint8Array(inventory.assets['install.sh'])
  mutatedInstaller[0] ^= 1
  inventory.assets['install.sh'] = mutatedInstaller
  inventory.manifest.set('install.sh', manifestDigest(mutatedInstaller))
  const manifest = `${RELEASE_ASSETS
    .filter((asset) => asset !== 'SHA256SUMS')
    .map((asset) => `${inventory.manifest.get(asset)}  ${asset}`)
    .join('\n')}\n`
  inventory.assets.SHA256SUMS = new TextEncoder().encode(manifest)
  inventory.manifest_sha256 = manifestDigest(inventory.assets.SHA256SUMS)

  const fake = fakeR2()
  await assert.rejects(
    fake.publisher.publish('v0.2.2', inventory),
    /validated release inventory changed/,
  )
  assert.equal(fake.signedCalls.length, 0)
})

test('rejects validated inventory asset-map key mutations before any signed request', async () => {
  for (const mutation of ['added', 'replaced', 'removed']) {
    const directory = await createReleaseAssets('v0.2.2')
    const inventory = await validateAssets(directory, 'v0.2.2')
    if (mutation === 'added') inventory.assets.extra = new Uint8Array([1])
    if (mutation === 'replaced') inventory.assets = { ...inventory.assets, extra: new Uint8Array([1]) }
    if (mutation === 'removed') delete inventory.assets['install.sh']

    const fake = fakeR2()
    await assert.rejects(
      fake.publisher.publish('v0.2.2', inventory),
      /validated release inventory changed/,
      mutation,
    )
    assert.equal(fake.signedCalls.length, 0, mutation)
  }
})

test('release workflow keeps draft creation tag-push-only and preview owner-gated', async () => {
  const workflow = await readFile(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  assert.match(workflow, /workflow_dispatch:/)
  assert.match(workflow, /github\.actor == github\.repository_owner/)
  assert.match(workflow, /if: \$\{\{ github\.event_name == 'push' &&/)
  assert.match(workflow, /environment: r2-release-production/)
  assert.match(workflow, /if: \$\{\{ github\.event_name == 'push' && github\.ref_type == 'tag' && github\.repository == 'kuo77122\/Snapifact-CLI' && github\.actor == github\.repository_owner && needs\.verify\.result == 'success' && needs\.publish\.result == 'success' && needs\.verify\.outputs\.canonical_tag == 'true' \}\}/)
  assert.doesNotMatch(workflow, /if: \$\{\{ false && github\.event_name == 'push'/)
  assert.doesNotMatch(workflow, /inputs\.(?:environment|bucket|public_origin)/)
  assert.match(workflow, /cancel-in-progress: false/)
})
