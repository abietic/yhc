# P49 Goal Default-On And Budget-Optional Execution Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** test-first implementation steps for the approved P49
> default-on, budget-optional Goal and restart-safe provider-attempt repair

**Goal:** Make Goal available by default and let an explicitly created Goal
run and continue durably without a numeric token budget, while preserving
positive explicit budgets as an optional limiter and restoring exact provider
attempt attribution after restart.

**Architecture:** The engine remains the only Goal lifecycle, accounting, and
continuation owner. P49 first upgrades the durable provider-admission identity,
then versions every continuation representation that currently requires a
scalar budget, then changes state transitions to treat nil as “limiter
disabled.” Configuration and entrypoint defaults change only after those
recovery boundaries are green. Legacy records are decoded by their own schema
rules and are never reinterpreted as newer authorization.

**Tech Stack:** Go 1.26.5, Eino provider usage descriptors, append-only Session
metadata and transcript usage records, `RuntimeInputCoordinator`, Cobra,
Bubble Tea, ACP v1 extensions, white-box Go tests, PTY tests, migration queue,
and Makefile gates.

## Global Constraints

- Execute only P49/G21/G47 as one observable rollback boundary.
- Adoption is `adapt`: no inferred token budget, pricing model, cost estimate,
  or Codex-specific Goal storage owner is introduced.
- Nil disables only the Goal token-budget limiter. Provider accounting,
  permission, cancellation, Plan containment, provider/account limits,
  ACP negotiation, recovery checks, and headless continuation bounds remain.
- Default enablement alone must not create, resume, enqueue, claim, or dispatch
  Goal work. Explicit create or resume authority remains mandatory.
- Persist the complete provider-attempt admission before provider entry and
  require an exact match for settlement after restart.
- Preserve legacy budgeted continuation item identities. Never recompute a
  version-1 digest with version-2 fields or use zero as a nil sentinel.
- A legacy paused nil-budget draft remains paused on load. A legacy active
  nil-budget record and a legacy in-flight admission remain unavailable.
- Default promotion cannot be committed ahead of schema, recovery, and
  continuation compatibility.
- Keep unsupported entrypoints unsupported: ordinary one-shot headless,
  child/review, ephemeral/administration, Plan, and standalone MCP.
- Preserve unrelated `PROJECT_GUIDE.md` and `artifacts/` content in the caller
  checkout.
- Before implementation, update the topic branch onto current
  `origin/master`; after every rebase rerun focused schema and docs checks.
- Final code verification must use `make fmt`, `make lint`, `make test`, and
  `make build`, plus the applicable contract, race, PTY, manifest,
  documentation, and diff gates.

---

## File Structure

| File | Responsibility in this slice |
|---|---|
| `engine/session/branch.go` | Define Goal state v4, continuation v2, admission v2, historical version constants, and optional durable budget fields. |
| `engine/goal_usage.go` | Persist/restore complete attempt identity and require exact settlement equality. |
| `engine/goal_persistence.go` | Apply source-version validation, legacy fail-closed rules, and v4 state migration without dispatch. |
| `engine/goal_continuation.go` | Hash, persist, project, validate, and compare optional-budget continuation cursors while preserving v1 identities. |
| `engine/input_coordinator.go` | Carry runtime continuation payload v2 and deep-clone its optional budget. |
| `engine/session/resume.go` | Deep-clone optional Goal budgets in restored coordinator payloads. |
| `engine/goal_state.go` | Make nil-budget create/resume active while retaining explicit-cap exhaustion. |
| `engine/goal_runtime.go` | Permit terminal-derived continuation when the budget limiter is absent. |
| `engine/config/config.go` | Enable Goal by default without supplying `DefaultTokenBudget`. |
| `cmd/eino-agent/cmd/root.go`, `server/acp/agent.go` | Project the enabled default through CLI and ACP while preserving explicit disable and ACP negotiation. |
| Goal tests under `engine/`, `engine/config/`, `cmd/eino-agent/cmd/`, `internal/tui/`, and `server/acp/` | Prove schema, recovery, lifecycle, entrypoint, race, and real-process behavior. |
| Current Goal architecture/guides and migration evidence | Describe verified behavior, close G21/G47, and retain rollback and limitations. |

### Task 1: Persist and strictly settle complete provider-attempt identity

**Files:**

- Modify: `engine/session/branch.go`
- Modify: `engine/goal_usage.go`
- Modify: `engine/goal_usage_test.go`
- Modify: `engine/goal_persistence_test.go`

**Interfaces:**

- Consumes: `execution.ProviderUsageDescriptor`, `goalUsageAdmission`,
  `transcript.GoalUsageRecord`, and Session metadata checkpoints.
- Produces: `PersistedGoalUsageAdmission` version 2 with five additional
  attempt fields; no public provider API change.

- [x] **Step 1: Add failing restart and mutation tests**

Extend the provider-attribution test so a checkpointed admission contains and
restores every field already present in memory:

```go
if admission.LogicalRequestID != "request-1" ||
	admission.ModelAttemptID != "attempt-1" ||
	admission.ModelAttemptIndex != 2 ||
	admission.ModelProfile != "primary" ||
	admission.ModelRetryIndex != 1 {
	t.Fatalf("persisted attempt identity = %#v", admission)
}
```

Add table-driven cases that mutate each of `LogicalRequestID`,
`ModelAttemptID`, `ModelAttemptIndex`, `ModelProfile`, and `ModelRetryIndex`
between persisted admission and transcript record. Restore into a fresh engine
and assert Goal becomes unavailable or usage-limited, the pending evidence is
not cleared, and the provider dispatch counter remains zero.

Add a legacy version-1 pending admission fixture. Assert restore reports a
typed unsupported-version warning, exposes no Goal execution authority, and a
subsequent unrelated checkpoint serializes the original nested admission
unchanged.

- [x] **Step 2: Run the focused tests and verify red**

```bash
go test ./engine/ -run '^(TestP294GoalUsageKeepsExactAttemptAttribution|TestP242bGoalUsageRecoveryRejectsUnknownAndStaleRecords|TestP49LegacyPendingGoalUsageAdmissionRemainsUnavailable)$' -count=1
```

Expected: FAIL because the checkpoint drops the five attempt fields and the
current matcher accepts their absence conditionally.

- [x] **Step 3: Version and encode the durable admission**

In `engine/session/branch.go`, retain the historical version and make v2
current:

```go
const (
	PersistedGoalUsageAdmissionLegacyVersion uint16 = 1
	PersistedGoalUsageAdmissionVersion       uint16 = 2
)
```

Add these exact fields to `PersistedGoalUsageAdmission`:

```go
LogicalRequestID  string `json:"logical_request_id"`
ModelAttemptID    string `json:"model_attempt_id"`
ModelAttemptIndex int    `json:"model_attempt_index"`
ModelProfile      string `json:"model_profile"`
ModelRetryIndex   int    `json:"model_retry_index"`
```

Copy them in both `persistedGoalUsageAdmission` and
`goalUsageAdmissionFromPersisted`. Keep the existing pre-dispatch checkpoint
ordering unchanged.

- [x] **Step 4: Reject legacy in-flight admissions before conversion**

In `restorePersistedGoalStateWithUsage`, inspect the nested admission before
normal state migration. A version-1 pending admission must return
`unavailableGoalState(record, goalReasonUnsupportedVersion, reason)` using the
original `record`, so later checkpoints preserve the unresolved evidence.
Unknown admission versions take the same fail-closed path. Do not silently
clear, synthesize, or settle them.

- [x] **Step 5: Require exact attempt equality**

Remove the conditional `LogicalRequestID != ""` branch in
`goalUsageRecordMatchesAdmission`. Include the five attempt fields in the
unconditional equality expression:

```go
record.LogicalRequestID == admission.LogicalRequestID &&
	record.ModelAttemptID == admission.ModelAttemptID &&
	record.ModelAttemptIndex == admission.ModelAttemptIndex &&
	record.ModelProfile == admission.ModelProfile &&
	record.ModelRetryIndex == admission.ModelRetryIndex
```

`validateGoalUsageAdmission` continues to build a
`transcript.GoalUsageRecord`, but accepts only admission v2. The transcript
validator remains the single identity-shape validator.

- [x] **Step 6: Run focused green and race tests**

```bash
go test ./engine/ -run '^(TestP294GoalUsageKeepsExactAttemptAttribution|TestP242bGoalUsageRecoveryRejectsUnknownAndStaleRecords|TestP242bGoalStateV1MigratesAndV2CarriesAdmission|TestP49LegacyPendingGoalUsageAdmissionRemainsUnavailable)$' -count=1
go test -race ./engine/ -run '^(TestP294GoalUsageKeepsExactAttemptAttribution|TestP242bGoalUsageRecoveryRejectsUnknownAndStaleRecords)$' -count=1
```

Expected: PASS, including zero replacement provider calls for every mismatch.

- [x] **Step 7: Commit the admission repair**

```bash
git add engine/session/branch.go engine/goal_usage.go engine/goal_usage_test.go engine/goal_persistence_test.go
git commit -m "fix(goal): persist provider attempt admission identity"
```

### Task 2: Version optional-budget state and continuation representations

**Files:**

- Modify: `engine/session/branch.go`
- Modify: `engine/goal_persistence.go`
- Modify: `engine/goal_continuation.go`
- Modify: `engine/input_coordinator.go`
- Modify: `engine/session/resume.go`
- Modify: `engine/goal_continuation_test.go`
- Modify: `engine/goal_persistence_test.go`
- Modify: `engine/input_coordinator_test.go`

**Interfaces:**

- Produces: `PersistedGoalState` v4, `PersistedGoalContinuation` v2, and
  `RuntimeGoalContinuation` v2 with `TokenBudget *uint64`.
- Preserves: valid v1 continuation item/checkpoint/turn digests and version-3
  Goal semantics for already persisted budgeted work.

- [x] **Step 1: Add failing version, round-trip, and compatibility tests**

Add fixtures for:

1. v4 active state with nil budget and no cursor;
2. v4 active state with one nil-budget v2 continuation;
3. a fresh coordinator round-trip of the matching runtime v2 payload;
4. mutations of budget presence/value, cursor version, runtime payload
   version, scope, item ID, checkpoint ID, and digest;
5. a valid version-3 state plus budgeted continuation v1 whose item identity is
   unchanged after restore/checkpoint; and
6. version-1 through version-3 active nil-budget records that remain
   unavailable.

For restore tests, assert replay alone performs no dispatch. For coordinator
tests, assert exactly one matching claim succeeds and every mutated case admits
zero provider calls.

- [x] **Step 2: Run schema tests and verify red**

```bash
go test ./engine/ -run '^(TestP49GoalStateV4AllowsUnbudgetedActive|TestP49OptionalBudgetContinuationRoundTrip|TestP49LegacyBudgetedContinuationKeepsIdentity|TestP49LegacyActiveNilBudgetFailsClosed|TestP243ClaimAdmissionReceiptAndRejectedRecovery)$' -count=1
```

Expected: FAIL because state v3 rejects active nil budget and both durable
continuation payloads require scalar budgets.

- [x] **Step 3: Introduce explicit historical/current schema constants**

Keep versions named instead of replacing their meaning:

```go
const (
	PersistedGoalStateLegacyVersion       uint16 = 1
	PersistedGoalStateAccountingVersion   uint16 = 2
	PersistedGoalStateContinuationVersion uint16 = 3
	PersistedGoalStateVersion             uint16 = 4

	PersistedGoalContinuationLegacyVersion uint16 = 1
	PersistedGoalContinuationVersion       uint16 = 2
)
```

Change `TokenBudget` in `PersistedGoalContinuation`,
`goalContinuationCursor`, `goalContinuationIdentity`, and
`RuntimeGoalContinuation` to `*uint64`. Use `json:"token_budget,omitempty"` in
the three serialized representations. Define runtime legacy/current versions
as 1 and 2.

- [x] **Step 4: Centralize optional-budget value semantics**

Use helpers rather than nil/zero checks scattered across owners:

```go
func goalBudgetHasRemaining(tokenBudget *uint64, tokensUsed uint64) bool {
	return tokenBudget == nil || *tokenBudget > tokensUsed
}

func sameGoalTokenBudget(left, right *uint64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
```

Zero remains invalid whenever a pointer is present. Clone every optional
budget with `cloneUint64`, including `cloneGoalContinuationCursor`,
`cloneRuntimeItem`, and the Session resume/coordinator copy path.

- [x] **Step 5: Preserve v1 digest identity and emit v2 for new cursors**

`newGoalContinuationCursor` must no longer reject nil. New identities use
continuation v2 and Goal schema v4. For a nonnil budget, JSON encoding of the
pointer remains the same numeric `token_budget` value used by v1, so validating
an existing v1 digest does not change its bytes.

`goalContinuationRuntimeItem` projects runtime payload v1 for a cursor v1 and
payload v2 for a cursor v2. `validateRuntimeGoalContinuation` reconstructs the
cursor using the payload-corresponding continuation version and rejects any
version pairing mismatch.

Update `validateGoalContinuationAdmission` at the claim boundary as well. Its
budget checks must use optional-value equality and remaining-budget semantics:

```go
!sameGoalTokenBudget(state.TokenBudget, cursor.TokenBudget) ||
	!goalBudgetHasRemaining(state.TokenBudget, state.TokensUsed)
```

Retain every other immutable Goal, scope, turn, generation, ledger,
disposition, and pending-admission comparison. This claim check is required in
addition to cursor/runtime decoding; otherwise a valid nil-budget v2 item would
still be retired as stale before dispatch.

- [x] **Step 6: Validate by source schema version**

In `restorePersistedGoalStateWithUsage`, save the source state version before
migration. Accept state versions 1-4, but apply these rules before setting the
working copy to v4:

```go
if tokenBudget != nil && *tokenBudget == 0 {
	return fmt.Errorf("zero token budget")
}
if status == goalStatusActive &&
	(sourceVersion < session.PersistedGoalStateVersion && tokenBudget == nil ||
		tokenBudget != nil && *tokenBudget <= tokensUsed) {
	return fmt.Errorf("active Goal has no remaining token budget")
}
```

Versions 1-2 still reject any continuation. Continuation v1 requires a nonnil
budget and `GoalSchemaVersion == 3`; continuation v2 permits nil and requires
`GoalSchemaVersion == 4`. A v4 state may carry a restored v1 cursor until its
existing lifecycle disposition finishes. Compare optional budgets by presence
and value, never by zero.

- [x] **Step 7: Run focused green and race tests**

```bash
go test ./engine/ -run '^(TestP49GoalStateV4AllowsUnbudgetedActive|TestP49OptionalBudgetContinuationRoundTrip|TestP49LegacyBudgetedContinuationKeepsIdentity|TestP49LegacyActiveNilBudgetFailsClosed|TestP243EligibleTerminalPersistsDormantContinuation|TestP243ContinuationEnqueueIsIdempotentAndConflictsFailClosed|TestP243ClaimAdmissionReceiptAndRejectedRecovery|TestP244RecoveredGoalItemSignalsDedicatedOnly)$' -count=1
go test -race ./engine/ -run '^(TestP49OptionalBudgetContinuationRoundTrip|TestP243.*|TestP244RecoveredGoalItemSignalsDedicatedOnly)$' -count=1
```

Expected: PASS with unchanged v1 identities and no replay dispatch.

- [x] **Step 8: Commit the durable optional-budget schema**

```bash
git add engine/session/branch.go engine/goal_persistence.go engine/goal_continuation.go engine/input_coordinator.go engine/session/resume.go engine/goal_continuation_test.go engine/goal_persistence_test.go engine/input_coordinator_test.go
git commit -m "fix(goal): version optional budget continuation state"
```

### Task 3: Make nil-budget Goal creation, resume, and continuation active

**Files:**

- Modify: `engine/goal_state.go`
- Modify: `engine/goal_runtime.go`
- Modify: `engine/goal_continuation.go`
- Modify: `engine/goal_state_test.go`
- Modify: `engine/goal_workflow_test.go`
- Modify: `engine/goal_usage_test.go`
- Modify: `engine/goal_continuation_test.go`

**Interfaces:**

- Changes: observable Goal create/resume semantics when `TokenBudget == nil`.
- Preserves: explicit positive budget validation and `budget_limited` behavior.

- [x] **Step 1: Add failing lifecycle tests**

Add tests proving:

- create without request/default budget persists `active` with nil budget and
  submits exactly one initial Goal turn;
- a restored legacy paused nil-budget draft stays paused until explicit resume,
  then becomes active;
- usage accumulates on a nil-budget Goal without changing it to
  `budget_limited`;
- adding an explicit cap later counts all prior committed usage and limits at
  the existing `>=` boundary; and
- one completed nil-budget turn derives one v2 continuation, which a fresh
  coordinator/engine claims exactly once.

- [x] **Step 2: Run lifecycle tests and verify red**

```bash
go test ./engine/ -run '^(TestP49CreateWithoutBudgetStartsImmediately|TestP49ResumeLegacyPausedDraftWithoutBudget|TestP49UnbudgetedUsageNeverBudgetLimits|TestP49BudgetAddedAfterUsageAppliesCommittedTotal|TestP49UnbudgetedGoalContinuesExactlyOnce)$' -count=1
```

Expected: FAIL because create pauses nil-budget Goals, resume returns
`errGoalBudget`, and continuation eligibility requires a nonnil cap.

- [x] **Step 3: Remove the synthetic budget-required transition**

In `goalService.create`, always initialize a valid explicit create as active;
delete only the nil-budget branch that sets `goalReasonBudgetRequired`. Keep
budget cloning and Plan conflict checks unchanged.

In `goalService.resume`, reject only an explicit cap with no remaining budget:

```go
if current.TokenBudget != nil &&
	*current.TokenBudget <= current.TokensUsed {
	return nil, errGoalBudget
}
```

Keep `usage_limited`, complete, unavailable, Plan, and explicit pause behavior
unchanged.

- [x] **Step 4: Admit terminal continuation without a limiter**

Replace the nonnil test in `goalContinuationEligible` with
`goalBudgetHasRemaining(state.TokenBudget, state.TokensUsed)`. Pass the cloned
optional budget into cursor construction and comparison. Do not change
terminal reason, waiting permission, blocker, pending completion, pending
usage admission, scope, or idempotency conditions.

`applyGoalUsageRecord` already guards `budget_limited` with
`next.TokenBudget != nil`; retain that condition and add regression assertions
rather than introducing a maximum-value sentinel.

- [x] **Step 5: Run focused green tests**

```bash
go test ./engine/ -run '^(TestP241GoalTransitionMatrixPersistsExactState|TestP244GoalCommandControlsDurableState|TestP242bGoalProviderUsageAdmissionAggregateAndBudget|TestP49CreateWithoutBudgetStartsImmediately|TestP49ResumeLegacyPausedDraftWithoutBudget|TestP49UnbudgetedUsageNeverBudgetLimits|TestP49BudgetAddedAfterUsageAppliesCommittedTotal|TestP49UnbudgetedGoalContinuesExactlyOnce)$' -count=1
go test -race ./engine/ -run '^(TestP49CreateWithoutBudgetStartsImmediately|TestP49UnbudgetedGoalContinuesExactlyOnce)$' -count=1
```

Expected: PASS; explicit budgets retain existing limiting behavior.

- [x] **Step 6: Commit lifecycle semantics**

```bash
git add engine/goal_state.go engine/goal_runtime.go engine/goal_continuation.go engine/goal_state_test.go engine/goal_workflow_test.go engine/goal_usage_test.go engine/goal_continuation_test.go
git commit -m "feat(goal): run explicitly created goals without a budget"
```

### Task 4: Default-enable supported entrypoints without widening authority

**Files:**

- Modify: `engine/config/config.go`
- Modify: `engine/config/goal_test.go`
- Modify: `cmd/eino-agent/cmd/root.go`
- Modify: `cmd/eino-agent/cmd/root_test.go`
- Modify: `cmd/eino-agent/cmd/headless_goal_test.go`
- Modify: `cmd/eino-agent/cmd/plain_goal_pty_unix_test.go`
- Modify: `internal/tui/goal_workflow_test.go`
- Modify: `internal/tui/goal_workflow_pty_unix_test.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/goal_extension_test.go`

**Interfaces:**

- Changes: missing configuration projects `GoalCapabilityConfig.Enabled=true`.
- Preserves: explicit `goal.enabled: false`, saved-root scope, dedicated
  headless process bound, and immutable ACP negotiation.

- [x] **Step 1: Add failing configuration and entrypoint tests**

Change the config expectation to enabled with no default budget. Add direct
projection tests for nil/missing config, explicit false, and explicit positive
budget in both CLI and ACP helpers.

Extend supported-entrypoint tests so TUI and Plain create without a budget and
submit one initial Goal prompt; dedicated headless continues an unbudgeted Goal
only up to positive `--max-continuations`; negotiated ACP creates active nil
budget and requires explicit continue. Retain negative assertions for
unnegotiated ACP and unsupported scopes.

- [x] **Step 2: Run entrypoint tests and verify red**

```bash
go test ./engine/config/ -run '^TestP244GoalConfig' -count=1
go test ./cmd/eino-agent/cmd/ -run '^(TestP49GoalCapabilityDefaultsEnabled|TestP245bHeadlessGoal|TestP245aPlain)' -count=1
go test ./internal/tui/ -run '^(TestP244Goal|TestP49)' -count=1
go test ./server/acp/ -run '^(TestP245c|TestP49)' -count=1
```

Expected: FAIL because default config and nil helper projections are disabled.

- [x] **Step 3: Change the configuration default and helper baselines**

In `config.DefaultConfig` use an enabled pointer and leave
`DefaultTokenBudget` nil:

```go
enabled := true
// ...
Goal: &GoalConfig{Enabled: &enabled},
```

Initialize both projection helpers with the enabled default, then overlay
explicit configuration:

```go
capability := &engine.GoalCapabilityConfig{Enabled: true}
```

ACP also initializes `ACPNegotiated` from its existing immutable negotiation
owner. An explicit false always overrides the enabled baseline. Do not change
`goalCapabilityConfigured`, saved-root checks, command registration scope, or
ACP negotiation predicates.

- [x] **Step 4: Run supported and negative entrypoint tests**

```bash
go test ./engine/config/ -run '^TestP244GoalConfig' -count=1
go test ./cmd/eino-agent/cmd/ -run '^(TestP49GoalCapabilityDefaultsEnabled|TestP245bHeadlessGoal|TestP245aPlain)' -count=1
go test ./internal/tui/ -run '^(TestP244Goal|TestP49)' -count=1
go test ./server/acp/ -run '^(TestP245c|TestP49)' -count=1
go test ./engine/ -run '^(TestP244GoalCapability|TestP49)' -count=1
```

Expected: PASS, including explicit-disable and unnegotiated-ACP cases.

- [x] **Step 5: Run real-process PTY coverage**

```bash
go test ./cmd/eino-agent/cmd/ -run '^TestP245aPlainGoalWorkflowPTY$' -count=1
go test ./internal/tui/ -run '^TestP244GoalWorkflowPTY$' -count=1
```

Expected: PASS without `goal.default_token_budget` in the fixture.

- [x] **Step 6: Commit default promotion**

```bash
git add engine/config/config.go engine/config/goal_test.go cmd/eino-agent/cmd/root.go cmd/eino-agent/cmd/root_test.go cmd/eino-agent/cmd/headless_goal_test.go cmd/eino-agent/cmd/plain_goal_pty_unix_test.go internal/tui/goal_workflow_test.go internal/tui/goal_workflow_pty_unix_test.go server/acp/agent.go server/acp/goal_extension_test.go
git commit -m "feat(goal): enable budget-optional goals by default"
```

### Task 5: Synchronize current documentation and close P49

**Files:**

- Modify: `docs/architecture/platform/configuration.md`
- Modify: `docs/architecture/runtime/query-engine.md`
- Modify: `docs/architecture/capabilities/commands.md`
- Modify: `docs/architecture/platform/entrypoints-and-transports.md`
- Modify: `docs/architecture/runtime/budgets-and-limits.md`
- Modify: `docs/architecture/platform/acp-adapter.md`
- Modify: `docs/architecture/state/sessions.md`
- Modify: `docs/guides/configuration-and-providers.md`
- Create: `docs/migration/verification/p49-goal-default-unbudgeted.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p49-goal-default-unbudgeted.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/history/runtime/README.md`
- Modify: `docs/migration/queue.yaml`
- Modify: `docs/migration/PLAN.md`
- Modify: `docs/migration/STATUS.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/plans/README.md`
- Modify: `docs/migration/plans/p49-goal-default-unbudgeted.md`
- Modify: `docs/superpowers/plans/2026-08-07-p49-goal-default-unbudgeted.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- Produces: current architecture/user guidance, reproducible verification, one
  immutable runtime history record, and a queue with P49 removed.
- Closes: G21 and G47 only after all deterministic source gates pass.

- [x] **Step 1: Update current owners from verified source**

Document that Goal is default-enabled, explicit create/resume remains the work
authority, nil disables only the Goal token limiter, explicit positive budgets
retain existing accounting, and all supported/unsupported entrypoint boundaries
remain. Document state v4, continuation/runtime v2, admission v2, and legacy
fail-closed recovery without presenting queued design as shipped behavior
before tests pass.

- [x] **Step 2: Write reproducible verification and limitations**

The verification record must list exact focused/race/PTY/gate commands and
their observed results. Explicitly exclude live-provider cost, physical
terminal rendering, representative adoption, and monetary safety claims.

- [x] **Step 3: Exercise rollback fixtures**

Add or reuse deterministic fixtures that prove:

1. P49 binary pauses active Goals, settles/rejects pending work, checkpoints,
   then survives explicit disable with zero dispatch; and
2. disable-first startup dispatches nothing, after which only restoring the
   P49 binary, re-enabling, quiescing, checkpointing, and disabling again makes
   downgrade safe.

No downgrade writer or direct configuration-file rejection is added.

- [x] **Step 4: Run focused risk packs**

```bash
go test ./engine/ -run 'TestP49|TestP294GoalUsageKeepsExactAttemptAttribution|TestP243|TestP244RecoveredGoalItemSignalsDedicatedOnly' -count=1
go test -race ./engine/ -run 'TestP49|TestP243|TestP244RecoveredGoalItemSignalsDedicatedOnly' -count=1
go test ./cmd/eino-agent/cmd/ ./internal/tui/ ./server/acp/ -run 'TestP49|TestP245' -count=1
make test-contract
make test-race
make test-pty
```

Expected: PASS. If a repository risk pack is inapplicable, record its exact
target output and the narrower deterministic substitute; do not silently omit
it.

- [x] **Step 5: Close the migration queue only after evidence is green**

Remove P49 from active `queue.yaml`, close G21/G47 in `REMAINING.md`, render the
generated plan region, mark this implementation plan historical/completed, and
add one runtime history record. Do not promote unrelated backlog.

```bash
go run ./scripts/migration_queue render
go run ./scripts/migration_manifest.go check
make docs-check
```

- [x] **Step 6: Run all repository gates**

```bash
make fmt
make lint
make test
make build
go run ./scripts/migration_manifest.go check
make docs-check
git diff --check
```

Expected: PASS. Local evidence is reported separately from remote CI and live
product validation.

- [x] **Step 7: Request independent diff review and resolve findings**

Have a read-only reviewer inspect the cohesive P49 diff for schema
compatibility, restart attribution, single-dispatch continuation, explicit
disable, ACP negotiation, and unsupported-scope regressions. Re-run every gate
affected by an accepted fix.

- [x] **Step 8: Commit closeout evidence**

```bash
git add docs/architecture docs/guides/configuration-and-providers.md docs/migration docs/superpowers/plans
git commit -m "docs(goal): close P49 default-on rollout"
```

The topic branch is then pushed, reviewed through a pull request, and squash
merged. Remote CI remains a distinct evidence class; any explicit user waiver
must be recorded without describing skipped CI as green.
