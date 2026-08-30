# Desktop Runtime Queue

**Status:** historical
**Completed:** 2026-08-30

> **Ownership:** delivery record for Desktop text runtime-queue adaptation;
> current behavior belongs to the
> [Desktop workbench architecture](../../architecture/desktop-workbench.md).

## Outcome

This delivery adapted the TUI's useful busy-turn queue outcome without moving
runtime ownership into Electron. Desktop now queues later text prompts under a
retry-stable client identity, displays the server-owned pending snapshot, and
removes only an exact still-pending item. The app-server serializes direct-Turn
and idle-queue admission, then submits claimed items through the existing
`QueryEngine` event and terminal path. QueryEngine remains the durable queue,
preflight, transcript, and settlement authority. A monotonic coordinator
revision prevents older HTTP, SSE, or Session-snapshot projections from
replacing newer queue state.

The retry identity is backed by a versioned engine-ledger admission receipt,
not a renderer or app-server cache. Its digest-only record survives settlement,
cancellation, and restart, so an exact retry after a lost acknowledgement
cannot create duplicate work. The v2 ledger reads and migrates v1 pending text
items; corrupt cross-scope receipts fail at recovery. Runtime claim failures
project a bounded blocked state to Desktop and recover only through an exact
successful queue or execution-setting mutation.

The renderer never claims or settles queued work. A successful terminal drains
the next generic item only when the Session returns to idle; waiting, error,
closed, and Goal-continuation states retain their existing boundaries. Durable
attach reserves its explicit first Turn, or restores its ProjectGraph
interaction, before starting the generic runtime-input pump.

## Evidence recorded with the delivery

- focused engine tests cover idempotent same-identity admission,
  different-payload conflict, settlement/cancellation and restart receipts,
  v1 migration, scope validation, and atomic persistence failure without queue
  mutation;
- focused app-server tests cover authenticated list/admit/cancel routes,
  automatic post-terminal claim, event ordering, processing conflicts,
  invalid identity, unavailable adapters, monotonic projections, attach-first
  recovery ordering, historical acknowledgement after Turn settlement,
  bounded claim-failure projection/recovery, and concurrent admission under
  the race detector; and
- `make desktop-check` covers renderer state replacement, active-Turn control
  admission, host/browser transport validation, safe queue rendering, and Node
  syntax; and
- `make publication-scan-expression PUBLICATION_ROOT=.` matched 1,002 exact
  reviewed findings and zero unresolved findings on the delivery tree. Queue
  UUID and token fixtures remained individually reviewed; no sentinel,
  allowlist, directory, or rule exemption was added.

## Exclusions at closeout

This delivery did not add rich queued media or editing, Goal controls, a Task
Explorer, command discovery, ghost suggestions, provider-portfolio editing,
MCP configuration, live-provider evidence, or physical Electron UI acceptance.
Those exclusions do not become accepted work merely because the TUI exposes a
related capability.

## Current replacement

Use the [Desktop workbench guide](../../guides/desktop-workbench.md) for the
operator workflow and the
[Desktop workbench architecture](../../architecture/desktop-workbench.md) for
current queue ownership, ordering, and failure semantics.
