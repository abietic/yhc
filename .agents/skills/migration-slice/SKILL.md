---
name: migration-slice
description: Execute exactly one accepted YHC migration, compatibility, or product-evolution slice from docs/migration/PLAN.md. Use when asked to continue the next P/Tier item, implement a named PLAN slice, or close one accepted checklist with source-backed code, tests, and documentation. Do not use for broad backlog completion, research-only work, unresolved product scope, or unresolved architecture decisions.
---

# Migration Slice

Execute accepted compatibility and project-native evolution slices as well as
legacy migration work.

Execute one accepted evolution slice at a time. Keep product scope, reference
adoption, architecture decisions, diff review, and final verification in Codex.

## Telemetry admission

Apply `$skill-runtime` admission and start one audit run before changing files,
delegating, or running final gates, with
`skill=migration-slice`, `kind=migration-slice`, and the selected slice ID
as scope. Record only applicable decision-bearing milestones:
`scope_selected`, `contract_frozen`,
`implementation_finished`, and `gates_finished` milestones.

The shared runtime owns data minimization, Terra start/finish/assessment,
terminal outcomes, logging-failure recovery, and the ban on measured ROI claims
without a complete logged run.

## Establish current state

1. Read `git status --short` and the relevant diff.
2. Preserve unrelated and uncommitted user changes.
3. Read `PROJECT_DIRECTION.md`, `docs/migration/PLAN.md`, `REMAINING.md`,
   `docs/contributing/documentation-policy.md`, and the affected architecture
   documents.
4. Select exactly one accepted slice. Do not silently expand its scope.

## Freeze the behavioral contract

Read current Go source, production callers, helpers, and tests first. Then read
direct source and tests from only the references named by the accepted contract.

Define observable acceptance criteria for:

- entrypoints;
- event and callback ordering;
- permissions;
- persistence and replay;
- cancellation and recovery;
- error semantics;
- model-visible behavior;
- TUI and ACP behavior where applicable.

Classify the accepted decision as `preserve`, `adapt`, `combine`, or
`project-native`; `reject` and `defer` do not enter implementation. Record
compatibility consequences. Do not treat planning checkboxes or a missing
reference feature as implementation evidence.

## Implement the smallest coherent slice

1. Add focused tests for the observable contract.
2. Implement only required behavior.
3. Cover every relevant entrypoint.
4. Preserve the project-owned ordering and failure semantics frozen by the
   accepted contract. Add compatibility coverage where reference behavior is
   intentionally retained.
5. Avoid unrelated cleanup or redesign.

## Verify the slice, then hand off closeout

Run focused tests while iterating. Add race, PTY, golden, or performance tests
when the boundary requires them.

Map every acceptance criterion to current source or focused-test evidence. Do
not reuse results from before the latest behavioral or contract change.

Then hand the changed slice and its focused evidence to `$iteration-workflow`.
If owned facts need durable documentation, route the edit through
`$write-docs`; this skill does not duplicate shared repository verification or
committed-evidence mechanics.
