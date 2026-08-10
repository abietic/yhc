# TUI Implementation Map 08: Sessions, Tasks, and Agents

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/screens/ResumeConversation.tsx`
- `src/components/TaskListV2.tsx`
- `src/components/AgentProgressLine.tsx`
- `src/components/CoordinatorAgentStatus.tsx`
- `src/components/tasks/RemoteSessionProgress.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### Task List (TaskListV2)

The reference shows active tasks inline during spinner:

```
⠋ Thinking…
  ├─ ☐ Implement auth module          ← pending task
  ├─ ☑ Add unit tests                 ← completed task
  └─ ⠙ Fix login validation           ← in-progress task (animated)
```

Features:
- Tasks created via TaskCreate tool
- Shown inline with spinner during generation
- Expandable with ctrl+o
- Task status: pending (☐), in-progress (spinner), completed (☑)

### Sub-Agent / Teammate Display

```
⠋ Thinking…
  ├─ Agent: implement auth   ⠙ Writing code…
  └─ Agent: fix tests        ⠹ Running tests…
```

- Each sub-agent shows its own spinner and status
- Color-coded per agent (8 agent colors)
- Shows tool use count and duration on completion
- Tree structure for nested agents

### Session Resume

Current resume dialog exists ✓ but could be enhanced:

Reference shows:
- Session list with timestamps
- Search/filter by content
- Preview of last messages
- Branch/fork indicators

### Compact Summary in Resumed Sessions

When resuming, first message shows:
```
● Summarized conversation
  ⎿  Summarized 42 messages up to this point
     Context: "working on auth module"
     (ctrl+o expand history)
```

### Background Tasks

```
                                            2 in background
```

Right-aligned dim text when background tasks are running.

## Implementation State at Closeout

- Resume dialog with search ✓
- Basic session info display ✓
- Sub-agent task tree with `renderTaskTree()` below spinner ✓
- `EventTaskProgress` handler updates `activeTasks` map ✓
- Bounded TaskCreate/TaskUpdate/TaskStop task-list entries render in the same
  inline task tree via `EventTaskLifecycle` ✓
- Tasks cleared on query end (terminal / interruption) ✓
- Compact summary for resumed sessions (`CompactSummaryMessage`) ✓
- Bounded engine-owned AppState task lifecycle store/snapshot API observes task
  lifecycle/progress events, unblocking the engine prerequisite for the
  AppState-backed task panel migration ✓
- Background task count shown in status bar when tasks are active ✓
- Ctrl+T, Ctrl+B, `/team`, inline Agent rows, and the background count consume
  one canonical-first engine selector ✓
- Agent launch identity/lineage/model/isolation/transcript/output metadata is
  canonical and durable before the first response ✓
- Ctrl+B and `/team` share bounded Overview, Activity, Transcript, Output, and
  Lineage detail for running, terminal, retained, and evicted Agents ✓
- The same detail views queue running-Agent input, resume retained/evicted
  Agents, pause/resume at safe query/tool-round boundaries, and abort through
  the engine-scoped runner ✓
- Parent Agent tool items show a three-activity child tail plus attention and
  terminal state, and open the existing full detail instead of embedding it ✓
- Session discovery uses bounded cursor pages, CWD/repository/all root scopes,
  backend sort/filter, moving-page deduplication, and stable TUI selection ✓
- The picker has explicit resume/fork modes, bounded recent transcript preview,
  full searchable transcript/return, and rich available metadata ✓
- Resume restores selected-source model/permission/CWD/worktree/scope and safe
  bounded view state; live request IDs and Agent threads require an actual
  in-process owner, otherwise Agent transcripts are replay-only ✓
- P10 PTY evidence covers a 300-row leader trace, concurrent Agents,
  inactive-owner approval, searchable leader/child switching, and failed
  evicted transcript projection; a separate process restart proves replay-only
  recovery with lineage and transcript intact ✓

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Priority |
|--------|-----------|---------|----------|
| Task list display | Inline with spinner | Bounded local TaskCreate/TaskUpdate/TaskStop entries render in existing task tree ✅; bounded engine-owned AppState lifecycle snapshots now available for task-panel migration ✅ | Engine prerequisite unblocked; full reference AppState/task-panel parity remains partial |
| Task status icons | ☐/☑/spinner | Bounded status icons: pending/in-progress/completed/failed/killed in the current task tree ✅ | **Done for bounded slice** |
| Sub-agent tree | Color-coded tree | `renderTaskTree()` with tree lines + per-task spinner ✅ | **Done** |
| Background count | Right-aligned indicator | `backgroundTaskCount()` in status bar ✅ | **Done** |
| Resume preview | Message preview in picker | Four-message bounded tail preview plus explicit full searchable transcript ✅ | **Done** |
| Compact summary | Structured first message | `CompactSummaryMessage` with metadata ✅ | **Done** |
| Session branch | Fork/branch indicators | Branch indicator (⎇) shown per-session in resume dialog; Ctrl+B toggles branch filter to current git branch ✅ | **Done** |
| Restore context | Messages plus runtime context | Model/permission/CWD/worktree/scope, safe view sidecar, request intersection, and live/replay Agent recovery ✅ | **Done** |

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 8.1: Render Task List with Spinner

**File:** `internal/tui/app.go` or new `internal/tui/tasks.go`

When tasks exist (from TaskCreate tool), show them below the spinner:

```go
type TaskDisplay struct {
    Subject string
    Status  string // "pending", "in_progress", "completed"
}

func (a *App) renderTaskList() string {
    if len(a.activeTasks) == 0 {
        return ""
    }
    var sb strings.Builder
    for i, task := range a.activeTasks {
        connector := "├─"
        if i == len(a.activeTasks)-1 {
            connector = "└─"
        }
        icon := taskIcon(task.Status, a.spinnerCount)
        sb.WriteString(fmt.Sprintf("  %s %s %s\n", connector, icon, task.Subject))
    }
    return sb.String()
}

func taskIcon(status string, spinnerCount int) string {
    switch status {
    case "completed":
        return "☑"
    case "in_progress":
        return spinnerFrame(spinnerCount)
    default:
        return "☐"
    }
}
```

### Step 8.2: Track Tasks from Engine Events

The engine emits task-related events. Add task tracking to App:

```go
// Add to App struct:
activeTasks []TaskDisplay

// On task create event:
a.activeTasks = append(a.activeTasks, TaskDisplay{...})

// On task update event:
// Find and update status

// On task completion:
// Mark as completed, remove after delay
```

### Step 8.3: Add Sub-Agent Display (When Available)

When the Agent tool spawns sub-agents, display them in a tree. This requires
the engine to emit sub-agent lifecycle events.

```go
type AgentDisplay struct {
    Description string
    Status      string
    ToolUses    int
    Duration    time.Duration
}

func (a *App) renderAgentTree() string {
    if len(a.activeAgents) == 0 {
        return ""
    }
    // Similar tree rendering as tasks
    // Color per agent using a rotating palette
}
```

### Step 8.4: Add Background Task Counter

When tasks are running in the background (after user continues with new input):

```go
func (a *App) renderBackgroundCount() string {
    count := a.backgroundTaskCount()
    if count == 0 {
        return ""
    }
    text := fmt.Sprintf("%d in background", count)
    return a.styles.Subtle.Render(text)
}
```

Display right-aligned in the status area or as a floating indicator.

### Step 8.5: Enhance Resume Dialog with Preview

**File:** `internal/tui/resume_dialog.go`

Add message preview to session entries:

```go
type SessionInfo struct {
    // existing fields...
    LastMessage string // preview of last user/assistant message
}

func (d *ResumeDialog) renderSessionEntry(s SessionInfo, selected bool) string {
    // Show: timestamp + session ID + truncated last message
    preview := truncate(s.LastMessage, 60)
    // ...
}
```

### Step 8.6: Add Compact Summary for Resumed Sessions

When a session is resumed and starts with compacted history, show a
structured compact summary message (see Section 2, Step 2.5).

---

## Post-Parity Modernization Boundary

The engine now provides one canonical bounded Agent/thread snapshot and
deterministic in-memory event replay. Agent detail and exact `/agent` navigation
now provide full live/retained/evicted child transcripts with per-thread view
state, target-aware follow-up, and owner-scoped unresolved attention. M6 adds
durable execution checkpoints, safe sidecar state, cross-CWD/worktree restore,
and explicit live/replay Agent recovery. Its accepted boundary is in
[`sessions.md`](../../../../architecture/tui/contracts/sessions.md). Responsive, terminal, and product
hardening are complete under the current
[`architecture/tui/README.md`](../../../../architecture/tui/README.md), with the full source comparison in
[`migration/reference/tui/modern-coding-agent-synthesis.md`](../../../reference/tui/modern-coding-agent-synthesis.md).

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Active tasks render as a tree below spinner for bounded running-agent progress and local task-list lifecycle slices
- [x] Task icons reflect basic local task-list status semantics (pending, in-progress, completed, failed, killed)
- [x] Task list disappears when all tasks complete
- [x] Sub-agent tree shows when agents are running (engine emits `EventTaskProgress`)
- [x] Background task count shown when applicable
- [x] Resume dialog shows message preview (when data available)
- [x] Performance: task rendering doesn't impact streaming

## Dependencies and entrypoints

- Bounded task lifecycle events (create, update, stop) now flow through
  `EventTaskLifecycle` and fold into the engine-owned AppState task lifecycle
  snapshot API; the full reference AppState/task-panel lifecycle remained
  outside this closed program
- Sub-agent lifecycle events (start, progress, complete)
- Background task tracking
- Session message preview in metadata
- These all require engine-side work before TUI can display them

## Files Modified

- `internal/tui/app.go` — Task/agent state, render integration
- `internal/tui/spinner.go` or new `internal/tui/tasks.go` — Task rendering
- `internal/tui/resume_dialog.go` — Enhanced session display
