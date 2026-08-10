---
name: runtime-depth-change
description: Change or review depth-sensitive YHC runtime boundaries including model-visible tool assembly, provider routing and fallback, recovery and compaction, hook lifecycle, streamed tool commit, or service lifecycle. Use for any accepted evolution slice where ordering, entrypoints, cancellation, fallback, side effects, or model-visible behavior are contract-sensitive.
---

# Runtime Depth Change

Handle one runtime subsystem per invocation. Read `PROJECT_DIRECTION.md`, the
current PLAN item, current production source, subsystem contract, and focused
tests before editing. Read selected reference source only when the accepted
contract depends on comparative or compatibility evidence.

Keep architecture decisions, OS process semantics, concurrency design, recovery
behavior, and reference-adoption decisions in the parent agent. Terra may gather
bounded evidence, implement a frozen helper contract, or review a cohesive diff.

## Telemetry admission

Apply `$skill-runtime` admission. This workflow is risk-bearing, so start one
audit run before choosing the subsystem branch with `skill=runtime-depth-change`,
`kind=runtime-depth-change`, and the smallest affected subsystem. Record only
applicable decision-bearing milestones: `subsystem_selected`, `contract_frozen`,
`entrypoints_checked`, `focused_verification_finished`, and
`repository_gates_finished`.

Use `entrypoint_gap`, `ordering_mismatch`, `fallback_mismatch`,
`process_lifecycle_failure`, `race_failure`, or `repository_gate_failed` as
terminal categories. The shared runtime owns structured-data limits, Terra
accounting, every exit's finish, and logging-failure behavior.

## Tool pool and MCP

Read `docs/architecture/capabilities/tool-registry.md`.

Preserve:

- complete runtime registry for dispatch;
- separately filtered model-visible projection;
- deterministic built-in precedence on conflicts;
- stable partition ordering;
- refresh only at safe lifecycle boundaries;
- permission checks as defense in depth;
- equivalent CLI, ACP, leader, and sub-agent behavior.

## Provider routing

Read `docs/architecture/platform/model-providers.md`.

Preserve:

- one explicit routing/resolution policy;
- provider-specific adapters and metadata;
- lazy creation of fallback routes;
- actionable configuration diagnostics;
- secret redaction;
- consistent behavior across all entrypoints.

Do not collapse provider-specific adapters merely because an endpoint is
OpenAI-compatible.

## Recovery and compaction

Read `docs/architecture/runtime/compaction.md` and
`docs/architecture/runtime/recovery.md`.

Preserve:

- ordered recovery stages;
- staged handling of oversized requests;
- immutable original input;
- progress only for a non-empty material transformation;
- no duplicate callback or recovery execution;
- bounded media and token transformations;
- observable evidence in TUI and ACP.

## Hook lifecycle

Read `docs/architecture/capabilities/hooks.md`, the current PLAN entry, current
production source, relevant accepted reference evidence, and tests before
changing behavior.

Freeze the contract for:

- default and configured timeout;
- cancellation;
- descendant process termination;
- exit-code semantics;
- blocking versus non-blocking failures;
- `continue=false`;
- user-prompt and post-tool branches.

Do not encode unfinished P4 implementation details as verified facts.

## Verification

Run subsystem-focused tests and race, PTY, or process-lifecycle tests when
relevant. Then hand the changed subsystem and risk evidence to
`$iteration-workflow` for shared repository verification and committed
evidence.
