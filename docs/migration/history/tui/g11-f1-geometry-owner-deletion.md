# G11.F1 Geometry Owner Deletion

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.F1 compatibility-owner deletion, residual source
> classification, focused evidence, compatibility, and rollback

## Outcome

The accepted G11 `combine` program's `project-native` deletion slice removes
the short-lived geometry owners left after G11.E4:

- `terminalLayoutSafetyWidth`, `overlayCentered`, and `truncateDisplay`;
- the `widthProfile`/`defaultWidthProfile` aliases, derived compatibility ID,
  table-only default/profile render wrappers, and default cell-minimum wrapper;
  and
- table-era cache/control-state names that no longer describe the
  App-selected `DisplayCellProfile`.

Production Markdown/table code and its tests now name
`DisplayCellProfile` directly. Exact profile-bearing table calls replace the
test-only wrappers, so production source retains no hidden default table
selector.

## Classified Production Boundary

The deletion inventory found residual direct geometry selection outside the
already-scoped G11.E1-E4 gates. Compatibility streaming and legacy
user/thinking history rows, Agent transcript rows, Markdown plain-text and
margin helpers, the window-too-small projection, header path ellipsis, and
the disconnected permission prompt now use `DisplayCellProfile` operations.
Canonical message/source bytes, editing, semantic offsets, runtime events,
persistence, permissions, and replay are unchanged.

[`display_cell_g11f1_test.go`](../../../../internal/tui/display_cell_g11f1_test.go)
loads every production Go package selected under `internal/tui` for the
supported Linux amd64, Darwin amd64/arm64, and Windows amd64 builds, reads
compiler export data with standard-library Go tooling, and inspects the typed
Go AST. Chained receivers, method values, method expressions, and
supported-platform build constraints therefore cannot bypass the gate. It
rejects:

- any declaration of a deleted compatibility owner or table-only adapter; and
- unclassified direct Lip Gloss, `x/ansi`, Glamour ANSI, Uniseg, or rune-count
  geometry selectors.

Fixed-width Lip Gloss composition is centralized behind
`contentRenderStyleWidth`, which projects origin-sensitive tabs with the
selected profile before delegating styling, padding, borders, and compatibility
wrapping. It remains an explicit renderer adapter until Lip Gloss accepts
`DisplayCellProfile` directly. Other classified selectors are streaming
statistics, attachment thresholds, composer/Vim rune offsets, semantic word
selection, low-level profile iterator policy, Bubbles textarea adapters, or
rendered-widget row accounting. Every allowlist entry names its current owner,
removal condition, and exact expected call count; stale entries fail the gate.

## Compatibility

The production default profile, semantic Markdown/table grammar, renderer
cache keys, content bytes, selection/cursor contracts, runtime state, durable
state, permissions, replay, and supported entrypoints remain compatible.
Injected profile variants now reach legacy user/thinking rows and Agent
transcript rendering instead of being reinterpreted through rune or library
width. This is the already-accepted G11 ownership contract, not a new profile
selection policy.

## Verification

Focused and race evidence:

```text
go test ./internal/tui -run '^TestG11F1' -count=1
go test ./internal/tui -count=1
go test -race ./internal/tui -run '^(TestG11F1|TestG11E[1-4]|TestEnoWelcomeResponsiveGolden)' -count=1
```

Repository closeout passes `make fmt`, `make lint`, `make test` (5,042
tests), `make build`, `make lint-new`, `make docs-check`, migration manifest
validation, and `git diff --check`. The final migration scanner records 463
production files/159,512 lines, 417 test files/143,540 lines, 92 TUI
production files/41,576 lines, and 114 TUI test files/27,745 lines.

## Rollback

Rollback restores the last coherent G11.E4 profile projection. A deleted
compatibility adapter may return only with its exact production owner and a
classified gate entry containing an explicit removal condition. The broad
emoji-range safety heuristic may not return as a second production width
owner, and a partial rollback that restores table-only aliases without their
callers is unsupported.
