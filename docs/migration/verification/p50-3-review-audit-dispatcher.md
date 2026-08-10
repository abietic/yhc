# P50.3 Non-Blocking Reviewer-Audit Dispatcher Verification

**Status:** verification
**Last verified:** 2026-08-08

> **Ownership:** reproducible evidence that reviewer-audit storage is outside
> permission authority and bounded QueryEngine shutdown

## Contract

Each QueryEngine configured with a reviewer-audit sink owns one capacity-128
single-writer dispatcher. Permission and reviewer producers perform only a
non-blocking enqueue. A full or closed queue records a typed in-memory counter;
it never waits for storage and never changes classification, prompting,
settlement, reviewer lifecycle, grants, or dispatch.

The writer contains one sink error or panic per attempted record and continues
with later accepted records while its context remains live. It coalesces
unreported counter deltas into strict redacted `dispatcher_diagnostic` records
when the sink can accept them. The in-memory snapshot remains authoritative for
evidence that the same unavailable sink cannot durably retain.

Engine close first cancels and joins reviewer producers, then closes audit
admission and gives the dispatcher 250ms to drain. A sink that ignores context
may retain its single blocked writer goroutine after close; Go cannot forcibly
stop it, but it cannot hold permission handling or QueryEngine shutdown.

## Deterministic Evidence

Focused fixtures prove:

- the writer can block on the first record while a full-queue producer returns
  immediately and the dropped record never reaches the sink;
- sink error and panic each increment `sink_failure` without preventing the
  next accepted record;
- a failed diagnostic write keeps its delta for a later successful retry and
  does not recursively emit another diagnostic in the same pass;
- concurrent accepted records reach the single writer exactly once, while
  concurrent close classifies every enqueue as delivered, full-queue drop, or
  enqueue-after-close;
- cooperative cancellation stops a context-aware sink, while a
  context-ignoring sink cannot hold bounded close;
- a blocked sink leaves legacy allow and deny outcomes, structured prompt
  settlement, context cancellation, and checking-before-terminal reviewer
  event order unchanged; and
- QueryEngine close returns within the audit flush bound and exposes the local
  `shutdown_flush_expiry` counter.

Strict schema fixtures reject unknown diagnostic codes, zero deltas, mixed
action/reviewer/comparison/corpus/recovery payloads, and unknown JSON fields.
Report fixtures saturate duplicate `uint64` deltas, keep diagnostics outside
all action groups and denominators, and mark any retained diagnostic as
`partial`. CLI fixtures expose only typed code/count totals and do not infer
missing lifecycle records.

## Commands

```bash
go test ./engine/permission/ -run 'ReviewAudit.*(Dispatcher|Diagnostic)' -count=1
go test ./engine/ -run '^TestReviewAuditDispatcher' -count=20
go test -race ./engine/ -run '^TestReviewAuditDispatcher' -count=20
go test ./engine/permission/ ./engine/ ./cmd/eino-agent/cmd/ -run 'ReviewAudit|PermissionReviewAudit|P503' -count=1
go test -race ./engine/ -run 'PermissionReviewAudit|ReviewAuditDispatcher|P503' -count=20
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All listed local commands pass on the closeout tree. `make test` completed
7,257 tests with three explicitly opt-in skips; the third-party ACP SDK suite
also passed. Remote CI remains a separate merge gate.

## Evidence Limits

This verifies local audit isolation, bounded waiting, and retained-window
diagnostic truth. It does not make the reviewer authoritative, prove reviewer
quality, reconstruct dropped records, upload telemetry, guarantee durable
diagnostics through a permanently failed sink, or establish an OS sandbox.
P44/G14 reviewer promotion remains deferred, and host containment remains a
separate P42/P51 program.
