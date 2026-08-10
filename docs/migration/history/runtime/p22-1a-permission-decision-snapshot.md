# P22.1a Permission Decision And Policy Snapshot

**Status:** historical
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for the behavior-preserving QueryEngine
> decision seam and shared effective-policy identity. Current behavior belongs
> in [`permissions.md`](../../../architecture/capabilities/permissions.md);
> remaining Auto-review work and executable order belong in
> [`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P22.1a was delivered under the **`combine`** decision:

- preserve QueryEngine as the sole user-visible invocation-policy coordinator;
- preserve every existing rule, Plan, mode, grant, path, classifier, prompt,
  event, and denial-accounting outcome;
- combine those branches behind one project-owned four-decision seam;
- adapt the existing ProjectGraph drift digest into one shared QueryEngine
  effective-policy snapshot identity; and
- keep direct nil/nil library construction caller-authoritative rather than
  misreporting the absence of policy as an allow.

No public API, durable schema, configuration key, model route, or entrypoint
interaction changed.

## Outcome

[`QueryEngine.evaluateInvocationPolicy`](../../../../engine/engine.go) now
returns one internal outcome before `wrapCanUseTool` adapts it to the
established bool/reason callback:

| Decision | Preserved branch family |
|---|---|
| `Allow` | Exact Plan-file capability, explicit allow, bypass, safe registry defaults, scoped grants, contained reads/writes, and current Auto safe fast paths. |
| `Deny` | Tool selection, Plan hard denial, explicit deny, and `dontAsk`. |
| `RequireHuman` | Exact Exit approval, explicit ask, and otherwise unmatched prompt or external-callback handling. |
| `Review` | The existing Auto classifier-eligible path, including allow, deny, and model-error fallback to the existing prompt. |

Pre-tool stop, `DenyReason`, and hook permission denial still terminate in
[`executeToolCall`](../../../../engine/tool_execution.go) before the seam, with
the same tool-result shape and without prompt or classifier work. A direct
engine whose `CanUseTool` and `PermissionPrompt` are both nil still installs no
wrapper; the typed `NoInvocationPolicyInstalled` boundary remains outside the
four decisions.

The legacy classifier remains the `Review` implementation in this slice.
Untagged classifier output still denies, model failure still clears classifier
status and reaches the existing prompt, and classifier allow/deny events and
denial accounting retain their prior ordering.

## Shared Effective-Policy Identity

[`QueryEngine.effectivePolicySnapshot`](../../../../engine/permission_policy.go)
immediately encodes detached values and freezes both the canonical encoding and
its SHA-256 identity:

| Input family | Identity content |
|---|---|
| Rules | Effective rules in load order, including `Source` provenance. |
| Grants | Canonically ordered approval key, session scope, and root-session scope; timestamps and display-only reasons are excluded. |
| Mode and Plan | Effective mode plus Plan phase, revision, and plan-file identity. |
| Session and filesystem | Root session, CWD, and additional roots. |
| Tools | Preset/names with an explicit distinction between no selection and an installed empty selection. |
| Reserved | Capability generation, reviewer-policy version, and child scope use one fixed omitted/unpopulated representation. |

An unchanged effective input produces the same identity, and every populated
input family changes it when mutated. The returned snapshot retains no mutable
rules, approval, root, or tool-selection alias. It is a detached observation,
not a new cross-owner transaction or lock hierarchy.

[`projectGraphPolicyRevision`](../../../../engine/graph_hitl.go) now returns
this snapshot ID directly. ProjectGraph therefore has no independent policy
hash owner and does not become a policy coordinator. The canonical encoding
preserves the pre-P22.1a ProjectGraph revision bytes for unchanged policy, so
an upgrade does not invalidate an already persisted pending HITL decision when
its effective policy has not changed.

## Entrypoint Evidence

Production composition roots already installed policy and required no wiring
change:

| Boundary | Production evidence | Focused evidence |
|---|---|---|
| TUI | [`runTUI`](../../../../cmd/yhc/cmd/root.go) installs `App.MakePermissionPromptFn` before constructing QueryEngine. | TUI permission prompt and structured-settlement tests pass. |
| Plain REPL | [`runPlainREPL`](../../../../cmd/yhc/cmd/root.go) installs `makePlainPermissionPrompt`. | Plain prompt, exact Plan target, and ProjectGraph resume tests pass. |
| Headless | [`configureHeadlessPermissions`](../../../../cmd/yhc/cmd/headless.go) clears interactive prompting and always installs an explicit allow-or-deny callback. | Headless no-interactive-policy and Plan-bypass tests pass. |
| ACP new/load/resume | [`agent.go`](../../../../server/acp/agent.go) installs the ACP permission prompt and canonical project registry on both construction paths. | Permission delegation, new/load/resume, and actual-session-CWD registry tests pass. |
| Child Agent | [`subagent.go`](../../../../engine/subagent.go) forwards the parent callback/prompt, registry, and root-session identity into the child QueryEngine. | Real child coalescing and root-isolation tests pass under the race detector. |
| Embedded library | Direct nil/nil construction remains caller-authoritative. | [`TestP221aInvocationPolicyProjectionAndLegacyReview`](../../../../engine/permission_policy_test.go) proves it is not an `Allow` decision. |

These roots all reach the same QueryEngine seam. They do not gain a separate
adapter policy or a reviewer-specific identity.

## Evidence

Focused tests prove:

- `TestP221aInvocationPolicyProjectionAndLegacyReview` exercises real
  deterministic deny/allow, human callback, classifier allow/deny, untagged
  denial, and model-error fallback branches through the new seam;
- `TestP221aPolicySnapshotIdentityInputs` covers every populated input family,
  including rule provenance/order and nil-versus-empty tool selection;
- `TestP221aPolicySnapshotCanonicalAndImmutable` covers canonical grant order,
  display-only exclusions, the fixed omitted reserved representation, and
  alias isolation;
- `TestP221aPolicySnapshotPreservesLegacyProjectGraphRevision` freezes the
  pre-upgrade encoding across nil, empty, preset, and named tool selections
  and proves pending Graph identity remains compatible; and
- `TestP221aProjectGraphRevisionUsesSharedSnapshot` proves ProjectGraph consumes
  the same snapshot identity and observes policy drift.

Closeout passed:

```text
go test -race ./engine -count=1 -run 'Test(P221a.*|AutoModeClassifierAllowAndDenyLifecycle|AutoModeClassifierErrorClearsAndPrompts|P138ProjectGraphPolicyChangeExpiresPriorIntent|PermissionSessionGrantCoalescesRealSubagentExecution|RootAndChildShareSessionApprovalWithoutSharingOtherRoots)$'
go test ./cmd/eino-agent/cmd -count=1 -run 'Test(ConfigureHeadlessPermissionsNeverInstallsInteractivePrompt|HeadlessBypassCannotFabricatePlanApproval|PlainPermissionPromptUsesTruthfulGrantScopes|PlainPlanApprovalReturnsExactStructuredTarget|DrivePlainQueryEventsResolvesProjectGraphPlanApproval)$'
go test ./server/acp -count=1 -run 'TestACP_(PermissionDelegation|ResumeSession|LoadSession|PermissionRegistryUsesActualSessionCWDAndCanonicalProjectIdentity)$'
go test ./internal/tui -count=1 -run 'Test(PermissionPrompt_|PermissionInteractionResult|PermissionTerminalResult|CoordinatorPermissionEvent)'
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check-ledger
git diff --check
```

## Compatibility, Exclusions, And Rollback

The compatibility contract is intentionally observational: callers still see
the same bool/reason values, and the same callbacks, events, denial records,
and execution decisions occur in the same order. The snapshot retains the
previous ProjectGraph canonical JSON layout and revision bytes, so P22.1a
introduces no durable-schema migration or interaction invalidation for an
unchanged effective policy.

P22.1a did not add a canonical action descriptor, capability policy, complete
updated-input re-evaluation, separate reviewer, review-request binding, shadow
audit, or enforcement. The policy snapshot is not an approval and is not
supplied to the legacy classifier. G14 therefore remains open.

Rollback is one code-and-test unit plus this documentation. It restores the
previous inline bool/reason branches and duplicate ProjectGraph hash without
data migration. Later P22 slices must not depend on the seam or identity until
this delivery remains merged.

## Next State

P22.1b-P22.6 remain queued. P22.1b owns the canonical descriptor and complete
updated-input decision cycle; P22.2a owns the separate reviewer and exact
binding. Root `PLAN.md` has no `Ready` slice after this closeout and must
explicitly select the next state owner.
