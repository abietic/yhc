# Public Source And Release Boundary

**Status:** current
**Last verified:** 2026-10-29

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
untracked files. The vendored ACP SDK retains its Apache-2.0 license plus a
visible modification notice. The Web UI also distributes Marked 18.0.9 as
third-party MIT material; its retained license and notice are under
`internal/webui/assets/vendor/`. The root [`NOTICE`](../../NOTICE) records both
third-party distributions.

`server/appserver/**`, `internal/webui/**`, `desktop/**`, and the desktop serve
composition file are separately classified as mutually exclusive project-owned
rules. Marked runtime and retained license/notice paths are separately
classified as MIT third-party material. The Node dependency policy
(`quality/node-dependency-licenses.yaml`) is the reviewed source of truth.
`make node-sbom` deterministically generates CycloneDX evidence below ignored
`build/publication/` state, matching the Go SBOM boundary; generated reports
are not public source. In particular,
`desktop/package-lock.json` is audited by the structured Node lockfile
validator for exact policy decisions, npm registry `resolved` URLs, and
integrity values. The generic expression scan still examines all other lockfile
fields after masking only those validated package fields; the publication URL
host allowlist contains no npm registry entry and this policy has no directory
exception for the lockfile.

Desktop packages, staged backend binaries, and platform artifacts remain
ignored local-QA outputs. They are not source-release evidence and this tree
does not claim that any local package is signed, notarized, or distribution
ready.

## Privacy and dependency gates

The repository scanner reports only path, line, rule identifier, and a digest
of the matched bytes. Fixed synthetic fixtures and non-secret public examples
may be accepted only through an exact reviewed tuple in the publication policy.
The scan fails when such a tuple is missing, duplicated, changed, or stale;
there are no directory-level or rule-level waivers.

Release checks also require:

- zero reachable Go vulnerability findings;
- an exact cross-platform dependency component set;
- a generated CycloneDX inventory reconciled with reviewed SPDX and NOTICE
  evidence for every component;
- a pinned secret scanner that first detects its own constructed canary; and
- the repository Makefile formatting, lint, test, and build gates.

`make sbom` writes the current CycloneDX report only below ignored
`build/publication/` state. `make license-check` regenerates that report and
passes it explicitly to the fail-closed dependency-license checker. The report
is build evidence, not public source and not part of publication-tree identity.
`quality/dependency-licenses.yaml` remains the reviewed source-of-truth for
license, notice, replacement, and local modification decisions.

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

## Native dependency notices

Chroma's formatter registry embeds Liberation Mono in the native executable,
even when YHC selects terminal output. The reviewed scopes are MIT for Chroma
and OFL-1.1-RFN for the font. The retained
[`COPYING`](../../third_party/notices/chroma.COPYING) includes both complete
licenses and the font copyright and reserved-name statements.
[`provenance`](../../third_party/notices/chroma.PROVENANCE.json) pins the
upstream version and source digest. Its detector observation remains separate:
CycloneDX reports OFL-1.1-no-RFN, which does not replace the reviewed scopes.
The dependency checker admits that raw observation only for the exact reviewed
Chroma version, requires both scopes and notice retention, and rejects unknown
license identifiers. The actual SBOM observation must equal the reviewed set;
another known SPDX identifier or an additional observation requires new review.

Use `make cli-package` for CLI delivery. Each of the four platform ZIP packages
contains its executable and readable license/provenance files. `make build`
also refreshes the license sidecars in each platform directory. Distribute the
whole package rather than extracting a binary and discarding its notices.
Desktop packaging copies the same two Chroma files into `resources/licenses/`.
The after-pack verifier checks their exact bytes and digest against the reviewed
source; file presence alone is insufficient. The notice directory's
`.gitattributes` retains upstream COPYING line endings during Windows Git
checkout, including `core.autocrlf=true`; the digest check still rejects CRLF
conversion or any other byte change. This checkout rule is separately classified
as project-owned control metadata and grants no permission to other notice files.
Both delivery entrypoints use Go's
read-only module resolver with `GOWORK=off` and offline lookup to bind the selected
Chroma version and reject active replacements. They inspect the actual backend's
Go build information, reject missing or changed Chroma dependencies and
replacements, and check both GOOS and GOARCH against the supported package
architecture. Missing architectures and universal backends are rejected.
Desktop inspection bootstraps the backend's build Go version with the launcher,
then selects that exact existing version from PATH or Go's standard toolchain
module cache. It never requests a toolchain download or disables checksum
validation, and all inspection commands share a 30-second deadline. Packaging
therefore needs the actual build toolchain and resolved module metadata in cache;
an older launcher can use an already cached newer compiler.

`go test ./scripts/publication` checks archive contents, source digest drift,
license-scope separation, changed detector sets, module and binary version drift,
replacements, unknown identifiers and unsafe inputs. Desktop tests use real Go
binary fixtures for each supported package layout. A real Git CRLF-checkout
regression verifies both license bundling and Windows CLI packaging, including
rejection without the checkout rule and after license-byte changes. Local
build and package evidence does not establish code signing, notarization or
permission to publish a release.
