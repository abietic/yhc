# G11.C Display-Cell Profile Kernel

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.C delivery result, immutable profile and diagnostic
> boundary, compatibility consequences, verification, and rollback

## Outcome

The accepted `combine` slice generalizes the G9 table `widthProfile` into one
immutable [`DisplayCellProfile`](../../../../internal/tui/display_cell.go#L55).
Its deterministic default records the Unicode, segmentation, width,
ambiguous-width, emoji-presentation, tab, ANSI/control, Indic, regional-
indicator, and bare-label policies. Canonical serialization of every policy
field produces one versioned SHA-256 identity; identical values retain the
same cache identity and each policy-field change produces a different one.

The service adds rectangle-origin-aware cluster iteration, measurement, tab
expansion, ANSI-aware truncation/wrapping, padding/alignment, and per-line
control balancing. [`measureRuns`](../../../../internal/tui/display_cell.go#L338)
concatenates visible semantic run text before EGC segmentation and assigns the
whole cluster the presentation metadata of its first visible scalar. Measured
clusters retain source bytes separately from the terminal projection, so tabs
can expand without normalizing source and later SGR/OSC emission cannot split
an EGC.

## App And Compatibility Boundary

[`App.New`](../../../../internal/tui/app.go#L308) copies a valid injected
profile or the deterministic default. No setter, engine event, transcript,
durable schema, permission, replay, terminal-name guess, or hidden probe was
added. [`App.terminalDiagnostics`](../../../../internal/tui/app.go#L4987)
extends `/terminal` with the active identity and policy and explicitly states
that terminal/font fit requires separate evidence.

`widthProfile` and `defaultWidthProfile` remain short-lived compatibility
names for the same value. Existing Markdown/table callers therefore retain
their prior deterministic default and goldens. No ChatView, layout, sidebar,
status, pill, hitbox, or final-frame call site consumes the App-selected value
yet; G11.D1 owns Markdown/App projection and exact cache invalidation.

## Verification

Focused evidence covers every identity field, App copy/fallback semantics,
terminal diagnostics, multiple tab origins, cross-run combining/ZWJ/variation
clusters, ANSI/OSC balance, progress, source preservation, and width bounds:

```text
go test ./internal/tui -run '^(TestG11C|TestWidthProfile|TestTerminalCommandReportsEffectiveCapabilities)' -count=1
go test -race ./internal/tui -run '^TestG11C' -count=1
go test ./internal/tui -run '^(TestG9|TestG11A)' -count=1
```

Repository closeout uses:

```text
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Compatibility And Rollback

The only user-visible addition is the display-cell block in `/terminal`.
Canonical Markdown, table output, final frame geometry, interaction, runtime
state, and persisted data remain unchanged.

Rollback removes the App field/config injection, diagnostic block, profile
kernel, compatibility alias, and focused tests together, restoring the
table-owned profile implementation. Do not partially retain an injected App
identity that no current geometry/cache owner consumes. Current behavior
belongs in [`architecture/tui/README.md`](../../../architecture/tui/README.md);
remaining propagation belongs in [`migration/PLAN.md`](../../PLAN.md).
