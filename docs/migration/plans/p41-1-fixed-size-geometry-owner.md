# P41.1 Fixed-Size Geometry Owner

**Status:** historical
**Execution state:** complete; G25 closed
**Last verified:** 2026-08-02

> **Ownership:** the completed P41.1 target contract and rollback boundary.
> Current behavior belongs in the [TUI architecture](../../architecture/tui/README.md),
> and delivery evidence is in the
> [P41.1 closeout](../history/tui/p41-1-fixed-size-geometry-owner.md).

## Decision

P41.1 completed under `project-native`: the App-selected
`DisplayCellProfile` owns the final cell rectangle, while Lip Gloss remains
available only for decoration that cannot wrap, align, pad, border, truncate,
or otherwise resize content.

This is not a speculative Unicode cleanup. Promotion fixtures reproduce two
current mismatches:

- a rounded box with one cell of left padding starts content at column 2, but
  [`contentRenderStyleWidth`](../../../internal/tui/content_geometry.go#L15)
  expands its tab at column 0 before Lip Gloss sees the box; and
- an Indic conjunct rendered through asymmetric padding produces a row that
  `DisplayCellProfile` measures one cell wider than the requested fixed body.

The complete reproduction and its limits are in
[`p41-1-fixed-size-geometry-promotion.md`](../verification/p41-1-fixed-size-geometry-promotion.md).
The implementation, source-owner deletion, interaction proof, and repository
gates passed; G25 is closed.

## User Problem And Scope

The current adapter has two independent answers to one question: which cells
does a fixed box occupy? `DisplayCellProfile` expands tabs, measures selected
Unicode clusters, and later truncates chat rows, while Lip Gloss independently
wraps, pads, aligns, borders, and fixes width/height. A border, content row, or
interactive rectangle can therefore disagree with the App-selected grid.

P41.1 changed only fixed-size TUI projection under `internal/tui`:

- `contentRenderStyleWidth` and `contentRenderStyleBox` become compatibility
  wrappers over one package-private profile-owned projection;
- every current production caller continues through those wrappers or consumes
  the richer projection when it publishes interaction geometry;
- user-message normal and selection rendering share the same projected rows;
- modal placement and Chat viewport publication consume the projection's
  returned frame wherever they route pointer or selection input; and
- the two Lip Gloss fixed-size exceptions leave the G11.F1 source gate.

No exported Go API changes. CLI, ACP, Plain, MCP, transcript, Session, model,
provider, permission, renderer-pool, and durable-state behavior are outside the
slice.

## Frozen Observable Contract

### Preserve the current call dimensions

The existing width parameter remains a **body width**: it includes horizontal
padding and excludes enabled left/right border cells. The rendered outer width
is therefore:

```text
body width + enabled left border width + enabled right border width
```

This preserves callers such as the editor that already reserve border cells.
For `contentRenderStyleBox`, the height parameter remains the requested total
rendered height, including any enabled vertical border. A width-only box derives
its height from projected content, padding, and borders. Non-positive body width
or fixed-box height returns an empty compatibility result; the private
projection's zero height is reserved for the width-only natural-height path.

Margins are caller-owned placement outside this fixed box. The current
production fixed-box styles use no margins; P41.1 does not introduce margin,
`MaxWidth`, `MaxHeight`, `Inline`, or geometry-changing `Transform` semantics.
A source/fixture gate must keep those fields out of this internal boundary.

### Return the rendered value and its frame together

Add one package-private projection result with:

- rendered rows/string;
- an outer `layoutRect` at local origin `(0,0)`;
- an inner content `layoutRect` after border and padding; and
- enough row identity to let selection metadata bind to the exact projected
  physical rows.

The compatibility wrappers may return only the rendered string. A caller that
publishes a pointer, selection, or overlay rectangle must consume the returned
frame or a placement derived directly from it; it must not remeasure the string
through Lip Gloss or infer a different fixed rectangle.

### One ordered projection owns all size decisions

For each render:

1. Normalize the selected profile and read the style's enabled border,
   padding, horizontal/vertical alignment, and decorative properties.
2. Compute outer, body, padding, and inner rectangles with the preserved
   dimension rules above. Clamp subtraction at zero; never pass a negative
   layout width to another renderer.
3. Walk source grapheme/control runs with `DisplayCellProfile`. Keep tabs and
   supported SGR/OSC8 controls semantic until the row's actual inner origin and
   horizontal alignment are known.
4. Wrap logical lines with the profile, never split an extended grapheme
   cluster, and truncate any impossible over-width physical row at a whole
   cluster boundary so the fixed outer rectangle remains exact.
5. Choose left/center/right placement with profile measurement at the actual
   candidate origin, then expand tabs and pad every physical row to the exact
   inner width. Apply top/center/bottom placement and exact height padding when
   a height was requested.
6. Generate padding and border cells from the computed rectangles. Apply
   foreground/background/emphasis and border decoration only through styles
   stripped of every geometry-changing field.
7. Balance supported SGR and OSC8 controls per physical row, return the exact
   rows and geometry together, and publish that same frame to interaction
   routing.

If a single cluster cannot fit, omission at a whole-cluster boundary is the
accepted compatibility consequence for a fixed box. Splitting a cluster or
letting a second owner widen the box is not.

## Invariants

- The immutable App `DisplayCellProfile` identity and `RenderEnvironment`
  generation remain unchanged.
- The exact requested body width plus enabled border cells is the maximum and
  minimum profile-measured width of every rendered row.
- Tabs use the physical content origin, including border, padding, and the
  selected alignment offset.
- Combining sequences, variation selectors, ZWJ emoji, regional indicators,
  Indic conjuncts, and ambiguous-width scalars remain whole and follow the
  selected profile.
- Supported SGR and OSC8 controls remain balanced on every emitted row; unsafe
  controls retain the existing profile sanitization policy.
- Normal and annotated user-message paths produce identical visible geometry.
  Annotation bytes cannot influence wrap, padding, border, or hit targets.
- Interactive callers publish the frame produced by the same render. Stale or
  independently measured geometry cannot activate a hidden control or select
  a different row.
- P41.2's App-owned capacity-32 renderer pool, exact renderer key, eviction,
  fallback, and concurrency behavior remain byte-compatible and separately
  rollbackable.

## Implementation Boundary

P41.1 was one behavior PR:

1. introduce the profile-owned fixed-box request/result and projection under
   [`content_geometry.go`](../../../internal/tui/content_geometry.go#L15);
2. migrate both compatibility helpers and all current production callers;
3. connect the returned frame to current interaction publications, including
   user-message selection and modal/chat placement;
4. remove only the two fixed-size Lip Gloss exceptions from
   [`display_cell_g11f1_test.go`](../../../internal/tui/display_cell_g11f1_test.go#L165);
5. replace the temporary mismatch characterization with desired-behavior
   fixtures and update current architecture, gap/status owners, manifest, and
   one historical closeout record.

Do not combine P43 evaluation, P39 rewind, P41.2 tuning, a general style system,
or removal of the separately classified Bubbles/rendered-row adapters.

## Deterministic Proof Matrix

| Boundary | Required implementation proof |
|---|---|
| Current mismatch closes | Origin-sensitive tab and Indic-conjunct fixtures flip from reproduced drift to exact profile-owned rows. |
| Profile matrix | Golden rows cover post-Unicode-15 scalars, Indic conjuncts, VS15/VS16, ZWJ emoji, lone/paired regional indicators, combining-only input, and narrow/wide ambiguous policy. |
| Box geometry | Widths 1, 2, 3, 4, 8, 10, and 20 cover no/full/one-sided borders, asymmetric padding, natural/fixed height, and all horizontal/vertical alignments. Every emitted row and returned rectangle is exact. |
| Controls | SGR and OSC8 span wrap/truncation/border insertion without leaking state; unsafe controls retain the existing replacement policy. |
| Selection and hit testing | Annotated tab/emoji/combining user messages prove visible bytes, row metadata, selection extraction, pointer boundaries, and published frame share one projection. |
| PTY lifecycle | Widths 40, 48, 72, 80, 120, 150, and 180 resize a fixed-box Unicode payload, repaint it, and route the current-frame click before clean terminal restoration. This does not claim real font/terminal physical-grid equivalence. |
| Source ownership | No production fixed-box path invokes Lip Gloss `Width`, `Height`, placement, wrap, or another width oracle; the two G11.F1 exceptions are gone and all other classified exceptions remain exact. |
| P41.2 compatibility | Markdown output, pool pointer propagation, strict LRU, in-flight eviction, and race tests remain unchanged. |

Focused implementation validation must include the new P41.1 matrix, the G11.F1
source gate, selection tests, the Unix PTY scenario, and race coverage for
touched shared TUI state. Final closeout runs `make fmt`, `make lint`,
`make test`, `make build`, the migration-manifest check, documentation checks,
and `git diff --check`.

## Rollback

Rollback restores `contentRenderStyleWidth`/`contentRenderStyleBox` as the
single Lip Gloss compatibility seam and restores the two exact G11.F1
exceptions. It also reverts the P41.1 caller/frame fields and desired-behavior
fixtures as one unit.

There is no schema, transcript, Session, cache-key, profile-identity, or public
API migration. P41.2 remains in place. G25 reopens if rollback restores the
second geometry owner.

## Evidence And Owners

| Boundary | Source or evidence | Role |
|---|---|---|
| Current fixed projection | [`contentProjectFixedBox`](../../../internal/tui/content_fixed_box.go), [`contentRenderStyleWidth`](../../../internal/tui/content_geometry.go), and [`contentRenderStyleBox`](../../../internal/tui/content_geometry.go) | Return exact profile-owned rows and geometry; compatibility wrappers expose width-only natural height or positive fixed height. |
| Profile primitives | [`DisplayCellProfile`](../../../internal/tui/display_cell.go) and [`contentWrapSemanticLines`](../../../internal/tui/content_fixed_box.go) | Own selected cell measurement, semantic controls, origin-aware tabs, wrapping, alignment, padding, and whole-cluster omission. |
| Current interaction publication | [`ChatView.publishViewportFrame`](../../../internal/tui/chat.go) and [`modalFrameGeometry`](../../../internal/tui/modal_geometry.go) | Bind selection, pointer, and overlay placement to the rows emitted by the same profile projection. |
| Promotion and implementation fixtures | [`p41_1_geometry_promotion_test.go`](../../../internal/tui/p41_1_geometry_promotion_test.go) | Retain the historical mismatch and prove the completed profile-owned behavior matrix. |
| Delivery closeout | [`p41-1-fixed-size-geometry-owner.md`](../history/tui/p41-1-fixed-size-geometry-owner.md) | Records implementation, interaction, source deletion, verification, compatibility, and rollback evidence. |
| Comparative evidence | [`recent-delivery-remediation-audit.md`](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g25-one-fixed-size-geometry-owner) | Records the original repair contract and evidence limits. |
