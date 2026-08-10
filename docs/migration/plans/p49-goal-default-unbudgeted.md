# P49 Default-On Budget-Optional Goal

**Status:** historical
**Created:** 2026-08-07
**Approved:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** retained target contract, compatibility boundary, and
> promotion evidence for the completed default-enabled, budget-optional Goal
> and restart-safe provider-attempt admission repair

## Decision

Make Goal discoverable by default and let an explicitly created Goal execute
immediately without inventing a token budget. A token budget is an optional
limiter: only a positive value explicitly supplied by the user or host enables
remaining-budget admission and `budget_limited` transitions.

This remains an `adapt` decision. It keeps Codex's useful optional-budget
outcome while preserving Eino-Agent's exact provider accounting, durable
continuation, permission, cancellation, usage-coverage, and recovery owners.
It deliberately replaces the P24 rule that a positive budget is mandatory for
activation.

P45 deferred a different proposal: default promotion coupled to an unknown
numeric budget and unsupported representative cost claims. P49 does not infer a
number or claim affordability. Enabling the capability creates no Goal and
starts no provider call; autonomous work still begins only after an explicit
Goal create or resume action.

## User Problem And Closed Defects

The prior defaults hid `/goal` from ordinary saved-root TUI use. Even when a
user enabled the capability, creating a Goal without
`goal.default_token_budget` persists a `paused` draft, so the explicit create
action does not start the requested work.

Making only the configuration default true would have preserved that broken
user journey. The prior runtime also encoded a positive budget into continuation
creation, persistence, restore, and claim admission, so a nil-budget Goal cannot
continue durably.

Before widening exposure, P49 also repaired a restart correctness defect in the
provider-usage gate. The in-memory admission contained logical-request and
model-attempt identity, but `PersistedGoalUsageAdmission` omitted that identity.
After restart the prior runtime could not prove that an exact provider attempt
matched its pending admission.

## Approaches Considered

| Approach | Decision | Reason |
|---|---|---|
| Default-enable Goal; nil budget is valid and only an explicit positive budget enables the limiter | Adopt | Matches the requested create-and-run workflow, preserves explicit Goal authority, and invents no provider-neutral cost number. |
| Default-enable Goal with a project-selected numeric budget | Reject | No measured value is safe across providers, models, context sizes, and user workloads. A guessed number either wastes money or stops useful work unpredictably. |
| Default-enable discovery but keep nil-budget Goals paused | Reject | Makes the command visible without delivering its primary action and preserves the current multi-step configuration trap. |

## Observable Contract

### Capability and creation

1. `config.DefaultConfig()` enables Goal and leaves
   `goal.default_token_budget` absent.
2. An explicit `goal.enabled: false` remains the global kill switch for TUI,
   Plain, dedicated headless Goal, and negotiated ACP projections.
3. Default enablement does not create a Goal, resume a paused Goal, enqueue a
   continuation, or call a provider by itself.
4. Creating a saved-root Goal without a budget persists `status=active` and
   `token_budget=null`.
5. Existing create-and-dispatch surfaces start their initial Goal turn
   immediately. ACP retains its negotiated control/continue transport split;
   default enablement does not bypass protocol negotiation.
6. A paused nil-budget Goal may become active only through an explicit resume
   action. Loading a paused record never starts work by itself.

### Budget semantics

1. Nil means that the Goal token-budget limiter is disabled. It is not encoded
   as zero, maximum integer, or an inferred provider limit.
2. A configured `goal.default_token_budget` is explicit host policy and applies
   to newly created Goals. It must remain positive when present.
3. A user-set budget must remain positive. Setting it to or below already
   committed usage produces `budget_limited` exactly as today.
4. For a nil budget, provider usage continues to accumulate and remain visible,
   but token count alone never produces `budget_limited` or rejects a
   continuation.
5. If a budget is added later, all previously committed Goal usage counts
   toward it. P49 adds no budget-clear operation; clearing and recreating the
   Goal remains the way to return to an unbudgeted Goal after setting a cap.

### Safety boundaries that do not change

- Explicit Goal creation or resume remains required; the model cannot create a
  Goal or expand its budget.
- Saved-root scope, Plan exclusion, permission checks, cancellation, blocker
  enforcement, and terminal ownership remain engine-owned.
- Provider usage is still recorded for unbudgeted Goals. Missing, ambiguous, or
  corrupt usage evidence remains fail-closed as `usage_limited` or unavailable
  state; nil budget does not waive accounting integrity.
- At most one Goal-bound provider admission may remain unsettled across the
  root and descendants.
- Dedicated `goal run` still requires an explicit saved Session and a positive
  `--max-continuations`; nil token budget does not remove the process bound.
- Unnegotiated ACP, ordinary one-shot headless execution, child/review Sessions,
  ephemeral or administration engines, Plan Mode, and standalone MCP gain no
  Goal authority.

## Persistence And Recovery Contract

### Versioned representations

P49 versions every persisted or coordinator-durable representation whose old
encoding treats a budget as mandatory:

| Representation | New contract |
|---|---|
| `PersistedGoalState` version 4 | Permits `active` with `TokenBudget=nil`; versions 1-3 retain their historical validation rules. |
| `PersistedGoalContinuation` version 2 | Stores an optional token-budget snapshot. Presence and value are part of immutable cursor identity; version 1 remains budgeted. |
| `RuntimeGoalContinuation` version 2 | Stores the same optional budget in the durable `RuntimeInputCoordinator` item. Version, scope, cursor identity, and coordinator payload must agree before claim. |
| `PersistedGoalUsageAdmission` version 2 | Persists `LogicalRequestID`, `ModelAttemptID`, `ModelAttemptIndex`, `ModelProfile`, and `ModelRetryIndex` in addition to the existing identity. |

The internal continuation cursor and its hashed identity also carry an optional
budget. The runtime must never use `0` as a compatibility sentinel. Nil-to-nil
and value-to-equal-value comparisons are valid; a presence or value change, a
cursor/payload version mismatch, or a digest mismatch makes a pending
continuation stale and retires it through the existing reconciliation owner
before any provider entry.

### Legacy behavior

1. Legacy budgeted active, paused, blocked, limited, and complete Goals migrate
   without changing their observable status or usage.
2. A legacy nil-budget paused draft remains paused after restore. A later user
   resume uses the new semantics and may activate it without adding a budget.
3. A legacy active nil-budget record remains fail-closed because that state was
   invalid in its schema version. It is not reinterpreted as evidence of user
   authorization for unbudgeted execution.
4. A legacy pending provider admission that lacks the new attempt identity is
   unsupported in flight. Restore preserves its durable evidence, makes the
   Goal unavailable with a typed compatibility reason, and admits zero further
   provider calls. It is never silently dropped, guessed, or matched by ledger
   revision alone.
5. A new-version active nil-budget Goal and its optional-budget continuation
   restore without dispatch during replay. Only the existing post-restore
   entrypoint owner may claim the exact pending continuation.

### Provider-attempt settlement

Before provider entry, the checkpoint must durably bind the complete admission
identity. Completion and recovery compare the transcript usage record with all
of these fields. Removing, changing, or mixing any logical request, attempt,
profile, retry, Goal, turn, generation, ledger, or provider-call identity fails
closed and starts no replacement call.

## Entrypoint Contract

| Entrypoint | Default-on behavior | Boundary retained |
|---|---|---|
| Saved-root TUI | `/goal` is available; create without budget becomes active and submits the initial Goal prompt. | Reducer remains projection-only; engine owns transitions and continuation. |
| Plain | The same typed Goal command and initial-prompt behavior are available by default for a saved root. | Generic queued input cannot claim Goal continuation. |
| Dedicated headless Goal | An unbudgeted active Goal may be resumed and continued. | Explicit Session and positive process continuation limit remain mandatory. |
| ACP | Configuration defaults on, but Goal remains invisible and unauthorized until immutable capability negotiation succeeds. Nil-budget create is active; execution follows the existing explicit continue request. | No slash parsing, implicit wake, or negotiation bypass. |
| Unsupported scopes | No change. | Ordinary headless, child/review, ephemeral/administration, Plan, and standalone MCP remain excluded. |

## Atomic Implementation Boundary

P49 ships as one rollback boundary because default promotion would magnify the
restart-attribution defect. The implementation must include all of the
following before `goal.enabled` changes its default:

1. persist and strictly validate complete provider-attempt admission identity;
2. version Goal state, continuation cursor, and coordinator runtime payload for
   an optional budget;
3. allow nil-budget create, resume, terminal-derived continuation, restore,
   claim, and provider admission;
4. retain explicit-budget limiting and every non-budget safety boundary;
5. default-enable all supported configuration projections while preserving
   explicit disable and ACP negotiation;
6. update current architecture and user guidance after source behavior passes;
   and
7. preserve deterministic regressions across state, recovery, entrypoint, race,
   and PTY boundaries.

Partial delivery must keep Goal default-disabled. In particular, configuration
promotion cannot merge ahead of persistence and continuation compatibility.

## Deterministic Acceptance Evidence

### State and configuration

- Missing Goal configuration resolves to `Enabled=true` and
  `DefaultTokenBudget=nil`; explicit false remains false; an explicit positive
  default remains intact; zero remains invalid.
- Nil-budget create and explicit resume produce active state. A legacy paused
  draft does not auto-resume during load.
- Token growth on a nil-budget Goal never produces `budget_limited`; the same
  usage reaches `budget_limited` when an explicit cap is present.

### Persistence and recovery

- A provider admission containing complete logical-request and attempt identity
  is checkpointed, restored in a fresh engine, and settled only by one exactly
  matching transcript usage record.
- Table-driven mutations of every newly persisted attempt field fail closed and
  prove zero replacement provider calls.
- New-version nil-budget active state and continuation round-trip exactly.
- A nil-budget terminal creates one version-2 coordinator item; a fresh
  coordinator restore permits exactly one matching claim and dispatch. Mutated
  budget presence/value, payload version, scope, cursor identity, or digest
  admits zero provider calls.
- Legacy budgeted records migrate, legacy nil-budget paused drafts stay paused,
  legacy active nil-budget records remain unavailable, and legacy in-flight
  admission records with incomplete identity remain unavailable.
- A subsequent unrelated checkpoint preserves an unsupported legacy in-flight
  admission byte-for-byte; fail-closed restore cannot erase the evidence it
  reports as unresolved.
- Replay reconstructs state without dispatching a tool or provider call.

### Entrypoints and lifecycle

- TUI and Plain creation without a configured budget submit one initial Goal
  turn and expose active/unbounded progress.
- Dedicated headless Goal consumes at most its explicit process continuation
  limit when the Goal token budget is nil.
- Negotiated ACP can create and explicitly continue a nil-budget Goal;
  unnegotiated ACP remains rejected.
- Permission wait/resume, cancellation, Plan exclusion, missing provider usage,
  stale cursor, budget changes, and explicit disable retain their current
  terminal behavior.
- Goal continuation and restore tests pass under the race detector, and the
  real-process TUI/Plain Goal PTY scenarios pass without a configured budget.
- A rollback fixture proves quiesce, settlement, explicit disable, checkpoint,
  and older-version fail-closed ordering. A disable-first fixture proves zero
  further dispatch; unfinished records can be quiesced only after restoring the
  P49 binary and explicitly re-enabling the capability.

Final verification uses `make fmt`, `make lint`, `make test`, `make build`, the
applicable `make test-contract`, `make test-race`, and `make test-pty` risk
packs, `go run ./scripts/migration_manifest.go check`, `make docs-check`, and
`git diff --check`. Local deterministic evidence does not claim live-provider,
physical-terminal, monetary-cost, or representative-adoption validation.

## Rollback

Use the P49-capable binary to pause active Goals and settle or fail closed every
pending admission and continuation first. Confirm that no Goal work remains in
flight and persist that quiescent Goal state, then set and persist
`goal.enabled: false` in configuration, verify the capability is unavailable,
and only then start the older binary. The rollback must not delete Session or
transcript evidence. Disabling the capability first is invalid because it also
removes the Goal controls needed to quiesce work. If that happens, disabled
startup must dispatch nothing; restore the P49 binary, explicitly re-enable the
capability, quiesce and persist the Goal, then disable it again before starting
the older binary. P49 adds no configuration writer or mechanism that can reject
a direct external edit.

Older binaries do not understand the new state, continuation, or admission
versions and therefore fail closed rather than executing ambiguous work. P49
adds no downgrade writer. Returning to an older binary can make a new-format
Goal unavailable until the P49-capable binary is restored or the user clears
the Goal.

## Source Owners

| Boundary | Current owner |
|---|---|
| Default and field-presence-aware merge | [`config.DefaultConfig`](../../../engine/config/config.go#L100) |
| Capability and supported entrypoints | [`QueryEngine.goalWorkflowEnabled`](../../../engine/goal_capability.go#L40) |
| Create, resume, and explicit budget transitions | [`goalService.create`](../../../engine/goal_state.go#L107), [`goalService.resume`](../../../engine/goal_state.go#L213), and [`goalService.setBudget`](../../../engine/goal_state.go#L251) |
| Provider usage admission and settlement | [`goalService.admitProviderUsage`](../../../engine/goal_usage.go#L159) and transcript Goal usage records |
| Durable state and validation | [`PersistedGoalState`](../../../engine/session/branch.go#L985) and [`restorePersistedGoalStateWithUsage`](../../../engine/goal_persistence.go#L56) |
| Continuation identity and claim | [`newGoalContinuationCursor`](../../../engine/goal_continuation.go#L77) and [`validateGoalContinuationAdmission`](../../../engine/goal_continuation.go#L639) |
| Coordinator-durable continuation payload | [`RuntimeGoalContinuation`](../../../engine/input_coordinator.go#L140) and [`RuntimeInputCoordinator.enqueueDormantGoalContinuation`](../../../engine/input_coordinator.go#L426) |
| TUI and Plain create-and-dispatch | [`QueryEngine.applyGoalCommand`](../../../engine/goal_command.go#L13) and the entrypoint consumers |
| ACP negotiated control | [`Agent.acpGoalCapabilityConfig`](../../../server/acp/agent.go#L1550) and the Goal extension adapter |

## Promotion Gate

The user approved this written contract on 2026-08-07 and confirmed all of
these statements together:

1. nil budget means no Goal token-budget limiter and supports durable automatic
   continuation;
2. no numeric default is shipped or inferred;
3. legacy paused drafts do not auto-start, while invalid legacy active-nil and
   incomplete in-flight admission records remain fail-closed;
4. exact provider accounting, usage-coverage failure, permissions,
   cancellation, ACP negotiation, and headless process limits remain; and
5. the persistence repair and default promotion ship atomically.

The gate was satisfied before implementation. P49 completed on 2026-08-07,
closed G21 and G47, left the active queue, and promoted P47.2 as the sole
`Ready` slice. The executed test-first plan remains under
`docs/superpowers/plans/` as closeout evidence.
