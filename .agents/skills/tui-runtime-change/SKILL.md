---
name: tui-runtime-change
description: Change or review YHC TUI and runtime-state behavior, including runtime events, reducers, sub-agent progress, composer and queue semantics, session replay, rendering, responsive layout, and terminal lifecycle. Use for changes spanning internal/tui, engine runtime state, sessions, transcripts, or TUI contracts.
---

# TUI Runtime Change

Treat the TUI as a projection of engine-owned runtime truth.

## Telemetry admission

Apply `$skill-runtime` admission. This workflow is risk-bearing, so start one
audit run before selecting a TUI contract with `skill=tui-runtime-change`,
`kind=tui-runtime-change`, and the exact TUI/runtime scope. Record only applicable
decision-bearing milestones: `contract_selected`, `invariants_checked`,
`runtime_state_finished`, `presentation_finished`, and
`verification_finished`.

Finish as `blocked` when the engine-owned state contract is unresolved. The
shared runtime owns safe structured fields, Terra accounting, parent review and
caller-worktree separation, every terminal finish, and logging-failure recovery.

## Route the change

Read `docs/architecture/tui/README.md` and the relevant contract under
`docs/architecture/tui/contracts/`:

- runtime events and reducer;
- composer and editing;
- prompt queue;
- session and replay;
- responsive layout;
- terminal capability;
- accessibility;
- renderer maps;
- compatibility, comparative, and performance verification.

## Preserve core invariants

1. Engine reducers own runtime facts; the TUI owns presentation.
2. Runtime-visible events carry stable session, thread, request, and owner
   identities where required.
3. Replay reconstructs state without dispatching tools or model calls.
4. Render cost is bounded by visible content and active animation.
5. Compact views may omit detail, but sanitized transcript/raw evidence remains
   available.
6. Terminal modes are restored after normal exit, error, timeout, and cancel.
7. Every model-visible tool has a dedicated renderer or audited generic path.

## Implement state before presentation

When adding a visible capability:

1. define the engine/runtime event;
2. update reducer transitions;
3. add replay behavior;
4. expose a stable projection;
5. render it in the TUI.

Do not make widget-local state the only source of runtime truth.

## Verify by boundary

Use:

- reducer and transition tests for runtime state;
- replay tests proving no dispatch;
- golden tests at representative widths such as 40, 80, 120, and 180;
- PTY tests for paste, resize, mouse, alternate-screen, approval, cancel, and
  terminal restoration;
- race tests for engine/session/TUI/ACP interactions;
- performance tests for long history, fan-out, and event batching.

Update only the affected TUI contract, map, or verification document through
`$write-docs` when documentation was requested or owned facts changed.
Finish through `$iteration-workflow`; it owns shared repository verification
and final caller-worktree evidence.
