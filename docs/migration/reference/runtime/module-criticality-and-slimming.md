# Module Criticality and Slimming

**Status:** reference-snapshot
**Last verified:** 2026-07-14
**Result:** P11 audit and P12 disconnected TUI removal complete at this snapshot

> **Ownership:** this report classifies module criticality and proposes bounded
> slimming experiments and records P11/P12 evidence. Executable scope remains
> authoritative in [`migration/PLAN.md`](../../PLAN.md), and current implementation facts
> belong in [`migration/STATUS.md`](../../STATUS.md).

## Objective Function

Slimming should optimize four costs independently:

1. **Reasoning cost:** how many runtime owners and state models a maintainer
   must understand.
2. **Maintenance cost:** source/test lines and contracts that must remain valid.
3. **Product surface:** tools, commands, modes, and settings exposed to users or
   the model.
4. **Distribution cost:** binary size, dependency graph, startup, and build
   matrix.

Deleting an unreferenced package improves the first two but may not shrink a Go
binary because the linker already excludes it. Provider build profiles can
shrink the binary while increasing release complexity. These are different
projects and should have different success metrics.

## Current Surface

| Surface | Current evidence | Interpretation |
|---|---:|---|
| Production Go | 407 files, 128,194 lines | P12 removed 19 disconnected files and 3,602 lines |
| Test Go | 317 files, 97,317 lines | Strong evidence, also a substantial maintenance surface |
| TUI | 81 production files, 34,781 lines | Live root and helper packages remain the primary product |
| Built-in tools | 41 | Several overlapping task/MCP/team/monitor surfaces |
| Built-in commands | 66 per runtime registry status | Broad operational and product UX |
| Packages with zero production importers | 11 non-entrypoints plus 3 legitimate `main` packages | Deterministic `go list` report after P12 |
| Zero-importer code | 6,655 production + 6,185 test lines | Remaining engine-only decision surface |
| Stripped binaries | about 50-55MB across current targets | Primarily affected by provider/protocol dependencies, not dead packages |

The liveness result now comes directly from the deterministic
`scripts/migration_scan` `go list -json` report, including production,
`TestImports`, and `XTestImports` reverse edges. P12 removed the 18-package TUI
cohort after P11 found no static, dynamic, generated, test, external-API, or
release-closure reachability. External API uncertainty still applies to the 11
exported `engine/*` packages.

## Criticality Rings

### K0: Agent Kernel

Without these concepts the product is no longer a coding agent.

| Capability | Current owner | Keep invariant |
|---|---|---|
| Model call and streaming | `engine/execution`, `engine/provider`, Eino adapters | One provider-aware call boundary |
| Turn/query loop | `engine/query.go`, `QueryEngine` | One ordering authority and terminal result |
| Tool schema and dispatch | `tools.Registry`, `engine/tool_execution.go` | Deterministic model-visible tools and one execution path |
| Tool safety | `engine/permission`, validation, cancellation | No UI-local authorization or side-effect admission |
| Context/system prompt | `engine/context`, config/model capability | Bounded, provider-aware prompt assembly |
| Runtime events | `engine/events.go`, runtime emitter | Stable identity and lossless terminal/interaction events |

Removal rule: K0 behavior may be simplified only behind an unchanged
observable contract. It should not be made optional through build tags.

### K1: Core Coding-Agent Product

These are not mathematically required for an agent loop, but they define the
intended daily product.

| Capability | Current owner | Why critical |
|---|---|---|
| TUI and terminal lifecycle | `internal/tui` root, keybindings, terminalcap | User's primary interaction surface |
| Session/transcript/replay | `engine/session`, `engine/transcript` | Work must survive restart and remain inspectable |
| Compaction/recovery/budget | `engine/compact`, `recovery`, `budget` | Long coding sessions must not fail abruptly |
| Subagents | `AgentRunner`, `engine/subagent.go`, runtime store | Current product differentiator and user priority |
| Busy input and controls | `engine/queue`, TUI per-thread state | Async work must remain controllable |
| File/result state | file history, storage, attachments | Safe edits, recovery, and rich prompts |

Removal rule: K1 work requires a user-workflow replacement, not just a compile
proof.

### K2: Standard Extensions

These should remain available but need not live in the kernel or every build.

| Capability cluster | Modules/surfaces | Direction |
|---|---|---|
| Hooks and plugins | `engine/hooks`, `engine/plugins` | Keep one extension lifecycle; reject shadow hook/plugin systems |
| Skills and custom Agents | `engine/skills`, active custom-agent loader | Keep one loader and one scope model |
| MCP client and resources | `engine/mcp`, dynamic MCP tools | Keep as standard profile if real workflows depend on it |
| Commands | `engine/commands` | Keep registry, reduce default visibility by profile/value |
| Web/LSP/worktree | corresponding tools and services | Standard coding profile, individually disableable |
| Notifications/onboarding/auth | engine and TUI adapters | Product shell, not agent kernel |

Removal rule: K2 modules need usage or explicit support-policy evidence.

### K3: Optional Or Niche Product Capabilities

| Capability | Current examples | Slimming question |
|---|---|---|
| Protocol servers | ACP and MCP server entrypoints | Separate binary/build profile or keep bundled? |
| Multi-team layer | TeamCreate/TeamDelete and team presentation | Does `Agent` plus SendMessage cover the real workflow? |
| Schedulers/monitors | ScheduleCron, ScheduleWakeup, Monitor, Sleep | Is persistent background automation a supported product? |
| Specialized editing | NotebookEdit | Is notebook work frequent enough for the default tool prompt? |
| Self-configuration | Config, McpAuth | Should the model mutate configuration, or should UI/commands own it? |
| Convenience synthesis | Brief and several workflow commands | Does it close a workflow not covered by prompting/skills? |
| Extra providers | all six statically linked adapters | Which provider combinations need one release artifact? |

Removal rule: first hide from the default profile, measure breakage/usage, then
delete only after a deprecation window.

### K4: Unreachable Or Shadow Surface

The engine row below is the current zero-import non-entrypoint surface; the TUI
row preserves the P12 removal baseline.

| Cohort | Packages | Production lines | Test lines | Initial confidence |
|---|---|---:|---:|---|
| Shadow/unused engine | `engine/agents`, `analytics`, `api`, `bridge`, `cron`, `errors`, `messages`, `remote`, `state`, `toolhooks`, `transport` | 6,655 | 6,185 | Statically unreachable; external-API decision remains |
| Disconnected TUI scaffolds | Retired by P12 | 0 current | 0 | 18 packages, 19 files, and 3,602 lines removed |
| **Current total** | 11 engine packages | **6,655** | **6,185** | Separate API/behavior decision pending |

The removed TUI cohort expanded to
`internal/tui/collapse`, `internal/tui/components/agents`,
`internal/tui/components/design`, `internal/tui/components/diff`,
`internal/tui/components/help`, `internal/tui/components/mcp`,
`internal/tui/components/messages`, `internal/tui/components/permissions`,
`internal/tui/components/prompt`, `internal/tui/components/select` (package
`selectcomp`), `internal/tui/components/settings`,
`internal/tui/components/shell`, `internal/tui/components/spinner`,
`internal/tui/components/tasks`, `internal/tui/input`,
`internal/tui/rendering`, `internal/tui/state`, and `internal/tui/ui`.

Notable duplication:

- `engine/state` still competes conceptually with the accepted
  `RuntimeStateStore`; P12 removed the disconnected TUI mirror.
- `engine/bridge` implements another state/client/plugin/service architecture
  but has no product importer.
- `engine/agents` is a second custom-Agent loader while the active runtime uses
  other loading and AgentRunner paths.
- `engine/toolhooks` shadows the integrated `engine/hooks` execution path.
- The retired TUI `components/*` tree mirrored a decomposed component
  architecture, while the live application renders from the root package.

Deletion rule: static in-repository reachability is already excluded. P11
accepted only the TUI cohort because it has no external API boundary. The 11
`engine/*` packages remain a separate decision.

## P11 Audit Result

P11 separated code reachability from migration-ledger truth:

- the 18 TUI packages contain 19 production files and 3,602 lines, have no
  production or test importer, and are absent from every release dependency
  closure;
- the live CLI enters `internal/tui.New`, and the root `internal/tui` package,
  `attachments`, `display`, `keybindings`, `terminalcap`, and `vim` own the
  actual product path;
- no build constraint, CGo, generator, embed, linkname, plugin, or string-based
  loader makes the disconnected packages reachable;
- `manifest.yaml` has one validated target path into
  `internal/tui/state/app_state.go` and 366 notes naming the disconnected
  cohort: 341 belong to `excluded` entries and 25 to `adapted`/`done` entries.

The last point prevented an unreviewed file deletion, not cohort acceptance.
P12 removed every retired reference and reviewed each affected mapping before
deletion.

## P12 Removal Result

### Source And Product Proof

| Metric | Before P12 | After P12 | Result |
|---|---:|---:|---|
| Production files | 426 | 407 | -19 |
| Production lines | 131,796 | 128,194 | -3,602 |
| Go packages | 70 | 52 | -18 |
| TUI production files | 100 | 81 | -19 |
| TUI production lines | 38,383 | 34,781 | -3,602 |
| Test files / lines | 317 / 97,317 | 317 / 97,317 | unchanged |
| Zero-import non-entrypoints | 29 | 11 | only the engine cohort remains |
| Registered tools / commands | 41 / 66 | 41 / 66 | unchanged |
| CLI `--help` SHA-256 | `486a397b...cfde` | `486a397b...cfde` | unchanged |

Forced release builds were byte-for-byte identical before and after deletion:

| Target | Before | After |
|---|---:|---:|
| linux/amd64 | 56,586,402 | 56,586,402 |
| darwin/amd64 | 58,256,832 | 58,256,832 |
| darwin/arm64 | 52,174,178 | 52,174,178 |
| windows/amd64 | 58,000,384 | 58,000,384 |

All four release dependency closures contain zero retired paths. The focused
PTY, current-output goldens, and performance-budget suite passed three
consecutive runs; the full 3,808-test suite and repository gates also passed.

### Manifest Reconciliation

The 341 entries already classified `excluded/not_started` now state that their
React/Ink files are file-level exclusions rather than falsely claiming a port
to the retired tree. The remaining affected entries were reviewed individually:

| Reference entry | Reviewed result and P12 decision |
|---|---|
| `src/context/notifications.tsx` | Live toast behavior is independently owned/tested; no file-level context parity, so `excluded/not_started` |
| `src/services/mcpServerApproval.tsx` | Live dialog lacks the reference pending-server discovery service; excluded |
| `src/state/AppStateStore.ts` | Canonical runtime state is a different bounded model; no full cross-subsystem store parity, so excluded |
| `src/state/store.ts` | No live generic React-style store owner; excluded |
| `src/state/teammateViewHelpers.ts` | Live thread navigation is independently mapped; retain/eviction helper parity is not claimed |
| `src/utils/agenticSessionSearch.ts` | Live deterministic session listing is not agentic transcript search; excluded |
| `src/utils/analyzeContext.ts` | No equivalent detailed context-accounting helper; excluded |
| `src/utils/ansiToSvg.ts` | SVG export is outside the terminal renderer scope; excluded |
| `src/utils/backgroundHousekeeping.ts` | Engine background services do not prove reference desktop housekeeping parity; excluded |
| `src/utils/cliHighlight.ts` | Live Markdown highlighting uses a different backend and contract; file-level helper excluded |
| `src/utils/collapseBackgroundBashNotifications.ts` | Live Bash lifecycle rendering does not implement the reference consecutive-message transform; excluded |
| `src/utils/collapseHookSummaries.ts` | No equivalent hook-summary collapse transform; excluded |
| `src/utils/collapseReadSearch.ts` | Live grouped rendering is independently mapped and does not claim the reference message transform |
| `src/utils/collapseTeammateShutdowns.ts` | No focused shutdown-collapse behavior; excluded |
| `src/utils/contextAnalysis.ts` | No equivalent token-metric helper; excluded |
| `src/utils/contextSuggestions.ts` | No equivalent context-suggestion product path; excluded |
| `src/utils/desktopDeepLink.ts` | Claude Desktop deep links are outside the supported CLI scope; excluded |
| `src/utils/directMemberMessage.ts` | Current Agent steering does not implement the reference `@member` mailbox syntax; excluded |
| `src/utils/earlyInput.ts` | Composer input does not implement pre-start stdin capture; excluded |
| `src/utils/fullscreen.ts` | Live terminal lifecycle is independently specified and does not claim reference tmux/env heuristic parity |
| `src/utils/ghPrStatus.ts` | No live GitHub PR-status fetch integration; excluded |
| `src/utils/handlePromptSubmit.ts` | Live submission/queue behavior is independently mapped; the reference orchestration helper is not a file-level port |
| `src/utils/hyperlink.ts` | Live file links do not implement the reference general URL capability/fallback helper; excluded |
| `src/utils/immediateCommand.ts` | Live busy-command handling does not claim the reference experiment-gated policy; excluded |
| `src/utils/inProcessTeammateHelpers.ts` | Live Agent/team state does not prove these AppState mutation helpers; excluded |
| `src/utils/sessionFileAccessHooks.ts` | No equivalent session-file analytics hook contract; excluded |

This reclassified 26 unsupported `adapted/done` entries, moving manifest totals
from 838 to 812 mapped/done files. A tested manifest lint now rejects any
retired path in `targets`, `tests`, or `notes`; the post-P12 hit count is zero.

## Tool Criticality

| Tier | Tools | Recommendation |
|---|---|---|
| T0 core | Read, Grep, Glob, Edit, Write, Bash, BashOutput, KillShell, AskUserQuestion, Agent | Default and always tested |
| T1 standard | plan transitions, Task lifecycle, SendMessage, TodoWrite, Skill, ToolSearch, Web, LSP, dynamic MCP, worktree | Standard coding profile |
| T2 optional | NotebookEdit, team tools, scheduler/wakeup, Monitor/Sleep, Brief, Config, explicit MCP resource/auth gateway | Hide behind capability/profile until value is proven |
| Compatibility | unified `Task` plus TaskCreate/Get/Update/List/Stop/Output and Agent overlap | Define one task vocabulary and deprecate duplicates gradually |

Do not remove BashOutput/KillShell independently from background Bash. Do not
remove SendMessage while asynchronous Agent steering remains a product goal.

## Command Criticality

Rather than judging 66 commands one by one immediately, classify command
families:

| Tier | Command families | Direction |
|---|---|---|
| C0 | help, quit, clear, compact, status, model, mode, resume, permissions | Core |
| C1 | agent/tasks/team, queue, files, context, history, diff, plan, session/fork/rewind | Standard TUI workflow |
| C2 | MCP, skills, hooks, settings, keybindings, terminal, doctor, auth | Optional operational profile |
| C3 | bug, release notes, share, PR/review/commit helpers, tags/themes/output style, convenience wrappers | Measure use; candidates for plugin/skill migration |

Commands that merely inject a prompt are usually cheaper as skills or plugin
commands. Commands that change runtime/session/terminal state need typed engine
actions and should remain built-ins.

## Snapshot-Era Source-Slimming Sequence

This sequence records the options considered at the assessment boundary. S0
and S1 are complete; S2-S5 are not an executable queue here. Unaccepted gaps
belong in [`migration/REMAINING.md`](../../REMAINING.md), and accepted order belongs
in [`migration/PLAN.md`](../../PLAN.md).

### S0: Guardrails

- P11 completed the TUI dynamic-reachability and external-API decision; retain
  the separate `engine/*` public-API decision.
- P12 completed a deterministic package-liveness report based on `go list`.
- Preserve H0's real-Makefile dependency regression for every production source
  root and release/debug target.
- Record baseline compile, test, binary size, startup, and default tool prompt.

### S1: Remove Disconnected TUI Scaffolds - Complete

P12 removed the complete disconnected TUI cohort with the required proof:

- zero production imports and no build-tag/blank-import reachability;
- no docs, manifest entries, or tests name it as the accepted live owner;
- TUI focused tests, PTY, goldens, full gates, and binary behavior are unchanged.

Realized benefit: 3,602 fewer production lines and one fewer misleading UI/state
ownership model. Binary size remained exactly unchanged, as expected.

### S2 Candidate: Remove Shadow Engine Packages

Start with small leaf packages, then audit `engine/bridge` separately because it
contains a large unused architecture and test suite. Keep removals cohesive and
reversible.

Expected upper bound: about 6.7K production and 6.2K test lines. Expected
binary-size benefit: negligible because these packages are already unreachable.

### S3 Candidate: Converge Task Models

Current task concepts include:

- generic typed tasks under `engine/tasks`;
- task-list records under `tools.TaskManager`;
- executable coding Agents under `AgentRunner`.

`engine/tasks` is imported only by command fallback code. Before removal, map
ShellTask to background Bash, AgentTask to AgentRunner, DreamTask to the actual
long-session service, and `/tasks` to the canonical runtime selector. The end
state should have one user-visible task list and one executable Agent owner.

### S4 Candidate: Reduce Default Product Surface

- Add persisted, privacy-safe local tool/command usage counters or run an
  explicit dogfood study; registry in-memory metadata is insufficient.
- Define `core`, `standard`, and `full` profiles.
- Hide T2/C3 surfaces from the default model/UI before deleting implementation.
- Measure prompt tokens, command discoverability, and workflow regressions.

### S5 Candidate: Slim Distribution Artifacts

Current stripped binaries are about 50-55MB. `go mod why` shows provider paths
pulling Google Cloud, AWS/Bedrock, and Volcengine SDK families; ACP and MCP also
add protocol dependencies.

Evaluate only after source ownership is clean:

1. split provider factories into build-tagged files or separate provider
   bundles;
2. consider `eino-agent` and `eino-agent-full` artifacts;
3. consider separate ACP/MCP server binaries if users do not need one universal
   executable;
4. compare binary size, cold startup, build duration, support matrix, and user
   configuration cost.

Do not trade one 50MB binary for a confusing provider installation story
without measured distribution pressure.

## Recommendations at the Snapshot Boundary

| Decision | Recommendation now |
|---|---|
| Delete K4 packages immediately | TUI cohort complete; retain all 11 engine packages until a separate API/behavior decision |
| Replace QueryEngine/query loop | No |
| Replace JSONL with SQLite | No |
| Add app-server boundary | No |
| Converge state owners | TUI shadow store removed; engine shadow stores require a separate audit |
| Converge task systems | Yes, after command fallback mapping |
| Reduce default tools/commands | Yes, profile-first and evidence-driven |
| Split provider/protocol binaries | Measure first; likely worthwhile only for distribution goals |
| Continue 1:1 Claude feature expansion | No; Claude remains behavioral evidence, not automatic scope |

## Evaluation Metrics for Any Accepted Slimming Program

A successful program should report:

- fewer production concepts and owners, not only fewer files;
- source/test lines removed by accepted cohort;
- unchanged K0/K1 scenario and PTY behavior;
- reduced default model-visible tool schema and prompt tokens where intended;
- binary/startup/build improvements only for distribution slices;
- no increase in conditional branches inside the core query loop;
- updated `STATUS`, subsystem docs, and history without reopening completed
  migration mappings.

The whole-runtime comparison and rationale are in
[`agent-runtime-comparison.md`](agent-runtime-comparison.md).
