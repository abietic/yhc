# TUI Implementation Map 02: Message Rendering and Conversation Display

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/Messages.tsx`
- `src/components/VirtualMessageList.tsx`
- `src/components/MessageResponse.tsx`
- `src/components/CompactSummary.tsx`
- `src/components/messages/AssistantTextMessage.tsx`
- `src/components/messages/UserTextMessage.tsx`
- `src/utils/messages.ts`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### Assistant Messages

```
● First line of assistant response
  Continuation lines are indented 2 spaces
  with the ● bullet only on first line.

  Paragraph breaks preserved normally.
```

- `●` (BLACK_CIRCLE) bullet in **bold** on first line, colored with assistant theme
- 2-space indent for all lines (including first, after the bullet)
- Streaming: `●` appears immediately, text streams after it
- Content rendered through `StreamingMarkdown` with `marked` lexer

### User Messages

```
❯ User's input text here
  Continuation of multi-line input
```

- `❯` prompt character (same as input indicator) in default color
- 2-space continuation indent
- Background color on user message block (reference uses `userMessageBackground`)

### Tool Results (MessageResponse Pattern)

```
  ⎿  Result text here
     Continuation indented further
```

- `⎿` (U+23BF) gutter character, rendered dim
- Layout: `"  ⎿  "` = 2 spaces + gutter + 2 spaces (5 chars total prefix)
- Content follows after the gutter on line 1; continuation lines get 5-space indent
- Single-line results use `height={1}` constraint (no extra vertical space)
- Nested `MessageResponse` elements don't re-add the gutter (context prevents nesting)

### System Messages

```
✻ Conversation compacted (ctrl+o for history)
```

- Dim text with `✻` prefix for compact boundaries
- Italic dim for general system messages

### Compact Summary

```
● Summarized conversation
  ⎿  Summarized 42 messages up to this point
     (ctrl+o expand history)
```

### Thinking Messages

- Rendered separately from content, before the assistant text
- Typically collapsed or shown as dim italic text
- May show "Thinking..." with duration

### Message Ordering

Reference `normalizeMessages()` and `reorderMessagesInUI()` ensure:
1. User message first
2. Thinking block (if any)
3. Assistant text blocks
4. Tool use messages (each paired with its result)
5. System messages (compact boundaries, rate limits)
6. Error messages inline

## Implementation State at Closeout

- Assistant: `● first_line` / `  continuation` (green `●`, 2-space indent) ✅
- User: `● user text` with `UserPrefix` style (bold orange `●`) ✅
- Tool results: Uses `"  ⎿  "` gutter (5-char prefix) ✅
- System: `✻` prefix, italic dim with padding ✅
- Thinking: Separate message item with collapse/expand ✅
- Message spacing: 1 blank line between items ✅
- Streaming: `●` appears immediately; complete top-level Markdown blocks form
  a source-backed stable region while the final block stays mutable ✅
- Multi-line user wrapping with 2-space continuation ✅
- User message background tint (`UserMessageBlock` with `#373737` bg) ✅
- Compact summary: `● Summarized conversation` with message count on resume ✅
- Interruption: styled `⏎ Request interrupted.` (amber icon + subtle text) ✅

### Streaming region lifecycle

`AssistantMessage` owns one authoritative source buffer and one
`StreamingMarkdown` renderer. During append-only streaming, the renderer parses
only the mutable suffix with Goldmark GFM. All top-level blocks before the final
block are promoted into a monotonic newline-complete prefix and retain their
rendered cache. The final list, table, setext/HTML block, or fence remains
mutable until a following block proves it complete. Reference-style links use a
conservative unsplit path because definitions can affect earlier blocks.

A width change or source replacement drops rendered regions and reseeds from
raw Markdown. `FinishAssistant` calls `AssistantMessage.Finalize`, invalidates
fragment renders, and forces the next draw through the complete source. The
same item then becomes the sole canonical rich/raw/transcript representation;
the next assistant stream allocates a new item.

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Gap |
|--------|-----------|---------|-----|
| Assistant bullet | `●` (BLACK_CIRCLE) bold | `●` green ✅ | Done |
| User prefix | `❯` (same as input prompt) | `●` bold orange ✅ | Done (adapted: `●` used for visual consistency across message types) |
| User background | Subtle background color | `UserMessageBlock` with `#373737` bg ✅ | Done |
| Tool result gutter | `"  ⎿  "` dim, 5-char prefix | `"  ⎿  "` subtle styled ✅ | Done |
| Compact boundary | `✻ Conversation compacted (ctrl+o...)` | `✻` system message style ✅ | Done |
| Compact summary | `● Summarized conversation` + metadata | `CompactSummaryMessage` with message count ✅ | Done |
| Message grouping | `applyGrouping()` groups related tools | Consecutive Read/Grep/Glob collapsed into `ToolGroupMessage` ✅ | Done |
| Collapsed read/search | Multiple reads → single summary | Collapsed tool groups with summary line ✅ | Done |
| Interruption display | `"Request interrupted."` styled | `InterruptionMessage` with `⏎` amber icon ✅ | Done |

## Dependencies and entrypoints

- Depends on ordered `engine.QueryEvent` delivery and transcript/session message
  semantics.
- Compact, interruption, error, and resumed-history presentation require shared
  engine metadata rather than text-pattern inference in the TUI.
- Headless and ACP output need equivalent semantic events even when their
  rendering differs.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 2.1: Change Assistant Bullet to `●`

**File:** `internal/tui/chat.go` — `AssistantMessage.RenderLines()`

Change the prefix character from `⏺` to `●` (BLACK_CIRCLE, U+25CF). This
matches the reference on non-macOS platforms. Keep the bold + colored styling.

```go
const assistantBullet = "●" // U+25CF BLACK CIRCLE
// Was: "⏺" (U+23FA)
```

### Step 2.2: Change User Message Prefix to `❯`

**File:** `internal/tui/chat.go` — `UserMessage.RenderLines()`

Current: Shows `You` as prefix text.
Target: Show `❯` character (same as the input prompt indicator).

```go
func (m *UserMessage) RenderLines(width int) []string {
    prefix := "❯ " // Match input prompt character
    // Wrap content with 2-space continuation indent
    ...
}
```

### Step 2.3: Standardize Tool Result Gutter Format

**File:** `internal/tui/tools.go` — `renderIndentedResult()`

Ensure exact spacing: `"  ⎿  "` for first line, `"     "` (5 spaces) for
continuation. At the planning snapshot it used `"  ⎿ "` (4 chars) + subtle for continuation.

```go
func renderIndentedResult(styles Styles, content string, width int) string {
    lines := strings.Split(content, "\n")
    out := make([]string, 0, len(lines))
    for i, line := range lines {
        var prefix string
        if i == 0 {
            prefix = "  " + styles.Subtle.Render("⎿") + "  "
        } else {
            prefix = "     " // 5 spaces, matching gutter width
        }
        if ansi.StringWidth(line) > width-5 {
            line = ansi.Truncate(line, width-5, "")
        }
        out = append(out, prefix+styles.Subtle.Render(line))
    }
    return strings.Join(out, "\n")
}
```

### Step 2.4: Add Compact Boundary Message Type

**File:** `internal/tui/chat.go`

Add a new message item type for compact boundaries that renders as:
```
✻ Conversation compacted (ctrl+o for history)
```

This is triggered by `engine.EventCompactBoundary` events. At the planning
snapshot it rendered as a generic system message; the plan proposed a dedicated
visual format.

```go
type CompactBoundaryMessage struct {
    Summary string
    Stats   string
}

func (m *CompactBoundaryMessage) RenderLines(width int) []string {
    line := "✻ Conversation compacted (ctrl+o for history)"
    if m.Stats != "" {
        line += "\n  " + m.Stats
    }
    return []string{dimStyle.Render(line)}
}
```

### Step 2.5: Add Compact Summary Display

When a conversation includes compacted history, show a summary block:

```
● Summarized conversation
  ⎿  Summarized N messages up to this point
     Context: "user provided context"
     (ctrl+o expand history)
```

This renders when the first message in the conversation is a compact summary
(loaded from session resume).

### Step 2.6: Ensure Proper Message Spacing

Reference has `marginY={1}` between messages (1 blank line above and below).
The initial visual target is 1 blank line between distinct
message groups (user→assistant→tools is one group; next user message starts
a new group).

**File:** `internal/tui/chat.go` — gap calculation between items

---

## Post-Parity Modernization Boundary

This section's structural message workflow originally targeted one visible
leader conversation. Runtime thread identity, deterministic bounded replay,
thread selection, Agent transcript switching, compact parent trace, and the
M4.1 semantic rich/raw/transcript history-item contract are now implemented.
Dedicated tool-family renderers, stable streaming regions, explicit
layout/dialog ownership, composer, and session work were completed in the
M0-M7 program indexed by [`migration/history/tui/README.md`](../README.md). The
accepted event contract is in
[`runtime-events.md`](../../../../architecture/tui/contracts/runtime-events.md); the target rendering/event split is documented in
[`migration/reference/tui/modern-coding-agent-synthesis.md`](../../../reference/tui/modern-coding-agent-synthesis.md).

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Assistant messages use `●` bullet (not `⏺`)
- [x] User messages use `●` prefix (adapted from reference `❯` for consistency)
- [x] Tool results use exact `"  ⎿  "` (5-char) gutter format
- [x] Compact boundaries show `✻ Conversation compacted (ctrl+o for history)`
- [x] 1 blank line separates message groups
- [x] Streaming text appears correctly after `●` bullet
- [x] Multi-line user input wraps with 2-space continuation
- [x] Existing markdown rendering and prefix cache remain functional

## Files Modified

- `internal/tui/chat.go` — Message type rendering, spacing, compact boundary
- `internal/tui/tools.go` — Gutter format alignment
- `internal/tui/styles.go` — Any new style constants
