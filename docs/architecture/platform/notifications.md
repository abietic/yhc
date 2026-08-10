# Notifications

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** This file owns engine notification dispatch and its CLI/TUI
> adapters. The TUI App owns toast insertion, expiry, rendering, and replay
> exclusion after adapter delivery; that lifecycle is documented by the
> [`TUI notification owner`](../tui/README.md#notification-lifecycle). Agent
> completion commands are a separate event surface.

## Current Flow

`NotificationManager` fan-outs one notification to supported handlers. The CLI
bootstrap installs terminal bell and OS handlers. TUI mode also installs an
in-app adapter and suppresses external interruption handlers while terminal
focus policy disallows them.

```text
QueryEngine turn completion
  -> notify.NotifyCompletion
  -> NotificationManager.Send
     -> TerminalBellHandler (focus policy permits)
     -> OSNotifyHandler      (focus policy permits)
     -> tuiNotifyAdapter     (TUI only)
```

`Send` snapshots its handler list under a lock, skips unsupported handlers,
continues after handler errors, and returns the first error. `Disable` makes it
a no-op. `SetMinLevel` stores a field, but `Send` does not currently consult
that field; callers must not assume notification-type filtering is active.

## Handler Boundary

| Handler | Delivery | External policy |
|---|---|---|
| `TerminalBellHandler` | BEL on stdout when stdout is a character device | gated |
| `OSNotifyHandler` | `notify-send` on Linux or `osascript` on macOS | gated |
| `LogHandler` | append-only file logger | not gated |
| `tuiNotifyAdapter` | maps engine type/urgency to TUI toast severity | not gated |

The production engine currently emits a completion notification after a turn.
The error and permission convenience functions exist, but no production engine
call site invokes them.

A model-fallback warning is deliberately outside `NotificationManager`.
Entrypoints project one safe `EventModelAttempt` notice through TUI toast,
Plain/Headless stderr, or ACP `_session/status`; the detailed boundary belongs
to [`model-providers.md`](model-providers.md#bounded-overload-failover) and the
[`runtime-event contract`](../tui/contracts/runtime-events.md).

## Code References

| Symbol | Evidence |
|---|---|
| notification and handler contracts | [`engine/notify/notify.go`](../../../engine/notify/notify.go), [`engine/notify/notify.go`](../../../engine/notify/notify.go) |
| manager fan-out | [`engine/notify/notify.go`](../../../engine/notify/notify.go), [`engine/notify/notify.go`](../../../engine/notify/notify.go) |
| convenience emitters | [`engine/notify/notify.go`](../../../engine/notify/notify.go) |
| CLI handlers and `tuiNotifyAdapter` | [`cmd/yhc/cmd/root.go`](../../../cmd/yhc/cmd/root.go) |
| completion call site | [`engine/engine.go`](../../../engine/engine.go) |
| TUI insertion, expiry scheduling, and rendering | [`App.Update` and `reconcileNotificationExpiry`](../../../internal/tui/app.go), [`NotificationStack`](../../../internal/tui/notifications.go) |

## Example

```go
mgr := notify.NewNotificationManager()
mgr.AddHandler(&notify.OSNotifyHandler{})
mgr.SetExternalPolicy(func() bool { return appIsUnfocused })
notify.NotifyCompletion(mgr, sessionID, "Turn completed")
```
