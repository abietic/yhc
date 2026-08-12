# Untracked SBOM Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `$iteration-workflow` for
> test-first execution, diff-bound verification, and committed evidence. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Status:** active-plan

> **Ownership:** executable steps for removing the committed SBOM while
> retaining dependency-license verification and publication-tree integrity

**Goal:** Remove `sbom.cdx.json` from source control and make SBOM generation an
ignored, on-demand input to dependency-license verification.

**Architecture:** Keep CycloneDX generation at the Makefile boundary, but write
only to `build/publication/`. Pass that generated path explicitly to the
license command. Remove SBOM ownership from publication configuration, tree
scanning, and the release manifest; schema version 2 records only source/tree
identity and policy, expression, and tree checks.

**Tech Stack:** Go 1.26.5, GNU Make, CycloneDX Go module generator, YAML policy,
JSON release manifest, repository iteration evidence.

## Global Constraints

- Keep root `LICENSE`, `NOTICE`, `third_party/**/LICENSE`, and
  `quality/dependency-licenses.yaml`.
- Generated SBOM bytes may exist only below ignored `build/publication/` state.
- Dependency-license reconciliation remains fail-closed for missing, stale,
  duplicate, or incompatible module evidence.
- Keep historical release plans unchanged; update only current publication
  contracts.
- Do not change runtime, CLI, provider, session, permission, protocol, or state
  behavior.
- Final integration uses a protected topic-branch PR and squash merge.

---

### Task 1: Decouple dependency-license checks from a tracked SBOM

**Files:**
- Modify: `Makefile`
- Modify: `scripts/publication/config.go`
- Modify: `scripts/publication/config_test.go`
- Modify: `scripts/publication/main.go`
- Modify: `scripts/publication/main_test.go`
- Modify: `scripts/publication/dependencies.go`
- Modify: `scripts/publication/dependencies_test.go`
- Modify: `scripts/publication/workflow_test.go`
- Modify: `quality/publication.yaml`

**Interfaces:**
- Consumes: `checkDependencyLicenses(ctx, repoRoot, policyPath, sbomPath)`.
- Produces: CLI `publication licenses --config PATH --root ROOT --sbom PATH
  --output PATH`; `make sbom` writes `$(PUBLICATION_SBOM_GENERATED)`; `make
  license-check` passes that path explicitly.

- [ ] **Step 1: Write failing CLI and Makefile contract tests**

Add assertions equivalent to:

```go
if err := run(ctx, []string{"licenses", "--config", configPath, "--root", root, "--output", out}, stdout, stderr); err == nil || !strings.Contains(err.Error(), "--sbom is required") {
	t.Fatalf("missing explicit SBOM was accepted: %v", err)
}
if err := run(ctx, []string{"normalize-sbom", "--config", configPath}, stdout, stderr); err == nil || !strings.Contains(err.Error(), "unknown publication command") {
	t.Fatalf("retired normalizer was accepted: %v", err)
}
```

Update `TestMakefileWiresPublicationSecurityTools` to require one generated
build path, no `cmp ... sbom.cdx.json`, `license-check: sbom`, and
`licenses ... --sbom $(PUBLICATION_SBOM_GENERATED)`.

- [ ] **Step 2: Run the focused tests and observe the old contract fail**

Run:

```bash
go test ./scripts/publication -run 'TestPublicationCommandsRejectMissingFlagsAndPositionalArguments|TestMakefileWiresPublicationSecurityTools' -count=1
```

Expected: failure because `licenses` reads the repository-relative configured
SBOM, `normalize-sbom` is still registered, and the Makefile compares a tracked
file.

- [ ] **Step 3: Implement the explicit build-artifact boundary**

Change `DependencyPolicy` to:

```go
type DependencyPolicy struct {
	LicensePolicy string `yaml:"license_policy"`
}
```

Add `--sbom` only to the `licenses` command and call:

```go
report, checkErr := checkDependencyLicenses(ctx, *rootPath, policyPath, *sbomPath)
```

Remove the `normalize-sbom` command and delete
`normalizeGeneratedSBOMFile`, `normalizeGeneratedSBOM`, and their generator-only
JSON types. Make `sbom` write directly to `$(PUBLICATION_SBOM_GENERATED)` and
remove the source-tree `cmp`. Keep `license-check: sbom`, then pass the absolute
generated path with `--sbom`.

Remove `dependencies.sbom` from `quality/publication.yaml`; retain
`dependencies.license_policy`.

- [ ] **Step 4: Preserve dependency reconciliation failures**

Keep and run the existing independent fixture tests:

```bash
go test ./scripts/publication -run 'TestDependency(PolicyAndSBOMAreStrict|SBOMRequiresExactComponents|SBOMRequiresExactSPDX|SBOMCombinesDeclaredAndDetectedSPDX|LocalLicenseMustBeSafe)' -count=1
```

Expected: PASS. Remove only normalizer-specific tests; do not weaken component
or SPDX assertions.

- [ ] **Step 5: Run the complete package**

Run:

```bash
go test ./scripts/publication -count=1
```

Expected: PASS.

### Task 2: Remove SBOM identity from publication trees and manifests

**Files:**
- Modify: `scripts/publication/materialize.go`
- Modify: `scripts/publication/materialize_test.go`
- Modify: `scripts/publication/mapping_test.go`
- Modify: `scripts/publication/scan_test.go`
- Modify: `scripts/publication/inventory_test.go`
- Modify: `PUBLICATION_MANIFEST.json`
- Modify: `quality/publication.yaml`
- Modify: `quality/iteration.yaml`
- Delete: `sbom.cdx.json`

**Interfaces:**
- Consumes: publication path rules and the tracked migration mapping manifest.
- Produces: `ReleaseManifest` schema version 2 with
  `source_tree_sha256`, `tree_sha256`, `file_count`, and three pass checks;
  `scanTree(...) ([]treeFile, error)` with no SBOM digest side channel.

- [ ] **Step 1: Write failing no-SBOM tree and manifest tests**

Update fixtures so their tracked files are only README/test content,
`.gitignore`, and `docs/migration/manifest.yaml`. Expect:

```go
ReleaseManifest{
	SchemaVersion:    2,
	SourceTreeSHA256: digest,
	TreeSHA256:       digest,
	FileCount:        count,
	Checks: map[string]string{
		"expression": "pass",
		"policy":     "pass",
		"tree":       "pass",
	},
}
```

Add an old-schema case containing `sbom_sha256` and expect
`readReleaseManifest` to reject the unknown field or schema 1.

- [ ] **Step 2: Run manifest and materialization tests and observe failure**

Run:

```bash
go test ./scripts/publication -run 'Test(MaterializeCopiesOnlyIncludedRegularFiles|ReleaseManifest|CheckTree|ScanTree)' -count=1
```

Expected: failure because current tree scanning requires the configured SBOM
and manifests require schema 1 plus `sbom` fields.

- [ ] **Step 3: Simplify the publication-tree owner**

Use these exact structures:

```go
type ReleaseManifest struct {
	SchemaVersion    int               `json:"schema_version"`
	SourceTreeSHA256 string            `json:"source_tree_sha256"`
	TreeSHA256       string            `json:"tree_sha256"`
	FileCount        int               `json:"file_count"`
	Checks           map[string]string `json:"checks"`
}

type TreeSummary struct {
	SourceTreeSHA256 string
	TreeSHA256       string
	FileCount        int
}
```

Remove SBOM lookup from `scanTreeWithOptions`, change its return to
`([]treeFile, error)`, and update all callers. `checkManifestShape` requires
schema 2 and exactly `policy`, `tree`, and `expression` pass checks.

- [ ] **Step 4: Remove the tracked artifact and its classification**

Delete `sbom.cdx.json`, the `sbom` path rule in `quality/publication.yaml`, and
the `sbom.cdx.json` dependency-owner path in `quality/iteration.yaml`. Update
fixture rules so every remaining tracked path is still classified exactly once.

- [ ] **Step 5: Run the complete publication package**

Run:

```bash
go test ./scripts/publication -count=1
```

Expected: PASS.

### Task 3: Synchronize current documentation and close the iteration

**Files:**
- Modify: `docs/publication/README.md`
- Modify: `docs/publication/root-clearance.md`
- Modify: `docs/superpowers/specs/2026-08-13-untracked-sbom-design.md`
- Modify: `docs/superpowers/specs/README.md`
- Modify: `docs/superpowers/plans/2026-08-13-untracked-sbom.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**
- Consumes: the final committed behavior and exact manifest generated by Tasks
  1-2.
- Produces: current docs that describe on-demand ignored SBOM generation while
  historical 2026-08-09 release records remain unchanged.

- [ ] **Step 1: Update only current contract prose**

Replace claims of a committed deterministic SBOM with this contract:

```text
Dependency-license checks generate a current CycloneDX report below ignored
build state. The report is an intermediate verification input, not tracked
source or part of the publication-tree digest.
```

Mark the design and plan `historical` only after implementation and verification
are complete; add completion metadata and update both indexes accordingly.

- [ ] **Step 2: Run documentation and package checks**

Run:

```bash
REFERENCE_DIR="${REFERENCE_DIR:?set REFERENCE_DIR to the audited reference parent}" make docs-check
git diff --check
go test ./scripts/publication -count=1
```

Expected: all checks PASS; no missing path classification in package fixtures.

- [ ] **Step 3: Commit the candidate implementation**

Stage only the paths listed in this plan and commit:

```bash
git commit -m "chore(publication): stop tracking generated SBOM"
```

- [ ] **Step 4: Generate the exact manifest and amend the candidate**

Use the committed source object so materialization cannot read dirty or ignored
state:

```bash
task_manifest_parent="$(mktemp -d /tmp/yhc-sbom-manifest.XXXXXX)"
make publication-materialize \
  PUBLICATION_SOURCE_COMMIT="$(git rev-parse HEAD)" \
  PUBLICATION_OUTPUT="$task_manifest_parent/tree"
make publication-manifest \
  PUBLICATION_ROOT="$task_manifest_parent/tree" \
  PUBLICATION_MANIFEST_OUTPUT="$task_manifest_parent/tree/PUBLICATION_MANIFEST.json"
cp "$task_manifest_parent/tree/PUBLICATION_MANIFEST.json" PUBLICATION_MANIFEST.json
git add PUBLICATION_MANIFEST.json
git commit --amend --no-edit
```

Confirm schema 2, three pass checks, no `sbom` field, and a payload file count
one lower than the pre-change manifest.

- [ ] **Step 5: Verify the exact committed tree**

Run:

```bash
make change-plan
make verify-focused
make verify-merge
make change-evidence
make change-evidence-ready
```

Expected: clean worktree and `evidence_ready` for the exact HEAD.

- [ ] **Step 6: Publish and close remotely**

Push `codex/chore/remove-committed-sbom`, open one ready PR with the decision,
compatibility, rollback, and verification evidence, wait for all required
checks, squash merge, delete the branch, then verify workflows on final
`master`.
