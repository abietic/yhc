# Terminal Capability and Lifecycle Contract

**Status:** current

**Last verified:** 2026-08-07

**Ownership:** `terminalcap.Capabilities` owns immutable startup facts;
`terminalcap.FocusState` owns observed focus; the declarative Bubble Tea View
owns normal terminal modes; `restoreTerminalCapabilitiesCmd` owns observed
focus reset, repaint, and virtual-cursor restart after terminal reacquisition;
`TerminalOutput` owns final application writes; `PanicRecovery` is a defensive
fallback.

## Detection and Activation

[`terminalcap.Current`](../../../../internal/tui/terminalcap/capabilities.go)
probes platform, TTY, environment, SSH/multiplexer, color, enhanced-key
availability, focus eligibility, hyperlinks, image protocol identity, mouse,
bracketed paste, and suspend support once at startup.

Detection is not activation:

| Capability | Current activation boundary |
|---|---|
| Alternate screen | `App.View().AltScreen` |
| Mouse | `App.View().MouseMode`, only when probed/requested |
| Focus | `App.View().ReportFocus` when eligible |
| Bracketed paste and raw mode | Bubble Tea lifecycle from the declarative View |
| Enhanced keys | Availability reported, compatibility mode remains active |
| Inline images | Protocol identity only; no terminal image-rendering claim |
| Suspend | `/suspend` returns `tea.Suspend` only when supported and no Agent/task work is active |

Terminal intent is declared by [`App.View`](../../../../internal/tui/app.go).
The composition root passes only context and the descriptor-preserving output
adapter to Bubble Tea in [`runTUI`](../../../../cmd/yhc/cmd/root.go).

## Focus and Notifications

[`FocusState`](../../../../internal/tui/terminalcap/focus.go) is atomic and
starts `unknown`. Only observed Bubble Tea focus/blur events make it
`focused`/`blurred`; resume resets it to `unknown`.

External notifications require reliable observed blur. Unknown and focused
states suppress them. In-process TUI notifications remain available. Headless
entrypoints do not inherit this TUI-only focus policy.

## Lifecycle Guarantees

The evidenced lifecycle is deliberately narrow:

1. Normal `tea.Program` exit owns restoration of alternate screen, raw mode,
   mouse, paste, cursor, and focus reporting.
2. `Ctrl+D` maps to `ActionAppExit`; the action sets `quitting`, and the next
   App update returns `tea.Quit`
   ([`DefaultBindings`](../../../../internal/tui/keybindings/defaults.go),
   [`handleKeyAction`](../../../../internal/tui/key_actions.go), and
   [`App.Update`](../../../../internal/tui/app.go)).
3. `/suspend` uses Bubble Tea's suspend/reacquire sequence; `ResumeMsg` clears
   and resets observed focus, then the next declarative View restores eligible
   focus reporting and mouse while the helper restarts blink.
4. [`PanicRecovery`](../../../../internal/tui/terminal.go), deferred by the
   TUI entrypoint, disables known modes, restores saved termios when available,
   and re-panics with the original value.

These guarantees do not imply that every error, cancellation, signal, or
process-termination mode bypassing Bubble Tea reaches the fallback. They also
do not imply CLI root-context propagation into every request started by the
App: [`startEngineRequestWithMetadata`](../../../../internal/tui/app.go)
currently creates an App-owned cancellation context.

## External-Editor Reacquisition

Bubble Tea v2.0.8 `ExecProcess` releases raw input and the active presentation
modes before the child process takes ownership. Its restore path reacquires
input and restarts the renderer; the next App View declaratively reasserts
alternate screen, paste, focus reporting, mouse, and title from current App
state. The dependency does not own the App's observed focus snapshot.

Composer and Plan completion therefore run
[`restoreTerminalCapabilitiesCmd`](../../../../internal/tui/external_editor.go).
The helper resets observed focus, requests a full repaint, and restarts blink.
It does not issue imperative focus or mouse commands because those modes are
owned by the next declarative View.

Plan completion validates thread/request/revision/path/generation identity
before disk reload. Stale or failed callbacks still run terminal
reacquisition when `ExecProcess` was entered, but cannot change or settle the
current approval. A successful callback reloads the exact Plan and restores
focus, selection, feedback, cursor, undo, and viewport before the resized
frame clamps geometry.

## Conservative Fallbacks

- `TERM=dumb` or non-TTY disables interactive focus/mouse/paste/suspend.
- Windows does not advertise suspend/resume.
- Unknown terminals do not assume hyperlink, image, or enhanced-key support.
- SSH alone does not disable mouse; explicit user/config disablement still wins.
- Enhanced-key support is reported but not enabled because the current Bubble
  Tea parser boundary does not safely consume all reports.

`/terminal` reports the effective snapshot and focus status plus the
App-selected immutable display-cell identity and its Unicode, segmentation,
width, ambiguous, emoji, tab, and control policies. The display-cell diagnostic
explicitly states that terminal/font fit is not inferred. The terminal
capability snapshot remains diagnostic input only and never selects or mutates
the profile; feature availability remains distinct from activation.

G11.F2 exercises one real Bubble Tea alternate-screen program from compact
through standard and wide layouts in a single PTY. Real `SIGWINCH` resizes
occur during an active streaming projection; primary SGR mouse reports consume
the App-published pill coordinates; a theme switch and no-color reprojection
force later frames; and the capture requires alternate-screen, mouse-tracking,
cursor, repaint, exit, and parent-output markers in lifecycle order. This is
byte/lifecycle evidence only. The separately labelled physical-grid diagnostic
requires a controlling terminal plus explicit terminal/version/font/fallback
metadata and does not turn a PTY capture into a font-rendering claim.

## Bounded Output-Resilience Boundary

Bubble Tea v2.0.8 still owns rendering and terminal mode transitions.
Production passes a descriptor-preserving writer to `tea.WithOutput`: writes
delegate to one [`TerminalOutput`](../../../../internal/tui/terminal_output.go),
while `Fd` reports the original output terminal so Bubble Tea can deliver its
initial window size and later `SIGWINCH` resize events. `TerminalOutput` remains
the sole owner of final application writes. Its unbuffered handoff and
synchronous acknowledgement allow at most one copied packet in flight, so no
second render queue or overflow policy exists.

The production lifecycle is:

1. a caller submits one packet and waits for its physical write result;
2. the first sink failure closes one failure signal and kills the Bubble Tea
   program;
3. close rejects later packets and waits up to 1 second for drain;
4. after the drain deadline, the platform sink is interrupted and receives
   another 250 ms to stop; individual writes are bounded at 750 ms;
5. direct terminal fallback restoration may run only when `Stopped` proves the
   writer can emit no further byte.

Unix duplicates the output descriptor, uses nonblocking netpoll deadlines, and
restores the original descriptor flags after the duplicate closes. Windows
duplicates the output handle, pins the writer to one OS thread, and invokes
`CancelSynchronousIo` before closing the duplicate. Unsupported build targets
fail construction instead of claiming an interrupt guarantee they do not have.

The direct Bubble Tea dependency probes remain intentionally red: an unwrapped
`io.Writer` can still block quit, panic cleanup, and suspend/release, and Bubble
Tea still ignores returned writer errors. The wrapped production seam has
typed write, drain, and interrupt timeout diagnostics; it never restores
through a writer that has not stopped. Resize and rapid invalidation retain
Bubble Tea's one replacement frame plus event-loop backpressure. Engine
runtime terminal and unresolved-interaction snapshots remain lossless because
reduction precedes presentation.

Exact categories and commands are owned by
[`terminal-output-resilience.md`](../../../migration/verification/terminal-output-resilience.md);
the completed P15 contract is retained in
[`p15-terminal-output-resilience.md`](../../../migration/history/runtime/p15-terminal-output-resilience.md).

## Evidence

- capability matrix: [`terminalcap/capabilities_test.go`](../../../../internal/tui/terminalcap/capabilities_test.go)
- focus policy: [`terminalcap/capabilities_test.go`](../../../../internal/tui/terminalcap/capabilities_test.go)
- quit/suspend/resume: [`TestSuspendCommandAndResumePreserveModelState`](../../../../internal/tui/terminal_lifecycle_test.go)
- Plan fake-Vim resize, repaint, mouse/focus, repeated round trips,
  post-reacquisition visible feedback cursor, and bypass-confirmation frame:
  [`TestP203PlanEditorRoundTripPTY`](../../../../internal/tui/plan_editor_pty_unix_test.go)
  runs in the normal Unix suite; its `-race` counterpart explicitly skips the
  Bubble Tea v2.0.8 `RestoreTerminal.initInput` versus resize-listener race
  while the project-owned P20.3 state tests remain race-enabled
- panic cleanup: [`TestPanicRecoveryRestoresAndRepanics`](../../../../internal/tui/terminal_test.go)
- PTY initial size plus normal/panic restoration paths: [`TestTUITerminalRestorationPTY`](../../../../cmd/yhc/cmd/terminal_lifecycle_unix_test.go)
- bounded writer and platform interruption: [`TestTerminalOutputBlockedWriteTimesOutAndCloseInterrupts`](../../../../internal/tui/terminal_output_test.go)
- production fail-closed restore: [`TestP151BlockedWriterFailsClosedAndRestoresAfterWriterStops`](../../../../cmd/yhc/cmd/terminal_output_resilience_test.go)
- blocked writer, failure, backpressure, and late sends: [`TestP150GracefulQuitHoldsFrameUntilWriteReleased`](../../../../cmd/yhc/cmd/terminal_output_resilience_test.go)
- slow-reader PTY and parent-shell restoration: [`TestP150SlowPTYRestoresParentShellAfterSustainedProgress`](../../../../cmd/yhc/cmd/terminal_output_resilience_unix_test.go)
- lossless runtime state under skipped frames: [`TestP150RuntimeSnapshotsSurviveSkippedPresentationFrames`](../../../../engine/terminal_output_resilience_test.go)
- same-program compact/standard/wide resize, streaming, semantic table,
  Agent/status, sticky pill click, theme/no-color, repaint, mode-exit, and
  restoration evidence:
  [`TestG11F2TerminalLifecyclePTY`](../../../../internal/tui/g11f2_terminal_closeout_unix_test.go)
