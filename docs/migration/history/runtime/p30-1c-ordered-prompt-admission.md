# P30.1c Ordered Prompt And Selected-Route Admission

**Status:** historical
**Completed:** 2026-07-29
**Last verified:** 2026-07-29

> **Ownership:** completed P30.1c `project-native` decision within P30's
> accepted `combine` program, immediate ordered prompt admission, selected-route
> capability binding, Hook compatibility, media lifetime, fallback boundary,
> verification, and rollback. Current behavior belongs in
> [`model-providers.md`](../../../architecture/platform/model-providers.md);
> durable and cross-entrypoint expansion remains in
> [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md).

## Outcome

`QueryEngine.SubmitPromptInput` now accepts one versioned ordered union of
immutable text and inline-image parts. The engine validates image bytes through
P30.1b's strict generic predicate, resolves the exact first-round provider
route, requires an explicit supported capability decision, copies decoded media
into an unpredictable turn-local store, and binds part order, opaque refs,
MIME/detail, route generation, provider/model, and capability source before
running the user-prompt Hook.

The Hook receives only the exact concatenation of text parts. A non-identical
rewrite maps back only when the prompt contains exactly one text part; zero or
multiple text parts fail before Goal, Hook-status, transcript, history,
permission-review, or model mutation. Hook rejection remains effective. The
provider receives the original interleaving and exact normalized image
MIME/detail through the existing Eino multipart and P25.1 lowering owners.

Every rich model call rechecks the bound generation, requested route, resolved
provider/model, capability source, ordered media binding, and live store. A
configured or provider-requested fallback is rejected before fallback events,
route mutation, or an alternate model call. Model changes, Plan phase changes,
session activation, and engine close advance the route generation.
Proactive auto-compaction uses its deterministic path for the admitted live
turn because `SummaryModel` has no separately admitted rich route.

## Decision And Compatibility

P30.1c used `project-native` within P30's accepted `combine` program. It
preserves QueryEngine, ProjectGraph, classic messages, provider routing, P25.1
lowering, and legacy command ownership instead of adding a second loop or
provider adapter.

`SubmitMessage` and metadata-only text paths retain their previous shape and
command/override behavior. `SubmitPromptInput` is deliberately literal:
slash-prefixed text is model input, not a command. The legacy
`SubmitMessageWithImages` shape remains complete text followed by caller-ordered
images, but valid images now also require a supported selected route. Missing,
incomplete, mismatched, unsupported, or unknown route/capability facts fail
closed with `TerminalPromptInputError`; stable errors expose only part
index/kind/reason and bounded route identity.

This slice adds no durable rich-input record, busy-queue ordered union,
transcript media indirection, TUI/ACP rich surface, provider upload or
derivative, persisted media cache, non-image media type, or rich fallback. The
existing durable queue therefore retains its P30.1b legacy image shape.

## Media Lifetime

`MediaRef` values carry no public fields and are valid only for one
cryptographically random store identity and store generation. The engine is
the only admission owner, while the narrow `ProviderMediaPreparer` boundary is
the only ref-to-provider lowering owner. Stores zero decoded byte slices and
invalidate every ref on success, cancellation, Hook rejection, model failure,
stale route, synchronous admission failure after store creation, or engine
close.

## Verification

Closeout uses the frozen focused, race, repository, documentation, manifest,
and diff gates:

```text
go test ./engine -run 'TestSubmitPromptInput|TestAdmittedPrompt|TestPromptRouteGeneration|TestSubmitMessageWithImages'
go test -race -timeout=20m ./engine/... -count=1
go test ./engine
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
git diff --check
```

Focused fixtures cover text/image/text/image order, exact detail and Hook text,
literal slash input, missing/unsupported/unknown/mismatched capability,
redacted zero-mutation failure, sole and ambiguous Hook rewrites, no alternate
fallback call or fallback event, no rich call to an unadmitted auto-compaction
summary route, model and Plan generation changes, and media cleanup on success,
cancellation, Hook rejection, model error, stale route, and close.

## Rollback And Next State

Rollback may remove the new immediate ordered API, capability resolver, and
turn-local store together. It may not restore valid legacy-image dispatch on
an unsupported or unknown selected route, weaken P30.1b generic validation, or
permit rich fallback to a different route.

P30.2-P30.6 remain accepted but queued, and G32 remains open. No successor
became `Ready` automatically; root `PLAN.md` must select the next slice
separately.
