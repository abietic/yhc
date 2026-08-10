# P41.1 Fixed-Size Geometry Owner

**Status:** historical
**Closed gaps:** G25
**Completed:** 2026-08-02
**Adoption:** `project-native`

> **Ownership:** completion evidence for the profile-owned fixed-box
> projection, same-render interaction geometry, source-owner deletion, and G25
> closure. Current behavior belongs in the
> [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P41.1 replaced the remaining Lip Gloss fixed-size layout seam with one
package-private `DisplayCellProfile` projection. The existing compatibility
helpers still accept a body width that includes horizontal padding and excludes
enabled side borders. Width-only callers derive natural height; fixed-height
callers request the complete rendered height including top and bottom borders.
Non-positive body width or fixed-box height returns an empty compatibility
result; the private projection's zero-height form is the width-only
natural-height path.

The projection returns the exact rendered string and rows together with local
inner and outer `layoutRect` values. It computes border, padding, content, and
height bands before decoration. Tabs remain semantic until wrapping and
horizontal alignment determine the physical inner origin. Combining sequences,
variation selectors, ZWJ emoji, regional indicators, Indic conjuncts, and
ambiguous-width scalars stay whole under the selected profile. A cluster that
cannot fit is omitted whole instead of splitting or widening the fixed box.

Lip Gloss remains a decoration helper only. The projector removes width,
height, maximum size, margin, padding, alignment, border, inline, tab-width,
and transform fields before applying foreground, background, and emphasis.
Padding and borders are generated from the computed profile-owned rectangles.
Every emitted row therefore measures exactly the requested body width plus its
enabled side borders.

## Interaction And Source Ownership

Normal and annotated user-message paths now produce the same visible rows.
`ChatView` binds selection metadata and viewport row identity to those rendered
rows, so extraction and pointer bounds cannot select a different wrap. Modal
centering and hit geometry derive from the same projected rows through
`DisplayCellProfile`; no interactive caller remeasures a fixed box through Lip
Gloss.

The G11.F1 type-aware source gate no longer allows Lip Gloss `Width` in
`contentRenderStyleWidth` or `Height` in `contentRenderStyleBox`. A production
style fixture also pins the accepted boundary: current fixed-box styles carry
no width, height, maximum size, margin, inline, or transform setting. P41.2's
App-owned renderer pool and its exact key, LRU, in-flight eviction, and fallback
behavior are unchanged.

## Proof

The deterministic matrix closes both promotion mismatches: bordered and padded
tabs expand at their actual non-zero origin, and the asymmetric-padding Indic
fixture no longer exceeds the selected profile width. Additional fixtures cover
post-Unicode-15 scalars, Indic conjuncts, VS15/VS16, ZWJ emoji, lone and paired
regional indicators, combining-only input, narrow/wide ambiguous policy,
widths 1/2/3/4/8/10/20, full and one-sided borders, natural and fixed height,
all horizontal and vertical alignments, impossible-cluster omission, SGR/OSC8
balancing, unsafe controls, selection extraction, pointer round trips, and
modal placement.

Focused P41.1, G11.F1, P41.2 compatibility, Unix PTY lifecycle, and race tests
pass. The complete TUI package and repository formatting, lint, test, build,
documentation, manifest, and diff gates also pass. PTY capture remains emitted
byte and lifecycle evidence; it does not infer a physical terminal/font grid.

## Compatibility And Rollback

No exported Go API, durable schema, transcript, Session, model, provider,
permission, or entrypoint contract changed. The user-message ANSI golden now
records equivalent background decoration with fewer redundant reset/reapply
sequences; visible text, color, and geometry are unchanged.

A squash revert can restore the prior compatibility helpers and their two exact
G11.F1 exceptions without data migration. That rollback reopens G25 and restores
Lip Gloss as a second fixed-size geometry owner. P41.2 remains independently
rollbackable.
