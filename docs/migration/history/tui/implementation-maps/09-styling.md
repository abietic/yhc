# TUI Implementation Map 09: Styling and Theming

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/design-system/ThemeProvider.tsx`
- `src/components/design-system/color.ts`
- `src/ink/styles.ts`
- `src/utils/theme.ts`
- `src/components/ThemePicker.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### Theme System

The reference has 6 themes with 89+ color tokens:
- `dark` (default)
- `light`
- `light-daltonized`
- `dark-daltonized`
- `light-ansi`
- `dark-ansi`

Key color roles:

| Role | Dark theme | Usage |
|------|-----------|-------|
| `claude` | orange `rgb(215,119,87)` | Brand, logo, prompt |
| `text` | white | Default text |
| `subtle` | gray | Secondary info, hints |
| `success` | green | Completed tools, confirmations |
| `error` | red | Errors, warnings |
| `warning` | amber | Running state, caution |
| `permission` | blue | Permission prompts |
| `diffAdded` | green | Diff additions |
| `diffRemoved` | red | Diff removals |
| Agent colors | 8 distinct | Sub-agent identification |

### Reference Color Values (Dark Theme)

```
claude:      rgb(215,119,87)  — warm orange
text:        rgb(255,255,255) — white
subtle:      rgb(128,128,128) — medium gray
success:     rgb(74,222,128)  — bright green
error:       rgb(248,113,113) — bright red
warning:     rgb(251,191,36)  — amber/gold
permission:  rgb(96,165,250)  — medium blue
```

### Styling Patterns

- **Bold** for emphasis (tool names, headings, user input prefix)
- **Dim** for secondary info (timestamps, gutters, hints)
- **Italic** for thinking/system messages
- **Reverse** for selected items in lists
- **Background color** for tool name badges (each tool type gets a bg color)
- **Underline** for links (when hyperlink support available)

### Tool Name Background Colors

Reference gives each tool a distinct background color on its name badge:
- Bash: specific bg color
- Read/Write/Edit: file-operation color
- Grep/Glob: search color
- Agent: agent color

## Implementation State at Closeout

```go
// Current color palette (styles.go defaultStyles()):
claude     := "#D77757"  // rgb(215,119,87) — brand orange
permission := "#B1B9F9"  // rgb(177,185,249) — permission blue-purple
green      := "#67C27E"  // rgb(103,194,126) — success
red        := "#ED6971"  // rgb(237,105,113) — error
warning    := "#E4B35C"  // rgb(228,179,92) — warning/amber
subtle     := "#646464"  // rgb(100,100,100) — subtle
inactive   := "#999999"  // rgb(153,153,153) — inactive/dim
userMsgBg  := "#373737"  // rgb(55,55,55) — user message background
shimmer    := "#FFA078"  // rgb(255,160,120) — spinner shimmer
stalledRed := "#AB2B3F"  // rgb(171,43,63) — stall indicator
// Diff colors (6 variants: added/removed × normal/dim/word)
```

Current approach: Single `Styles` struct with all colors centralized in `defaultStyles()`.
No inline color literals outside styles.go. Semantic roles: brand, success, error,
warning, subtle, dim, diff (added/removed). Future theme switching possible by
replacing the `Styles` struct.

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Gap |
|--------|-----------|---------|-----|
| Brand color | Orange `#D77757` | Orange `#D77757` ✅ | Done |
| Color roles | 89+ tokens | 18 named colors, 26 style entries ✅ | Done (sufficient for dark theme) |
| Centralized | All in theme files | All in `defaultStyles()` ✅ | Done |
| Semantic roles | brand/success/error/warning/subtle | All present ✅ | Done |
| Theme switching | 6 themes | 6 themes via `theme.go` ✅ | Done |
| Tool name bg | Per-tool background | Per-category bg via `toolCategoryBg()` ✅ | Done |
| Dim styling | Extensive use | `Dim` + `Subtle` styles ✅ | Done |
| User msg background | Slight tint | `#373737` background ✅ | Done |
| Selection highlight | Reverse video | Reverse video ✅ | Done |
| Diff colors | Added/removed with word highlights | 6-variant diff palette ✅ | Done |
| ANSI fallback | `dark-ansi`/`light-ansi` | ANSI-16 palette with auto-detection ✅ | Done |
| Light theme | Light palette | `light` and `snowy` themes ✅ | Done |
| User-configurable colors | EINO_THEME env / config | `ResolveTheme()` with env + config ✅ | Done |
| Customizable status line | Hook-based | `StatusLineFunc` hook in Config ✅ | Done |

## Dependencies and entrypoints

- Depends on semantic status roles from messages, tools, permissions, and
  provider capability/error presentation.
- Styling must not be the only carrier of status; text/icons must remain usable
  in non-color and reduced-capability terminals.
- Theme/configuration choices belong in shared configuration, not ad hoc TUI
  globals.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 9.1: Align Color Roles with Semantic Meaning

The current palette is reasonable. Refine for closer reference alignment:

```go
func defaultStyles() Styles {
    // Primary brand (keep purple — our identity, not orange)
    brand   := lipgloss.Color("#7C3AED")

    // Semantic colors (align with reference roles)
    text    := lipgloss.Color("#E5E5E5") // slightly off-white
    subtle  := lipgloss.Color("#6B7280") // gray for secondary
    success := lipgloss.Color("#10B981") // green
    error   := lipgloss.Color("#EF4444") // red
    warning := lipgloss.Color("#F59E0B") // amber
    info    := lipgloss.Color("#06B6D4") // cyan for info/tools
    border  := lipgloss.Color("#525252") // neutral border

    // Diff colors (for future use)
    // diffAdded   := lipgloss.Color("#4ADE80")
    // diffRemoved := lipgloss.Color("#F87171")
}
```

### Step 9.2: Add Tool Name Background Color (Optional)

Give tool names a subtle background highlight instead of just foreground color:

```go
ToolName: lipgloss.NewStyle().
    Foreground(lipgloss.Color("#E5E5E5")).
    Background(lipgloss.Color("#1E3A5F")). // subtle dark blue bg
    Bold(true).
    Padding(0, 1), // 1-char horizontal padding inside the badge
```

**Consideration**: Background colors in terminals can look jarring. The
reference uses very subtle bg colors that work in their React renderer but
may not translate well to all terminal emulators.

**Decision**: Keep foreground-only (cyan+bold) for now. Add background only
if user feedback requests it.

### Step 9.3: Add Dim Helper to Styles

Add a dedicated `Dim` style for consistent dimming:

```go
// Add to Styles struct:
Dim lipgloss.Style

// In defaultStyles():
Dim: lipgloss.NewStyle().Faint(true),
```

Use `Faint(true)` which maps to ANSI dim attribute — works universally.

### Step 9.4: Add User Message Styling

Give user messages a slightly different visual weight:

```go
UserPrefix: lipgloss.NewStyle().
    Bold(true).
    Foreground(lipgloss.Color("#E5E5E5")), // bright white, bold
UserContent: lipgloss.NewStyle().
    Foreground(lipgloss.Color("#E5E5E5")), // normal white
```

Consider: reference uses `userMessageBackground` (very subtle bg tint on user
messages). This helps visually separate turns. Could add:

```go
UserBlock: lipgloss.NewStyle().
    Background(lipgloss.Color("#1a1a2e")), // very subtle dark blue tint
```

**Provisional decision**: Do not add background tinting until golden tests cover
representative terminal color schemes.

### Step 9.5: Ensure Consistent Style Application

Audit all rendering code to use styles from the `Styles` struct consistently:
- Never use `lipgloss.NewStyle()` inline in rendering functions
- Always reference `a.styles.X` or pass styles to helper functions
- This enables future theme switching by swapping the `Styles` struct

### Step 9.6: Theme Interface Proposal at the Snapshot

Design for future theme switching without implementing it yet:

```go
// Future: Theme interface
type Theme interface {
    Name() string
    Styles() Styles
}

// For now, just ensure all color decisions flow through defaultStyles()
// so switching is a single point of change later.
```

### Step 9.7: ANSI-Safe Fallback Proposal at the Snapshot

For terminals with limited color support, provide an ANSI-16 fallback:

```go
func ansiStyles() Styles {
    return Styles{
        Header:          lipgloss.NewStyle().Bold(true),
        AssistantPrefix: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(5)), // magenta
        ToolSuccess:     lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2)), // green
        ToolError:       lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(1)), // red
        ToolRunning:     lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(3)), // yellow
        Subtle:          lipgloss.NewStyle().Faint(true),
        // ...
    }
}
```

**Provisional decision**: Keep ANSI fallback pending until compatibility testing identifies
the supported terminal/color-profile matrix.

---

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] All colors are defined in `defaultStyles()` (no inline color literals)
- [x] Semantic roles clear: brand, success, error, warning, info, subtle
- [x] Dim/Faint style available for secondary text
- [x] User messages visually distinguishable from assistant
- [x] Tool names have consistent bold+colored styling
- [x] Style changes don't break existing tests
- [x] Future theme switching possible by replacing `Styles` struct

## Files Modified

- `internal/tui/styles.go` — Color refinement, Dim style, consistency
- Any file with inline `lipgloss.Color(...)` calls → migrate to styles

## Decisions Deferred at Closeout

- ~~Multiple theme support~~: Done — 6 themes (dark, light, dark-ansi, light-ansi, snowy, aubergine)
- ~~ANSI-16 fallback~~: Done — auto-detection via `termSupportsTruecolor()`
- ~~User-configurable colors~~: Done — `EINO_THEME` env var + config `theme` field
- Tool-specific backgrounds: adapt only if terminal readability tests justify them
- ~~Light theme~~: Done — `light` and `snowy` palettes

## Files Modified

- `internal/tui/theme.go` — Theme system: palettes, resolution, auto-detection
- `internal/tui/styles.go` — `Styles` struct and `defaultStyles()` (dark-only fallback)
- `internal/tui/app.go` — Theme wiring in `New()`, `StatusLineFunc` hook
- `cmd/eino-agent/cmd/root.go` — Pass config theme to TUI
