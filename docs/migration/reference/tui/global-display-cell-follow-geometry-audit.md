# Global Display-Cell And Follow Geometry Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-26

> **Ownership:** source-backed evidence for the TUI-wide display-cell and
> scroll-follow failures, relevant local-reference comparison, and the G11
> adoption recommendation; execution order belongs in
> [`migration/PLAN.md`](../../PLAN.md)

## Decision

Use a `combine` decision:

- preserve the current Goldmark-derived semantic table runs, incomplete-table
  lifecycle, raw transcript bytes, and responsive rectangle allocation;
- adapt Claude Code Ripe's separation of pill visibility from unseen-message
  count and its token-owned rich table cells;
- adapt Codex's rich inline table spans;
- adapt Crush's rule that measurement, drawing, and cache identity consume the
  same selected width method;
- introduce one project-native, immutable App-selected
  `DisplayCellProfile`; and
- reject raw-cell Markdown reparsing, post-render border repair, isolated emoji
  range patches, and Eino/Eino-ext involvement in terminal presentation.

The accepted design is split between two presentation-state owners:

1. `App` selects and propagates terminal display-cell policy.
2. `ChatView` owns follow position, unseen baseline, and whether the jump pill
   exists.

The detailed accepted contracts are indexed by the
[`G11 frame-integrity plan`](../../plans/g11-tui-frame-integrity/README.md).

## Snapshot Boundary

| Source | Snapshot | Question answered |
|---|---|---|
| Eino-Agent | `022ef9b3fb60` | Current semantic tables, width owners, final layout, sidebar, pill rendering, and hitbox behavior; production source is unchanged by G11.A |
| Claude Code Ripe | `4b9d30f79532` | Parsed table tokens and scroll-position-owned pill visibility |
| Crush | `2af939d8e900` | Active screen width method in drawing and cache identity |
| Codex | `66bd101fff6f` | Rich inline spans retained inside table cells |

The reference commits are local evidence snapshots. This report makes no claim
about floating upstream behavior.

## Observable Question

How should Eino-Agent preserve one physical terminal-cell grid across rich
Markdown tables, the main/sidebar split, status and chat chrome, and the
Jump-to-bottom overlay, while keeping the pill reachable whenever the user has
left follow mode?

Success requires both:

- semantic content and every visual border use one selected display-cell
  contract through measurement, clipping, padding, drawing, caching, and mouse
  geometry; and
- `follow=false` makes the pill visible independently of whether a new message
  has arrived.

## Verified Current Behavior

### Rich table semantics already exist

[`tableCell`](../../../../internal/tui/table_render.go#L24) stores a plain
semantic projection and structured runs. [`semanticRuns`](../../../../internal/tui/table_render.go#L895)
derives bold, italic, code, strike, image, and link meaning from Goldmark AST
nodes. [`renderTableCell`](../../../../internal/tui/table_render.go#L992)
styles each run independently, so a cell need not be wholly bold or wholly
code.

The earlier dirty-worktree patch was deliberately not restored because it
reparsed raw cell Markdown and selected independent theme/width owners. The
current semantic path supersedes that rejected mechanism. The remaining G11
work must protect this current behavior rather than reintroduce the old parser.

### Final geometry has multiple width owners

[`defaultWidthProfile`](../../../../internal/tui/display_cell.go#L21) owns
Markdown-table sizing, wrapping, truncation, and padding. The same file retains
[`terminalLayoutSafetyWidth`](../../../../internal/tui/display_cell.go#L36) as
a separate non-table overflow estimate.

Final bands and main/sidebar columns instead use `x/ansi` through
[`renderLayoutBands`](../../../../internal/tui/layout.go#L110) and
[`fitLayoutColumnLine`](../../../../internal/tui/layout.go#L298). The wide
sidebar performs another direct `x/ansi` truncation in
[`renderWideSidebar`](../../../../internal/tui/responsive_sidebar.go#L18).
The pill renderer and its App hitbox both select `lipgloss.Width`, duplicating
their text and geometry.

A focused diagnostic against current source produced these cell counts:

| Cluster | `x/ansi` | table `WidthProfile` | non-table safety |
|---|---:|---:|---:|
| `🖥` | 1 | 1 | 2 |
| `⚙` | 1 | 1 | 2 |
| `🏷` | 1 | 2 | 2 |
| `✦` | 1 | 1 | 2 |

When the terminal draws one of these clusters differently from the method that
padded the row, later table borders and the sidebar separator begin at
different physical columns. A conservative overflow guard cannot solve exact
alignment if final composition uses another method.

G11.A extends that diagnostic through CJK, Indic, VS15/VS16, bare and repeated
warning symbols, ZWJ, paired and lone regional indicators, combining
sequences, ANSI SGR, and OSC 8. Its independent oracle does not use any of the
three measured production methods as truth. A 180-column App fixture proves
that semantic-table rows containing the table profile's wide `🏷` and Indic
cases reach the sidebar separator at a different oracle column from rows
composed only by the final `x/ansi` owner. This is a deterministic project-grid
failure, not evidence about glyph pixels in an arbitrary terminal/font.

### Pill visibility no longer conflates two facts

At the audit baseline, `ScrollUp` snapshotted current item count while
`ScrollToTop` could set `follow=false` without establishing that sentinel.
Rendering and hit testing both required `scrollAwayCount > 0`, so the reachable
state `follow=false, scrollAwayCount=0` hid the only recovery action.

G11.B has replaced that implementation. [`ChatView`](../../../../internal/tui/chat.go)
now owns follow, a live append epoch, the first departure baseline, and
explicit baseline validity in one value. [`ChatView.Render`](../../../../internal/tui/chat.go)
and [`App.pillClickHits`](../../../../internal/tui/app.go) consume one semantic
pill model. Invalid or zero unseen baselines select `Jump to bottom`; they do
not suppress the action. Lip Gloss width and centering remain a geometry gap
for G11.D3.

### G11.A evidence decisions

The test-only characterization closes the inputs that previously blocked
production API design:

- active terminal tables remain literal, while stable-prefix promotion,
  `Finalize`, and resize preserve the current Goldmark semantic runs for the
  exact `` `eino-agent` `` / `**Codebase**` and mixed-run fixtures;
- SGR and OSC 8 can currently be emitted between a base scalar and a combining
  continuation when one visible EGC crosses semantic runs;
- at the G11.A baseline, `ScrollToTop`, empty/non-scrollable top navigation,
  and durable restored-away state reproduced
  `follow=false, scrollAwayCount=0`, so both the rendered pill and hitbox
  disappeared;
- item/search jumps established the old item-count sentinel, while grouping,
  truncation, and projection restoration proved that item length was not a
  truthful append epoch; G11.B has replaced those assertions;
- one deterministic default profile is selected for internal grid integrity;
  terminal/font-specific variants, terminal-name guessing, and hidden probing
  remain rejected; and
- the semantic run containing an EGC's first visible scalar owns the future
  cluster-wide style/link resolution.

The exact fixture matrix, cache identity consequences, and reproduction
commands are owned by
[`g11-a-frame-integrity-characterization.md`](../../verification/g11-a-frame-integrity-characterization.md).

## Reference Evidence

| Behavior | Claude Code Ripe | Codex | Crush | G11 consequence |
|---|---|---|---|---|
| rich table cells | `MarkdownTable.formatCell` formats parser tokens and measures their plain projection | table cells retain rich inline spans and style code separately | not selected for table semantics | Preserve the current Goldmark semantic-run owner; do not parse raw cell strings |
| width ownership | shared `stringWidth` is used by table sizing and padding | Ratatui owns span width and drawing | the active screen width method sizes the draw cache and performs drawing | One App-selected profile must reach final composition and cache identity |
| pill visibility | viewport position determines visibility; count zero explicitly means `Jump to bottom` | no selected mechanism | no selected mechanism | Follow position controls existence; unseen count controls only label text |
| terminal variability | deterministic library policy | renderer-selected Unicode width | screen-selected width method | Pin one deterministic project profile first; expose no runtime guessing or content mutation |

### Claude Code Ripe

`.reference/claude-code-ripe/src/components/MarkdownTable.tsx` formats the
parser-owned token list for each cell before width allocation. Its
`FullscreenLayout.tsx` derives `pillVisible` from scroll position and passes an
independent `newMessageCount`; a count of zero intentionally renders
`Jump to bottom`.

### Codex

`.reference/codex/codex-rs/tui/src/markdown_render.rs` stores multiple styled
spans in a table cell and pushes inline code with its code style. This supports
the current Eino-Agent semantic-run direction but does not define its terminal
profile.

### Crush

`.reference/crush/internal/ui/model/chat.go` keys its decoded draw cache by the
active screen width method and computes the buffer bounds with that same
method. G11 adapts the ownership rule, not Crush's screen-buffer architecture.

## Root-Cause Classification

| Failure | Root cause | Not the root cause |
|---|---|---|
| literal `**` and backticks in the reported completed tables | the captured run used a branch before the G9 semantic-table commits | missing capability on current `master` |
| table border drift | selected width policy can disagree with the terminal and later composition selects another policy | lack of another post-render row trimmer |
| sidebar separator drift | main padding, sidebar truncation, and physical drawing do not share one profile | Agent/task runtime state |
| pill disappears | visibility is gated by unseen-baseline sentinel state | glyph width alone |
| click column can diverge | renderer and hitbox duplicate geometry and select width independently | Bubble Tea mouse transport |

## Compatibility Consequences

- Final table wrapping, sidebar truncation, status spacing, and pill centering
  may change where the old owners disagreed.
- Canonical Markdown, transcript, raw history, and user/model bytes remain
  unchanged.
- Project-owned ambiguous glyphs may adopt an explicit presentation sequence,
  but G11 does not inject variation selectors into user/model content.
- `follow=false` will always expose a jump action, including when no unseen
  messages exist.
- No engine event, durable state, permission rule, Graph topology, or
  Eino/Eino-ext dependency changes.

## Rejected Shortcuts

- restoring the deferred raw-cell inline parser;
- stripping Markdown markers before width calculation;
- calling the broad safety heuristic from table code;
- adding one more sidebar- or pill-specific width helper;
- blindly appending VS16 to user/model content;
- using one screenshot as the portable width specification;
- replacing the Bubble Tea string renderer with a screen buffer before the
  selected profile proves the current architecture; and
- moving scroll-follow presentation state into the engine.

## Evidence Limits

- The cell-count diagnostic records the current library policies, not the pixel
  behavior of every terminal/font.
- Existing PTY tests validate deterministic profile geometry; G11.A now adds
  the independent whole-frame oracle required before production semantics
  change.
- Earlier positive table tests proved semantic runs in isolation. G11.A now
  pins the reported mixed bold/code source through streaming promotion and
  `Finalize`.
