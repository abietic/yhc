# P31.5 Old-Owner Deletion and Cross-Entrypoint Closeout

**Status:** historical
**Closed gaps:** G33
**Completed:** 2026-07-31

> **Ownership:** completion evidence for explicit QueryEngine and standalone
> Task/Todo/Agent owners, canonical task-facing projections, exact
> queued-input cancellation, entrypoint labels, G33 closure, and the retained
> no-global rollback boundary.

## Outcome

P31.5 completed the accepted `combine` program. A root QueryEngine lineage now
owns one durable WorkBoard compatibility facade and one AgentRunner, and every
tool call receives those exact owners explicitly. Direct or non-Session calls
without an owner fail with one stable error and mutate nothing. Explicit
embeddings may bind isolated ephemeral owners without naming a durable Session.

The package Todo map, default TaskManager and AgentRunner, current package
runtime snapshot, AppState task store, compatibility selector, and TUI
TaskManager or AgentID/status mutation providers are gone. Task/Todo schemas
and successful result bytes remain unchanged; the WorkBoard v2/v3 reader,
marker, immutable links, settlement, and Session lifecycle remain authoritative.

## Entrypoints and Projection

Every standalone MCP `Serve` creates fresh ephemeral Todo and Task owners and
exposes only the exact Task/Todo compatibility allowlist. It has no Agent,
Team, Goal, plan, Session, WorkBoard artifact, or execution-link authority, and
one server cannot inspect or stop another server's records.

`/tasks`, Ctrl+T, Ctrl+B, `/team`, activity, sidebar, thread detail, queued
input, and eviction now consume one bounded TaskExplorer projection. The text
command labels durable WorkBoard ownership and read-only command control. A
legacy Session without authoritative WorkBoard state keeps exact-generation
read-only transcript and navigation rows without mutation capability.

## Exact Queued Input

TaskExplorer send returns the request ID as a stable MessageID and uses it as
the Agent command UUID. Retrying the same request for the same generation does
not enqueue twice. `cancel_input` carries that MessageID and exact board and
generation identity; cancellation and child drain linearize under one message
lock. The result distinguishes cancelled, already drained or unknown, and
stale generation without touching another queued-input chip.

## Verification and Rollback

Focused owner, compatibility, standalone isolation, exact-generation,
concurrency, lifecycle, canonical TUI, responsive/golden, PTY, repository,
documentation, manifest, and source-policy evidence passes. Independent
second-line review reported no findings after verifying that retained
QueryEngine-scoped Go compatibility methods are absent from every TUI
production path and guarded by a static source test. Reproducible commands are
in
[`p31-5-old-owner-closeout.md`](../../verification/p31-5-old-owner-closeout.md).

Rollback may disable standalone Task/Todo exposure or use an earlier
presentation over TaskExplorer. It retains explicit owners, the v2/v3 reader
floor, authority record, immutable links, and exact-generation dispatcher. It
cannot restore package globals, AppState task ownership, implicit fallback,
linked Session downgrade, or ID/status TUI control. G33 is closed, and no
successor became `Ready`.
