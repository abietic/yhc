# G11.E1 Modal Geometry

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.E1 delivery result, six-modal render-environment
> projection, shared final-row/outer-rectangle geometry, Plan hitbox evidence,
> verification, compatibility, and rollback

## Outcome

The accepted `combine` slice extends the App-selected immutable
[`RenderEnvironment`](../../../../internal/tui/render_environment.go) into the
six production modal components: Plan, permission, MCP approval, MCP settings,
resume, and question. Construction, real terminal resize, and runtime theme
changes project the exact same profile/theme/geometry identity without
changing each dialog's selection, focus, settlement, or visible semantic
state.

[`modal_geometry.go`](../../../../internal/tui/modal_geometry.go) is the one
package-private owner for final modal row projection and published outer
rectangles. It performs profile-cell truncation, EGC-safe ellipsis/path
projection, origin-aware tab expansion, per-row SGR/OSC balance, top/bottom
placement, and centering of the complete rendered box after border and padding
exist.

## Placement And Interaction Contract

Plan and question remain top-origin full-width projections. Permission remains
bottom-aligned when it fits. Resume and both MCP surfaces preserve their
existing width allocations while the selected profile measures and centers
the complete final outer box. If any surface is taller than the overlay, it
starts at row zero and keeps the first overlay-height rows.

Bubbles remains the Plan feedback editor's wrapping, cursor, and editing owner.
Each exact rendered editor row is projected once through the selected profile;
those same rows reach the frame and determine the published `X=3` feedback
rectangle. Review and action rectangles likewise describe the final rows
consumed by `HandleMouse`. The other five components remain keyboard-only, and
App continues to swallow modal mouse input before chat/sidebar routing.

The disconnected compatibility `PermissionPrompt` is not routed through the
production App overlay and is deliberately outside this slice.

## Verification

Focused evidence covers:

- exact App environment identity at construction, real resize, and theme
  change for all six components, with semantic-state preservation;
- 40/80/120/180 columns across ASCII, CJK, combining, Indic, VS15/VS16, ZWJ,
  paired/lone regional indicator, bare-label, assistant-star, tab, ANSI, and
  OSC fixtures;
- bounded and control-balanced final rows, final-outer-box centering at its
  actual start column, bottom/top/centered placement, and head-priority
  vertical overflow;
- Plan feedback render/hitbox sharing and existing modal pointer non-leakage;
  and
- an AST ownership guard rejecting direct Lip Gloss placement/width,
  `x/ansi`, legacy display helpers, and manual visible-byte slicing in the
  migrated paths.

```text
go test ./internal/tui -run 'Test(G11E1|P201PlanDialogGoldenStates|PlanDialog|PermissionDialog|ResumePicker|QuestionDialog|AppDialogStack|G11D1AppProjectsEnvironment)' -count=1
go test -race ./internal/tui -run 'Test(G11E1|AppDialogStack|PlanDialog|ResumePicker)' -count=1
go test ./internal/tui -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_scan
go run ./scripts/migration_manifest.go check
git diff --check
```

The fresh scanner reports 461 production files / 158,323 lines, 413 test
files / 141,533 lines, 90 TUI production files / 40,387 lines, 110 TUI test
files / 25,738 lines, and 57 `go list` packages including scripts. The
complete Makefile test gate passes 4,978 tests; the reference manifest remains
valid at 1,884 classified files with 816 mapped.

## Compatibility And Rollback

Dialog order, width clamps, compact thresholds, row budgets, textual options,
keyboard navigation, Plan/permission/question settlement, focus, thread
attention, formal dialog-stack routing, runtime events, persistence, replay,
and supported entrypoints remain compatible. The deliberate presentation
change is that migrated rows, centering, and Plan hitboxes now follow the
selected project grid rather than byte/rune length, `x/ansi`, or Lip Gloss
geometry.

Rollback reverts the environment fields, shared modal helper, six call-site
adapters, and focused tests together to the coherent G11.D3 boundary. A
partial rollback would restore mixed render and hit-test owners and is not
supported. Current behavior belongs in the
[`responsive layout contract`](../../../architecture/tui/contracts/responsive-layout.md);
G11.E2 promotion belongs in [`migration/PLAN.md`](../../PLAN.md).
