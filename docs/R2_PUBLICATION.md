# R2 publication boundaries

This repository contains the fixed-key publication helper, one protected
Preview rehearsal, and one protected signed-read-only stable validator
diagnostic. Local and contributor workflows never resolve R2 credentials or
send signed requests.

## Dormant Production boundary

FAT-483 adds a complete but inert Production path. Its explicit `false &&`
guard is the first condition of the job predicate, so the job and its
downstream finalizer are skipped on every current event. The remaining
predicates are deliberately narrow: the repository owner must push a canonical
`vMAJOR.MINOR.PATCH` tag to `kuo77122/Snapifact-CLI`, verification must pass,
and the draft publication job must succeed. Pull requests, forks, branches,
manual inputs, mutable refs, and failed dependencies cannot select it.

The dormant job is bound literally to `r2-release-production`, uses a
non-canceling concurrency group, and has only `contents: read` and
`actions: read`. It checks out the exact event SHA and proves the tag commit,
then downloads only the current run's provenance artifact by the publish-job
output ID. It checks the artifact run, name, expiration state, platform digest,
fresh archive digest, exact six-file inventory, installer pin, and recomputed
asset digest set before accessing the protected environment.

The one credential-bearing publication step first reads the canonical public
`channels/stable.json` index, requires exactly `version` and `manifest_sha256`,
and verifies that previously stable version with the fixed helper. It publishes
the candidate once, marks mutation success only after that command returns, and
then verifies the candidate. Credential-free smoke fetches the canonical
versioned installer, installs into a temporary directory, checks exact
`snapifact vMAJOR.MINOR.PATCH` output, and runs the matching `go install`
version check. If verification or smoke fails after a successful publish,
exactly one compensation step rolls back to the previously verified stable
version and verifies it. The candidate is never retried.

The separate finalizer has only `contents: write` and no Production environment
or R2 values. It runs only after Production succeeds, rechecks the matching
draft Release, canonical tag and event SHA, exact six assets, and digest set,
then undrafts that one release. It has no `always()` or bypass path. Any
finalizer failure leaves the Release draft and requires a separately approved
finalize-or-rollback decision.

### Activation prerequisites and recovery

Before FAT-484 activation, the owner must select the canonical version and
revalidate the literal `r2-release-production` environment: required owner
review, admin bypass disabled, deployment policies `main` and `v*`, and only
the approved three variable names and two secret names. The repository must
also have immutable tag and required-check protection configured and verified;
the current repository has no ruleset and `main` is unprotected. FAT-512 must
merge before FAT-484 so Core retains a trustworthy rollback helper. FAT-484 may
remove only the reviewed inert guard and update its contract assertion; it must
not redesign this path.

Record only job, run, artifact, SHA, six digest, command outcome, and
convergence evidence. Never record secret values, signed material, URLs with
credentials, raw response bodies, or asset bytes. This ticket performs no
dispatch, approval, credential access, signed request, Production mutation,
public deployment, tag or Release action, or merge.

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
canonical release version and `preview` operation. The workflow:

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

To diagnose the stable validator instead, dispatch the literal
`stable-diagnostic` operation without a version. Before the protected Preview
values are available, it proves the checked-out 40-hex SHA is the repository's
dispatch-time `origin/main` commit and is reachable from fetched `main`. The
final step performs exactly one signed GET for the literal
`channels/stable.json` key and emits only the status, validator presence,
mutually exclusive shape, byte length, and first 12 lowercase SHA-256
characters of the validator. It never emits the validator, body, URL,
credentials, headers, account, bucket, or origin.

During publication, a valid weak-quoted stable validator is reduced only by
removing its exact `W/` prefix. Before any exact or mutable object write, the
helper performs one signed conditional GET for the same fixed key using that
candidate. It proceeds only when the response succeeds, has the approved
stable metadata and canonical schema, and has bytes identical to the initial
stable read. The unchanged candidate is then used for the final stable PUT;
proof failures stop without mutation, and a final 412 uses the existing
compensation path without retrying the stable PUT.

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
