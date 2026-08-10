---
name: migration-loop
description: Orchestrate the full YHC product-evolution workflow across align, plan, and execute phases while preserving the existing migration ledger. Use only when explicitly asked to run the migration loop, realign and replan tracked evolution, or advance multiple PLAN items over successive invocations. Do not use for one named slice, one reference audit, one closeout, or an isolated runtime/TUI change; use the corresponding focused skill instead.
---

# Migration Loop

Route one product-evolution phase per invocation. This skill owns phase
selection, readiness, and the continuation signal; focused skills own research,
implementation, documentation, and closeout details.

`PROJECT_DIRECTION.md` owns product goals and reference-adoption rules. Current
source, production wiring, tests, and git history outrank generated status
documents. References under `.reference/` are read-only evidence, never an
automatic product specification.

## Admission

Use this skill only after an explicit request for the migration/product-
evolution loop, realignment, replanning, or successive PLAN advancement.
Complete exactly one `align` phase, one `plan` phase, or one ready execute slice
per invocation. A request for one named task belongs to its focused skill.

Keep architecture, public contracts, security boundaries, irreversible
migrations, and adoption decisions in the parent agent. Delegation may gather
evidence, implement a frozen bounded contract, or review a diff.

## Telemetry admission

Apply `$skill-runtime` admission before choosing the phase. Skip logging only
when this invocation remains a short, local, read-only readiness check with no
Terra delegation or final gate. Otherwise start an audit run before the first
trigger with `skill=migration-loop`, `kind=migration-loop`, and `align`, `plan`,
or the exact execute slice as scope. Record only applicable decision-bearing
milestones: `phase_selected`, `readiness_checked`, `phase_finished`, and
`continuation_decided`.

The shared runtime owns data minimization, Terra accounting, the distinction
between child completion and parent adoption, every terminal finish, and the
logging-failure/ROI boundary.

## Select the phase from current evidence

Read `git status --short`, `PROJECT_DIRECTION.md`, current production source,
`docs/migration/PLAN.md`, `docs/migration/STATUS.md`,
`docs/migration/REMAINING.md`, and the applicable plan contract. Preserve
unrelated user changes.

Choose one phase:

| Evidence | Phase | Owner route |
|---|---|---|
| Ledger facts are stale or cannot be verified | `align` | scanner plus `$write-docs` |
| Outcomes or priorities are unresolved | `plan` | optional `$reference-parity-audit`, then `$write-docs` |
| One accepted row is dependency-ready | `execute` | `$migration-slice` plus applicable companions |
| Accepted rows remain but every gate is unmet | `blocked` | report the first named gate |
| No accepted incomplete row remains | `complete` | no implementation |

Do not equate an unchecked row, a reference-only feature, or a registered but
unwired symbol with a product gap.

## Align

1. Run `go run ./scripts/migration_scan` and
   `go run ./scripts/migration_manifest.go check`.
2. Verify ledger claims against current source, callers, tests, and supported
   entrypoints. Classify discrepancies as stale documentation, reproduced gap,
   or unresolved evidence.
3. Apply `$write-docs` only to the affected fact owners. Do not copy
   volatile counts or completion narratives across owners.
4. Apply `$iteration-workflow` if files changed.

## Plan

1. Start from a verified user, safety, reliability, performance,
   compatibility, or maintenance outcome.
2. Use `$reference-parity-audit` only when one observable contract needs
   comparative evidence. Record `preserve`, `adapt`, `combine`,
   `project-native`, `reject`, or `defer` before implementation readiness.
3. Apply `$write-docs` to update the owning plan and root execution row.
   Each ready slice needs dependencies, state, promotion gate, acceptance
   evidence, rollback boundary, and one owner; do not duplicate its checklist
   in the root ledger.
4. Apply `$iteration-workflow` if files changed.

## Execute

1. Select the first accepted incomplete row whose dependencies and promotion
   gates are closed. If none is ready, stop as `blocked` or `complete` rather
   than widening scope.
2. Apply `$runtime-depth-change` for model-visible tools, providers,
   recovery/compaction, hooks, streamed commits, or service lifecycle.
3. Apply `$tui-runtime-change` for runtime events, reducers, replay,
   composer/queue behavior, rendering, layout, or terminal lifecycle.
4. Apply `$migration-slice` for the frozen one-slice implementation and
   focused tests.
5. Hand the selected vertical slice to `$iteration-workflow` for shared
   verification, committed evidence, and any explicitly authorized commit.

Companion skills add invariants; they do not create a second owner or expand the
accepted slice.

## Continue or stop

After closing the log, emit exactly one final signal:

- `MIGRATION_LOOP_SIGNAL=continue` when another accepted row is ready for a
  later invocation.
- `MIGRATION_LOOP_SIGNAL=blocked` when accepted work remains but the first
  ready candidate is waiting on a named gate.
- `MIGRATION_LOOP_SIGNAL=complete` when no accepted incomplete row remains.

The signal schedules no work by itself. Never turn one invocation into an
unbounded backlog run, modify reference checkouts, refactor unrelated code, or
commit without explicit authorization.
