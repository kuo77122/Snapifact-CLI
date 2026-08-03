import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createHash } from 'node:crypto'

import {
  BINARY_ASSETS,
  CACHE_CONTROL,
  CONTENT_TYPES,
  RELEASE_ASSETS,
  compareVersions,
  isAllowedKey,
  manifestDigest,
  objectMetadata,
  parseStable,
  parseVersion,
  R2Publisher,
  run,
  sanitizeError,
  stableBytes,
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

function fakeR2() {
  const objects = new Map()
  const signedCalls = []
  const publicCalls = []
  let etagNumber = 0
  let stablePutHook = null
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
    else publicCalls.push({ method, key })
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
      if (headers.get('if-match') && (!object || headers.get('if-match') !== object.etag)) return response(412)
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
    if (!object) return response(404)
    return response(200, object, method === 'GET')
  }

  return {
    objects,
    signedCalls,
    publicCalls,
    setStablePutHook(hook) { stablePutHook = hook },
    setPutFailure(key, status, { persist = false } = {}) { putFailures.set(key, { status, persist }) },
    publisher: new R2Publisher({
      s3Origin: 'https://account.example/bucket',
      publicOrigin: 'https://public.example',
      signedFetch: (url, init) => request(url, init, true),
      publicFetch: (url, init) => request(url, init, false),
    }),
  }
}

function publicPublisher(publicFetch) {
  return new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
    signedFetch: async () => new Response(null, { status: 404 }),
    publicFetch,
  })
}

test('publishes exact objects, refreshes latest in order, and commits stable last', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  const result = await fake.publisher.publish('v0.2.2', directory)

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

test('publishes from signed GET state without requesting the public origin', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  fake.publisher.publicFetch = async () => {
    throw new Error('public origin must not be requested during publication')
  }

  const result = await fake.publisher.publish('v0.2.2', directory)

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

  await fake.publisher.publish('v0.2.2', directory)
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

  await assert.rejects(fake.publisher.publish('v0.2.1', directory), /older than stable/)
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
    fake.publisher.publish('v0.2.3', newDirectory),
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
    fake.publisher.publish('v0.2.3', newDirectory),
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

  const result = await fake.publisher.publish('v0.2.3', directory)
  assert.deepEqual(result, { version: 'v0.2.3', manifest_sha256: inventory.manifest_sha256 })
})

test('preserves the opaque quoted ETag for same-version conditional promotion', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  await fake.publisher.publish('v0.2.2', directory)
  fake.signedCalls.length = 0

  await fake.publisher.publish('v0.2.2', directory)
  const stablePut = fake.signedCalls.find(({ method, key }) => method === 'PUT' && key === 'channels/stable.json')
  assert.match(stablePut.headers.get('if-match'), /^"fake-/)
})

test('rejects malformed stable ETags before sending a conditional write', async () => {
  let signedPuts = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
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

  await fake.publisher.publish('v0.2.2', directory)
  const writes = fake.signedCalls.filter(({ method, key }) => method === 'PUT' && key === 'downloads/v0.2.2/snapifact_linux_amd64')
  assert.equal(writes.length, 1)
  assert.equal(fake.publicCalls.length, 0)
})

test('fails mutable write ambiguity closed when signed read-back is missing', async () => {
  let signedCalls = 0
  let publicCalls = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
    signedFetch: async (_url, init) => {
      signedCalls += 1
      if (init.method === 'PUT') throw new Error('transport')
      return new Response(null, { status: 404 })
    },
    publicFetch: async () => {
      publicCalls += 1
      return new Response(null, { status: 404 })
    },
  })

  await assert.rejects(
    publisher.putLatest('snapifact_linux_amd64', new Uint8Array([1])),
    /mutable object write outcome is unknown/,
  )
  assert.equal(signedCalls, 2)
  assert.equal(publicCalls, 0)
})

test('fails mutable ambiguity closed on signed authorization failure', async () => {
  let signedCalls = 0
  let publicCalls = 0
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
    signedFetch: async (_url, init) => {
      signedCalls += 1
      if (init.method === 'PUT') throw new Error('transport')
      return new Response(null, { status: 403 })
    },
    publicFetch: async () => {
      publicCalls += 1
      return new Response(null, { status: 403 })
    },
  })

  await assert.rejects(
    publisher.putLatest('snapifact_linux_amd64', new Uint8Array([1])),
    /signed GET request failed/,
  )
  assert.equal(signedCalls, 2)
  assert.equal(publicCalls, 0)
})

test('resolves an exact conditional 412 from one matching signed GET', async () => {
  const bytes = new TextEncoder().encode('exact bytes\n')
  const metadata = objectMetadata('install.sh')
  let signedPuts = 0
  const signedMethods = []
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
    signedFetch: async (_url, init) => {
      signedMethods.push(init.method)
      if (init.method === 'PUT') {
        signedPuts += 1
        return new Response(null, { status: 412 })
      }
      return new Response(bytes, { status: 200, headers: { ...metadata, etag: '"existing"' } })
    },
    publicFetch: async () => { throw new Error('public origin must not be requested') },
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
    publicOrigin: 'https://public.example',
    signedFetch: async (_url, init) => {
      if (init.method === 'PUT') {
        signedPuts += 1
        return new Response(null, { status: 503 })
      }
      signedGets += 1
      return new Response(null, { status: 503 })
    },
    publicFetch: async () => { throw new Error('public origin must not be requested') },
  })

  await assert.rejects(publisher.putLatest('install.sh', bytes), /signed GET request failed/)
  assert.equal(signedPuts, 1)
  assert.equal(signedGets, 1)
})

test('compensates a partial latest failure to the previous stable release', async () => {
  const oldDirectory = await createReleaseAssets('v0.2.2')
  const newDirectory = await createReleaseAssets('v0.2.3')
  const fake = fakeR2()
  fake.publisher.publicFetch = async () => {
    throw new Error('public origin must not be requested during compensation')
  }
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

  await assert.rejects(fake.publisher.publish('v0.2.3', newDirectory), /signed object bytes do not match/)
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
  fake.publisher.publicFetch = async () => {
    throw new Error('public origin must not be requested during rollback')
  }
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
  assert.equal(fake.publicCalls.length, 0)
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
    assert.equal(fake.publicCalls.length, 0)
  }
})

test('explicit verify continues to check public exact, latest, and stable delivery', async () => {
  const directory = await createReleaseAssets('v0.2.2')
  const fake = fakeR2()
  await fake.publisher.publish('v0.2.2', directory)
  fake.publicCalls.length = 0

  await fake.publisher.verify('v0.2.2')

  assert.equal(fake.publicCalls.length, RELEASE_ASSETS.length * 2 + 1)
  assert.deepEqual(new Set(fake.publicCalls.map(({ method }) => method)), new Set(['GET']))
  assert.ok(fake.publicCalls.some(({ key }) => key === 'channels/stable.json'))
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

  await assert.rejects(fake.publisher.verifyPublicRelease('v0.2.2'), /installer pin does not match/)
})

test('treats a public stable GET 404 as missing state without a public HEAD', async () => {
  const methods = []
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
    signedFetch: async () => new Response(null, { status: 404 }),
    publicFetch: async (_url, init) => {
      methods.push(init.method)
      return new Response(null, { status: 404 })
    },
  })

  assert.equal(await publisher.readStablePublic(), null)
  assert.deepEqual(methods, ['GET'])
})

test('recovers expected public stable reads from transient statuses and 404 visibility', async () => {
  const stable = stableBytes('v0.2.2', 'a'.repeat(64))
  const responses = [
    new Response(null, { status: 503 }),
    new Response(null, { status: 404 }),
    new Response(stable, {
      status: 200,
      headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': CACHE_CONTROL.mutable },
    }),
  ]
  let calls = 0
  const methods = []
  const publisher = new R2Publisher({
    s3Origin: 'https://account.example/bucket',
    publicOrigin: 'https://public.example',
    signedFetch: async () => new Response(null, { status: 404 }),
    publicFetch: async (_url, init) => {
      methods.push(init.method)
      return responses[calls++]
    },
  })

  const result = await publisher.readStablePublic({ expected: true })

  assert.equal(result.version, 'v0.2.2')
  assert.equal(calls, responses.length)
  assert.deepEqual(methods, ['GET', 'GET', 'GET'])
})

test('retries every approved transient public status', async () => {
  for (const status of [408, 425, 429, 500]) {
    let calls = 0
    const publisher = publicPublisher(async () => {
      calls += 1
      return new Response(null, { status: calls === 1 ? status : 200 })
    })

    const response = await publisher.publicRequest('GET', 'downloads/v0.2.2/install.sh', { retry: true, retry404: true })

    assert.equal(response.status, 200)
    assert.equal(calls, 2)
  }
})

test('recovers expected exact and latest objects after 404 visibility', async () => {
  for (const [key, metadata] of [
    ['downloads/v0.2.2/install.sh', objectMetadata('install.sh')],
    ['downloads/latest/install.sh', objectMetadata('install.sh', { immutable: false })],
  ]) {
    const expected = new TextEncoder().encode(`${key}\n`)
    let calls = 0
    const methods = []
    const publisher = publicPublisher(async (_url, init) => {
      calls += 1
      methods.push(init.method)
      if (calls === 1) return new Response(null, { status: 404 })
      return new Response(expected, { status: 200, headers: metadata })
    })

    const result = await publisher.publicObject(key, expected, metadata, { retry: true, retry404: true })

    assert.deepEqual(result.bytes, expected)
    assert.equal(calls, 2)
    assert.deepEqual(methods, ['GET', 'GET'])
  }
})

test('recovers an expected public read after a request timeout', async () => {
  const stable = stableBytes('v0.2.2', 'a'.repeat(64))
  let calls = 0
  const methods = []
  const publisher = publicPublisher(async (_url, init) => {
    calls += 1
    methods.push(init.method)
    if (calls === 1) {
      return new Promise((_, reject) => init.signal.addEventListener('abort', () => reject(new Error('aborted'))))
    }
    return new Response(stable, {
      status: 200,
      headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': CACHE_CONTROL.mutable },
    })
  })

  const result = await publisher.readStablePublic({ expected: true })

  assert.equal(result.version, 'v0.2.2')
  assert.equal(calls, 2)
  assert.deepEqual(methods, ['GET', 'GET'])
})

test('fails permanent public authorization errors without retrying', async () => {
  for (const status of [401, 403]) {
    let calls = 0
    const methods = []
    const publisher = publicPublisher(async (_url, init) => {
      calls += 1
      methods.push(init.method)
      return new Response(null, { status })
    })

    await assert.rejects(
      publisher.readStablePublic({ expected: true }),
      (error) => {
        assert.match(error.message, new RegExp(`status ${status}, attempt 1`))
        assert.doesNotMatch(error.message, /public\.example|content-type|signature/i)
        return true
      },
    )
    assert.equal(calls, 1)
    assert.deepEqual(methods, ['GET'])
  }
})

test('bounds public retry exhaustion to three attempts with sanitized context', async () => {
  let calls = 0
  const publisher = publicPublisher(async () => {
    calls += 1
    return new Response(null, { status: 503 })
  })

  await assert.rejects(
    publisher.readStablePublic({ expected: true }),
    (error) => {
      assert.match(error.message, /status 503, attempt 3/)
      assert.doesNotMatch(error.message, /public\.example|content-type|signature/i)
      return true
    },
  )
  assert.equal(calls, 3)
})

test('does not retry public metadata or byte mismatches', async () => {
  let metadataCalls = 0
  const metadataMethods = []
  const metadataPublisher = publicPublisher(async (_url, init) => {
    metadataCalls += 1
    metadataMethods.push(init.method)
    return new Response(null, {
      status: 200,
      headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'public, max-age=1' },
    })
  })
  await assert.rejects(metadataPublisher.readStablePublic({ expected: true }), /metadata does not match/)
  assert.equal(metadataCalls, 1)
  assert.deepEqual(metadataMethods, ['GET'])

  let byteCalls = 0
  const byteMethods = []
  const bytePublisher = publicPublisher(async (_url, init) => {
    byteCalls += 1
    byteMethods.push(init.method)
    return new Response(new Uint8Array([2]), { status: 200, headers: { ...objectMetadata('snapifact_linux_amd64') } })
  })
  await assert.rejects(
    bytePublisher.publicObject(
      'downloads/v0.2.2/snapifact_linux_amd64',
      new Uint8Array([1]),
      objectMetadata('snapifact_linux_amd64'),
      { retry: true, retry404: true },
    ),
    /public object bytes do not match/,
  )
  assert.equal(byteCalls, 1)
  assert.deepEqual(byteMethods, ['GET'])
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

test('release workflow keeps draft creation tag-push-only and preview owner-gated', async () => {
  const workflow = await readFile(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8')
  assert.match(workflow, /workflow_dispatch:/)
  assert.match(workflow, /github\.actor == github\.repository_owner/)
  assert.match(workflow, /if: \$\{\{ github\.event_name == 'push' &&/)
  assert.doesNotMatch(workflow, /r2-release-production/)
  assert.match(workflow, /cancel-in-progress: false/)
})
