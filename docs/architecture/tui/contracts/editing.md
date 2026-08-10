# TUI Editing Contract

**Status:** current

**Last verified:** 2026-08-07

**Ownership:** `internal/tui.App` owns reverse history search and
external-editor handoff; the composer and Plan dialog own independent drafts
over one shared bounded textarea/snapshot/undo contract.

This contract extends [`composer.md`](composer.md). Every
editing operation preserves the selected thread's text, structured elements,
and cursor or deliberately rejects a stale result.

## Reverse History Search

[`composerHistorySearch`](../../../../internal/tui/composer_history_search.go)
stores the original rich draft, query, matching history indices, and selected
match.

- `Ctrl+R` starts search or advances to an older match.
- Matching is case-insensitive substring search, newest first.
- `Enter` accepts the preview without submitting it.
- `Esc` or `Ctrl+C` restores the original rich draft.
- Thread switching cancels search before another input owner activates.
- Search owns keys before ordinary textarea, Vim, and autocomplete handling.

No-match state restores the original draft while keeping the search query
visible. Search performs no I/O on a keypress; it scans the history already
loaded by the App.

## External Editor

[`externalEditorCommand`](../../../../internal/tui/external_editor.go)
is the shared composer/Plan resolver. It retains `x/editor` option behavior and
adds conventional `VISUAL` over `EDITOR` precedence; argument-bearing editor
values and Unicode or spaced file paths remain separate argv entries. The
upstream Snap guard stays fail closed. The composer expands retained paste
text into a temporary Markdown file before Bubble Tea `ExecProcess`.

When neither `VISUAL` nor `EDITOR` is configured, the accepted `x/editor`
platform default is nano on Unix and notepad on Windows. The footer currently
shows only that display name, not whether it came from `VISUAL`, `EDITOR`, or
the platform default. This is a discoverability limitation rather than an
external-editor lifecycle failure.

The completion message carries the target thread ID. The App applies the result
only if that thread is still active
([`applyComposerEditorResult`](../../../../internal/tui/composer_editor.go)).
A changed target is rejected without replacing either draft.

External editing is available only for ordinary prompts. Slash and shell modes
retain their typed parsing boundary. General editor output is reconciled using
the composer range rule; intersected elements are removed rather than guessed.

Plan launches carry the active thread, approval request, Plan revision, exact
path, monotonic dialog generation, and a presentation snapshot containing
focus, selection, feedback, cursor, undo, and viewport. Completion validates
that identity against the active thread-attention request before reading disk.
A stale result is ignored; a process or read error remains visible without
closing or settling approval. Successful reload updates the reviewed-byte
digest and restores the snapshot, with current terminal geometry clamping the
viewport on render.

After either editor returns,
[`restoreTerminalCapabilitiesCmd`](../../../../internal/tui/external_editor.go)
resets observed focus, requests a full repaint, and restores eligible focus
reporting, mouse cell motion, and blink in deterministic order. Suspend/resume
uses the same helper.

## Undo

[`composerUndoEntry`](../../../../internal/tui/composer_undo.go) captures full
text, cloned elements, and rune cursor offset. Each thread retains at most 100
entries. Text-changing composer operations record the prior state; successful
submission clears the stack so undo cannot resurrect sent input.

Undo, active history search, and external-editor transient state are
presentation-only and are not persisted in the session view sidecar.

## Ordinary History Recall

Up/Down recall and autocomplete have separate ownership. When history places
an entry into the composer, the exact untouched recalled text suppresses
command, file, and mention candidates. This keeps Up/Down assigned to history
traversal even when the recalled entry starts with `/`. The first edit makes
the text differ from the recall marker and re-enables normal hints.

Input-mode synchronization still follows the recalled text: a slash entry
enters command dispatch mode, while a later plain-text entry exits it. The
marker is presentation-only and is cleared when traversal returns to the
draft.

## Plan Feedback

The final Plan action opens a separate Bubbles textarea owned by
[`PlanDialog`](../../../../internal/tui/plan_dialog.go). It uses the same
bounded construction, rune-cursor snapshot/restore, and max-100 undo helpers as
the composer
([`newBoundedTextarea`](../../../../internal/tui/text_editor.go)), but it
never shares composer text, structured elements, history, or undo entries.

Feedback is multiline and resolves submit, newline, and undo through the App's
configured keybinding resolver. The visible footer renders those effective
bindings rather than hardcoded defaults. While Feedback owns focus, key and
non-key textarea messages—including paste completion and cursor ticks—remain
inside the modal. Pending multi-key chords are reset whenever focus ownership
changes.

Whitespace-only submit returns to Actions without settling. A nonempty submit
produces explicit typed Revise feedback; Esc returns to Actions while
preserving the draft, cursor, and undo stack. `PlanDialog.PlanResult` is the
only TUI Plan-intent source: Actions/Review Esc and force-close produce typed
Cancel with empty decision feedback even when the editor retains a draft.
The generic permission response channel is only a completion signal; missing
explicit Plan intent fails closed and cannot reconstruct Revise from widget
text.

Selecting a bypass target does not settle the dialog. `PlanDialog` replaces
the action rows with a visible risk statement and explicit No/Yes choices,
defaults to No, and requires a second Enter on Yes before emitting
`Confirmed=true`. No or Esc returns to Actions without changing the review
offset, feedback draft, cursor, or undo stack.

Theme changes, resize, and Plan reload preserve the same state. The
identity-matched external-editor round trip preserves that same snapshot and
has real PTY evidence. The semantic cursor token is stored in Bubbles'
pre-reversal form, producing a brand-background final cell in every color
profile. `App` explicitly selects the no-color path from terminal
capabilities; a width-reserved render-only textarea clone inserts one literal
`▏` at the logical cursor while leaving the current character visible. It
does not change submitted bytes, rune cursor, undo, focus, viewport, or
runtime state. Blur and blink-hidden frames omit the caret; reduced motion
keeps it statically visible. Final-cell emulation, normalized goldens, and a
real color/no-color PTY capture cover empty/start/middle/end positions.

The shared Bubbles v1 textarea still edits by rune. It preserves valid UTF-8
and renders CJK, combining, and ZWJ sequences, but this contract does not claim
grapheme-atomic deletion. The completed
[`DisplayCellProfile`](../../../../internal/tui/display_cell.go) owns measured
render geometry and width-stable projection; it deliberately does not replace
the textarea's editing unit.

## Key Ownership

`Ctrl+R` belongs to reverse search and `Ctrl+G` to the external editor.
Composer and Plan undo use the effective chat undo binding, `Ctrl+Z` by
default; Plan submit and newline likewise use the configured chat actions.
Conversation rewrite remains a separate `/rewrite` or `/retry` workflow.
Product keybindings such as the command palette and chat scrolling are not
reinterpreted as textarea kill-ring operations.

## Evidence

- search behavior: [`TestReverseHistorySearchCyclesOlderAndCancelRestoresRichDraft`](../../../../internal/tui/composer_history_search_test.go)
- editor stale-target and cleanup behavior: [`TestExternalEditorRejectsWrongThreadResult`](../../../../internal/tui/composer_editor_test.go)
- shared resolver and Plan callback identity:
  [`TestP203ExternalEditorCommandResolution`](../../../../internal/tui/external_editor_test.go)
- real Plan editor terminal round trip:
  [`TestP203PlanEditorRoundTripPTY`](../../../../internal/tui/plan_editor_pty_unix_test.go)
  (normal Unix suite; after two editor handoffs it verifies the visible
  feedback cursor and explicit risk/No/Yes bypass-confirmation frame; the race
  build explicitly skips Bubble Tea v2.0.8's dependency-owned restore/resize
  race)
- bounded per-thread undo: [`TestComposerUndoIsBoundedAndClearsAfterSubmit`](../../../../internal/tui/composer_undo_test.go)
- shared-but-independent Plan feedback editing:
  [`TestP202FeedbackEditorSupportsComposerEditingContract`](../../../../internal/tui/plan_feedback_editor_test.go)
- configured Plan key ownership:
  [`TestP202FeedbackUsesConfiguredSubmitNewlineAndUndoKeys`](../../../../internal/tui/plan_feedback_editor_test.go)
- Plan feedback final-cell, no-color projection, layout/Unicode, and
  focus/blink/reduced-motion behavior:
  [`TestP20R2FeedbackCursorFinalCellsAcrossProfilesAndPositions`](../../../../internal/tui/plan_feedback_editor_test.go)
- real color/no-color Plan feedback cursor frame:
  [`TestP20R2FeedbackCursorPTY`](../../../../internal/tui/plan_feedback_cursor_pty_unix_test.go)
- explicit Plan terminal intent and bypass-back state preservation:
  [`TestP20R1BypassConfirmationBackPreservesPresentationAndCancelIsExplicit`](../../../../internal/tui/plan_dialog_state_test.go)
- visible two-action bypass confirmation with default No:
  [`TestP20R3BypassConfirmationIsVisibleAndRequiresDistinctChoice`](../../../../internal/tui/plan_dialog_state_test.go)
- fail-closed generic-response isolation:
  [`TestPermissionInteractionResultFailsClosedWithoutPlanTerminalResult`](../../../../internal/tui/permission_lifecycle_test.go)
