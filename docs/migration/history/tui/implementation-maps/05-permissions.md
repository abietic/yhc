# TUI Implementation Map 05: Permission UX

**Status:** historical
**Last verified:** 2026-07-14
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/permissions/PermissionDialog.tsx`
- `src/components/permissions/PermissionRequest.tsx`
- `src/components/permissions/PermissionPrompt.tsx`
- `src/components/permissions/BashPermissionRequest/BashPermissionRequest.tsx`
- `src/hooks/toolPermission/handlers/interactiveHandler.ts`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### Permission Prompt Layout

The reference renders permission prompts as part of the bottom slot (below
messages, above input), not as a centered overlay:

```
● Bash (rm -rf /tmp/build)
  ⎿  Waiting for permission…

Do you want to proceed?
❯ Yes, allow                         ← arrow key selection
  No, deny
  Always allow for this tool
  Allow in this directory

Esc to cancel · Tab to expand feedback
```

Features:
- **Selection-based**: Arrow keys navigate options (not single-key shortcuts)
- **Highlight**: Selected option has `❯` indicator
- **Feedback input**: Tab opens inline text input for "tell Claude what to do next/differently"
- **Keyboard shortcuts**: Options can bind to keys (e.g., `y` for yes)
- **Contextual**: Tool-specific permission UIs (Bash, FileEdit, FileWrite, etc.)
- **Auto-classifier**: Shows "Auto classifier checking…" shimmer before prompting

### Permission Options (per tool type)

**Bash commands:**
- Allow (run this command)
- Deny
- Always allow in this directory
- Allow all bash commands this session

**File operations:**
- Allow (edit/write this file)
- Deny
- Always allow edits to this file
- Allow all file edits this session

**Generic fallback:**
- Allow
- Deny
- Allow for session
- Always allow

### Visual States

1. **Checking**: "Auto classifier checking…" with shimmer animation
2. **Prompting**: Full selection UI shown
3. **Denied by classifier**: Brief message, no prompt shown
4. **Allowed by classifier**: No UI shown (auto-approved)

## Implementation State at Closeout

Current implementation uses a **bottom-anchored inline dialog** with arrow-key selection:

```
──────────────────────────────────────────────────
  Bash command
  Bash(echo hi)

  ❯ Yes
    Yes, and don't ask again for this command
    No

  ↑/↓ navigate · enter select · esc cancel
```

- Arrow-key (↑/↓) navigation with `❯` selection indicator and wrapping
- Enter confirms selected option
- Esc cancels (deny)
- Single-key accelerators preserved: `a`/`y` (allow), `s` (session), `A` (always), `d`/`n`/esc (deny)
- Tool-specific titles: "Bash command", "File access", "Agent dispatch", "Tool use"
- Session scope displayed in option label when provided
- Structured tool input display via `formatToolArgs()` (not raw JSON)
- Tool-specific content: Bash shows full command, Edit shows inline old/new diff, Write shows content preview ✅
- Tab-to-toggle feedback text input with cursor ✅
- Feedback text shown in chat after permission decision ✅
- Engine prerequisite for auto-classifier shimmer is now unblocked: classifier-sourced permission paths emit bounded structured `classifier_status` events via explicit helpers, while generic permission callbacks do not emit classifier status ✅
- TUI renders auto-classifier shimmer: spinner shows "Auto classifier checking <tool>…" during checking phase, clears on completed/cleared ✅
- `App.MakePermissionPromptFn` is presentation-only and returns structured
  once/session/always/deny/cancel/timeout decisions; the engine coordinator owns
  grant commit and request/resolved events ✅
- Coordinator request events update runtime state but do not enqueue a duplicate
  dialog; the adapter response remains owner-thread scoped ✅
- Root/child session grants use explicit root-lineage identity, and concurrent
  project-local always commits are serialized before terminal resolution ✅

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Gap |
|--------|-----------|---------|-----|
| Layout position | Bottom slot, inline | Bottom-anchored overlay ✅ | Done |
| Interaction | Arrow key selection | Arrow-key ↑/↓ + enter + wrapping ✅ | Done |
| Selection indicator | `❯` on focused option | `❯` indicator ✅ | Done |
| Options | Tool-specific with context | Tool-specific titles + session-scope labels ✅ | Done |
| Single-key shortcuts | Optional accelerators | `a`/`y`/`s`/`A`/`d`/`n`/esc preserved ✅ | Done |
| Input display | Structured per-tool summary | `formatToolArgs()` structured display ✅ | Done |
| Footer hints | "Esc to cancel · Tab to amend" | "↑/↓ navigate · enter select · tab feedback · esc cancel" ✅ | Done |
| Feedback | Tab to expand inline input | Tab toggles feedback text field with cursor ✅ | Done |
| Tool-specific diffs | Edit shows old/new diff, Write shows content | Inline colored diff for Edit, content preview for Write ✅ | Done |
| Auto-classifier | Shimmer animation state | Engine emits `classifier_status` events ✅; TUI renders shimmer spinner with "Auto classifier checking <tool>…" ✅ | Done |
| Concurrent handling | One actionable request row per tool use | Engine coordinates concurrent requests; TUI serializes owner-scoped presentation without duplicating coordinator events ✅ | Adapted |

## Dependencies and entrypoints

- Depends on `engine/permission_interaction.go`, `engine/permission/`, scoped
  root-lineage approvals, project-local persistence, denial tracking, and
  cancellable permission requests.
- Bounded structured `classifier_status` events now provide the engine-side
  prerequisite for the reference auto-classifier checking shimmer, and the TUI
  renders the bounded checking state; full classifier and permission UX parity
  still requires broader scenario coverage.
- TUI, plain, and ACP decisions apply the same engine policy and structured
  terminal vocabulary.
- The dialog may format decisions but must not duplicate rule evaluation,
  persistence, lifecycle events, or terminal claims.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 5.1: Redesign Permission Dialog Position

**File:** `internal/tui/dialog.go`

Instead of centering the dialog over the full screen, render it inline between
the chat area and the input. This means the permission UI replaces the editor
area temporarily.

Option A (simpler): Keep overlay but anchor it to the bottom third.
Option B (reference-faithful): Render as content in the bottom slot.

**Recommended: Option A** — Keep the overlay approach but make it narrower and
bottom-anchored. This preserves message visibility while matching the reference
feel.

```go
func (d *PermissionDialog) Overlay(base string, width, height int) string {
    // Render dialog content
    content := d.renderContent(width)

    // Position at bottom of screen instead of center
    dialog := d.style.Width(width - 4).Render(content)
    dialogLines := strings.Count(dialog, "\n") + 1

    // Overlay at bottom (above status bar position)
    baseLines := strings.Split(base, "\n")
    startLine := len(baseLines) - dialogLines - 3 // 3 for status bar
    // ... overlay logic ...
}
```

### Step 5.2: Add Arrow-Key Selection to Permission Dialog

**File:** `internal/tui/dialog.go`

Replace single-key shortcuts with arrow-key navigation:

```go
type PermissionDialog struct {
    // ... existing fields ...
    options     []PermissionOption
    selectedIdx int // 0-based selection index
}

type PermissionOption struct {
    Label    string
    Response PermissionResponse
    Key      rune // shortcut key (optional)
}

func (d *PermissionDialog) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
    switch msg.String() {
    case "up", "k":
        if d.selectedIdx > 0 {
            d.selectedIdx--
        }
    case "down", "j":
        if d.selectedIdx < len(d.options)-1 {
            d.selectedIdx++
        }
    case "enter":
        d.submit(d.options[d.selectedIdx].Response)
        return true, nil
    case "esc":
        d.submit(PermissionDeny)
        return true, nil
    default:
        // Check shortcut keys
        for _, opt := range d.options {
            if opt.Key != 0 && rune(msg.String()[0]) == opt.Key {
                d.submit(opt.Response)
                return true, nil
            }
        }
    }
    return false, nil
}
```

### Step 5.3: Render Selection with `❯` Indicator

```go
func (d *PermissionDialog) renderOptions() string {
    var sb strings.Builder
    for i, opt := range d.options {
        if i == d.selectedIdx {
            sb.WriteString("❯ ")
            sb.WriteString(opt.Label)
        } else {
            sb.WriteString("  ")
            sb.WriteString(opt.Label)
        }
        sb.WriteByte('\n')
    }
    return sb.String()
}
```

### Step 5.4: Format Tool Input Display

Instead of raw JSON, show structured tool input:

```go
func formatPermissionInput(toolName, input string) string {
    var params map[string]any
    if err := json.Unmarshal([]byte(input), &params); err != nil {
        return truncate(input, 200)
    }

    switch toolName {
    case "Bash":
        if cmd, ok := params["command"].(string); ok {
            return "$ " + truncate(cmd, 200)
        }
    case "Write", "Edit", "Read":
        if fp, ok := params["file_path"].(string); ok {
            return fp
        }
    }

    // Fallback: formatted key-value pairs
    var lines []string
    for k, v := range params {
        lines = append(lines, fmt.Sprintf("  %s: %v", k, truncate(fmt.Sprint(v), 100)))
    }
    return strings.Join(lines, "\n")
}
```

### Step 5.5: Add Tool-Specific Permission Options

```go
func permissionOptionsForTool(toolName string) []PermissionOption {
    switch toolName {
    case "Bash":
        return []PermissionOption{
            {Label: "Yes, allow this command", Response: PermissionAllow, Key: 'y'},
            {Label: "No, deny", Response: PermissionDeny, Key: 'n'},
            {Label: "Allow all bash this session", Response: PermissionAllowSession, Key: 's'},
            {Label: "Always allow", Response: PermissionAllowAlways, Key: 'a'},
        }
    case "Write", "Edit":
        return []PermissionOption{
            {Label: "Yes, allow this edit", Response: PermissionAllow, Key: 'y'},
            {Label: "No, deny", Response: PermissionDeny, Key: 'n'},
            {Label: "Allow edits this session", Response: PermissionAllowSession, Key: 's'},
            {Label: "Always allow", Response: PermissionAllowAlways, Key: 'a'},
        }
    default:
        return []PermissionOption{
            {Label: "Allow", Response: PermissionAllow, Key: 'y'},
            {Label: "Deny", Response: PermissionDeny, Key: 'n'},
            {Label: "Allow for session", Response: PermissionAllowSession, Key: 's'},
            {Label: "Always allow", Response: PermissionAllowAlways, Key: 'a'},
        }
    }
}
```

### Step 5.6: Add Footer Hints

Below the options, show key hints:

```
Esc to cancel · y/n shortcuts available
```

### Step 5.7: Keep Single-Key Shortcuts as Acceleration

Preserve the existing single-key behavior (`a`/`s`/`A`/`d`) as accelerators
alongside the arrow-key selection. Users who know the shortcuts can bypass
navigation.

---

## Post-Parity Modernization Boundary

The runtime emits typed permission request/resolution events and retains only
unresolved requests in defensive per-thread snapshots. M3 now reports both
event-prompter and callback prompts through the current query emitter, keeps
approval and user-input requests keyed by owner thread, and uses a bounded
TUI-only response-handle store. Inactive requests produce only a compact status
summary; the full dialog opens after switching to its owner, and canonical
reconciliation replays only requests that remain unresolved. Tool-specific
semantic projections remain a later M4 refinement.

P9.2 keeps durable-grant coalescing in the engine coordinator. Each newly
allowed request emits its own owner-scoped resolution, and the TUI removes only
that request's attention/dialog state. It never infers approval from another
row or authorizes from the local dialog queue.

M4.4 also removes modal-routing ambiguity. Permission, MCP approval, plan
approval, user question, resume, help, pickers, settings, task/team panels, and
confirmation overlays now enter one formal `DialogStack`. Only the top layer
receives keys; layers render back-to-front; every frame records the state to
restore. If a covered asynchronous permission resolves or is canceled, removing
that frame rewires the upper frame's return state rather than dropping the user
back to chat. Runtime-interaction dialogs answer/cancel before Ctrl+C continues
to query interruption; passive overlays consume Ctrl+C after closing.

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Permission dialog shows at bottom (not dead center)
- [x] Arrow keys (↑/↓) navigate permission options
- [x] Selected option highlighted with `❯` indicator
- [x] Enter confirms selected option
- [x] Esc denies/cancels
- [x] Single-key shortcuts still work (y/n/s/a)
- [x] Tool input displayed in structured format (not raw JSON)
- [x] Tool-specific option labels (Bash vs File vs generic)
- [x] Footer shows available key hints
- [x] Concurrent permission requests still serialize correctly
- [x] Coalesced resolution removes only the matching owner attention
- [x] Dialog dismisses cleanly on response
- [x] Nested overlays restore the exact underlying chat/search/thread state
- [x] Covered asynchronous permission removal preserves the upper dialog stack
- [x] Modal mouse and keyboard input cannot leak to the chat beneath it

## Files Modified

- `internal/tui/dialog.go` — Selection model with `permOption` type, `buildOptions()`, `dialogTitle()`, arrow-key `HandleKey()`, `❯` rendering in `Overlay()`
- `internal/tui/dialog_test.go` — 8 tests: title, indicator, navigation, enter selection, accelerators, tool titles, session scope, option structure
- `internal/tui/dialog_stack.go` — shared modal ordering, focus, rendering, return-state, and covered-removal lifecycle
- `internal/tui/app_dialog_stack_test.go` — nested/async restoration, permission response, mouse isolation, and Ctrl+C evidence
- `internal/tui/dialog.go` — dialog-local fixed permission accelerators, separate from configurable composer actions
