# Iteration Quality S4 Defect And Boundary Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-08
**Completed:** 2026-08-09
**Plan state:** Executed for defect discovery and new-edge boundary governance; product-module acceptance remains intake-gated

> **Ownership:** fourth governance stage of the accepted
> [Iteration Quality Kernel design](../specs/2026-08-08-iteration-quality-kernel-design.md),
> consuming [S1 policy/planning](2026-08-08-iteration-quality-s1-policy-planner.md),
> [S2 verification/E2E](2026-08-08-iteration-quality-s2-verification-e2e.md),
> and [S3 context/workflow](2026-08-08-iteration-quality-s3-context-workflow.md)

**Goal:** Stop new module-boundary erosion, make long-running defect discovery
bounded and reproducible, and turn historical investigation patterns into one
privacy-safe workflow with deterministic PTY/E2E oracles.

**Architecture:** `scripts/iteration boundaries` compares repository-internal
production and test import edges at the S1 base and head, rejects only newly
introduced forbidden production edges, and reports test-only edges separately.
`verify-deep` selects longer fuzz, fault, E2E, and PTY packs from the same
policy, stops on the first reproducible divergence, and writes a sanitized
intake under the current diff digest. The defect skill owns causal reasoning;
an optional session-shape tool consumes only pre-sanitized administrative
metadata and emits aggregate counts.

**Tech Stack:** Go 1.26.5 standard `go/parser`, `go/token`, `encoding/json`,
native fuzzing, existing race/PTY/E2E oracles, Python 3 standard library for a
small metadata aggregator, tracked project skills, and Makefile gates.

## Global Constraints

- Start only after S3 has merged and current `origin/master` still contains the
  locked policy, evidence, hook, and workflow interfaces.
- The current migration queue is verified empty on 2026-08-08: zero active
  slices and deferred P44 only. This plan does not invent a product refactor or
  promote a deferred decision.
- S4 Tasks 1-6 complete defect-discovery and boundary governance. The design's
  separate requirement for one real product-slice module deepening remains
  open until intake accepts a Ready slice that actually touches a broad
  composition root or parameter object. That future slice must have its own
  exact implementation plan; it is not a blank task in this document.
- Check only repository-internal imports under
  `github.com/yuhaichuan/eino-agent`. Standard-library and external dependency
  edges are outside the first boundary policy.
- Parse production and `_test.go` files separately. Test-only edges are visible
  diagnostics but do not fail a production rule.
- Compare global edge sets at base and head. A rename or move with unchanged
  imports must not appear as a new dependency merely because the file path
  changed.
- Fail only newly introduced forbidden production edges. Existing edges are
  reported in `boundaries --all` for diagnosis and cannot be silently added to
  an exception baseline.
- `engine-runtime` and `tool-runtime` production code may not depend on
  `cli-entrypoint`, `tui-adapter`, `acp-adapter`, or `mcp-adapter`. Entrypoint
  adapters may depend on engine/tools, never the reverse. `tools` remains flat.
- A new exception requires an explicit policy review and focused negative test.
  Do not update a baseline merely to make a PR green.
- `verify-deep` is opt-in, bounded, and first-failure preserving. It diagnoses
  and creates an intake; it never edits product code or launches a speculative
  repair in the same command.
- Deep target timeout is a `fail`; missing required environment is `blocked`;
  unsupported platform is `not_applicable`. A retry cannot erase the first
  result.
- The session-shape tool must never read a Codex transcript or raw product
  session directly. It accepts only strict, pre-sanitized JSONL records with
  five allowlisted categorical fields and rejects every unknown key.
- Computer Use is supplementary only for font, pixel, OS clipboard, focus, or
  window integration after structured state, process, and PTY tests pass.
- Preserve runtime behavior. Boundary and discovery tooling changes do not
  authorize engine/TUI/provider changes.

---

## Locked Interfaces

```go
type ImportEdge struct {
	FromModule string `json:"from_module"`
	ToModule   string `json:"to_module"`
	ImportPath string `json:"import_path"`
	TestOnly   bool   `json:"test_only"`
}

type BoundaryReport struct {
	SchemaVersion          int          `json:"schema_version"`
	Base                   string       `json:"base"`
	Head                   string       `json:"head"`
	DiffDigest             string       `json:"diff_digest"`
	NewProductionEdges     []ImportEdge `json:"new_production_edges"`
	NewTestEdges           []ImportEdge `json:"new_test_edges"`
	ForbiddenNewEdges      []ImportEdge `json:"forbidden_new_edges"`
	NewFlatPackageViolations []string   `json:"new_flat_package_violations"`
}

type DeepIntake struct {
	SchemaVersion     int        `json:"schema_version"`
	DiffDigest       string     `json:"diff_digest"`
	Target           string     `json:"target"`
	Status           GateStatus `json:"status"`
	Platform         string     `json:"platform"`
	FailureLogPath   string     `json:"failure_log_path,omitempty"`
	FirstFailingSeed string     `json:"first_failing_seed,omitempty"`
}
```

Boundary CLI:

```text
iteration boundaries [--all] [--format json|markdown]
```

Default exits 1 only for new forbidden production edges or new subpackages
under a configured flat root. `--all` adds current allowed/baseline diagnostics
but has the same failure rule.

Deep CLI:

```text
iteration deep [--format json|markdown]
make verify-deep
```

It stores `build/iteration/<diff_digest>/deep-intake.json` only on the first
`fail` or required `blocked` result.

## File Structure

| File | Responsibility in this stage |
|---|---|
| `scripts/iteration/boundaries.go` | Parse, normalize, diff, and enforce internal import edges and flat roots. |
| `scripts/iteration/boundaries_test.go` | Production/test, rename, baseline, and forbidden-edge fixtures. |
| `scripts/iteration/deep.go` | Select deep targets, stop on first divergence, and write sanitized intake. |
| `scripts/iteration/deep_test.go` | Ordering, status, retry, platform, and intake privacy tests. |
| `quality/iteration.yaml` | Initial forbidden edges, flat root, and per-risk deep targets. |
| `Makefile` | `check-boundaries`, deep fuzz/fault/E2E/PTY targets, and `verify-deep`. |
| `.agents/skills/defect-investigation/SKILL.md` | Standard causal workflow and iteration-evidence handoff. |
| `.agents/skills/defect-investigation/scripts/session_shape.py` | Strict sanitized JSONL aggregation. |
| `.agents/skills/defect-investigation/scripts/test_session_shape.py` | Unknown-field, privacy, determinism, and malformed-input tests. |
| `.agents/skills/defect-investigation/templates/defect-intake.md` | Minimal reproducible intake fields. |
| `.agents/skills/defect-investigation/references/e2e-scenarios.md` | Executable test mapping plus PTY/Computer Use escalation boundary. |
| `.agents/skills/defect-investigation/references/forward-test-cases.md` | Symptom-only sanitized cases for blind workflow validation. |
| `docs/contributing/testing-strategy.md` | Deep discovery and boundary-check evidence semantics. |
| `docs/architecture/code-map.md` | Stable direction rules and checker ownership. |

### Task 1: Classify repository-internal production and test import edges

**Files:**

- Create: `scripts/iteration/boundaries.go`
- Create: `scripts/iteration/boundaries_test.go`
- Modify: `scripts/iteration/git.go`
- Modify: `scripts/iteration/git_test.go`

**Interfaces:**

- Produces: `buildBoundaryReport(ctx, plan, policy, source) (BoundaryReport,
  error)`.
- Extends the Git source with committed/current tree file listing and confined
  file reads; no shell or `go list` output parsing is required.
- Consumes S1 module package prefixes and boundary policy.

- [ ] **Step 1: Add an in-memory tree source and edge tests**

Freeze this interface:

```go
type TreeSource interface {
	ListFiles(ctx context.Context, revision string) ([]string, error)
	ReadFile(ctx context.Context, revision, name string) ([]byte, error)
}
```

Use `fstest.MapFS`-backed base/head trees and assert:

1. `engine/a.go` importing `tools` creates production edge
   `engine-runtime -> tool-runtime`;
2. the same import in `engine/a_test.go` has `test_only=true`;
3. standard and external imports are omitted;
4. comments, aliases, blank imports, and dot imports normalize to the same
   import path;
5. syntax error returns path plus parser diagnostic without source text;
6. renamed file with the same edge produces no new edge; and
7. duplicate imports across files collapse to one module edge.

- [ ] **Step 2: Add package-to-module resolution tests**

Normalize S1 package patterns by removing `./` and a terminal `/...`, prepend
the repository module path, then use the longest matching package prefix.
Reject equal-length matches owned by different modules and internal imports
with no module owner.

```go
func moduleForImport(policy Policy, importPath string) (string, error)
```

Assert `github.com/yuhaichuan/eino-agent/internal/tui/render` resolves to
`tui-adapter`, while `github.com/yuhaichuan/eino-agent/engine/session` resolves
to `engine-runtime`.

- [ ] **Step 3: Run tests and verify red**

```bash
go test ./scripts/iteration -run 'Boundary|ImportEdge|ModuleForImport' -count=1
```

Expected: FAIL because import classification is absent.

- [ ] **Step 4: Implement import-only parsing and global set difference**

Parse with:

```go
file, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly)
```

Use `strconv.Unquote` for each import. Build base and head edge sets keyed by
`fromModule + "\x00" + toModule + "\x00" + importPath + "\x00" + testOnly`.
Sort all report slices by from, to, import path, then test-only. Skip deleted
head files; never open paths outside the repository tree source.

- [ ] **Step 5: Add real Git tree support**

For a committed revision use fixed commands:

```text
git ls-tree -r -z --name-only <revision> --
git show <revision>:<validated-repository-relative-path>
```

For the current tracked tree, read only changed files through `os.Root` and use
`git ls-files -z` for the file list. Cap individual Go source at 8 MiB and total
parsed source at 256 MiB. Store no source bytes after parsing.

- [ ] **Step 6: Run tests and commit**

```bash
go test ./scripts/iteration -run 'Boundary|ImportEdge|ModuleForImport|TreeSource' -count=1
git add scripts/iteration/boundaries.go scripts/iteration/boundaries_test.go scripts/iteration/git.go scripts/iteration/git_test.go
git commit -m "feat(quality): classify internal import edges"
```

### Task 2: Reject new forbidden production edges and flat tools packages

**Files:**

- Modify: `quality/iteration.yaml`
- Modify: `scripts/iteration/boundaries.go`
- Modify: `scripts/iteration/boundaries_test.go`
- Modify: `scripts/iteration/main.go`
- Modify: `scripts/iteration/main_test.go`
- Modify: `Makefile`

**Interfaces:**

- Produces `iteration boundaries`, `make check-boundaries`, and merge evidence
  target `check-boundaries`.
- Changes policy only through the already locked
  `forbidden_production_edges` and `flat_package_roots` fields.

- [ ] **Step 1: Add negative and positive policy tests**

Seed a head tree with:

```go
// engine/forbidden.go
package engine
import _ "github.com/yuhaichuan/eino-agent/internal/tui"
```

Assert default boundaries exits 1 and reports exactly
`engine-runtime -> tui-adapter`. Move the import to
`engine/forbidden_test.go`; assert exit 0 and one visible test-only edge. Put
the same forbidden production edge in both base and head; assert it is current
diagnostic only and does not fail as new.

Seed `tools/nested/new.go`; assert a new flat-root violation. Seed an existing
base subdirectory in the fixture; report but do not newly fail it.

- [ ] **Step 2: Set the initial policy**

```yaml
boundaries:
  forbidden_production_edges:
    - from: [engine-runtime, tool-runtime]
      to: [cli-entrypoint, tui-adapter, acp-adapter, mcp-adapter]
  flat_package_roots: [tools]
```

Add `check-boundaries` to the base required targets for every non-documentation
plan. It is a built-in Make target and must be allowlisted by S1/S2 target
validation.

- [ ] **Step 3: Implement enforcement and CLI rendering**

Match exact module IDs, not path prefixes. A forbidden production edge appears
in both `NewProductionEdges` and `ForbiddenNewEdges`. Test-only edges never
appear in `ForbiddenNewEdges`. Markdown names stable modules/imports but not
source excerpts or line numbers.

Add:

```make
check-boundaries:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) boundaries
```

Add the target to `.PHONY` and to S2's allowlisted runner map.

- [ ] **Step 4: Run focused tests and commit**

```bash
go test ./scripts/iteration -run 'Boundaries|Forbidden|FlatPackage' -count=1
make check-boundaries
git add Makefile quality/iteration.yaml scripts/iteration/boundaries.go scripts/iteration/boundaries_test.go scripts/iteration/main.go scripts/iteration/main_test.go
git commit -m "feat(quality): block new module boundary erosion"
```

### Task 3: Add bounded first-failure deep verification

**Files:**

- Create: `scripts/iteration/deep.go`
- Create: `scripts/iteration/deep_test.go`
- Modify: `scripts/iteration/main.go`
- Modify: `scripts/iteration/main_test.go`
- Modify: `quality/iteration.yaml`
- Modify: `Makefile`

**Interfaces:**

- Produces `iteration deep` and `make verify-deep`.
- Reuses S2 `TargetRunner`, `EvidenceStore`, and first-result immutability.
- Writes only the locked `DeepIntake` schema on first fail/required block.

- [ ] **Step 1: Add deep-selection and stop-order tests**

For an engine+TUI plan, freeze this selected order:

```text
check-boundaries
test-fault-injection
test-fuzz-deep
test-e2e-deep
test-pty-deep
```

For documentation-only, select no deep targets. For engine-only, omit PTY.
For Windows, retain `test-pty-deep` as `not_applicable` and continue. On the
first `fail` or required `blocked`, execute no later target and write one
intake. A later passing retry must not replace it.

- [ ] **Step 2: Add intake privacy and confinement tests**

Feed the same six synthetic privacy markers used by S3 through fake runner
internals. Assert the intake contains only digest, target, status, platform,
relative log path, and validated fuzz seed. Reject a symlink target, invalid
digest, second JSON document, and overwrite of an existing first-failure
intake.

- [ ] **Step 3: Add exact deep Make targets**

```make
TEST_FUZZ_DEEP_TIME ?= 30s
TEST_DEEP_TIMEOUT ?= 10m

test-fault-injection:
	$(GO) test ./engine -run '^(TestScenarioInterruptionDuringToolExecutionPropagatesCancellation|TestP234aRestoreStagingCommitAbortRace|TestQueryMalformedToolArgsYieldsErrorToolResult|TestP138ProjectGraphInterruptResumeExecutesToolExactlyOnce)$$' -count=1 -timeout=$(TEST_DEEP_TIMEOUT)

test-fuzz-deep:
	$(GO) test ./engine/commands -run '^$$' -fuzz '^FuzzParseCommandInput$$' -fuzztime=$(TEST_FUZZ_DEEP_TIME) -timeout=$(TEST_DEEP_TIMEOUT)
	$(GO) test ./internal/tui -run '^$$' -fuzz '^FuzzWidthProfileClusterAndControlPreservation$$' -fuzztime=$(TEST_FUZZ_DEEP_TIME) -timeout=$(TEST_DEEP_TIMEOUT)
	$(GO) test ./internal/tui -run '^$$' -fuzz '^FuzzP272AnnotationRoundTrip$$' -fuzztime=$(TEST_FUZZ_DEEP_TIME) -timeout=$(TEST_DEEP_TIMEOUT)

test-e2e-deep: $(E2E_BINARY)
	EINO_E2E_BINARY=$(abspath $(E2E_BINARY)) $(GO) test ./scripts/e2e -count=3 -timeout=$(TEST_DEEP_TIMEOUT)

test-pty-deep:
	@if [[ "$$($(GO) env GOOS)" == "windows" ]]; then \
		echo "test-pty-deep is Unix-only"; \
	else \
		$(GO) test ./cmd/eino-agent/cmd ./internal/tui -run '^(TestP245aPlainGoalWorkflowPTY|TestTUITerminalRestorationPTY|TestTUIWorkflowPTY)$$' -count=3 -timeout=$(TEST_DEEP_TIMEOUT); \
	fi

verify-deep:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) deep
```

Verify every selector through `go test -list` before editing. Do not replace a
missing exact symbol with a broad package run.

- [ ] **Step 4: Map risks to deep targets**

```yaml
risk_packs:
  contract:
    deep_targets: [test-fault-injection, test-e2e-deep]
  concurrency:
    deep_targets: [test-fault-injection, test-e2e-deep]
  terminal:
    deep_targets: [test-pty-deep, test-e2e-deep]
  fuzz:
    deep_targets: [test-fuzz-deep]
  e2e:
    deep_targets: [test-e2e-deep]
```

Retain each pack's existing `target` and `platforms`; the snippet shows only the
new field. Deduplicate selected deep targets while preserving the fixed global
order.

- [ ] **Step 5: Implement deep orchestration**

Use S2 runner/store semantics with level `deep`. Always start with
`check-boundaries`, then append selected targets. A pass produces deep evidence
but does not change merge `evidence_ready`; deep is an independent opt-in
class. Fail/block writes the intake atomically and returns non-zero.

- [ ] **Step 6: Run tests and commit**

```bash
go test ./scripts/iteration -run 'Deep|DeepIntake|DeepSelection' -count=1
make verify-deep
git add Makefile quality/iteration.yaml scripts/iteration/deep.go scripts/iteration/deep_test.go scripts/iteration/main.go scripts/iteration/main_test.go
git commit -m "feat(quality): add bounded deep verification"
```

### Task 4: Standardize defect intake and sanitized session-shape aggregation

**Files:**

- Modify: `.agents/skills/defect-investigation/SKILL.md`
- Create: `.agents/skills/defect-investigation/scripts/session_shape.py`
- Create: `.agents/skills/defect-investigation/scripts/test_session_shape.py`
- Create: `.agents/skills/defect-investigation/templates/defect-intake.md`
- Modify: `.agents/skills/defect-investigation/references/e2e-scenarios.md`

**Interfaces:**

- `session_shape.py --input <sanitized.jsonl> [--input ...] --output json|markdown`.
- Each input line permits only `entrypoint`, `tool_kind`, `event_kind`,
  `terminal_reason`, and `transition`, each a stable string atom of at most 64
  characters.
- Output contains sorted counts only; it does not include session IDs or source
  paths.

- [ ] **Step 1: Add aggregator tests before implementation**

Use `unittest` and temporary files to prove:

- repeated categorical records produce deterministic sorted counts;
- malformed JSON, arrays, unknown keys, missing keys, whitespace-bearing atoms,
  more than 100,000 records, or a line over 4 KiB fails;
- prompt/response/title/transcript/credential/environment/source/argv keys are
  rejected; and
- synthetic privacy markers never occur in stdout, stderr, or an output file.

Expected JSON:

```json
{
  "schema_version": 1,
  "records": 3,
  "counts": {
    "entrypoint": {"headless.exec": 2, "tui": 1},
    "tool_kind": {"Bash": 1, "Edit": 1, "Write": 1},
    "event_kind": {"terminal": 1, "tool_result": 2},
    "terminal_reason": {"success": 3},
    "transition": {"started-to-terminal": 3}
  }
}
```

- [ ] **Step 2: Run tests and verify red**

```bash
python3 .agents/skills/defect-investigation/scripts/test_session_shape.py
```

Expected: FAIL because the script is absent.

- [ ] **Step 3: Implement strict streaming aggregation**

Read line by line, reject unknown or missing fields before counting, validate
atoms with `^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`, use `collections.Counter`, and
sort both dimensions and values before rendering. Refuse stdin by default so a
raw transcript cannot be accidentally piped; require explicit `--input` paths.
Write no cache or side file.

- [ ] **Step 4: Replace defect workflow duplication with an exact intake**

Keep the causal loop:

```text
freeze symptom -> reproduce one independent oracle -> falsify hypotheses
-> identify one owner -> add the lowest stable red test -> repair minimally
-> make verify-focused -> commit -> make verify-merge -> evidence handoff
```

The template contains only:

```markdown
# Defect Intake

- Stable ID:
- Current base/head/diff digest:
- Observable symptom:
- Smallest reproducer command or scenario ID:
- Independent oracle:
- First failing gate/seed/log path:
- Primary owner and affected entrypoints:
- Permission/cancellation/persistence/concurrency risk:
- Cleanup result:
- Regression-test owner:
- Blocked/not-applicable evidence:
```

It explicitly forbids prompts, source dumps, transcripts, credentials,
environment dumps, and speculative fixes before localization.

- [ ] **Step 5: Map the existing S1-S10 catalog to executable packs**

For each scenario row, add columns `Automated owner`, `Primary command`, and
`Supplementary only`. Map permission/cancel/restart/failover/malformed/concurrent
settlement to the S2 exact test names; map terminal lifecycle to PTY; mark font,
clipboard, focus, and window integration as Computer Use supplementary. Do not
claim a scenario automated if only prose exists.

- [ ] **Step 6: Validate and commit**

```bash
python3 .agents/skills/defect-investigation/scripts/test_session_shape.py
python3 .agents/skill-runtime/validate_skills.py
git add .agents/skills/defect-investigation
git commit -m "feat(skills): standardize defect investigation intake"
```

### Task 5: Forward-test the defect skill and PTY/Computer Use escalation

**Files:**

- Create: `.agents/skills/defect-investigation/references/forward-test-cases.md`
- Modify: `.agents/skills/defect-investigation/references/e2e-scenarios.md`
- Modify: `docs/contributing/testing-strategy.md`

**Interfaces:**

- Produces three sanitized symptom-only forward-test cases.
- Human/agent validation output remains ephemeral or under ignored
  `build/iteration/`; it does not commit child transcripts.

- [ ] **Step 1: Add three blind cases without causes or repairs**

Use these exact cases:

1. `permission-late-write`: a rejected write appears after terminal
   cancellation; oracle is target-file absence after barrier-controlled
   quiescence.
2. `resume-duplicate-dispatch`: a fresh-process resume sends the same tool call
   twice; oracle is provider request count plus exact marker hash/line count.
3. `pty-child-leak`: CLI exits after cancel but a child remains and terminal
   mode is not restored; oracle is child PID disappearance plus termios state.

Each case gives only fixture/setup, symptom, allowed artifacts, and oracle. It
must not reveal the known owner, cause, expected code change, or prior repair.

- [ ] **Step 2: Run one bounded blind delegation per case**

Use a read-only explorer for localization. Require its result to start with
`ADMISSION: ACCEPT|DECLINE`, name one primary owner, list falsified hypotheses,
and propose the lowest stable regression seam. Do not give it source excerpts
or the known answer. Record child completion and parent adoption separately
through `skill-runtime`.

- [ ] **Step 3: Parent-score the workflow**

For each case, require:

- correct observable boundary before code search;
- independent oracle preserved;
- no timing sleep proposed as synchronization;
- one owner rather than a broad refactor;
- cleanup and first-failure retention;
- correct focused/merge/deep evidence separation; and
- no privacy marker or transcript export.

Revise the skill only for repeated routing failures across at least two cases;
do not encode one case's answer into generic instructions.

- [ ] **Step 4: Freeze the PTY-to-Computer-Use ladder**

Document this exact order:

```text
typed/runtime state -> package contract -> real process -> PTY bytes/modes
-> Computer Use only for remaining OS/window/pixel claim
```

Computer Use acceptance must record platform/app version, scenario ID, visible
claim, redacted screenshot when authorized, and cleanup. It cannot promote a
failed or blocked structured gate.

- [ ] **Step 5: Commit forward-test assets**

```bash
git add .agents/skills/defect-investigation/references/forward-test-cases.md .agents/skills/defect-investigation/references/e2e-scenarios.md docs/contributing/testing-strategy.md
git commit -m "docs(testing): add blind defect workflow validation"
```

### Task 6: Close the governance scope without inventing a product slice

**Files:**

- Modify: `docs/architecture/code-map.md`
- Modify: `docs/contributing/testing-strategy.md`
- Modify: `docs/contributing/verification.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- Records S4 governance evidence and the separate product-module acceptance
  gate.
- Leaves `docs/migration/queue.yaml` unchanged while it has zero active slices.

- [ ] **Step 1: Document stable module direction rules**

In `code-map.md`, name module IDs, stable package/path owners, permitted adapter
direction, flat tools rule, and `scripts/iteration boundaries` as the executable
no-new-edge oracle. Do not paste a generated edge inventory or line-number
anchors.

- [ ] **Step 2: Verify the queue remains a valid zero-active terminal state**

```bash
go run ./scripts/migration_queue check
go run ./scripts/migration_queue print
```

Expected: zero accepted active slices and deferred P44 only. If a Ready slice
exists by implementation time, stop this closeout, read its accepted contract,
and create a separate exact plan for its own behavior before any module
deepening.

- [ ] **Step 3: Record the non-substitutable acceptance boundary**

The plan index and verification docs must say:

```text
Boundary enforcement and deep defect discovery are implemented. Overall S4
product-module acceptance remains open until an accepted Ready product slice
touches a broad owner and demonstrates behavior-preserving deepening in its own
test-first plan. Tooling cleanup, file-size reduction, or a deferred gap does
not satisfy that gate.
```

This is a terminal honest state, not a backlog row and not evidence that P44 is
ready.

- [ ] **Step 4: Run self-review scans**

```bash
rg -n 'TBD|TODO|FIXME|some test|add tests|unknown owner' docs/superpowers/plans/2026-08-08-iteration-quality-s4-defect-boundaries.md | rg -v 'rg -n'
rg -n 'forbidden_production_edges|flat_package_roots|deep_targets|DeepIntake|GateNotApplicable' docs/superpowers/plans/2026-08-08-iteration-quality-s*.md
git diff --check
```

Expected: no actionable incomplete-work marker and consistent shared names/types.

- [ ] **Step 5: Run focused, deep, and final gates**

```bash
go test ./scripts/iteration -count=1
python3 .agents/skills/defect-investigation/scripts/test_session_shape.py
python3 .agents/skill-runtime/validate_skills.py
make check-boundaries
make verify-deep
make test-e2e
make test-race
make test-pty
make docs-check
make fmt
make lint
make test
make build
git diff --check
```

Expected: every applicable command passes; any deep first failure remains in
its intake and blocks a green claim until investigated separately. Computer Use
is reported only if an authorized UI-only claim was actually exercised.

- [ ] **Step 6: Commit governance closeout and prepare the S4 PR**

```bash
git add docs/architecture/code-map.md docs/contributing/testing-strategy.md docs/contributing/verification.md docs/superpowers/plans/README.md
git commit -m "docs(quality): close defect and boundary governance"
```

The PR must distinguish implemented boundary/discovery behavior from the
intake-gated product-module demonstration, list exact new-edge and test-only
fixtures, report deep/PTY/E2E/platform evidence separately, and state that the
empty migration queue was preserved rather than repopulated for process optics.
