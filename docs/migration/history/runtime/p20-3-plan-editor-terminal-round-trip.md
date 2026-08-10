# P20.3 Plan Editor Terminal Round Trip

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption, compatibility, rollback, and
> verification evidence for the completed external-editor and terminal
> restoration slice

## Outcome

Ctrl+G now opens the configured editor on the exact engine-owned Plan file and
returns to the same approval context. Focus, selected action, feedback draft,
cursor, undo history, reviewed-byte digest, and viewport survive the round
trip. Direction keys, PageUp/PageDown, mouse wheel, focus reporting, full
screen repaint, and terminal resize continue to work after return.

Process failures and file reload failures remain visible without closing or
settling approval. A callback for a stale thread, request, revision, path, or
dialog generation is ignored before disk is read.

## Adoption And Ownership

P20.3 was a `combine` decision:

- `preserve` P20.0 request/revision/path and reviewed-byte identity plus P20.1
  focus/selection/viewport and P20.2 feedback/cursor/undo state;
- `adapt` the existing composer `x/editor` option path for Plan editing;
- `adapt` Bubble Tea's normal process release/reacquisition lifecycle while
  explicitly restoring the mouse capability it omits;
- `adapt` Claude/Pi full-screen repaint and terminal handoff outcomes; and
- use `project-native` `VISUAL` precedence, monotonic callback generation,
  active thread-attention validation, and one ordered capability helper.

[`externalEditorCommand`](../../../../internal/tui/external_editor.go#L19)
owns shared command resolution. It layers `VISUAL` over `EDITOR` while
retaining `x/editor` argument and editor-position options, the Snap guard, and
separate argv entries for Unicode or spaced paths.

This intentionally changes the Plan-only empty-environment fallback from
`vim` to the same `x/editor` default already used by the composer: `nano` on
Unix and `notepad` on Windows. Explicit `VISUAL` or `EDITOR` remains
authoritative.

[`PlanDialog.editInEditor`](../../../../internal/tui/plan_dialog.go#L534)
captures the runtime and presentation identity.
[`App.applyPlanEditorResult`](../../../../internal/tui/external_editor.go#L106)
validates the active thread-attention request before reload.
[`restoreTerminalCapabilitiesCmd`](../../../../internal/tui/external_editor.go#L67)
owns ordered App terminal reacquisition for Plan, composer, and
suspend/resume.

## Identity And Failure Boundary

The callback identity is the active thread ID, immutable Plan approval request
ID, Plan revision, exact Plan path, and a monotonic dialog generation. The
presentation snapshot contains focus, selected action, viewport offset,
feedback text/cursor, and cloned undo entries.

Identity is checked twice: first against the active thread-attention owner,
then against the visible dialog and its active launch generation. A stale
result can still request terminal restoration when `ExecProcess` was entered,
but it cannot read or replace the current Plan. A matching process or read
error clears only the launch state, leaves the approval open, and surfaces an
error notification.

Successful completion reloads the exact path, computes the reviewed digest,
and restores the presentation snapshot. The next render applies the current
terminal size and clamps only if the new rendered Plan has fewer rows.

## Terminal Boundary

Bubble Tea v1.3.10 restores alternate screen, bracketed paste, renderer focus
reporting, and resize detection after `ExecProcess`, but not mouse tracking or
the App's observed focus state. The shared ordered helper resets observed
focus, clears for repaint, restores eligible focus reporting and mouse cell
motion, and restarts blink unless reduced motion is active.

Composer editor completion and `ResumeMsg` use the same helper. P20.3 changed
no QueryEngine production behavior, Graph topology, permission settlement,
durable schema, or Eino/Eino-ext dependency.

## Verification

Focused unit tests cover `VISUAL` precedence, argument-bearing `EDITOR`,
GUI/terminal position syntax, Unicode and spaced paths, missing editors, the
Snap guard, process failure, exact presentation restoration, and stale
thread/request/revision/path/generation rejection.

The Unix PTY fake-Vim scenario performs two real `ExecProcess` round trips. It
resizes while the child editor owns the terminal and then proves Plan reload,
unchanged viewport and feedback, alternate-screen repaint, arrow selection,
PageUp/PageDown, mouse-wheel scrolling, and repeated mouse/focus restoration.
It runs without `-race`: Bubble Tea v1.3.10 writes its input cancel reader from
`RestoreTerminal.initInput` while the resize listener reads the same field, so
the upstream dependency reports a race before the P20.3 callback returns. The
race build therefore records an explicit skip for only this dependency-owned
PTY case; all project-owned P20.3 resolver, identity, failure, and presentation
tests run under `-race`. The normal PTY acceptance, focused project-owned race,
full TUI package, repository format/lint/test/build/new-lint, documentation,
manifest, and diff gates passed before merge.

## Rollback

Revert the shared resolver, identity-bearing Plan callback, capability
reacquisition helper, PTY/unit tests, and documentation as one unit. Typed Plan
approval and the P20.1/P20.2 dialog state remain valid, but Ctrl+G must be
disabled rather than restoring the known broken handoff.

Current behavior is owned by
[`architecture/tui/contracts/editing.md`](../../../architecture/tui/contracts/editing.md)
and
[`architecture/tui/contracts/terminal-lifecycle.md`](../../../architecture/tui/contracts/terminal-lifecycle.md).
