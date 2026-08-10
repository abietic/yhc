# G27 Result-Bound Command Recency

**Created:** 2026-07-28
**Completed:** 2026-07-28
**Status:** historical
**Closed gaps:** G27
**Adoption:** `preserve`

> **Ownership:** completion evidence for G27.1. Current command behavior belongs
> in [`commands.md`](../../../architecture/capabilities/commands.md); executable
> order belongs in [`migration/PLAN.md`](../../PLAN.md).

## User Problem

Command-palette Enter previously recorded a selection in process-local Recent
after contextual revalidation but before strict dispatch, typed result
settlement, or existing action application succeeded. A rejected, failed,
cancelled, superseded, or stale selection could therefore look like a
successfully used command.

G27.1 repaired presentation truth only. It did not change command names,
availability, dispatch behavior, actions, QueryEngine ownership, durable
events, permissions, cancellation policy, or non-TUI entrypoints.

## Delivered Contract

The live TUI `App` owns one pending palette-submission record. Palette Enter
revalidates the selected canonical command and creates that provenance without
mutating Recent.

- Engine-owned commands bind the record to the exact `queryID` allocated by the
  normal request path. Matching single or batched `CommandResultSucceeded`
  delivery commits once only after the existing TUI result projection.
- TUI-local commands use a separate monotonic submission identity. They commit
  once only after strict `Registry.Dispatch` and the existing local action
  owner accepts and applies the action. `ActionCopy` additionally binds the
  exact clipboard request and waits for the existing typed result to confirm
  native success.
- Failed, unsupported, cancelled, missing, stale, superseded, mismatched, or
  duplicate settlement clears or cannot match the record.
- Replay, async-hook delivery, typed or queued manual same-text input, and
  non-palette entrypoints cannot create or inherit palette provenance.
- `CommandPalette` remains the newest-first, deduplicated, maximum-three
  process-local list owner.

The implementation is bounded to
[`app.go`](../../../../internal/tui/app.go),
[`dialog_stack.go`](../../../../internal/tui/dialog_stack.go), and
[`command_palette.go`](../../../../internal/tui/command_palette.go). Focused
coverage is in
[`g27_command_recency_test.go`](../../../../internal/tui/g27_command_recency_test.go),
while the existing P21 stale-admission and normalized local-command coverage
remains authoritative.

## Verification

Focused and race-sensitive tests cover matching single and batch success,
duplicate delivery, failure and unsupported results, cancellation, terminal
or events-done settlement, supersession and stale identity, consecutive
same-name submissions, manual and queued same-text isolation, replay and async
hook exclusion, strict local dispatch, capability loss, local action failure,
clipboard success/failure/stale-result settlement, and missing engine
ownership.

Closeout uses:

```bash
go test ./internal/tui -run 'TestG27|TestP21PaletteSelectionRechecksAdmissionBeforeRecording|TestTUILocalCommandsUseNormalizedRegistryNames'
go test -race ./internal/tui -run 'TestG27|TestP21PaletteSelectionRechecksAdmissionBeforeRecording|TestTUILocalCommandsUseNormalizedRegistryNames'
go test ./internal/tui
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
git diff --check
```

The focused, race, full-package, repository, documentation, and diff gates
passed before merge. Independent lifecycle review found the asynchronous
clipboard request/result boundary; exact request binding and confirmed-success
settlement plus failure/stale/manual-supersession regressions passed re-review
with no remaining ownership, ordering, replay, or compatibility defect.

## Compatibility And Rollback

This is a `preserve` repair: P21 registry, discovery, strict dispatch, typed
results, action application, entrypoint projection, and durable schemas are
unchanged. Recent remains TUI-only and process-local.

One squash revert removes the App-owned pending provenance and settlement
hooks and restores the eager presentation mutation. No data migration,
registry cleanup, or cross-entrypoint rollback is required.

The original reproduction, comparison, and accepted repair boundary remain in
the
[`recent-delivery remediation audit`](../../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g27-result-bound-recent-commands).
