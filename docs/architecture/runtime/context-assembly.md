# Prompt and Message Context

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** QueryEngine for source collection; canonical ProjectGraph
> round preparation for pre-model order; `engine/context` helpers

## Current Assembly Order

The context package builds user/system metadata, discovers instruction files,
and provides prompt and attachment helpers. Exported prompt builders are not all
production composition roots. The authoritative model-facing order is the
sequence currently called by `QueryEngine.submitMessage` and
`runCanonicalRoundPreparation`.

## QueryEngine source collection

For a submitted turn, QueryEngine:

1. Runs user-prompt hooks, then appends the user message and hook attachments.
2. Builds user context (date, platform, CWD) and system context (current git
   snapshot).
3. Builds the base system text from custom plus append prompts.
4. Appends the enabled unified memory prompt.
5. Appends discovered CLAUDE.md content.
6. Captures the current model-visible tools in `ToolUseOptions` and passes all
   sources to `Query`.

This is the current code order. The broader `AssembleFullSystemPrompt` helper is
an available API but is not the authoritative QueryEngine assembly path.

## Pre-model order in canonical Graph preparation

1. Append drained async-hook messages to history.
2. Select history after the compact boundary.
3. Apply tool-result budget, snip, microcompact, and collapse transforms.
4. Append system context to the system message.
5. Run optional auto/reactive compaction and reinjection.
6. Apply stateful content-replacement budgeting.
7. Prepend user context as a meta user message.
8. Normalize messages for the provider API.
9. Call the model with the stable tool projection.

After a tool round, queue messages, generated attachments, memory-prefetch, and
skill-prefetch results are appended to tool results and therefore enter the
next iteration in that order.

## Invariants and edge cases

- Context helpers return copies or new slices; callers must not mutate shared
  history while a model request is in flight.
- `PrependUserContext` skips injection when `NODE_ENV=test`.
- Missing instruction files are non-errors; malformed or inaccessible files are
  handled by their discovery/load contract.
- System and user context have different API roles. Moving both into one string
  changes provider-visible message order and caching.
- The `engine/attachments.Processor` seam is called by canonical after-tool
  reconciliation, but its
  current `GetAttachments` implementation returns no attachments. Generated
  tool attachments and hook attachments use other active paths; do not infer a
  populated processor pipeline from the package's presence.

## Code references

- [QueryEngine context collection](../../../engine/engine.go)
- [`BuildUserContext`](../../../engine/context/context.go)
- [`BuildSystemContext`](../../../engine/context/context.go)
- [`ComposeBaseSystemPrompt`](../../../engine/context/context.go)
- [`AppendSystemContext`](../../../engine/context/context.go)
- [`PrependUserContext`](../../../engine/context/context.go)
- [`LoadClaudeMdContent`](../../../engine/context/claudemd.go)
- [`attachments.Processor.GetAttachments`](../../../engine/attachments/attachments.go)
- [Canonical round preparation](../../../engine/round_lifecycle.go)
- [Queue/attachment/prefetch order](../../../engine/round_lifecycle.go)

## Related tracking

Compaction details are in [`compaction.md`](compaction.md), tool visibility in
[`tool-registry.md`](../capabilities/tool-registry.md), and open gaps in
[`REMAINING.md`](../../migration/REMAINING.md).
