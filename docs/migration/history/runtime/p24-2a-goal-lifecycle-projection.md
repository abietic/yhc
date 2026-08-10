# P24.2a Goal Lifecycle Projection

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** `adapt`

> **Ownership:** completion evidence for P24.2a. Current Goal execution and
> read-projection behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`sessions.md`](../../../architecture/state/sessions.md), and
> [`runtime-events.md`](../../../architecture/tui/contracts/runtime-events.md).
> Executable order belongs in [`migration/PLAN.md`](../../PLAN.md).

## User Problem

P24.1 made Goal intent durable but deliberately left it opaque and inert. The
engine could not identify one logical Goal turn across a ProjectGraph
permission resume, attribute descendant work to an exact Agent generation,
measure root active time, or retain completion and blocker evidence for later
guards.

P24.2a closes that observation boundary. It does not add automatic
continuation, provider usage accounting, model tools, slash commands, runtime
items, transport capabilities, or UI consumption. Completion intent therefore
remains pending until a later accounting slice can prove its gate.

## Delivered Contract

One QueryEngine-owned Goal lifecycle now provides:

- an ordered, lossless `EventGoalLifecycle` stream and defensive
  `GoalSnapshot` read projection;
- immutable Goal ID, objective revision, logical Goal turn ID, root Session
  ID, Agent ID, and Agent generation attribution;
- the same logical Goal turn across internal ProjectGraph permission query
  turns;
- inert persisted descendant binding that carries exact identity but never
  grants Goal mutation authority;
- root active time that excludes permission waits, foreground descendant
  waits, paused, blocked, limited, and terminal intervals;
- completion intent bound to the exact Goal ID, objective revision, and
  logical turn while accounting remains unresolved; and
- blocker transition only after three distinct logical Goal turns report the
  same validated blocker key, with duplicates ignored and edit, resume,
  steering, or key change resetting the streak.

Terminal commit persists the exact turn-evidence sequence before reducing it
into runtime state. `turn_finished` and the terminal lifecycle event are
contiguous, with the terminal event published last. `waiting_input` is a
recoverable query interruption and does not close the logical Goal turn.

## Restore And Failure Semantics

Cold restore installs the durable Goal snapshot directly into the runtime read
store without manufacturing a lifecycle event. A supported exact child
binding is restored as attribution-only metadata; malformed, unsupported,
root-owned, stale-generation, or conflicting bindings never grant descendant
authority. A branch clears both Goal state and descendant binding.

Terminal checkpoint failure publishes an explicit persistence error, restores
the interrupted active-time interval, releases the process-local Goal turn,
and does not advance a durable terminal cursor. Runtime replay rejects stale
revisions, incomplete identities, generation mismatches, invalid phases,
non-monotonic active time, and terminal sequences that do not match their
Goal snapshot.

## Evidence

The implementation and focused fixtures are owned by:

- [Goal execution identity, lifecycle producer, and terminal commit](../../../../engine/goal_runtime.go);
- [persisted descendant binding](../../../../engine/goal_binding.go);
- [runtime envelope and event family](../../../../engine/events.go);
- [runtime validation, reduction, and read projection](../../../../engine/runtime_state.go);
- [Session checkpoint, restore, resume, and fork integration](../../../../engine/session_checkpoint.go);
- [root/descendant generation admission](../../../../engine/subagent.go); and
- [P24.2a lifecycle and recovery fixtures](../../../../engine/goal_runtime_test.go).

Focused tests prove lifecycle ordering and excluded time, logical identity
across both direct and production ProjectGraph permission resume, visible
terminal checkpoint failure, deterministic replay and stale rejection, three
distinct blocker turns and reset paths, exact pending completion intent,
descendant generation attribution without mutation authority, and supported
versus rejected persisted bindings. The restore matrix also completes
generation one, resumes generation two, rejects a mismatched generation three,
and restores exact generation-two attribution in a fresh root engine.

Independent lifecycle review first found that resumed generations above one
were excluded from child discovery. After that fix, it found that a legacy
generation-zero orphan could still project an untrusted syntactically valid
binding. Positive generations now require exact durable Runner/Session
agreement, while generation zero remains attribution-free. The corrected
matrix passed re-review with no remaining finding. Final diff inspection then
found and closed one process-local binding leak on failed launch admission:
`launch_failed` and rejected initial lifecycle projection release the exact
generation binding, while a successful launch retains it through executor
entry. Focused race evidence and the final independent re-review found no
remaining issue. Final repository gates are recorded in the root PLAN closeout
entry.

## Compatibility And Rollback

The event envelope, runtime projection, and descendant Session binding are
additive. Existing consumers that do not understand Goal events continue to
run, while the engine marks the new family lossless so future consumers cannot
silently discard it. No current TUI, ACP, plain, headless, tool, command,
provider, or continuation owner consumes the projection.

One squash revert removes the additive projection and binding while retaining
the P24.1 Goal record and transition service. It does not migrate transcripts
or change current permission, Plan, provider, transport, command, or UI
behavior.

P24.2b and every later P24 slice remain queued. Root `PLAN.md` must complete a
new intake before provider accounting, automatic continuation, user controls,
or entrypoint projection becomes executable.
