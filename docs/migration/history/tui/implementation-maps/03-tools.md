# TUI Implementation Map 03: Tool Calls and Results

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/messages/AssistantToolUseMessage.tsx`
- `src/components/messages/UserToolResultMessage/UserToolSuccessMessage.tsx`
- `src/components/messages/UserToolResultMessage/UserToolErrorMessage.tsx`
- `src/tools/BashTool/UI.tsx`
- `src/tools/FileReadTool/UI.tsx`
- `src/tools/FileWriteTool/UI.tsx`
- `src/tools/GrepTool/UI.tsx`
- `src/tools/AgentTool/UI.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### Tool Use Header (One-Line Invocation)

```
● Read (src/utils/file.ts)                        ← resolved (green ●)
⠋ Bash (npm test)                                 ← in-progress (animated)
● Write (src/new-file.ts)                         ← resolved
● Grep (pattern: "TODO", path: "src")             ← resolved
● Agent (implement feature X)                     ← resolved
```

Structure: `[status_icon] [ToolName] ([renderToolUseMessage output])`

- **Status icons:**
  - Queued: `●` dimmed (gray)
  - In-progress: Animated braille spinner `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`
  - Success: `●` green (solid)
  - Error: `●` red (solid)

- **Tool name:** Bold text (reference has background color per tool, we adapt to bold+cyan)

- **Arguments rendering** (per tool):
  - `Bash`: Command string (max 2 lines, max 160 chars, `…` on truncation)
  - `Read`: File path only (shortened via display path)
  - `Write`: File path only
  - `Edit`: File path only
  - `Grep`: `pattern: "pattern", path: "path"`
  - `Glob`: `pattern: "pattern", path: "path"`
  - `Agent`: Description string from input

### Tool Result (MessageResponse Pattern)

```
● Read (src/utils/file.ts)
  ⎿  Read 42 lines

● Bash (npm test)
  ⎿  ✓ 12 tests passed
     output line 2
     output line 3
     ... (+7 line(s)) (ctrl+o to expand)

● Write (src/new-file.ts)
  ⎿  Wrote 15 lines to src/new-file.ts
     [first 10 lines of code...]
     … +5 lines (ctrl+o to expand)

● Agent (implement feature X)
  ⎿  Done (5 tool uses · 2,340 tokens · 12.3s)
     (ctrl+o to expand)
```

### Collapse Rules

| Tool | Max visible lines | Collapse text |
|------|-------------------|---------------|
| Bash | 10 lines | `... (+N line(s)) (ctrl+o to expand)` |
| Read | Show summary only: "Read N lines" | N/A (single line) |
| Write | 10 lines of code | `… +N lines (ctrl+o to expand)` |
| Edit | Show diff summary | Structured diff with hunk headers, dual line numbers, colored ±lines ✅ |
| Grep | Summary: "Found N files" | `(ctrl+o to expand)` |
| Glob | Summary: "Found N files" | `(ctrl+o to expand)` |
| Agent | Summary: "Done (N tool uses · size)" | `(ctrl+o to expand)` |
| Others | 10 lines | `... (+N line(s)) (ctrl+o to expand)` |

### Tool Error Display

```
● Bash (rm /protected)
  ⎿  Error: permission denied
     /bin/rm: cannot remove '/protected': Permission denied
```

- Error text in red color
- Same `⎿` gutter pattern
- Max 10 lines, then truncated with expand hint

## Implementation State at Closeout

- Tool header: `● **ToolName** (structured args)` — bold name, parens around args ✅
- Icons: Blinking `●`/space for running ✅, `●` green for success ✅, `●` red for error ✅
- Arguments: Ordered by tool-specific priority, per-tool format ✅
  - Bash: command directly (max 160 chars, 2-line join) ✅
  - Read/Write/Edit: shortened file path with OSC 8 hyperlinks ✅
  - Grep/Glob: `pattern: "...", path: "..."` ✅
  - Agent: description string ✅
- Collapse: Line-based with tool-specific limits ✅
- Collapsed tool groups: consecutive Read/Grep/Glob collapsed into summary line ✅
- Read result: "Read N lines" summary + syntax highlighting (chroma) ✅
- Write result: first 10 lines + overflow ✅
- Edit result: LCS-based structured diff with hunk headers (`@@ -X,Y +A,B @@`), dual line number gutter, colored additions/deletions, and context lines ✅
- Grep/Glob result: "Found N files/lines" summary ✅
- Agent result: "Done (N tool uses, ↓ size)" ✅
- Gutter: `"  ⎿  "` (5-char prefix) ✅
- Expand hint: `(ctrl+o to expand)` ✅
- Error results: red color via `styles.Error` ✅
- Live shell progress: streaming last 5 stdout lines during Bash execution ✅
- OSC 8 terminal hyperlinks for file paths ✅

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Gap |
|--------|-----------|---------|-----|
| Bullet character | `●` (not `⏺`) | `●` ✅ | Done |
| Tool name format | Bold + background color per tool | Bold `ToolName` + per-category bg ✅ | Done |
| Bash arg display | Command only (max 160 chars, 2 lines) | Command directly, truncated ✅ | Done |
| Read result | "Read N lines" (single line) | "Read N lines" summary ✅ | Done |
| Write result | "Wrote N lines to path" + code preview | First 10 lines + overflow ✅ | Done |
| Grep/Glob result | "Found N files/matches" summary | "Found N files/lines" summary ✅ | Done |
| Agent result | "Done (N tool uses · tokens · time)" | "Done (N tool uses, ↓ size)" ✅ | Done |
| Error color | Red text for error content | Red via `styles.Error` ✅ | Done |
| Queued state | Dim `●` while waiting | `styles.Subtle.Render("●")` for default ✅ | Done |

## Dependencies and entrypoints

- Depends on stable tool-call IDs, normalized tool inputs/results, lifecycle
  events, and task/sub-agent metadata from the engine.
- TUI collapse is presentation only; complete results must remain available in
  transcripts and protocol entrypoints.
- Permission and cancellation outcomes must not be inferred from display text.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 3.1: Change Tool Icon to `●`

**File:** `internal/tui/tools.go` — `toolIcon()`

```go
func toolIcon(styles Styles, status ToolStatus, spinnerCount int) string {
    switch status {
    case ToolRunning:
        return styles.ToolRunning.Render(spinnerFrame(spinnerCount))
    case ToolSuccess:
        return styles.ToolSuccess.Render("●")  // Was: "⏺"
    case ToolError:
        return styles.ToolError.Render("●")    // Was: "⏺"
    default:
        return styles.Subtle.Render("●")       // Was: "⏺"
    }
}
```

### Step 3.2: Separate Tool Name from Arguments in Display

**File:** `internal/tui/tools.go` — `renderToolHeader()`

Current: `ToolName(arg1: val1, arg2: val2)` — all plain text.
Target: `**ToolName** (arg_summary)` — name is bold+cyan, args in parens.

```go
func renderToolHeader(styles Styles, name string, status ToolStatus, input string, spinnerCount int) string {
    icon := toolIcon(styles, status, spinnerCount)
    styledName := styles.ToolName.Render(name)
    args := formatToolArgs(name, input)
    if args == "" {
        return fmt.Sprintf("%s %s", icon, styledName)
    }
    return fmt.Sprintf("%s %s (%s)", icon, styledName, args)
}
```

### Step 3.3: Simplify Bash Tool Argument Display

For Bash tool, show the command directly (not as `command: "value"` key-value):

```go
func formatToolArgs(toolName, input string) string {
    if input == "" || input == "{}" {
        return ""
    }
    var params map[string]any
    if err := json.Unmarshal([]byte(input), &params); err != nil {
        return truncate(input, 80)
    }

    switch toolName {
    case "Bash":
        if cmd, ok := params["command"].(string); ok {
            return truncateBashCommand(cmd, 160)
        }
    case "Read":
        if fp, ok := params["file_path"].(string); ok {
            return shortenPath(fp)
        }
    case "Write":
        if fp, ok := params["file_path"].(string); ok {
            return shortenPath(fp)
        }
    case "Edit":
        if fp, ok := params["file_path"].(string); ok {
            return shortenPath(fp)
        }
    case "Agent":
        if desc, ok := params["description"].(string); ok {
            return truncate(desc, 80)
        }
    case "Grep":
        return formatGrepArgs(params)
    case "Glob":
        return formatGlobArgs(params)
    }

    // Fallback: key-value format
    return formatKeyValueArgs(toolName, params)
}

func truncateBashCommand(cmd string, max int) string {
    // Max 2 lines, max 160 chars
    lines := strings.SplitN(cmd, "\n", 3)
    if len(lines) > 2 {
        cmd = strings.Join(lines[:2], "\n") + "…"
    }
    if len(cmd) > max {
        cmd = cmd[:max-1] + "…"
    }
    return cmd
}
```

### Step 3.4: Add Structured Tool Result Rendering

**File:** `internal/tui/tools.go` — `formatToolOutput()`

Add structured result formats for specific tools:

```go
func formatToolOutput(toolName, output string, expanded bool, width int) string {
    switch toolName {
    case "Read":
        return formatReadResult(output, expanded)
    case "Write":
        return formatWriteResult(output, expanded, width)
    case "Grep", "Glob":
        return formatSearchResult(toolName, output, expanded)
    case "Bash":
        return formatCollapsedLines(output, expanded, 10, "line(s)")
    case "Agent", "Explore", "Plan":
        return formatAgentOutput(output, expanded)
    default:
        return formatCollapsedLines(output, expanded, 10, "line(s)")
    }
}

func formatReadResult(output string, expanded bool) string {
    if expanded {
        return output
    }
    lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
    return fmt.Sprintf("Read %d lines", len(lines))
}

func formatWriteResult(output string, expanded bool, width int) string {
    if expanded {
        return output
    }
    lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
    if len(lines) <= 1 {
        return output // Short result, show as-is
    }
    // Show first line (summary like "Wrote N lines to path") + code preview
    const maxPreview = 10
    if len(lines) <= maxPreview {
        return strings.Join(lines, "\n")
    }
    visible := lines[:maxPreview]
    hidden := len(lines) - maxPreview
    return strings.Join(visible, "\n") + "\n" +
        fmt.Sprintf("… +%d lines (ctrl+o to expand)", hidden)
}

func formatSearchResult(toolName, output string, expanded bool) string {
    if expanded {
        return output
    }
    lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
    count := len(lines)
    unit := "files"
    if toolName == "Grep" {
        unit = "matches"
    }
    summary := fmt.Sprintf("Found %d %s", count, unit)
    if count > 0 {
        summary += " (ctrl+o to expand)"
    }
    return summary
}
```

### Step 3.5: Color Error Results Red

**File:** `internal/tui/tools.go` — `renderIndentedResult()`

When the tool status is `ToolError`, render the content in error color (red)
instead of subtle (gray):

```go
func renderToolBody(styles Styles, name, output string, status ToolStatus, expanded bool, width int) string {
    // ... existing logic ...
    contentStyle := styles.Subtle
    if status == ToolError {
        contentStyle = styles.Error
    }
    return renderIndentedResultWithStyle(styles, contentStyle, formatted, bodyWidth)
}
```

### Step 3.6: Handle Multiline Bash Commands in Header

When a bash command spans multiple lines, show only the first meaningful line
in the header with `…` to indicate more:

```go
func truncateBashCommand(cmd string, max int) string {
    cmd = strings.TrimSpace(cmd)
    lines := strings.Split(cmd, "\n")
    if len(lines) == 1 {
        if len(cmd) > max {
            return cmd[:max-1] + "…"
        }
        return cmd
    }
    // Multi-line: show first line + ellipsis
    first := strings.TrimSpace(lines[0])
    if len(first) > max-1 {
        first = first[:max-2] + "…"
    } else {
        first += "…"
    }
    return first
}
```

---

## Post-Parity Modernization Boundary

The current formatter provides broad structural tool coverage. The completed
M4.1 slice indexed by [`migration/history/tui/README.md`](../README.md) supplied
semantic identity/version/finalization, rich/raw/height behavior, optional
capabilities, and legacy adapters beneath the existing O(viewport) cache.
M4.2 is complete. Every dedicated renderer and the generic fallback expose
bounded compact/rich output plus complete expanded/raw/transcript forms where
relevant. The Bash family is now migrated: Bash, BashOutput, and KillShell share
one dedicated strategy with foreground/background semantics, explicit textual
status, head/tail collapse, full transcript output, and a tested generic
fallback. Read/Grep/Glob are now migrated as well: Read retains highlighted
head/tail content, search rows expose result counts and truncation, full
projections keep all stored output, and Explore groups retain stable nested
IDs. Edit/Write/diff are now migrated too: normal history bounds semantic diff
rows, expanded and transcript views retain full hunks, and soft guard/no-op
results are labeled `not applied`. Agent/nested trace is migrated too: parent
rows expose the child lifecycle and lineage, retain only bounded recent
activity, navigate to the canonical detail view, keep child-aware animation,
and provide complete raw/transcript metadata without embedding the full child
conversation. MCP is migrated too: first-class and legacy calls resolve to one
server/tool identity, compact arguments redact credential-shaped keys, normal
JSON/content-block output is structured and bounded, protocol failures and
large responses are explicit, and expanded/raw/transcript forms retain the
complete stored payload. Plan/task/todo is migrated too: plan transitions and
approval states hide boilerplate without losing plan evidence; task CRUD/list/
retrieval calls expose IDs, dependencies, lifecycle, and bounded output; Todo
shows ratio, active form, and ordered check-state. Historical Agent calls named
`Task` still use the Agent renderer. WebFetch/WebSearch is migrated too: Fetch
distinguishes redirects, AI fallback, truncation and HTTP/network failure;
Search preserves query/filter/source evidence through bounded safe links; both
sanitize remote terminal controls and retain complete full projections. The
completion audit classifies all 41 defaults, pins the 19 generic defaults,
redacts compact credentials, bounds normal line/column work with explicit
omission, and keeps unknown dynamic tools plus complete full projections.
M2.4 now provides the narrower parent Agent history contract: three bounded
child activities plus attention/terminal state and a link into the existing
detail view. M4 may refine that item without duplicating the child transcript
in the parent card.

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Tool icons use `●` character (not `⏺`)
- [x] Tool name is bold, separated from args by space+parens
- [x] Bash tool shows command directly (not `command: "..."`)
- [x] Read tool result shows "Read N lines" (one-line summary)
- [x] Write tool result shows summary + first 10 lines of code
- [x] Grep/Glob results show "Found N files/lines" summary
- [x] Agent results show "Done (N tool uses · size)"
- [x] Error results render in red color
- [x] Collapse hints say `(ctrl+o to expand)`
- [x] Expanded view (ctrl+o) shows full content
- [x] Existing tool tests still pass
- [x] Every registered default is classified as dedicated or audited generic
- [x] Unknown/plugin tools retain bounded rich and complete raw/transcript forms

## Files Modified

- `internal/tui/tools.go` — Icon, header format, arg display, result format
- `internal/tui/chat.go` — Pass tool status to body renderer
- `internal/tui/styles.go` — Ensure ToolName style is bold+cyan
- `internal/tui/tool_history_*.go` — Semantic family renderers and bounded/full projections
