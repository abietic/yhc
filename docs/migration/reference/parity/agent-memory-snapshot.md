# Agent Memory Snapshot Parity Audit

**Status:** reference-snapshot
**Last verified:** 2026-07-12
**Result:** P8 adaptation complete at this snapshot

> **Ownership:** This report owns reference comparison and accepted adaptation
> for P8 Agent memory snapshots. Current implementation belongs in
> [`architecture/state/memory-directory.md`](../../../architecture/state/memory-directory.md);
> closeout evidence belongs in
> [`migration/history/runtime/p1-p8.md`](../../history/runtime/p1-p8.md).

## Observable Question

How does a custom agent discover project-provided memory snapshots, initialize
or update its scoped local memory, and remember which snapshot timestamp has
already been handled?

## Reference Contract

| Behavior | Reference evidence | Observable result |
|---|---|---|
| Snapshot source | `src/tools/AgentTool/agentMemorySnapshot.ts:getSnapshotDirForAgent` | Project snapshots live at `<cwd>/.claude/agent-memory-snapshots/<agentType>/`; `snapshot.json` contains a non-empty `updatedAt`. |
| Destination | `agentMemory.ts:getAgentMemoryDir` | Agent memory is distinct per agent and supports `user`, `project`, and `local` scopes. |
| Discovery | `checkAgentMemorySnapshot` | Missing/invalid metadata yields `none`; no local `.md` yields `initialize`; existing local `.md` plus missing or older sync metadata yields `prompt-update`. |
| Initial copy | `initializeFromSnapshot`, `copySnapshotToLocal` | Direct regular snapshot files except `snapshot.json` are copied without deleting local files, then the snapshot timestamp is stored. |
| Replace | `replaceFromSnapshot` | Existing top-level local `.md` files are deleted before copying; non-Markdown files and nested directories remain. The sync timestamp advances afterward. |
| Keep | `markSnapshotSynced` | Local content is unchanged and `.snapshot-synced.json` records `{ "syncedFrom": snapshotTimestamp }`. |
| Definition load | `loadAgentsDir.ts:initializeAgentMemorySnapshots` | Only custom agents with `memory: user` participate. Empty local memory initializes automatically; newer snapshots become `pendingSnapshotUpdate` and are not applied silently. |
| Agent prompt | `agentMemory.ts:loadAgentMemoryPrompt`, `loadAgentsDir.ts` | A memory-enabled custom agent receives Read/Edit/Write access and an agent-specific persistent-memory prompt/index. |
| User choice | `main.tsx` and `dialogLaunchers.tsx` | The interactive top-level custom-agent mode offers merge/keep/replace. The referenced `SnapshotUpdateDialog` source is absent from this snapshot, so its internal merge completion ordering is unresolved. |

Reference copy errors are logged and swallowed inside `copySnapshotToLocal`, so
the caller may still write a sync marker after a partial copy. The Go
adaptation will intentionally fail the operation and preserve the old marker;
advancing durable metadata without durable content would make recovery
impossible.

## Pre-Slice Go State

| Area | Evidence | Classification |
|---|---|---|
| Agent memory paths | `engine/memdir/paths.go:GetAgentMemoryDir` | Present, but project/local resolution uses process CWD rather than an engine-owned root. |
| Custom agent metadata | `engine/agent_definitions.go` | `memory:` is not parsed or validated. |
| Agent memory prompt | `engine/subagent.go:buildSystemPrompt` | No agent-specific memory index or scope guidance. |
| Snapshot discovery/copy/replace/marker | No production symbols | Missing. |
| Pending update behavior | No definition field or resolver | Missing. |

AgentRunner execution metadata and JSONL transcripts are unrelated runtime
state; they do not satisfy this file-seeding contract.

## Accepted Go Adaptation

1. Add project-root-aware agent-memory paths and a sanitized agent directory
   key; existing compatibility helpers continue to use the configured/current
   root.
2. Add strict snapshot and sync metadata readers. Timestamps must be RFC3339;
   invalid metadata fails closed as absent/unsynced rather than relying on
   JavaScript `Invalid Date` comparisons.
3. Serialize snapshot mutations, read all source content before mutation, use
   atomic file writes, and advance `.snapshot-synced.json` only after the copy
   or replace succeeds.
4. Preserve reference file selection: direct regular files only,
   `snapshot.json` excluded, and replace deletes top-level `.md` files only.
5. Parse `memory: user|project|local` on custom agents. As in the reference,
   snapshot auto-initialization and pending-update discovery apply only to
   `user` scope.
6. Append the agent-specific memory prompt/index at spawn time and include
   Read/Edit/Write in an explicit custom-agent tool allowlist when memory is
   configured.
7. Expose replace and keep resolution through the engine-owned agent catalog.
   A newer snapshot remains pending until an explicit caller resolves it. Go
   has no reference-equivalent top-level `--agent` startup dialog, so no silent
   default choice is introduced.

## Acceptance Criteria

- Snapshot paths cannot escape through agent names, traversal, or symlinks.
- Missing, malformed, equal, older, and newer timestamps produce deterministic
  `none`, `initialize`, or `prompt-update` actions.
- Initialize copies direct snapshot files and writes the marker only on success.
- Replace removes only top-level Markdown, preserves unrelated files, and does
  not leave an advanced marker after copy failure.
- Keep changes only the synced marker.
- Custom-agent `memory:` parsing rejects unknown scopes and injects the scoped
  prompt into a real subagent system prompt.
- Definition loading initializes empty user memory and exposes newer snapshots
  as pending without overwriting local content.
- Parent/child project roots, resume behavior, focused race tests, and all
  repository gates pass.

## Non-Goals

- reconstructing the missing React `SnapshotUpdateDialog` implementation;
- adding a new top-level custom-agent CLI mode solely for dialog parity;
- treating AgentRunner transcript persistence as agent memory;
- recursive snapshot directories, content-level merge, or cross-process locks
  absent from the reference contract.

## Completion Evidence

- `engine/memdir/agent_snapshot.go` owns strict discovery, direct-file copy,
  replace, keep markers, atomic writes, rollback, and containment checks.
- `engine/agent_definitions.go` parses `memory:` and initializes user-scope
  custom agents; `engine/subagent.go` owns pending decisions and scoped prompt
  assembly; `engine/engine.go` exposes the explicit resolver.
- `engine/memdir/agent_snapshot_test.go` covers the timestamp matrix, copy and
  preservation rules, failed replace, and traversal/symlink rejection.
- `engine/agent_definitions_test.go` covers metadata, tools, real prompt
  assembly, user-only initialization, and keep/replace resolution.
- Focused package and race tests pass. Repository-wide gate evidence is owned
  by `../STATUS.md` after closeout.
