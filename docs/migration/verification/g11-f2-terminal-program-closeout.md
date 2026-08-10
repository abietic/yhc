# G11.F2 Terminal Program Closeout Verification

**Status:** verification

**Last verified:** 2026-07-26

> **Ownership:** reproducible G11.F2 real-program PTY, physical-grid claim
> boundary, steady-frame structure, portable performance budgets, and
> repository closeout procedure

## Result

G11.F2 changes no production behavior. It closes the accepted G11 `combine`
program with real-program lifecycle and steady-frame evidence:

1. The existing semantic-table PTY covers 32/48/72 columns. One additional
   Bubble Tea program starts at 40 columns and traverses 48/72/80/120/150/180
   in the same alternate-screen session. The union is the accepted
   32/40/48/72/80/120/150/180 matrix.
2. Every normal-layout width exposes a semantic table, sticky header, shared
   jump pill, status row, and real SGR primary mouse click. Wide widths also
   expose the live Agent row.
3. A streaming table remains visible across real PTY resizes. Later frames
   apply a runtime theme change and test-only no-color capability switch. The
   capture requires alternate-screen and mouse entry/exit, cursor restoration,
   a post-resize repaint, then a parent-output restoration marker in order.
4. A 10,000-item completed-history fixture proves only a viewport-bounded
   subset renders during warm-up and no item renderer runs again for a dirty
   steady frame. Portable p95 budgets cover that path and a 100-live-sidebar
   wide frame.

## PTY Procedure

Run:

```bash
go test ./internal/tui \
  -run '^(TestG9ESemanticTablePTYGeometry|TestG11F2TerminalLifecyclePTY)$' \
  -count=1 -v
go test ./cmd/eino-agent/cmd \
  -run '^Test(TUITerminalRestorationPTY|P150SlowPTYRestoresParentShellAfterSustainedProgress)$' \
  -count=1
```

The G11.F2 program publishes the exact final pill row and inclusive/exclusive
cell bounds through its status hook, then sends a real SGR mouse press/release
at the resulting absolute terminal coordinate. A click passes only when the
same `ChatView` returns to follow mode. Each later resize repeats the away,
published-coordinate, click, and follow transition.

PTY capture proves bytes and lifecycle, not glyph pixels. It cannot identify
the user's font, fallback chain, ambiguous-width policy, or terminal renderer.
`/terminal` therefore continues to report `Terminal/font: not inferred`.

## Physical-Grid Diagnostic

There is no physical terminal/font-grid claim in this automated closeout, so
the diagnostic is expected to skip in ordinary tests. A person making a claim
about one concrete terminal/font combination must run it from that controlling
terminal and supply all metadata:

```bash
EINO_AGENT_G11F2_PHYSICAL_GRID=1 \
EINO_AGENT_G11F2_TERMINAL='<terminal>' \
EINO_AGENT_G11F2_TERMINAL_VERSION='<version>' \
EINO_AGENT_G11F2_FONT='<font and size>' \
EINO_AGENT_G11F2_FONT_FALLBACK='<fallback chain>' \
go test ./internal/tui -run '^TestG11F2PhysicalGridDiagnostic$' -count=1 -v
```

The diagnostic enters raw mode, writes ASCII, CJK, NFD, Indic, VS16, ZWJ,
flag, and keycap fixtures, reads DSR cursor positions, and compares that one
observed grid with the selected profile. Missing metadata, no controlling TTY,
no DSR response, wrap, or width disagreement fails the claim. A result cannot
be generalized to another terminal, version, font, or fallback.

## Performance Procedure

Portable gates and the structural assertion:

```bash
go test ./internal/tui \
  -run '^TestG11F2(SteadyFrameKeepsFrozenHistorySegmentedAndViewportBounded|SteadyFramePerformanceBudgets)$' \
  -count=1 -v
```

Accepted budgets are `< 20 ms` p95 for a 10K frozen-history steady dirty frame
and `< 50 ms` p95 for a 100-live-sidebar-row steady frame. The structural
counter, not timing alone, rejects frozen-history re-segmentation and
full-history rendering.

Diagnostic benchmark:

```bash
go test ./internal/tui -run '^$' -bench '^BenchmarkG11F2' \
  -benchmem -benchtime=200ms -count=3
```

On Darwin arm64 / Apple M5 Pro, the median of three G11.F2 runs was:

| Benchmark | Median | Bytes/op | Allocs/op |
|---|---:|---:|---:|
| 10K frozen-history steady dirty frame | 18.95 us | 1,344 | 2 |
| 100 live-sidebar-row steady frame | 1.636448 ms | about 793 KB | 4,448-4,449 |

These machine numbers provide regression context only. The p95 budgets above
remain the portable pass/fail contract.

## Repository Closeout

Run:

```bash
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_scan -json
go run ./scripts/migration_manifest.go check
git diff --check
git diff --exit-code -- go.mod go.sum
```

The result may be promoted to current architecture/history only after every
local gate passes. A GitHub job that never starts solely because of account
usage/billing is external infrastructure evidence, not a substitute for these
local gates; any real check failure still reopens the slice.
