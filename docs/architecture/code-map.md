# Production Code Map

**Status:** current
**Last verified:** 2026-08-24

> **Ownership:** released-CLI package reachability, wiring labels, and owning
> architecture documents

This page answers two different questions: whether a package is present in the
released CLI dependency closure, and whether a particular responsibility in
that package is actually called. Reachability is necessary evidence, not proof
that every exported API is production behavior.

The reproducible scope is:

```bash
go list -deps ./cmd/yhc
```

Filtering that result to this module yields **52 module-local packages in the
released CLI dependency closure**. The closure already includes both supported
server implementations because the Cobra tree wires `serve acp` and `serve
mcp`. Reachability still does not make every exported capability active.

Comparing the same result with `go list ./...` yields 22 module-local packages
outside the closure: 11 runtime/library surfaces and 11 development or
verification-tool packages under `scripts/`. Both sets are listed below so a
new package cannot disappear behind the word "tooling."

## Wiring labels

- **active**: a production path constructs or invokes the stated responsibility.
- **entrypoint-specific**: active only in the named transport or process.
- **partially wired**: the package is reachable, but only part of the stated
  surface has production callers or the current implementation is deliberately
  incomplete.
- **outside released CLI closure**: the package compiles or has tests, but
  `cmd/yhc` does not import it transitively.

The execution authority is [`QueryEngine`](../../engine/engine.go), durable
[kernel validation](../../engine/query_kernel_selection.go), and the single
[ProjectGraph traversal](../../engine/graph_query_kernel.go). Direct `Query`
and supported new or resumed Sessions enter that Graph. Retired Legacy and
unpinned transcripts have no fallback executor.

## Enforced module directions

The iteration policy groups stable package/path owners under these module IDs:

| Module ID | Stable package/path owner |
|---|---|
| `engine-runtime` | `engine/...` |
| `tool-runtime` | the flat `tools` package |
| `cli-entrypoint` | `cmd/yhc/...` |
| `tui-adapter` | `internal/tui/...` |
| `acp-adapter` | `server/acp` |
| `mcp-adapter` | `server/mcp` |
| `build-metadata` | `internal/buildinfo` |
| `repository-tooling` | `scripts/...` |

Entrypoint adapters may compose and import engine/tool capabilities.
`engine-runtime` and `tool-runtime` production code may not import the CLI,
TUI, ACP, or MCP adapters. Test-only edges remain visible diagnostics but do
not satisfy or violate a production rule. `tools` remains flat: a new Go
package directory beneath `tools/` is rejected.

`go run ./scripts/iteration boundaries` is the executable no-new-edge oracle.
It compares global repository-internal base/head edge sets, so a rename or a
duplicate import does not manufacture a new edge. It fails only for a newly
introduced forbidden production edge or new flat-root package; existing edges
remain diagnostics under `boundaries --all`, not an exception list to expand.
This initial rule is deliberately narrower than a complete layer DAG.

## Composition and shared runtime

| Package | Stable symbol | Wiring | Owning document | Boundary |
|---|---|---|---|---|
| `cmd/yhc` | [`main`](../../cmd/yhc/main.go) | active | [entrypoints and transports](platform/entrypoints-and-transports.md) | Owns signal context and stable process exits. |
| `cmd/yhc/cmd` | [`newRootCommand`](../../cmd/yhc/cmd/root.go) | active | [entrypoints and transports](platform/entrypoints-and-transports.md) | Selects conversation, server, Goal, and administration processes. |
| `engine` | [`NewQueryEngine`](../../engine/engine.go) | active | [query engine](runtime/query-engine.md) | Owns conversation orchestration, durable session state, and provider-free administration hosts. |
| `internal/buildinfo` | [`Current`](../../internal/buildinfo/buildinfo.go) | active | [entrypoints and transports](platform/entrypoints-and-transports.md) | One renderer-neutral build identity feeds CLI, slash, MCP, and release metadata. |
| `tools` | [`Registry`](../../tools/registry.go) | active | [tool registry](capabilities/tool-registry.md) | Dispatch inventory is distinct from the filtered model-visible projection. |
| `internal/webui` | [`Assets`](../../internal/webui/assets.go) | entrypoint-specific: `serve app --web` | [desktop workbench](desktop-workbench.md) | Embedded same-origin UI assets and safe client projections; it owns no engine runtime. |

## Engine modules

| Package | Stable symbol | Wiring | Owning document | Boundary |
|---|---|---|---|---|
| `engine/attachments` | [`Processor.GetAttachments`](../../engine/attachments/attachments.go) | partially wired | [context assembly](runtime/context-assembly.md) | Canonical preparation calls the seam; the current processor contributes no attachments. |
| `engine/auth` | [`NewCredentialStore`](../../engine/auth/auth.go) | active | [model providers](platform/model-providers.md) | Credential storage supports provider resolution; it is not an authentication protocol. |
| `engine/budget` | [`TokenBudget`](../../engine/budget/token.go), [`USDBudget`](../../engine/budget/usd.go) | partially wired | [budgets and limits](runtime/budgets-and-limits.md) | Budget types do not imply one fully enforced global budget. |
| `engine/commands` | [`Registry`](../../engine/commands/registry.go) | active | [commands](capabilities/commands.md) | Owns contextual slash discovery, strict dispatch, tombstones, and atomic prompt-command generations. |
| `engine/compact` | [`BuildLLMAutoCompact`](../../engine/compact/llm_compact.go) | active | [compaction](runtime/compaction.md) | Owns thresholds and summary mechanics beneath the Graph lifecycle. |
| `engine/config` | [`LoadEffectiveConfig`](../../engine/config/config.go) | active | [configuration](platform/configuration.md) | Produces layered settings for composition roots. |
| `engine/containment` | [`NewExecutionPolicySnapshot`](../../engine/containment/policy.go) | active | [runtime services](platform/runtime-services.md) | Immutable execution-policy identity is propagated, but the current adapter is ambient-host compatibility rather than OS containment. |
| `engine/context` | [`BuildUserContext`](../../engine/context/context.go) | active | [context assembly](runtime/context-assembly.md) | Builds model context for canonical round preparation. |
| `engine/cron` | [`InspectLegacy`](../../engine/cron/migration.go) | entrypoint-specific: `migrate-state`; scheduler disconnected | [runtime services](platform/runtime-services.md) | Owns canonical task storage and explicit legacy import; no released entrypoint starts `Scheduler`. |
| `engine/diagnostics` | [`Snapshot`](../../engine/diagnostics/types.go) | active | [commands](capabilities/commands.md) | Renderer-neutral diagnostic schema; `QueryEngine` owns fact collection and redaction. |
| `engine/errors` | [`IsAbort`](../../engine/errors/errors.go) | entrypoint-specific: CLI; partially wired | [typed errors](runtime/typed-errors.md) | CLI exit mapping uses abort classification; the wider taxonomy is not runtime-wide authority. |
| `engine/execution` | [`CallModel`](../../engine/execution/call.go), [`ExecuteCommittedToolCalls`](../../engine/execution/streaming.go) | active | [model and tool execution](runtime/model-and-tool-execution.md) | Provider/stream mechanics feed Graph; they do not own traversal or admission policy. |
| `engine/history` | [`Manager`](../../engine/history/history.go) | entrypoint-specific: TUI | [TUI architecture](tui/README.md) | Prompt recall is separate from durable transcripts. |
| `engine/hooks` | [`Executor`](../../engine/hooks/hooks.go) | active | [hooks](capabilities/hooks.md) | QueryEngine owns synchronous and asynchronous hook lifetime. |
| `engine/internal/mediaimage` | [`Inspect`](../../engine/internal/mediaimage/validate.go) | active | [transcripts](state/transcripts.md) | Shared strict image validation and deterministic recovery derivatives. |
| `engine/internal/mediastore` | [`Store`](../../engine/internal/mediastore/store.go) | active | [transcripts](state/transcripts.md) | Session-private durable media bytes and opaque references. |
| `engine/internal/promptrecord` | [`Record`](../../engine/internal/promptrecord/record.go) | active | [transcripts](state/transcripts.md) | Versioned ordered prompt codec for replay, queueing, fork, and export. |
| `engine/internal/providerorigin` | [`Origin`](../../engine/internal/providerorigin/origin.go) | active | [model providers](platform/model-providers.md) | Binds durable provider and reasoning origin to dispatch identity. |
| `engine/internal/workboard` | [`NewLogicalWorkAdapter`](../../engine/internal/workboard/adapter.go) | active | [tasks and agents](runtime/tasks-and-agents.md) | Authoritative logical-work store, projection, execution links, and explicit recovery. |
| `engine/mcp` | [`MCPClient`](../../engine/mcp/sdk_client.go) | active | [MCP](capabilities/mcp.md) | Agent-side MCP client/manager; distinct from the standalone server. |
| `engine/memdir` | [`BuildUnifiedMemoryPrompt`](../../engine/memdir/prompt.go) | active | [memory directory](state/memory-directory.md) | Loads project/user memory into model context. |
| `engine/messages` | [`NormalizeMessagesForAPI`](../../engine/messages/normalize.go) | active | [model and tool execution](runtime/model-and-tool-execution.md) | Normalizes API sequences and preserves tool-call boundaries. |
| `engine/model` | [`GetCapabilities`](../../engine/model/capabilities.go) | active | [model providers](platform/model-providers.md) | Capability metadata also feeds context limits and role selection. |
| `engine/notify` | [`NotificationManager`](../../engine/notify/notify.go) | entrypoint-specific | [notifications](platform/notifications.md) | Constructed only by compositions that opt into notifications. |
| `engine/onboarding` | [`CheckOnboardingNeeded`](../../engine/onboarding/onboarding.go) | entrypoint-specific: TUI | [onboarding](platform/onboarding.md) | Interactive first-run surface. |
| `engine/permission` | [`PermissionPrompter`](../../engine/permission/check.go) | active | [permissions](capabilities/permissions.md) | Shared policy, reviewer, and durable settlement coordination. |
| `engine/plugins` | [`Loader.BuildCommandGeneration`](../../engine/plugins/loader.go) | partially wired | [plugins](capabilities/plugins.md) | Prompt commands and provider-free validation are active; other contribution kinds remain disconnected. |
| `engine/prefetch` | [`MemoryPrefetch`](../../engine/prefetch/memory.go) | partially wired | [prefetch](runtime/prefetch.md) | Memory/skill enrichers are active; the generic runner is not. |
| `engine/provider` | [`NewRuntime`](../../engine/provider/runtime.go) | active | [model providers](platform/model-providers.md) | Provider selection, credentials, role routing, and bounded failover composition. |
| `engine/recovery` | [`TryPTLRecovery`](../../engine/recovery/ptl.go) | active | [recovery](runtime/recovery.md) | Bounded context, media, and output recovery feed canonical lifecycle decisions. |
| `engine/services` | [`BackgroundServices`](../../engine/services/background_services.go) | partially wired; entrypoint-specific | [runtime services](platform/runtime-services.md) | Engine-owned long-session services are enabled only by selected root entrypoints. |
| `engine/session` | [`QuerySessions`](../../engine/session/query.go), [`SessionService`](../../engine/session_service.go) | active | [sessions](state/sessions.md) | Durable discovery, resume, fork, export, delete, and recovery facade. |
| `engine/skills` | [`SkillRegistry`](../../engine/skills/skills.go) | active | [skills](capabilities/skills.md) | Skill discovery and activation are capabilities, not an execution loop. |
| `engine/storage` | [`ResultStorage`](../../engine/storage/persistence.go) | active | [large tool results](state/large-tool-results.md) | Persists oversized tool output out of band. |
| `engine/transcript` | [`Recorder`](../../engine/transcript/persist.go) | active | [transcripts](state/transcripts.md) | Append-only messages, checkpoints, prompt records, file state, and usage authority. |
| `engine/transport` | [`ProjectLifecycleEvent`](../../engine/transport/lifecycle_jsonl.go) | entrypoint-specific: headless JSONL | [entrypoints and transports](platform/entrypoints-and-transports.md) | Projects validated canonical lifecycle facts and one final process result; legacy `StructuredIO` remains inactive. |
| `engine/worktree` | [`Service`](../../engine/worktree/service.go) | active | [tasks and agents](runtime/tasks-and-agents.md) | Owns isolated Agent worktree launch, handoff, continuation, cleanup, and restart projection. |

## TUI modules

Every package in this table is reachable only through the TUI entrypoint.

| Package | Stable symbol | Wiring | Owning document | Boundary |
|---|---|---|---|---|
| `internal/tui` | [`App`](../../internal/tui/app.go) | entrypoint-specific: TUI | [TUI architecture](tui/README.md) | Bubble Tea interaction shell and projection owner. |
| `internal/tui/attachments` | [`ResolveAttachment`](../../internal/tui/attachments/attachments.go) | entrypoint-specific: TUI | [composer contract](tui/contracts/composer.md) | Composer path/media resolution, distinct from engine context enrichment. |
| `internal/tui/keybindings` | [`NewResolver`](../../internal/tui/keybindings/resolver.go) | entrypoint-specific: TUI | [editing contract](tui/contracts/editing.md) | Effective keymap and user overrides. |
| `internal/tui/terminalcap` | [`Current`](../../internal/tui/terminalcap/capabilities.go) | entrypoint-specific: TUI | [terminal lifecycle](tui/contracts/terminal-lifecycle.md) | Terminal capability detection and conservative fallbacks. |
| `internal/tui/vim` | [`New`](../../internal/tui/vim/vim.go) | entrypoint-specific: TUI | [editing contract](tui/contracts/editing.md) | Composer Vim-mode state machine. |

## Server modules

| Package | Stable symbol | Wiring | Owning document | Boundary |
|---|---|---|---|---|
| `server/acp` | [`NewAgent`](../../server/acp/agent.go) | entrypoint-specific: ACP | [ACP adapter](platform/acp-adapter.md) | Owns per-session engines, protocol replay, negotiated extensions, and event projection. |
| `server/mcp` | [`Serve`](../../server/mcp/server.go) | entrypoint-specific: standalone MCP | [MCP](capabilities/mcp.md) | Dispatches tools directly and creates no conversation engine. |
| `server/appserver` | [`New`](../../server/appserver/server.go) | entrypoint-specific: `serve app` | [desktop workbench](desktop-workbench.md) | Authenticated loopback app-server, bounded session admission, replay-only durable history, and typed Desktop projection. |

## Packages outside the released CLI closure

These are real library, compatibility, experiment, or test surfaces. Their
presence must not be reported as shipped runtime policy.

| Package | Current placement | Relevant architecture owner |
|---|---|---|
| `engine/agents` | outside released CLI closure | [tasks and agents](runtime/tasks-and-agents.md) |
| `engine/analytics` | outside released CLI closure | [budgets and limits](runtime/budgets-and-limits.md) |
| `engine/api` | outside released CLI closure | [entrypoints and transports](platform/entrypoints-and-transports.md) |
| `engine/bridge` | outside released CLI closure | [entrypoints and transports](platform/entrypoints-and-transports.md) |
| `engine/filehistory` | outside released CLI closure; `FileTracker` does not power `/rewind` | [transcripts](state/transcripts.md) |
| `engine/queue` | outside released CLI closure; live input uses `RuntimeInputCoordinator` | [input queue](runtime/input-queue.md) |
| `engine/remote` | outside released CLI closure | [entrypoints and transports](platform/entrypoints-and-transports.md) |
| `engine/state` | outside released CLI closure | [runtime events](tui/contracts/runtime-events.md) |
| `engine/tasks` | outside released CLI closure; active work ownership is elsewhere | [tasks and agents](runtime/tasks-and-agents.md) |
| `engine/toolhooks` | outside released CLI closure | [hooks](capabilities/hooks.md) |
| `internal/tui/display` | outside released CLI closure; active display-cell logic lives in `internal/tui` | [responsive layout](tui/contracts/responsive-layout.md) |

Development and verification commands are also outside the released CLI
closure. They may execute the product as a subprocess or inspect its source,
but they do not become runtime owners:

| Package | Current placement | Relevant owner |
|---|---|---|
| `scripts` | outside released CLI closure; manifest command and build-dependency tests | [evolution process](../migration/GUIDELINE.md) |
| `scripts/cutover_recovery` | outside released CLI closure; explicit cutover inspection and repair | [evolution process](../migration/GUIDELINE.md) |
| `scripts/docs_check` | outside released CLI closure; documentation gate | [documentation policy](../contributing/documentation-policy.md) |
| `scripts/e2e` | outside released CLI closure; deterministic real-binary correctness harness | [testing strategy](../contributing/testing-strategy.md) |
| `scripts/evaluation` | outside released CLI closure; opt-in real-repository headless evaluation harness | [testing strategy](../contributing/testing-strategy.md) |
| `scripts/internal/ownedprocess` | outside released CLI closure; bounded descendant-process ownership shared by test/evaluation harnesses | [testing strategy](../contributing/testing-strategy.md) |
| `scripts/iteration` | outside released CLI closure; diff policy, evidence, boundary, and deep-discovery commands | [verification guide](../contributing/verification.md) |
| `scripts/migration_queue` | outside released CLI closure; evolution-queue validator and renderer | [product evolution plan](../migration/PLAN.md) |
| `scripts/migration_scan` | outside released CLI closure; dated repository/reference inventory | [project evolution status](../migration/STATUS.md) |
| `scripts/publication` | outside released CLI closure; public-tree publication policy checks | [verification guide](../contributing/verification.md) |
| `scripts/worktree_audit` | outside released CLI closure; worktree/session routing inspection | [verification guide](../contributing/verification.md) |

## Known partial boundaries

- Attachment enrichment is invoked but currently contributes nothing; TUI
  attachments are a separate active feature.
- The durable file-state cache and checkpoint repair support resume
  reconstruction, not workspace rewind. `engine/filehistory` remains outside
  the closure.
- Token, task, USD, context-window, and output limits have different wiring
  states; see [budgets and limits](runtime/budgets-and-limits.md).
- `engine/services`, plugins, and prefetch each combine active seams with
  disconnected helpers; their owning documents state the exact split.
- Historical `query_kernel_canary_stage` JSON remains readable for transcript
  compatibility, while current execution uses ProjectGraph terminology and one
  kernel owner.
- The P43 real-repository evaluation harness lives in `scripts/evaluation` and
  drives the public headless binary from outside the product closure. Its
  result is verification evidence, not another entrypoint or runtime owner.
