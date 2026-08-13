# Auto Permission Containment Admission Implementation Plan

> **Historical note:** this candidate plan is superseded by the public
> Desktop-aware proof-bound Auto Bash forward-port plan.

**Status:** historical
**Queue state:** superseded by the public Desktop-aware forward-port plan
**Created:** 2026-08-07
**Source snapshot:** `origin/master` at
`de74294b29f40d19bfd0e37f09889bd6f8037d90`
**Adoption:** `project-native`

> **Ownership:** candidate test-first delivery plan for P51.2, the Auto
> Permission stage accepted in design but not yet promoted by the root queue;
> see the [Darwin Sandbox And Auto Permission Design](../specs/2026-08-07-darwin-sandbox-auto-permission-design.md)

**Goal:** Remove routine Auto-mode Bash permission requests only when the exact
canonical action is bound to the exact Darwin Guest containment proof, while
retaining deterministic destructive prompts, explicit policy authority, hook
rewrite re-evaluation, and pre-dispatch binding validation.

**Architecture:** QueryEngine projects immutable local containment facts into
`PermissionActionDescriptor`; those facts never enter the semantic reviewer.
After explicit deny/ask and exact user authority, a narrow deterministic gate
may allow canonical Bash in `ModeAuto`. The settled action binds the Guest
policy/proof generation, and `toolExecutor` plus `ShellManager` revalidate it
immediately before execution. Containment stays active in every permission mode.

**Tech Stack:** Go 1.26.5, QueryEngine canonical invocation policy,
`ToolRiskClassifier`, P42.1 Guest bindings, PreToolUse rewrites, persistent
ShellManager, entrypoint fixtures, race detector, and Makefile gates.

## Global Constraints

- Execute only after P51.1 is merged and root `docs/migration/queue.yaml`
  admits P51.2 as its sole `Ready` slice.
- Only `ModeAuto` plus canonical built-in `Bash` may use the shortcut. Do not
  change Default, Plan, AcceptEdits, DontAsk, Bypass, or Bubble semantics.
- Bypass may skip permission but never bypasses Seatbelt.
- Explicit deny and ask rules, Plan containment, tool selection, schema/custom
  validation, and exact user authority retain their current ordering.
- Deterministically destructive Bash prompts unless exact user authority covers
  the same canonical command/input.
- Require filesystem read/write, network denial, root identity, descendant
  confinement, descendant cleanup, wall time, and output axes. Deliberately do
  not require memory, file descriptors, or process count.
- Require aggregate `StateDegraded`, `AdapterDarwinSeatbelt`,
  `NetworkDenied`, and `CredentialAmbientEnvironment`; never reinterpret
  `degraded` as fully enforced.
- PreToolUse rewrites rebuild and re-evaluate the complete action. No stale
  decision or grant may survive changed input.
- Binding drift returns `sandbox_binding_expired`, persists no grant, dispatches
  nothing, and never retries ambient.
- Reviewer shadow remains non-authoritative and is not a prerequisite for the
  shortcut.

---

## Task 1: Bind trusted containment facts into canonical actions

**Files:**

- Modify: `engine/permission_action.go`
- Modify: `engine/permission_action_test.go`
- Modify: `engine/engine.go`
- Modify: `tools/bash_shell.go`

- [ ] **Step 1: Add local descriptor fields**

Extend `PermissionActionDescriptor` with:

```go
ExecutionPolicyDigest string
ExecutionBindingDigest string
ExecutionProfile      containment.Profile
ExecutionState        containment.State
ExecutionAdapter      containment.AdapterFamily
ExecutionNetwork      containment.NetworkMode
CredentialMode        containment.CredentialMode
AdapterAxes           containment.EnforcementAxes
RuntimeAxes           containment.EnforcementAxes
EnforcementAxes       containment.EnforcementAxes
ExecutionCapabilityGeneration string
GuestProcess          bool
ContainmentAvailable  bool
ContainmentReasonCode string
```

Keep the existing tool-registry `CapabilityGeneration uint64` unchanged.
`ExecutionCapabilityGeneration` is the adapter-generation identity; include
both fields in action equality and execution-lease comparisons so tool-registry
and sandbox capabilities cannot be confused.

- [ ] **Step 2: Populate facts only from the engine Guest binding**

`buildPermissionActionDescriptor` reads the immutable QueryEngine Guest binding
and marks `GuestProcess` only when the resolved action is the registered,
selected, built-in canonical `Bash`. Tool input, hooks, permission mode, ACP,
and model output cannot set any containment field. Diagnostics carry stable
reason codes but no paths/profile text.

- [ ] **Step 3: Include every containment fact in exact binding checks**

Update `samePermissionActionAuthorityBinding`, clone helpers, permission
settlement, ProjectGraph revision identity, and dispatch checks. A policy or
binding digest, generation, adapter/runtime/combined proof-axis, profile, state,
network, credential, availability, or Guest-class mismatch must make the action
unequal.

- [ ] **Step 4: Expose ShellManager's pinned identity safely**

Add a value-returning `ExecutionBindingDiagnostic()` that contains policy and
binding digests, adapter generation, adapter/runtime/combined axes, state, and
adapter only. It must not expose roots, environment, profile text, or the
binding pointer.

- [ ] **Step 5: Run descriptor tests**

```bash
go test ./engine/ ./tools/ -run 'P512.*(Descriptor|Binding)|PermissionAction' -count=1
```

## Task 2: Fail required-but-unavailable Guest Bash before permission

**Files:**

- Modify: `engine/engine.go`
- Modify: `engine/query_engine_permission_test.go`
- Modify: `engine/permission_action_test.go`

- [ ] **Step 1: Add a pre-permission availability gate**

Immediately after canonical action construction and tool selection, reject
Guest Bash with its stable reason code when `ContainmentAvailable` is false.
This occurs before rule/mode/classifier/prompt decisions, so an allow rule,
exact grant, `--yolo`, or reviewer cannot authorize an unavailable spawn.

- [ ] **Step 2: Pin every mode**

Table-test Default, Plan, AcceptEdits, DontAsk, Auto, Bypass, and Bubble with a
missing executable and failed probe. Require zero prompt calls, zero classifier
calls, zero grants, zero tool dispatch, and the correct sandbox diagnostic.
Keep the application/engine otherwise usable.

- [ ] **Step 3: Prove non-Bash behavior is unchanged**

Use Read, Write, BashOutput, KillShell, Agent, MCP, dynamic, network, and direct
user-interaction fixtures. An unavailable Guest binding must not be presented as
their permission reason because those process classes do not use it.

- [ ] **Step 4: Run focused tests**

```bash
go test ./engine/ -run '^TestP512.*Unavailable' -count=1
```

## Task 3: Add the narrow Auto containment shortcut

**Files:**

- Modify: `engine/permission_action.go`
- Modify: `engine/engine.go`
- Modify: `engine/query_engine_permission_test.go`
- Modify: `engine/permission_action_test.go`
- Modify: `engine/permission/risk_classifier.go`
- Modify: `engine/permission/evaluator_test.go`

- [ ] **Step 1: Define the required proof mask in one place**

Add:

```go
const autoBashRequiredAxes =
    containment.AxisFilesystemRead |
    containment.AxisFilesystemWrite |
    containment.AxisNetworkDenied |
    containment.AxisRootIdentity |
    containment.AxisDescendantConfinement |
    containment.AxisDescendantCleanup |
    containment.AxisWallTime |
    containment.AxisOutput
```

Implement `autoContainmentAdmits(action)` as a pure predicate requiring exact
mode, canonical tool, Guest ownership, workspace-write, degraded, Darwin
Seatbelt, network denied, ambient-environment credential mode, matching
non-empty policy/binding digest and generation, availability, the accepted
adapter/runtime source masks, and the complete combined mask. Explicitly ignore
memory/FD/process-count axes rather than requiring or fabricating them.

- [ ] **Step 2: Keep exact user authority ahead of risk prompting**

Retain `approvalAuthorizesAction` and exact user/local rule authority before the
new gate. Then classify canonical Bash with the existing deterministic
`ToolRiskClassifier`:

```go
risk := permission.NewToolRiskClassifier().Classify(
    actionDescriptor.CanonicalToolName,
    actionDescriptor.Input,
)
if risk.Level == permission.RiskDestructive {
    allowed, reason := e.promptForTool(
        ctx,
        inner,
        toolName,
        input,
        toolCtx,
        &actionDescriptor,
    )
    return e.recordPromptPolicyOutcome(
        ctx,
        actionDescriptor,
        allowed,
        reason,
        false,
    )
}
if autoContainmentAdmits(actionDescriptor) {
    return allowInvocationPolicy()
}
```

Use the existing prompt/settlement owner rather than adding a second prompt
API. Read-only, write, and unknown Bash risks may take the shortcut.

- [ ] **Step 3: Preserve all preceding authorities**

Add table tests proving explicit deny denies, explicit ask prompts, Plan denies,
tool exclusion denies, invalid schema/custom input denies, and an exact user
grant authorizes the same destructive input. None of these decisions may be
weakened by containment.

- [ ] **Step 4: Pin the accepted resource risk**

Add a non-destructive-classified high-process or high-FD shell fixture to the
pure decision test and require the shortcut when all required axes exist. Name
the test and failure message as accepted missing-resource-axis behavior; do not
execute a real fork bomb or resource exhaustion command.

- [ ] **Step 5: Run decision tests**

```bash
go test ./engine/ ./engine/permission/ -run 'P512|Auto.*Permission|RiskClassifier' -count=1
```

## Task 4: Revalidate hook rewrites and final dispatch binding

**Files:**

- Modify: `engine/tool_execution.go`
- Create: `engine/p51_2_auto_containment_test.go`
- Modify: `engine/permission_action_test.go`
- Modify: `engine/engine.go`
- Modify: `tools/bash_shell.go`
- Modify: `tools/bash_shell_test.go`

- [ ] **Step 1: Prove PreToolUse rewrite runs before the shortcut**

Add fixtures that rewrite routine Bash to destructive Bash, routine Bash to an
invalid command shape, and one routine command to another routine command. The
first must prompt, the second must deny, and the third may shortcut only after
its rewritten canonical input receives a fresh descriptor and risk result.
Require one hook call and one complete permission evaluation.

- [ ] **Step 2: Bind the settled action through `toolExecutor`**

Before acquiring the tool execution lease, compare the settled action's Guest
digest, adapter generation, and axes with both the current QueryEngine Guest
binding and `ShellManager.ExecutionBindingDiagnostic()`. Return the stable
`sandbox_binding_expired` error on mismatch. Do not persist or reuse a grant
from the failed dispatch.

- [ ] **Step 3: Revalidate again inside ShellManager**

`BashTool` passes the exact Guest binding through context. `ExecuteAt` requires
the request, manager, and persistent shell to share policy digest and adapter
generation before writing the command. This final local check owns process
selection; QueryEngine's check owns permission/action settlement.

- [ ] **Step 4: Add deterministic drift barriers**

Use a test-only barrier after permission settlement and before
`ToolExecutor`. Swap or invalidate the root/binding, release the barrier, and
require `sandbox_binding_expired`, zero command output, zero new grant, and no
ambient retry. Cover concurrent shell recovery and engine close under race.

- [ ] **Step 5: Run focused race tests**

```bash
go test ./engine/ ./tools/ -run 'P512.*(Rewrite|Dispatch|Drift)' -count=1
go test -race ./engine/ ./tools/ -run 'P512.*(Rewrite|Dispatch|Drift)' -count=20
```

## Task 5: Prove prompt reduction and cross-entrypoint consistency

**Files:**

- Modify: `engine/p51_2_auto_containment_test.go`
- Modify: `cmd/eino-agent/cmd/headless_permission_test.go`
- Modify: `cmd/eino-agent/cmd/root_test.go`
- Modify: `server/acp/agent_test.go`
- Modify: `internal/tui/permission_lifecycle_test.go`
- Modify: `engine/execution_policy_test.go`

- [ ] **Step 1: Add a routine zero-prompt corpus**

Exercise `go test`, `make test`, `git status`, `git diff`, search, formatting,
generated-file writes, cwd changes, foreground Bash, background Bash, and child
Agent Bash. Under a valid P51.1 execution proof, require zero permission prompts
and the same sandbox binding at dispatch.

- [ ] **Step 2: Add a destructive prompt corpus**

Cover recursive deletion, `git reset --hard`, `git clean -fd`, forced push,
raw-disk write patterns, dangerous chmod/chown, and shell-pipe/eval patterns.
Absent exact authority, require one human prompt and zero dispatch on denial.

- [ ] **Step 3: Prove entrypoint and mode matrix**

TUI, Plain, headless, Goal, ACP, and Child Agent must agree on the same action.
Default, Plan, and AcceptEdits retain current outcomes. Bypass dispatches without
a permission prompt but still carries the Seatbelt binding. Hooks and MCP never
contribute Guest proof.

- [ ] **Step 4: Measure prompt reduction without claiming universal safety**

Report corpus action count, prompt count before the shortcut, prompt count after
the shortcut, destructive prompt count, and stale/unavailable denials. Do not
label this security recall or reviewer accuracy; it is deterministic fixture
coverage only.

- [ ] **Step 5: Run package-width and repeated tests**

```bash
go test ./engine/ ./cmd/eino-agent/cmd/ ./server/acp/ ./internal/tui/ -run 'P512' -count=1
go test ./engine/ -run '^TestP512AutoContainmentCorpus$' -count=100
go test -race ./engine/ ./tools/ -run 'P512' -count=20
```

## Task 6: Close P51.2 while retaining the accepted limits

**Files:**

- Modify: `docs/architecture/capabilities/permissions.md`
- Modify: `docs/architecture/platform/runtime-services.md`
- Modify: `docs/guides/permissions-and-safety.md`
- Create: `docs/migration/verification/p51-2-auto-containment-admission.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p51-2-auto-containment-admission.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p42-host-execution-containment.md`
- Modify: `docs/migration/plans/p22-auto-permission-review.md`

- [ ] **Step 1: Document the exact shortcut and non-goals**

State every required axis, destructive prompt rule, binding revalidation,
environment consequence, and missing resource axis. Keep reviewer enforcement
disabled, G28 open, and Darwin-only scope explicit. Remove only P51.2 from the
queue and render the next legal state.

- [ ] **Step 2: Run real Darwin product acceptance**

Repeat the routine/destructive corpus through at least one interactive and one
non-interactive real entrypoint. Report unit/race, real Seatbelt, physical
terminal, remote CI, and product acceptance as separate evidence classes.

- [ ] **Step 3: Run final repository gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [ ] **Step 4: Commit and open one atomic Auto integration PR**

Use scoped `git add` over P51.2 source, tests, and owned docs, then commit:

```bash
git commit -m "feat(permission): admit contained Auto Bash"
```

The PR must state `project-native`, exact decision order, proof axes, accepted
environment/resource risks, prompt corpus numbers, no reviewer enforcement,
rollback by removing only the shortcut, full local gates, remote CI, and real
Darwin acceptance.
