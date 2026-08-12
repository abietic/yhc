# Command Surface Simplification Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-25; Eino-Agent `47d07aaf0111` and six local reference repositories

> **Ownership:** source-backed evidence for the post-P16 command discovery,
> compatibility-retirement, and default-workflow simplification decision.
> Current behavior belongs in
> [`architecture/capabilities/commands.md`](../../../architecture/capabilities/commands.md);
> completed delivery evidence belongs in
> [`p21-command-surface-simplification.md`](../../history/runtime/p21-command-surface-simplification.md).

## Answer

P16 solved command correctness and ownership. The remaining problem is product
discovery: the registry still represents removed or unavailable names as full
commands, the compatibility lifecycle is metadata-only, empty-query discovery
gives every supported command equal alphabetical weight, task phase is checked
after palette discovery, and the default workflow pack mixes universal coding
workflows with redundant or GitHub-specific recipes.

The revised target is not a fixed canonical-name count. It is:

- 39 currently supported core outcomes remain intact unless a separate
  user-outcome decision removes one;
- unavailable and hidden compatibility names leave the active `Command`
  catalog and become non-discoverable tombstones with replacement guidance and
  unqualified namespace reservation;
- stable aliases and deprecated aliases become different contracts, with
  warnings and enforceable removal boundaries only for the latter;
- empty-query TUI discovery shows a bounded high-frequency set plus
  process-local recent selections, while typed search still reaches every
  context-valid command;
- help is category- and entrypoint-aware;
- discovery uses the same typed availability rules as dispatch, while dispatch
  revalidates current state instead of trusting a stale palette snapshot; and
- the default bundled workflow pack contains only `/commit` and `/review`.

This corrects the earlier provisional “approximately 24 static commands plus 2
workflows” target. Mechanical family consolidation would reduce name count but
make distinct diagnostics, session shortcuts, Agent views, and TUI controls
harder to remember. Consolidation requires later usage evidence and is not P21
scope.

## Snapshot Boundary

| Repository | Verified revision | Command evidence reviewed |
|---|---|---|
| Eino-Agent | `47d07aaf0111` | Registry contract/defaults, strict dispatch, engine action owner, TUI admission/palette, bundled generation, focused tests, architecture and migration owners |
| Claude Code Ripe | `4b9d30f79532` | `prompt`/`local`/`local-jsx` types, static and runtime gates, source attribution, user/model invocation, lazy implementations |
| Codex | `66bd101fff6f` | Frequency-ordered built-ins, feature gates, active-task and side-conversation scopes, exact typed unavailable dispatch |
| Crush | `2af939d8e900` | System/custom/MCP/skill categories, user/project/server namespaces, argument metadata, loader diagnostics boundary |
| OpenCode | `411eff73f026` | Reachable command projection, category/suggested metadata, keymap-owned dispatch, empty-query suggestions |
| Pi | `c55ae2faa5d8` | 22-command core, extension/prompt/skill separation, source diagnostics, streaming command ordering |
| Grok Build | `a5727c596045` | Fail-closed capability gates, advertised/resolved command parity, session-scoped dynamic generation and collision tests |

These are repository-local snapshots. They are not claims about the latest
upstream releases.

## Corrections To The P16 Audit

The 2026-07-18
[`command-surface-audit.md`](command-surface-audit.md) remains the historical
P16 decision basis. Its pre-P16 Eino-Agent current-state section is no longer
current, and five reference revisions have advanced.

| Earlier conclusion | 2026-07-25 correction |
|---|---|
| Production parsing and action application were split. | P16 installed strict quote-aware `Registry.Dispatch`, one engine action owner, and typed cross-entrypoint results. |
| False visibility was the highest-priority problem. | False success is contained; the remaining issue is cognitive priority, compatibility retirement, and phase-aware discovery. |
| A small command surface implied converging many names. | Name-count reduction is not a success metric. Preserve distinct useful outcomes and simplify the default projection first. |
| Discovery and dispatch should use one capability snapshot. | They must use one typed rule set, but dispatch must revalidate live state so a stale palette snapshot cannot authorize execution. |
| Approximately 24 static commands plus 2 workflows was a useful target. | Only the 2-workflow default is retained. Static family consolidation is deferred until real usage evidence shows a net benefit. |

## Verified Current Eino-Agent Behavior

### Reachable inventory

Focused registry and palette tests confirm the current contract:

| Layer | Count | Discovery boundary |
|---|---:|---|
| Compiled core command records | 57 | Includes 39 supported, 13 unavailable, and 5 hidden |
| TUI core | 39 | Context may additionally remove `/effort` or `/copy` |
| Plain core | 29 | TUI-local commands excluded |
| Headless core | 18 | Engine-owned non-interactive subset |
| ACP core | 14 | Identity-changing slash lifecycle excluded |
| CLI administration slash commands | 0 | Cobra calls existing service/read-model owners |
| Default bundled workflows | 7 | TUI/plain only |
| Default maximum discovery | TUI 46; plain 36; headless 18; ACP 14 | Configured qualified plugin commands may add more |

Standalone MCP remains tools-only and is not a slash-command entrypoint.

### Reproduced productization gaps

| Gap | Current evidence | Consequence |
|---|---|---|
| Compatibility names remain full commands | `RegisterDefaults` constructs all 57 records; default contract logic later marks 13 unavailable and 5 hidden. | Registry/action/test complexity represents names that are not product capabilities. |
| Alias lifecycle is not executable | `CommandCompatibility` is cloned and defaulted, but dispatch/help do not distinguish or warn on deprecated aliases and do not enforce `RemovalBoundary`. | “One P16 compatibility window” can persist indefinitely and every alias is implicitly treated as deprecated. |
| Empty-query discovery has no product priority | `CommandPalette.applyFilter` gives every command score zero and `sortPaletteItems` breaks ties alphabetically. | High-frequency commands compete equally with advanced inspection and TUI configuration commands. |
| Help is flat | `/help` iterates `ListForContext` into one ungrouped list. | Users must understand a broad vocabulary before choosing an outcome. |
| Task phase is not part of discovery | The TUI palette receives terminal/model context but not `a.running`; after selection, `sendSlashCommand` permits only Agent/team/keybinding/queue projections and rejects the rest. | A command can be offered while a turn is active and then fail after selection. |
| The default workflow pack is not minimal | `/summary` duplicates an ordinary request; `/onboarding` mentions foreign `.claude/settings.json`; GitHub-specific `/pr-comments`, `/issue`, and `/commit-push-pr` are enabled for every user. | Default discovery mixes universal workflows, redundant prompts, and provider-specific external operations. |
| Dead action projections remain | TUI/plain/engine switches still contain actions used only by unavailable or hidden commands, including tag, logout, fast, branch, and undo paths. | Compatibility containment still carries maintenance and regression surface. |

These are product and maintainability gaps, not evidence that the 39 supported
outcomes are broken.

## Reference Findings

| Reference | Useful mechanism | Eino-Agent consequence | Rejected boundary |
|---|---|---|---|
| Claude Code Ripe | Separate prompt/local/UI command types, lazy local implementations, source/trust and user/model invocation metadata | Retain source/kind separation; consider lazy loading only after measured startup cost | Vendor auth, experiments, hosted workflows, and broad callback-owned local state |
| Codex | Presentation order is intentionally frequency-based; feature, active-task, and side-conversation gates hide commands while exact typed invocation can return a precise reason | Add explicit display order and phase scope; separate discovery hiding from dispatch diagnostics | Copying the large TUI-specific enum or its product integrations |
| Crush | System, user/project Markdown, MCP prompts, and skills occupy separate categories and qualified namespaces | Preserve qualified plugin commands and category boundaries | Silently skipping invalid custom files |
| OpenCode | Palette consumes only reachable commands; `category` and dynamic `suggested` drive an empty-query Suggested section | Add primary/secondary discovery and category-aware help/palette | Replacing the engine registry with a keymap or app-server architecture |
| Pi | A 22-command core is separate from extensions, prompt templates, and `/skill:*` commands | Preserve core/workflow/source separation and collision diagnostics | Copying provider-auth/share/tree commands or executing extensions immediately during streaming |
| Grok Build | Capability defaults fail closed; advertising and resolution share gates; dynamic commands are source-qualified and tested atomically | Make phase/capability rules typed and fail closed; preserve atomic prompt generation | Its much larger product surface and product-specific pager/shell commands |

No reference establishes that one command family is easier to use than several
searchable canonical names. The consistent cross-reference pattern is layered
discovery, typed context, precise direct errors, and source separation.

## Selected P21 Contract

### Catalog and compatibility

- `Command` represents supported executable capability only.
- `RemovedCommand` is a separate non-discoverable record containing canonical
  name, old aliases, reason, replacement, and removal version.
- Direct invocation may resolve a tombstone to typed unsupported guidance.
  `Get`, `List`, completion, help, palette, and active command counts do not
  treat it as a command, while its unqualified names remain reserved against
  dynamic-command capture.
- `Aliases` remain accepted stable names. Only names explicitly listed in
  `DeprecatedAliases` emit a warning and carry a removal boundary.
- P21 removes legacy action constants, handlers, and renderer cases after no
  supported command or compatibility path can produce them.

### Discovery

The minimal new metadata is:

```text
Category
DiscoveryTier: primary | secondary
DisplayOrder
PhaseScope: idle-only | any
```

The TUI derives one discovery view:

1. up to three process-local, successfully admitted recent commands that
   remain currently reachable;
2. the ordered primary set;
3. no secondary commands until the user starts searching.

Typed search covers all currently reachable primary and secondary commands.
`/help` shows all reachable commands grouped by category. Recent selections are
not persisted and create no new configuration or telemetry store.

The initial primary set is:

```text
new compact sessions model plan permissions
status diff files agents review commit
```

Entrypoint filtering can remove an item. Dynamic plugin commands default to
secondary unless their trusted source schema explicitly opts into a supported
tier in a later contract.

### Phase and capability semantics

- Discovery evaluates a detached typed command environment.
- Direct dispatch evaluates the current environment again immediately before
  handler execution.
- The initial active-turn policy preserves the exact current TUI behavior:
  `/agent`, `/team`, `/keybindings`, and `/queue` remain available; other
  commands remain idle-only.
- P21 does not expand mutation availability during a running turn.
- Engine/service turn locks and permission checks remain authoritative after
  command admission.

### Default workflows

The versioned embedded pack retains only:

```text
commit
review
```

The five removed names receive compatibility guidance. Configured qualified
plugin commands can restore equivalent local workflows without changing core
registry ownership. Prompt-command validation, source/trust attribution,
collision precedence, functional digest, and atomic generation replacement
remain unchanged.

## Exclusions

- No command-family merge solely to hit a count.
- No persistent command telemetry or MRU store.
- No model-visible slash-command invocation.
- No automatic MCP prompt-to-slash projection.
- No new plugin installer, marketplace, GitHub backend, or auth flow.
- No restoration of undo/rewrite/branch/rewind before their existing
  persistence and recovery gates close.

## Evidence

| Boundary | Current source |
|---|---|
| Command metadata and compatibility | [`Command`](../../../../engine/commands/registry.go#L197) |
| Registration/default compatibility behavior | [`applyCommandContractDefaults`](../../../../engine/commands/registry.go#L851) |
| Contextual discovery | [`Registry.ListForContext`](../../../../engine/commands/registry.go#L1266) |
| Live dispatch revalidation | [`Registry.Dispatch`](../../../../engine/commands/registry.go#L1325) |
| Exact current inventory | [`TestDefaultCommandContractAndAliasSnapshot`](../../../../engine/commands/commands_test.go#L653) |
| Single engine action owner | [`QueryEngine.executeCommand`](../../../../engine/command_executor.go#L28) |
| TUI running-turn admission | [`App.sendSlashCommand`](../../../../internal/tui/app.go#L4520) |
| TUI discovery environment | [`App.commandCapabilityContext`](../../../../internal/tui/app.go#L4956) |
| Alphabetical empty-query palette | [`sortPaletteItems`](../../../../internal/tui/command_palette.go#L424) |
| Default workflow data | [`workflows.json`](../../../../engine/plugins/bundled/workflows.json) |
| Atomic bundled generation | [`buildBundledWorkflowPack`](../../../../engine/plugins/bundled_workflows.go#L46) |

Focused evidence passed:

```bash
env GOCACHE=/tmp/eino-agent-go-cache go test ./engine/commands \
  -run 'TestDefaultCommandContractAndAliasSnapshot|TestUnavailableCommandsReturnCompatibilityErrorsWithoutActions|TestRegistryDispatchRejectsMalformedQuotedInputBeforeExecution|TestListForContext'

env GOCACHE=/tmp/eino-agent-go-cache go test ./internal/tui \
  -run 'TestCommandPalette'
```

## Recommendation

**Adoption decision: `combine`.** Preserve P16's strict typed execution and
atomic dynamic generation, adapt reference-proven category/suggested/phase
discovery, and use a project-owned tombstone and alias lifecycle. The observable
result is a smaller default cognitive surface without deleting useful current
capabilities or authorizing commands from stale discovery state.
