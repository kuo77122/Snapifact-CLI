# R2 publication boundaries

This repository contains the fixed-key publication helper, one protected
Preview rehearsal, and one protected signed-read-only stable validator
diagnostic. Local and contributor workflows never resolve R2 credentials or
send signed requests.

## Active Production boundary

FAT-484 activates the reviewed Production path by removing only its inert
guard. The remaining predicate is deliberately narrow: the repository owner
must push a canonical `vMAJOR.MINOR.PATCH` tag to `kuo77122/Snapifact-CLI`,
verification must pass, and the draft publication job must succeed. Pull
requests, forks, branches, manual inputs, mutable refs, and failed
dependencies cannot select it.

The active job is bound literally to `r2-release-production`, uses a
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

### Correction status and fresh release gates

The `v0.2.4` attempt failed closed in run `30975075137` during current-run
provenance revalidation, before the credential-bearing step. The upload action
exposed a raw SHA-256 hex digest while the REST artifact and downloaded archive
checks use the canonical `sha256:<hex>` form. The publish output now adds that
prefix exactly once at the job boundary; the Production consumer still requires
literal equality with both the REST `.digest` and the downloaded ZIP checksum.

`v0.2.4` is abandoned as a release candidate. Its tag and draft Release remain
untouched, its provenance artifact remains an audit record, all six exact
`v0.2.4` objects remain absent, and public exact/latest/stable remains healthy
`v0.2.2`. Do not rerun, move, delete, replace, or clean up `v0.2.4`.

The fresh candidate is `v0.3.0`, created only at the correction PR's future
merge SHA. Its gates are separate and ordered:

1. Planner proves the correction merge SHA and green configured checks.
2. The owner separately authorizes and creates/pushes `v0.3.0` at that exact
   SHA. This workflow does not create tags.
3. The new tag run creates its own draft Release, revalidates six assets, and
   uploads its own current-run provenance artifact with the canonical prefixed
   digest.
4. Before Production approval, Planner rechecks the new tag/run/artifact/SHA,
   exact six digests, draft identity, and healthy unchanged `v0.2.2` state.
5. The owner separately approves the waiting `r2-release-production`
   deployment.
6. The workflow verifies prior stable, publishes `v0.3.0` once, verifies it,
   performs the existing credential-free installer and `go install` smoke, and
   compensates once only if publication succeeded and a later check fails.
7. The finalizer undrafts the matching `v0.3.0` Release only after complete
   Production success.

Repository rulesets and classic `main` protection are intentionally deferred at
the current single-writer scale. Accepted compensating controls are one
reviewed PR, current-head review, green `go` and `Verify (ubuntu-latest)`
checks, exact tag/SHA/provenance gates, and the required Production reviewer.
Paid immutable-tag and required-check enforcement becomes mandatory before a
second writer or write-capable automation, or before another Production release
after `v0.3.0`. FAT-484 release and recovery rely only on the reviewed
standalone workflow/helper/runbook; do not assume or recreate Core rollback.

Calling `publish` may write immutable exact `downloads/v0.3.0/*` objects before
returning. Rollback never deletes or overwrites exact history; it restores only
mutable latest/stable to the verified `v0.2.2` state. These outcomes remain
distinct and require no retry or delete behavior:

- no exact candidate residue and healthy `v0.2.2`: leave the draft and stop;
- all present exact candidate objects match the approved digests and healthy
  `v0.2.2`: record irreversible residue, leave the draft, and require a
  separately approved replan before any same-version rerun or replacement;
- partial or mismatched exact candidate objects: stop as an immutable-version
  incident and never overwrite, delete, or reuse `v0.3.0` without a separate
  recovery decision;
- unproven prior `v0.2.2` latest/stable: stop and require a separate recovery
  decision using only reviewed standalone behavior; do not assume or recreate
  Core rollback;
- after `mutation_succeeded=true`, allow the existing one-time mutable
  compensation; failed compensation or finalization leaves the draft and
  requires a separately approved finalize-or-rollback decision.

Stop on scope drift, changed head, missing or failed checks, mutation of the
abandoned `v0.2.4` tag/draft/artifact, a pre-existing `v0.3.0` tag/Release/object,
unresolved gate timing, unexpected environment or credential names,
tag/run/artifact/SHA/digest drift, partial or mismatched residue, public/signed
divergence, latest/stable non-convergence, unavailable rollback, or any
deployment requirement outside this workflow. Record only sanitized job, run,
artifact, SHA, digest, command outcome, and convergence evidence; never record
credentials, private values, raw bodies, signed material, URLs with credentials,
or asset bytes.

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
