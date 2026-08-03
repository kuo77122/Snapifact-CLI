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
repository-scoped credentials or production values. Select an existing
canonical `vMAJOR.MINOR.PATCH` draft release containing exactly the four
binaries, `SHA256SUMS`, and `install.sh`.

## Rehearsal flow

Dispatch **Release** from standalone `main` as the repository owner with the
canonical release version. The workflow:

1. checks out the dispatch `github.sha`, never the version tag, for trusted
   helper/workflow code;
2. resolves the canonical tag commit and proves the draft has exactly six
   approved assets;
3. validates checksums and the installer version pin, then records only the
   tag SHA and six asset digests;
4. requests the protected Preview environment only after preflight succeeds;
5. independently re-downloads into a fresh directory and requires the tag
   SHA and digest set to match preflight; and
6. runs `publish`, public `verify`, matching `publish` retry, public `verify`,
   same-version `rollback`, and final public `verify` from that directory.

No PR, fork, ordinary push, non-main ref, non-owner dispatch, noncanonical
version, tag drift, draft inventory drift, or digest drift can reach the
credential-bearing step. Manual dispatch cannot create or edit a GitHub
Release.

## Stop conditions

Stop before signed access on any preflight or independent revalidation
failure. The helper never overwrites an existing mismatched exact object,
lists or deletes objects, accepts arbitrary keys, or falls back to production.
Do not retry a failed compensation or remove an unexpected anonymous-write
canary automatically; record the sanitized failure and obtain owner
remediation approval.

Record only the workflow/job identity, dispatch SHA, tag SHA, six digests,
command outcomes, and exact/latest/stable convergence results. Never record
credentials, authorization or signature material, signed URLs, raw bodies,
asset bytes, private endpoints, or secret-derived values. This ticket does
not configure environments or perform a live Preview run.
