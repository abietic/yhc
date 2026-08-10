# P27 Selection Viewport Geometry And Clipboard Closeout

**Status:** historical
**Closed gaps:** G30
**Completed:** 2026-07-28
**Last verified:** 2026-07-28

> **Ownership:** final delivery evidence for the completed P27 `combine`
> program and G30 closure. Current behavior belongs in
> [`architecture/tui/README.md`](../../../architecture/tui/README.md);
> executable order and remaining gaps belong in
> [`PLAN.md`](../../PLAN.md) and [`REMAINING.md`](../../REMAINING.md).

## Decision

P27 completed under `combine`:

- preserve the App-owned `DisplayCellProfile`, string renderer,
  viewport-bounded work, Shift escape, and transient item-local selection;
- adapt same-frame projection, exact renderer-owned selectable semantics,
  selection-aware action precedence, and asynchronous result feedback from
  relevant references;
- add one project-owned final-frame row projection, compatible content
  identity, generation-fenced edge-scroll owner, and typed clipboard service;
  and
- keep QueryEngine, transcript/session durability, permissions, tools,
  providers, and non-TUI entrypoints unchanged.

## Delivered Outcome

P27.1 pairs every rendered chat frame with one immutable viewport projection.
Empty, sticky, padding, transcript, inter-item gap, and pill rows are
classified after final composition. Hit testing, inverse highlight, drag
bounds, pill ownership, Agent trace actions, and extraction identity consume
that exact frame; only transcript rows can create endpoints.

P27.2 publishes immutable semantic cell spans and soft/hard/inter-item
boundaries beside each selectable built-in's existing cached render.
Extraction preserves represented whitespace, emits no byte for visual wraps,
emits exactly one newline for semantic and cross-item boundaries, excludes
presentation/control bytes, and clamps through complete graphemes.
Content-identity drift clears selection before any consumer. Double/triple
click, forward/reverse drag, keyboard extension, and generation-fenced
50 ms edge scrolling use the same semantic rows.

P27.3 replaces four result-free synchronous clipboard callers with one
TUI-local typed Bubble Tea command/result boundary. The composition root
injects the exact `TerminalOutput` already used by Bubble Tea. Direct, tmux,
and screen OSC 52 packets therefore share the renderer's serialized physical
writer; raw `os.Stdout` and the old `CopyToClipboard` helper are deleted.

One App request ID/caller pair may be pending. Empty, invalid UTF-8, and input
above 262,144 source bytes fail before transport. Native routing is
snapshotted once for macOS, Wayland/X11, Windows, SSH, and unavailable
environments; it uses fixed argv and stdin without a shell and has a
two-second deadline. `TerminalOutput` atomically fences native admission
against failure and close, while a later output failure cancels an admitted
helper and remains authoritative for the typed result.

Only native exit zero produces ordinary “copied” feedback. A successful OSC
write alone is reported as an unacknowledged terminal request. Busy,
oversized, unavailable, timeout, cancellation, partial/closed output, and
failure paths never claim success or expose selected text, stderr, a command
string, or a host path.

## Evidence

Focused fake and App tests prove:

- exact direct/tmux/screen packets, tmux precedence, and the
  262,144/262,145-byte boundary without truncation;
- fixed macOS, Wayland/X11, Windows, SSH, unavailable, timeout, cancellation,
  and redacted failure outcomes;
- one non-queued request at both App and defensive service boundaries,
  stale request/caller rejection, and caller-specific selection retention;
- output failure before native admission and during native execution,
  including the atomic post-check start fence; and
- truthful typed feedback for chat selection, expand selection, keyboard
  selection, and `ActionCopy`, including empty and non-string command data.

The Unix PTY matrix sends renderer and clipboard packets through the same
`TerminalOutput` for direct, tmux, and screen modes and proves packets do not
interleave. Static construction-root evidence proves one production terminal
writer is constructed, injected before `tea.NewProgram`, and reused by the
clipboard service. Independent review accepted the failure/start
linearization and result-channel lifecycle with no remaining finding.

Closeout passed:

```text
go test ./internal/tui ./cmd/eino-agent/cmd -run 'TestP273' -count=1
go test -race ./internal/tui -run 'TestP273' -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan
git diff --check
```

The verified source, test, TUI, suite, and reference counts are recorded in
[`STATUS.md`](../../STATUS.md).

## Compatibility And Rollback

Mouse and expand-view copy retain visible selection. Keyboard copy clears only
after validation and admission; busy or oversized attempts retain it.
OSC-only environments now receive truthful degraded feedback, and payloads
above 256 KiB fail visibly instead of starting unbounded work. These are the
intentional compatibility changes.

One squash revert restores the previous clipboard command/result adapter and
its caller projections. P27.1 geometry and P27.2 extraction can remain
intact. No schema, transcript, runtime reducer, QueryEngine, provider, tool,
permission, or non-TUI rollback is required.

P27 and G30 leave the live execution queue. Root `PLAN.md` returns to intake
with no `Ready` or `In progress` row.
