# Preview R2 rehearsal

This repository contains the fixed-key publication helper and one protected
Preview rehearsal. Local and contributor workflows never resolve R2
credentials or send signed requests.

## Before dispatch

The owner must configure the literal `r2-release-preview` environment after
merge with only these values:

- Secrets: `SNAPIFACT_R2_ACCESS_KEY_ID`, `SNAPIFACT_R2_SECRET_ACCESS_KEY`
- Variables: `SNAPIFACT_R2_ACCOUNT_ID`, `SNAPIFACT_R2_BUCKET`,
  `SNAPIFACT_R2_PUBLIC_ORIGIN`

Use a dedicated Preview bucket and require owner approval. Do not configure
repository-scoped credentials or production values. Future canonical tag
publishes create the draft, re-download and validate exactly six release files,
then upload those files as one immutable artifact named
`release-provenance-vMAJOR.MINOR.PATCH`. The artifact is the only Preview byte
source; Preview never reads the draft release.

The owner may separately dispatch the literal `legacy-backfill` operation only
for `v0.2.3`. It checks out tag SHA
`8ece01f324f5d6f37a120b5efcbb3796fa6eab6e`, rebuilds and validates the six
files, compares them with `release-provenance/v0.2.3.json`, and uploads the
same artifact shape. A mismatch uploads nothing.

## Rehearsal flow

Dispatch **Release** from standalone `main` as the repository owner with the
canonical release version. The workflow:

1. checks out the dispatch `github.sha`, never the version tag, for trusted
   helper/workflow code;
2. resolves the canonical tag commit and selects exactly one successful,
   non-expired version-named artifact from the matching owner run;
3. verifies run identity, artifact ID, GitHub artifact SHA-256, exact six-file
   inventory, checksums, and installer version pin in a fresh local directory;
4. requests the protected Preview environment only after preflight succeeds;
5. independently selects the same unique run and artifact, rechecks every
   identity/digest/inventory invariant, and publishes only from that directory;
6. runs `publish`, public `verify`, matching `publish` retry, public `verify`,
   same-version `rollback`, and final public `verify` from that directory.

No PR, fork, ordinary push, non-main ref, non-owner dispatch, noncanonical
version, tag drift, missing/failed/multiple/ambiguous/expired artifact,
artifact digest drift, inventory drift, or asset digest drift can reach the
credential-bearing step. Manual Preview dispatch has only `contents: read` and
`actions: read`; the legacy job has only `actions: write` as a write
capability, plus checkout-required `contents: read`. Neither path can create
or edit a GitHub Release.

## Stop conditions

Stop before signed access on any preflight or independent revalidation
failure. The helper never overwrites an existing mismatched exact object,
lists or deletes objects, accepts arbitrary keys, or falls back to production.
Do not retry a failed compensation or remove an unexpected anonymous-write
canary automatically; record the sanitized failure and obtain owner
remediation approval.

Record only the workflow/job identity, run/artifact identity and digest,
dispatch SHA, tag SHA, six digests, command outcomes, and exact/latest/stable
convergence results. Never record credentials, authorization or signature
material, signed URLs, raw bodies, asset bytes, private endpoints, or
secret-derived values. This ticket does not configure environments or perform
a live Preview run.
