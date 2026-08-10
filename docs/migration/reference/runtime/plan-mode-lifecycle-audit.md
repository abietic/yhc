# Plan Mode Lifecycle Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-18

> **Ownership:** source-backed comparison of Plan Mode state ownership,
> admission, approval, persistence, replay, and entrypoint behavior; accepted
> execution belongs in `PLAN.md`

## Observable Question

What project-owned contract should govern Plan Mode entry, model-visible
capabilities, execution enforcement, plan-file mutation, approval, exit,
cancellation, persistence, replay, and supported entrypoints?

This audit treats Plan Mode as an execution-permission state. Checklist or todo
updates are separate projections and do not enter or exit the mode.

## Snapshots

| Repository | Revision |
|---|---|
| Eino-Agent | `026e6001ac24` |
| Claude Code Ripe | `4b9d30f79532` |
| Codex | `800715d20165` |
| Crush | `3446255daa02` |
| Grok Build | `b189869b7755` |
| OpenCode | `4a760b574349` |
| Pi | `c6d8371521fc` |

These are local research snapshots, not claims about current upstream heads.

## Snapshot Baseline Evidence

| Boundary | Verified behavior | Consequence |
|---|---|---|
| owner | Permission mode is stored on `QueryEngine`; active execution also carries `ToolUseContext.PlanMode` and `ToolUseOptions.PermissionMode`. | Successful tool transitions synchronize common paths, but several mutable representations remain. |
| model visibility | `QueryEngine.modelVisibleTools` applies selection, simple mode, hidden/disabled state, and blanket denies but not Plan Mode. | The model sees mutating tools that runtime later rejects. |
| runtime guard | `executeToolCall` rejects non-read-only non-transition tools while Plan is active. | Defense exists, but correctness depends on tool metadata. |
| plan file | Write/Edit is allowed when `isPlanFileWrite` accepts a path under the plans-directory prefix. | Prefix siblings, traversal, aliases, and symlinks are not an exact session-file capability. |
| exit | Exit is permission-gated and successful execution maps to Default. | The tool-local validator does not prove the session is actively planning. |
| approval | TUI renders distinct approval labels, all mapped to the same allow response. | Target permission semantics are lost. |
| semantic prompts | Exit output prints semantic `allowedPrompts` as granted. | No runtime consumer creates an enforceable grant. |
| durability | Session metadata stores permission mode and request references. | A cold process cannot revive callbacks; request IDs alone are not actionable approval state. |
| ACP | `SetSessionMode` directly updates engine permission mode. | It does not serialize through the active-turn Plan transition. |

This table freezes the pre-P17.H0 baseline used to accept the program. P17.H0
has since replaced the model-visible, runtime-guard, exact-path, inactive-Exit,
and false-grant behaviors. Current behavior is owned by
[`tool-registry.md`](../../../architecture/capabilities/tool-registry.md) and
[`permissions.md`](../../../architecture/capabilities/permissions.md);
completion evidence is in
[`post-parity.md`](../../history/runtime/post-parity.md#p17h0-fail-closed-plan-admission).

Current replacement anchors:

- [`QueryEngine.modelVisibleTools`](../../../../engine/engine.go#L1377)
- [`evaluatePlanToolPolicy`](../../../../engine/plan_tool_policy.go#L47)
- [`executeToolCall`](../../../../engine/tool_execution.go#L30)
- [`isExactPlanFileMutation`](../../../../engine/plan_tool_policy.go#L171)
- [`EnterPlanModeTool`](../../../../tools/plan_mode.go#L64)
- [`ExitPlanModeTool`](../../../../tools/plan_mode.go#L172)
- [`Agent.SetSessionMode`](../../../../server/acp/agent.go#L900)
- [`NewPlanDialog`](../../../../internal/tui/plan_dialog.go#L111)
- [`persistSessionCheckpointMessages`](../../../../engine/session_checkpoint.go#L26)

Focused Plan/Worktree tests passed across `engine`, `tools`, `internal/tui`,
and `server/acp` on this snapshot. Those baseline tests proved transitions and
runtime rejection, not the then-missing model-visible, exact-path,
cold-approval, or cross-entrypoint negative contracts.

## Cross-Reference Matrix

| Reference | State owner | Admission/enforcement | Approval and recovery | Verdict for this question |
|---|---|---|---|---|
| Claude Code Ripe | permission context stores Plan and pre-Plan mode | ordinary model-visible pool remains broad; filesystem permission recognizes session plan files | explicit approval UI, feedback, target permission updates, plan content recovery | adapt approval/prior-mode context; reject broad model-visible behavior |
| Codex | collaboration mode is thread/session protocol state | mode-specific instructions and at least idle/background gating; `update_plan` is explicitly only a checklist | protocol/TUI replay preserves mode; handoff may create another thread | adapt canonical mode/protocol/replay boundary, not thread handoff |
| Crush | no product-level Plan Mode found | no Plan-specific filter or guard found | no Plan-specific approval/replay found | no Plan lifecycle mechanism to adopt |
| Grok Build | session actor owns `Inactive/Pending/Active/ExitPending` | active Plan enforces an edit gate with plan-file exception | approval is persisted; transient states normalize safely on resume | adapt phase machine, runtime gate, and resume normalization |
| OpenCode | persisted session selects the Plan agent | permission denies edit except session plan paths | `plan_exit` asks the user and switches to Build after approval | adapt permission-backed file exception and explicit exit |
| Pi | optional extension closure owns state | extension changes active tools and allowlists safe Bash commands | extension entry persists in session history and drives a TUI execute/stay/refine choice | adapt UX ideas only; reject as core safety owner |

## Important Reference Boundaries

### Claude Code Ripe

- `src/tools/EnterPlanModeTool/EnterPlanModeTool.ts` enters through a shared
  permission transition and records pre-Plan context.
- `src/tools/ExitPlanModeTool/ExitPlanModeV2Tool.ts` validates Plan state and
  requests approval.
- `src/utils/permissions/filesystem.ts` grants the session plan-file exception.
- `src/hooks/useMergedTools.ts` and `src/utils/toolPool.ts` show that Plan does
  not generally remove mutating tools from the model-visible set.

### Codex

- `codex-rs/protocol/src/config_types.rs` defines Plan as collaboration mode.
- `codex-rs/protocol/src/plan_tool.rs` explicitly distinguishes checklist plan
  updates from Plan Mode.
- `codex-rs/core/src/session/inject.rs` rejects extension-initiated idle work in
  Plan Mode.
- TUI Plan tests cover collaboration-mode replay and handoff behavior.

### Grok Build

- `xai-grok-shell/src/session/plan_mode.rs` owns phases, plan path, approval,
  persistence, and resume normalization.
- `xai-grok-shell/src/session/acp_session_impl/tool_calls.rs` applies the
  runtime edit gate.
- plan approval resume fixtures prove reattachment/recovery behavior.

### OpenCode

- `packages/core/src/plugin/agent.ts` defines Plan agent permissions.
- `packages/opencode/src/session/reminders.ts` injects the session plan path.
- `packages/opencode/src/tool/plan.ts` asks approval and switches to Build.

### Pi And Crush

Pi's `packages/coding-agent/examples/extensions/plan-mode/` is a useful
extension example, not a default engine contract. Crush has generic workspace,
permission, cancellation, and session mechanisms but no verified Plan Mode
owner.

## Findings

Verified:

1. Plan Mode must be session/runtime state; a tool result alone is not an
   owner.
2. Hiding tools and runtime authorization are separate defenses.
3. A plan-file exception must identify the exact session capability, not a
   directory prefix.
4. Checklist state is orthogonal to execution permission state.
5. Cold resume must not revive a callback or treat a historical approval as a
   current grant.

Recommendation:

- adopt one QueryEngine-owned phase machine;
- make model-visible admission mode-aware and keep execution enforcement;
- retain exact plan-file Write/Edit compatibility behind an exact capability;
- use explicit structured approval with target permission mode;
- normalize cold AwaitingApproval to Active and require a new Exit request; and
- keep TUI/ACP as projections and adapters.

Unresolved and deliberately excluded:

- safe arbitrary Bash execution during planning;
- conversion of semantic natural-language prompts into enforceable permission
  rules;
- Codex-style new-thread implementation handoff; and
- automatic coupling between Plan Mode and worktree creation.

## Adoption Decision

`combine`: combine complementary Plan Mode behaviors behind one Eino-Agent
contract. This decision is local to Plan Mode and does not combine it with the
worktree subsystem.

The completed implementation contract is
[`migration/history/runtime/p17-plan-mode-runtime.md`](../../history/runtime/p17-plan-mode-runtime.md).
Current implementation facts continue to belong in architecture documents
until each slice closes.
