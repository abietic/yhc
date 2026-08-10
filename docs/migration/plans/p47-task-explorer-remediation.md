# P47 Task Explorer Correctness and Depth Remediation

**Status:** historical
**Created:** 2026-08-07
**Approval:** approved by the user on 2026-08-07
**Execution state:** P47.1-P47.7 complete; G38-G41 closed

> **Ownership:** accepted target behavior, slice boundaries, invariants, and
> rollback gates for G38-G41. Root [`queue.yaml`](../queue.yaml) alone decides
> whether a slice is executable. Current behavior remains owned by
> [`tasks-and-agents.md`](../../architecture/runtime/tasks-and-agents.md) and
> [`architecture/tui/README.md`](../../architecture/tui/README.md).

## Decision

P47 adopts a staged `combine` design:

- `preserve` P31's exact WorkItem, execution-generation, replay, and
  render-purity contracts;
- `combine` an engine-resolved exact navigation target with TUI-owned view
  activation and correlation; and
- `adapt` the Ctrl+T presentation into a mixed, keyboard-accessible explorer
  without creating a second runtime owner.

Correctness comes before visual depth. P47.1-P47.3 independently repair three
identity and settlement faults. P47.4-P47.7 then make the existing runtime
capability discoverable through four smaller presentation seams. Each slice has
one observable rollback boundary and one focused negative oracle.

## User Problem

At intake, the Task Explorer could display useful WorkBoard and Agent facts,
but three seams made a visually plausible action unsafe or ineffective.
P47.1-P47.3 have closed all three correctness seams; the original boundaries
were:

1. send, continue, and cancel confirmation retain only an action kind; a
   refresh can move selection before submission, so the input can target a
   different row;
2. terminal WorkItem settlement checks every execution link on the board, so
   an unrelated live execution can prevent completion of an otherwise settled
   item; and
3. switch availability is declared from a non-empty `ThreadID`, while the TUI
   later resolves only that ID and may silently do nothing when the exact
   execution target is no longer current.

After those repairs, P47.4 made execution rows discoverable beside WorkItems
with exact selection, P47.5 added composable filters and explicit focus, P47.6
split cached detail into truthful WorkItem/execution `overview` and `activity`
tabs, and P47.7 added lazy exact transcript/output/lineage inspection. The
seven completed slices close G38-G41 without moving runtime truth into the TUI.

## Current Evidence

Current production paths establish these facts:

- `TaskExplorerPanel` freezes the exact request, Board/runtime revision,
  Agent/generation, message, action, and label before input or confirmation;
  result correlation cannot retarget or clear a newer intent.
- `LogicalWorkAdapter.guardTerminalLinksLocked` selects only immutable links for
  the WorkItems becoming terminal under the current Board mutation.
- `QueryEngine.ResolveTaskExplorerNavigationTarget`, switch declaration, and
  switch application share one exact execution-generation resolver. Ctrl+T
  retains the typed target and revalidates it before activation, paging, and
  async result application without using the generic ID-only selector.
- `TaskExplorerPanel` now projects engine-ordered WorkItems followed by exact
  execution generations, labels both kinds textually, and restores only exact
  WorkItem or execution identity across refresh, reorder, removal, and resize.
- The panel composes `all`, `active`, `attention`, and `terminal` filters with
  local search, exposes controls/list/detail focus textually, resolves keys and
  hints from one local descriptor, and routes render-derived mouse geometry
  before chat without submitting runtime actions.
- `Refresh` takes a defensive panel-local copy of the bounded snapshot.
  `overview` and `activity` project only cached row-kind facts, switch and
  scroll locally, preserve exact selection, and fail closed when boardless
  WorkItem attention or diagnostics cannot be attributed unambiguously.
- `AgentTranscriptPage` is generation-bound and may perform bounded durable
  reads. `AgentExecutionDetail` validates exact current Agent, generation,
  Session, and thread identity before and after optional bounded terminal
  output I/O; nonterminal generations cannot reuse a prior terminal file.
- Ctrl+T schedules deep detail only through Bubble Tea commands and accepts a
  result only for the same open panel, exact row, Session/thread, request
  generation, tab, and cursor. Cached render remains provider- and I/O-free.

The completed behavior is owned by the current runtime and TUI architecture;
the [P47.7 closeout](../history/tui/p47-7-lazy-execution-detail.md) preserves its
delivery evidence.

## Alternatives Considered

### A. Patch the visible symptoms in place

Keep selection as the implicit target, special-case WorkItem completion in the
TUI, and retry ID-only thread activation. This is rejected because it leaves
identity split across layers and turns stale-state behavior into timing-dependent
UI policy.

### B. Freeze exact values, repair the owner, then deepen the view

Freeze a complete action intent, scope settlement in the WorkBoard adapter,
resolve navigation in the engine, and only then add mixed rows and lazy detail.
This is selected because every repair lands at its current authority boundary
and can be proved with a deterministic negative test.

### C. Replace Ctrl+T, Ctrl+B, and `/team` with one new task application

This may eventually reduce presentation duplication, but it expands scope into
background-task and team-monitor semantics before the correctness seams are
closed. P47 rejects that rewrite. Existing surfaces may share deeper helpers
only when their observable contracts remain unchanged.

## Scope

### In scope

- Ctrl+T action-input and confirmation correlation;
- WorkItem terminal-settlement validation;
- exact current-generation navigation declaration and activation;
- mixed WorkItem/execution rows with stable exact selection;
- local search/filter, explicit focus, and textual controls in Ctrl+T;
- bounded snapshot-only overview/activity structure;
- lazy transcript, output, and lineage detail; and
- deterministic reducer, engine, replay, geometry, and PTY evidence required
  by the affected boundary.

### Non-goals

- arbitrary human mutation of WorkItems from the TUI;
- automatic WorkItem completion when an Agent terminates;
- a new durable owner, event schema, session format, ACP wire extension, or
  replay dispatch;
- navigation to an evicted or retained historical generation whose exact
  transcript is unavailable;
- changing Ctrl+B or `/team` product scope; and
- a redesign of Bubble Tea, `AgentRunner`, QueryEngine, or WorkBoard.

## Frozen Invariants

Every slice must preserve these properties:

1. A user action addresses the row displayed when the action started, never
   whichever row is selected when input is finally submitted.
2. Board revision is the WorkBoard concurrency token. Runtime revision is
   correlation-only and must not become a durable mutation precondition.
3. Execution controls require exact `(AgentID, Generation)` identity.
4. Thread activation requires an engine-declared exact current target; a
   non-empty `ThreadID` alone is insufficient.
5. Stale, evicted, replay-only, unresolved, or superseded targets fail closed
   with a visible result and dispatch no model, tool, Agent, or permission work.
6. A WorkItem terminal transition is blocked only by a live execution linked
   to that exact item on that exact board.
7. WorkItem and execution ownership remain separate. Execution termination
   never implicitly mutates WorkItem lifecycle.
8. Snapshot selection is bounded and side-effect-free. Rendering performs no
   engine, transcript, filesystem, provider, or Git I/O.
9. Async detail results apply only to the exact request, row identity,
   generation, tab, and cursor that requested them.
10. Status, selection, focus, disabled actions, hidden counts, and attention
    remain understandable without color or glyph shape alone.
11. Session restore and reducer replay reconstruct presentation facts without
    dispatching historical work.
12. Existing plain/headless, ACP, Ctrl+B, `/team`, `/tasks`, and sidebar
    contracts do not regress merely because Ctrl+T becomes deeper.

## Exact Values

### Pending action intent

Ctrl+T owns one immutable transient value while collecting input or awaiting
confirmation:

```go
type taskExplorerActionIntent struct {
	RequestID       string
	BoardID         string
	BoardRevision   uint64
	RuntimeRevision uint64
	AgentID         string
	Generation      int64
	MessageID       string
	Action          engine.TaskExplorerAction
	DisplayLabel    string
}
```

The identity fields mirror `TaskExplorerActionRequest` and are copied from the
selected row and engine declaration when the action starts. Payload text is
collected separately. Refresh may replace the snapshot and move the cursor,
but it must not rewrite the intent. Submission either sends that frozen
identity to the engine, or visibly rejects it if it is no longer
present/actionable. It must never retarget a newer selection.

`DisplayLabel` is presentation-only. The engine continues to authorize from
typed identity and current facts, not from labels or cached `AllowedActions`.

### Navigation target

Switch becomes an engine-resolved value rather than a boolean inferred from a
thread string:

```go
type TaskExplorerNavigationTarget struct {
	SessionID  string
	ThreadID   string
	AgentID    string
	Generation int64
	Mode       ThreadAttachmentMode
}
```

The engine owns one resolver with this conceptual contract:

```go
ResolveTaskExplorerNavigationTarget(
	agentID string,
	generation int64,
) (TaskExplorerNavigationTarget, error)
```

The resolver selects the exact `(AgentID, Generation)` execution row from the
current `TaskExplorerSnapshot`, then joins exactly one current
`RuntimeThreadCatalogEntry` by the row's `ThreadID`, `AgentID`, snapshot
`SessionID`, and declared attachment mode. Generation stays bound from the
execution row; it is never guessed from the catalog and does not require the
catalog to invent a generation field. Missing, duplicate, mismatched, or
unsupported facts return a typed unavailable/stale result.

Action declaration uses this resolver to decide whether switch is present.
`ApplyTaskExplorerAction` calls the same resolver again with the request's
exact key and returns the typed target, not just a thread string. Ctrl+T is the
only P47 switch consumer: it compares all five target fields, builds transcript
selection directly from that exact target, and activates the matching catalog
entry without calling an ID-only or “first/latest generation” fallback. A
failed comparison leaves the view unchanged and schedules no transcript
command.

Historical generations remain inspectable where retained facts allow it, but
they expose no switch action unless the exact generation has a valid current
navigation target.

## P47.1 Exact Pending Action Intent

**Status:** complete on 2026-08-07
**Closes:** G38

**Observable contract:** Starting send, continue, or cancel on row A freezes
row A's full action identity. If refresh removes A and selects B before
submission, no request may address B. The outcome is either an exact request
for A that the engine accepts/rejects from current truth, or a visible local
stale-target rejection.

**Implementation boundary:** `TaskExplorerPanel` in
`internal/tui/task_explorer_panel.go` and focused TUI tests. Do not change
engine authorization or WorkBoard persistence.

**Required evidence:**

- table tests for send, continue, and cancel confirmation across refresh;
- removal, reorder, revision-change, and same-label/different-identity cases;
- result correlation that cannot clear or mutate a newer pending intent; and
- reducer replay/render tests showing no I/O or dispatch.

**Rollback:** remove only the immutable intent value and its tests. No durable
data or cross-entrypoint behavior changes.

## P47.2 WorkItem-Scoped Settlement

**Status:** complete on 2026-08-07
**Closes:** G39

**Observable contract:** Terminal transition of WorkItem A succeeds when all
executions linked to A are terminal, even if WorkItem B has a live linked
execution. The same transition fails while any exact execution linked to A is
live. Unlinked executions and links belonging to another item or board cannot
affect A.

**Implementation boundary:** `LogicalWorkAdapter.guardTerminalLinksLocked` in
`engine/internal/workboard/adapter.go` and focused adapter/engine tests. The TUI
remains a caller, not the owner.

**Required evidence:**

- two-item oracle with A settled and B live;
- inverse oracle with A live and B settled;
- multiple generations, unlinked execution, missing execution fact, and
  board/item identity cases;
- race coverage selected by the WorkBoard mutation boundary; and
- unchanged expected-revision and durable-commit behavior.

**Rollback:** restore the prior guard. No schema or persisted-record migration
is introduced.

## P47.3 Exact Thread Navigation Target

**Status:** complete on 2026-08-07
**Closes:** G40

**Observable contract:** The engine advertises switch only with an exact
current navigation target. A stale or mismatched generation returns an
explicit typed failure and the TUI remains on its current thread. A successful
result activates the exact target once and starts at most one bounded
generation-bound transcript request.

**Implementation boundary:** the one engine resolver, action
declaration/application, typed result, Ctrl+T result handling, and exact target
activation helper. Existing generic thread navigation remains unchanged unless
it can consume the exact target without weakening other callers. Runtime
ownership stays in the engine; view-state ownership stays in the TUI.

**Required evidence:**

- same `ThreadID` with superseded or mismatched generation, proving that the
  catalog cannot rebind the request;
- missing catalog entry, replay-only/evicted transcript, and stale result;
- success correlation across request, thread, Agent, generation, and mode;
- no silent ID-only/first/latest-generation fallback, no transcript command on
  resolver failure, and no model/tool dispatch; and
- compatibility tests for Ctrl+B and `/team` if a shared resolver changes.

**Rollback:** remove the typed target and return to the prior declaration and
activation. No durable state changes.

## P47.4 Mixed Rows and Stable Selection

**Status:** complete on 2026-08-07
**Closes:** part of G41

**Observable contract:** Ctrl+T presents WorkItems and exact execution
generations in one ordered list with textual group identity. Refresh, resize,
and snapshot reorder preserve selection only by exact WorkItem or execution
identity; a missing identity moves deterministically to the nearest visible
row. No title, owner, thread, or list position acts as identity.

**Implementation boundary:** Ctrl+T row construction, identity, selection,
list rendering, and focused tests. Existing action keys continue to use the
engine declarations repaired by P47.1/P47.3. No filter or new detail state is
introduced in this slice.

**Required evidence:**

- mixed group ordering and textual WorkItem/execution distinction;
- same-label, same-thread, and multiple-generation identity cases;
- refresh removal, insertion, reorder, empty-list, and resize selection; and
- narrow/compact/standard/wide list structure without color-only meaning.

**Rollback:** return Ctrl+T to the logical-work-only projection. P47.1-P47.3
remain valid.

## P47.5 Filter and Focus Navigation

**Status:** complete on 2026-08-07
**Closes:** part of G41

**Observable contract:** Local filters are `all`, `active`, `attention`, and
`terminal`; search composes with the selected filter. Focus moves explicitly
among search/filter, list, and detail controls without changing runtime truth.
Filtering never alters the underlying snapshot or engine-declared actions.

The recommended bindings preserve current mnemonic controls:

| Key | Meaning |
|---|---|
| `Enter` | inspect selected row or open its current detail |
| `x` | switch when an exact navigation target exists |
| `s` | send input to an exact live execution |
| `p` | pause or resume according to the engine declaration |
| `c` | cancel/abort with confirmation |
| `n` | continue an exact supported execution |
| `r` | refresh cached snapshot |
| `f` | cycle local filter |
| `/` | focus local search |
| `Tab` / `Shift+Tab` | move focus region |

Historical documentation that used `x` for cancel does not override the
current product mnemonic. Hints are resolved from one binding owner rather
than duplicated literals.

**Implementation boundary:** Ctrl+T local filter/search/focus state, one key
binding resolver, mouse hit regions already owned by the panel, and focused
tests. The engine snapshot is unchanged and no action eligibility is inferred
locally.

**Required evidence:**

- filter/search composition, stable exact selection, empty-filter,
  hidden-count, and focus-cycle tests;
- keyboard and mouse parity for selection and scroll where mouse is supported;
- no-color textual state and disabled-action proof; and
- narrow/compact/standard/wide focus/hint structure plus a real PTY smoke test.

**Rollback:** remove filter/focus state and retain the mixed list.

## P47.6 Snapshot Detail Structure

**Status:** complete on 2026-08-07
**Closes:** part of G41

**Observable contract:** The detail region has explicit `overview` and
`activity` tabs backed only by the cached `TaskExplorerSnapshot`. Capability
and unavailable text differ truthfully for a WorkItem and an exact execution.
Opening, switching, scrolling, resizing, and rendering these tabs performs no
engine, transcript, filesystem, provider, or Git I/O.

**Implementation boundary:** Ctrl+T detail state/layout and cached projection
rendering. Add defensive-copy mutation tests for projection slices exposed to
the component. No Bubble Tea I/O command or engine reader is added.

**Required evidence:**

- WorkItem/execution tab capability and unavailable-state tests;
- render-purity and reducer replay no-dispatch tests;
- defensive-copy mutation tests for newly consumed slices; and
- bounded-size, resize, scroll, no-color, and structural golden evidence.

**Rollback:** remove the tab structure and retain mixed list/filter/focus.

## P47.7 Lazy Execution Detail

**Status:** complete on 2026-08-07
**Closes:** the remainder of G41

**Observable contract:** Exact execution detail adds bounded `transcript`,
`output`, and `lineage` tabs through existing engine readers. Tabs load lazily
through Bubble Tea commands and correlate their result to exact row identity,
generation, request, tab, and cursor. Unsupported historical detail is
explicitly unavailable rather than resolved against a newer generation.

**Implementation boundary:** existing engine readers, TUI async request/result
types, and focused tests. Do not create a second transcript or output store.
Consolidate duplicated exact-execution lookup only if one helper can preserve
each caller's error and availability contract.

**Required evidence:**

- capability/unavailable tests for live execution, terminal current
  generation, and retained historical generation;
- stale, duplicate, and out-of-order async result rejection;
- exact transcript/output/lineage request and result correlation;
- render-purity and replay no-dispatch tests; and
- focused race, structural goldens, and real PTY inspection before closeout.

**Rollback:** remove lazy execution tabs and retain snapshot-only detail. No
durable migration is required.

## Promotion Gate

The written contract was approved. P47.1-P47.3 closed G38-G40; P47.4-P47.6
delivered mixed rows, local filter/focus, and defensive cached detail; P47.7
completed lazy exact execution detail. Their source-backed closeouts close G41
and remove every P47 row from the active queue. P48.1 is now the sole `Ready`
slice.

The P47.5-P47.7 written-approval gates are satisfied. Those rows form a
presentation dependency chain because each consumes the immediately preceding
component seam. Every predecessor closes with source-backed tests and
repository gates before its dependent row can become `Ready`.

Approval does not waive implementation review, final Makefile gates, or real
terminal evidence. CI quota exhaustion may explain absent remote evidence, but
it does not turn a failing local gate into a pass.

## Closeout Ownership

Each implementation PR must:

1. close only its named gap or named portion of G41;
2. update current architecture and `STATUS.md` only for behavior actually
   delivered;
3. add one history record and remove its queue row;
4. explicitly promote the dependency successor or leave it queued;
5. run the risk-selected focused tests and required repository gates; and
6. distinguish local automated, race, PTY, remote CI, and physical-terminal
   evidence.

P47 is complete: P47.7 closed G41 and the queue no longer contains a P47 row.
The history records retain the seven independently reviewable rollback
boundaries.
