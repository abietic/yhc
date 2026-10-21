# Tool Registry and Model-Visible Pool

**Status:** current
**Last verified:** 2026-10-21

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

For each selected tool, `AssembleToolPool` includes host-owned `ToolImpl.Prompt`
instructions in that tool's model-visible description. It evaluates callbacks
outside the registry lock after pool filtering and decorates a copy of the
schema, so refresh does not duplicate instructions or mutate dispatch metadata.
Instructions already present in the description are not repeated. The model
receives the instructions with the tool schema through the shared QueryEngine
path, including child Agents; this does not add a checker or another model call.
Tools without a registered prompt keep their existing descriptions. The Read
guidance distinguishes successful text replacement from behavioral verification.

QueryEngine translates `--tools` exclusions into deny rules as defense in
depth, builds the initial projection in `ToolUseOptions.Tools`, and installs
`RefreshTools`. Canonical ProjectGraph round preparation calls that refresh
only between completed tool rounds, so one model request sees a stable tool
surface.

QueryEngine restricts the assembly name scope for Plan Mode, unavailable
built-ins, and Goal visibility before tool instructions are evaluated. Child
prompt assembly also applies Plan filtering first. While Plan is Active, the
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

`WebSearchTool` snapshots the optional `YHC_DEEPSEEK_SEARCH_API_KEY` environment
credential when the registry is built. With that explicit credential, ordinary
WebSearch calls first use DeepSeek's standalone Search JSON API; no provider
switch or native Responses `web_search` support is required. Without it, the
tool uses DuckDuckGo. The dedicated variable can reference the same key used by
the model (`export YHC_DEEPSEEK_SEARCH_API_KEY="$PROV_API_KEY"`), but the tool does
not infer credentials from unrelated provider accounts or proxy endpoints.

Both backends keep the existing query/domain schema, result limit, and final
allow/block filtering. DeepSeek summaries are preferred over short snippets;
page bodies are not requested. Queries beyond its 128-character limit, empty
results, known HTTP failures, and unbudgeted transport failures use DuckDuckGo.
The bearer key goes only to the fixed official HTTPS endpoint; paid redirects
and automatic retries are disabled. Cancellation reaches both requests and
prevents starting a fallback after cancellation. The primary gets at most ten
seconds within a shared 30-second deadline for the whole search, including
fallback. No server error body is exposed.

The default registry serves ordinary CLI/TUI, headless, ACP, child Agent, and
standalone MCP dispatch; no benchmark-only search implementation is installed.
QueryEngine supplies its existing usage scope during canonical tool execution.
A budgeted invocation without a matching request-price route uses the free
backend before paid admission. An ambiguous paid response under a cost/token
budget retains its liability and stops execution instead of issuing a fallback.
Request units and native-currency reservation semantics are owned by
[`budgets-and-limits.md`](../runtime/budgets-and-limits.md). WebFetch continues
to use its existing direct-page implementation; this change integrates Search.

Foreground `BashTool` formats captured results through
[`formatShellResult`](../../../tools/bash.go). Command echoes have a separate
512-byte preview budget; the tool-call input retains the full command, so a
long script is not repeated in full. Stdout and stderr each have a 10,000-byte
preview budget, including truncation notices. Oversized previews retain the head and
tail without splitting valid UTF-8, so final test summaries remain visible.
The combined result stays within 30,000 bytes and preserves the existing
nonzero-exit, timeout, and cancellation markers. Commands up to 512 bytes keep
their existing echoed text.
These are presentation limits: a preview cannot recover bytes already dropped
by the shell's containment output cap, and it does not change execution,
permissions, background-shell monitoring, or tool-error classification.

Non-empty text `Read` windows retain their numbered content and append a bounded
`Partial Read` notice when the selected window omits lines. The notice gives
the actual displayed range and total line count, including default-window and
hard-limit truncation. The input `offset` is zero-based, `line` is a one-based
centering target, and displayed line numbers start at one. Complete windows
omit the notice. Within the existing per-line truncation limit, text after the
line-number prefix preserves source tabs and spaces so it can be copied into
an exact `Edit.old_string`. Read does not expand tabs for terminal layout;
terminal consumers own that rendering. This notice describes the current view, not edit
admission: a prior complete read or successful Write/Edit remains sufficient
after a narrower reread.

Edit and overwriting Write still require complete read/write state. Partial
windows alone, even several separate windows, and shell reads do not establish
that state. Model-visible Read/Edit/Write guidance states this existing rule;
neither the state tracker nor the guard is relaxed. The per-call line limit
still prevents a first complete Read of files larger than that limit; cumulative
range admission is outside this change.

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
- [`ReadTool`](../../../tools/read.go) owns text-window feedback and read-state recording.
- [`RecordFileRead` and `HasFileBeenRead`](../../../tools/read_state.go) own complete-read admission state.
- [`editToolPrompt` and `readToolPrompt`](../../../tools/tool_prompts.go) describe the same rule to the model.
- [`QueryEngine.modelVisibleTools`](../../../engine/engine.go)
- [`PlanState` transition owner](../../../engine/plan_state.go)
- [Central Plan tool policy](../../../engine/plan_tool_policy.go)
- [`executeToolCall` Plan defense](../../../engine/tool_execution.go)
- [`standaloneMCPToolExposable`](../../../server/mcp/server.go)
- [Canonical refresh boundary](../../../engine/round_lifecycle.go)

## Related tracking

Tool-call ordering is documented in [`model-and-tool-execution.md`](../runtime/model-and-tool-execution.md).
Migration decisions remain in [`migration/PLAN.md`](../../migration/PLAN.md).
