# P31.1a Reversible WorkBoard Shadow

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for the off-by-default WorkBoard format,
> comparison shadow, failure isolation, Session-sidecar deletion, and rollback
> boundary. P31.1b still owns any authoritative cutover.

## Outcome

P31.1a completed the first `combine` slice without replacing current
logical-work owners. `tools.TaskManager` and the trusted
`(SessionID, AgentID)` Todo store remain authoritative. Successful TaskCreate,
mutating TaskUpdate/TaskStop, and TodoWrite operations freeze their existing
result and detached state before invoking an optional observer. Invalid input,
reads, unchanged updates, and terminal stop no-ops do not observe.

The trusted `QueryEngineConfig.WorkBoardShadow` switch is off by default. When
enabled on a root engine, one `workboard.Shadow` is shared only with child
engines in that lineage. Independent roots remain isolated. Observer panic,
codec rejection, filesystem failure, or invalid target-domain state cannot
change legacy output, runtime state, TUI projection, or the authoritative
stores.

## Record, Identity, And Lifecycle

`engine/internal/workboard` owns version-1 domain types, strict JSON codec,
stable Task and Todo identities, partitioned full-replacement projection,
graph/status validation, and bounded diagnostics. The record limits are 1,024
items, 4,096 dependency references, 64 KiB per textual or canonical-metadata
field, 4 MiB encoded JSON, and 128 diagnostics.

The shadow writes
`<transcript-dir>/<session-id>.workboard-shadow-v1.json` only when the parent
directory is private. It refuses a link, non-regular target, or non-`0700`
existing directory instead of changing that directory's mode. The mode-0600
record is installed with one serialized same-directory
create/chmod/write/sync/close/rename transaction. Every frozen writer and load
stage is injectable; a failed replacement preserves the previous complete
record or no record and removes its temporary file when possible.

Existing records are validation-only. They never seed TaskManager, Todo state,
runtime events, model input, Session replay, or TUI state. Resume, fork, and
new-session activation clear the source lineage observer before rebinding
identity. Session deletion owns only the exact validated shadow suffix and
reports whether it was removed.

## Verification And Rollback

The frozen compatibility fixture runs with the observer disabled and enabled.
Focused domain, codec, stable-ID, partition, graph, budget, corrupt-record,
identity/version, private-directory, failure-stage, replacement, concurrency,
root/child isolation, baseline resume, activation disable, and deletion-race
tests pass. Focused race tests and all repository, documentation, manifest,
format, lint, test, and build gates are recorded in
[`p31-1a-reversible-workboard-shadow.md`](../../verification/p31-1a-reversible-workboard-shadow.md).
An independent persistence/concurrency review found and closed stale-shadow
activation and non-private-parent defects; re-review found no remaining issue.

Rollback disables the trusted switch and removes only the exact shadow
sidecar. No authoritative WorkBoard mutation or compatibility marker exists,
so rollback loses no user-visible plan state and raises no Session reader
floor. P31.1b remains queued for a separate forward-only authority decision.
