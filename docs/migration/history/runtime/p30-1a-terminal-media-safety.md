# P30.1a Terminal Media-Size Safety

**Status:** historical
**Completed:** 2026-07-29
**Last verified:** 2026-07-29

> **Ownership:** completed P30.1a `project-native` decision, fail-closed media
> recovery outcome, compatibility consequence, verification, and rollback.
> Current behavior belongs in
> [`recovery.md`](../../../architecture/runtime/recovery.md); later multimodal
> work belongs in
> [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md).

## Outcome

The first `media_size` provider failure now ends the canonical query round with
`TerminalImageError`. `runCanonicalAfterModelRound` publishes the existing
withheld provider error and runs stop-failure settlement once, but it does not
invoke reactive compaction, replace query messages, emit
`EventCompactBoundary`, or call the model again.

Every supported SDK, TUI, Plain, headless, ACP, and child execution path
inherits the same rule through QueryEngine. No adapter gained recovery policy,
and no persisted schema, public event type, error payload, capability, or media
API changed.

## Decision And Compatibility

P30.1a used `project-native` within P30's accepted `combine` program. The
previous reference-derived strip-and-retry path could not distinguish
historical media from the user's current turn. Failing closed is safer than
answering a text-only substitute that is no longer the submitted question.

This intentionally removes successful strip-and-answer compatibility.
Historical-only media failures also terminate until P30.2 supplies durable
turn identity and P30.3 proves bounded historical-only omission. The
P30.0-characterized transform remains test evidence but has no production
`media_size` caller.

## Verification

Closeout passed the frozen focused, race, repository, documentation, manifest,
and diff gates:

```text
go test ./engine/recovery -run 'TestTryMediaRecovery'
go test ./engine -run 'TestQueryMediaSize|TestP301a'
go test -race -timeout=20m ./engine/...
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
git diff --check
```

The focused fixtures prove one model call, one withheld error, one
stop-failure settlement, no compact boundary or successful replacement
answer, source-message immutability, and the temporary historical-only
fail-closed rule. Existing prompt-too-long recovery remains covered by the
engine race suite and repository gates.

## Rollback And Next State

Rollback must retain an equivalent fail-closed `media_size` terminal at the
canonical round owner. It may not restore strip-current-and-complete behavior.
Historical recovery stays disabled until trusted turn identity and bounded
omission land together.

P30.1b-P30.6 remain accepted but queued. No successor became `Ready`
automatically; root `PLAN.md` must select the next slice separately.
