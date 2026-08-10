# Revontuli — eino-agent TUI visual redesign

**Status:** reference-snapshot
**Last reviewed:** 2026-07-24

> **Ownership:** target visual language, terminal mockups, and design rationale
> for P19; executable order and acceptance gates remain in the linked contract

Implementation is tracked as program **P19**. The executable contract and
ordered slices live in
[`p19-tui-revontuli-identity.md`](../p19-tui-revontuli-identity.md), and the
live queue is owned by [`migration/PLAN.md`](../../PLAN.md). This package is a
design input, not current architecture and not an execution tracker.

**Scope:** `internal/tui/` visual identity. Status-line productization, toast
lifecycle, resize strategy, and render diagnostics are follow-on candidates,
not part of the accepted identity scope.
**Demo:** open [`demo/index.html`](demo/index.html) — pages: overview · components · dynamics · mascot · themes

> Round-2 changes: fixed the hints-border and sidebar-tree continuity artifacts;
> explored a configurable single status line as a follow-on; specified composer
> multiline/wrap/paste states and the real block cursor; added the Dynamics page
> (stacking, streaming, animation, performance); added the feasibility review (§11)
> and the reference-borrowing list (§12).

---

## Review disposition

The visual direction is retained, but the mockups are design evidence rather
than current-source truth. Source review on 2026-07-23 found four gates that
became binding implementation acceptance criteria:

1. **Theme ownership and cache invalidation.** The intake baseline replaced
   only `App.styles`; P19.0.0 therefore requires one App-owned style source,
   propagation to every theme-aware component, and invalidation of all
   style-dependent caches.
2. **Explicit runtime precedence.** Startup resolution remains
   `EINO_THEME` → config → capability detection, but an explicit `/theme`
   selection must win for the current process, and an invalid explicit value
   must not mutate the active theme.
3. **Daybreak contrast.** The proposed light-theme accents do not meet the
   stated 4.5:1 target on `bg0`: teal is 3.12:1, sky 3.27:1, violet 4.21:1,
   success 3.07:1, warning 4.24:1, and error 4.27:1. Those values are
   provisional and are retuned in P19.0.2 behind an automated design-swatch
   contrast gate (accents ≥ 4.5:1 on documented `bg0`). The TUI does not paint
   the user's global terminal background, so this is not a universal WCAG
   guarantee.
4. **Terminal background ownership.** The HTML demo paints `bg0`, while the
   current TUI does not own the terminal's full background. Decision: the
   mascot face cells render as **terminal-default negative space** — the TUI
   does not paint a base surface, and `enoFace` is not a hex token.

The executable contract also separates visual identity from unrelated runtime
work. A configurable status line, toast expiry/layering, resize pre-warming,
dead-header cleanup, and a render-frequency overlay remain useful ideas, but
they need their own user outcome, state owner, and acceptance decision.

## 1. Why redesign

The intake baseline borrowed its identity from Claude Code (`claude-code-ripe`):
brand orange `#D77757` (`styles.go:69` says so outright), the Clawd mascot ported
pose-for-pose (`mascot.go:36`), Claude's `✻` glyph, plus palette-bypassing hardcoded
colors (`markdown.go:70-160`, `app.go:2342-2346`, `tools.go:43-66`,
`error_display.go:606`). Clawd's fill is painted on a `#000000` background
(`mascot.go:64-67`) — a real rendering bug on non-black terminals.

Reference survey: Claude Code and OpenCode own warm-orange-on-neutral; Crush owns
neon multi-hue; pi owns muted teal; codex owns terminal-adaptive cyan. An **aurora
(teal → sky → violet) system on a polar-night navy base** is unclaimed, ties to the
Finnish name *Eino* (revontulet = aurora = "fox fires"), and gives the project its
own mascot story.

## 2. Design concept

**Revontuli.** Deep polar-night surfaces, ice-colored text, one aurora gradient used
with restraint, and a small fox that lives in the welcome card.

Principles:

1. **One accent family.** Teal `#4FE3C1` is the brand; sky and violet only appear as
   semantic companions (plan/info, permission/agents). No rainbow.
2. **Motion carries personality**, but calmer: a 2.4s shimmer sweep and a breathing
   `✦` replace the busy 12-frame spinner cycle — all on the existing 120ms tick.
3. **Terminal-safe by construction.** Foreground colors only; no hardcoded hex outside
   `theme.go`; everything degrades to ANSI-16.
4. **Preserve what works.** Layout, ordering, permission semantics, the 30fps stream
   batch, and the cache stack stay exactly as they are (§7, §11).

## 3. Identity

| Element | Now | Proposed |
|---|---|---|
| Brand mark | `✻` (Claude's) | `✦` filled spark — agent voice; `✧` outline — system voice |
| Wordmark | orange "Eino Agent" text | gradient "Eino Agent" on the welcome card; no persistent header band (§6.1) |
| Mascot | Clawd (ported) | **Eno**, the aurora fox (§8) |
| Spinner | `· ✢ * ✶ ✻ ✽` ping-pong | breathing `✦` + shimmer-swept verb |

## 4. Color system

Full token tables: [`demo/themes.html`](demo/themes.html). Core values:

**Polar Night (default dark)** — surfaces `#0B0F1A / #131B2E / #1C2740`; borders
`#2C3A5E / #1E2942`; text `#DBE2F2 / #8B95AE / #566180`; aurora teal `#4FE3C1`
(brand) · sky `#7CD4F7` (plan/info) · violet `#A78BFA` (permission/agents); status
`#8ADE8A / #F2C66D / #F27E93 / stalled #C2455C`; user panel `#151D31`; diff
`#133127·#8ADE8A` vs `#3A1E2B·#F27E93`; mascot tones `#4FE3C1 / #2FA88D` with a
terminal-default (negative-space) face.

**Daybreak (light)** — same tokens on warm paper `#F5F2EC`. The initial draft
values failed the 4.5:1 design-swatch target; the accepted contract uses
retuned values guarded against the documented `bg0` reference swatch. Mascot
face cells use terminal-default negative space per the background-ownership
decision above.

**ANSI-16** — teal→cyan, sky→blue, violet→magenta; gradient collapses to flat cyan.

Semantic discipline: violet = **human-decision surfaces** and agents only; amber =
**warning/shell** only — today it also marks running tools, which makes normal work
look like a warning. Running tools become teal.

## 5. Glyph & typography inventory

Standard Unicode only (no Nerd Font). `✦/✧` agent/system · `❯` prompt+selection ·
`▎` user bar + quotes · `●` tool status (teal running, green ok, red err, subtle
pending) · `⎿` result gutter · `├─ └─ ✓ ✗ ○` trees · `▰▱` context meter ·
`╭╮╰╯` rounded borders · `⚠ ✖ ℹ ▶ ↗ ⑂` errors/affordances (unchanged).
Removed: `✻`, `✽ ✢ ✶` spinner frames, `⬡` model icon (becomes `✦`).
Cursor: reverse-video **block** over the character under it — §6.6.

## 6. Component specifications

Rendered examples: [`demo/components.html`](demo/components.html).

### 6.1 One info line, not two

The header band is **removed from the design**. Evidence: `renderHeader` is already
dead code in production (`app.go:2321` — nothing in `View()` calls it); the header
rect only hosts the transient search/message-select context bar. Keeping a designed
header would re-introduce duplication with the status line (model/thread/cwd twice).

The identity design keeps one persistent **status line**. Making its segments
user-configurable is a useful follow-on product proposal, not an accepted P19
slice (borrowing codex's model, adapted):

- Config `tui.statusLine = ["mode","thread","keys","|","model","cwd","tokens","cost","context"]`
  — ids before `|` render left, after render right. Available ids: `mode thread keys
  scroll model model-effort cwd git tokens cost elapsed tools tasks context version`.
  Unknown ids warn once.
- `context` renders the new `▰▰▱▱▱ 27%` meter; data already exists
  (`engine.GetContextUsage`), warning ≥75% / error ≥90%, hidden when unreported.
- `/statusline` opens a setup dialog with live preview + multi-select (parity with
  codex's `status_line_setup`).
- Escape hatch: the existing `StatusLineFunc` callback is already wired through
  the Go `tui.Config` construction seam and runs in `renderStatus`; it is not a
  serializable user-config key. A shell-command or config-driven full override
  remains **deferred** because it needs an explicit trust and persistence model.

### 6.2 Welcome

Layout and four responsive tiers preserved. Border teal, title aurora gradient,
system line `✧`, mascot slot holds Eno.

### 6.3 Messages

User: soft `userBg` panel with teal `▎` bar (replaces the `●` prefix). Assistant:
`✦` teal. Thinking: italic subtle. System: `✧` muted. Warnings/errors keep
`⚠ ✖ ℹ ▶`, recolored to tokens.

### 6.4 Tool groups

Status dots recolored (running teal, not amber); category badges move from dark-hue
backgrounds (`tools.go:43-66`) to colored text on the neutral `element` surface:
Bash teal, file ops green, search sky, agent/plan violet, MCP teal, web sky.

### 6.5 Spinner

Breathing `✦` (960ms opacity pulse) + shimmer-swept verb (2.4s teal→sky→violet);
waiting state sky; stalled `#C2455C`. Runs entirely on the existing 120ms tick.

### 6.6 Composer — all input states

Specified in the demo (round-1 gap): single-line, **multiline** (2-space
continuation indent from the existing prompt func, `[N lines]` indicator),
**soft-wrapped long line** (no break glyph, no line numbers; editor rect grows to
the height cap then scrolls), **paste placeholder** (`[Pasted Content N chars]`,
atomic element — existing behavior, thresholds unchanged), and four mode borders
(teal/sky/amber/red).

**Cursor:** reverse-video block over the character under it — what bubbles
`cursor.Model` already renders (the round-1 thin `▏` was a mock error). Blink 530ms;
reduced-motion already suppresses blink. Exposing a user-configurable steady
mode or blink speed would be a separate configuration change. A thin bar is
**not offered**: bubbles v1.0.0 textarea always inverts the cell under the cursor —
a bar would require forking the textarea (see §11, rejected-for-now).

### 6.7 Dialogs / toasts / hints / sidebar

Violet top rule for permission (human-decision surfaces); structure unchanged.
Toasts: neutral `element` surface + status glyph, promoted to their own layer (§7.2).
Hints: rounded frame in `border-subtle` with a corner label. Sidebar: violet agent
accents, `border-subtle` separator, tree semantics unchanged.

### 6.8 Markdown

Moves off ANSI-256 indices onto the palette: H1 teal bold, H2 sky, H3 violet, inline
code sky on `element`, quotes with teal `▎`, HR in `border-subtle`.

## 7. Dynamics

Full page with live demos: [`demo/dynamics.html`](demo/dynamics.html).

### 7.1 Frame production (existing, preserved)

`engine events → eventChan → 30fps batch (33ms / ≤64 events) → Update → View →
renderer line-diff`. The 33ms coalescing window (`streamBatchWindow`, `app.go:33`)
is the stream-event throttle; a bench test enforces the ceiling. The App's
120ms spinner tick advances spinner, streaming, thinking, and chat animation
while runtime work is active (500ms polling under reduced motion). The textarea
cursor uses Bubbles' independent blink command, and mascot click animation has
its own 60ms tick. New idle decoration therefore needs an explicitly gated
timer rather than being described as one existing shared clock.

### 7.2 Stacking (z-order; follow-on candidate)

- **L0** base bands (chat · activity · composer · status) — `renderLayoutBands`.
- **L1** toasts — **CANDIDATE**: own layer + expiry wake-up. Today toasts squat in the
  status line and expire lazily (`Prune()` only runs on unrelated redraws,
  `notifications.go:81-94`), so they can outlive their TTL.
- **L2** dialog stack — LIFO, front-only keys, 200ms push grace (existing).
- **L3** hardware cursor.

Escape pops the top layer first; permission stays bottom-docked, pickers centered.

### 7.3 Animation rules

Every animation names its tick and its cache key or it doesn't ship. Per-frame
gradient text is confined to the spinner line (re-rendered every tick anyway);
gradient text inside chat history is **rejected** (would require animatable history
items + per-frame re-render). No layout-shifting motion beyond the sanctioned
spinner appear/disappear. Full catalog + reduced-motion matrix in the demo.

### 7.4 Performance budget

Existing cache stack makes 30fps affordable: per-item render cache with frozen
finished items · whole-viewport string cache · streaming-markdown prefix cache ·
pooled glamour renderers · O(viewport) render only. Rule: incremental per-frame work
stays well under 33ms. Follow-on candidates include crush's resize pre-warming,
off-screen animation pause, and a `CRUSH_UI_DEBUG`-style render-frequency overlay
behind a debug flag. Deferred: pi's synchronized output (`?2026`), codex's 120fps frame actor
(our policy ceiling is 30fps), codex's adaptive stream drain (adopt on evidence).

## 8. Mascot spec — Eno

Rendered poses, anatomy, live animation: [`demo/mascot.html`](demo/mascot.html).

- **Grid:** fixed 15 wide × 6 tall (Clawd is 9×3). Proportions follow the fox
  emoji: big two-row ears with dark tips, large round wide-set eyes — and the
  fox-versus-cat cue: the cheek row flares out as the widest row of the face
  (15 cells under a 13-cell head), then the chin tucks back to 9. Poses only
  swap eye cells, the sparkle row, and a trailing `z`, so tests pin exact
  strings like `mascot_welcome_test.go` today.
- **Tones:** `enoBody / enoOutline` replace `clawdBody / clawdBg` in the
  palette. Face cells (eyes, nose) render with terminal-default styling as
  negative space — decided in the 2026-07-23 review; `enoFace` is not a hex
  token.
- **Rule:** foreground colors only, never background fills (fixes the
  `#000000`-rectangle bug).
- **Poses:** `default · look-left · look-right · blink · happy · celebrate · sleep`.
- **Idle blink/glance** needs a new gated timer (pattern: `ensureSpinnerTick`,
  `app.go:4496`) running only while the welcome state is visible — today's
  `mascotTickMsg` only fires during click sequences (`app.go:882-887`).
- **Fallbacks:** hidden below 57 cols (current); `✦` is the always-available mark.

## 9. Themes

`polar-night` default dark, `daybreak` light; legacy `dark`/`light` alias for one
release; `aubergine`/`snowy` retoned or dropped at implementation. Resolution order
(`EINO_THEME` → config → auto-detect) unchanged.

## 10. Adoption decisions (per `PROJECT_DIRECTION.md`)

| Item | Decision | Rationale |
|---|---|---|
| Layout, state machine, band structure | **preserve** | proven; re-skin only |
| 30fps stream batch + cache stack | **preserve** | measured and sufficient (bench-enforced) |
| `theme.go` token architecture | **adapt** | keep palette→styles; extend tokens; remove bypassing hex |
| Clawd mascot | **reject** | borrowed identity + background-fill bug → project-native Eno |
| Shimmer mechanism | **adapt** | keep, recolor to aurora, slow to 2.4s |
| Spinner glyph cycle | **reject** | busy → breathing `✦` |
| Mode-reactive composer border | **project-native** | safety signal at the point of typing |
| Single configurable status line | **defer** | promising `combine`, but outside G6/G7 and needs its own config/persistence outcome |
| Shell-command statusline (claude-code) | **defer** | needs a command-trust model |
| Thin-bar cursor | **defer** | impossible without forking bubbles v1 textarea; revisit with bubbles v2 |
| Crush gradient wordmark | **combine** | restrained: welcome title + shimmer only |
| Codex terminal-palette adaptation | **defer** | separate, larger contract change |

## 11. Feasibility review — can the current architecture do this?

Verdict per design element, with implementation strategy. The core identity is
implementable on the current stack after the review gates above close. Follow-on
status, toast, resize, and diagnostic ideas are technically possible but are not
admitted merely because they are feasible.

| # | Element | Verdict | Implementation strategy |
|---|---|---|---|
| 1 | Palette/token swap | **Moderate** | `theme.go` values + new fields (`auroraSky`, `selection`, `eno*`). `ChatView`, inactive thread views, dialogs/panels, and frozen render caches all retain style state today; P19.0.0 needs one theme generation plus a complete target inventory and cache invalidation, not only assignment to `a.styles`. |
| 2 | Mascot Eno | **Moderate** | Rewrite pose map to 15×6 three-tone segments (`mascot.go:51-56`), foreground-only render, update `welcome.go` box sizing + pinned test strings. Resolve the base-background/face rule before rendering; idle blink uses a new gated tick (§8). |
| 3 | Glyph swap `✦/✧` | **Easy** | `chat.go:1505,1538`, `spinner.go:17-28`, status bar (`app.go:2460-2506`). |
| 4 | Hardcoded color removal | **Easy** | Four sites listed in §1 → palette lookups. |
| 5 | Mode composer border | **Easy** | `renderEditor` reapplies `EditorBorder` every frame already (`app.go:2365-2385`); mode is known (`permissionMode()`); pick palette colors, keep it allocation-light (layout runs per key). |
| 6 | Context meter | **Easy–moderate** | Data exists (`engine.GetContextUsage`). Costs columns in a crowded line — mitigated by §6.1 configurability. Refresh: piggyback the 120ms tick while running; hidden when idle/unknown (no new idle timer). |
| 7 | Single configurable status line | **Moderate, deferred** | Add segment registry + `tui.statusLine` config + `/statusline` setup dialog only after a separate product/config contract. `StatusLineFunc` is already active as a Go callback and is not user-configurable data. |
| 8 | Block cursor + blink options | **Existing / deferred config** | Reverse-video block and reduced-motion suppression already exist. User-selectable cursor mode/speed needs a separate config surface. |
| 9 | Thin-bar cursor | **Not feasible now** | bubbles v1.0.0 textarea hardcodes the invert-the-cell cursor (`cursor.Model.View`). Requires a textarea fork or bubbles v2 — **deferred**, explicitly out of scope. |
| 10 | Toast layer + expiry wake-up | **Moderate, separate gap** | Route expiry through the Bubble Tea event owner with an injectable clock; define overlay geometry and avoid concurrent direct mutation from notification callbacks. |
| 11 | Aurora shimmer on spinner line | **Easy–moderate** | Line re-renders every 120ms tick; extend `RenderShimmerText` (`spinner.go:209-283`) from 2-stop to 3-stop; ANSI themes fall back to flat cyan (current precedent). |
| 12 | Chat-history gradients | **Rejected** | Would require `HistoryAnimatableItem` re-render per frame — pays full markdown cache churn for decoration. |
| 13 | bubbletea v2 + ultraviolet (crush's model) | **Big — defer** | Screen-buffer drawing, hardware cursor, native progress. A separate migration decision with its own plan; not required by anything in this proposal. |
| 14 | Synchronized output `?2026` (pi) | **Defer** | Tear-free frames need renderer support in bubbletea v1; evaluate with #13. |
| 15 | Adaptive stream drain (codex) | **Defer** | Smooth/CatchUp hysteresis (`chunking.rs:85-116`) is polish atop our 30fps batch; adopt only on measured smoothness issues. |

## 12. What the references teach (borrowing list)

Same-stack evidence comes from **crush** (Go/Bubble Tea, albeit v2 + ultraviolet):

- **Resize pre-warming** (`model/chat.go:313-339`): reflow visible items only during
  resize, then warm the width cache in batches after a 120ms settle. Defer until a
  repository benchmark reproduces a resize budget failure and freezes height/
  scroll-anchor behavior.
- **Off-screen animation pause** (`model/chat.go:440-503`) and **content-hash-cached
  animation frames** (`anim/anim.go`): adopt when the WORK panel animates sub-agents.
- **`CRUSH_UI_DEBUG` render-frequency overlay** behind a debug flag: retain as a
  verification-tool candidate, not a user-facing identity slice.
- Crush's 33ms streaming debounce at the data layer validates our existing 30fps
  batch; nothing to change.

From **codex** (ratatui):

- **Status-line segment enum + live-preview setup + warn-once unknown ids** — the
  model for §6.1. Its luma-softened accent trick is noted for future theme polish.
- `FrameRequester` coalescing confirms the batch pattern we already have.

From **claude-code-ripe** (Ink): the shared-clock idea (already ours via the spinner
tick); the shell-command statusline (deferred, §6.1).

From **pi**: synchronized output (deferred, #14); overlays composited into the line
buffer before diffing — equivalent to our existing line-replacement compositing.

From **opencode**: selection-aware dialog dismissal — small UX polish, noted for the
dialog-stack backlog, not in this redesign.

## 13. Implementation map

The executable version of this map is program **P19**: slice contracts, ordered
dependencies, promotion gates, and rollback boundaries live in
[`p19-tui-revontuli-identity.md`](../p19-tui-revontuli-identity.md); live state
lives in [`migration/PLAN.md`](../../PLAN.md).
Correspondence (this section is descriptive only; slice IDs are the atomic
sub-slices in the contract):

1. **Track A · P19.0.0-0.2 Theme propagation, precedence, palettes + contrast gate** — #1; the visible identity change.
2. **Track B · P19.1.0-1.1 Eno mascot (negative-space face)** — #2.
3. **Track C · P19.2.0-2.2 Glyphs + spinner** — #3, #5 partial.
4. **Track D · P19.3.0-3.5 Color purge, composer border, user `▎` panel, welcome gradient** — #4, #5, #6.
5. **Follow-on candidates (not P19 slices)** — configurable status line,
   toast expiry/layering, resize pre-warming, dead-header cleanup, and render
   diagnostics require separate admission.

Verification per slice: update golden files; `make fmt`, `make lint`,
`make test`, `make build`, plus the P19 contract's required Makefile/docs/
manifest gates; eyeball compact/standard/wide at 80×24 and 150×24, light and
ANSI themes, reduced-motion on.

## 14. Demo files

- `demo/index.html` — concept, before/after, welcome screen
- `demo/components.html` — transcript, status-line segments, composer states, tools, dialogs, toasts, sidebar
- `demo/dynamics.html` — frame model, stacking, streaming demo, animation catalog, performance budget
- `demo/mascot.html` — Eno anatomy, pose sheet, live animation
- `demo/themes.html` — token tables, ANSI fallback, contrast, diff samples
- `demo/assets/demo.css` — shared styles; CSS variables mirror the token names 1:1
