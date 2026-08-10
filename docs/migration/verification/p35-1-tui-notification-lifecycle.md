# P35.1 TUI Notification Lifecycle Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P35.1 only

> **Ownership:** reproducible acceptance evidence for bounded notification
> transport, Bubble Tea update ownership, deterministic idle TTL settlement,
> teardown, pure rendering, and unchanged entrypoint scope.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Producer isolation | Concurrent `NotificationManager` handlers perform one short bounded offer and return while the sole pump or App update is blocked. |
| Bounded loss | One in-flight value plus the latest three pending values are retained; overflow drops only the oldest pending value and preserves retained FIFO order. |
| Presentation owner | A typed Tea value reaches `App.Update` before stack mutation or visible-time assignment. No composition-root callback keeps an App pointer. |
| Idle expiry | An injected App clock and deadline-command factory prove immediate redraw state, exact five-second absence, earliest-first and same-deadline pruning, and next-deadline scheduling without sleep or input polling. |
| Generation | Superseded ticks, evicted-earliest wakes, and empty-stack invalidation cannot prune or reschedule newer state. |
| Pure reads | Repeated `View`, `activeToast`, `Active`, `Count`, and render calls preserve notification items and timer identity; `Active` returns a defensive copy. |
| Terminal lifecycle | A real Bubble Tea program proves pre-run offer, active-loop delivery, blocked Update/Send producer progress, idle expiry, program termination, adapter close/join, and post-termination no-op. |
| Compatibility | Severity, status-line projection, five-second TTL, newest-three visible stack, reduced motion, and focused/unknown in-process behavior remain active; non-TUI handlers are unchanged. |
| Review | Independent concurrency/lifecycle review returned `ADMISSION: ACCEPT` with no findings. |

## Focused Commands

```text
go test ./internal/tui/ -run 'TestP351|TestNotificationStack' -count=1
go test ./cmd/eino-agent/cmd/ -run 'TestP351' -count=1
go test ./internal/tui/... ./cmd/eino-agent/cmd/... -count=1
go test -race ./internal/tui/ ./cmd/eino-agent/cmd/ -run 'TestP351' -count=20
```

Timing assertions inject time and deadline commands. `time.After` appears only
as a bounded test-harness failure guard, not as acceptance evidence.

## Source Gates

```text
go test ./internal/tui/ -run '^TestP351ProductionNotificationOwnerBoundary$' -count=1
go test ./cmd/eino-agent/cmd/ -run '^TestP351ProductionAdapterLifecycleOrdering$' -count=1
test -z "$(rg -n 'ShowNotification|toastMsg|toastTime|toastDuration' internal/tui/app.go cmd/eino-agent/cmd/root.go || true)"
```

The TUI gate rejects wall-clock or render-time stack pruning and requires one
App mutation site. The composition-root gate requires Program construction
before adapter registration/start and adapter close/join after `Program.Run`
but before terminal-output close.

## Repository Closeout

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

A GitHub Actions billing or usage failure may be waived only after the exact
job annotation proves that no runner started. A real check failure still
reopens P35.1.
