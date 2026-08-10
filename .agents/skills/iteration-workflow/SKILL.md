---
name: iteration-workflow
description: Plan, implement, verify, and hand off one evidence-bound YHC repository iteration. Use for any code, test, documentation, or defect-fix change that needs risk-selected checks and current committed-tree merge evidence. Do not choose product scope, replace domain skills, or treat blocked checks as success.
---

# Iteration Workflow

Own the shared repository path from a scoped change plan through committed
evidence. Domain skills still own their behavioral and risk invariants. Follow
[`AGENTS.md`](../../../AGENTS.md) for repository safety and
[`docs/contributing/verification.md`](../../../docs/contributing/verification.md)
for command semantics.

## Telemetry admission and scope

Apply `$skill-runtime` before the first write, delegation, or final gate.
Inspect status, preserve unrelated changes, and run `make change-plan`. If a
migration slice is accepted, pass only its active `slice_id`; an empty queue is
valid.

## Deliver one vertical slice

Use the applicable domain skill, add the lowest stable failing test first, make
the smallest cohesive change, and run `make verify-focused` after each coherent
step. A required blocked or failing check stops completion. Report a genuinely
inapplicable check as `not-applicable`; never turn either state into success.

## Commit, then verify merge evidence

Format and inspect the scoped diff, commit explicit paths on a topic branch,
then run `make verify-merge` on the clean committed tree. If formatting changes
the tree, commit and rerun. Use `make change-evidence` to inspect status and run
`make change-evidence-ready` as the fail-closed completion assertion before
push.

## Hand off honestly

Report local gates, applicable risk packs, blocked/not-applicable checks,
remote CI, live-provider, PTY, and physical UI evidence separately. Child completion
or review is not parent acceptance or proof for the caller tree.
