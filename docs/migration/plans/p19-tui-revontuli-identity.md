# P19 Revontuli TUI Identity

**Status:** historical
**Created:** 2026-07-23
**Completed:** 2026-07-25
**Last updated:** 2026-07-25 (P19.3.5 and program closeout)

> **Ownership:** historical contract, ordered slice boundaries, acceptance
> gates, and rollback evidence for the completed P19 TUI visual identity
> ("Revontuli") program

Root [`migration/PLAN.md`](../PLAN.md) owns execution order and slice state.
Current TUI ownership belongs in
[`architecture/tui/README.md`](../../architecture/tui/README.md).
The design source of truth — concept, tokens, glyph inventory, component and
dynamics specs, mascot spec, feasibility review, and rendered demos — is
[`p19-tui-revontuli-design/README.md`](p19-tui-revontuli-design/README.md)
with HTML demos under [`p19-tui-revontuli-design/demo/`](p19-tui-revontuli-design/demo/index.html).

## User Outcome

The TUI stops presenting another product's identity. The interface ships its
own aurora visual system (Polar Night / Daybreak themes), its own mascot (Eno
the aurora fox), and its own brand glyph (`✦`), while layout, interaction,
permission semantics, streaming cadence, and the measured 30fps render ceiling
remain unchanged. Runtime theme switching applies completely — to every
existing and future view — and never fights the user's explicit choice.

## Review Convergence (2026-07-23)

A design review cut this program from the original 22 slices to the 14 below.
Recorded corrections, all binding on implementation:

1. **Removed from scope:** status-line productization (segment registry,
   `tui.statusLine` config, `/statusline` dialog, context meter),
   `StatusLineFunc` wiring (factual error: the hook is **already** reachable
   through Go `tui.Config`, no user-facing gap), toast lifecycle changes,
   resize optimization, debug overlay, and dead-`renderHeader` cleanup.
2. **Theme switching must cover more than `App.styles`:** inactive thread
   views, dialogs/panels, views created after the switch, and the frozen
   per-item chat render cache (cache keys must mix in a theme generation).
3. **`/theme` vs `EINO_THEME` precedence:** the environment variable decides
   the startup theme only; an explicit user `/theme` wins for the rest of the
   process lifetime.
4. **Daybreak contrast:** the originally claimed ≥4.5:1 ratios against the
   design's painted `bg0` swatch were wrong
   (teal 3.12:1, sky 3.27:1, violet 4.21:1). Daybreak accent values are
   **provisional** and must pass an automated design-reference contrast gate
   added by the palette slice. This is not a claim about the user's terminal
   background, which the TUI does not own.
5. **Mascot face:** the HTML mock paints `bg0`, but the real TUI does not own
   the terminal's global background. Eno's face cells must not fake a cutout
   with an `enoFace = bg0` hex; they render as terminal-default negative
   space.
6. **Two designed elements were missing from the plan** and are now slices:
   the user-message `▎` panel and the welcome gradient wordmark.
7. **G8 (new, outside P19):** the production notification adapter mutates the
   toast stack outside the Bubble Tea event loop and has no expiry wake-up —
   tracked in [`REMAINING.md`](../REMAINING.md).

## Delivery State (2026-07-25)

All fourteen visual slices landed independently. The combined source candidate
is preserved only as extraction provenance and owns no current behavior.
P19.0.0-P19.3.2 established one App-owned theme identity, semantic palette,
mascot/glyph/spinner/Markdown/tool-badge system, and theme-only static-color
boundary. P19.3.3-P19.3.4 added the mode-reactive composer border and the
theme-owned user-message panel. P19.3.5 completed the program with the static
truecolor welcome wordmark and flat reduced-color/no-color fallbacks.
Current behavior belongs in
[`architecture/tui/README.md`](../../architecture/tui/README.md); delivery
evidence belongs in
[`migration/history/tui/p19-3-5-welcome-wordmark.md`](../history/tui/p19-3-5-welcome-wordmark.md).
Review corrections that every extracted slice must preserve:

- explicit `/theme` values are validated before any active-theme or component
  state changes; invalid startup values fall through to the next valid source;
- Plan and AskUserQuestion dialogs consume semantic palette styles instead of
  constructing ANSI-256 colors, and the color-source gate covers literal
  `lipgloss.Color` constructors as well as hex values;
- Unicode truncation and dialog editing use rune/display-width boundaries;
- table inline markdown is prepared once per cell per render and shared by
  sizing, wrapping, horizontal rendering, and vertical fallback; and
- provider output remains byte-for-byte canonical. The adjacent replay fix
  delegates indexed assistant-part concatenation to public Eino
  `schema.ConcatMessages` rather than rewriting whitespace or inventing a
  second merge rule; it has landed independently and is not a P19 dependency;
  and
- ACP tool-call `rawInput` projection has likewise landed independently and
  must not be coupled to a visual-identity rollback; and
- chat downward scrolling now clamps at the exact follow offset, independently
  preventing a top-padded blank intermediate frame. P19 must preserve that
  behavior but does not own or roll it back.

Each P19 PR extracted only its own slice from the combined candidate and proved
its standalone compile/test state. P19.0.0-P19.0.1 retained an empty golden
diff; appearance-changing slices closed the 80×24, 150×24, compact, both
truecolor themes, ANSI, no-color, and reduced-motion matrix. The final P19.3.5
closeout repeated the composed TUI and repository gates.

## Intake Baseline And Closed Gaps

The following failures were reproduced at intake and are now closed by the
completed program:

- **G6 — borrowed identity + mascot rendering bug.** Brand color is Claude
  orange `#D77757` (`internal/tui/styles.go` notes the inheritance), the
  mascot is Clawd ported pose-for-pose (`internal/tui/mascot.go` poses), the
  identity glyph is Claude's `✻`, and the mascot fill paints orange on
  `#000000` background, rendering as black rectangles on non-black terminals.
- **G7 — theme system was half-applied.** P19.0.0-P19.3.5 propagate
  explicit runtime choices through captured component/thread styles,
  invalidate frozen and nested render caches by theme identity, remove
  palette-bypassing static colors, and apply the semantic palette to the
  composer mode border, user-message panel, and capability-aware welcome
  wordmark. The composed visual matrix closes the program-level gap.

## Decision

P19 is a mixed adoption program, per element:

| Element | Decision |
|---|---|
| Layout, band structure, component structure | `preserve` |
| 30fps stream batch + render cache stack | `preserve` |
| `theme.go` token architecture | `adapt` — extend tokens, remove palette-bypassing hex |
| Clawd mascot, `✻` glyph, spinner glyph cycle | `reject` — replaced project-native |
| Shimmer motion | `adapt` — recolor to aurora, slow to 2.4s |
| Mode-reactive composer border | `project-native` |
| User `▎` panel, welcome gradient wordmark | `project-native` (design-defined) |
| Status-line productization, toast lifecycle, resize/debug tooling | `defer` — out of this program per review |
| Thin-bar cursor, chat-history gradients, bubbletea v2, sync output | `defer`/`reject` per design §11 |

## Scope And Non-Goals

P19 owns the TUI visual identity contract in `internal/tui/`: theme palettes
and semantic tokens, complete theme propagation, mascot art/animation, glyph
set, spinner presentation, composer presentation, user-message treatment,
welcome wordmark, dialog/sidebar coloring, and markdown palette mapping.

P19 does not:

- change layout rects, state machine, key bindings, permission semantics,
  engine events, streaming batching, or scrolling behavior;
- touch headless CLI, ACP, or MCP output contracts (visual skin is TUI-only);
- add status-line configurability, a context meter, toast lifecycle changes,
  resize optimization, or debug tooling;
- add a shell-command statusline, thin-bar cursor, chat-history gradients,
  synchronized output, or a bubbletea/bubbles v2 migration;
- change transcripts, persistence, or any durable schema.

## Program Invariants

1. No interaction contract changes: identical Update message flow, key
   handling, dialog stack order, and permission prompts.
2. The 30fps stream batch ceiling and the per-item/viewport/markdown render
   caches keep their current complexity; `performance_bench_test.go` stays
   green without threshold edits.
3. Theme resolution order is the first valid value from `EINO_THEME` → config
   → auto-detect **at
   startup**; an explicit `/theme` then wins for the process lifetime.
   Legacy `dark`/`light` names keep resolving via aliases, and an unsupported
   explicit name fails without mutating active presentation state.
4. ANSI-16 themes receive full fallbacks (aurora teal→cyan, sky→blue,
   violet→magenta; no gradient, no shimmer interpolation).
5. Reduced-motion keeps disabling cursor blink, mascot ticks, and spinner
   frame advancement; new animations gate on the same flag.
6. Mascot rendering uses foreground tones for body/outline and
   terminal-default negative space for face cells — never a background fill
   or a hardcoded face hex.
7. Every new animation names its tick and cache key; per-frame work stays on
   already-repainted lines (spinner line, welcome view while idle).
8. Golden-file and pinned-string tests are updated in the same PR as the
   rendering change they pin.

## Ordered Slices

Fourteen atomic slices in four tracks. Every slice names exactly one
observable contract, its deterministic acceptance test, and its rollback
boundary. "Zero-diff" slices must produce no golden-file change at all;
"golden" slices update exactly the pinned fixtures listed. `A ← B` means A
starts only after B merges.

### Track A — theme plumbing (G7 first, or every later slice half-applies)

**P19.0.0 Complete theme propagation + precedence — Complete** — depends: none.
Make every style consumer read the live theme: `ChatView` (no hardcoded
`defaultStyles()`), inactive thread views, dialogs/panels, and views created
after a switch (construct-time style injection from one source of truth).
Mix a theme generation into the frozen per-item chat render cache key so
switched themes invalidate frozen items. Resolve precedence: `EINO_THEME`
decides the startup theme only; an explicit `/theme` wins for the process
lifetime. No palette value changes.
- Observable contract: at runtime, `/theme` restyles chat history (including
  frozen items), inactive thread views, and one open dialog without restart;
  restarting with `EINO_THEME` set honors it only until the user picks a
  theme explicitly.
- Tests: propagation test (chat + dialog + thread view restyle at runtime);
  frozen-cache invalidation test (theme switch re-renders a frozen item);
  precedence test (env startup vs explicit `/theme`); zero golden diff.
- Rollback: revert plumbing and cache-key change; no fixtures touched.
- Delivery: `App` is the single live style owner; `ChatView` and future thread
  views receive styles at construction; existing component/thread styles
  propagate in place; explicit names are validated independently of startup
  sources; focused propagation, precedence, and cache tests pass with no
  golden-file change.

**P19.0.1 Token schema extension (zero-diff) — Complete** — ← P19.0.0.
Add `auroraSky`, `selection`, `enoBody`, `enoOutline` to `colorPalette` and
`stylesFromPalette`, mapped from existing values so no renderer output
changes. `enoFace` is deliberately **not** a hex token (invariant 6). No
consumer may reference the new fields in this slice.
- Observable contract: none (schema only).
- Tests: palette assembly unit test asserting the four fields exist and are
  non-empty in all six themes; zero golden diff.
- Rollback: delete the fields.
- Delivery: one provisional mapper reuses existing semantic values, including
  ANSI border fallbacks for selection; all six palettes assemble the four
  fields, `stylesFromPalette` wires them into `Styles`, and no renderer
  consumes them.

**P19.0.2 Polar Night / Daybreak values + aliases + contrast gate — Complete** — ← P19.0.1.
Replace `dark`/`light` palette values with the Polar Night / Daybreak sets
(design §4), alias `dark`→`polar-night` and `light`→`daybreak` for one
release, retone `aubergine`/`snowy` onto the new token names (kept, not
dropped), and update the ANSI palettes to the fallback mapping. Daybreak
accent values are **provisional**: this slice adds an automated design-swatch
test (accent and inactive text-side token on the documented `bg0` reference
swatch ≥ 4.5:1 for both truecolor themes) and adjusts the provisional values
until the gate passes. Primary terminal-default text and the user's actual
terminal background remain outside this palette-owned test; the TUI cannot
truthfully guarantee a global WCAG ratio without painting both sides.
- Observable contract: default dark theme renders aurora colors; old theme
  names still resolve; the contrast gate runs in CI.
- Tests: alias resolution; ANSI palettes contain no truecolor values; the new
  design-reference contrast test passes for both themes; golden refresh across the suite,
  reviewed file by file.
- Rollback: data-only revert of the palettes; aliases work both directions.
- Delivery: canonical and legacy names resolve through explicit, environment,
  config, palette, and style paths; all six palettes have explicit mappings;
  dark/light ANSI use bright/standard cyan-blue-magenta fallbacks; the lowest
  canonical design-swatch result is 4.72:1; the full TUI golden suite passes
  without fixture changes because the owned text goldens normalize ANSI color.

### Track B — mascot (needs the project-owned eno values from P19.0.2)

**P19.1.0 Eno static art + negative-space render — Complete** — ← P19.0.2.
Replace the pose map with the 15×6 Eno segment set (design §8,
`demo/mascot.html` pose sheet: default, look-left, look-right, blink, happy,
celebrate, sleep). Body/outline use the `enoBody`/`enoOutline` tones; face
cells (eyes, nose) render with **terminal-default styling** (negative space),
never a hardcoded hex and never a background fill. Welcome box sizing follows
the new grid. Existing click animations keep working, driving the new poses
(celebrate replaces arms-up). No idle timer yet.
- Observable contract: all four responsive tiers retain an owned snapshot;
  Eno appears in the condensed/full tiers while too-small/compact keep their
  established fallback, the face reads as cutouts on dark, light, and unknown
  terminal backgrounds, and click animations play.
- Tests: pinned-string pose tests (replacing `mascot_welcome_test.go`
  strings); welcome goldens at all four tiers; a styled-output test proving
  face cells carry no explicit color and no background fill; reduced-motion
  static pose.
- Rollback: isolated to `mascot.go`, `welcome.go`, and their fixtures.
- Delivery: all seven pose strings and the unknown-pose fallback are pinned at
  15×6; all six themes use body/outline foreground tones with no mascot
  background escape and a reset before every face glyph; `celebrate` preserves
  the two click sequences and 60ms frame cadence; reduced motion stays static;
  four-tier `eno_welcome.golden` output owns the enlarged welcome layout.

**P19.1.1 Idle blink/glance timer — Complete** — ← P19.1.0.
Add one `App`-owned gated timer chain (pattern: `ensureSpinnerTick`) that fires
only while the welcome state and a mascot-bearing tier are visible, motion is
allowed, and no animation owns the viewport. A generation-bearing idle message
must make uncancellable `tea.Tick` results inert after a state change, resize,
click, or reschedule. Delay and sequence choice use injected deterministic
sources: each accepted idle beat waits 3–5s, then chooses a blink
(`●`→`▄`, three 60ms frames = 180ms) or an occasional 420–540ms left/right
glance. The existing click sequence may preempt idle animation; idle never
preempts a click. When a sequence ends, at most one next idle delay is armed.
- Observable contract: the mascot blinks and glances while idle on the
  welcome screen, click interaction remains immediate, and no timer advances
  after chat transition, mascot-hiding resize, or reduced-motion selection.
- Tests: one-chain scheduling; exact 3s/5s jitter bounds; deterministic
  blink/glance direction and frame lengths; click preemption; stale-generation
  rejection across state/resize/reschedule; sequence completion rearms once;
  reduced-motion and hidden tiers produce no tick; focused race run.
- Non-goals: no long-idle sleep pose, tool-success happy pose, global idle
  tracker reuse, persistence, extra goroutine, or animation outside welcome.
- Rollback: remove idle message/state and scheduling calls; P19.1.0 poses and
  click animation remain.
- Delivery: `App` owns one generation-bearing 3–5 second delay chain with
  injected random and delay sources. Accepted beats choose a three-frame blink
  or a bounded seven-to-nine-frame left/right glance. Resize, click, first
  submission, hidden tiers, and reduced motion invalidate or suppress the
  chain; stale messages cannot clear a newer schedule. Click preemption reuses
  the idle sequence's pending 60ms frame tick so pointer input stays consumed
  without doubling animation speed, and sequence completion rearms exactly one
  delay.

### Track C — glyphs and spinner

**P19.2.0 Glyph swap — Complete** — ← P19.0.2.
System prefix `✻` → `✧`, assistant prefix `✦` in aurora teal (`chat.go`),
status-bar model icon `⬡` → `✦`.
- Observable contract: the two glyph weights render as specified.
- Tests: chat and status goldens refreshed.
- Rollback: revert the glyph constants.
- Delivery: `styles.go` owns the outline/filled identity constants;
  finalized and streaming assistant renderers plus the model status icon use
  `AssistantPrefix`, while system/help messages keep the semantic system style.
  Status padding now uses the conservative emoji-aware width contract so the
  Dingbats star cannot overflow the owned layout width.

**P19.2.1 Spinner breathing pulse — Complete** — ← P19.2.0.
Replace the 12-frame glyph ping-pong with the breathing `✦` opacity pulse
(960ms), driven by the existing 120ms tick; verbs and elapsed/token text
unchanged; reduced-motion keeps the static glyph.
- Observable contract: spinner line animates as specified with no new timers.
- Tests: frame sequencer unit test incl. reduced-motion; spinner goldens.
- Rollback: restore the frame list.
- Delivery: `spinner.go` reuses the centralized filled identity glyph and
  derives one symmetric eight-tick foreground pulse from the existing counter.
  Truecolor themes interpolate from `Subtle` to the caller's semantic peak
  while ANSI themes choose between the same two palette colors. The main
  spinner peaks at `AssistantPrefix`; stalled and inline task/tool icons retain
  their caller-owned peak styles. Reduced motion renders the static peak glyph.
  Verbs, shimmer, waiting text, effort, elapsed time, token usage, ordering,
  and tick ownership are unchanged.

**P19.2.2 Aurora shimmer + stalled recolor — Complete** — ← P19.2.1.
Extend `RenderShimmerText` from the 2-stop amber interpolation to the 3-stop
teal→sky→violet gradient at a 2.4s period; waiting state → sky; stalled
state → `stalled` token; ANSI themes fall back to flat cyan with no
interpolation.
- Observable contract: shimmer sweep renders the aurora gradient on the
  spinner verb only.
- Tests: color interpolation unit tests including the ANSI path (no truecolor
  escapes); spinner goldens.
- Rollback: revert `RenderShimmerText` and the two state colors.
- Delivery: one deterministic helper owns the 2.4-second sine phase shared by
  thinking, responding, and tool-use modes after the preserved thinking delay.
  The verb keeps its positional three-cell glimmer while the highlight
  interpolates from `AssistantPrefix` through `AuroraSky` to the permission
  violet foreground. Both ANSI themes return their flat cyan
  `SpinnerShimmer` semantic without constructing a truecolor value. Reduced
  motion renders the whole verb through `AssistantPrefix`; early waiting uses
  `AuroraSky`, and full stall uses `SpinnerStalled`. The fixed-star pulse,
  120ms timer, inline icons, text, suffix ordering, layout, and runtime owners
  are unchanged.

### Track D — hardcoded-color purge + composer border

**P19.3.0 Markdown onto palette — Complete** — ← P19.0.2.
Replace the ANSI-256 indices in `markdown.go` with palette tokens: H1 teal
bold, H2 sky, H3 violet, inline code sky on `element`, quotes with teal `▎`,
HR in `border-subtle`; ANSI themes map to their 16-color equivalents.
- Observable contract: markdown headings/code/quotes render in aurora tokens.
- Tests: markdown goldens at polar-night, daybreak, and one ANSI theme.
- Rollback: revert style config.
- Delivery: `Styles` now carries a private canonical theme identity and the
  independent `element` surface. Finalized assistant messages, compatibility
  streaming messages, and Plan dialogs pass that identity into
  `StreamingMarkdown`; no package-global active theme exists. Renderer pooling
  keys width plus immutable palette identity, while nested stable/full caches
  invalidate before their exact-match fast path and retain finalized-source
  lifecycle state. H1 uses brand teal, H2 sky, H3 violet, H4-H5 inactive,
  H6 subtle, inline code sky on `element`, quotes a teal `▎` with inactive
  content, and rules `border-subtle`. Dark ANSI selects an ANSI-16 profile and
  disables Glamour's terminal256 Chroma path; the cached hot path remains
  zero-allocation.

**P19.3.1 Tool category badges onto element surface — Complete** — ← P19.3.0.
Replace the dark-hue badge backgrounds (`tools.go` category map) with
category-colored text on the neutral `element` surface: Bash teal, file ops
green, search sky, agent/plan violet, MCP teal, web sky.
- Observable contract: badges render as specified.
- Tests: tool-history goldens per category.
- Rollback: revert the badge style map.
- Delivery: one shared `toolNameStyled` path maps shell/MCP to brand teal,
  file/To-Do to success green, search/Task/web to sky, and Agent/Plan to
  permission violet, then applies the palette-owned `element` background.
  Unknown dynamic tools retain the plain `ToolName` style. Focused history
  output pins Polar Night, Daybreak, and dark ANSI SGR state, all current
  aliases, exact display width, and ANSI-16 containment without changing
  interaction, transcript, or replay ownership.

**P19.3.2 Error + mode badge colors onto tokens — Complete** — ← P19.3.1.
Move `error_display.go` title color and any remaining inline mode-badge hex
onto `warning`/`error`/mode tokens. Running tool status dot amber → teal
(amber becomes warning-only).
- Observable contract: no literal hex or `lipgloss.Color("…")` constructor
  remains outside `theme.go` (source gate).
- Tests: a Go test scanning production TUI files for literal color sources;
  affected goldens.
- Rollback: revert the color sources.
- Delivery: `defaultStyles` now aliases the canonical Polar Night theme
  pipeline; a recursive production-source gate makes `theme.go` the only
  static color owner. Informational/running state uses brand teal, warning
  state and shell mode use amber, errors/bypass use red, and Plan uses sky.
  Dialogs, pickers, permission risks, search/selection surfaces, and truecolor
  Chroma syntax reuse existing semantic tokens. Polar Night, Daybreak, and
  dark-ANSI SGR output is pinned without changing runtime, interaction,
  permission, transcript, replay, or cache ownership.

**P19.3.3 Mode-reactive composer border — Complete** — ← P19.3.2.
`renderEditor` picks the border color from the current mode: teal default,
sky plan, amber shell, red yolo, reusing the existing per-frame style
application with no new allocations in the layout path.
- Observable contract: composer border tracks mode on the next frame after a
  mode switch.
- Tests: border style per mode (4 cases); layout benchmark shows no new
  allocation; goldens.
- Rollback: restore the single border style.
- Delivery: `App.renderEditor` selects the current semantic foreground on each
  render with shell taking precedence over Plan/bypass, matching the header
  badge. Polar Night, Daybreak, and dark-ANSI exact SGR output is pinned;
  compact, standard, wide, and reduced-motion geometry remains unchanged.
  The pre/post `BenchmarkAppViewExplicitLayout` result remains 402 allocs/op.

**P19.3.4 User-message `▎` panel — Complete** — ← P19.3.2.
Render user messages as a soft `userBg` panel with a teal `▎` left bar
(design §6.3), replacing the brand-colored `●` prefix; respects the palette
and ANSI fallback.
- Observable contract: user messages render as the panel in chat history and
  while scrolling; other message types unchanged.
- Tests: chat goldens with user messages at both themes + ANSI; long/wrapped
  user message panel integrity.
- Rollback: revert the user-message renderer.
- Delivery: `UserMessage.Render` repeats one semantic brand `▎` on every
  visible row and explicitly reapplies `UserMessageBlock` to the text run so
  the nested bar reset cannot punch a background hole. Polar Night and
  Daybreak use `userBg`; dark ANSI stays background-free. The scrolling sticky
  prompt uses the same bar and surface. The existing `width-4` wrap budget,
  finished-item cache identity, raw content, selection, and scroll transitions
  are unchanged, and the full App benchmark remains 402 allocs/op.

**P19.3.5 Welcome gradient wordmark — Complete** — ← P19.3.2.
Render the welcome title "Eino Agent" with the teal→sky→violet horizontal
gradient (truecolor themes; ANSI and reduced-color fall back to flat teal),
inside the existing welcome card layout.
- Observable contract: the welcome title shows the aurora gradient at all
  four responsive tiers; fallbacks render flat.
- Tests: welcome goldens per tier; ANSI/no-color fallback test.
- Rollback: revert the title renderer.
- Delivery: `renderWelcomeWordmark` consumes the current App `Styles` snapshot
  and terminal capability on every render. Polar Night and Daybreak interpolate
  Header brand → `AuroraSky` → `DialogTitle` permission color across the title.
  ANSI, reduced-color, and no-color paths reuse the flat Header style; no
  package-global theme identity or second palette owner was added. Compact,
  condensed, full, and wide welcome tiers preserve their visible width and
  existing normalized golden. Exact truecolor SGR output and runtime theme
  restyling are pinned; the full App benchmark remains 402 allocs/op.

## Promotion Gates

```text
P19.0.0 → P19.0.1 → P19.0.2 ─┬─→ P19.1.0 → P19.1.1              (mascot)
                             ├─→ P19.2.0 → P19.2.1 → P19.2.2    (glyphs/spinner)
                             └─→ P19.3.0 → P19.3.1 → P19.3.2 ─┬─→ P19.3.3 (composer border)
                                                              ├─→ P19.3.4 (user ▎ panel)
                                                              └─→ P19.3.5 (welcome gradient)
```

- One `In progress` slice at a time; Track A is the critical path.
- A slice promotes when its own acceptance test passes with all Makefile
  gates green; zero-diff slices additionally show an empty golden diff.
- Any slice failing its gate returns to `Blocked` with evidence; it never
  merges partial.

## Required Verification (every slice)

`make fmt`, `make lint`, `make test`, `make build`, `make lint-new`,
`make docs-check`, manifest validation, and `git diff --check`. Visual QA at
80×24, 150×24, compact mode, both truecolor themes, one ANSI theme, and
reduced-motion enabled. Affected `manifest.yaml` entries (WelcomeV2,
Spinner.*, theme commands) receive status/note updates in the same PR as
their behavior change.

## Documentation

Per-slice updates: [`architecture/tui/`](../../architecture/tui/README.md)
owning documents whose facts changed, `STATUS.md` verified facts, this
contract's slice states, and one `history/` record at program closeout. The
design package remains a `reference-snapshot`; merged implementation truth
moves to the owning architecture documents, `STATUS.md`, and closeout history.
