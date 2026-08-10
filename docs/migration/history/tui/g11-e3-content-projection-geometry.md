# G11.E3 Content Projection Geometry

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.E3 delivery result, content render-environment projection,
> focused evidence, compatibility, and rollback

## Outcome

The accepted `combine` slice makes the App-selected immutable
[`RenderEnvironment`](../../../../internal/tui/render_environment.go) the
horizontal-geometry input for:

1. generic, Agent-trace, Bash, Read/Search, Edit/Write diff, MCP,
   Plan/Task/Todo, and Web tool-history families;
2. inline ErrorMessage rich, expanded, raw, and transcript projections;
3. full-screen expanded/raw content and its final status row;
4. compact, condensed-mascot, and full-bordered welcome tiers; and
5. notification multi-line compatibility and production single-line status
   projections.

[`content_geometry.go`](../../../../internal/tui/content_geometry.go) owns
profile-selected final-line projection, ellipsizing, wrapping, and bounded row
projection. Production tool and error history adapters retain the exact
`HistoryRenderContext.Environment`; renderer-local headers, previews, diff
lines, wraps, and final rows derive geometry from that profile instead of
selecting another width policy.

The expanded/raw view projects highlighted rows and its status line after
semantic rendering. Welcome rendering and mascot hit bounds consume the same
profile-projected lines. Notification rendering calls `Active()` once and
preserves active-item pruning and newest-item selection.

## Compatibility Boundary

Canonical content, tool parsers, structured diff hunks, line budgets, tool
status, rich/expanded/raw/transcript dispatch, raw ANSI stripping, selection
prefixes, versions, finished state, ChatView cache identity, the expanded/raw
120-column semantic render cap, search, scrolling, welcome tier thresholds and
lifecycle, notification TTL/eviction/severity/suffix/fallback behavior,
keyboard routing, runtime events, persistence, permissions, replay, and every
supported entrypoint remain compatible.

The `/errors` panel has no proven production App route and remains unchanged.
Completion hints, autocomplete, Agent-thread and command/model/theme/session
pickers, and residual string-to-click-column helpers remain G11.E4. Global
legacy-helper deletion and the universal production source gate remain
G11.F1.

The deliberate presentation change is that migrated final rows, wrapping,
truncation, tab expansion, control balancing, and welcome mascot bounds follow
the selected project display-cell grid rather than byte/rune length,
`x/ansi`, or Lip Gloss width selection.

## Verification

Focused evidence covers:

- 40/80/120/180 columns for every tool family, diff, inline error,
  expanded/raw, welcome, and notifications;
- ASCII, CJK, combining, Indic, VS15/VS16, ZWJ, paired flag, lone regional
  indicator, bare label, tab, ANSI, and OSC fixtures;
- rich, expanded, and raw semantic preservation with control-free raw output;
- exact environment invalidation for finished history after geometry, theme,
  or profile changes;
- bounded, valid, control-balanced rows, expanded/raw status projection,
  welcome mascot hit bounds, and notification lifecycle pruning;
- focused existing tool/history/error/welcome/notification regressions and
  their race suite; and
- a scoped Go-aware source gate rejecting second visible-width owners,
  legacy helpers, and visible byte/rune slicing while retaining explicit
  semantic-parser exemptions.

```text
go test ./internal/tui -run '^TestG11E3' -count=1
go test ./internal/tui -run 'Test(G11E3|ToolHistory|ErrorMessage|Welcome|Notification|HistoryItem)' -count=1
go test -race ./internal/tui -run 'TestG11E3|Test(ToolHistory|ErrorMessage|Welcome|Notification|HistoryItem)' -count=1
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

The fresh scanner reports 462 production files / 159,132 lines, 415 test
files / 142,463 lines, 91 TUI production files / 41,196 lines, 112 TUI test
files / 26,668 lines, and 57 live `go list` packages including scripts. The
complete Makefile test gate passes 5,035 tests. An independent bounded review
found no blocking regression and accepted the slice.

## Rollback

Rollback reverts the content-geometry owner, exact-environment history adapter,
tool/diff/error propagation, direct App expanded/raw/welcome/notification
projection, focused evidence, and E3 source allowlist together to the coherent
G11.E2 boundary. A partial rollback would restore mixed selected-profile and
compatibility geometry within one final content frame and is not supported.
Current behavior belongs in the
[`responsive layout contract`](../../../architecture/tui/contracts/responsive-layout.md);
G11.E4 promotion belongs in [`migration/PLAN.md`](../../PLAN.md).
