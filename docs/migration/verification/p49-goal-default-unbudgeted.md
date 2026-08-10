# P49 Default-Enabled Budget-Optional Goal Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that supported composition roots
> default-enable Goal, explicit unbudgeted work runs durably, and provider
> admission remains exactly attributable across restart

## Contract

Missing production Goal configuration projects `Enabled=true` and no numeric
budget. Explicit create or resume is still required. Nil budget disables only
the Goal token limiter; provider usage and every other admission, permission,
cancellation, provider/account, persistence, recovery, and entrypoint boundary
remain enforced. Only an explicit positive cap activates cumulative Goal token
limiting, and zero is invalid.

State version 4 and continuation/runtime payload version 2 preserve optional
budget identity. Provider-admission version 2 persists and strictly settles the
logical request plus model attempt ID, index, profile, and retry index. Legacy
in-flight admission without that proof is retained and fails closed.

## Deterministic Evidence

Focused engine fixtures prove unbudgeted create/resume, cumulative usage,
adding a cap after usage, exact-once continuation, schema migration, mutated
attempt-field rejection, verbatim legacy admission preservation, and recovery
without replay dispatch.

Entrypoint fixtures prove:

- saved-root TUI and Plain expose `/goal`, create an active unbounded Goal, and
  submit one initial prompt when configuration omits Goal;
- dedicated Headless Goal can continue an unbudgeted Goal only within its
  positive `--max-continuations` process bound;
- ACP remains unavailable before private capability negotiation and requires
  explicit continue afterward; and
- unsupported roots, Plan, child/review, administration, ordinary headless,
  standalone MCP, generic wake, and generic claim paths remain excluded.

`TestP49DedicatedUnbudgetedGoalWakeClaimAndKillSwitch` proves a running
kill-switch reconciliation rejects the pending cursor. The quiesced-restart
fixture writes a real Session message, pauses and checkpoints an unbudgeted
Goal, restarts with `enabled:false`, and proves paused state, zero runtime
items, no Goal wake channel, and no valid Goal claim.

## Commands

```bash
go test ./engine/ -run 'TestP49|TestP294GoalUsageKeepsExactAttemptAttribution|TestP243|TestP244RecoveredGoalItemSignalsDedicatedOnly' -count=1
go test -race ./engine/ -run 'TestP49|TestP243|TestP244RecoveredGoalItemSignalsDedicatedOnly' -count=1
go test ./engine/config/ -run '^(TestP244GoalConfig|TestP49)' -count=1
go test ./cmd/eino-agent/cmd/ ./internal/tui/ ./server/acp/ -run 'TestP49|TestP245' -count=1
go test ./cmd/eino-agent/cmd/ -run '^TestP245aPlainGoalWorkflowPTY$' -count=1
go test ./internal/tui/ -run '^TestP244GoalWorkflowPTY$' -count=1
make test-contract
make test-race
make test-pty
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_queue check
git diff --check
```

All listed commands pass on the final closeout tree. Remote CI was not used as
acceptance evidence under the explicit usage-availability waiver; it is not
described as green.

## Evidence Limits

Provider behavior is exercised through deterministic in-process seams. PTY
tests prove subprocess and terminal-protocol behavior, not a physical font or
pixel grid. The evidence does not establish live-provider tokenization or cost,
representative Goal adoption, remote endpoint behavior, or remote-CI
availability.
