# TUI Implementation Map 07: Status Bar and Footer

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/StatusLine.tsx`
- `src/components/PromptInput/PromptInputFooter.tsx`
- `src/components/PromptInput/PromptInputFooterLeftSide.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### StatusLine Component

The reference uses a customizable status line (user-configurable via hook/script):

```
model-name · 42% context · $0.03                   ← right-aligned
```

Default data provided to status line:
- `model` (display name)
- `context_window` (used percentage, remaining tokens)
- `cost` (total session cost in USD)
- `rate_limits` (5h/7d utilization percentage)
- `workspace` (cwd, project dir)
- `vim.mode` (when vim enabled)

### PromptInputFooter (Below Input)

The footer shows contextual key hints:

```
Enter send · Ctrl+J newline · /command · Esc clear         model-name
```

Or when in specific states:
```
Ctrl+C interrupt · Ctrl+O transcript                        ⬡ running
```

### Mode Indicators in Footer

```
[default mode]     → no indicator
[plan mode]        → "⏸ plan mode" (colored)
[bypass mode]      → "⏵⏵ accept all" (colored)
[running]          → "● running" with dim ❯ prompt
```

### Token/Context Warning

When approaching context limits:
```
⚠ 85% context window used — consider /compact
```

## Implementation State at Closeout

```
  $/! shell mode • / command mode • Ctrl+J newline    ⬡ model_name · 12.3k (42%)
  (shift+tab to cycle mode)
```

Single-line status bar:
- Left side: Keybind hints (context-sensitive per mode/state)
- Right side: `⬡ model_name · TOKEN_COUNT (PCT%)`
- Toast notifications temporarily replace left-side hints (4s auto-dismiss)

Features:
- Context usage indicator with `humanTokens()` formatter (e.g. "12.3k", "1.2M") ✅
- Color-coded percentage: normal (default), warning amber at ≥75%, error red at ≥90% ✅
- Toast notifications: "Press Ctrl+C again to exit", "Context compacted" ✅
- Toast auto-clears after 4 seconds ✅
- Mode indicators with colored icons (plan=amber, bypass=red, running=green) ✅
- Engine integration via `GetContextUsage()` and `GetTotalUsage()` ✅

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Gap |
|--------|-----------|---------|-----|
| Content | Model + context% + cost | Model + token count + context% ✅ | Done (adapted: token count instead of just %) |
| Position | Below input (footer) | Bottom of screen (status bar) ✅ | Done |
| Context % | Shows used percentage | Color-coded percentage shown ✅ | Done |
| Token count | Shows remaining tokens | Shows total tokens used ✅ | Done (adapted: used vs remaining) |
| Cost display | Total session cost | `sessionCost()` shows $X.XX estimate ✅ | Done |
| Token warning | Warning at high usage | Color-coded at 75%/90% thresholds ✅ | Done |
| Mode indicator | Colored mode text | Colored mode indicators ✅ | Done |
| Key hints | Contextual | Context-sensitive per mode/state ✅ | Done |
| Toast/transient msgs | Transient notifications | 4s auto-dismiss toasts ✅ | Done |
| Customizable | User script hook | `StatusLineFunc` hook in Config ✅ | Done |

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 7.1: Add Context Window Usage Display

**File:** `internal/tui/app.go` — `renderStatus()`

Show context window usage on the right side of the status bar:

```go
func (a *App) renderStatus() string {
    left := a.statusLeftContent()

    // Right side: model + context usage
    right := "⬡ " + a.model
    if a.contextUsage > 0 {
        right = fmt.Sprintf("%d%% ctx · %s", a.contextUsage, right)
    }

    first := alignStatusLine(left, right, a.layout.width)
    mode := a.statusModeContent()
    if mode == "" {
        return a.styles.StatusBar.Render(first)
    }
    return a.styles.StatusBar.Render(first + "\n" + mode)
}
```

This requires the engine to expose context window usage. Add a field to App:

```go
// Add to App struct:
contextUsage int // percentage of context window used (0-100)
```

Updated when receiving engine events that include token counts.

### Step 7.2: Add Token Warning at High Usage

When context usage exceeds 80%, show a warning:

```go
func (a *App) statusModeContent() string {
    // Existing mode indicators...

    // Add context warning
    if a.contextUsage >= 80 && !a.running {
        warning := "  ⚠ " + fmt.Sprintf("%d%%", a.contextUsage) +
            " context used — consider /compact"
        return a.styles.Error.Render(warning)
    }

    // Existing mode logic...
}
```

### Step 7.3: Simplify Key Hints

Make key hints more concise and contextual:

```go
func (a *App) statusLeftContent() string {
    if a.focus == FocusChat {
        return "  ↑↓ scroll · PgUp/Dn page · ctrl+o expand · tab edit"
    }

    switch a.inputMode {
    case InputCommand:
        return "  ↑↓ navigate · enter select · esc cancel"
    case InputShell:
        return "  enter run · esc cancel"
    default:
        return "  enter send · ctrl+j newline · / commands · ! shell"
    }
}
```

### Step 7.4: Improve Mode Indicator Styling

Add color to mode indicators to match reference:

```go
func (a *App) statusModeContent() string {
    if a.running {
        return "  " + a.styles.ToolSuccess.Render("●") + " running (ctrl+c to interrupt)"
    }

    switch a.permMode {
    case permission.ModePlan:
        return "  " + a.styles.ToolRunning.Render("⏸") + " plan mode (shift+tab to cycle)"
    case permission.ModeBypassPermissions:
        return "  " + a.styles.Error.Render("⏵⏵") + " accept all (shift+tab to cycle)"
    default:
        return "  (shift+tab to cycle mode)"
    }
}
```

### Step 7.5: Display Cost (When Available)

When session cost tracking is implemented in the engine:

```go
// Right side includes cost:
right := fmt.Sprintf("$%.2f · %d%% · ⬡ %s", a.sessionCost, a.contextUsage, a.model)
```

This requires engine-level cost tracking. For now, show only model + context%.

### Step 7.6: Consolidate Running State Display

When running:
- Status mode line shows: `● running (ctrl+c to interrupt)` in green
- Key hints adjust to: `ctrl+c interrupt · ctrl+o expand`
- Right side keeps model name

```go
func (a *App) statusLeftContent() string {
    if a.running {
        return "  ctrl+c interrupt · ctrl+o expand"
    }
    // ... normal hints
}
```

---

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Status bar shows model name on right side
- [x] Context usage percentage shown when available (e.g., "12.3k (42%)")
- [x] Warning displayed at ≥75% (amber) and ≥90% (red) context usage
- [x] Key hints are concise and context-appropriate
- [x] Mode indicators have appropriate colors (amber for plan, red for bypass)
- [x] Running state shows green `●` with interrupt hint
- [x] Toast notifications replace left hints temporarily (4s auto-dismiss)
- [x] Status bar doesn't wrap or overflow at narrow widths

## Files Modified

- `internal/tui/app.go` — `renderStatus()`, context tracking fields
- `internal/tui/styles.go` — Warning style if needed

## Dependencies and entrypoints

- Context window usage percentage (from token counting in model responses)
- Session cost tracking (from API response metadata)
- These may require engine changes to expose the data as events
