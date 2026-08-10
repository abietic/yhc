# G11.A Frame-Integrity Characterization

**Status:** verification

**Last verified:** 2026-07-26

> **Ownership:** deterministic reproduction procedure and evidence boundary for
> G11.A. Current TUI behavior belongs in
> [`architecture/tui/`](../../architecture/tui/README.md), target behavior
> belongs in the
> [`G11 contract`](../plans/g11-tui-frame-integrity/README.md), and live order
> belongs in [`PLAN.md`](../PLAN.md).

## Result

G11.A changed no production behavior. It originally froze four facts so later
slices could replace them deliberately. The first three remain active geometry
evidence:

1. Goldmark-derived table runs preserve mixed plain, bold, inline-code, and
   suffix content after stable-prefix promotion, `Finalize`, and resize.
2. Table measurement, non-table safety, and final `x/ansi` composition
   disagreed for accepted Unicode fixtures; the G11.A wide App frame exposed
   the resulting sidebar-boundary shift on affected semantic-table rows.
3. Per-run SGR or OSC 8 emission can place control bytes between the base and
   continuation code points of one visible extended grapheme cluster.
4. At the G11.A baseline, `follow=false` and a valid unseen baseline were
   different facts, but the pill and hitbox required both. Top, empty,
   non-scrollable, and durable restored-away states could therefore lose the
   jump action.

The fourth fact is historical: G11.B replaced those assertions with
`TestG11B*` transition and interaction tests. G11.C has supplied the
independently tested profile/cluster kernel, and G11.D1 replaced the
production Markdown/cache assertions with exact App-selected profile
projection. G11.D2 has now replaced the wide-frame assertion with exact
profile-cell table/sidebar equality and added whole-frame bounds/control
evidence. G11.D3 has replaced the pill-geometry assertions with one shared
profile-owned render/hitbox result.

## Independent Cell Oracle

The test oracle owns a finite, explicit fixture grid. It:

- segments visible text with `github.com/rivo/uniseg`;
- removes supported 7-bit CSI and OSC 8 sequences with a test-only scanner;
- assigns fixture cells from constants rather than a production width helper;
  and
- compares those expected cells with the current table profile, non-table
  safety method, and `x/ansi` layout method as measured subjects.

The matrix covers ASCII controls around visible text, `🖥`, `⚙`, `🏷`, `✦`,
CJK, Indic conjuncts, VS15, VS16, bare and repeated warning symbols, ZWJ,
paired and lone regional indicators, combining sequences, ANSI SGR, and OSC 8.
Diagnostics include the source cluster, measured method or profile, expected
and actual cells, layout mode, terminal width, and row.

The locked dependency result for a lone regional indicator `🇺` is
`x/ansi = 2`, table profile `= 1`, and non-table safety `= 2`. This corrects
the earlier provisional assumption that `x/ansi` also returned one cell; the
test uses the current module versions as the measured subject while retaining
the explicit one-cell project oracle.

This oracle specifies one deterministic project grid. It does not observe
glyph pixels, terminal cursor reports, or font fallback. A PTY can prove that
the TUI emitted the selected grid; it cannot prove that an arbitrary
terminal/font pair draws every cluster at the same width.

## Frozen Decisions For G11.C

G11.A selects these inputs for the profile kernel:

- **Profile selection:** one versioned deterministic default is sufficient for
  the production integrity contract. G11.C provides App constructor injection
  and reports the selected immutable identity, but adds no terminal-name
  guessing, hidden calibration, or user-facing font-specific width variant.
  Terminal/font mismatch remains an explicit diagnostic limitation.
- **Cross-run cluster style:** the semantic run containing the first visible
  scalar of an extended grapheme cluster owns the style and link for the whole
  cluster. Later runs cannot open or close SGR/OSC inside that cluster. This
  tie-breaker changes presentation only; it does not normalize or mutate
  canonical Markdown or transcript bytes.
- **Cache identity:** renderer pools, streaming stable/full fragments,
  per-history-item renders, restored thread views, and any future pill geometry
  must consume the exact profile identity. Geometry also keys the exact width,
  owning rectangle start column, completeness where relevant, and geometry
  generation. G11.D1 therefore removed the frozen history render's former
  `±2` width tolerance.

The current Markdown renderer pool, `StreamingMarkdown` stable/full
identities, `ChatView` frozen-item cache, and viewport cache now consume one
exact App-selected render environment and width. Active, inactive, restored,
future, and durable-reset thread views receive the same profile. Final layout
composition, the wide sidebar, status alignment, generic/Assistant row
clipping, and sticky prompts now consume that profile under G11.D2. Duplicated
pill render/hit-test geometry is removed by G11.D3; the cached replacement
binds the semantic model, rectangle, and exact render environment.

## Historical Follow And Projection Evidence

The G11.A transition fixture recorded the pre-G11.B visibility and count
separately:

| Current input | Current follow/baseline result | Current pill |
|---|---|---|
| effective `ScrollUp` | away; snapshots `len(items)` | visible, count-free until append |
| `ScrollToTop` | away; zero baseline sentinel | hidden dead zone |
| item or search jump | away; snapshots `len(items)` | visible |
| append while away | item-length delta becomes unseen count | one/many label |
| tool grouping or projection collapse | item-length delta changes without a user-visible append fact | label may regress |
| truncation | returns to follow but can retain a stale baseline | hidden by follow |
| reset | follow and baseline both clear | hidden |
| empty or non-scrollable top jump | manufactures away with zero baseline | hidden |
| in-memory thread restore | restores the complete `ChatView` presentation object | preserves its current state |
| durable restored-away sidecar | restores follow/offset only; baseline is zero | hidden dead zone |

G11.B has now replaced this table in production. `ChatView` owns a monotonic
live append epoch and explicit baseline validity; every nonempty away state
publishes a semantic jump action, while hydration and projection replacement
cannot create a false unseen count. The completed contract remains in
[`scroll-follow-pill.md`](../plans/g11-tui-frame-integrity/scroll-follow-pill.md),
with closeout evidence in
[`g11-b-scroll-follow-pill.md`](../history/tui/g11-b-scroll-follow-pill.md).

## Reproduction

Run the focused characterization:

```bash
go test ./internal/tui -run '^TestG11A' -count=1
go test ./internal/tui -run '^TestG11A' -race -count=1
```

Run the replacement follow-state contract:

```bash
go test ./internal/tui -run '^TestG11B' -count=1
go test -race ./internal/tui -run '^TestG11B' -count=1
```

Run the replacement App/Markdown projection contract:

```bash
go test ./internal/tui -run '^TestG11D1' -count=1
go test -race ./internal/tui -run '^TestG11D1' -count=1
```

Run the replacement final-frame/sidebar/status contract:

```bash
go test ./internal/tui -run '^TestG11D2' -count=1
go test -race ./internal/tui -run '^TestG11D2' -count=1
```

Run the replacement shared pill-geometry contract:

```bash
go test ./internal/tui -run 'Test(G11D3|StickyHeaderKeepsPill|JumpToBottomPill|G11B)' -count=1
go test -race ./internal/tui -run 'Test(G11D3|StickyHeaderKeepsPill|JumpToBottomPill|G11B)' -count=1
```

Run the repository and documentation gates before accepting the slice:

```bash
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

The focused test file is
[`g11a_frame_integrity_characterization_test.go`](../../../internal/tui/g11a_frame_integrity_characterization_test.go).
G11.D2 moves the wide table/sidebar replacement assertion under the
`TestG11D2` prefix and adds the final-frame matrix/source gate in
[`display_cell_g11d2_test.go`](../../../internal/tui/display_cell_g11d2_test.go).
G11.D3 adds accepted-width/glyph/tab/control/cache/hitbox/source-owner evidence
in
[`display_cell_g11d3_test.go`](../../../internal/tui/display_cell_g11d3_test.go)
and sticky-final-row evidence in
[`sticky_header_geometry_test.go`](../../../internal/tui/sticky_header_geometry_test.go).
The remaining G11.A tests stay historical evidence until their named
replacement slices close.
