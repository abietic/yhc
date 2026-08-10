# P41.1 Fixed-Size Geometry Promotion Evidence

**Status:** verification
**Snapshot:** `bc7ce07e127dfbb03924ad9da18212a2117d288f`
**Measured:** 2026-08-02

> **Ownership:** the promotion-snapshot differential reproduction, evidence
> limits, and commands that make the P41.1 owner-deletion contract executable

## Result

P41.1 is ready because deterministic fixtures now reproduce actual geometry
differences, not merely a structural second owner:

- in a rounded box with one cell of horizontal padding, the current adapter
  expands `\tX` at column 0 even though content starts at column 2; the current
  visible row contains four tab spaces where the selected profile requires two;
- with asymmetric padding, the current Lip Gloss fixed-width result containing
  an Indic conjunct measures one profile cell wider than its requested body.

The same matrix establishes non-drift examples for combining text, ZWJ emoji,
centered wrapping, and balanced SGR/OSC8 content. These examples narrow the
claim: P41.1 fixes one reproduced tab-origin difference, one reproduced Indic
width difference, and the verified duplicate owner. It does not claim every
Unicode scalar, terminal, or font currently drifts.

The accepted target and rollback boundary are in
[`p41-1-fixed-size-geometry-owner.md`](../plans/p41-1-fixed-size-geometry-owner.md).
P41.1 later merged, the final proof matrix passed, and G25 closed. Delivery
evidence is in
[`p41-1-fixed-size-geometry-owner.md`](../history/tui/p41-1-fixed-size-geometry-owner.md).

## Reproduce The Promotion Evidence

Run the portable differential and target-primitive matrix:

```bash
go test ./internal/tui -run '^TestP411Promotion' -count=1
```

The three fixtures establish separate facts:

| Fixture | Fact established |
|---|---|
| `TestP411PromotionReproducesNonZeroOriginTabMismatch` | Current origin-0 expansion differs from the selected profile at the actual bordered/padded content origin while both frames keep the same outer width. |
| `TestP411PromotionCurrentAdapterDifferentialBoundaries` | The selected Indic fixture exceeds the requested profile width by one cell; named combining, emoji, wrapping, and control fixtures do not overstate drift. |
| `TestP411PromotionFreezesProfileOwnedInnerProjection` | Existing profile primitives can produce exact rows for tabs, combining/ZWJ clusters, narrow/wide ambiguous policy, wrapping, alignment, and row padding. This is target evidence, not current fixed-box behavior. |

The Unix real-program lifecycle matrix now includes a fixed-size user-message
payload containing a ZWJ emoji and combining sequence:

```bash
go test ./internal/tui \
  -run '^TestG11F2TerminalLifecyclePTY$' \
  -count=1
```

It resizes through 40, 48, 72, 80, 120, 150, and 180 columns, repaints, routes
the current jump-pill click, and verifies terminal restoration. This proves the
payload reaches the production fixed-box path during a real PTY lifecycle. The
sticky-header path normalizes whitespace, so this PTY fixture deliberately does
not claim tab-origin coverage; the deterministic differential fixture owns that
fact. The PTY run did not by itself prove P41.1's target rectangle or a
physical terminal/font grid; the later implementation added deterministic
exact-frame assertions while retaining that terminal/font evidence limit.

## Current Ownership Evidence

[`contentRenderStyleWidth`](../../../internal/tui/content_geometry.go#L15)
calls `profile.expandTabs(content, 0)` and then Lip Gloss `Width(...).Render`.
[`contentRenderStyleBox`](../../../internal/tui/content_geometry.go#L31) adds
Lip Gloss `Height`. Lip Gloss v2.0.5 then performs its own wrap, padding,
vertical/horizontal alignment, border, and max-size operations. The project
profile later measures and truncates rows through a different cell policy.

The G11.F1 source gate explicitly retains exactly those two calls as temporary
exceptions in
[`display_cell_g11f1_test.go`](../../../internal/tui/display_cell_g11f1_test.go#L165).
The implementation is complete only when the calls and exceptions disappear,
not when a second helper is added beside them.

## Target Fixture Matrix

The implementation replaces the temporary mismatch expectations with exact
desired-behavior tests across:

- tabs at inner origins 0, 1, 2, and 3 and at left/center/right placement;
- post-Unicode-15 scalars, Indic conjuncts, VS15/VS16, ZWJ emoji, regional
  indicators, combining-only input, and narrow/wide ambiguous policy;
- SGR and OSC8 crossing wrap/truncate boundaries;
- widths 1, 2, 3, 4, 8, 10, and 20 with no/full/one-sided border, asymmetric
  padding, natural/fixed height, and all alignments;
- annotated user messages whose visible frame, selection rows, hit targets,
  and extracted text derive from one returned projection; and
- the fixed-box PTY payload at the existing resize/click/restoration widths.

Each physical row must measure exactly the projection's outer width under the
selected profile. Every returned inner/outer rectangle must match the emitted
rows. An impossible over-width cluster may be omitted whole; it may not be split
or widen the fixed box.

## Reference Decision And Limits

Claude Code Ripe explicitly reduces container padding before its own text wrap,
which supports one explicit layout owner but does not supply this project's
Unicode policy. Crush commonly delegates fixed width to Lip Gloss, which keeps
the duplicate owner P41.1 is removing. The decision is therefore
`project-native`: preserve useful decoration, not either reference's width
implementation.

Promotion changed tests and accepted documentation only. It did not change
production rendering, interaction geometry, P41.2, durable state, or any public
entrypoint. Its green run did not close G25; the separately reviewed
implementation and closeout did.
