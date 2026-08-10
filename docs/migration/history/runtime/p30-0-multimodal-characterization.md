# P30.0 Multimodal Characterization And Owner Seam

**Status:** historical
**Completed:** 2026-07-29
**Last verified:** 2026-07-29

> **Ownership:** completed P30.0 `combine` decision, characterization outcome,
> owner inventory, compatibility boundary, verification, and rollback. Current
> behavior remains in the architecture documents; later P30 execution belongs
> in [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md)
> and root [`PLAN.md`](../../PLAN.md).

## Outcome

P30.0 proved the accepted G32 premises without changing production behavior,
public APIs, persisted schemas, provider requests, capability advertisement,
commands, or UI.

Focused production-path fixtures now pin:

- TUI interleaved placeholder order versus its flattened prompt/separate-image
  handoff and the resulting complete-text-before-images engine order;
- ACP's exact ordered, non-fetching Text/ResourceLink fallback and
  pre-mutation rejection of image, audio, and embedded blocks;
- one current-TUI-legal two-image turn whose encoded transcript record exceeds
  the canonical full reader's 8 MiB scanner budget;
- known supported, known unsupported, and missing capability facts, including
  the current missing-fact fail-open branches;
- identical media stripping for historical and current-turn messages,
  current-turn semantic downgrade, and immutable source messages; and
- non-interchangeable ordered untrusted/admitted target interfaces compiled
  only in tests.

The complete current writer, reader, queue, branch/fork, delete, export,
compaction/recovery, ACP ingress, capability, and provider-lowering ownership
map is retained in
[`p30-0-multimodal-characterization.md`](../../verification/p30-0-multimodal-characterization.md).

## Decision And Compatibility

P30.0 used `combine`:

- preserve every existing production owner and the P23.H1 explicit
  zero-mutation rich-input rejection;
- characterize the ordered typed-input, private-store, tagged-persistence, and
  provider-preparation target mechanisms selected by the accepted P30 audit;
  and
- keep P29 capability/profile ownership and P25.1 provider lowering unchanged.

Compatibility is unchanged because the slice added tests and documentation
only. It did not accept a new media kind, advertise ACP image/embedded support,
fetch ResourceLinks, move bytes, alter recovery, or make P30.1 executable.

## Verification

Closeout passed the focused P30 suites, all package race suites required by the
promotion freeze, and repository/documentation gates:

```text
go test ./engine -run 'TestSubmitMessageWithImages|TestQueryMediaSize|TestP30'
go test ./engine/transcript -run 'Test.*P30|Test.*Image|Test.*Record'
go test ./engine/session -run 'Test.*P30|Test.*Branch|Test.*Delete|Test.*Export'
go test ./internal/tui -run 'Test.*P30|Test.*Composer.*Image|Test.*ModelSupportsImages'
go test ./server/acp -run 'Test.*P30|Test.*Prompt'
go test -race -timeout=20m ./engine/...
go test -race -timeout=20m ./internal/tui/...
go test -race -timeout=20m ./server/acp/...
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

Passing these fixtures proves the current mismatch and owner map. It does not
prove P30's target ordered input, capability resolver, private media
durability, ACP rich path, or safe recovery.

## Rollback And Next State

Rollback removes the P30.0 tests, test-only target types, verification record,
and tracker/history updates. There is no production or durable-state rollback.

P30.1-P30.6 remain accepted but queued. No successor became `Ready` as a side
effect of this closeout; root `PLAN.md` must perform a separate evidence-backed
selection.

## Current Replacements

- Current TUI composer behavior:
  [`composer.md`](../../../architecture/tui/contracts/composer.md)
- Current ACP behavior:
  [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md)
- Current provider behavior:
  [`model-providers.md`](../../../architecture/platform/model-providers.md)
- Current persistence behavior:
  [`transcripts.md`](../../../architecture/state/transcripts.md)
- Current recovery behavior:
  [`recovery.md`](../../../architecture/runtime/recovery.md)
- Reproducible P30.0 evidence:
  [`p30-0-multimodal-characterization.md`](../../verification/p30-0-multimodal-characterization.md)
