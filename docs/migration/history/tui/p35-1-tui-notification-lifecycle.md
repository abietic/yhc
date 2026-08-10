# P35.1 TUI Notification Lifecycle

**Status:** historical
**Closed gaps:** G8
**Completed:** 2026-07-31

> **Ownership:** completion evidence for bounded engine-to-TUI notification
> delivery, Bubble Tea-owned redraw and TTL expiry, G8 closure, and unchanged
> non-TUI notification behavior.

## Outcome

P35.1 completed the accepted `combine` contract and closed G8. Interactive
notifications retain status-line placement, Info/Success/Warning/Error
styling, the five-second visible TTL, newest-three visible eviction, and the
`(+N)` suffix. Delivery and expiry now enter the Bubble Tea update loop instead
of mutating presentation state from an engine callback or a render read.

Plain, headless, ACP, standalone MCP, log, BEL, and desktop notification
handlers retain their existing owners. Focus continues to gate only external
notifications, and reduced motion does not suppress in-process delivery or
expiry.

## Transport and Teardown

The TUI composition root constructs one adapter after `tea.NewProgram` and
immediately before `Program.Run`. A synchronous engine handler copies message
and severity into a bounded latest-three pending mailbox. Overflow drops the
oldest pending value while one removed value may remain in flight. One pump
calls `Program.Send` outside the mailbox lock, so startup, a blocked Update,
rendering, and shutdown do not backpressure engine producers.

After `Program.Run` terminates, `runTUIProgram` closes the adapter before
closing `TerminalOutput`. Close rejects later offers, drops pending values, and
joins the sole pump; no notification creates its own goroutine.

## Presentation and Time

`NotificationDeliveryMsg` carries no creation timestamp. `App.Update` assigns
the injected App time when it accepts the value and remains the sole stack
mutation owner. `NotificationStack` exposes explicit `PushAt`, `PruneAt`, and
earliest-deadline operations; `Active`, `Count`, `View`, and render methods are
clock-free and mutation-free.

The App keeps one authoritative generation and earliest deadline. A valid Tea
deadline message prunes all items due at that instant and schedules the next
earliest value. An earlier replacement advances the generation, stale ticks
are inert, and eviction that moves the real deadline later leaves the earlier
wake in place until it reconciles.

## Proof and Review

Focused deterministic tests cover typed delivery, no direct App mutation,
pre-run offer, blocked `Program.Send` and reentrant Update backpressure,
latest-three pending overflow, retained FIFO order, App-assigned time, exact
idle expiry, different and equal deadlines, stale generations,
evicted-earliest reconciliation, empty invalidation, pure repeated reads,
reduced motion, real Bubble Tea termination, late offers, and production owner
source gates. Twenty repeated race runs cover both `internal/tui` and the
composition-root adapter.

An independent lifecycle/concurrency review returned `ADMISSION: ACCEPT` with
no findings after inspecting the implementation, tests, teardown order, timer
generation, and bounded-loss contract. Reproducible commands are in
[`p35-1-tui-notification-lifecycle.md`](../../verification/p35-1-tui-notification-lifecycle.md).

## Compatibility and Rollback

No durable schema, replay format, terminal geometry, notification priority, or
configuration changed. A squash revert removes the mailbox/pump, typed
delivery, explicit stack time, and App deadline generation as one unit. It
would restore the direct callback/render-pruning owner mismatch and must reopen
G8.
