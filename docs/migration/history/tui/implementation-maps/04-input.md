# TUI Implementation Map 04: Input and Commands

**Status:** historical
**Completed:** 2026-07-11

> **Ownership:** historical reference-to-Go evidence map; not an active plan

Current ownership and data flow are defined in
[`architecture/tui/README.md`](../../../../architecture/tui/README.md). Active work is owned by
[`migration/PLAN.md`](../../../PLAN.md).

## Authority and reference anchors

This implementation map is subordinate to [`architecture/tui/README.md`](../../../../architecture/tui/README.md), [`migration/GUIDELINE.md`](../../../GUIDELINE.md), and the file classifications in [`../../manifest.yaml`](../../../manifest.yaml). Its primary reference anchors are:

- `src/components/PromptInput/PromptInput.tsx`
- `src/components/PromptInput/PromptInputModeIndicator.tsx`
- `src/components/PromptInput/inputModes.ts`
- `src/components/PromptInput/inputPaste.ts`
- `src/hooks/useTypeahead.tsx`
- `src/hooks/unifiedSuggestions.ts`
- `src/utils/processUserInput/processSlashCommand.tsx`

The anchors define behavior to review; they are not complete until their manifest entries record Go targets, tests, entrypoints, and known gaps.

## Reference Behavior

### Prompt Input Structure

```
❯ user types here_                    ← ❯ prompt char + text input
                                       ← multiline supported (ctrl+j)
```

- `❯` (U+276F) prompt character, colored with theme (purple/agent color)
- Dims when model is loading/thinking
- Bash mode: `!` replaces `❯`, colored with bash border color
- No visible border around input (text flows freely)
- Cursor at end of text by default

### Input Modes

| Mode | Trigger | Visual | Behavior |
|------|---------|--------|----------|
| Normal | Default | `❯` prompt | Text sent to model |
| Bash | `!` at empty | `!` prompt (amber) | Command executed in shell |
| Command | `/` at empty | `❯` + suggestions overlay | Slash command picker |

### Slash Command Autocomplete

Reference `PromptInput` renders suggestions as an overlay ABOVE the input:

```
┌─────────────────────────────────┐
│ /compact  Compact conversation  │  ← suggestion list (above input)
│ /clear    Clear conversation    │
│ /config   Edit config           │
│ /cost     Show token costs      │
└─────────────────────────────────┘
❯ /co_                               ← input with partial command
```

Features:
- Filtered by prefix as user types
- Arrow keys navigate (highlight with reverse video)
- Tab/Enter accepts selection (fills `/<name> ` into input)
- Esc dismisses
- ~80+ commands in reference; we have a subset

### Key Bindings (Reference)

| Key | Context | Action |
|-----|---------|--------|
| Enter | Normal mode | Submit message to model |
| Ctrl+J | Normal mode | Insert newline |
| Ctrl+C | Any | Interrupt running query / exit confirmation |
| Ctrl+O | Any | Toggle transcript mode (expand view) |
| Up/Down | Empty input | History navigation |
| Up/Down | Command hints visible | Navigate suggestions |
| Tab | Chat focus | Return to editor focus |
| Tab | Command mode, selection | Accept selected command |
| Escape | Command/Bash mode | Exit mode |
| Escape | Normal mode | Clear input |
| Shift+Tab | Any | Cycle permission mode |

### History Navigation

- Up arrow when input is empty → previous history entry
- Down arrow → next history entry (or back to empty/draft)
- History persists across sessions (file-based)
- Current draft is preserved while navigating history

## Implementation State at Closeout

- Three input modes (Normal, Command, Shell) ✓
- `/` triggers command mode with hint list ✓
- Arrow key navigation in command hints ✓
- Tab/Enter accepts hint selection ✓
- History navigation with Up/Down ✓
- History persistence ✓
- Shift+Tab cycles permission mode ✓
- Ctrl+C interrupts ✓
- Ctrl+O expand view ✓
- Shell mode with `!`/`$` prefix ✓
- Multiline with Ctrl+J / Enter on existing multiline ✓
- Large-paste placeholders with retained source and edit validity ✓
- Clipboard/local image placeholders and multimodal leader submit ✓
- Unified `@` file/skill/MCP resource completion ✓
- Rich recent history/thread/rewrite state with text-only persistence ✓
- Busy leader Enter queues/steers; Ctrl+C explicitly cancels active work ✓
- Per-thread queued rows with `/queue list|edit|remove` ✓
- Ctrl+R reverse incremental rich history search ✓
- Ctrl+G ordinary-prompt external editor while runtime work continues ✓
- Ctrl+Z bounded per-thread text/element/cursor undo ✓

## Verification Evidence

- `internal/tui/welcome_lifecycle_test.go` covers command and shell mode
  triggers on the welcome screen, first shell submit transition to chat, and
  shell prompt rendering context.
- `internal/tui/mascot_welcome_test.go` verifies shell input state is surfaced
  in welcome rendering.
- `/vim` now activates the existing modal buffer in the production editor.
  Normal/visual/insert editing keys precede configurable plain-character
  actions, while global control chords and explicit submit/newline gestures
  remain reachable.
- Voice input is not implemented; voice keybinding schema exists, but no
  voice runtime/transport/UI state is wired.

## Gap Analysis at Planning Snapshot

| Aspect | Reference | Current | Gap |
|--------|-----------|---------|-----|
| Input border | None (borderless) | Rounded border box | **Adapted** — border kept for visual clarity |
| Prompt char | `❯` colored | `❯` with EditorPrompt style ✅ | Done |
| Prompt dimming | Dims when loading | Dims via `styles.Subtle` when `a.running` ✅ | Done |
| Suggestions position | Overlay ABOVE input | Above input ✅ | Done |
| Suggestion styling | Boxed overlay with border | HintBorder boxed overlay ✅ | Done |
| Enter behavior | Always submits (shift+enter for newline) | Enter submits; Ctrl+J inserts newline | **Adapted** — Ctrl+J is the reliable newline gesture |
| Escape in normal | Clears input text | `textarea.Reset()` on esc ✅ | Done |
| Double Ctrl+C | Exit confirmation | Double-press within 500ms exits ✅ | Done |
| Command count | ~80+ commands | 66 built-ins + aliases + session plugin commands ✅ | Done (core parity; Anthropic-specific commands excluded) |
| File autocomplete | `useTypeahead` for file paths | Path typeahead in command args via `updateFileHints()` ✅ | **Done** |
| Paste detection | Large paste handling | Bracketed paste bypasses keys; >800 runes becomes retained placeholder | **Done (M5.2)** |
| Configurable actions | Context resolver, chords, user overrides | Nine active contexts, 48 reachable defaults, strict validation, dynamic Help/status | **Done (M5.1-M7.3)** |
| Structured paste | Placeholder retaining full source | Bounded range element, submit expansion, edit prune, thread/history restore | **Done (M5.2)** |
| Images | Clipboard/path attach with model feedback | Async `Ctrl+V`/path load, stable placeholder, multimodal leader submit | **Done (M5.2)** |
| Mentions | File/skill/MCP completion and payload binding | One `@` popup, bounded async index/read, stable-ID payload join | **Done (M5.2)** |
| Busy submit | Queue/steer without implicit replacement | Engine-owned rich queue, tool-round drain or terminal claim, visible per-thread rows | **Done (M5.3)** |
| Cancel | Explicit active-turn cancellation | Ctrl+C drains old stream through terminal and preserves pending queue | **Done (M5.3)** |
| Reverse history | Ctrl+R incremental search | Dedicated hint state, rich preview, older cycling, accept/cancel restore | **Done (M5.4)** |
| External editor | Edit ordinary prompt in `$EDITOR` | Secure temp file, runtime continues, target-thread rejoin, rich reconciliation | **Done (M5.4)** |
| Undo/kill | Undo or kill-buffer semantics | 100-entry per-thread rich undo; Bubbles word delete retained; no kill ring claimed | **Done (M5.4)** |

## Dependencies and entrypoints

- Depends on the shared command registry/dispatcher and engine submission,
  cancellation, queue, and permission-mode APIs.
- Commands representing product actions require headless or ACP equivalents
  where interaction is possible without a terminal.
- Input parsing must remain separate from visual suggestion rendering.

## Historical Implementation Steps

> The steps and code fragments below are design sketches, not API contracts. Re-read the current Go and reference symbols before implementation, preserve shared engine boundaries, and update the manifest when the selected design changes.

### Step 4.1: Move Suggestions Above Input

**File:** `internal/tui/app.go` — `renderEditor()`, `View()`

At the planning snapshot, command hints rendered below the editor. The accepted
target moved them above:

```go
func (a *App) View() string {
    // ...
    // Command hints above editor (not below)
    hints := ""
    if h := a.renderCommandHints(); h != "" {
        hints = a.styles.Subtle.Render(h)
    }

    sections = append(sections, a.renderHeader())
    sections = append(sections, a.chat.Render(a.layout.width, a.layout.chatHeight))
    if hints != "" {
        sections = append(sections, hints) // hints between chat and editor
    }
    sections = append(sections, a.renderEditor()) // editor without hints
    sections = append(sections, a.renderStatus())
    // ...
}
```

Layout calculation must account for hints height being subtracted from chat area.

### Step 4.2: Add Border/Box to Suggestions Overlay

Style the command hints as a bordered overlay rather than plain text:

```go
func (a *App) renderCommandHints() string {
    if len(a.commandHints) == 0 {
        return ""
    }
    // ... existing filter and render logic ...

    // Wrap in a subtle border
    boxStyle := lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(lipgloss.Color("#525252")).
        Padding(0, 1)

    return boxStyle.Width(a.layout.width - 4).Render(content)
}
```

### Step 4.3: Add Escape-to-Clear in Normal Mode

**File:** `internal/tui/app.go` — `handleEditorKey()`

When in normal mode (not command/shell), pressing Escape clears the textarea:

```go
// In handleEditorKey, add case for Escape in normal mode:
case key.Matches(msg, a.keyMap.Escape):
    if a.inputMode == InputCommand || a.inputMode == InputShell {
        // existing: exit mode
        a.exitCommandMode()
        return nil
    }
    // Normal mode: clear input
    if a.textarea.Value() != "" {
        a.textarea.SetValue("")
        return nil
    }
    return nil
```

### Step 4.4: Standardize Enter/Newline Behavior

Reference: Enter always submits. Ctrl+J inserts newline; distinguishable Shift+Enter remains compatible when a terminal emits it.

The accepted target at the planning snapshot was:
- **Enter** → Submit message (even if multiline content exists)
- **Ctrl+J** → Insert newline
- **Shift+Enter** → Insert newline when the terminal reports it distinctly
- **Enter** with empty input → No-op (don't send empty)

Verify this matches current implementation. The current code uses `key.Matches(msg, a.keyMap.Send)` for Enter and `a.keyMap.Newline` for Ctrl+J. Ensure Enter always submits regardless of cursor position within multiline text.

### Step 4.5: Add Double-Press Ctrl+C for Exit

**File:** `internal/tui/app.go`

When not running and no input, require double Ctrl+C to quit (with message):

```go
// Add to App struct:
lastCtrlC time.Time

// In handleKey, ForceQuit case:
if !a.running && a.textarea.Value() == "" {
    if time.Since(a.lastCtrlC) < 500*time.Millisecond {
        a.quitting = true
        return nil
    }
    a.lastCtrlC = time.Now()
    a.chat.AppendSystem("Press Ctrl+C again to exit")
    return nil
}
```

### Step 4.6: Adjust Layout for Hints-Above-Editor

**File:** `internal/tui/layout.go`

When command hints are visible, their height must be accounted for in the
layout calculation. Add a `hintsHeight` parameter:

```go
func computeLayout(width, height, hintsLines int) layout {
    // ... existing calculations ...
    chatHeight -= hintsLines // hints steal from chat area
    // ...
}
```

---

## Post-Parity Modernization Boundary

M5.1 makes `keybindings.Resolver` the App key-action authority. Global, Chat,
Autocomplete, Scroll, Help, Transcript, Task, and MessageSelector resolve with
specific-context precedence and Global fallback. The resolver owns normalized
single keys and multi-step chord state; config merge is defensive and supports
explicit null unbinding. Invalid syntax, context, unsupported/unreachable
action, normalized alias conflict, non-rebindable control keys, terminal-
reserved keys, and unsupported super/cmd modifiers are diagnosed before apply;
an invalid file leaves the last valid map active.

This combines the strongest parts of the references:

- Claude Code's action/context/chord model and runtime shortcut display;
- Codex's refusal to accept ambiguous/reserved configured bindings;
- Crush's principle that visible Help comes from the active key map.

The Go adaptation deliberately advertises only actions with real handlers.
Image paste, external editor, reverse history, and undo now have production
handlers and defaults; stash and Agent kill remain staged schema actions rather
than visible promises. Help, status, task hints, and TUI `/keybindings` read the
resolver; tool renderers use key-neutral “expand for details” wording. Ctrl+C
stays non-rebindable ahead of all focus owners, and Vim editing keys precede
configurable plain characters.

Structured paste/image/mention elements, safe busy queueing, reverse history,
ordinary external editing, and rich per-thread undo are complete. Enter during
a running leader request queues an engine-owned rich snapshot; Ctrl+C is the
explicit cancellation path and continues draining until terminal.

### M5.2 Structured composer contract

`threadComposerElement` is now the single per-thread contract for paste, image,
file, skill, and MCP resource values. The textarea contains a compact label and
the element retains a stable ID, rune range, canonical source, MIME metadata,
and payload. A single-edit rune diff shifts elements strictly before/after an
edit and prunes every intersected or text-mismatched range. Async image/file/MCP
results only mutate a still-present element with the same ID.

The implementation combines the reference strengths without copying their UI
stacks:

- Claude Code's compact ref/payload split and text-only persisted history;
- Codex's range-aware element validity, pending-paste expansion, multimodal
  message parts, and rich in-session/backtrack state;
- Crush's asynchronous attachment I/O, bounded 5 MiB payload policy, active
  image-paste shortcut, and file/MCP completion sources.

Eino adaptation details:

- a draft accepts at most 32 elements, 5 MiB per payload, and 10 MiB retained
  payload total;
- `Ctrl+V` and pasted local image paths load outside Update; valid images are
  sent through `engine.SubmitMessageWithImages` as Eino
  `UserInputMultiContent` and are limited to the leader thread;
- a bounded project file index skips heavy generated/reference directories,
  skills come from the engine registry, and MCP list/read calls have timeouts;
- normal prompts encode file/skill/MCP payloads as JSON `composer_context`
  records; shell/slash surfaces reject attachments rather than executing their
  context representation;
- recent local history and rewrite-selected user messages retain elements;
  cross-session history expands paste text and degrades media/mentions to
  canonical text references.

### M5.3 Safe busy submission

The leader QueryEngine now owns one persistent queue manager. Busy Enter stores
the full composer prompt and image snapshot without cancelling. A completed tool
round consumes it through the existing `queued_command` lifecycle; otherwise the
old terminal schedules an atomic next-turn claim. Agent-thread input continues
through AgentRunner pending messages and retained/evicted resume.

`/queue list|edit|remove` operates only on still-pending runtime items. The TUI
keeps a bounded rich preview for rendering and draft restoration, removes the
matching preview on lifecycle start, and never treats that preview as queue
truth. Ctrl+C cancels the active turn but keeps draining to terminal so pending
input can continue. See [`busy-queue.md`](../../../../architecture/tui/contracts/busy-queue.md).

### M5.4 Editing ergonomics

Ctrl+R now opens a dedicated HistorySearch context rather than prior-message
rewrite. It searches newest-to-oldest, previews recent rich elements, repeats
toward older matches, accepts with Enter, and restores the exact original draft
on Esc, Ctrl+C, no-match, or thread switch. `/rewrite` and `/retry` retain the
existing message selector.

Ctrl+G opens ordinary prompt text through `$EDITOR` using a secure temporary
file and Bubble Tea terminal handoff. Large paste values expand for editing;
return applies only to the original thread, trims trailing whitespace, and
conservatively reconciles element ranges. Ctrl+Z restores at most 100 per-thread
text/element/cursor snapshots and clears after submit. Bubbles word deletion is
undoable; no kill ring is advertised. See
[`editing.md`](../../../../architecture/tui/contracts/editing.md).

## Evidence Rule

The checklist below records the section-level evidence accepted at closeout under [`GUIDELINE.md`](../../../GUIDELINE.md).

## Acceptance Criteria

- [x] Command suggestions appear ABOVE the input (between chat and editor)
- [x] Suggestions have a subtle rounded border
- [x] Escape clears input in normal mode
- [x] Enter always submits (ctrl+j for newline; shift+enter when distinguishable)
- [x] Double Ctrl+C required to quit when idle (with hint message)
- [x] Layout recalculates when hints appear/disappear
- [x] Existing command hint navigation (↑/↓/Tab/Enter) still works
- [x] History navigation still works when hints are not visible
- [x] Shell mode `!` prefix still works
- [x] Active Global/Chat/Autocomplete/Scroll/Help/Transcript/Task/MessageSelector actions resolve through one contextual path
- [x] Chord prefixes are consumed without leaking bytes into the textarea
- [x] Vim editing precedence and Ctrl+J/Shift+Enter/Windows/Alt fallbacks are preserved
- [x] Default Help/status actions are all reachable; scaffold-only actions are not advertised
- [x] Invalid, reserved, conflicting, and unreachable user bindings retain the last valid configuration
- [x] Large paste retains full content while showing a bounded placeholder
- [x] Clipboard/local images reach the model only while their placeholder survives
- [x] File, skill, and MCP resource mentions share one completion/element path
- [x] Edits, thread switches, history recall, and rewrite preserve or safely prune element payloads
- [x] Cross-session history contains text only
- [x] Busy leader Enter queues/steers without cancelling active work
- [x] Ctrl+C preserves pending input and drains the old stream through terminal
- [x] Queued rows remain visible, editable, removable, and isolated per thread
- [x] Tool-round drain and terminal fallback consume each queued input once
- [x] Ctrl+R reverse search accepts rich matches and cancel restores the draft
- [x] Ctrl+G external editing works while running and preserves target ownership
- [x] Ctrl+Z undo is bounded, thread-local, rich, and cleared after submit
- [x] Command/shell/autocomplete/Vim ownership remains deterministic

## Files Modified

- `internal/tui/app.go` — `View()`, `renderEditor()`, `handleEditorKey()`, hint rendering
- `internal/tui/key_actions.go` — contextual action dispatch, Vim boundary, active shortcut projection
- `internal/tui/keybindings/parser.go`, `resolver.go`, `validation.go`, `defaults.go` — chords, matching, merge, validation, reachable defaults
- `internal/tui/key_actions_test.go`, `keybindings/resolver_test.go` — active context, chord, Vim, display, conflict/reserved, rollback evidence
- `internal/tui/composer_elements.go`, `composer_mentions.go` — range contract,
  paste/image/mention integration, async payloads, submission/history semantics
- `engine/user_input.go` — backwards-compatible multimodal user-message builder
- `engine/queued_input.go`, `engine/queue/manager.go` — persistent ownership,
  defensive rich snapshots, bounds, cancel, and terminal claim
- `internal/tui/queued_input.go`, `thread_view_state.go` — pending projection,
  local controls, lifecycle promotion, and per-thread restore
- `internal/tui/composer_history_search.go`, `composer_editor.go`,
  `composer_undo.go` — reverse search, external process handoff, and rich undo
- `internal/tui/composer_elements_test.go`, `composer_mentions_test.go`,
  `engine/user_input_test.go` — element lifecycle, async source, history, and
  model-delivery evidence
- `engine/queued_input_test.go`, `engine/query_queue_drain_test.go`,
  `internal/tui/queued_input_test.go` — queue ownership, tool steering,
  cancellation, fallback, capacity, rich payload, and thread isolation
- `internal/tui/composer_history_search_test.go`, `composer_editor_test.go`,
  `composer_undo_test.go` — query/cycle/cancel, editor round trip/thread guard,
  undo bounds/rich restore, and input-owner precedence
- `internal/tui/layout.go` — Account for hints height
