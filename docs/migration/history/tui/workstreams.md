# TUI Workstream History

**Closed:** 2026-07-11
**Status:** historical

> **Ownership:** historical T0-T8 and M0-M7 workstream decomposition; not an
> active plan

Active work is owned by [`migration/PLAN.md`](../../PLAN.md). Current architecture
is in [`architecture/tui/README.md`](../../../architecture/tui/README.md). Full M0-M7 evidence is in the
[completion report](m0-m7-completion-report.md).

This document preserves the workstream decomposition used to reach structural
TUI parity and the modern multi-Agent M0-M7 experience. It must not be used to
infer current backlog.

## Structural Workstreams

| ID | Workstream | Evidence map | Final state |
|---|---|---|---|
| T0 | Inventory and behavior matrix | `manifest.yaml` and section maps | Complete |
| T1 | Application state and input | [`migration/history/tui/implementation-maps/04-input.md`](implementation-maps/04-input.md) | Complete |
| T2 | Permissions and modal interaction | [`migration/history/tui/implementation-maps/05-permissions.md`](implementation-maps/05-permissions.md) | Complete |
| T3 | Messages and conversation semantics | [`migration/history/tui/implementation-maps/02-messages.md`](implementation-maps/02-messages.md) | Complete |
| T4 | Tool presentation | [`migration/history/tui/implementation-maps/03-tools.md`](implementation-maps/03-tools.md) | Complete |
| T5 | Sessions, tasks, and Agents | [`migration/history/tui/implementation-maps/08-sessions.md`](implementation-maps/08-sessions.md) | Complete |
| T6 | Layout, progress, and status | Maps 01, 06, and 07 | Complete |
| T7 | Styling and accessibility | [`migration/history/tui/implementation-maps/09-styling.md`](implementation-maps/09-styling.md) | Complete |
| T8 | Terminal hardening | Terminal and accessibility contracts | Complete |

## Modernization Workstreams

| ID | Workstream | Final outcome |
|---|---|---|
| M0 | Research, contracts, baselines | Four-project research and accepted contracts |
| M1 | Canonical runtime state and live Agents | Identified events, reducer, selectors, live progress |
| M2 | Agent detail, lineage, and control | Transcript/detail, steering, pause/resume/abort, parent trace |
| M3 | Thread switching and attention | Per-thread view state, navigation, owner-scoped requests |
| M4 | Semantic rendering and tool traces | History contract, bounded renderers, stable streaming |
| M5 | Composer modernization | Contextual keys, rich elements, queue, history, editor, undo |
| M6 | Session and transcript modernization | Bounded discovery, inspect, fork/resume, durable restore |
| M7 | Product hardening | Responsive, accessible, PTY/performance/parity verification |

## Reference Surface Closure

The original broader TUI surface list contained 24 items: cost warning, help,
welcome, model picker, structured diff, doctor, context visualization, search,
compact summary, transcript search, idle return, message selection, bypass
confirmation, export, inline tasks, narrow-terminal handling, MCP approval,
highlighted code, Agent wizard, MCP settings, background tasks, teams, settings,
and session branch/fork/rewind.

All 24 received a bounded implementation slice. This statement records closure
of that historical inventory; it is not a claim that future UX improvements are
forbidden.

## Decisions That Remain Architectural

- Port workflows and observable semantics, not React/Ink component structure.
- Keep business/runtime state in engine and tool packages.
- Preserve O(viewport) rendering and bounded replay/view stores.
- Treat mouse, enhanced keys, terminal images, and provider-specific features
  as explicit capability decisions rather than assumptions.
- Require reference-derived behavior, focused tests, and applicable entrypoint
  wiring before declaring a workflow complete.

## Historical Verification Standard

M0-M7 closed only after all required Makefile gates, reducer properties,
Bubble Tea transitions, product goldens, real PTY restoration, focused race
tests, performance budgets, manifest validation, and deterministic isolated
Codex startup passed.
