# G11.F2 Terminal Program Closeout

**Status:** historical
**Closed gaps:** G11

**Completed:** 2026-07-26

> **Ownership:** accepted G11.F2 PTY/program closeout, physical-grid claim
> boundary, steady-frame evidence, compatibility, and rollback

## Outcome

The accepted G11 `combine` program closes with a test/docs-only slice. No
production Go source, public API, persisted data, or runtime behavior changes.

The semantic-table PTY's 32/48/72 widths combine with one real Bubble Tea
alternate-screen program that starts at 40 columns and traverses
48/72/80/120/150/180 in the same terminal session. At every normal-layout
width it verifies semantic-table content, sticky history, the shared jump-pill
row and hitbox, status output, and a primary SGR mouse click. Wide layouts also
verify a live Agent row. Active streaming survives real PTY resizes, and later
frames cover runtime theme and no-color reprojection.

The capture requires alternate-screen and mouse-tracking entry/exit, visible
cursor restoration, a repaint after the last resize, mode exit, and a
parent-output marker in order. Existing command-level normal/panic and
slow-writer PTY tests remain the process-restoration authority.

## Terminal And Font Boundary

PTY output proves emitted bytes, resize/repaint delivery, interaction routing,
and terminal-mode lifecycle. It cannot observe glyph pixels or font fallback
and is not terminal/font physical-grid evidence. `/terminal` continues to
report `Terminal/font: not inferred`.

The separately labelled opt-in cursor-position diagnostic requires an
interactive controlling terminal plus explicit terminal, terminal version,
font, and fallback metadata. It compares ASCII, CJK, NFD, Indic, VS16, ZWJ,
flag, and keycap cursor advances with the selected profile. Any result is
limited to that named combination. No physical-grid claim was made during the
automated closeout, so the diagnostic's ordinary skip is intentional.

## Steady-Frame Boundary

A native 10,000-completed-item `ChatView` fixture records each semantic render.
Warm-up must touch only a viewport-bounded subset and retain at most 64 frozen
render-cache entries. A dirty steady frame must not increment any item render
counter, proving it neither re-segments frozen items nor renders the complete
history.

Portable p95 gates are `< 20 ms` for that frozen-history steady dirty frame and
`< 50 ms` for a wide frame backed by 100 live sidebar rows. On the recorded
Darwin arm64 Apple M5 Pro baseline, three benchmark runs produced medians of
18.95 us / 1,344 bytes / 2 allocations for frozen history and 1.636448 ms /
about 793 KB / 4,448-4,449 allocations for the live sidebar. Machine values
are diagnostic; portable budgets and the structural counter are authoritative.

## Compatibility

The deterministic `DisplayCellProfile`, semantic Markdown/table content,
follow state, pill geometry, selection, canonical history, theme policy,
terminal capabilities, runtime state, persistence, permissions, replay, and
supported entrypoints are unchanged. The test-only no-color switch exercises
final-frame projection without adding a production runtime transition.

G11 leaves the live queue. The remaining P21 runtime/TUI contract work owns no
implicit geometry or terminal/font change.

## Verification

Focused evidence:

```text
go test ./internal/tui -run '^(TestG9ESemanticTablePTYGeometry|TestG11F2)' -count=1
go test ./cmd/eino-agent/cmd -run '^Test(TUITerminalRestorationPTY|P150SlowPTYRestoresParentShellAfterSustainedProgress)$' -count=1
go test -race ./internal/tui -run '^TestG11F2(Steady|Terminal)' -count=1
go test ./internal/tui -run '^$' -bench '^BenchmarkG11F2' -benchmem -benchtime=200ms -count=3
```

Repository closeout passes `make fmt`, `make lint`, `make test`, `make build`,
`make lint-new`, `make docs-check`, migration scanning, manifest validation,
`git diff --check`, and unchanged `go.mod`/`go.sum`. Exact suite and scanner
counts remain current in [`STATUS.md`](../../STATUS.md).

Reproduction details and the opt-in physical-grid command live in
[`g11-f2-terminal-program-closeout.md`](../../verification/g11-f2-terminal-program-closeout.md).

## Rollback

Rollback removes only the G11.F2 fixtures and closeout records, then restores
G11.F2 to the live plan. It must not revive a deleted geometry owner, add a
terminal-name/font guess, weaken lifecycle restoration, or remove the
structural performance boundary. A disproved lifecycle or budget reopens the
slice until the evidence or the production owner is corrected.
