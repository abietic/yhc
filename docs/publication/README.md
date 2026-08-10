# Public Source And Release Boundary

**Status:** current
**Last verified:** 2026-08-10

> **Ownership:** public-source classification, provenance clearance, privacy
> review, and clean-root release boundary

This directory explains why a tracked file may enter the YHC public source
tree. It does not authorize a remote change. The release operator must still
materialize a clean tree, verify that exact tree, obtain explicit approval
before remote mutation, and obtain a separate approval before changing
visibility.

## What the public tree contains

[`quality/publication.yaml`](../../quality/publication.yaml) classifies every
tracked path exactly once. A publishable path must be one of:

- project-owned original expression;
- independently expressed, reference-informed material with a current entry in
  [`docs/migration/manifest.yaml`](../migration/manifest.yaml); or
- compatible third-party material carrying its retained license and notice
  evidence.

The checker rejects unresolved, rewrite-pending, excluded-but-present,
unclassified, overlapping, private-operational, or reconstructable material.
Reference mappings preserve provenance and learning value; they are evidence,
not a license grant or future design authority.

The public tree never copies `.reference`, `.git`, `.eino-agent`, `.yhc`,
`.claude`, ignored build evidence, local transcripts, credentials, or
untracked files. The vendored ACP SDK is the only included third-party source
tree and retains its Apache-2.0 license plus a visible modification notice.

## Privacy and dependency gates

The repository scanner reports only path, line, rule identifier, and a digest
of the matched bytes. Fixed synthetic fixtures and non-secret public examples
may be accepted only through an exact reviewed tuple in the publication policy.
The scan fails when such a tuple is missing, duplicated, changed, or stale;
there are no directory-level or rule-level waivers.

Release checks also require:

- zero reachable Go vulnerability findings;
- an exact cross-platform dependency/SBOM component set;
- reviewed SPDX and NOTICE evidence for every component;
- a deterministic CycloneDX SBOM;
- a pinned secret scanner that first detects its own constructed canary; and
- the repository Makefile formatting, lint, test, and build gates.

See [`root-clearance.md`](root-clearance.md) for the candidate review summary
and reproduction commands.

## Clean-root publication

The public repository starts with one newly created root commit containing
only the materialized, verified tree. The private source repository keeps its
complete Git, pull-request, Actions, and artifact history. The clean root does
not copy those objects merely to preserve blame or commit identifiers.

Remote preparation and public visibility are distinct approval boundaries. If
any repository identity, scan result, signature, ruleset, or exact-root CI
fact differs at either boundary, the release remains private and stops.
