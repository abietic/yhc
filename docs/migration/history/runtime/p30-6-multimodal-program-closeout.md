# P30.6 Multimodal Program Closeout

**Status:** historical
**Closed gaps:** G32
**Completed:** 2026-07-30

> **Ownership:** completion evidence for the final P30 writer, reader,
> lifecycle, privacy, and bounded-cost closeout. Current behavior belongs in
> the linked architecture documents. Provider-rich assistant replay remains
> G20 and is not claimed here.

## Outcome

P30.6 completed a `preserve` slice inside P30's accepted `combine` program and
closed G32. It changed no multimodal wire capability, prompt-record version,
MIME allowlist, provider route, command grammar, replay order, queue limit,
export format, or recovery budget.

The closeout sealed the writer boundary:

- TUI rich input originates from one immutable composer snapshot and reaches
  only `SubmitPromptInput` or `EnqueuePromptInput`; the unreachable alternate
  image/metadata request helpers are deleted.
- ACP continues to build `UntrustedPromptInput` after bounded structural
  validation. Its load path remains a reader of Session's exact neutral replay
  projection.
- `SubmitMessageWithImages` remains source-compatible and delegates
  non-command input to typed admission.
- image-bearing `EnqueueUserInput` preserves text-then-image order, clears
  caller name/path provenance, delegates to `EnqueuePromptInput`, and returns
  the legacy sanitized projection.
- generic `RuntimeInputCoordinator` enqueue rejects every newly supplied
  inline image payload before ledger mutation. Legacy JSON remains readable;
  it is decode-only and is never rewritten as a new rich item.
- only `buildDurableRuntimePromptFromAdmitted` may create the ref-backed
  durable queue representation. The alternate direct `[]UserImage` writer is
  deleted.

`validateUserImages`, `ValidateUntrustedPromptInputMetadata`,
`SubmitMessageWithImages`, terminal image-protocol capability detection, and
legacy inline readers remain because current source and compatibility tests
still require them.

## Owner And Lifecycle Proof

The current owner inventory is:

- [`composer.md`](../../../architecture/tui/contracts/composer.md) for the one
  TUI draft snapshot and idle/busy submission boundary;
- [`busy-queue.md`](../../../architecture/tui/contracts/busy-queue.md) for
  ref-backed queue publication, claim, edit, and decode-only compatibility;
- [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) for typed
  ACP ingress and ordered rich load projection;
- [`model-providers.md`](../../../architecture/platform/model-providers.md)
  for selected-route capability admission and provider lowering;
- [`transcripts.md`](../../../architecture/state/transcripts.md) for strict
  prompt-record/ref durability;
- [`sessions.md`](../../../architecture/state/sessions.md) for replay,
  branch/export/delete, and private migration ownership; and
- [`recovery.md`](../../../architecture/runtime/recovery.md) for exact rich
  compaction, restart, and attempt-local media recovery.

Named closeout fixtures prove that the legacy public rich queue wrapper reaches
the same selected-route admission and ref-only ledger as typed entrypoints;
unknown and unsupported routes fail before publication; seeded legacy inline
JSON still loads; new inline coordinator input fails before publication; and
rich prompt refs survive compaction plus restart without ref rewriting or an
unbounded retained derivative. An AST/source gate prevents the deleted writers
from returning unnoticed.

The implementation diff received a second independent security/lifecycle
review. It returned `ADMISSION: ACCEPT` with no high-risk or correctness
finding and confirmed the frozen public/internal writer split, fail-before-
mutation behavior, legacy decode compatibility, ref-only durability, and
retained validators/capabilities.

## Bounded-Cost Evidence

The maximum supported fixture contains 32 ordered prompt parts with distinct
one-pixel safe-raster refs. On the closeout machine:

```text
BenchmarkP306DurablePromptRecordBytesMaxParts-15
4863 ns/op  3698 record-bytes  4147 B/op  2 allocs/op

BenchmarkP306DurablePromptMaterializationMaxParts-15
2589513 ns/op  1203557 B/op  3208 allocs/op
```

The first result demonstrates that serialized durable queue bytes are bounded
by part metadata and fixed-size refs rather than image payload bytes. The
second measures scoped read materialization only; it is not a production
latency or resident-memory service-level objective.

## Verification

Focused P30.6 owner, typed-admission, legacy decode, compaction/restart,
privacy, ACP migration, and cross-entrypoint matrices passed. The benchmark
command was:

```bash
go test -run '^$' -bench 'BenchmarkP306' -benchmem ./engine/... ./engine/session/...
```

The frozen engine/TUI/ACP race matrix passed without a data-race report
(`engine` 1136.268 s, `internal/tui` 444.947 s, and `server/acp` 591.235 s).
Windows test compilation for all three owners and the official ACP SDK v1.3.0
harness also passed. Repository Makefile gates, documentation checks,
migration-manifest checks, and `git diff --check` passed before merge.

Race instrumentation exposed a pre-existing timing flaw in
`TestSpinnerWaitingUsesAuroraSkyThenStalledToken`: its expected early-stall
style had only 500 ms of headroom while the instrumented render could consume
that entire interval. The test now leaves almost the complete one-second
early-stall window available without changing production timing or rendering;
50 instrumented repetitions pass.

## Compatibility And Rollback

Existing version-1 and version-2 prompt records, including legacy inline queue
JSON, remain readable without format migration or conversion to a new
ref-backed rich item. Load may still persist the existing removal of legacy
caller name/path provenance. Text-only queue behavior and every delivered
P30.1a-P30.5b reader remain unchanged.

Rollback restores the deleted transitional helpers and direct legacy rich
queue writer while retaining the strict admission, MediaStore, prompt-record,
Session lifecycle, TUI composer, and ACP readers. No data migration is
required. G20 remains open for provider-rich assistant durability, portable
replay, and real-client rendering proof.
