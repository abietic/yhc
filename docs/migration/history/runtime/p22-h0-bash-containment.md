# P22.H0 Bash Permission Containment

**Status:** historical
**Closed gaps:** G13
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** delivery evidence for removing Bash from the `acceptEdits`
> fast path. Current behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md);
> remaining Auto-review work and executable order belong in
> [`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P22.H0 was delivered under the **`combine`** decision:

- preserve QueryEngine permission ordering, explicit rules, Plan precedence,
  durable grants, denial tracking, bypass mode, and the existing Auto
  classifier;
- preserve resolved-root containment for Write and Edit;
- remove the project-owned first-token Bash shortcut instead of extending it
  into a partial shell parser; and
- defer policy snapshots, action descriptors, reviewer separation, provider
  routing, shadow measurements, and reviewer enforcement to later P22 slices.

## Outcome

`AcceptEditsCheck` now admits only contained Write and Edit operations. Every
Bash invocation returns `false` from that predicate.

The existing QueryEngine coordinator therefore supplies the outcome:

- `acceptEdits` Bash reaches the ordinary interactive prompt, or fails closed
  when no interaction owner exists;
- `auto` Bash reaches the configured classifier and then its existing
  prompt/fail-closed fallback; and
- explicit rules, exact session approvals, bypass mode, and other earlier
  permission branches keep their prior precedence.

No public API, durable schema, provider route, event contract, UI flow, or
entrypoint-specific policy was added.

## Evidence

Focused tests prove:

- `TestAcceptEditsCheckBashAlwaysFallsThrough` rejects all seven historical
  first tokens across contained, outside-root, compound, substituted, wrapped,
  redirected, symlink-sensitive, and protected-path examples;
- `TestAcceptEditsPromptsForEveryBashInvocation` proves those forms reach the
  existing prompt while contained Write/Edit still skip it;
- `TestQueryEngineAcceptEditsModePromptsForBashFilesystemCmd` proves
  `acceptEdits` no longer treats Bash as an edit fast path;
- `TestQueryEngineAutoModeBashSkipsAcceptEditsFastPath` proves `auto` no longer
  bypasses its later permission handling; and
- `TestAutoModeClassifierAllowAndDenyLifecycle` plus
  `TestAutoModeClassifierErrorClearsAndPrompts` prove a formerly admitted Bash
  token reaches the configured classifier and its fail-closed fallback.

Closeout passed:

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan -reference .reference/claude-code-ripe -json
git diff --check
```

The full suite reported 5,027 passing tests and one opt-in physical-terminal
diagnostic skipped.

## Compatibility And Rollback

Compatibility intentionally narrows convenience: Bash commands beginning with
`mkdir`, `touch`, `rm`, `rmdir`, `mv`, `cp`, or `sed` no longer inherit
approval from the first token. Existing explicit allow rules, request-bound
approvals, bypass mode, and Auto classifier decisions are unchanged.

Rollback is one code-and-test unit, but restoring the removed Bash shortcut
would restore G13's unsafe authorization boundary. No data or configuration
migration is required.

## Next State

G13 is closed. G14 remains accepted under P22, and P22.1a remains queued.
P23.H0 is the preferred next candidate under the root safety/data-integrity
selection rule, but no later slice becomes executable until root `PLAN.md`
explicitly promotes exactly one.
