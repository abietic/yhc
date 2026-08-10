# P47.3 Exact Thread Navigation

**Status:** historical
**Closed gaps:** G40
**Completed:** 2026-08-07
**Adoption:** `combine`

> **Ownership:** completion evidence for the exact Task Explorer switch target,
> bounded TUI correlation, compatibility boundaries, and G40 closure. Current
> behavior belongs in the [Task and Agent runtime
> architecture](../../../architecture/runtime/tasks-and-agents.md) and the
> [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P47.3 keeps `QueryEngine` as the runtime authority and `App` as the view-state
authority. The engine now resolves one
`TaskExplorerNavigationTarget{SessionID, ThreadID, AgentID, Generation, Mode}`
from the exact execution generation and exactly one current runtime-thread
catalog entry. Switch declaration and `ApplyTaskExplorerAction` call the same
resolver, so a switch result cannot be reconstructed from a standalone thread
string or a different generation.

Only one readable current `live_attach` target acquires switch. Missing,
duplicate, mismatched, superseded, replay-only, evicted, predispatch, or
revision-raced facts return typed unavailable or stale outcomes before model,
tool, Agent, permission, transcript, or Git dispatch.

Ctrl+T copies the complete typed result and revalidates it before view
activation. The exact correlation remains attached to the bounded transcript
pager: initial, older, and forced-refresh requests validate before dispatch and
their results validate again before projection. A reused ThreadID therefore
cannot apply a superseded generation's delayed or paged transcript, nor can an
ID-only catalog lookup prewrite the exact view's mode or label.

## Compatibility

The result's legacy SessionID and ThreadID fields remain populated for additive
Go source compatibility. No stored schema, WorkBoard record, replay event,
wire protocol, permission rule, provider route, or transcript format changed.
The Agent picker, Ctrl+B, `/team`, replay and evicted inspection, and generic
`activateThreadByID` callers retain their existing ID-oriented resolution and
bounded pager behavior. P47.4-P47.7 still own mixed rows, filter/focus, and
snapshot/lazy detail depth.

## Proof And Review

Engine table oracles cover the exact current generation, same-thread
supersession, missing and duplicate catalog facts, Session/Agent/transcript/mode
mismatch, replay-only, evicted, predispatch, and runtime/catalog revision
races. An engine-wired runner oracle proves successful declaration, public
resolution, typed application, and unchanged runner generation/status/pending
facts.

TUI oracles cover malformed result rejection, activation-before-mutation,
exact single-page dispatch, same-thread generation rebound, delayed initial
page rejection, PgUp rejection before a second request, and refresh failure
without mode or label prewrite. Focused normal/race suites and the complete
engine and TUI package tests passed before repository closeout.

Independent review found and drove closure of three related async boundaries:
delayed initial-page application, later paging correlation, and ID-only refresh
metadata prewrite. The final follow-up reported no remaining finding. The
repository formatting, lint, test, build, documentation, queue, migration
manifest, and diff gates passed on the final caller worktree. This slice changes
no terminal geometry or protocol lifecycle, so it claims no new PTY,
physical-terminal, or live-provider evidence; remote CI remains a separate PR
gate.

## Rollback

A squash revert removes the additive resolver/target and the Ctrl+T-only exact
activation path without data migration. Generic thread navigation remains
available, but rollback reopens G40 because switch availability and activation
would again be separated by an ID-only boundary.
