# Detailed Product Evolution Contracts

**Status:** current
**Last verified:** 2026-08-13

> **Ownership:** index and authoring rules for detailed accepted contracts.
> Machine queue state belongs in [`queue.yaml`](../queue.yaml), its human
> projection in [`PLAN.md`](../PLAN.md), current behavior in architecture, and
> completed delivery in [`history/`](../history/README.md).

## Current Intake Outcome

P50.1 completed the ProjectGraph rebuilt-revision fence and closed G48. P50.2
completed the reviewer attempt-latency denominator and closed G49. P50.3 moved
reviewer-audit storage behind a bounded non-blocking single-writer dispatcher
and closed G50. The completed program repairs one ProjectGraph authorization
race, one reviewer latency denominator, and synchronous reviewer-audit I/O
without enabling reviewer enforcement.

The subsequent containment intake accepted P42.1's granular proof and explicit
Guest/hook/stdio-MCP binding contract. P51.1 delivered the Darwin Guest subset.
P51.2 now admits ordinary canonical Auto Bash only from its exact complete
proof, constrains critical literal `rm`/`rmdir` to a fresh `AllowOnce`, and
revalidates the binding before dispatch and shell submission. G28 stays open
for ambient credentials, hooks/MCP, and missing hard resource limits; no
successor slice is admitted.

P49 completed the approved budget-optional Goal and restart-attribution repair,
closing G21/G47. P47.1-P47.7 completed the approved Task Explorer program and
closed G38-G41. P48.1-P48.5 completed the approved ACP boundary program and
closed G42-G46; its queue was empty before P50 intake. P46 completed and closed
G36/G37; earlier intake groups have these terminal outcomes:

| Contract | Outcome |
|---|---|
| [`p51-2-auto-containment-admission.md`](p51-2-auto-containment-admission.md) | Historical executable contract; P51.2 completed on 2026-08-13, while G28 remains open without an admitted successor. |
| [`p50-permission-runtime-remediation.md`](p50-permission-runtime-remediation.md) | Historical approved contract; P50.1-P50.3 completed, closed G48-G50, and left reviewer enforcement deferred. |
| [`p49-goal-default-unbudgeted.md`](p49-goal-default-unbudgeted.md) | P49 completed the approved budget-optional default-on Goal and restart-attribution repair; G21/G47 are closed. |
| [`p38-provider-reasoning-origin.md`](p38-provider-reasoning-origin.md) | P38.0 completed under `adapt`, closed G34, and retained conservative public exclusion. |
| [`p39-workspace-recovery-contract.md`](p39-workspace-recovery-contract.md) | P39.0 completed the test-backed characterization contract; G2 remains open because no production writer or command was added. |
| [`p38-p45-next-product-intake.md`](p38-p45-next-product-intake.md) | P40.1, P41.1, and P41.2 completed and closed G12, G25, and G26; P44/P45 record `defer` for G14/G21. |
| [`p42-host-execution-containment.md`](p42-host-execution-containment.md) | P42.0 completed the immutable compatibility seam, P51.1 delivered the Darwin Guest binding, and P51.2 completed proof-bound Auto Bash admission; G28 remains open. |
| [`p43-real-repository-evaluation.md`](p43-real-repository-evaluation.md) | P43.0 completed the opt-in non-authoritative baseline and closed G29. |
| [`p46-model-failover-repair.md`](p46-model-failover-repair.md) | P46.1 completed complete-request context admission and P46.2 completed explicit disposal plus safe cross-entrypoint fallback visibility; G36/G37 are closed. |
| [`p47-task-explorer-remediation.md`](p47-task-explorer-remediation.md) | Historical approved contract; P47.1-P47.7 completed and closed G38-G41. |
| [`p48-acp-boundary-remediation.md`](p48-acp-boundary-remediation.md) | Historical approved contract; P48.1-P48.5 completed and closed G42-G46. |

Root queue state always wins over historical state text retained inside a
completed contract. A contract cannot promote itself.

## Retained Historical Contracts

These files preserve compatibility, ordering, migration, recovery, and rollback
decisions. They do not own current queue state or current implementation facts.

| Area | Retained contracts | Current replacement |
|---|---|---|
| Query/runtime | [`p13-project-graph-kernel.md`](p13-project-graph-kernel.md), [`p14-async-child-interaction.md`](p14-async-child-interaction.md), [`p18-worktree-lifecycle.md`](p18-worktree-lifecycle.md) | [`architecture/runtime/`](../../architecture/runtime/README.md) and runtime history |
| Commands and Plan | [`p16-command-surface.md`](p16-command-surface.md), [`p20-plan-mode-interaction.md`](p20-plan-mode-interaction.md), [`g24-plan-confirmation-input-isolation.md`](g24-plan-confirmation-input-isolation.md) | [`commands.md`](../../architecture/capabilities/commands.md), [`permissions.md`](../../architecture/capabilities/permissions.md), and runtime/TUI history |
| Permissions and ACP | [`p22-auto-permission-review.md`](p22-auto-permission-review.md), [`p23-acp-adapter-hardening.md`](p23-acp-adapter-hardening.md), [`p36-acp-assistant-replay.md`](p36-acp-assistant-replay.md), [`p37-concurrent-exact-permission-settlement.md`](p37-concurrent-exact-permission-settlement.md) | [`permissions.md`](../../architecture/capabilities/permissions.md), [`acp-adapter.md`](../../architecture/platform/acp-adapter.md), and runtime history |
| Goal, providers, media | [`p24-durable-goal-lifecycle.md`](p24-durable-goal-lifecycle.md), [`p29-model-portfolio-routing.md`](p29-model-portfolio-routing.md), [`p30-cross-entrypoint-multimodal-input.md`](p30-cross-entrypoint-multimodal-input.md) | Current runtime/platform/state owners and runtime history |
| Task, plugin, MCP, recovery | [`p31-task-todo-explorer.md`](p31-task-todo-explorer.md), [`p32-plugin-file-authority.md`](p32-plugin-file-authority.md), [`p33-mcp-live-tool-generation.md`](p33-mcp-live-tool-generation.md), [`p34-file-state-checkpoint-repair.md`](p34-file-state-checkpoint-repair.md) | Current runtime/platform/state owners and runtime history |
| TUI lifecycle | [`g11-tui-frame-integrity/`](g11-tui-frame-integrity/README.md), [`g9-markdown-display-cell-geometry.md`](g9-markdown-display-cell-geometry.md), [`p19-tui-revontuli-identity.md`](p19-tui-revontuli-identity.md), [`p27-tui-selection-viewport-geometry.md`](p27-tui-selection-viewport-geometry.md), [`p35-tui-notification-lifecycle.md`](p35-tui-notification-lifecycle.md) | [`architecture/tui/`](../../architecture/tui/README.md) and TUI history |

## Authoring Rule

Create or retain a detailed plan only when one page cannot safely hold the
observable contract, entrypoints, ordering, persistence/recovery semantics,
promotion evidence, and rollback boundary. A detailed plan must:

1. identify the user problem and adoption decision;
2. separate current evidence from target behavior;
3. name non-goals and supported entrypoints;
4. define deterministic promotion and regression evidence;
5. state durable migration and rollback behavior when applicable; and
6. link one queue row without duplicating its mutable state.

After completion, add one history record and remove the executable row from
`queue.yaml`. Retain the contract only when it remains useful compatibility or
recovery evidence; otherwise Git history is the archive.
