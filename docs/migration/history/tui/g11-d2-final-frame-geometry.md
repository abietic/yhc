# G11.D2 Final-Frame Geometry

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.D2 delivery result, final-frame/sidebar/status profile
> projection, first-overflow diagnostics, verification, compatibility, and
> rollback

## Outcome

The accepted `combine` slice extends the App-selected immutable
[`DisplayCellProfile`](../../../../internal/tui/display_cell.go#L55) from the
G11.D1 Markdown/cache boundary through the remaining non-pill final
composition path. Generic and Assistant chat rows, sticky prompts, vertical
bands, main/sidebar fitting, wide-sidebar content, status-hook output, and the
complete App frame now measure, truncate, pad, and balance against that one
profile.

Rectangle allocation and textual labels did not change. Band and sidebar
operations receive their owning rectangle X coordinate, so tabs expand from
the same column that draws their text. The wide sidebar therefore begins at
its allocated separator X even beside semantic-table rows containing clusters
whose `x/ansi` width differs from the selected project grid.

## Final Frame And Diagnostics

[`finalizeFrameGeometry`](../../../../internal/tui/app.go#L1736) balances
supported SGR/OSC state per physical row, applies the selected profile's
terminal-width bound, and returns the first pre-clip overflow diagnostic. That
diagnostic contains the complete profile summary plus the zero-based row,
measured width, and limit. It remains package-private for development and
tests; `App.View` stays side-effect-free and adds no persistent user chrome.

No-color finalization strips terminal controls before the same profile bound.
The profile path also sanitizes unsupported controls and never splits an
extended grapheme cluster. Status hooks may supply ANSI, OSC, CJK, emoji, and
tabs; their left/right alignment and crowded left-only fallback use the same
profile as the finalizer.

## Ownership Guard

Focused source inspection covers
[`ChatView.renderItem`](../../../../internal/tui/chat.go#L1234),
[`truncateStickyPrompt`](../../../../internal/tui/chat.go),
[`renderLayoutBands`](../../../../internal/tui/layout.go#L109),
[`joinLayoutColumns`](../../../../internal/tui/layout.go#L281),
[`fitLayoutColumnLine`](../../../../internal/tui/layout.go#L295),
[`App.renderWideSidebar`](../../../../internal/tui/responsive_sidebar.go#L16),
status alignment, and finalization. It rejects a reintroduced
`x/ansi`/Lip Gloss/`terminalLayoutSafetyWidth`/`truncateDisplay` geometry
selection in those migrated owners. Pill centering remains deliberately
exempt for G11.D3.

## Verification

Focused and race evidence covers the 40/80/120/150/180 terminal-width matrix,
exact table/sidebar separator X, origin-aware tabs, generic and Assistant
history rows, sticky prompts, status-hook controls, first-overflow policy
diagnostics, ANSI/OSC balance, no-color output, and the source-owner guard:
The responsive welcome/status golden was refreshed only for the selected
profile's observable spacing and truncation changes.

```text
go test ./internal/tui -run '^(TestG11A|TestG11D1|TestG11D2|TestAlignStatusLine|TestGlyph|TestRenderTool|TestStickyHeader|TestResponsiveLayout|TestLayoutRectangles|TestNoColorFinalFrame)' -count=1
go test -race ./internal/tui -run '^TestG11D2' -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Compatibility And Rollback

Canonical Markdown/transcript bytes, runtime events, permissions, replay,
durable schemas, rectangle allocation, status text, sidebar ordering, and
supported entrypoints remain compatible. The deliberate geometry change is
that every migrated final-frame boundary now follows the deterministic
project grid instead of a neighboring `x/ansi`, Lip Gloss, or conservative
range heuristic.

Rollback reverts chat clipping/sticky output, band/column composition,
wide-sidebar truncation, status alignment, final-frame clipping/diagnostics,
and focused tests together to the coherent G11.D1 boundary. A partial rollback
would restore mixed width owners and is not supported. Current behavior
belongs in the
[`responsive layout contract`](../../../architecture/tui/contracts/responsive-layout.md);
remaining pill geometry belongs in
[`migration/PLAN.md`](../../PLAN.md).
