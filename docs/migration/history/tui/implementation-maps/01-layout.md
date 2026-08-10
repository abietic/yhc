# TUI Implementation Map 01: Layout and Visual Structure

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/FullscreenLayout.tsx`
- `src/components/LogoV2/CondensedLogo.tsx`
- `src/components/PromptInput/PromptInput.tsx`
- `src/screens/REPL.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

The reference uses `FullscreenLayout.tsx` with this vertical structure:

```
┌─────────────────────────────────────────────────┐
│ [StickyPromptHeader] (visible when scrolled up) │  ← Shows last user prompt
├─────────────────────────────────────────────────┤
│ ScrollBox (flexGrow=1, stickyScroll=true)        │
│   ├── CondensedLogo + StatusNotices (top)       │
│   ├── Messages (conversation history)           │
│   ├── <Box flexGrow=1 /> (spacer, pushes down)  │
│   ├── SpinnerWithVerb (when loading)            │
│   └── QueuedCommands                            │
├─────────────────────────────────────────────────┤
│ Bottom Slot (flexShrink=0, maxHeight=50%)       │
│   ├── SuggestionsOverlay (absolute, above)      │
│   ├── PermissionRequest (when needed)           │
│   └── PromptInput + Footer                      │
├─────────────────────────────────────────────────┤
│ [Modal Overlay] (absolute, for /config etc.)    │
└─────────────────────────────────────────────────┘
```

Key visual elements:
- **CondensedLogo** (top): `"Claude Code"` bold + `v{version}` dim + model name dim + cwd dim
- **No separator line** between header and messages (reference has no `───` separator)
- **Spacer** between messages and spinner pushes content to bottom of viewport
- **Spinner** lives inside the scroll area, directly above the input
- **Bottom slot** has max 50% height and does not scroll

## Implementation State at Closeout

```
┌─────────────────────────────────────────────────┐  ← tea.WithAltScreen (fullscreen)
│ Chat Area (scrollable viewport)                  │  (variable height)
├─────────────────────────────────────────────────┤
│ ✶ Thinking…  3.2s                               │  (1 line, only when running)
├─────────────────────────────────────────────────┤
│ ╭─────────────────────────────────────────────╮ │
│ │ ❯ textarea (bordered input, orange prompt)  │ │  (3-10 lines)
│ ╰─────────────────────────────────────────────╯ │
│ /command hints (if visible, arrow-key navigable) │
├─────────────────────────────────────────────────┤
│ Status bar (keybind hints · ⬡ model)             │  (1 line)
└─────────────────────────────────────────────────┘
```

Implemented features:
- **Condensed header** — `renderHeader()` shows "Eino Agent v{ver} · model · ~/cwd" matching reference CondensedLogo
- **No separator** — removed entirely
- **Alternate screen** — one `tea.WithAltScreen` program option owns fullscreen TUI lifecycle
- **Bordered input** — rounded border with `❯` prompt in claude orange `#D77757`
- **Spinner between chat and editor** — `renderSpinner()` shows shimmer + verb + elapsed time
- **Spinner mode transitions** — Thinking → Responding → ToolUse with contextual verb text
- **Explicit region ownership** — `calculateLayout(layoutRequest)` partitions
  header, chat, activity, hints, editor, optional sidebar, status, and overlay
  into contiguous `layoutRect` values
- **Measured dynamic bands** — spinner, task tree, command hints, context header,
  and editor use rendered heights rather than fixed estimates
- **Bounded compositor** — every band is ANSI-width-clipped to its rectangle and
  the base plus overlay output remains exactly the terminal height
- **Single-line status bar** — key hints left, model name right
- **Command hint arrow-key navigation** — up/down/enter/tab selection with reverse-video highlight
- **Color system** — claude orange `#D77757`, permission blue-purple `#B1B9F9`, success green, error red

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Status |
|--------|-----------|---------|--------|
| Header content | Logo + model + cwd (condensed) | "Eino Agent v{ver} · model · ~/cwd" condensed line ✅ | **Done** |
| Separator | None between header/messages | None | **Done** |
| Spinner location | Inside scroll area, above input | Between chat and editor (`renderSpinner()`) | **Done** |
| Spinner verb + time | "Thinking…" / "Responding…" + elapsed | `SpinnerState` with mode transitions + `Duration()` | **Done** |
| Input border | No visible border, just `❯` prompt char | Rounded border box with `❯` prompt | **Adapted** — border kept for visual clarity |
| Alternate screen | Fullscreen terminal takeover | `tea.WithAltScreen` at CLI startup | **Done** |
| Bottom max height | 50% of terminal | Dynamic cap: `min(editorMax, totalHeight/2)` ✅ | **Done** |
| Spacer/gravity | Content pushed to bottom via flexGrow | Bottom-gravity padding in `Render()` ✅ | **Done** |
| Sticky prompt | Shows user's last prompt when scrolled up | `findLastUserPromptBefore()` sticky header ✅ | **Done** |
| "New messages" pill | Floating indicator when scrolled up | Centered pill with message count ✅ | **Done** |
| Narrow terminal | Doesn't break at < 40 cols | Min 30×10 clamp in `calculateLayout()` ✅ | **Done** |
| Region ownership | Flex/Yoga box ownership | Explicit contiguous rectangles for every base/overlay band | **Done** |
| Modal composition | Absolute overlay with one focus owner | Formal dialog stack over the owned overlay rectangle | **Done** |
| Wide sidebar | Context-dependent side content | Reserved zero-width sidebar rectangle; responsive sidebar deferred | **Partial** |

## Dependencies and entrypoints

- Primary entrypoint: interactive TUI from `cmd/eino-agent/cmd/root.go`.
- Shared dependency: application lifecycle must preserve session shutdown,
  interruption, and terminal restoration behavior.
- No business logic should be introduced into layout helpers.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 1.1: Redesign Header to CondensedLogo Style

**File:** `internal/tui/app.go` — `renderHeader()`

Current:
```
 eino-agent [model_name]  ⠋ running
─────────────────────────────────────────
```

Target:
```
● eino-agent v0.1.0
  model_name · ~/workspace/path
```

Changes:
- Replace `" eino-agent [model]"` with bold `"● eino-agent"` + dim version
- Second line: dim model name + ` · ` + dim truncated cwd
- Remove the `───` separator line entirely
- Running indicator moves to spinner area (Section 6)

```go
func (a *App) renderHeader() string {
    title := a.styles.AssistantPrefix.Render("●") + " " +
        a.styles.Bold.Render("eino-agent")
    version := a.styles.Subtle.Render(" v" + Version)
    line1 := title + version

    cwd, _ := os.Getwd()
    cwd = truncatePath(cwd, a.layout.width-len(a.model)-6)
    line2 := "  " + a.styles.Subtle.Render(a.model+" · "+cwd)

    return line1 + "\n" + line2
}
```

### Step 1.2: Remove Input Border, Add `❯` Prompt Character

**File:** `internal/tui/app.go` — `renderEditor()`

Current: Rounded border box wrapping textarea.

Target: No border. `❯` character as prompt indicator (like reference's `PromptInputModeIndicator`).

Changes:
- Remove `EditorBorder` style application
- Prepend `❯ ` (colored purple, or dim when loading) before the textarea
- Shell mode shows `! ` instead of `❯ `
- Keep textarea height calculation but without border overhead

```go
func (a *App) renderEditor() string {
    indicator := a.styles.EditorPrompt.Render("❯") + " "
    if a.inputMode == InputShell {
        indicator = a.styles.ToolRunning.Render("!") + " "
    }
    if a.running {
        indicator = a.styles.Subtle.Render("❯") + " " // dimmed when loading
    }
    content := a.textarea.View()
    editor := indicator + content

    if hints := a.renderCommandHints(); hints != "" {
        editor += "\n" + a.styles.Subtle.Render(hints)
    }
    return editor
}
```

### Step 1.3: Adjust Layout Calculator

**File:** `internal/tui/layout.go`

Changes:
- Header now takes 2 lines (no separator = save 1 line)
- Editor height: no border means content height = raw textarea lines (no -2)
- Status bar remains 2 lines
- Chat area gains the freed line from removed separator

### Step 1.4: Add Bottom-Gravity Spacer

**File:** `internal/tui/chat.go` — `ChatView.Render()`

When follow mode is active and content doesn't fill viewport, add empty lines
at the TOP of the chat area (pushing content to bottom), matching reference's
`<Box flexGrow=1 />` spacer behavior.

At the planning snapshot, the viewport rendered items top-aligned. In follow mode with less
content than viewport height, add padding lines before the first item.

```go
// In Render(), after computing totalLines of visible items:
if cv.follow && totalLines < height {
    topPad := height - totalLines
    lines = append(strings.Repeat("\n", topPad-1), lines...)
}
```

### Step 1.5: Move Spinner to Chat Area (Above Input)

The reference shows the spinner as the last item in the scroll area, directly
above the input. At the planning snapshot, the spinner was in the header.

Changes:
- Remove spinner text from `renderHeader()`
- When `a.running`, append a spinner line as the last visible element in chat
- This requires the spinner to be rendered as part of `ChatView` output or
  appended between chat and editor in `View()`

Implementation: Add a `renderSpinner()` method that returns the spinner line
when running, empty string otherwise. Insert it between chat and editor in
`View()`.

---

## Post-Parity Layout Boundary

M4.4 replaces the former height-only contract with explicit screen geometry.
`App.View` renders header/chat/activity/hints/editor/status as independent bands,
bottom-aligns followed chat content, then applies the formal dialog stack inside
the full overlay rectangle. Mouse hit testing derives chat coordinates from the
same rectangle, so render and input ownership cannot drift independently.

The implementation intentionally remains string based. Three benchmark runs put
`renderLayoutBands` at about 4.2 us/op with 14 allocations and a 100-turn
`App.View` at about 127-130 us/op with 387 allocations on Apple M5 Pro. This is
not evidence that ANSI decode/composition dominates, so adopting a cell screen
buffer now would add state and invalidation complexity without a measured user
benefit. Compact/wide layouts and a nonzero Agent sidebar remain M7 work, which
is why this section stays `Partial`.

- [x] Base and permission-overlay output are frozen in a representative golden.
- [x] Regions are contiguous, bounded, exact-height, and safe at minimum size.
- [x] Activity/task rows and hint borders are included in the budget.
- [x] Modal input cannot leak into chat mouse/keyboard handling.

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Header shows product name + version + model + cwd (no separator line)
- [x] Input area uses `❯` prompt character (bordered variant)
- [x] `❯` dims when model is running/loading
- [x] Shell mode shows `$` instead of `❯`
- [x] Content in follow mode is bottom-aligned (pushed down by spacer)
- [x] Spinner appears above the input area, not in header
- [x] Layout calculation accounts for spinner line height
- [x] Terminal resize recalculates cleanly
- [x] Narrow terminals (< 40 cols) don't break layout
- [x] Alternate screen mode (`tea.WithAltScreen`) has one lifecycle owner

## Files Modified

- `internal/tui/app.go` — `renderHeader()`, `renderEditor()`, `View()`, `renderSpinner()`, lifecycle messages
- `cmd/eino-agent/cmd/root.go` — alternate-screen and capability-derived mouse startup
- `internal/tui/layout.go` — Rectangle partitioning and bounded band compositor
- `internal/tui/dialog_stack.go` — Formal modal ordering, focus, and return-state stack
- `internal/tui/app_layout_golden_test.go`, `layout_regions_test.go` — Golden and geometry evidence
- `internal/tui/layout_bench_test.go` — String-composition and full-view benchmarks
- `internal/tui/spinner.go` — `SpinnerState`, `SpinnerMode`, verb text, duration
- `internal/tui/styles.go` — Color system (claude orange, permission blue-purple, etc.)
