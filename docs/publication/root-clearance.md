# YHC Public Root Clearance

**Status:** historical
**Last verified:** 2026-08-10

> **Ownership:** redacted content-clearance record for the reviewed private
> source candidate and its materialized public root

This record states the reproducible release conditions used for YHC's original
clean public root. It contains no raw scanner value, removed path, local machine
path, or private operational identifier. Its counts and SBOM digest are
bootstrap evidence, not the current source-tree contract.

## Cleared source set

The final tracked inventory contains 1,834 included regular files and no
`exclude`, `rewrite`, `unresolved`, submodule, symlink, or special-file row.

| Class | Files | Clearance |
|---|---:|---|
| Project-owned original expression | 590 | Included under the root Apache-2.0 license |
| Reference-informed independent expression | 1,227 | Included; all 1,227 paths have current source mappings |
| Compatible third-party material | 17 | Included with retained license and notice evidence |

Source mappings retain provenance and learning value. They do not grant rights,
replace third-party terms, or make a reference implementation the product
specification. No source path remains in `rewrite` state.

## Redacted verification summary

| Gate | Bootstrap cleared result |
|---|---|
| Publication policy and tracked-path coverage | Pass; 1,834 of 1,834 paths included exactly once |
| Reference mapping coverage | Pass; 1,227 of 1,227 reference-informed paths mapped |
| Repository-owned expression scan | Pass; zero unresolved findings after 720 exact reviewed tuples |
| Gitleaks canary and source-tree scan | Pass; the constructed canary is detected and the candidate has zero findings |
| Reachable Go vulnerability scan | Pass; zero reachable affected vulnerabilities |
| Dependency license and NOTICE inventory | Pass; 144 of 144 components cleared |
| CycloneDX SBOM drift check | Pass; the bootstrap release's committed static cross-platform SBOM matched regeneration |
| Repository Makefile gates | Pass; formatting, lint, tests, build, documentation, contract, race, and real-binary checks |

Reviewed tuples contain only repository path, line, rule ID, matched-value
SHA-256, and a low-cardinality purpose. They are exact current observations,
not path, pattern, value, or detector waivers. Missing, changed, duplicated, or
stale tuples fail closed.

## Bootstrap evidence anchors

| Evidence | SHA-256 or version |
|---|---|
| Signed clean public root | `8e34cc4794f0e1e9ae404c5bcf453d5e71a159c0` |
| Source-mapping manifest | `fb60b8385845b158f64932116a7b46850e96d7fd4c0ffb764688988374b7cdf3` |
| Dependency-license policy | `a3e7920a65178782d3009c60088e5e4226705811faf09a34ed3aa21f3fa618ca` |
| Bootstrap CycloneDX SBOM | `a8ad37c431845ac3526bcfeeb9be139c0a9b1a774295e800c241ec6348079262` |
| Root Apache-2.0 text | `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30` |
| Vendored ACP SDK Apache-2.0 text | `3cf3fec4549ad049b3defd633001ce9e89923cdaee3d45d5ff4686750706e3cd` |
| Go / govulncheck | Go 1.26.5 / govulncheck 1.6.0 |
| Secret scanner / SBOM generator | Gitleaks 8.29.1 / CycloneDX Go module 1.10.0 |

The committed source document cannot contain a non-self-referential digest of
its own final tree. The bootstrap manifest schema recorded an SBOM digest.
Current `PUBLICATION_MANIFEST.json` schema 2 owns only the exact source-tree
digest, materialized-tree digest, file count, and policy, expression, and tree
pass statuses. Current CycloneDX output, timestamps, and raw tool reports belong
only to ignored `build/publication/` evidence.

## Reproduction

From a clean committed private source candidate:

~~~bash
make fmt-check
make lint
make test
make build
make docs-check
make test-race
make test-contract
make test-e2e
make verify-publication
make license-check
~~~

Then materialize into a new empty sibling directory and run the tree checks
documented in the approved publication plan. The full source-mapping check in a
tree without `.reference` must receive the audited external snapshot parent via
`REFERENCE_DIR`; the snapshot remains outside the candidate. Do not patch the
materialized tree by hand and do not copy ignored `build/publication` reports
into it.

Passing this record authorizes neither remote creation nor visibility changes.
Those remain separate explicit approval boundaries, and any mismatch in the
exact root, repository identity, ruleset, or first public CI run stops release.
