# YHC Publication Readiness Implementation Plan

> **Historical execution note:** This plan records the completed publication
> readiness implementation. Future changes follow the current repository
> workflow and accepted contracts. Checkboxes remain as closeout evidence.
> No live worker instruction is retained here.

**Goal:** Build a deterministic publication sieve, clear every candidate path,
remove reachable dependency vulnerabilities, and add the license, security,
governance, SBOM, and CI contracts required before any YHC source becomes
public.

**Architecture:** A repository-owned `scripts/publication` command treats
`quality/publication.yaml` as the policy SSOT. It inventories tracked paths,
requires exactly one clearance decision, validates source mappings and
third-party licenses, scans privacy-sensitive expression, and materializes only
approved regular files from one clean commit. External scanners are pinned by
the Makefile; raw findings remain under ignored `build/publication` while the
candidate contains only redacted status and digests.

**Tech Stack:** Go 1.26.5, `gopkg.in/yaml.v3`, Git plumbing, SHA-256,
`govulncheck`, Gitleaks, CycloneDX Go module license evidence, GitHub Actions,
Dependabot, CodeQL, Markdown governance files, and Makefile gates.

**Status:** historical
**Created:** 2026-08-09
**Plan state:** Completed; Tasks 1-8 complete

> **Ownership:** provenance inventory, dependency/security remediation, public
> governance, publication tooling, and the final content gate for the
> [YHC public-release design](../specs/2026-08-09-yhc-public-release-design.md).

## Global Constraints

- Run Tasks 1-2 before identity or clean-room edits. Rerun Tasks 3-8 after the
  identity, state, and protocol plans complete.
- `quality/publication.yaml` covers every tracked path exactly once by explicit
  path or non-overlapping rule. `unresolved` may appear in review inventory but
  always fails `check` and `materialize`.
- Classes are `project-owned-original`,
  `reference-informed-independent`, `license-compatible-third-party`,
  `proprietary-or-reconstructable`, and `private-operational`. Decisions are
  `include`, `exclude`, `rewrite`, or `unresolved`.
- Source mapping is evidence, never a license. Production, tests, prompts,
  fixtures, goldens, generated assets, vendored source, skills, and research
  prose receive the same review.
- The materializer reads only a clean tracked source tree whose HEAD equals the
  frozen commit. It rejects symlinks, submodules, special files, source/root
  replacement, unsafe output roots, collisions, and unclassified paths.
- Discovery never traverses `.reference`, `.git`, `.eino-agent`, `.yhc`,
  `.claude`, build/evaluation output, or untracked paths. Negative tests use a
  read spy to prove this.
- Raw security/privacy findings live only under mode-`0700` ignored
  `build/publication`. Public reports contain scanner name/version, input
  digest, timestamp, and status; never matching values or removed paths.
- Apache-2.0 applies only to cleared project-owned expression. Keep compatible
  third-party licenses and modification notices with their material.
- No reachable vulnerability, unknown license, unresolved provenance decision,
  secret candidate, personal path, private email, hidden endpoint, or
  unapproved author identity is waived in this plan.
- Workflows use read-only defaults, immutable action SHAs, no
  `pull_request_target`, no fork secrets, and no scanner-log artifacts.
- The confidential reporting route is GitHub private vulnerability reporting;
  do not publish a private contact address.

---

## Locked Interfaces

```go
type Config struct {
	Version      int              `yaml:"version"`
	Source       SourcePolicy     `yaml:"source"`
	Rules        []PathRule       `yaml:"rules"`
	Mappings     MappingPolicy    `yaml:"mappings"`
	Privacy      PrivacyPolicy    `yaml:"privacy"`
	Dependencies DependencyPolicy `yaml:"dependencies"`
}

type SourcePolicy struct {
	Repository     string `yaml:"repository"`
	BaselineCommit string `yaml:"baseline_commit"`
}

type PathRule struct {
	ID       string   `yaml:"id"`
	Include  []string `yaml:"include"`
	Exclude  []string `yaml:"exclude,omitempty"`
	Class    string   `yaml:"class"`
	Decision string   `yaml:"decision"`
	License  string   `yaml:"license,omitempty"`
	Evidence []string `yaml:"evidence"`
}

type PrivacyPolicy struct {
	AllowedEmails    []string          `yaml:"allowed_emails,omitempty"`
	AllowedURLHosts  []string          `yaml:"allowed_url_hosts,omitempty"`
	TestSentinels    []string          `yaml:"test_sentinels,omitempty"`
	ReviewedFindings []ReviewedFinding `yaml:"reviewed_findings,omitempty"`
}

type ReviewedFinding struct {
	Path        string `yaml:"path"`
	Line        int    `yaml:"line"`
	RuleID      string `yaml:"rule_id"`
	MatchSHA256 string `yaml:"match_sha256"`
	Purpose     string `yaml:"purpose"`
}

type FileDecision struct {
	Path       string `json:"path"`
	BlobSHA256 string `json:"blob_sha256"`
	RuleID     string `json:"rule_id"`
	Class      string `json:"class"`
	Decision   string `json:"decision"`
	License    string `json:"license,omitempty"`
	Mapped     bool   `json:"mapped"`
}

type ReleaseManifest struct {
	SchemaVersion int               `json:"schema_version"`
	SourceTreeSHA256 string          `json:"source_tree_sha256"`
	TreeSHA256    string            `json:"tree_sha256"`
	FileCount     int               `json:"file_count"`
	Checks        map[string]string `json:"checks"`
	SBOMSHA256    string            `json:"sbom_sha256"`
}
```

CLI:

```text
publication inventory --config quality/publication.yaml --output build/publication/inventory.json
publication check --config quality/publication.yaml
publication scan-expression --config quality/publication.yaml --root .
publication materialize --config quality/publication.yaml --source-commit SHA --output PATH
publication check-tree --config quality/publication.yaml --root PATH
publication manifest --config quality/publication.yaml --root PATH --output PATH
```

Reviewed findings are redacted exact tuples, not value, path-pattern, or rule
waivers. A missing, duplicated, changed, or stale tuple fails the scan.

`inventory` may report unresolved rows but exits non-zero for duplicate or
unmatched paths. `check` and `materialize` exit non-zero for every unresolved,
excluded-but-present, rewrite-pending, unsafe, or digest-mismatched row.

## Task 1: Create The Policy And Complete-Path Inventory

**Files:**

- Create: `quality/publication.yaml`
- Create: `scripts/publication/main.go`
- Create: `scripts/publication/config.go`
- Create: `scripts/publication/config_test.go`
- Create: `scripts/publication/inventory.go`
- Create: `scripts/publication/inventory_test.go`
- Modify: `quality/iteration.yaml`
- Modify: `Makefile`

- [x] **Step 1: Add red tests**

Create `TestPublicationConfigRejectsUnknownFieldsAndDuplicateRuleIDs`,
`TestInventoryRequiresExactlyOneDecisionPerTrackedPath`,
`TestInventoryRejectsUnresolvedForCheckButReportsItForReview`,
`TestInventoryTreatsTestsFixturesPromptsAssetsAndVendorAsOrdinaryPaths`, and
`TestInventoryDoesNotReadIgnoredOrUntrackedRoots`.

- [x] **Step 2: Run red**

```bash
go test ./scripts/publication -run 'Test(PublicationConfig|Inventory)' -count=1
```

Expected: FAIL because the package and schema do not exist.

- [x] **Step 3: Implement strict tracked-path coverage**

Use `yaml.Decoder.KnownFields(true)` and `git ls-files --stage -z`. Freeze
`github.com/abietic/yhc` plus baseline
`6500a09be6ec641c31348a4322a085eeaa029241`. Reject
non-regular modes and submodules; normalize repository paths; require exactly
one rule; hash bytes with SHA-256; never emit content. Classify
`third_party/acp-go-sdk/**` separately and leave unreviewed provenance as
`unresolved`. Do not use a catch-all include rule to force green.

- [x] **Step 4: Run green and commit**

```bash
go test ./scripts/publication -run 'Test(PublicationConfig|Inventory)' -count=1
make publication-inventory
git add quality/publication.yaml quality/iteration.yaml scripts/publication Makefile
git commit -m "feat(publication): inventory every candidate path"
```

Tests must pass. Inventory may remain blocked only by named `unresolved` rows;
every tracked path still appears exactly once.

## Task 2: Build The Fail-Closed Materializer And Expression Scanner

**Files:**

- Create: `scripts/publication/materialize.go`
- Create: `scripts/publication/materialize_test.go`
- Create: `scripts/publication/scan.go`
- Create: `scripts/publication/scan_test.go`
- Modify: `scripts/publication/main.go`
- Modify: `Makefile`
- Modify: `.gitignore`

- [x] **Step 1: Add materializer red tests**

Create `TestMaterializeRejectsDirtyOrMismatchedSourceCommit`,
`TestMaterializeCopiesOnlyIncludedRegularFiles`,
`TestMaterializeRejectsSymlinkSubmoduleSpecialFileAndCollision`,
`TestMaterializePinsSourceAndDestinationRootsAgainstReplacement`,
`TestCheckTreeRejectsGitReferenceStateAndPrivateOperationalRoots`, and
`TestReleaseManifestIsDeterministicAndContainsNoFindingValues`.

The fixture contains `.reference/secret.txt`, `.git/config`,
`.eino-agent/transcripts/session.jsonl`, `.claude/credentials.json`, an
untracked file, and an outside-pointing symlink. None may be opened or copied.

- [x] **Step 2: Add scan red tests**

Create `TestScanExpressionDetectsPrivatePathEmailEndpointAndCredentialPatterns`
and `TestScanExpressionAllowsDocumentedPublicContactsAndTestSentinels`.
Cover macOS/Linux/Windows home paths, non-allowlisted email, private-key
headers, credential assignments, provider/GitHub/AWS/bearer-token forms,
non-public URL hosts, and high-entropy strings. Diagnostics contain path, line,
rule ID, and match digest only.

- [x] **Step 3: Run red**

```bash
go test ./scripts/publication -run 'Test(Materialize|CheckTree|ReleaseManifest|ScanExpression)' -count=1
```

- [x] **Step 4: Implement root-pinned materialization**

Use `os.OpenRoot`. Require the supplied source commit to equal clean HEAD and
descend from `source.baseline_commit`. Re-stat source files after open, require `os.SameFile`,
create directories `0700`, preserve only tracked execute bits, fsync file and
parent, and promote a sibling staging directory only after `check-tree`.
Compute the tree digest over ordered
`path NUL mode NUL content-digest NUL` records.

- [x] **Step 5: Run green and commit**

```bash
go test ./scripts/publication -run 'Test(Materialize|CheckTree|ReleaseManifest|ScanExpression)' -count=1
git add scripts/publication Makefile .gitignore
git commit -m "feat(publication): materialize a fail-closed public tree"
```

## Task 3: Resolve Provenance And Source-Mapping Decisions

**Files:**

- Modify: `quality/publication.yaml`
- Modify as reported: exact tracked sources, tests, fixtures, prompts, assets,
  skills, and research paths in `build/publication/inventory.json`
- Modify: `docs/migration/manifest.yaml` only when a mapping is inaccurate
- Create: `docs/publication/README.md`
- Create: `docs/publication/root-clearance.md`

- [x] **Step 1: Generate the mapping review set**

```bash
make publication-inventory
```

Explicitly review every file containing `Mirrors`, `Ported from`,
`Reference`, a `.reference/` path, or a migration-manifest mapping together
with related tests, prompts, fixtures, goldens, assets, vendor, and prose.

- [x] **Step 2: Freeze behavior before each rewrite**

For each `rewrite` row, add a project-owned characterization test. Prove it can
fail with a perturbed fixture and pass on current behavior before replacing
expression. If independent expression cannot be written from the observable
contract without consulting mapped source bodies, exclude the path and stop
dependent public builds.

- [x] **Step 3: Clear all decisions**

Keep accurate short mappings, paraphrase long copied prose, and retain truthful
historical old-name citations. Run:

```bash
make publication-check-policy
go run ./scripts/publication scan-expression --config quality/publication.yaml --root .
```

Expected: zero unresolved, rewrite-pending, proprietary-included, or
private-operational-included rows.

- [x] **Step 4: Commit only reviewed paths**

```bash
git add quality/publication.yaml docs/publication docs/migration/manifest.yaml
git add --pathspec-from-file=build/publication/reviewed-source-paths.txt --pathspec-file-nul
git diff --cached --check
git commit -m "refactor(publication): clear YHC source provenance"
```

## Task 4: Remediate Vulnerabilities And Inventory Licenses

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `Makefile`
- Create: `quality/dependency-licenses.yaml`
- Create: `scripts/publication/dependencies.go`
- Create: `scripts/publication/dependencies_test.go`

- [x] **Step 1: Pin tools and capture red vulnerability evidence**

Add exact Makefile pins:

```make
GOVULNCHECK_VERSION := v1.6.0
GITLEAKS_VERSION := v8.29.1
CYCLONEDX_GOMOD_VERSION := v1.10.0
```

These were resolved from their published Go module versions on 2026-08-09.
Do not use `@latest`. Gitleaks v8.30.1 is excluded because its directory
scanner has a confirmed [silent-detection regression](https://github.com/gitleaks/gitleaks/issues/2170);
`secret-check` must prove
the pinned binary with a deterministic provider-token canary before scanning
the candidate. Both go-licenses v1.6.0 and v2.0.1 misclassify standard-library
packages under this Go 1.26.5 module, so they are not accepted as release
oracles. Add
`prepare-publication-tools`, `vuln-check`, `secret-check`,
`license-check`, `sbom`, `verify-publication` for a clean Git source,
and `verify-publication-tree` for a materialized tree with no `.git`.

```bash
make prepare-publication-tools
make vuln-check
```

Expected before remediation: non-zero if the refreshed database still finds
the design-audit gRPC, AWS eventstream, Goldmark, or `x/net` paths. Raw JSON
stays under `build/publication`.

- [x] **Step 2: Remediate one dependency family at a time**

For each official advisory, select its minimum compatible fixed module release,
update only that family, run `go mod tidy` and the focused packages named by
`govulncheck`, then run:

```bash
make test
make build
make vuln-check
```

Expected: the targeted reachable finding disappears without runtime contract
changes. Keep a transitive fixed minimum explicit in `go.mod` when required.

- [x] **Step 3: Add license completeness tests**

Create `TestDependencyInventoryRequiresEveryGoListModule`,
`TestDependencyInventoryIncludesLocalReplacementLicense`,
`TestDependencyInventoryRejectsUnknownSPDXOrMissingNotice`, and
`TestDependencyReportContainsNoModuleCacheOrHomePath`.

```bash
go test ./scripts/publication -run '^TestDependency' -count=1
```

Expected: FAIL until the checker and complete policy exist.

- [x] **Step 4: Implement and clear the inventory**

Compose the union of `go list -deps -test -json ./...` for every supported
CGO-disabled build target, direct build-tag dependencies from `go list -m`,
CycloneDX 1.6 license evidence, and local replacement metadata. Require exact
module/version reconciliation, SPDX ID, source URL, license file,
compatibility decision, NOTICE rule, and an explicit low-cardinality override
for detector misses or reviewed mixed-license scope. The local ACP replacement
keeps its upstream version as identity and records its repository-contained
modified replacement separately.

```bash
go test ./scripts/publication -run '^TestDependency' -count=1
make license-check
make sbom
```

Expected: PASS; `sbom.cdx.json` has no cache/home path or secret.

- [x] **Step 5: Commit**

```bash
git add go.mod go.sum Makefile quality/dependency-licenses.yaml scripts/publication/dependencies.go scripts/publication/dependencies_test.go sbom.cdx.json
git commit -m "fix(deps): clear YHC publication vulnerabilities"
```

## Task 5: Add Apache-2.0 And Public Governance

**Files:**

- Create: `LICENSE`
- Create: `NOTICE`
- Create: `SECURITY.md`
- Create: `CONTRIBUTING.md`
- Create: `CODE_OF_CONDUCT.md`
- Create: `.github/dependabot.yml`
- Create: `.github/ISSUE_TEMPLATE/bug.yml`
- Create: `.github/ISSUE_TEMPLATE/feature.yml`
- Modify: `.github/pull_request_template.md`
- Modify: `docs/contributing/README.md`
- Modify: `scripts/docs_check/main_test.go`

- [x] **Step 1: Add and run the red governance test**

Create `TestPublicGovernanceFilesAndCanonicalLinks`. Require all root files,
public YHC URLs, no private-archive link in current copy, and source-mapping
policy links.

```bash
go test ./scripts/docs_check -run '^TestPublicGovernanceFilesAndCanonicalLinks$' -count=1
```

Expected: FAIL because the files do not exist.

- [x] **Step 2: Add reviewed governance**

Use unmodified Apache-2.0 text. NOTICE identifies YHC authors, retained
third-party notices, modified vendored ACP SDK material, and Contributor
Covenant 2.1 attribution without claiming dependency ownership.
`SECURITY.md` uses
`https://github.com/abietic/yhc/security/advisories/new`. Contribution rules
require provenance, compatible licensing, local gates, and rights affirmation,
but no CLA. Dependabot checks Go modules and GitHub Actions monthly with a small
open-PR limit.

- [x] **Step 3: Run green and commit**

```bash
go test ./scripts/docs_check -run '^TestPublicGovernanceFilesAndCanonicalLinks$' -count=1
make docs-check
git add LICENSE NOTICE SECURITY.md CONTRIBUTING.md CODE_OF_CONDUCT.md .github docs/contributing/README.md scripts/docs_check/main_test.go
git commit -m "docs: add YHC public governance"
```

## Task 6: Harden Public CI And Desired Repository State

**Files:**

- Modify: `.github/workflows/ci.yml`
- Create: `.github/workflows/codeql.yml`
- Create: `.github/rulesets/master.json`
- Create: `.github/repository-settings.json`
- Modify: `quality/publication.yaml`
- Modify: `Makefile`
- Create: `scripts/publication/workflow_test.go`

- [x] **Step 1: Add red workflow tests**

Create `TestPublicWorkflowUsesImmutableActionsAndMinimalPermissions`,
`TestPublicWorkflowRejectsPullRequestTargetAndForkSecrets`,
`TestRequiredGateDependsOnPublicationSafety`, and
`TestRepositoryDesiredStateMatchesApprovedRules`.

```bash
go test ./scripts/publication -run 'Test(PublicWorkflow|RequiredGate|RepositoryDesired)' -count=1
```

- [x] **Step 2: Implement the desired state**

Add a `Publication safety` CI job and make `Required gates` depend on it.
Run policy, expression, vulnerability, license, SBOM drift, and secret checks
for every change. Add CodeQL on PR, `master` push, and weekly schedule with only
`contents: read` and `security-events: write`. Keep `build/publication` out of
artifacts.

Desired state enables squash-only, delete-on-merge, read-only workflow tokens,
no token-based PR approvals, SHA pinning, PR/conversation resolution, no human
approval until a second maintainer, no force push/deletion, and
`Required gates`.

- [x] **Step 3: Run green and commit**

```bash
go test ./scripts/publication -run 'Test(PublicWorkflow|RequiredGate|RepositoryDesired)' -count=1
make docs-check
git add .github Makefile quality/publication.yaml scripts/publication/workflow_test.go
git commit -m "ci: enforce YHC public release gates"
```

## Task 7: Finalize The Private Source Candidate

**Files:**

- Modify: `quality/publication.yaml`
- Modify: `quality/dependency-licenses.yaml`
- Modify: `docs/publication/root-clearance.md`
- Modify: `sbom.cdx.json`
- Generated outside Git: `build/publication/*.json`

- [x] **Step 1: Generate final redacted evidence**

```bash
make publication-check-policy
make vuln-check
make license-check
make sbom
```

Update `root-clearance.md` with input digests, scanner/tool versions, counts,
and pass/fail only. Expected: zero exits and no raw finding value.

- [x] **Step 2: Commit the exact private source candidate**

```bash
git add quality/publication.yaml quality/dependency-licenses.yaml docs/publication sbom.cdx.json
git diff --cached --check
git commit -m "chore(publication): close YHC content readiness"
```

- [x] **Step 3: Run final source gates on the committed candidate**

```bash
git status --short
make fmt
make lint
make test
make build
make docs-check
make test-race
make test-contract
make test-e2e
make verify-publication
git diff --check
```

Expected: status is empty and every command exits 0.

## Task 8: Materialize And Scan The Final Candidate

**Files:**

- Generated outside source: `build/publication/tree/`
- Generated outside public tree: `build/publication/*.json`
- Generated inside candidate: `PUBLICATION_MANIFEST.json`
- Existing inside candidate: `sbom.cdx.json`

- [x] **Step 1: Materialize and run two secret checks**

```bash
publication_parent=$(mktemp -d /tmp/yhc-publication-tree.XXXXXX)
publication_tree="$publication_parent/tree"
test ! -e "$publication_tree"
source_commit=$(git rev-parse HEAD)
go run ./scripts/publication materialize --config quality/publication.yaml --source-commit "$source_commit" --output "$publication_tree"
make secret-check PUBLICATION_ROOT="$publication_tree"
go run ./scripts/publication scan-expression --config quality/publication.yaml --root "$publication_tree"
go run ./scripts/publication check-tree --config quality/publication.yaml --root "$publication_tree"
```

Gitleaks is primary; the repository-owned credential/entropy detector is the
independent second check. Expected: all pass and source bytes remain unchanged.

- [x] **Step 2: Generate and recheck the redacted manifest**

```bash
go run ./scripts/publication manifest --config quality/publication.yaml --root "$publication_tree" --output "$publication_tree/PUBLICATION_MANIFEST.json"
go run ./scripts/publication check-tree --config quality/publication.yaml --root "$publication_tree"
```

- [x] **Step 3: Run the required gates inside the materialized root**

Use task-specific config variables in the harness; do not repurpose shell
`HOME`, `home`, or `CODEX_HOME`.

```bash
make -C "$publication_tree" fmt-check
make -C "$publication_tree" lint
make -C "$publication_tree" test
make -C "$publication_tree" build
REFERENCE_DIR=/path/to/reference-snapshots make -C "$publication_tree" docs-check
make -C "$publication_tree" verify-publication-tree PUBLICATION_ROOT="$publication_tree"
```

Never copy `build/publication` or raw scanner output into the materialized
tree. A materialized failure requires a source fix, new commit, and full repeat
from Task 7; do not patch the candidate directory by hand. Passing this task
does not authorize remote rename or visibility change.
