# Iteration Quality S5A Context And Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `$write-docs` for lifecycle
> ownership and `$iteration-workflow` for test-first execution, diff-bound
> verification, and closeout.

**Status:** active-plan
**Created:** 2026-08-09
**Plan state:** Active; F0 executed and A1 is the current lifecycle slice

> **Ownership:** implementation sequence for the documentation lifecycle and
> report-only worktree audit accepted by the
> [S5 completion design](../specs/2026-08-09-iteration-quality-s5-completion-design.md)

**Goal:** Make plan state and skill ownership truthful and machine-checkable,
then give maintainers a safe read-only inventory for deciding which Git
worktrees require preservation or manual cleanup review.

**Architecture:** `scripts/docs_check` compares the explicit plan index with
linked lifecycle metadata and resolves only top-level required project skills.
A separate `scripts/worktree_audit` deep module hides Git porcelain parsing and
classification behind one report interface. Neither command mutates product or
Git state. A prerequisite Make wrapper turns the existing CLI
`--require-ready` behavior into the shared fail-closed closeout command.

**Tech stack:** Go 1.26.5 standard library, current Markdown lifecycle fields,
Git porcelain output, white-box Go tests, and repository Make targets.

## Frozen invariants

- Preserve every unrelated dirty or untracked file and every existing
  worktree/branch.
- Product order remains in `docs/migration/queue.yaml`; this plan does not add
  a second queue.
- S1-S4 governance execution becomes historical, while S4 product-module
  acceptance remains intake-gated and open.
- The checker parses explicit metadata/table cells only. It does not infer
  runtime truth from prose or token presence.
- Worktree audit is report-only. It never fetches, prunes, repairs, removes,
  deletes, checks out, rebases, resets, or cleans.
- Review hints are not cleanup authorization.
- No new dependency or model-visible tool is added.
- Ordinary `make change-evidence` remains diagnostic; only
  `make change-evidence-ready` can complete an iteration.

## PR F0: expose a fail-closed evidence assertion

### Task 1: freeze the Make contract before editing it

- [x] Confirm the CLI already rejects missing explicit head, stale plan
  identity, and non-ready evidence through the existing
  `evidence --require-ready` tests in `scripts/iteration/main_test.go`.
- [x] Capture the current mismatch: `make change-evidence` can exit zero while
  rendering a non-ready state, whereas the pre-push hook calls the strict CLI
  form directly.

```bash
go test ./scripts/iteration -run 'TestRun.*RequireReady|Test.*RequireReady' -count=1
```

### Task 2: add one public strict wrapper

- [x] Add phony `change-evidence-ready` to `Makefile` with the exact command:

```text
go run ./scripts/iteration --base $(ITERATION_BASE) --head HEAD \
  --format $(ITERATION_FORMAT) evidence --require-ready
```

- [x] Preserve optional `ITERATION_SLICE_ID` in the same position as the
  existing plan/evidence wrappers.
- [x] Update `AGENTS.md`, `.agents/skills/iteration-workflow/SKILL.md`, and
  `docs/contributing/verification.md`: status inspection uses
  `make change-evidence`; completion uses `make change-evidence-ready`.
- [x] Add a shell/Make regression that proves the new target includes explicit
  `--head HEAD` and `--require-ready`, and that a non-ready fixture exits
  non-zero.

```bash
make change-evidence
make change-evidence-ready
```

Expected before merge verification: the first command may render a non-ready
state; the second must fail. After committing and running `make verify-merge`,
the strict command must succeed.

### Task 3: close F0

```bash
make fmt
make lint
make test
make build
make verify-merge
make change-evidence-ready
```

- [x] Commit only Make, its regression, and the three workflow owners. Merge F0
  before starting A1, B1, or C1.

## PR A1: repair lifecycle and skill routing

### Task 1: prove the current drift

- [ ] Run the narrow source scan and retain its output in the PR evidence, not
  in a new repository artifact:

```bash
rg -n 'Status:|Plan state:|superpowers:subagent-driven-development|superpowers:executing-plans' \
  docs/superpowers/plans docs/superpowers/specs/README.md
```

- [ ] Confirm the plan index says S1-S4 executed while their headers remain
  active/queued. Do not edit code in this task.

### Task 2: make completed plans historical

- [ ] In the four `2026-08-08-iteration-quality-s*` plans, set
  `**Status:** historical`, add `**Completed:** 2026-08-09`, and replace stale
  plan state with exact executed wording. S4 must explicitly exclude the open
  product-module admission.
- [ ] Remove the live `REQUIRED SUB-SKILL` block from every historical plan in
  `docs/superpowers/plans/`; replace it with one short historical-use banner
  only when the document otherwise looks executable.
- [ ] In the still-active P51.2 plan, route future execution through
  `$migration-slice` and `$iteration-workflow`; preserve its queue admission
  condition.
- [ ] Update both superpowers indexes so the Iteration Quality Kernel says
  S1-S4 governance is executed and S4 module acceptance remains intake-gated.
  Do not change migration queue state.

### Task 3: verify and commit A1

```bash
make docs-check-ci
git diff --check
make change-plan
make verify-focused
```

- [ ] Review only the plan/index paths, commit them, then run
  `make verify-merge` and `make change-evidence-ready` through
  `$iteration-workflow`.

## PR A2: enforce plan lifecycle semantics

### Task 1: add failing checker fixtures

Modify `scripts/docs_check/main_test.go` first.

- [ ] Add `TestCheckRepositoryRejectsPlanIndexLifecycleMismatch` with an index
  row whose state is Executed and a linked `active-plan` document.
- [ ] Add `TestCheckRepositoryRejectsMissingOrDuplicatePlanIndexEntry`.
- [ ] Add `TestCheckRepositoryRejectsUnknownRequiredProjectSkill` with a
  top-level active-plan instruction naming a missing `$skill`.
- [ ] Add acceptance cases for historical plans without a live instruction,
  an active local skill, and skill-like text inside fenced examples.

```bash
go test ./scripts/docs_check -run 'TestCheckRepository.*Plan|TestCheckRepository.*Skill' -count=1
```

Expected first result: the negative fixtures fail because the semantic
validators do not yet exist. If they unexpectedly pass, strengthen only the
independent fixture oracle; do not assert implementation helpers.

### Task 2: implement the smallest validators

Modify `scripts/docs_check/main.go`.

- [ ] Add a parser for plan-index rows that returns linked path and normalized
  lifecycle category. Reject unknown state prefixes and duplicate/missing
  focused plan entries.
- [ ] Compare historical index categories with `Status: historical` and active
  categories with `Status: active-plan`.
- [ ] Parse only the first top-level `REQUIRED SUB-SKILL` block. Resolve
  `$name` against `.agents/skills/<name>/SKILL.md`; reject legacy
  `superpowers:*` names and live instructions in historical plans.
- [ ] Call both validators once from `checkRepository`; keep them pure over the
  existing content cache where possible.
- [ ] Update `docs/contributing/documentation-policy.md` with the exact newly
  enforced contract.

```bash
go test ./scripts/docs_check -count=1
make docs-check-ci
```

### Task 3: close A2

```bash
make fmt
make lint
make test
make build
```

- [ ] Commit only checker, tests, and the documentation-policy owner. Run
  committed-tree merge evidence and confirm the checker catches a temporary
  lifecycle mismatch before reverting that temporary diagnostic edit.
- [ ] Finish with `make change-evidence-ready`; rendered status alone is not
  acceptance.

## PR A3: add the report-only worktree audit

### Task 1: write the classification tests first

Create `scripts/worktree_audit/main_test.go` in white-box package `main`.

- [ ] Freeze v1 JSON decoding and deterministic ordering for clean branches,
  dirty worktrees, detached heads, locks, prunable registrations, missing
  upstreams, base-reachable heads, divergence, and unreadable paths.
- [ ] Use a fake command runner to assert the exact read-only Git argv
  allowlist. Add a negative test that any unrecognized/mutating subcommand is
  rejected before execution.
- [ ] Assert a clean base-reachable review hint is distinct from authorization,
  while dirty, locked, unreadable, and diverged records receive preserve or
  inspect hints.

```bash
go test ./scripts/worktree_audit -count=1
```

Expected first result: package or symbols are missing.

### Task 2: implement one deep report module

Create `scripts/worktree_audit/main.go` and keep the external interface to one
`run` path plus CLI flags.

- [ ] Parse `git worktree list --porcelain` strictly, including unknown keys as
  diagnostics rather than silently changing semantics.
- [ ] Resolve `--base` to a commit without fetch and collect status, upstream,
  ahead/behind, and reachability through allowlisted Git reads.
- [ ] Emit schema v1 JSON or a concise text table from the same sorted report.
- [ ] Exit zero when risks/hints are present; exit non-zero only when the audit
  itself is invalid or unreadable.
- [ ] Add `make worktree-audit` as the public wrapper and document it in
  `docs/contributing/verification.md` as a decision aid, not a gate.

```bash
go test ./scripts/worktree_audit -count=1
go run ./scripts/worktree_audit --base origin/master --format json
make worktree-audit
```

### Task 3: close A3 without deleting anything

```bash
make fmt
make lint
make test
make build
make docs-check-ci
git diff --check
```

- [ ] Inspect the report against `git worktree list --porcelain`.
- [ ] Do not run any suggested removal. Commit the command, tests, Make target,
  and verification owner, then produce committed diff-bound evidence.
- [ ] Finish with `make change-evidence-ready` on the committed head.

## Completion

S5A is complete when F0 and A1-A3 are historical in the plan index, the
checker rejects a reproduced mismatch, the worktree report is deterministic,
no worktree or branch was mutated, and `make change-evidence-ready` succeeds for
each committed PR. Remote CI remains separate evidence.
