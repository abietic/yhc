# Tool Registry and Model-Visible Pool

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** `tools.Registry` for dispatch; QueryEngine and `AssembleToolPool` for model visibility

## Current Tool Boundary

The runtime tool registry and the model-visible tool list are deliberately
different objects:

- The registry is the complete dispatch inventory. It retains executable
  implementations and metadata needed to validate and execute a tool call.
- The model-visible pool is a filtered projection passed to the model for the
  current query iteration.

Hiding a tool from the model is not authorization. The execution path still
resolves the registry entry and applies validation, plan/repeated guards, hooks,
and permissions.

## Assembly contract

`AssembleToolPool` snapshots enabled, non-hidden registry entries, then applies:

1. Optional whole-pool scope (`AllowedNames`).
2. Optional built-in selection (`BuiltInNames`); MCP tools are a separate
   partition and are not removed by built-in selection.
3. Simple-mode filtering of built-ins.
4. Blanket-deny permission rules.
5. Stable sorting within built-in and MCP partitions.
6. Deduplication with the built-in partition first.

QueryEngine translates `--tools` exclusions into deny rules as defense in
depth, builds the initial projection in `ToolUseOptions.Tools`, and installs
`RefreshTools`. Canonical ProjectGraph round preparation calls that refresh
only between completed tool rounds, so one model request sees a stable tool
surface.

Plan Mode adds a central projection after ordinary assembly. While Active, the
model sees only the explicit exploration and clarification allowlist,
TodoWrite for process-local planning state, Write/Edit for the plan-file
capability, and Exit. Bash, Agent/background, ordinary project mutators,
unknown dynamic/MCP tools, and Enter fail closed. The same decision runs again
at execution, where Write/Edit must target the exact absolute session/Agent
plan file and cannot use prefix, traversal, cross-session, or symlink aliases.
Registry entries remain available for dispatch validation; Plan filtering does
not unregister them. Active state comes from
`QueryEngine.PlanState`, not a transport-local flag. A committed Enter/Exit
therefore changes the model projection only when `RefreshTools` runs at the
next completed-round boundary; a denied, failed, or cancelled transition does
not refresh the surface. A resumed session retains its validated exact
Plan-file identity even when the current `HOME` differs; the engine carries
that capability through canonical tool context instead of letting tools
recompute another path.

## Registration and execution

`RegisterDefaults` installs built-ins and their behavioral metadata. MCP client
discovery can add `mcp__server__tool` entries to the same registry. `Registry.Get`
rejects disabled tools; `GetIncludeDisabled` is administrative and must not be
used to bypass runtime policy.

`Registry.Resolve` atomically returns requested/canonical identity,
registered/enabled state, implementation, and the current capability
generation. Register, unregister, enable, disable, contract application, and
implementation update advance that generation. Aliases resolve to the
canonical implementation and share enable/disable/unregister behavior.
Immediately before a permission-bound execution, QueryEngine acquires an exact
requested/canonical/generation lease. Registry mutations wait until the lease
is consumed at dispatch, so a disable, unregister, alias replacement, or
implementation update cannot cross the final authorized generation.
Dynamic Agent descriptions are updated through the owning QueryEngine registry,
not the process-wide compatibility pointer. Starting an engine with a different
registry therefore cannot advance this registry's generation or invalidate an
unrelated in-flight permission action.

Dynamic MCP rows use the same generation and lease boundary. One connection
generation owns one complete registry contribution through
`ReplaceOwnedTools`; refresh replaces that owner's full set, reconnect removes
the retired owner and adds the new owner in one commit, and close removes only
the exact owner. The registered implementation captures an exact SDK session
target. Replacement after permission but before lease acquisition fails the
generation check; replacement after lease acquisition waits for dispatch or
cancellation, and the dispatched implementation cannot re-resolve to a newer
MCP client.

EnterWorktree and ExitWorktree are reserved unavailable names, not default
tools. `Registry.Register` rejects either name as a primary name or alias, so
they cannot reappear in model-visible or dispatch inventory through a custom
registry. The final model-visible projection also removes caller-supplied
reserved schemas when an SDK supplies tools without a registry. Canonical
admission recognizes old transcript calls before input parsing, hooks,
permissions, or execution. The exported Go constructors are compatibility
stubs with no Git or process-CWD behavior; supported worktree isolation
remains an `Agent(isolation="worktree")` child-CWD capability backed by the
parent QueryEngine's durable lifecycle service.

`ToolImpl` metadata affects independent MCP-server strict mode, QueryEngine's
permission action, concurrency, result budgeting, interrupt behavior, tool
prompts, and custom validation. `ToolCapabilities` is host-owned evidence for
origin, action family, network, child, dynamic, direct-interaction, and shell
completeness; every default built-in declares it, and discovered MCP tools are
dynamic/network-capable. Registration metadata never becomes user authority.
Missing or external capability facts require a person in Auto unless exact
user authority covers the action.

`DefaultPermissionAllowed` is not a read-only claim or a general Auto escape:
QueryEngine accepts it only for a declared built-in process-local action.
TodoWrite uses it because it changes only process-local Session/Agent task
state and requires no ordinary interactive prompt, while an explicit deny or
ask rule remains authoritative. Plan capability admission does not trust
either that flag or `IsReadOnly`: the central allowlist is explicit, so an
unknown tool with permissive metadata still fails closed. Metadata changes
advance the registry generation and remain observable behavior changes.

The independent MCP server additionally rejects every
`IsPlanModeTransition` implementation before registration. That server has no
QueryEngine phase, exact-file identity, reviewed-byte settlement, or
interactive resume owner, so even a custom executable alias carrying the
transition marker cannot expose Enter/Exit through standalone MCP.

## Invariants and edge cases

- Registry completeness is required for dispatch even when a tool is not model
  visible in the current iteration.
- Permission-bound dispatch must use the exact resolution generation and
  execution lease; a prior `Get` result is not equivalent authority.
- Engine-owned metadata updates must target the engine registry. The exported
  default registry remains a compatibility fallback, not a cross-engine write
  owner.
- Built-in and MCP ordering is deterministic for prompt-cache stability.
- Built-in selection applies only to built-ins; explicit whole-pool scoping can
  restrict both partitions.
- A nil selection means no extra built-in restriction. An explicit empty
  selection exposes no built-ins.
- Registry changes become model-visible only at the safe refresh boundary; do
  not mutate a tool list while a model call is in flight.
- MCP publication changes the complete registry immediately and the model
  projection on its next safe assembly. It never edits an already submitted
  request.
- Equivalent CLI, ACP, leader, and child-agent behavior depends on using this
  assembly boundary instead of transport-local filtering.
- Hiding a Plan-disallowed tool is usability only; direct calls from an old
  transcript or caller must still fail at `executeToolCall`.
- Reserved unavailable built-in names fail before parsing or registry lookup
  and cannot be restored through `Registry.Register`.
- Exact-path and symlink checks run at admission, after supported input
  rewrites, and immediately before dispatch; complete resolved
  representations and containment are part of the settled action binding.
  The generic Write/Edit tools do not yet use descriptor-relative no-follow
  opens, so this does not claim to eliminate a hostile same-user change after
  the dispatch linearization point.
- TodoWrite derives its process-local state key from the trusted SessionID and
  AgentID injected by `QueryEngine.toolExecutor`; model input cannot select
  another Session or Agent. Leader and child lists therefore remain distinct.

## Code references

- [`Registry` and `ToolImpl`](../../../tools/registry.go)
- [`Registry.Register`](../../../tools/registry.go)
- [`RegisterDefaults`](../../../tools/registry.go)
- [`SetAgentTypeDescriptionsForRegistry`](../../../tools/agent.go)
- [`TodoWriteTool`](../../../tools/todo_write.go)
- [`Registry.List`](../../../tools/registry.go)
- [`Registry.ReplaceOwnedTools`](../../../tools/registry.go)
- [`Registry.RemoveOwnedTools`](../../../tools/registry.go)
- [`ToolPoolOptions` and `AssembleToolPool`](../../../tools/tool_pool.go)
- [`ToolSelection` and `ParseToolSelection`](../../../tools/presets.go)
- [`QueryEngine.modelVisibleTools`](../../../engine/engine.go)
- [`PlanState` transition owner](../../../engine/plan_state.go)
- [Central Plan tool policy](../../../engine/plan_tool_policy.go)
- [`executeToolCall` Plan defense](../../../engine/tool_execution.go)
- [`standaloneMCPToolExposable`](../../../server/mcp/server.go)
- [Canonical refresh boundary](../../../engine/round_lifecycle.go)

## Related tracking

Tool-call ordering is documented in [`model-and-tool-execution.md`](../runtime/model-and-tool-execution.md).
Migration decisions remain in [`migration/PLAN.md`](../../migration/PLAN.md).
