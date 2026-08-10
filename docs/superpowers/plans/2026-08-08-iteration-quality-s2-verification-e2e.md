# Iteration Quality S2 Verification And E2E Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-08
**Completed:** 2026-08-09
**Plan state:** Executed; diff-bound focused and merge evidence, advisory coverage, and deterministic real-binary E2E

> **Ownership:** second executable stage of the accepted
> [Iteration Quality Kernel design](../specs/2026-08-08-iteration-quality-kernel-design.md),
> consuming the locked interfaces in the
> [S1 policy and planner plan](2026-08-08-iteration-quality-s1-policy-planner.md)

**Goal:** Execute the smallest risk-matched checks during development and a
complete local merge gate against a committed diff, while adding a hermetic
real-binary correctness pack that prevents known permission, cancellation,
recovery, failover, malformed-input, and entrypoint regressions.

**Architecture:** `scripts/iteration` gains one target runner and one atomic
evidence store under the ignored `build/iteration/<diff_digest>/` directory.
Focused verification executes changed-package tests plus selected fast packs.
Merge verification requires a clean topic-branch tree, runs formatting first,
recomputes the plan, then executes the four repository gates and every
applicable risk pack. A separate `scripts/e2e` test harness owns disposable
repositories and a scripted local provider; it shares only process-tree
ownership with `scripts/evaluation`, not evaluation reports or product claims.

**Tech Stack:** Go 1.26.5 standard `testing`, `testing/fstest`, native fuzzing,
`context`, `os/exec`, `os.Root`, `net/http/httptest`, existing Unix PTY tests,
existing Windows job-object support, `gotestsum`, and Makefile gates. No BDD,
assertion, snapshot, or mocking framework is added.

## Global Constraints

- Start only after S1's JSON schema, base/digest semantics, classifier, and
  initial policy are merged and re-read from current `origin/master`.
- Keep `make verify` unchanged. Add `verify-focused`, `verify-merge`, and
  `test-e2e` as separate targets.
- Focused evidence is useful feedback, not merge proof. Only a current,
  committed-tree `verify-merge` invocation may produce `evidence_ready`.
- Before merge verification, the current branch must not be `master`, Git must
  have no merge/rebase in progress, and the tracked worktree/index must match
  `HEAD`. Untracked files remain outside scope and are not deleted.
- Merge verification runs `make fmt` first. If formatting changes the tracked
  tree, record `head-tree-clean=blocked`, stop, commit the formatting change,
  and rerun. Never certify an uncommitted post-format tree for pre-push use.
- After formatting, rebuild the S1 plan. Every later result is stored under the
  recomputed digest. Results for the pre-format digest cannot be promoted.
- A gate has only `pass`, `fail`, `blocked`, or `not_applicable`. A missing
  executable, parent cancellation, or required but unreadable input is not
  silently green. An absent optional local `.reference` directory makes only
  the reference-dependent `docs-check` target `not_applicable`; the runner must
  not start that target, and `docs-check-ci` still executes. Test timeout and
  non-zero test/build exit are `fail`, not infrastructure `blocked`.
- The first executed result for a target/digest/level is immutable. A diagnostic
  retry may create a local retry log, but cannot replace `fail` with `pass`.
- Evidence stores target name, level, status, exit code, duration, bounded
  failure-log path, and first failing seed only. It never stores argv, prompt,
  source, transcript, environment, credential, or full output.
- Full command logs remain ignored local artifacts, use mode `0600`, are capped
  at 2 MiB per target, and are referenced only for failures or blocks.
- `make eval-baseline` remains opt-in and outside `verify`, required CI, release
  builds, `verify-focused`, `verify-merge`, and `test-e2e`.
- Computer Use and live-provider runs are not part of S2 merge evidence.
- `testscript` and `goleak` remain unadded. Introduce either only in a later
  focused pilot with a missing oracle, not as a syntax migration.

---

## Locked Interfaces

S2 consumes S1's `Plan`, `Evidence`, `GateEvidence`, `GateStatus`, and exact
state strings. Add only these execution interfaces:

```go
type VerifyLevel string

const (
	VerifyFocused VerifyLevel = "focused"
	VerifyMerge   VerifyLevel = "merge"
)

type RunResult struct {
	Status           GateStatus
	ExitCode         *int
	DurationMillis   int64
	FailureLogPath   string
	FirstFailingSeed string
}

type TargetRunner interface {
	Run(ctx context.Context, root, digest, target string) RunResult
}

type EvidenceStore interface {
	Load(plan Plan) (Evidence, error)
	Record(plan Plan, gate GateEvidence) (Evidence, error)
}

type VerifyOptions struct {
	Level VerifyLevel
	Plan  Plan
}

func verify(
	ctx context.Context,
	root string,
	options VerifyOptions,
	runner TargetRunner,
	store EvidenceStore,
	replan func(context.Context) (Plan, error),
) (Evidence, error)

func targetApplicability(root, target, goos string) (GateStatus, bool, error)
```

`targetApplicability` returns `(GateNotApplicable, false, nil)` only for a
policy-proven platform/input boundary, `(GateBlocked, false, err)` when an
applicable prerequisite cannot be inspected, and `("", true, nil)` when the
runner must execute the target. It never infers applicability from command
stderr.

The store path is fixed:

```text
build/iteration/<diff_digest>/plan.json
build/iteration/<diff_digest>/evidence.json
build/iteration/<diff_digest>/logs/<safe-target>.log
build/iteration/<diff_digest>/coverage.json
```

`safe-target` is derived from the allowlisted target name by replacing `/`
with `-`; no user-provided path enters the artifact filename.

## File Structure

| File | Responsibility in this stage |
|---|---|
| `scripts/iteration/store.go` | Confined atomic evidence load/record and immutable-first-result rule. |
| `scripts/iteration/runner.go` | Fixed target-to-command mapping and bounded logs. |
| `scripts/iteration/verify.go` | Focused and merge orchestration/state promotion. |
| `scripts/iteration/coverage.go` | Advisory changed-package coverage extraction. |
| `scripts/iteration/*_test.go` | Runner ordering, failure semantics, invalidation, and coverage tests. |
| `scripts/internal/ownedprocess/` | Shared cross-platform whole-process-tree runner. |
| `scripts/evaluation/process*.go` | Replace local process implementation with the shared package. |
| `scripts/e2e/harness_test.go` | Disposable repo, bounded output, binary invocation, cleanup. |
| `scripts/e2e/provider_test.go` | Scripted local Responses-compatible provider and request journal. |
| `scripts/e2e/scenarios_test.go` | Hermetic correctness scenarios and independent oracles. |
| `scripts/e2e/testdata/` | Minimal fixture repos and provider step manifests. |
| `Makefile` | `verify-focused`, `verify-merge`, and `test-e2e` wrappers. |
| `quality/iteration.yaml` | Add deterministic E2E risk selection after the target exists. |
| `docs/contributing/testing-strategy.md` | Record pack purpose, seam, and non-claims. |
| `docs/contributing/verification.md` | Record focused versus merge evidence lifecycle. |

### Task 1: Persist gate evidence without allowing a retry to rewrite history

**Files:**

- Create: `scripts/iteration/store.go`
- Create: `scripts/iteration/store_test.go`

**Interfaces:**

- Produces: `fileEvidenceStore` implementing `EvidenceStore`.
- Consumes: S1's exact `Plan` and `Evidence` schemas.
- Preserves: artifacts remain under the repository-owned `build/iteration`
  root even when the digest or target is malicious.

- [ ] **Step 1: Add lifecycle and immutability tests**

Write table tests for these transitions:

```text
changed + all focused pass                 -> focused_verified
changed + one focused blocked              -> changed
focused_verified + all merge pass/N/A      -> evidence_ready
focused_verified + one merge fail          -> focused_verified
merge_verified + evidence serialization OK -> evidence_ready
new diff digest                            -> new changed evidence
```

Record an initial `blocked` target, then `fail`, then attempt to record `pass`.
Assert the final stored target remains the first executed `fail`. An initial
unexecuted `blocked` may be replaced once by the first actual `pass` or
`fail`; an environment-derived `blocked` with a duration is an executed result
and is immutable.

- [ ] **Step 2: Add confinement and atomicity tests**

Reject digests other than 64 lowercase hex characters and targets containing
path separators after normalization. Seed:

- a symlink at `build/iteration/<digest>` pointing outside the repository;
- a symlink evidence file;
- a second JSON document;
- an unknown JSON field;
- a mismatched embedded `plan.diff_digest`; and
- a simulated failure before atomic rename.

Assert outside content is unchanged and the prior evidence remains readable.

- [ ] **Step 3: Run focused tests and verify red**

```bash
go test ./scripts/iteration -run 'EvidenceStore|EvidenceTransition' -count=1
```

Expected: FAIL because the store does not exist.

- [ ] **Step 4: Implement confined atomic recording**

Use `os.OpenRoot`, create directories with `0700`, files with `0600`, encode to
a sibling temporary file, `Sync`, close, and rename inside the same root. Decode
with `DisallowUnknownFields` and require EOF.

Use this replacement rule:

```go
func mayReplace(existing, next GateEvidence) bool {
	if existing.Target != next.Target || existing.Level != next.Level {
		return false
	}
	return existing.Status == GateBlocked &&
		existing.DurationMillis == 0 && existing.ExitCode == nil &&
		(next.Status == GatePass || next.Status == GateFail ||
			next.Status == GateBlocked)
}
```

State derivation must examine required target names from the embedded plan and
the focused target set from the current invocation. It must never trust a
caller-supplied state string.

- [ ] **Step 5: Run tests and commit**

```bash
go test ./scripts/iteration -run 'EvidenceStore|EvidenceTransition' -count=1
git add scripts/iteration/store.go scripts/iteration/store_test.go
git commit -m "feat(quality): persist immutable gate evidence"
```

### Task 2: Execute only fixed, risk-selected targets

**Files:**

- Create: `scripts/iteration/runner.go`
- Create: `scripts/iteration/runner_test.go`

**Interfaces:**

- Produces: `commandTargetRunner` implementing `TargetRunner`.
- Consumes stable target names; arbitrary argv is not a public interface.
- Special built-in target: `git-diff-check` runs
  `git diff --check <base>..HEAD --` without a shell.

- [ ] **Step 1: Add exact target-mapping tests**

Freeze the mapping through an injectable command factory:

```go
var makeTargets = map[string]time.Duration{
	"fmt":             3 * time.Minute,
	"lint":            10 * time.Minute,
	"test":            15 * time.Minute,
	"build":           15 * time.Minute,
	"docs-check":      5 * time.Minute,
	"docs-check-ci":   5 * time.Minute,
	"test-contract":   5 * time.Minute,
	"test-race":       10 * time.Minute,
	"test-pty":        5 * time.Minute,
	"test-fuzz-smoke": 5 * time.Minute,
	"test-e2e":        10 * time.Minute,
}
```

Assert each target produces `make <target>` with no policy-supplied tokens.
Construct `commandTargetRunner` with a map derived from S1
`Plan.FocusedChecks`. Assert `focused/engine-runtime` becomes
`go test ./engine/... -count=1` and `focused/governance` becomes
`go test ./scripts/iteration -count=1`. Unknown targets are `blocked` before
process start.

- [ ] **Step 2: Add status-classification tests**

Use fake processes to prove:

- exit 0 -> `pass`;
- exit 1 -> `fail` with exit code 1;
- target deadline -> `fail`;
- parent cancellation before or during start -> `blocked`;
- executable unavailable -> `blocked`;
- output overflow -> `fail`; and
- a failing fuzz result extracts only a validated seed token such as
  `FuzzWidthProfileClusterAndControlPreservation/0123456789abcdef`.

No test should require timing sleeps; signal start and completion with
channels.

- [ ] **Step 3: Implement bounded command execution**

Keep command details in memory. Stream at most 2 MiB of combined output to a
mode-`0600` log and discard excess after setting a truncation flag. On pass,
remove the log; on fail or block, keep only its repository-relative path in
evidence.

Do not inherit all environment variables. Start from `os.Environ()` only for
repository gates that already own provider-independent execution, delete known
credential variables before spawning, and preserve only variables required by
the Makefile/toolchain. The E2E harness defines an even smaller environment in
Task 5.

- [ ] **Step 4: Run focused tests and commit**

```bash
go test ./scripts/iteration -run 'TargetRunner|TargetMapping|FuzzSeed' -count=1
git add scripts/iteration/runner.go scripts/iteration/runner_test.go
git commit -m "feat(quality): run allowlisted verification targets"
```

### Task 3: Implement focused and committed-tree merge verification

**Files:**

- Create: `scripts/iteration/verify.go`
- Create: `scripts/iteration/verify_test.go`
- Modify: `scripts/iteration/main.go`
- Modify: `scripts/iteration/main_test.go`
- Modify: `Makefile`

**Interfaces:**

- Produces CLI: `verify --level focused|merge`.
- Produces Make targets: `verify-focused` and `verify-merge`.
- Preserves: S1 flags `--policy`, `--base`, `--head`, `--slice-id`, and
  `--format`; verification output is always a final JSON or Markdown evidence
  view after writing the structured store.

- [ ] **Step 1: Add focused-selection tests**

For an engine plus ACP plan, assert deterministic execution order:

```text
focused/acp-adapter
focused/engine-runtime
test-contract
```

The focused level runs each affected package once and only the cheapest
immediately relevant non-race/non-PTY pack. Select `test-contract` when present;
select `test-fuzz-smoke` only for the `fuzz` risk. Defer `test-race`, `test-pty`,
full `test`, `lint`, and `build` to merge level.

A governance-only `scripts/iteration` plan runs:

```text
focused/governance
docs-check-ci
```

This is the policy/tool self-test required by the accepted design; full `test`
remains a merge-level target. A documentation-only plan runs `docs-check-ci`
and `git-diff-check`; it does not run Go package tests.

- [ ] **Step 2: Add merge-order and clean-tree tests**

Freeze this order for code/governance/dependency diffs:

```text
head-tree-clean
fmt
<replan>
head-tree-clean
lint
test
build
docs-check-ci
docs-check
git-diff-check
<remaining selected risk targets in lexical order>
```

Before dispatching `docs-check`, call `os.Lstat(<root>/.reference)`. If the path
does not exist, record `docs-check=not_applicable` without invoking the runner
and continue with the other targets. Any other stat error is `blocked`; if the
path exists, run `make docs-check` and classify its exit normally. On Windows,
`test-pty` is `not_applicable` through the same pre-dispatch mechanism. If the
first clean-tree check fails, execute no target. If `fmt` changes the tree,
record the second `head-tree-clean` as `blocked`, execute nothing else, and
require commit plus rerun.

For documentation-only changes, run only `docs-check-ci`, conditionally
`docs-check`, and `git-diff-check`. For any target `fail` or required `blocked`,
stop immediately; retain later targets as unexecuted `blocked`.

- [ ] **Step 3: Add invalidation and first-failure tests**

Have `replan` return a new digest after `fmt`. Assert no result from the old
digest appears in the new store. Fail `test-race`, then make a diagnostic retry
pass, and assert the evidence stays failed with the first log/seed.

Add an integration fixture with no `.reference`: assert `docs-check-ci` and
`git-diff-check` execute, `docs-check` does not execute, its evidence is
`not_applicable`, and the remaining successful required gates can still reach
`evidence_ready`. Add a second fixture whose `.reference` stat returns a
permission error; assert `docs-check=blocked` and no green promotion.

- [ ] **Step 4: Implement orchestration**

Use an explicit target slice; do not encode order in map iteration:

```go
func mergeTargets(plan Plan) []string {
	base := []string{"lint", "test", "build", "docs-check-ci", "docs-check", "git-diff-check"}
	if documentationOnly(plan) {
		base = []string{"docs-check-ci", "docs-check", "git-diff-check"}
	}
	return appendUniqueInOrder(base, sortedRiskTargets(plan)...)
}
```

`fmt` and the two clean-tree checks are orchestration prerequisites, not
policy-supplied commands. Rebuild and persist the post-format plan before any
other gate. Promote to `merge_verified`, then `evidence_ready` only after the
atomic evidence write succeeds and every required merge gate is pass or
not-applicable.

- [ ] **Step 5: Add CLI and Make wrappers**

```make
verify-focused:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) verify --level focused

verify-merge:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) verify --level merge
```

Add both to `.PHONY`. Keep `verify: fmt-check lint test build` byte-for-byte
unchanged.

- [ ] **Step 6: Run tests and commit**

```bash
go test ./scripts/iteration -run 'VerifyFocused|VerifyMerge|Invalidat|FirstFailure' -count=1
git add Makefile scripts/iteration/main.go scripts/iteration/main_test.go scripts/iteration/verify.go scripts/iteration/verify_test.go
git commit -m "feat(quality): add focused and merge verification"
```

### Task 4: Extract process-tree ownership without changing evaluation behavior

**Files:**

- Create: `scripts/internal/ownedprocess/process.go`
- Create: `scripts/internal/ownedprocess/process_unix.go`
- Create: `scripts/internal/ownedprocess/process_windows.go`
- Create: `scripts/internal/ownedprocess/process_other.go`
- Create: `scripts/internal/ownedprocess/process_test.go`
- Modify: `scripts/evaluation/process.go`
- Modify: `scripts/evaluation/process_unix.go`
- Modify: `scripts/evaluation/process_windows.go`
- Modify: `scripts/evaluation/process_other.go`
- Modify: `scripts/evaluation/runner.go`
- Modify: `scripts/evaluation/main_test.go`

**Interfaces:**

- Produces: `ownedprocess.Run(ctx context.Context, command *exec.Cmd) error`.
- Produces: `ownedprocess.Code(err error) string` with exactly
  `process_tree_unavailable`, `process_start_failed`, `process_failed`,
  `process_tree_close_failed`, `process_timeout`, or `process_canceled`.
- Preserves: evaluation's existing external behavior, report codes, whole-tree
  termination, Windows suspended-process admission, and cleanup precedence.

- [ ] **Step 1: Add characterization tests at the new public seam**

On Unix, start a child that starts a grandchild and writes its PID to a file.
Cancel the context, assert `Code(err) == "process_canceled"`, and poll the
process table through a bounded retry until both PIDs are gone. Add a success
case and a timeout case. On Windows, retain the existing job-object tests or
add the equivalent child/grandchild fixture behind build tags. Unsupported
platforms must return `process_tree_unavailable`, not fall back to direct-child
kill.

- [ ] **Step 2: Move the implementation with only package/name changes**

The shared package owns its typed error:

```go
type Error struct {
	code string
	err  error
}

func (err *Error) Error() string { return err.code + ": " + err.err.Error() }
func (err *Error) Unwrap() error { return err.err }
func Code(err error) string {
	var owned *Error
	if errors.As(err, &owned) {
		return owned.code
	}
	return ""
}
```

Keep the current terminate, two-second grace, kill, wait, and close order.
Evaluation maps `ownedprocess.Code(err)` back through its existing `fail`
helper so its JSON report and tests do not change.

- [ ] **Step 3: Prove evaluation parity**

```bash
go test ./scripts/internal/ownedprocess ./scripts/evaluation -count=1
make eval-baseline
```

Expected: unit tests pass and the opt-in hermetic baseline still passes with the
same report schema. This invocation validates parity only; it does not make
`eval-baseline` a required merge gate.

- [ ] **Step 4: Commit the shared process seam**

```bash
git add scripts/internal/ownedprocess scripts/evaluation
git commit -m "refactor(testing): share owned process runner"
```

### Task 5: Add a hermetic external-binary correctness harness

**Files:**

- Create: `scripts/e2e/harness_test.go`
- Create: `scripts/e2e/provider_test.go`
- Create: `scripts/e2e/harness_unix_test.go`
- Create: `scripts/e2e/harness_windows_test.go`
- Create: `scripts/e2e/testdata/read-edit-test/fixture/go.mod`
- Create: `scripts/e2e/testdata/read-edit-test/fixture/calc/calc.go`
- Create: `scripts/e2e/testdata/read-edit-test/fixture/calc/calc_test.go`
- Create: `scripts/e2e/testdata/read-edit-test/scenario.json`
- Create: `scripts/e2e/testdata/read-edit-test/provider/steps.json`
- Modify: `Makefile`

**Interfaces:**

- Produces `make test-e2e` and `EINO_E2E_BINARY` test input.
- The harness invokes only the built `eino-agent exec ... --output-format json`
  interface inside a disposable Git repository.
- Provider steps are strict JSON and permit only `tool_call`, `final`,
  `overload`, `block_until_cancel`, and `assert_prior_tool_result`.

- [ ] **Step 1: Add strict scenario and provider-manifest tests**

Define these types with `DisallowUnknownFields` and one-document EOF checks:

```go
type scenario struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Prompt        string   `json:"prompt"`
	Tools         []string `json:"tools"`
	PermissionMode string  `json:"permission_mode"`
	MaxTurns      int      `json:"max_turns"`
	TimeoutMillis int      `json:"timeout_milliseconds"`
	Expected      expected `json:"expected"`
}

type expected struct {
	ExitCode       int               `json:"exit_code"`
	Status         string            `json:"status"`
	TerminalReason string            `json:"terminal_reason,omitempty"`
	FileSHA256     map[string]string `json:"file_sha256"`
	Absent         []string          `json:"absent"`
	GitStatus      []string          `json:"git_status"`
}
```

Reject absolute/traversing fixture paths, duplicate files, unknown tools, zero
budgets, unexpected provider calls, raw credential-shaped strings, and manifests
larger than 64 KiB.

- [ ] **Step 2: Implement the disposable repository and provider journal**

The harness must:

1. create a private temporary root and subdirectories for repo, HOME, XDG
   config/data/cache, artifacts, and outside sentinel;
2. copy only manifest-listed regular fixture files with a 256 KiB total cap;
3. initialize and commit the fixture repository with local Git identity;
4. start an `httptest.Server` that validates model, call order, tool name,
   input, and prior tool result;
5. invoke the external binary through `ownedprocess.Run` with only `PATH`,
   private HOME/XDG/TMPDIR, loopback `NO_PROXY`, and `GOTOOLCHAIN=local`;
6. cap stdout/stderr at 64 KiB each;
7. decode one `headlessEnvelope` and require EOF;
8. grade exact exit/status/terminal reason, file hashes, absent files, and
   porcelain Git status; and
9. remove the whole private root on every success, fail, timeout, or cancel.

Never publish an evaluation report. Test failure output may contain stable
scenario IDs and oracle mismatches, not prompts, provider bodies, source, or
credentials.

- [ ] **Step 3: Add the read-edit-test scenario**

The fixture contains a failing `Add` test. Provider steps must call, in order:

```text
Read(calc/calc.go)
Edit(calc/calc.go, exact old body, exact corrected body)
Bash(go test ./calc)
final("fixed and verified")
```

The oracle requires exit 0, status `success`, only `calc/calc.go` modified, the
exact corrected SHA-256, no file outside the repo changed, four provider calls,
three tool results in order, and `go test ./calc` success observed through the
Bash tool result. The grader independently reruns `go test ./...` after the
agent exits.

- [ ] **Step 4: Add the Make target**

```make
E2E_OUTPUT_DIR ?= $(BUILD_DIR)/e2e
E2E_BINARY ?= $(E2E_OUTPUT_DIR)/eino-agent$(if $(filter windows,$(shell $(GO) env GOHOSTOS)),.exe,)
TEST_E2E_TIMEOUT ?= 10m

test-e2e: $(E2E_BINARY)
	EINO_E2E_BINARY=$(abspath $(E2E_BINARY)) $(GO) test ./scripts/e2e -count=1 -timeout=$(TEST_E2E_TIMEOUT)

$(E2E_BINARY): $(SOURCES) go.mod go.sum
	@mkdir -p $(E2E_OUTPUT_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/eino-agent/
```

Do not reuse `EVAL_REPORT`, `EVAL_SCENARIO`, or evaluation report code.

- [ ] **Step 5: Run the first scenario and commit**

```bash
go test ./scripts/e2e -run '^TestReadEditTest$' -count=1
make test-e2e
git add Makefile scripts/e2e
git commit -m "test(e2e): add hermetic external binary harness"
```

### Task 6: Freeze permission, cancellation, recovery, failover, and entrypoint regressions

**Files:**

- Create: `scripts/e2e/testdata/permission-rejected/scenario.json`
- Create: `scripts/e2e/testdata/permission-rejected/provider/steps.json`
- Create: `scripts/e2e/testdata/permission-rejected/fixture/go.mod`
- Create: `scripts/e2e/testdata/cancel-no-late-write/scenario.json`
- Create: `scripts/e2e/testdata/cancel-no-late-write/provider/steps.json`
- Create: `scripts/e2e/testdata/cancel-no-late-write/fixture/go.mod`
- Create: `scripts/e2e/testdata/resume-no-duplicate-write/scenario.json`
- Create: `scripts/e2e/testdata/resume-no-duplicate-write/provider/steps.json`
- Create: `scripts/e2e/testdata/resume-no-duplicate-write/fixture/go.mod`
- Create: `scripts/e2e/testdata/failover-disposition/scenario.json`
- Create: `scripts/e2e/testdata/failover-disposition/provider/steps.json`
- Create: `scripts/e2e/testdata/failover-disposition/fixture/go.mod`
- Create: `scripts/e2e/testdata/malformed-tool-input/scenario.json`
- Create: `scripts/e2e/testdata/malformed-tool-input/provider/steps.json`
- Create: `scripts/e2e/testdata/malformed-tool-input/fixture/go.mod`
- Modify: `scripts/e2e/scenarios_test.go`
- Modify: `Makefile`
- Modify: `quality/iteration.yaml`

**Interfaces:**

- Adds five real-binary scenarios to `test-e2e`.
- Adds existing deterministic engine, ACP, permission-race, and PTY entrypoint
  oracles to the same correctness pack without moving their ownership.
- Adds risk pack `e2e -> test-e2e` and assigns it to engine, tools, CLI, ACP,
  and MCP modules.

- [ ] **Step 1: Add exact real-binary scenario oracles**

Implement these tests and fixtures:

| Test | Provider action | Independent oracle |
|---|---|---|
| `TestPermissionRejectedNoWrite` | Request `Write` under `permission-mode=plan`, assert the rejection tool result, then return a final response | Exit 0 with status `success`, target and outside sentinel absent, provider observes one rejection, Git clean |
| `TestCancelNoLateWrite` | Block after streaming a committed tool prefix; harness cancels the process | Envelope/exit is cancellation when emitted, no target after bounded quiescence, provider sees no later request, all child PIDs gone |
| `TestResumeNoDuplicateWrite` | First run writes one marker and returns session ID; second run uses `exec --resume <id>` with a final-only provider step | Marker hash and line count unchanged, no replay tool request, one terminal result per invocation |
| `TestFailoverDisposition` | Return overload for main model and success for `--fallback-model fallback-model` | Full admitted request seen on both candidates, main then fallback order, one final output, no duplicate tool write |
| `TestMalformedToolInput` | Emit `Write` without required path and then a final response | Error tool result observed, no panic, no file delta, terminal output remains well formed |

Use provider barriers and request journals, never `time.Sleep`, to establish
ordering. A bounded polling loop is allowed only to prove process disappearance
after an explicit cancel/kill.

- [ ] **Step 2: Add existing cross-entrypoint oracles to `test-e2e`**

Append these exact commands after the external harness:

```make
	$(GO) test ./engine -run '^(TestGoldenEventOrdering|TestGoldenTerminalEventIsLast|TestCanonicalProjectGraphQueryTrace)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)
	$(GO) test ./server/acp -run '^(TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts|TestP235ACPStdioLoadResumeAndExactActiveReconnect)$$' -count=1 -timeout=$(TEST_CONTRACT_TIMEOUT)
	$(GO) test -race ./engine -run '^TestPermissionCoordinatorClassifierWinsUserRaceExactlyOnce$$' -count=1 -timeout=$(TEST_RACE_TIMEOUT)
```

Before editing the Makefile, run `go test -list
'GoldenEventOrdering|GoldenTerminalEventIsLast|CanonicalProjectGraphQueryTrace'
./engine` and stop if any exact symbol has drifted. Do not substitute a broad
package run without updating this plan and the accepted oracle.

On Unix, also call `$(MAKE) test-pty`; on Windows, let the existing target
report its platform boundary. The PTY result remains separately visible in
iteration evidence even though `test-e2e` exercises it for pack completeness.

- [ ] **Step 3: Add the E2E risk to policy**

```yaml
risk_packs:
  e2e:
    target: test-e2e
    platforms: [all]
```

Add `e2e` to `engine-runtime`, `tool-runtime`, `cli-entrypoint`, `acp-adapter`,
and `mcp-adapter`. Do not add it to documentation-only or build-metadata paths.

- [ ] **Step 4: Run the regression matrix**

```bash
go test ./scripts/e2e -run 'PermissionRejected|CancelNoLateWrite|ResumeNoDuplicateWrite|FailoverDisposition|MalformedToolInput' -count=1
make test-e2e
make test-contract
make test-race
make test-pty
```

Expected: all applicable commands pass. On Windows, report PTY as
`not_applicable`; do not claim a PTY pass.

- [ ] **Step 5: Commit the regression matrix**

```bash
git add Makefile quality/iteration.yaml scripts/e2e
git commit -m "test(e2e): freeze critical runtime regressions"
```

### Task 7: Add retained fuzz input and advisory changed-package coverage

**Files:**

- Create: `engine/commands/testdata/fuzz/FuzzParseCommandInput/quoted-invalid-utf8`
- Create: `scripts/iteration/coverage.go`
- Create: `scripts/iteration/coverage_test.go`
- Modify: `scripts/iteration/verify.go`
- Modify: `docs/contributing/testing-strategy.md`
- Modify: `docs/contributing/verification.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- Produces ignored `coverage.json` advisory with package, statement percentage,
  and changed-file list only.
- Does not add coverage to `Evidence.Gates`, required targets, or
  `evidence_ready` calculation.
- Preserves the two existing TUI fuzz corpora and adds only a minimized parser
  seed that reproduced a real or intentionally seeded invariant boundary.

- [ ] **Step 1: Add coverage parser tests**

Use a synthetic cover profile and S1 plan. Assert only changed packages appear,
deleted files are reported as unavailable, missing `build/coverage.out` yields
`available:false`, and percentages are rounded to one decimal only in the
advisory renderer.

```go
type CoverageAdvisory struct {
	SchemaVersion int               `json:"schema_version"`
	DiffDigest    string            `json:"diff_digest"`
	Available     bool              `json:"available"`
	Packages      []PackageCoverage `json:"packages"`
}

type PackageCoverage struct {
	Package      string   `json:"package"`
	Statements   float64  `json:"statements_percent"`
	ChangedFiles []string `json:"changed_files"`
}
```

- [ ] **Step 2: Implement advisory extraction**

Parse the Go cover profile directly; do not scrape HTML. Map file paths to the
validated module package list, sort output, and atomically write
`coverage.json` after `make test` when a profile exists. A parse failure is a
visible advisory error but does not rewrite a passing test gate or block merge.

- [ ] **Step 3: Minimize and retain one parser seed**

Run:

```bash
go test ./engine/commands -run '^$' -fuzz '^FuzzParseCommandInput$' -fuzztime=30s -timeout=2m
```

If no new failure is found, retain a small, reviewed seed that covers nested
quotes plus invalid UTF-8 only when the fuzz invariant accepts it as a useful
corpus entry. Do not commit random bulk corpus files. Record the exact invariant
in a test comment, not the discovery machine path.

- [ ] **Step 4: Document the evidence boundaries**

Update testing strategy and verification docs to state:

- focused versus merge purpose;
- exact observable seams covered by `test-e2e`;
- why `eval-baseline` remains a separate product evaluation;
- why coverage is advisory;
- why race, PTY, fuzz, live provider, and Computer Use remain distinct evidence;
  and
- that one retry never erases the first failure.

- [ ] **Step 5: Self-review the plan and implementation**

```bash
rg -n 'TBD|TODO|FIXME|some test|add tests|later implementation' docs/superpowers/plans/2026-08-08-iteration-quality-s2-verification-e2e.md | rg -v 'rg -n'
go test -list . ./engine ./server/acp ./scripts/e2e
git diff --check
```

Every selector must remain an exact test proven by `go test -list`; a broad
package fallback is not acceptable.

- [ ] **Step 6: Run final gates**

```bash
go test ./scripts/iteration ./scripts/internal/ownedprocess ./scripts/evaluation ./scripts/e2e -count=1
make test-contract
make test-race
make test-pty
make test-fuzz-smoke
make test-e2e
make docs-check
make fmt
make lint
make test
make build
git diff --check
```

Expected: every applicable command passes and `make verify-merge` on the clean
committed candidate produces current `evidence_ready`. If `.reference` is
absent, `docs-check` is `not_applicable` inside iteration evidence and
`docs-check-ci` must still pass.

- [ ] **Step 7: Commit S2 closeout documentation**

```bash
git add engine/commands/testdata/fuzz scripts/iteration/coverage.go scripts/iteration/coverage_test.go scripts/iteration/verify.go docs/contributing/testing-strategy.md docs/contributing/verification.md docs/superpowers/plans/README.md
git commit -m "docs(quality): define automated regression evidence"
```

- [ ] **Step 8: Prepare the S2 PR**

Report external-binary scenarios, exact selected risk packs, evaluation parity,
first-failure behavior, process/PTY cleanup, coverage non-gate status, local
Make evidence, any platform `not_applicable` result, rollback of the new runner
and E2E target, and best-effort remote CI as a separate evidence class.
