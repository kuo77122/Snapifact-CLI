import { createHash } from 'node:crypto'
import { readdir, readFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'

import { AwsClient } from 'aws4fetch'

export const BINARY_ASSETS = Object.freeze([
  'snapifact_linux_amd64',
  'snapifact_linux_arm64',
  'snapifact_darwin_amd64',
  'snapifact_darwin_arm64',
])

export const RELEASE_ASSETS = Object.freeze([...BINARY_ASSETS, 'SHA256SUMS', 'install.sh'])
const CHECKSUM_ASSETS = Object.freeze([...BINARY_ASSETS, 'install.sh'])
const VERSION_PATTERN = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/
const HEX_PATTERN = /^[0-9a-f]{64}$/
const ETAG_PATTERN = /^"(?:[^"\\\r\n]|\\.)+"$/
const validatedInventories = new WeakSet()

export const CONTENT_TYPES = Object.freeze({
  snapifact_linux_amd64: 'application/octet-stream',
  snapifact_linux_arm64: 'application/octet-stream',
  snapifact_darwin_amd64: 'application/octet-stream',
  snapifact_darwin_arm64: 'application/octet-stream',
  SHA256SUMS: 'text/plain; charset=utf-8',
  'install.sh': 'text/x-shellscript; charset=utf-8',
})

export const CACHE_CONTROL = Object.freeze({
  immutable: 'public, max-age=31536000, immutable',
  mutable: 'no-store',
})

export const STABLE_CONTENT_TYPE = 'application/json; charset=utf-8'
export const COMMANDS = Object.freeze(['publish', 'verify', 'rollback'])

export class PublicationError extends Error {
  constructor(code, message, { retryable = false, status = undefined, diagnostics = undefined } = {}) {
    super(message)
    this.name = 'PublicationError'
    this.code = code
    this.retryable = retryable
    this.status = status
    this.diagnostics = diagnostics
  }
}

function fail(code, message, options) {
  throw new PublicationError(code, message, options)
}

export function parseVersion(version) {
  const match = VERSION_PATTERN.exec(version)
  if (!match) fail('invalid-version', 'version must be canonical vMAJOR.MINOR.PATCH')
  return match.slice(1).map(Number)
}

export function compareVersions(left, right) {
  const leftParts = VERSION_PATTERN.exec(left)
  const rightParts = VERSION_PATTERN.exec(right)
  if (!leftParts || !rightParts) fail('invalid-version', 'version must be canonical vMAJOR.MINOR.PATCH')
  for (let i = 1; i <= 3; i += 1) {
    const a = BigInt(leftParts[i])
    const b = BigInt(rightParts[i])
    if (a < b) return -1
    if (a > b) return 1
  }
  return 0
}

export function manifestDigest(bytes) {
  return createHash('sha256').update(bytes).digest('hex')
}

export function stableBytes(version, digest) {
  parseVersion(version)
  if (!HEX_PATTERN.test(digest)) fail('invalid-stable', 'stable manifest digest must be lowercase SHA-256')
  return new TextEncoder().encode(`${JSON.stringify({ version, manifest_sha256: digest })}\n`)
}

export function parseStable(bytes) {
  let text
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    fail('invalid-stable', 'stable index is not valid UTF-8')
  }
  let value
  try {
    value = JSON.parse(text)
  } catch {
    fail('invalid-stable', 'stable index is not valid JSON')
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    fail('invalid-stable', 'stable index must be an object')
  }
  const keys = Object.keys(value).sort()
  if (keys.length !== 2 || keys[0] !== 'manifest_sha256' || keys[1] !== 'version') {
    fail('invalid-stable', 'stable index has an invalid schema')
  }
  if (typeof value.version !== 'string' || typeof value.manifest_sha256 !== 'string') {
    fail('invalid-stable', 'stable index has an invalid schema')
  }
  stableBytes(value.version, value.manifest_sha256)
  if (!bytesEqual(new TextEncoder().encode(text), stableBytes(value.version, value.manifest_sha256))) {
    fail('invalid-stable', 'stable index is not canonical')
  }
  return value
}

export function keyForVersion(version, asset) {
  parseVersion(version)
  if (!RELEASE_ASSETS.includes(asset)) fail('invalid-key', 'asset is not an approved release object')
  return `downloads/${version}/${asset}`
}

export function keyForLatest(asset) {
  if (!RELEASE_ASSETS.includes(asset)) fail('invalid-key', 'asset is not an approved release object')
  return `downloads/latest/${asset}`
}

export function isAllowedKey(key) {
  return /^downloads\/(?:v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)|latest)\/(?:snapifact_(?:linux|darwin)_(?:amd64|arm64)|SHA256SUMS|install\.sh)$/.test(key)
    || key === 'channels/stable.json'
}

export function objectMetadata(asset, { immutable = true } = {}) {
  if (!RELEASE_ASSETS.includes(asset)) fail('invalid-key', 'asset is not an approved release object')
  return {
    'content-type': CONTENT_TYPES[asset],
    'cache-control': immutable ? CACHE_CONTROL.immutable : CACHE_CONTROL.mutable,
  }
}

function stableMetadata() {
  return { 'content-type': STABLE_CONTENT_TYPE, 'cache-control': CACHE_CONTROL.mutable }
}

function bytesEqual(left, right) {
  if (left.byteLength !== right.byteLength) return false
  for (let i = 0; i < left.byteLength; i += 1) if (left[i] !== right[i]) return false
  return true
}

function header(response, name) {
  return response.headers?.get(name) ?? ''
}

function etagDiagnostics(etag) {
  return {
    present: Boolean(etag),
    quoted: typeof etag === 'string' && ETAG_PATTERN.test(etag),
    ...(etag ? { fingerprint: createHash('sha256').update(etag).digest('hex').slice(0, 16) } : {}),
  }
}

function stableProofFailure(classification, status) {
  fail('invalid-precondition', 'stable ETag candidate proof failed', {
    diagnostics: {
      proof: classification,
      ...(status === undefined ? {} : { status }),
    },
  })
}

function stableConflictError(message, status, { classification, precondition, readback } = {}) {
  return new PublicationError('conditional-conflict', message, {
    status,
    diagnostics: {
      classification,
      precondition: etagDiagnostics(precondition?.etag),
      ...(readback ? { readback } : {}),
    },
  })
}

function responseBytes(response) {
  return response.arrayBuffer().then((value) => new Uint8Array(value))
}

function validateMetadata(response, metadata) {
  return header(response, 'content-type') === metadata['content-type']
    && header(response, 'cache-control') === metadata['cache-control']
}

function safeResponseError(operation, response) {
  const retryable = response.status >= 500
  return new PublicationError(
    retryable ? 'ambiguous-response' : 'request-failed',
    `${operation} request failed`,
    { retryable, status: response.status },
  )
}

async function readBytes(path) {
  try {
    return new Uint8Array(await readFile(path))
  } catch {
    fail('invalid-assets', 'release asset could not be read')
  }
}

function decodeUTF8(bytes, message) {
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    fail('invalid-assets', message)
  }
}

function parseManifest(bytes) {
  const text = decodeUTF8(bytes, 'checksum manifest is not valid UTF-8')
  if (!text.endsWith('\n')) fail('invalid-assets', 'checksum manifest must end with a newline')
  const lines = text.slice(0, -1).split('\n')
  if (lines.length !== CHECKSUM_ASSETS.length) fail('invalid-assets', 'checksum manifest has the wrong asset count')
  const entries = new Map()
  for (const line of lines) {
    const match = /^([0-9a-fA-F]{64})  ([^\s]+)$/.exec(line)
    if (!match || !CHECKSUM_ASSETS.includes(match[2]) || entries.has(match[2])) {
      fail('invalid-assets', 'checksum manifest has an invalid entry')
    }
    entries.set(match[2], match[1].toLowerCase())
  }
  for (const asset of CHECKSUM_ASSETS) if (!entries.has(asset)) fail('invalid-assets', 'checksum manifest is missing an asset')
  return entries
}

export async function validateAssets(directory, version) {
  parseVersion(version)
  let entries
  try {
    entries = await readdir(directory, { withFileTypes: true })
  } catch {
    fail('invalid-assets', 'release assets directory could not be read')
  }
  if (entries.length !== RELEASE_ASSETS.length || entries.some((entry) => !entry.isFile() || !RELEASE_ASSETS.includes(entry.name))) {
    fail('invalid-assets', 'release assets must contain exactly the six approved files')
  }
  const assets = {}
  for (const asset of RELEASE_ASSETS) assets[asset] = await readBytes(`${directory}/${asset}`)
  const manifest = parseManifest(assets.SHA256SUMS)
  for (const asset of CHECKSUM_ASSETS) {
    if (manifest.get(asset) !== manifestDigest(assets[asset])) fail('invalid-assets', `checksum mismatch for ${asset}`)
  }
  const installer = decodeUTF8(assets['install.sh'], 'installer is not valid UTF-8')
  const pins = installer.match(/^default_version='([^']*)'$/gm) ?? []
  if (pins.length !== 1 || pins[0] !== `default_version='${version}'`) {
    fail('invalid-assets', 'installer is not pinned to the requested version')
  }
  const inventory = {
    version,
    assets,
    manifest,
    manifest_sha256: manifestDigest(assets.SHA256SUMS),
  }
  validatedInventories.add(inventory)
  return inventory
}

function encodedURL(origin, key) {
  return `${origin.replace(/\/+$/, '')}/${key.split('/').map(encodeURIComponent).join('/')}`
}

function requireEnv(env, name) {
  if (!env[name]) fail('configuration', `missing required configuration: ${name}`)
  return env[name]
}

export function createPublisherFromEnv(env = process.env) {
  const accessKeyId = requireEnv(env, 'SNAPIFACT_R2_ACCESS_KEY_ID')
  const secretAccessKey = requireEnv(env, 'SNAPIFACT_R2_SECRET_ACCESS_KEY')
  const accountId = requireEnv(env, 'SNAPIFACT_R2_ACCOUNT_ID')
  const bucket = requireEnv(env, 'SNAPIFACT_R2_BUCKET')
  const s3Origin = `https://${accountId}.r2.cloudflarestorage.com/${encodeURIComponent(bucket)}`
  const client = new AwsClient({ accessKeyId, secretAccessKey, service: 's3', region: 'auto', retries: 0 })
  return new R2Publisher({
    s3Origin,
    signedFetch: (url, init) => client.fetch(url, init),
  })
}

export class R2Publisher {
  constructor({ s3Origin, signedFetch }) {
    if (!s3Origin || typeof signedFetch !== 'function') {
      fail('configuration', 'publication transport is not configured')
    }
    this.s3Origin = s3Origin.replace(/\/+$/, '')
    this.signedFetch = signedFetch
  }

  signedURL(key) {
    if (key !== 'channels/stable.json' && !isAllowedKey(key)) fail('invalid-key', 'object key is not approved')
    return encodedURL(this.s3Origin, key)
  }

  async signedRequest(method, key, init = {}) {
    try {
      return await this.signedFetch(this.signedURL(key), { ...init, method })
    } catch {
      throw new PublicationError('transport', `${method} request failed`, { retryable: true })
    }
  }

  async signedObjectHead(key) {
    const response = await this.signedRequest('HEAD', key)
    if (response.status === 404) return null
    if (!response.ok) throw safeResponseError('signed HEAD', response)
    return response
  }

  async signedObjectGet(key, expected, metadata, { allowMissing = false } = {}) {
    const get = await this.signedRequest('GET', key)
    if (get.status === 404 && allowMissing) return null
    if (!get.ok) throw safeResponseError('signed GET', get)
    if (!validateMetadata(get, metadata)) fail('metadata-mismatch', 'signed object metadata does not match')
    const bytes = await responseBytes(get)
    if (expected && !bytesEqual(bytes, expected)) fail('content-mismatch', 'signed object bytes do not match')
    return { bytes, get }
  }

  async verifyExactObject(key, expected, asset) {
    const metadata = objectMetadata(asset)
    return this.signedObjectGet(key, expected, metadata, { allowMissing: true })
  }

  async resolveExactAmbiguity(key, expected, asset) {
    const match = await this.verifyExactObject(key, expected, asset)
    if (match) return match
    fail('ambiguous-missing', 'conditional object write outcome is unknown', { retryable: true })
  }

  async putExact(key, bytes, asset) {
    const metadata = objectMetadata(asset)
    let response
    try {
      response = await this.signedRequest('PUT', key, {
        headers: { ...metadata, 'if-none-match': '*' },
        body: bytes,
      })
    } catch (error) {
      return this.resolveExactAmbiguity(key, bytes, asset).catch((readbackError) => {
        if (readbackError instanceof PublicationError) throw readbackError
        throw error
      })
    }
    if (response.ok) return
    if (response.status === 412 || response.status >= 500) return this.resolveExactAmbiguity(key, bytes, asset)
    throw safeResponseError('exact object PUT', response)
  }

  async resolveMutableAmbiguity(key, bytes, asset) {
    const match = await this.signedObjectGet(
      key,
      bytes,
      objectMetadata(asset, { immutable: false }),
      { allowMissing: true },
    )
    if (match) return match
    fail('ambiguous-missing', 'mutable object write outcome is unknown', { retryable: true })
  }

  async putLatest(asset, bytes) {
    const key = keyForLatest(asset)
    const metadata = objectMetadata(asset, { immutable: false })
    let response
    try {
      response = await this.signedRequest('PUT', key, { headers: metadata, body: bytes })
    } catch (error) {
      return this.resolveMutableAmbiguity(key, bytes, asset).catch((readbackError) => {
        if (readbackError instanceof PublicationError) throw readbackError
        throw error
      })
    }
    if (response.ok) return
    if (response.status >= 500) return this.resolveMutableAmbiguity(key, bytes, asset)
    throw safeResponseError('latest object PUT', response)
  }

  async refreshLatest(assets) {
    for (const asset of BINARY_ASSETS) await this.putLatest(asset, assets[asset])
    await this.putLatest('SHA256SUMS', assets.SHA256SUMS)
    await this.putLatest('install.sh', assets['install.sh'])
  }

  async readStableSigned({ allowMissing = false } = {}) {
    const response = await this.signedRequest('GET', 'channels/stable.json')
    if (response.status === 404 && allowMissing) return null
    if (!response.ok) throw safeResponseError('signed stable GET', response)
    if (!validateMetadata(response, stableMetadata())) fail('metadata-mismatch', 'signed stable metadata does not match')
    const bytes = await responseBytes(response)
    return {
      ...parseStable(bytes),
      bytes,
      etag: header(response, 'etag') || null,
    }
  }

  async proveStablePrecondition(current) {
    if (!current) return null
    if (!current.etag) fail('invalid-stable', 'stable index has no usable ETag')
    if (ETAG_PATTERN.test(current.etag)) return current

    const candidate = current.etag.startsWith('W/') ? current.etag.slice(2) : null
    if (!candidate || !ETAG_PATTERN.test(candidate)) {
      fail('invalid-precondition', 'stable index ETag precondition is malformed', {
        diagnostics: { precondition: etagDiagnostics(current.etag) },
      })
    }

    let response
    try {
      response = await this.signedRequest('GET', 'channels/stable.json', {
        headers: { 'if-match': candidate },
      })
    } catch {
      stableProofFailure('request-failed')
    }
    if (!response.ok) {
      stableProofFailure(
        response.status === 404 ? 'not-found' : response.status === 412 ? 'precondition-failed' : 'request-failed',
        response.status,
      )
    }
    if (!validateMetadata(response, stableMetadata())) stableProofFailure('metadata-mismatch', response.status)

    let bytes
    try {
      bytes = await responseBytes(response)
      parseStable(bytes)
    } catch {
      stableProofFailure('schema-mismatch', response.status)
    }
    if (!bytesEqual(bytes, current.bytes)) stableProofFailure('body-mismatch', response.status)
    return { ...current, etag: candidate }
  }

  async writeStable(stable, current, { allowConflict = true } = {}) {
    const headers = stableMetadata()
    if (current) {
      if (!current.etag) fail('invalid-stable', 'stable index has no usable ETag')
      if (!ETAG_PATTERN.test(current.etag)) {
        fail('invalid-precondition', 'stable index ETag precondition is malformed', {
          diagnostics: { precondition: etagDiagnostics(current.etag) },
        })
      }
      headers['if-match'] = current.etag
    } else {
      headers['if-none-match'] = '*'
    }
    let response
    try {
      response = await this.signedRequest('PUT', 'channels/stable.json', { headers, body: stable.bytes })
    } catch {
      return this.resolveStableAmbiguity(stable, current)
    }
    if (response.ok) return
    if (response.status === 412 && allowConflict) {
      throw stableConflictError('stable index conditional precondition failed', response.status, {
        classification: 'precondition-failed',
        precondition: current,
      })
    }
    if (response.status >= 500) return this.resolveStableAmbiguity(stable, current)
    throw safeResponseError('stable PUT', response)
  }

  async resolveStableAmbiguity(stable, current) {
    const actual = await this.readStableSigned({ allowMissing: true })
    if (!actual) fail('ambiguous-missing', 'stable write outcome is unknown', { retryable: true })
    if (bytesEqual(actual.bytes, stable.bytes)) {
      return
    }
    fail('ambiguous-mismatch', 'stable write outcome is mismatched')
  }

  async recoverLatestFromPrior(prior, diagnostics) {
    if (!prior) return
    try {
      await this.restoreLatest(prior)
    } catch {
      fail('compensation-failed', 'latest release compensation did not converge', { diagnostics })
    }
  }

  async resolveStableConflict(stable, prior, conflict) {
    let actual
    try {
      actual = await this.readStableSigned({ allowMissing: true })
    } catch {
      const diagnostics = {
        classification: 'readback-unavailable',
        precondition: etagDiagnostics(prior?.etag),
        readback: 'unavailable',
      }
      await this.recoverLatestFromPrior(prior, diagnostics)
      throw stableConflictError('stable index readback was unavailable after conditional failure', conflict.status, {
        classification: diagnostics.classification,
        precondition: prior,
        readback: diagnostics.readback,
      })
    }
    if (!actual) {
      const diagnostics = {
        classification: 'readback-missing',
        precondition: etagDiagnostics(prior?.etag),
        readback: 'missing',
      }
      await this.recoverLatestFromPrior(prior, diagnostics)
      throw stableConflictError('stable index was missing after conditional failure', conflict.status, {
        classification: diagnostics.classification,
        precondition: prior,
        readback: diagnostics.readback,
      })
    }
    if (bytesEqual(actual.bytes, stable.bytes)) {
      try {
        await this.verifyConvergence(stable)
        return true
      } catch {
        try {
          await this.restoreLatest(actual)
        } catch {
          fail('compensation-failed', 'latest release compensation did not converge')
        }
        throw stableConflictError('stable index reached target but latest did not converge', conflict.status, {
          classification: 'stale-precondition',
          precondition: prior,
          readback: 'same-target',
        })
      }
    }
    try {
      await this.restoreLatest(actual)
    } catch {
      fail('compensation-failed', 'latest release compensation did not converge')
    }
    throw stableConflictError('stable index changed during publication', conflict.status, {
      classification: 'state-changed',
      precondition: prior,
      readback: 'different-target',
    })
  }

  async verifySignedRelease(version) {
    parseVersion(version)
    const assets = {}
    for (const asset of RELEASE_ASSETS) {
      const key = keyForVersion(version, asset)
      const object = await this.signedObjectGet(key, undefined, objectMetadata(asset), { allowMissing: true })
      if (!object) fail('missing-object', 'immutable release object is missing', { retryable: true })
      assets[asset] = object.bytes
    }
    const manifest = parseManifest(assets.SHA256SUMS)
    for (const asset of CHECKSUM_ASSETS) {
      if (manifest.get(asset) !== manifestDigest(assets[asset])) fail('content-mismatch', 'immutable release checksum mismatch')
    }
    const installer = decodeUTF8(assets['install.sh'], 'immutable installer is not valid UTF-8')
    const pins = installer.match(/^default_version='([^']*)'$/gm) ?? []
    if (pins.length !== 1 || pins[0] !== `default_version='${version}'`) {
      fail('content-mismatch', 'immutable installer pin does not match')
    }
    return { version, assets, manifest, manifest_sha256: manifestDigest(assets.SHA256SUMS) }
  }

  async verifyLatestSigned(verified) {
    for (const asset of RELEASE_ASSETS) {
      const object = await this.signedObjectGet(
        keyForLatest(asset),
        verified.assets[asset],
        objectMetadata(asset, { immutable: false }),
        { allowMissing: true },
      )
      if (!object) fail('missing-object', 'latest release object is missing', { retryable: true })
    }
  }

  async verifyConvergence(verified) {
    const signed = await this.verifySignedRelease(verified.version)
    await this.verifyLatestSigned(signed)
    const signedStable = await this.readStableSigned()
    const expected = stableBytes(signed.version, signed.manifest_sha256)
    if (!bytesEqual(signedStable.bytes, expected)) {
      fail('convergence-failed', 'stable release state does not converge')
    }
  }

  async restoreLatest(stable) {
    const verified = await this.verifySignedRelease(stable.version)
    if (verified.manifest_sha256 !== stable.manifest_sha256) fail('content-mismatch', 'stable release manifest does not match')
    await this.refreshLatest(verified.assets)
    await this.verifyConvergence(verified)
  }

  async recoverLatest(prior) {
    if (!prior) return
    try {
      const observed = await this.readStableSigned({ allowMissing: true })
      await this.restoreLatest(observed ?? prior)
    } catch {
      fail('compensation-failed', 'latest release compensation did not converge')
    }
  }

  async signedPromotion(verified, state) {
    const { prior, precondition } = state ?? {
      prior: await this.readStableSigned({ allowMissing: true }),
      precondition: undefined,
    }
    const stablePrecondition = precondition ?? await this.proveStablePrecondition(prior)
    const stable = {
      version: verified.version,
      manifest_sha256: verified.manifest_sha256,
      bytes: stableBytes(verified.version, verified.manifest_sha256),
    }
    let latestStarted = false
    try {
      latestStarted = true
      await this.refreshLatest(verified.assets)
      await this.writeStable(stable, stablePrecondition)
      await this.verifyConvergence(verified)
      return { version: verified.version, manifest_sha256: verified.manifest_sha256 }
    } catch (error) {
      if (error.code === 'conditional-conflict') {
        if (await this.resolveStableConflict(stable, prior, error)) {
          return { version: verified.version, manifest_sha256: verified.manifest_sha256 }
        }
      }
      if (latestStarted && prior) await this.recoverLatest(prior)
      throw error
    }
  }

  async publish(version, inventory) {
    if (!validatedInventories.has(inventory) || inventory.version !== version) {
      fail('invalid-assets', 'publication requires a validated release inventory')
    }
    const prior = await this.readStableSigned({ allowMissing: true })
    if (prior && compareVersions(version, prior.version) < 0) fail('stale-version', 'version is older than stable')
    const precondition = await this.proveStablePrecondition(prior)
    for (const asset of RELEASE_ASSETS) await this.putExact(keyForVersion(version, asset), inventory.assets[asset], asset)
    const verified = await this.verifySignedRelease(version)
    return this.signedPromotion(verified, { prior, precondition })
  }

  async verify(version) {
    const verified = await this.verifySignedRelease(version)
    await this.verifyLatestSigned(verified)
    const signedStable = await this.readStableSigned()
    const expected = stableBytes(version, verified.manifest_sha256)
    if (!bytesEqual(signedStable.bytes, expected)) {
      fail('convergence-failed', 'release state does not converge')
    }
    return { version, manifest_sha256: verified.manifest_sha256 }
  }

  async rollback(version) {
    const verified = await this.verifySignedRelease(version)
    return this.signedPromotion(verified)
  }
}

export function parseArguments(argv) {
  const [command, ...args] = argv
  if (!COMMANDS.includes(command)) fail('usage', 'command is not approved')
  const values = {}
  for (let i = 0; i < args.length; i += 1) {
    const flag = args[i]
    if (flag === '--version' || flag === '--assets') {
      if (!args[i + 1] || args[i + 1].startsWith('--') || values[flag.slice(2)]) fail('usage', 'duplicate or missing command option')
      values[flag.slice(2)] = args[++i]
    } else {
      fail('usage', 'unapproved command option')
    }
  }
  if (!values.version || (command === 'publish' && !values.assets) || (command !== 'publish' && values.assets)) {
    fail('usage', 'command options do not match the approved interface')
  }
  parseVersion(values.version)
  return { command, ...values }
}

export function sanitizeError(error) {
  if (error instanceof PublicationError) return error.message
  return 'publication failed'
}

export async function run(argv, {
  env = process.env,
  publisherFactory = createPublisherFromEnv,
} = {}) {
  let command
  try {
    command = parseArguments(argv)
    const inventory = command.command === 'publish'
      ? await validateAssets(command.assets, command.version)
      : undefined
    const publisher = publisherFactory(env)
    if (command.command === 'publish') await publisher.publish(command.version, inventory)
    if (command.command === 'verify') await publisher.verify(command.version)
    if (command.command === 'rollback') await publisher.rollback(command.version)
    return 0
  } catch (error) {
    process.stderr.write(`publish-r2: ${sanitizeError(error)}\n`)
    return 1
  }
}

const invokedPath = process.argv[1] && pathToFileURL(process.argv[1]).href
if (invokedPath === import.meta.url) {
  run(process.argv.slice(2)).then((status) => { process.exitCode = status })
}
