# R2 publication boundaries

The standalone helper is authoritative for signed R2 exact, latest, and stable
state. It accepts only fixed release keys and metadata, writes immutable exact
objects with `If-None-Match: *`, promotes latest before stable, and uses stable
ETag/CAS as the final commit point. Ambiguous writes use signed readback and
fail closed; partial mutable promotion recovers to the observed prior stable
release.

## Production boundary

Production is reachable only when the repository owner pushes a canonical
`vMAJOR.MINOR.PATCH` tag to `kuo77122/Snapifact-CLI`, the canonical tag checks
pass, and the draft publication job succeeds. It uses the literal
`r2-release-production` environment, non-canceling concurrency, pinned actions,
least privilege, exact event-SHA checkout, and `npm ci`.

The publication job creates one draft Release with exactly these six assets:

1. `snapifact_linux_amd64`
2. `snapifact_linux_arm64`
3. `snapifact_darwin_amd64`
4. `snapifact_darwin_arm64`
5. `SHA256SUMS`
6. `install.sh`

The Production job revalidates the current run's artifact ID, run, name,
expiration, REST digest, archive digest, exact six-file inventory, checksums,
installer pin, tag SHA, and asset digest set before credential-bearing
publication. The helper validates the same six-file inventory once before
publisher construction and receives that validated inventory directly.

Production calls signed `publish` once. A successful helper call means exact,
latest, and stable signed state converged; there is no public stable
prerequisite, duplicate helper verification, or workflow compensation.

After signed publication, one canonical installer smoke runs without R2
credentials and is `continue-on-error`. Its current impact is delayed public
visibility without corrupting signed state. Make it blocking only if measured
user-facing propagation failures become common or a release SLO is adopted.

Finalization runs only after Production success, has `contents: write` only,
and receives the numeric Release ID selected by the publication job. The
finalizer GETs that same ID, checks the numeric ID, draft state, canonical tag
binding, and exact six asset names, then PATCHes that same ID with `draft=false`.
It does not use tag-based Release lookup or tag-based CLI editing, and does not
download or hash the assets a second time.

### v0.3.2 finalization recovery

Attempt 2 of run `31009358275` successfully published the signed Production
exact/latest/stable state and passed the canonical installer smoke. Finalization
then failed because authenticated `GET /repos/$GITHUB_REPOSITORY/releases/tags/v0.3.2`
returned 404 for draft Release `365540523`; this was a Release lookup failure,
not a signed publication failure. The owner revalidated the numeric Release ID,
tag `v0.3.2`, unchanged tag SHA
`f5f627bbf450770299b702749583a19b4aea8de2`, draft state, and six assets, then
manually published only Release `365540523` with a numeric REST PATCH.

Do not rerun the workflow, mutate R2, move the tag, create a new version, or
edit the finalized Release as recovery. Future finalization selects exactly one
matching draft from the authenticated Release list immediately after creation,
passes its numeric ID between jobs, revalidates that ID before publication, and
PATCHes only that numeric Release endpoint.

## Preview rehearsal

Dispatch **Release** from standalone `main` as the repository owner with a
canonical version and `preview` operation. Preview uses
`r2-release-preview`, preserves exact dispatch-SHA and provenance isolation,
and independently selects one successful, non-expired current provenance
artifact. It verifies the tag, run, artifact ID and digest, archive digest,
exact six-file inventory, checksums, installer pin, and asset digest set before
accessing signed R2 values.

The rehearsal runs signed `publish`, `verify`, `rollback`, and `verify`. It does
not read public release state. Weak ETags are proved with a signed conditional
read; malformed validators stop before mutation. Strong and weak validators,
conditional conflicts, ambiguous outcomes, stable-last ordering, and recovery
remain covered by the helper tests.

## Manual rollback

Rollback is an explicit operator command against a previously verified
immutable version:

```sh
node scripts/publish-r2.mjs verify --version vMAJOR.MINOR.PATCH
node scripts/publish-r2.mjs rollback --version vMAJOR.MINOR.PATCH
node scripts/publish-r2.mjs verify --version vMAJOR.MINOR.PATCH
```

Rollback reads and verifies every immutable object, never overwrites or deletes
exact history, then restores mutable latest/stable through the same signed
promotion path. Do not retry a failed exact write or reuse a version with
unknown immutable residue.

## Failed candidates and fresh versions

Failed `v0.2.4`, `v0.3.0`, and `v0.3.1` tags, drafts, artifacts, and object
history remain untouched. Production remains `v0.2.2` until a separately
approved fresh version is selected. A candidate with exact residue requires a
separate recovery decision and a fresh version; exact objects are never
overwritten, deleted, or retried automatically.

The current one-writer tradeoff accepts unprotected refs. Add repository
enforcement before a second writer or write-capable automation. Restore finalizer
digest revalidation before a second writer can change a draft, or after any
observed draft drift. Restore workflow-level compensation only if a new
blocking post-publication mutation is introduced.

## Stop conditions

Stop on scope drift, changed head, missing or failed checks, tag/SHA/artifact or
digest drift, partial or mismatched exact residue, public/signed divergence,
latest/stable non-convergence, unavailable rollback, unexpected environment or
credential names, or any live release action without separate owner approval.
Never record credentials, signed material, raw response bodies, private values,
or asset bytes.
