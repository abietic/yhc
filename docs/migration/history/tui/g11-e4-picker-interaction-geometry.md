# G11.E4 Picker And Interaction Geometry

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.E4 delivery result, picker/hint/search render-environment
> projection, display-cell/source selection, focused evidence, compatibility,
> and rollback

## Outcome

The accepted `combine` slice makes the App-selected immutable
[`RenderEnvironment`](../../../../internal/tui/render_environment.go) the
horizontal-geometry input for:

1. command, file, composer-mention, queued-input, and reverse-history-search
   hints;
2. chat and expanded-view search bars;
3. CommandPalette, ModelPicker, AgentThreadPicker, and Help overlays;
4. bypass confirmation, rewrite-mode hint/selection, and active-thread labels;
   and
5. chat and expanded-view conversion between terminal-cell columns and source
   spans for selection, highlighting, and copy.

Construction, real terminal resize, runtime theme changes, inactive/restored
thread views, and future thread views receive the exact same profile/theme/
geometry identity. Profile-owned projection now performs final alignment,
ellipsis, origin-aware tab expansion, control balancing, centered placement,
and display-cell/source boundary conversion.

Selection strips terminal controls before copy, walks complete extended
grapheme clusters, rounds interior wide-cell boundaries to complete source
spans, and highlights only the selected cells. Cross-viewport expanded
selection resets clipped leading/trailing columns to whole visible rows.

## Compatibility Boundary

Candidate construction, filtering, sorting, cursor/selection identity,
scrolling, keyboard routing, dialog-stack precedence, search match byte
ranges, history suppression, queued-preview ordering, rewrite semantics,
copy-on-select, whitespace trimming, runtime events, persistence, permissions,
replay, and every supported entrypoint remain compatible.

Bubbles remains the Agent-thread/search editor and main composer cursor/
editing owner; G11.E4 projects only the final rendered row. The migrated
pickers remain keyboard-only and gain no mouse action. Resume/session geometry
already belonged to G11.E1, and no standalone production Theme picker exists;
both are explicit no-op inventory results.

The deliberate presentation change is that migrated rows and selection
coordinates follow the App-selected project display-cell grid rather than
byte/rune length, `x/ansi`, or legacy Lip Gloss placement.

The last production callers of `overlayCentered` and `truncateDisplay` are
removed. Their zero-caller definitions retain a narrowly scoped temporary
`unused` marker so E4 remains an independent PR; G11.F1 owns their physical
deletion together with the broader compatibility-owner inventory and
universal classified source gate.

## Verification

Focused evidence covers:

- 40/80/120/180 columns across all included surfaces;
- ASCII, CJK, combining, Indic, VS15/VS16, ZWJ, paired flag, lone regional
  indicator, tab, ANSI, and OSC fixtures;
- exact environment identity through construction, resize, theme, inactive/
  restored, and future thread-view projection;
- complete EGC source extraction at boundaries before, inside, and after wide
  clusters for chat and expanded selection;
- search-row translation and cross-viewport selection clipping;
- bounded, valid, control-balanced picker, hint, search, Help, bypass, rewrite,
  queued/history, and active-thread rows;
- focused existing picker/search/selection regressions and race evidence; and
- a scoped Go-aware source gate rejecting the two legacy helpers and
  unclassified direct width owners in migrated functions while retaining the
  semantic word classifier and Bubbles editor boundary.

```text
go test ./internal/tui -run '^TestG11E4' -count=1
go test -race ./internal/tui -run '^TestG11E4' -count=1
go test ./internal/tui -count=1
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

The fresh scanner reports 463 production files / 159,481 lines, 416 test
files / 143,033 lines, 92 TUI production files / 41,545 lines, 113 TUI test
files / 27,238 lines, and 57 live `go list` packages including scripts. The
complete Makefile test gate passes 5,050 tests. An independent bounded review
found and closed the expanded-search mouse-row offset defect, then accepted
the repaired slice with no remaining blocker.

## Rollback

Rollback reverts picker/search/help/rewrite environment propagation,
hint/search/modal/label projection, cell/source selection conversion, focused
evidence, and the E4 source allowlist together to the coherent G11.E3
boundary. A partial rollback would restore mixed cell coordinates and width
owners within one interactive frame and is not supported. Current behavior
belongs in the
[`TUI architecture`](../../../architecture/tui/README.md); G11.F1 promotion
belongs in [`migration/PLAN.md`](../../PLAN.md).
