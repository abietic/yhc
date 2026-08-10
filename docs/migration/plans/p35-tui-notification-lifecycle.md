# P35 TUI Notification Lifecycle

**Status:** historical
**Accepted:** 2026-07-31
**Completed:** 2026-07-31
**Adoption:** `combine`

> **Ownership:** completed atomic contract for Bubble Tea-owned notification
> delivery, redraw, and TTL expiry. Current behavior belongs to the
> [TUI architecture](../../architecture/tui/README.md#notification-lifecycle).

## Problem

Before P35.1, the production engine notification adapter called
`App.ShowNotification` directly. That callback could run outside Bubble Tea
`Update`, mutated
`NotificationStack` without scheduling a redraw, and left TTL pruning to a
later render or query. A notification could appear late, remain physically
visible past five seconds while idle, or race with presentation reads.

The current auto-dismiss test sleeps and then calls `Count`, so it proves only
pull-based pruning. Source comparison and the accepted ownership decision are
in
[`notification-lifecycle-audit.md`](../reference/tui/notification-lifecycle-audit.md).

## P35.1 Atomic Slice

**Status:** `Complete`

P35.1 offers external notifications to one bounded, non-blocking transport
mailbox. Its sole pump sends typed Tea messages without holding a mailbox lock;
`App.Update` remains the only presentation mutation owner. Every internal or
external push is followed by one App-owned reconciliation that schedules the
earliest active deadline. A generation-fenced Tea tick wakes the idle program,
prunes notifications due under an injected clock, redraws, and schedules the
next deadline.

The stack becomes clock-free and render-pure. Existing severity, placement,
TTL, eviction, and suffix behavior remain unchanged.

## Scope

P35.1 may change:

- `cmd/eino-agent/cmd/root.go` for a bounded latest-three transport mailbox,
  one pump, explicit close/join, and adapter registration after `tea.Program`
  exists;
- `internal/tui/app.go` for the typed delivery/expiry messages, App-owned clock
  and generation state, update reconciliation, and removal of legacy
  render-time toast mutation;
- `internal/tui/notifications.go` for clock-free push/prune/deadline operations
  and pure read/render methods;
- focused command/TUI tests, source-owner gates, race tests, and one
  program-level idle expiry proof;
- current TUI architecture, `STATUS.md`, `REMAINING.md`, root `PLAN.md`, and
  one verification/history record at closeout.

The behavior applies only to the interactive TUI and every production producer
that reaches its notification adapter. Plain, ordinary headless, ACP,
standalone MCP, and external desktop-notification delivery retain their
current owners and behavior.

## Non-Goals

P35.1 does not:

- move notifications from the status line to an overlay or change layout;
- add priority, queueing, folding, deduplication, user dismissal, persistence,
  replay, or configurable duration;
- change the default five-second TTL, newest-three eviction, severity mapping,
  or `(+N)` suffix;
- suppress in-process notifications while focused or alter external desktop
  notification focus policy;
- introduce a permanent ticker, polling interval, unbounded goroutine/queue,
  background presentation-state owner, mutex-protected widget mutation, or
  cancellable timer service;
- change Bubble Tea, terminal modes, display-cell geometry, or reduced-motion
  animation behavior.

## Frozen Invariants

### Ownership and ordering

1. `tuiNotifyAdapter.Notify` may construct and offer one immutable message to
   the transport mailbox; it cannot call `Program.Send`, wait for `App.Update`,
   or read or mutate App, stack, layout, or renderer state.
2. The mailbox retains the latest three pending notifications in FIFO order.
   Overflow drops the oldest pending item; one item already removed by the
   pump may remain in flight. This transport policy owns no visible stack,
   creation time, TTL, or delivery acknowledgement.
3. Adapter and pump creation occurs after `tea.NewProgram` and immediately
   before the supported run boundary. Pre-run offers return promptly. The
   production run boundary terminates the program before closing the adapter:
   termination releases an in-flight `Program.Send`; close then drops pending
   items, joins the sole pump, and makes future offers no-ops.
4. The pump removes a message before calling `Program.Send` and holds no
   mailbox lock across that blocking call. Engine notification producers never
   wait for Bubble Tea startup, update, rendering, or shutdown.
5. `App.Update` is the sole notification mutation owner. Existing internal
   helpers may push only while handling an update.
6. Delivery mutation precedes the redraw returned by that update. Expiry
   mutation precedes the redraw returned by the matching tick.
7. `View`, `activeToast`, stack reads, counts, and render methods are pure and
   do not read wall time, prune, clear, or rewrite legacy fields.

### Time and expiry

1. Notification creation time is assigned when `App.Update` accepts the typed
   message, not when the callback offers it. Production uses the current
   five-second visible TTL; tests can inject both `now` and the Tea
   deadline-command factory without sleeping.
2. Each notification retains its own creation time and TTL. Adding a newer
   notification does not extend an older notification's deadline.
3. A notification is absent once the App-owned clock reaches its deadline.
   One expiry update prunes all items due at that instant.
4. The App schedules the earliest active deadline. After a valid expiry wake,
   it schedules the next earliest deadline or leaves no timer active.
5. A scheduled earlier wake may remain authoritative after its item is
   evicted; it may wake, prune nothing, and reconcile the real earliest
   deadline. The implementation must not eagerly replace a timer merely
   because the earliest deadline moved later.

### Generation and concurrency

1. At most one generation is authoritative. Tea ticks are not cancellable, so
   every superseding earlier deadline or empty-stack invalidation advances the
   generation.
2. A stale generation cannot prune, clear, reschedule, or otherwise mutate a
   newer stack.
3. Concurrent engine producers may call the adapter, but a bounded offer never
   blocks them. Retained messages are handed to Bubble Tea serially, and
   `App.Update` serializes their presentation mutations.
4. A burst beyond three notifications preserves newest-three eviction without
   creating a replacement timer for every default-TTL eviction.
5. No notification code holds a lock across `Program.Send`, Tea command
   execution, rendering, another callback, or adapter close/join.

### Compatibility and terminal behavior

1. Info, Success, Warning, and Error retain their current icons, styles, and
   status-line projection.
2. The most recent notification remains the single-line value, with `(+N)` for
   additional active items.
3. Reduced motion does not disable delivery or expiry because TTL settlement is
   state behavior, not decorative animation.
4. Focused and unknown focus states still allow in-process notification
   rendering; only the separate external notification policy is suppressed.
5. Program quit, cancellation, output failure, and normal termination stop and
   join the sole mailbox pump. They create no per-notification goroutine,
   timer owner, blocked producer, or unbounded shutdown wait outside Bubble
   Tea.

## Deterministic Proof

Focused tests cover:

- an adapter notification offered with no direct App mutation, handed to a
  typed Tea message, then applied by `App.Update`;
- pre-run notification offers returning promptly, then reaching App after the
  program starts;
- a blocked `Program.Send` or App update while concurrent producers continue
  returning promptly, including a reentrant engine notification from Update;
- deterministic latest-three pending overflow, retained FIFO order, and at
  most one additional in-flight message;
- immediate redraw state plus one injected deadline command, followed by idle
  expiry without `time.Sleep`, polling, or a second input event;
- several different deadlines, proving earliest-first pruning and exact next
  scheduling without extending older items;
- newest-three overflow while a prior earliest wake is pending;
- a superseded stale tick after a newer notification, proving it cannot delete
  or reschedule current state;
- multiple items due at the same instant and empty-stack invalidation;
- repeated `View`, `Active`, `Count`, and render calls leaving stack and App
  state deeply equal;
- concurrent adapter producers while presentation reads run, under repeated
  `-race`;
- reduced-motion and focused/unknown-focus paths retaining in-process expiry;
- adapter close and program termination releasing an in-flight send, dropping
  pending messages, joining the pump, and making late offers no-ops without
  goroutine leakage;
- a source-owner gate rejecting production direct stack mutation and
  render-time pruning.

The program-level proof must exercise pre-run offer, active-loop delivery,
backpressure, expiry redraw, and post-termination offer on a real Bubble Tea
message loop. It need not claim terminal/font geometry because placement and
cell composition do not change.

Final verification:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
git diff --check
```

Focused iteration also requires repeated race runs for `internal/tui` and
`cmd/eino-agent/cmd`. Timing assertions must use injected time; wall-clock
sleeps are not acceptance evidence.

## Completion and Rollback

P35.1 completed after current-source reproduction, reference comparison,
deterministic focused and race proof, and independent lifecycle/concurrency
review selected and verified `combine`. Delivery evidence is in
[`p35-1-tui-notification-lifecycle.md`](../history/tui/p35-1-tui-notification-lifecycle.md);
reproducible commands are in
[`p35-1-tui-notification-lifecycle.md`](../verification/p35-1-tui-notification-lifecycle.md).

Rollback removes the bounded mailbox/pump, typed adapter message, App deadline
generation, and clock-free stack operations as one unit. It restores no
durable data and requires no migration. The rollback reopens G8 because direct
callback mutation and render-triggered expiry would again be the production
behavior.
