# TUI Implementation Map 06: Spinner, Loading, and Progress

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/Spinner.tsx`
- `src/components/Spinner/index.ts`
- `src/components/Spinner/SpinnerAnimationRow.tsx`
- `src/components/Spinner/useStalledAnimation.ts`
- `src/components/Spinner/TeammateSpinnerTree.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### SpinnerWithVerb Component

The reference shows a spinner with contextual verb text when the model is working:

```
⠋ Thinking…                                       ← animated braille + verb
  12.3s · ↑42k ↓1.2k tokens                       ← timing + token counts
```

Features:
- **Animation**: Braille sequence `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` at 50ms interval
- **Shimmer effect**: Characters shimmer with color gradient
- **Verb rotation**: Random verbs ("Thinking…", "Analyzing…", "Writing…")
- **Duration display**: Shows elapsed time since start
- **Token display**: Shows input/output token count vs budget
- **Stall detection**: Color turns warning/error after extended stall
- **Position**: Inside scroll area, directly above input (not in header)
- **Effort suffix**: Shows effort level like "[extended]"

### SpinnerMode Types

| Mode | Text | When shown |
|------|------|-----------|
| `thinking` | "Thinking…" | Before first token |
| `responding` | "Responding…" | After first token, streaming |
| `tool_use` | Tool name | During tool execution |

### Teammate Spinner Tree

For sub-agents, shows a tree of running tasks:
```
⠋ Thinking…
  ├─ Agent: implement auth   ⠙ Writing…
  └─ Agent: fix tests        ⠹ Analyzing…
```

### ToolUseLoader (Per-Tool Progress)

Each in-progress tool shows a blinking `●` indicator:
- Blink interval: 600ms
- Synchronized across all instances (shared clock)
- Color: undefined → success (green) / error (red) on completion

### Reduced Motion

Reference respects `prefersReducedMotion` setting — shows static `●` instead
of animation.

## Implementation State at Closeout

- Shimmer spinner frames: `·✢*✶✻✽` (matches reference Linux variant) ✓
- Tick rate: 200ms (matches reference shimmer speed) ✓
- Position: Between chat and editor via `renderSpinner()` in `View()` ✓
- Verb text: `SpinnerState.Text()` returns "Thinking…" / "Responding…" / tool name ✓
- Mode transitions: `SpinnerThinking` → `SpinnerResponding` (on first content delta) → `SpinnerToolUse` (on tool call) ✓
- Duration display: `SpinnerState.Duration()` shows elapsed time after 1 second ✓
- Layout integration: `calculateLayout()` takes `spinnerVisible bool` to reserve 1 line ✓
- Tool blinking `●`: In tool items via `tools.go` with visibility toggle ✓
- Status bar: No longer shows redundant spinner — spinner is its own line ✓
- Shimmer color gradient: `ShimmerColor()` interpolates between amber (#E4B35C) and shimmer orange (#FFA078) via sine wave ✓
- Effort suffix: `effortSuffix()` shows [extended]/[high]/[low] based on engine budget level ✓
- Token count: `spinnerTokens()` shows ↑Nk ↓Nk from engine usage data ✓
- Stall detection: `StallIntensity()` ramps color after 30s inactivity, shows "(waiting)" suffix ✓
- Teammate spinner tree: `renderTaskTree()` with `├─`/`└─` prefixes, per-task animated spinner, description, and status suffix ✓

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Status |
|--------|-----------|---------|--------|
| Position | In scroll area, above input | Between chat and editor (`renderSpinner()`) | **Done** |
| Tick rate | 50ms (braille) / 200ms (shimmer) | 200ms shimmer frames `·✢*✶✻✽` | **Done** |
| Verb text | "Thinking…" / "Responding…" / tool name | `SpinnerState.Text()` with mode transitions | **Done** |
| Duration | Shows elapsed seconds | `SpinnerState.Duration()` after 1s | **Done** |
| Mode transitions | thinking → responding → tool_use | Wired in `handleEngineEvent()` | **Done** |
| Token count | Input/output tokens | `spinnerTokens()` shows ↑Nk ↓Nk | **Done** |
| Shimmer | Color gradient animation | `ShimmerColor()` smooth sine-wave interpolation | **Done** |
| Stall detect | Color change after N seconds | `StallIntensity()` + "(waiting)" suffix after 30s | **Done** |
| Effort suffix | "[extended]" etc. | `effortSuffix()` shows [extended]/[high]/[low] ✅ | **Done** |
| Teammate tree | Multi-agent progress | `renderTaskTree()` with tree lines, per-task spinner | **Done** |

## Dependencies and entrypoints

- Depends on explicit engine lifecycle/status events for model calls, hooks,
  tools, tasks, sub-agents, retries, compaction, and stalls.
- Animation must be presentation-only and respect reduced-motion decisions.
- Protocol entrypoints should receive status events without terminal animation.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 6.1: Create Standalone Spinner Component ✅

**File:** `internal/tui/spinner.go` (implemented)

`SpinnerState` struct with `Mode`, `StartTime`, `ToolName` fields.
`SpinnerMode` enum: `SpinnerThinking`, `SpinnerResponding`, `SpinnerToolUse`.
`Text()` returns contextual verb; `Duration()` returns elapsed time after 1s.

### Step 6.2: Render Spinner Between Chat and Editor ✅

**File:** `internal/tui/app.go` — `View()`, `renderSpinner()` (implemented)

`renderSpinner()` shows shimmer frame + verb + elapsed time between chat and editor.
`View()` inserts spinner line when `a.running` is true.

### Step 6.3: Add Duration Tracking ✅

**File:** `internal/tui/app.go` (implemented)

`spinnerState` field on `App` struct. Initialized in `startEngineRequest()` with
`SpinnerThinking` mode and `time.Now()`. Mode transitions wired in
`handleEngineEvent()`: first content delta → `SpinnerResponding`, tool call →
`SpinnerToolUse` with tool name.

### Step 6.4: Add Stall Detection (Color Change)

After 30 seconds without new events, change spinner color from amber to
warning:

```go
func (a *App) spinnerColor() lipgloss.Color {
    elapsed := time.Since(a.spinnerState.StartTime)
    if elapsed > 60*time.Second {
        return lipgloss.Color("#EF4444") // red — likely stalled
    }
    if elapsed > 30*time.Second {
        return lipgloss.Color("#F59E0B") // amber — slow
    }
    return lipgloss.Color("#F59E0B") // normal amber
}
```

Note: True stall detection should track time since last event, not total
elapsed. Add `lastEventTime` to App:

```go
lastEventTime time.Time

// Updated on every engine event:
a.lastEventTime = time.Now()

// Stall check:
func (a *App) isStalled() bool {
    return time.Since(a.lastEventTime) > 30*time.Second
}
```

### Step 6.5: Consider Tick Rate ✅

**Decision**: 200ms tick rate with shimmer frames `·✢*✶✻✽` matching the reference
Linux variant. This provides smooth animation while keeping render overhead low.

### Step 6.6: Remove Spinner from Header ✅

**File:** `internal/tui/app.go` — `renderHeader()` (implemented)

`renderHeader()` returns `""` — header removed entirely. Spinner is now its own
dedicated line between chat and editor via `renderSpinner()`.

### Step 6.7: Token Count Display (Engine Integration)

The engine needs to expose token usage data for the current turn. This requires:

1. Engine emits token count in streaming events (or accumulates)
2. TUI displays: `↑{input_tokens} ↓{output_tokens}` next to duration

This depends on engine changes. For now, show only duration. Add token display
when engine provides the data.

```go
// Future: when engine provides token data
func (a *App) renderSpinnerTokens() string {
    if a.currentTurnTokens.Input == 0 {
        return ""
    }
    return fmt.Sprintf("↑%s ↓%s",
        formatTokenCount(a.currentTurnTokens.Input),
        formatTokenCount(a.currentTurnTokens.Output))
}

func formatTokenCount(n int) string {
    if n >= 1000 {
        return fmt.Sprintf("%.1fk", float64(n)/1000)
    }
    return fmt.Sprintf("%d", n)
}
```

---

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Spinner renders between chat and editor (not in header)
- [x] Shows contextual verb ("Thinking…", "Responding…", tool name)
- [x] Shows elapsed duration after 1 second
- [x] Shimmer animation at 200ms tick rate with `·✢*✶✻✽` frames
- [x] Spinner disappears when query completes
- [x] Mode transitions work (thinking → responding → tool_use)
- [x] Stall detection changes spinner appearance after 30s inactivity
- [x] Header no longer shows spinner text (header removed entirely)
- [x] Performance: spinner tick doesn't cause full chat re-render

## Files Modified

- `internal/tui/spinner.go` — SpinnerState, mode tracking, verb generation
- `internal/tui/app.go` — `View()`, `renderSpinner()`, state transitions
- `internal/tui/layout.go` — Account for spinner line height
