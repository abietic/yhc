# P21 Command Surface Simplification Delivery Record

**Status:** historical
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** completed post-P16 command-surface delivery: retired
> non-capability records, layered discovery, truthful phase availability, and
> a reduced default workflow pack without deleting useful supported outcomes.
> Current behavior belongs in
> [`architecture/capabilities/commands.md`](../../../architecture/capabilities/commands.md);
> current planning belongs in [`migration/PLAN.md`](../../PLAN.md).

## Decision

P21 was delivered under the **`combine`** decision:

- preserve P16's strict parser, typed registry, entrypoint scope, one action
  owner, typed result projection, and atomic prompt-command generation;
- adapt Codex active-task scopes, OpenCode suggested/category discovery, Crush
  and Pi source separation, and Grok Build fail-closed capability gates;
- use project-owned removed-command tombstones and explicit alias lifecycle;
  and
- reject command-count parity and mechanical family consolidation as success
  metrics.

The source comparison and correction of the earlier provisional 24-command
target are retained in
[`command-surface-simplification-audit.md`](../../reference/runtime/command-surface-simplification-audit.md).
Current behavior remains owned by
[`architecture/capabilities/commands.md`](../../../architecture/capabilities/commands.md).

## User Problem

P16 made visible commands truthful and single-owner. At P21 acceptance, its
compatibility containment had become permanent structure:

1. 13 unavailable and 5 hidden names were still constructed as full commands;
2. compatibility metadata is not rendered or enforced, and defaulting treats
   every alias as deprecated;
3. empty-query TUI discovery alphabetizes every reachable command instead of
   prioritizing common work;
4. help is ungrouped;
5. active-turn restrictions are applied after palette discovery; and
6. seven default workflows mix universal coding tasks, redundant prompts,
   foreign onboarding configuration, and GitHub-specific external operations.

Users therefore see a broad, equal-weight vocabulary even though the execution
kernel is already correct. Maintainers continue to carry handlers, action
variants, renderer switches, and compatibility tests for names that are not
capabilities.

## Scope And Non-Goals

### In scope

- compiled core command registration, removed-command guidance, aliases, and
  compatibility warnings;
- TUI/help/completion discovery metadata and ordering;
- idle versus active-turn availability across declared entrypoints;
- bundled workflow membership and compatibility guidance;
- dead action/handler/projection removal after reachability proof; and
- exact snapshots, negative entrypoint/phase tests, and documentation closeout.

### Not in scope

- merging supported commands merely to reduce the name count;
- changing the user outcome or durable owner of the 39 supported core commands;
- persistent usage telemetry or a persistent MRU store;
- exposing slash commands to the model;
- MCP prompt-to-slash projection;
- plugin install, marketplace, trust, GitHub backend, or auth work;
- restoring undo/rewrite/branch/rewind; or
- changing standalone MCP from a tools-only server.

## Acceptance Baseline

At Eino-Agent `47d07aaf0111`:

| Layer | Value at acceptance |
|---|---:|
| Compiled core command records | 57 |
| Supported core commands | 39 |
| Unavailable core records | 13 |
| Hidden core records | 5 |
| Default bundled workflows | 7 |
| Maximum TUI/plain/headless/ACP/administration discovery | 46/36/18/14/0 |

The implementation reproduced these values from source and focused tests before
changing them. Registration alone was not treated as reachability evidence.

## Frozen Invariants

1. Every discoverable command remains executable on the current entrypoint and
   phase or is excluded before selection.
2. Direct invocation revalidates live capability immediately before handler
   execution; a stale discovery result cannot authorize an action.
3. Runtime/session mutation remains single-owner and at-most-once.
4. Removed names can return typed migration guidance but cannot appear in
   active lookup, help, completion, palette, or active-count results. Their
   unqualified names remain reserved against dynamic-command capture.
5. Stable aliases remain accepted without deprecation warnings. Deprecated
   aliases are explicit, warn on use, and have a machine-testable removal
   boundary.
6. Prompt workflows continue through ordinary model tools and permissions.
   No slash handler gains privileged Git, filesystem, or external-service I/O.
7. Prompt-command generation remains collision-safe and atomic; a rejected
   candidate leaves the prior generation live.
8. TUI-only actions remain TUI-only. Headless, ACP, administration, and
   standalone MCP do not gain presentation owners.
9. No durable schema or configuration store is added.
10. Rollback restores the last single owner; it never restores unsafe logout,
    incomplete history mutation, or successful-looking placeholders.

## Delivered Contract

### Active commands and removed names

`Command` represents an executable capability. A separate `RemovedCommand`
record contains:

```text
Name
Aliases
Reason
Replacement
RemovedIn
```

Registry discovery and active counts ignore removed records. Their canonical
names and old aliases remain reserved against unqualified dynamic commands, so
a plugin cannot silently replace migration guidance. Strict direct dispatch
may resolve one only to an unsupported result with `ActionNone`; source-
qualified plugin commands remain independent names.

The 13 unavailable and 5 hidden P16 records move to this catalog. Dead action
variants and renderer branches are deleted once no supported or tombstone path
can produce them. P21 does not delete any of the 39 supported outcomes.

### Alias policy

The registry stops auto-populating `DeprecatedAliases` from every `Aliases`
entry.

Initial stable aliases:

```text
clear: reset
quit: exit
context: ctx
team: teams
```

Expired P16 aliases stop executing and move to replacement guidance:

```text
stats, cost -> usage
settings -> config
allowed-tools -> permissions
bashes -> tasks
```

Aliases belonging to removed commands stay only on their tombstone.

### Discovery metadata

The active contract adds:

```text
Category
DiscoveryTier: primary | secondary
DisplayOrder
PhaseScope: idle-only | any
```

The initial categories are Session, Runtime, Safety, Workspace, Agents,
Extensions, UI, and Workflow.

Empty-query TUI discovery shows, in order:

1. up to three process-local recent selections that are still reachable;
2. the following deduplicated primary commands; and
3. no secondary commands until search text exists.

```text
new compact sessions model plan permissions
status diff files agents review commit
```

Typed search covers every context-valid primary and secondary command.
`/help` groups every reachable command by category and display order.
Configured plugin commands default to secondary.

Recent selections live only in the `CommandPalette` instance and are recorded
only after successful command admission. Rejected, cancelled, or stale-phase
dispatches do not enter Recent. P21 adds no telemetry, disk write, config key,
or cross-session behavioral claim.

### Phase availability

Discovery receives a detached typed environment with at least entrypoint and
idle/active-turn phase. Dispatch rebuilds or refreshes that environment and
evaluates the same availability rules again.

The delivered active-turn set preserved the exact accepted TUI projection:

- `/agent`;
- `/team`;
- `/keybindings`; and
- `/queue`.

All other commands remain idle-only until a separate user outcome and
state-owner analysis accepts broader concurrency.

### Default workflow pack

The embedded pack is version-bumped and retains:

```text
commit
review
```

Compatibility guidance for removed defaults:

| Removed workflow | Guidance |
|---|---|
| `summary` | Ask for a summary normally; use `/compact` only when context compaction is the desired outcome. |
| `onboarding` | Use project-native `/init`. |
| `pr-comments` | Request the workflow normally or define a qualified configured plugin command. |
| `issue` | Request the workflow normally or define a qualified configured plugin command. |
| `commit-push-pr`, `cpr` | Use `/commit`, then explicitly request push/PR creation under ordinary tools and permissions, or define a qualified plugin workflow. |

Configured plugin commands can restore equivalent local workflows without
reintroducing them into the default pack.

## Ordered Slices

| Slice | Delivered outcome | Dependency | Final state |
|---|---|---|---|
| P21.0 | Replace unavailable/hidden command records with typed tombstones; make alias lifecycle explicit; delete newly unreachable action paths | G11.A promotion boundary | `Complete` |
| P21.1 | Add category/tier/order/phase discovery, grouped help, bounded Suggested/Recent palette, and live dispatch revalidation | P21.0 | `Complete` |
| P21.2 | Reduce the embedded workflow pack from seven commands to `commit` and `review` while preserving generation atomicity and migration guidance | P21.1 | `Complete` |
| P21.3 | Ran cross-entrypoint closeout, synchronized current owners, closed the gap, and recorded this history | P21.2 | `Complete` |

P20.R3 had already closed G10. P21.0-P21.3 formed one serialized delivery
program and are all complete. The program has left the live queue; current
planning is now intake-only in [`migration/PLAN.md`](../../PLAN.md).

## P21.0 Catalog And Compatibility Hygiene

### Delivered changes

- introduce the removed-command record and direct guidance path;
- move all 13 unavailable and 5 hidden built-ins out of active registration;
- remove implicit “all aliases are deprecated” defaulting;
- classify stable and expired aliases explicitly;
- emit a typed deprecation warning only for aliases still inside an accepted
  boundary;
- remove dead handlers, action constants, engine/TUI/plain cases, and tests
  only after reachability proves no supported command emits them; and
- update exact inventory and collision tests.

### Delivered evidence

- active compiled core snapshot is exactly 39 supported commands;
- no active command has hidden or unavailable static availability;
- removed names and aliases never enter discovery or active counts, but reject
  unqualified dynamic-command collisions;
- every removed direct invocation returns stable unsupported guidance and
  `ActionNone`;
- stable aliases produce no warning;
- expired aliases cannot execute the replacement command implicitly; and
- no removed action reaches engine or renderer application.

### Recovery boundary

Rollback could restore the tombstone catalog and alias diagnostics independently;
it could not restore unavailable names as discoverable commands or re-enable
their actions.

## P21.1 Layered And Phase-Aware Discovery

### Delivered changes

- add category, discovery tier, display order, and phase scope to immutable
  command snapshots;
- type the command environment rather than adding more stringly `Extra` keys;
- make help/completion/palette consume the same discovery projection;
- make dispatch re-evaluate current environment and produce an exact phase or
  capability reason;
- implement bounded Suggested and process-local Recent palette sections;
- record Recent only after successful admission;
- search the complete reachable set only after query input; and
- preserve source/trust display for dynamic commands.

### Delivered evidence

- empty-query TUI palette contains at most 15 unique entries: three recent plus
  twelve primary;
- rejected, cancelled, and stale-phase dispatches do not alter Recent;
- the primary order is deterministic and not alphabetical;
- typing searches every currently reachable primary and secondary command by
  name, alias, and description;
- `/help` groups all reachable commands without hiding secondary capabilities;
- active-turn discovery exposes exactly the accepted four current projections;
- a command discovered while idle but dispatched after a turn starts fails
  closed with a precise reason; and
- plain/headless/ACP projections do not gain TUI-only commands.

### Recovery boundary

The metadata fields can remain ignored while the old full-list discovery is
restored. Rollback cannot bypass live dispatch availability.

## P21.2 Minimal Default Workflows

### Delivered changes

- version-bump the embedded workflow pack;
- retain only `commit` and `review`;
- add removed-workflow guidance to the tombstone catalog;
- keep configured plugin precedence and qualified names unchanged; and
- update generation, digest, discovery, prompt, and compatibility fixtures.

### Delivered evidence

- default maximum discovery became TUI 41, plain 31, headless 18, ACP 14,
  administration 0;
- disabling bundled workflows still removes both defaults without changing
  core commands;
- malformed bundled or configured candidates leave the previous generation
  live;
- configured plugins can provide qualified replacements without colliding with
  core or unqualified bundled names; and
- `/onboarding` cannot inject foreign `.claude` configuration guidance.

### Recovery boundary

The rollback boundary required restoring the previous versioned pack as one
atomic data rollback without partially mixing workflow versions or bypassing
ordinary tool permissions.

## P21.3 Closeout

Closeout updated the current architecture and interaction guide, recorded the
final state in `STATUS.md`, removed G13 from `REMAINING.md`, removed P21 from
the live queue, and moved this detailed contract to history. Current owners
contain the final active-core/tombstone/workflow counts and no active P21.3
language. Future command-family consolidation remains excluded unless new
usage evidence enters gap intake.

## Verification

The delivery slices ran their focused tests. P21.3 also passed the
repository-wide gates and recorded this closeout matrix:

```bash
make fmt
make lint
make test
make build
make docs-check
git diff --check
```

Recorded focused evidence:

| Boundary | Minimum test |
|---|---|
| Active/tombstone catalog | Exact command, alias, tombstone, collision, and unavailable-result snapshots |
| Compatibility | Stable alias, deprecated warning, expired alias, and removal-version tests |
| Phase | Idle/active-turn positive and negative tests for TUI, plain, headless, and ACP |
| Discovery | Suggested order, recent deduplication, search-all, category help, and contextual omission tests |
| Execution | Single action owner and stale-discovery/live-dispatch revalidation |
| Workflows | Exact two-command pack, version/digest, precedence, malformed reload, and qualified plugin replacement |
| Closeout | Registry parser/catalog/aliases/tombstones, palette/help/phase, single action/replay, plain/headless/ACP projection, bundled generation, and CLI plugin inspection suites passed |

The focused cross-entrypoint command was:

```bash
go test ./engine/commands ./internal/tui ./engine/plugins ./engine ./cmd/eino-agent/cmd ./server/acp -run 'Test(P21|ParseCommandInput|DefaultCommandContractAndAliasSnapshot|UnavailableCommandsReturnCompatibilityErrorsWithoutActions|CommandExecutorDispatchesAndAppliesExactlyOnce|RuntimeReplayRetainsTypedCommandOutcomes|HeadlessProjectsTypedCommandResult|PlainCommandRunsThroughEngine|ACPProjectsTypedCommandResult|BundledWorkflow|BuildCommandGeneration|QueryEnginePluginCommands|PluginsCLIUsesCandidateValidationAndAtomicInspectionReload|InspectionAdministrationEngineLoadsOnlyInspectionOwners)' -count=1
```

It passed, covering the exact catalog/aliases/tombstones, phase/discovery/
palette/help, single action/replay, plain/headless/ACP projection,
workflows/generation, and administration inspection boundaries.

## Source Owners

| Boundary | Owner |
|---|---|
| Command metadata, registration, dispatch, tombstones | [`engine/commands/registry.go`](../../../../engine/commands/registry.go) |
| Core command tests | [`engine/commands/commands_test.go`](../../../../engine/commands/commands_test.go) |
| Engine action application | [`engine/command_executor.go`](../../../../engine/command_executor.go) |
| TUI phase admission | [`internal/tui/app.go`](../../../../internal/tui/app.go) |
| TUI discovery and recent state | [`internal/tui/command_palette.go`](../../../../internal/tui/command_palette.go) |
| Bundled workflow data and validation | [`engine/plugins/bundled/workflows.json`](../../../../engine/plugins/bundled/workflows.json), [`engine/plugins/bundled_workflows.go`](../../../../engine/plugins/bundled_workflows.go) |
| Current architecture | [`docs/architecture/capabilities/commands.md`](../../../architecture/capabilities/commands.md) |
| User projection guide | [`docs/guides/interaction-modes-and-commands.md`](../../../guides/interaction-modes-and-commands.md) |
