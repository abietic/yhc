# G11.D3 Shared Pill Geometry

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.D3 delivery result, shared jump-pill render/hitbox
> geometry, cache identity, verification, compatibility, and rollback

## Outcome

The accepted `combine` slice extends the App-selected immutable
[`DisplayCellProfile`](../../../../internal/tui/display_cell.go) through the
jump-to-bottom pill. G11.B remains the sole follow, append-epoch, baseline,
visibility, label, and action owner. G11.D3 converts that semantic model, the
chat rectangle, and the exact render environment into one package-private
`ChatView` geometry result.

That result publishes the styled run, final chat-relative row, inclusive start
cell, exclusive end cell, follow action, selected profile identity, rectangle,
and complete theme/geometry/profile cache identity. Theme, profile, width, or
height changes recompute presentation without mutating follow or unseen state.

## Shared Rendering And Hit Testing

[`ChatView.Render`](../../../../internal/tui/chat.go) places the geometry's
published run on its published final row after sticky-header composition.
[`App.pillClickHits`](../../../../internal/tui/app.go) selects the same geometry
and invokes its hit test; it no longer reconstructs the semantic label, styled
width, centering, row, or cell bounds.

Centering searches the selected profile's cell grid and expands tabs from the
candidate start cell. The chosen run is bounded by the chat rectangle and
balances supported SGR/OSC state. If the run cannot fit, the same profile
truncates it at the rectangle origin without splitting an extended grapheme
cluster or leaking control state.

Modal, expanded-content, sidebar, and chat routing order did not change, so
foreground surfaces continue to preempt the background pill hitbox.

## Verification

Focused and race evidence covers zero/one/many labels at
40/80/120/150/180 columns; ASCII, tab, CJK, combining, Indic,
variation-selector, ZWJ, paired/lone flag, bare-label, assistant-star, ANSI,
and OSC fixtures; inclusive/exclusive hit boundaries; sticky headers; resize,
theme, and profile cache separation; unchanged follow state; routing
precedence; and a source-owner guard that rejects independent width selection
inside pill rendering or App hit testing.

```text
go test ./internal/tui -run 'Test(G11D3|StickyHeaderKeepsPill|JumpToBottomPill|G11B)' -count=1
go test -race ./internal/tui -run 'Test(G11D3|StickyHeaderKeepsPill|JumpToBottomPill|G11B)' -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_scan
go run ./scripts/migration_manifest.go check
git diff --check
```

The source scan reports 460 production files / 157,919 lines, 412 test files /
140,931 lines, 89 TUI production files / 39,983 lines, 109 TUI test files /
25,136 lines, and 57 `go list` packages including scripts. The reference
manifest remains valid at 1,884 classified files with 816 mapped.

## Compatibility And Rollback

Canonical chat labels, scroll/follow transitions, append classification,
rectangle allocation, runtime events, durable schemas, permissions, replay,
and all supported entrypoints remain compatible. The deliberate presentation
change is that the pill now follows the selected project grid rather than a
duplicated Lip Gloss width calculation.

Rollback reverts the geometry cache, centered profile projection, shared hit
test, and focused tests together to the coherent G11.B semantic-model adapter.
It must not restore the pre-G11.B visibility sentinel or leave rendering and
mouse routing on different width owners. Current behavior belongs in the
[`responsive layout contract`](../../../architecture/tui/contracts/responsive-layout.md);
G11.E1 promotion belongs in [`migration/PLAN.md`](../../PLAN.md).
