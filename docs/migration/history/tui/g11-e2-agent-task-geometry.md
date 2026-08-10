# G11.E2 Agent And Task Geometry

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.E2 delivery result, Agent/task render-environment
> projection, transient centered geometry, full-screen Task Panel projection,
> focused evidence, compatibility, and rollback

## Outcome

The accepted `combine` slice extends the App-selected immutable
[`RenderEnvironment`](../../../../internal/tui/render_environment.go) into four
production presentation surfaces:

1. the Agent create/edit wizard;
2. the Ctrl+B background-task list and Agent detail;
3. the `/team` monitor and read-only peek; and
4. the full-screen Ctrl+T Task Panel.

Construction, real terminal resize, and runtime theme changes project the
exact same profile/theme/geometry identity into the three centered components
without changing their visible, selection, focus, scrolling, editing, or
control state. Each centered `Overlay` clears and replaces one transient
`modalFrameGeometry` from the exact profile-owned frame returned to App. That
rectangle is presentation-only and is not persisted, cached, copied into App,
or consumed by runtime state.

Ctrl+T retains the existing full-screen `layout.overlayRect` as its only
rectangle. Its existing task ordering, bounded row budget, scroll window, and
status row now enter one top-origin profile projection; no second Task Panel
geometry owner was added.

## Content Boundary

Agent detail and transcript lines that feed Ctrl+B and `/team` use the
App-selected profile for EGC-safe wrapping, truncation, origin-aware tabs, and
balanced supported controls. The generic `agentTranscriptHistoryItem`, Agent
trace, and tool-history renderers retain their pre-E2 projection and remain
the G11.E3 content-migration boundary. The searchable Agent thread picker
remains a G11.E4 picker.

Bubbles continues to own internal text-input/textarea editing and cursor
geometry. G11.E2 projects the final wizard frame but does not replace the
library editor model.

## Verification

Focused evidence covers:

- exact App environment identity at construction, real resize, and theme
  change for the Agent wizard, background/detail, and Team monitor/peek, with
  semantic-state preservation;
- 40/80/120/180 columns across ASCII, CJK, combining, Indic, VS15/VS16, ZWJ,
  paired/lone regional indicator, bare-label, assistant-star, tab, ANSI, and
  OSC fixtures on all four final surfaces;
- bounded, valid, control-balanced rows; final centered outer rectangles;
  hidden-render geometry reset; and the full-screen Task Panel rectangle;
- preserved keyboard-only routing and mouse non-leakage;
- focused existing Agent/detail/transcript/Task regressions plus their race
  suite; and
- an AST owner gate rejecting legacy centered placement, direct `x/ansi`,
  rune-count truncation, and second geometry owners in migrated production
  paths.

```text
go test ./internal/tui -run 'TestG11E2' -count=1
go test ./internal/tui -run 'Test(G11E2|AgentMonitor|BackgroundTasks|Teams|AgentDetail|P142c|TaskPanel)' -count=1
go test -race ./internal/tui -run 'Test(G11E2|AgentMonitor|BackgroundTasks|Teams|AgentDetail|P142c|TaskPanel)' -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_scan -json
go run ./scripts/migration_manifest.go check
git diff --check
```

The fresh scanner reports 461 production files / 158,539 lines, 414 test
files / 141,949 lines, 90 TUI production files / 40,603 lines, 111 TUI test
files / 26,154 lines, and 57 live `go list` packages including scripts. The
complete Makefile test gate passes 4,994 tests.

## Compatibility And Rollback

Dialog allocations, Team compact threshold, row budgets, task ordering,
scroll windows, textual labels, Bubbles editing, detail tabs, transcript
paging, Agent message/pause/resume/abort controls, read-only Team restriction,
thread switching, focus, keyboard routing, formal dialog-stack routing,
runtime events, persistence, permissions, replay, and every supported
entrypoint remain compatible.

The deliberate presentation change is that migrated Agent/task rows,
centering, wrapping, truncation, and full-screen bounds now follow the selected
project grid rather than byte/rune length, `x/ansi`, or legacy Lip Gloss
placement.

Rollback reverts the three component environment/geometry fields, panel-
specific profile adapters, Task Panel top-frame projection, App propagation,
and focused tests together to the coherent G11.E1 boundary. A partial rollback
would restore mixed selected-profile and compatibility geometry in one
interactive flow and is not supported. Current behavior belongs in the
[`responsive layout contract`](../../../architecture/tui/contracts/responsive-layout.md);
G11.E3 promotion belongs in [`migration/PLAN.md`](../../PLAN.md).
