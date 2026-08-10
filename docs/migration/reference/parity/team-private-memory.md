# Team and Private Memory Parity Audit

**Status:** reference-snapshot
**Last verified:** 2026-07-12
**Result:** P8 adaptation complete at this snapshot

> **Ownership:** This report owns source-backed comparison and the accepted Go
> adaptation for P8 team/private memory. Current implementation facts belong in
> [`architecture/state/memory-directory.md`](../../../architecture/state/memory-directory.md);
> closeout evidence belongs in
> [`migration/history/runtime/p1-p8.md`](../../history/runtime/p1-p8.md).

## Observable Question

How do private and team memory paths become model-visible, searchable, safe to
write, and synchronized, and which parts can a provider-neutral Go runtime
faithfully expose without Anthropic's hosted product backend?

## Reference Contract

| Behavior | Reference evidence | Observable result |
|---|---|---|
| Enablement | `src/memdir/teamMemPaths.ts:isTeamMemoryEnabled` | Team memory requires auto memory and a separate feature gate; no team-only mode exists. |
| Paths | `getAutoMemPath`, `getTeamMemPath`, `getTeamMemEntrypoint` | Private memory is project-scoped; team memory has a distinct `team/` root and its own `MEMORY.md`. |
| Prompt policy | `src/memdir/memdir.ts:loadMemoryPrompt`, `src/memdir/teamMemPrompts.ts:buildCombinedMemoryPrompt` | The model sees both roots, scope guidance, two-step topic-file/index writes, sensitivity rules, and an explicit statement that both directories already exist. |
| Index injection | `src/utils/claudemd.ts:getMemoryFiles`, `getClaudeMds` | Private and team `MEMORY.md` files are loaded independently; team content is labeled shared and wrapped in `team-memory-content`. Each index is capped at 200 lines and 25,000 bytes. |
| Topic discovery | `src/memdir/memoryScan.ts:scanMemoryFiles` | Recursive markdown discovery excludes every `MEMORY.md`, reads at most 30 frontmatter lines, sorts newest first, and caps the manifest at 200 files. |
| Path safety | `src/memdir/teamMemPaths.ts:validateTeamMemWritePath`, `validateTeamMemKey` | Traversal, encoded/unicode separators, dangling links, link loops, and deepest-existing-ancestor symlink escapes fail closed. |
| Secret safety | `src/services/teamMemorySync/teamMemSecretGuard.ts`, `secretScanner.ts` | High-confidence credentials are rejected before team-memory writes and skipped before upload. |
| Startup sync | `src/services/teamMemorySync/watcher.ts:startTeamMemoryWatcher` | Hosted sync performs an initial pull before watching, then debounces pushes and flushes pending work during shutdown. |
| Conflict behavior | `src/services/teamMemorySync/index.ts:pullTeamMemory`, `pushTeamMemory`, `syncTeamMemory` | Session startup is pull-first; a local edit is local-wins for the edited key; ETags and per-entry SHA-256 hashes bound retries and delta uploads. |
| Hosted prerequisites | `isUsingOAuth`, `getTeamMemorySyncEndpoint`, `getGithubRepo` call sites | Remote sync requires first-party Anthropic OAuth scopes, the private `/api/claude_code/team_memory` endpoint, and a GitHub repository slug. |

## Go State at the Assessment Boundary

| Area | Evidence | Classification |
|---|---|---|
| Private path and index primitives | `engine/memdir/paths.go`, `memdir.go`, and `engine/engine.go` | Implemented and wired into production prompt assembly. |
| Recursive topic scan | `engine/memdir/scan.go`, `team.go:ScanMemoryDirectories`, and focused tests | Implemented independently per scope and combined private-before-team. |
| Combined private/team paths | `engine/memdir/team.go:GetTeamMemPath`, `ScanMemoryDirectories` | Implemented through an explicit provider-neutral shared root. |
| Combined prompt and dual index injection | `engine/memdir/prompt.go:BuildUnifiedMemoryPrompt`, `engine/engine.go` | Implemented in real QueryEngine model input. |
| Shared-write safety | `ValidateTeamMemWritePath`, `ValidateTeamMemoryContent`, and Write/Edit callers | Implemented with resolved containment and bounded credential detection. |
| Synchronization | `EINO_AGENT_TEAM_MEMORY_DIR` is the canonical shared directory; `EINO_AGENT_REMOTE_MEMORY_DIR` remains the private base override | Implemented as direct shared storage without a local mirror. |
| Hosted Anthropic protocol | Manifest excludes `src/services/teamMemorySync/*` | Intentionally unavailable and provider-specific. |

`engine/context.ContextRefresher` has a separate generic `MemoryDir` facility,
but it has no production caller and loads only top-level markdown files. It is
not evidence that memdir indexes or team scope reach the model.

## Accepted Go Adaptation

The first P8 slice will preserve the reference's model-visible and filesystem
contracts while replacing the hosted backend boundary:

1. `EINO_AGENT_TEAM_MEMORY_DIR` is an explicit absolute shared-directory
   backend. A valid value enables team memory only when auto memory is enabled.
   The directory itself is canonical, so collaborators using the same mounted
   or synchronized storage observe writes without a duplicate local mirror.
2. Private memory remains at `GetAutoMemPath()`. Team memory receives separate
   path, entrypoint, scope, prompt, scan, and write-safety handling.
3. Both directories are created before model invocation. Their `MEMORY.md`
   indexes are independently truncated and injected every turn; team content is
   visibly marked shared.
4. Team path validation reuses the same logical, final, and
   deepest-existing-ancestor containment principles as the permission layer.
   Team writes additionally reject a bounded set of high-confidence credential
   patterns.
5. Recursive topic manifests remain directory-local, newest-first, and capped
   at 200 entries. The combined API preserves private-before-team ordering and
   scope metadata.

This is an intentional provider-neutral divergence from HTTP pull/push,
OAuth, ETags, server entry caps, and GitHub-only repository identity. Those
hosted protocol files remain excluded; direct shared storage is the sync
boundary and must not be described as Anthropic organization sync.

## Acceptance Criteria

- Disabled auto memory disables team memory even when a shared root is set.
- Relative, empty, root-level, traversal, NUL, and symlink-escaping team paths
  fail closed.
- Private and team directories and entrypoints are distinct and deterministic.
- Both indexes reach the real QueryEngine model input in private-before-team
  order, with independent truncation and shared-content labeling.
- Combined topic discovery preserves scope and never treats an index as a topic
  file.
- Write and Edit reject high-confidence secrets only for team-memory targets;
  private memory and ordinary project files retain existing behavior.
- TUI, plain/headless, sub-agent, and ACP entrypoints inherit the engine-owned
  prompt behavior without frontend-specific assembly.
- Focused race tests and all repository gates pass.

## Deferred Non-Goals

- Anthropic OAuth and `/api/claude_code/team_memory` compatibility;
- GitHub-only repository identity and hosted organization policy;
- HTTP ETag, 412/413, server-side entry-cap, and telemetry semantics;
- an additional local mirror or conflict resolver on top of an already shared
  directory.

## Implementation Status

Implemented and verified on 2026-07-12. The shared-directory adaptation is
wired through QueryEngine, CLI, ACP, and nested QueryEngines; focused race tests
and all repository gates pass. The hosted protocol exclusions above remain
unchanged.
