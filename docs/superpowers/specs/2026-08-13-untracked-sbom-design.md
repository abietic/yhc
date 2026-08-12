# Untracked SBOM Design

**Status:** historical
**Accepted:** 2026-08-13
**Completed:** 2026-08-13
**Adoption:** `project-native`

> **Ownership:** accepted contract for removing the committed SBOM while
> retaining open-source licensing, third-party attribution, and fail-closed
> dependency-license verification
>
> **Reader task:** a maintainer can decide which licensing evidence remains
> committed, where an SBOM is produced, and which checks must still pass. Update
> this design if YHC starts publishing regulated artifacts or a downstream
> consumer requires a committed SBOM.

## Decision

Keep YHC's Apache-2.0 license, third-party licenses, `NOTICE`, and reviewed
dependency-license policy. Stop committing `sbom.cdx.json` and stop treating a
static SBOM digest as part of the publication-tree identity.

Dependency-license verification may generate an SBOM under ignored build state
as an intermediate input. `make sbom` remains an on-demand local command whose
output is not source. GitHub's dependency graph remains an independent way to
export an SBOM for the current public repository.

## Why the license stays

Repository visibility grants public access, not a general right to use, modify,
or redistribute YHC. The root Apache-2.0 text supplies that grant and matches the
contribution contract. Removing it would turn YHC into source-visible,
all-rights-reserved code while leaving third-party attribution obligations in
place.

The repository also distributes a modified ACP SDK and adapted Contributor
Covenant material. Their license and attribution evidence remains independent
of YHC's project-owned license and must not be removed as part of SBOM cleanup.

## Current problem

The repository currently commits one generated CycloneDX document and couples
it to:

- publication path classification;
- a deterministic normalization command;
- source-tree materialization and manifest digests;
- dependency-license verification; and
- current publication documentation.

That coupling is self-imposed. Neither the Apache-2.0 grant nor GitHub public
repository operation requires a committed SBOM. It adds generated-source churn
to ordinary dependency updates and makes an otherwise optional report part of
the source-tree contract.

## Contract after the change

1. `sbom.cdx.json` is absent from the tracked tree.
2. `make sbom` generates the current dependency report only below ignored
   `build/publication/` state.
3. `make license-check` generates or consumes that ignored report and still
   reconciles the complete module set with `quality/dependency-licenses.yaml`.
4. Publication materialization no longer searches for an SBOM path or stores an
   SBOM digest/pass field in `PUBLICATION_MANIFEST.json`.
5. Publication policy still covers every tracked file exactly once and retains
   source-mapping, secret, vulnerability, license, and tree checks.
6. No runtime, CLI, provider, session, permission, protocol, or state behavior
   changes.

## Implementation boundary

Remove the tracked SBOM rule and manifest fields. Decouple the license command
from a repository-relative SBOM path by passing the generated build artifact
explicitly. Delete normalization that existed only to make host-specific
generator metadata byte-identical for source control.

Update focused publication tests to prove:

- a source tree without `sbom.cdx.json` materializes and verifies;
- release manifests reject obsolete or malformed fields under the selected
  schema;
- dependency-license reconciliation still fails on missing, stale, duplicate,
  or incompatible dependency evidence; and
- the generated SBOM stays outside the tracked publication payload.

Historical release plans continue to record that the bootstrap release used a
committed SBOM. Only current publication documentation changes.

## Failure and rollback

SBOM generation or dependency reconciliation remains fail-closed for
`license-check`; an unavailable generator is a failed check, not a reason to
skip license evidence. Publication-tree verification must not depend on build
state left by an earlier command.

The change is reversible with one squash revert. A future distribution contract
may add artifact-attached or release-generated SBOMs without restoring a
generated document to the source tree.

## Verification

Use the repository iteration workflow and require:

- focused publication package tests;
- `make change-plan` and `make verify-focused`;
- a committed clean tree followed by `make verify-merge`;
- `make change-evidence-ready`;
- public pull-request required checks on the exact branch commit; and
- post-squash checks on the final `master` commit.

## Non-goals

- changing YHC's Apache-2.0 license;
- removing third-party license or notice material;
- weakening dependency-license review;
- changing product runtime behavior; or
- deciding whether to replace the vendored ACP SDK. That is a separate
  post-merge compatibility audit against current upstream releases.
