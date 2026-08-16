# Proof-Bound Auto Bash Public Forward-Port Implementation Plan

**Status:** historical
**Accepted:** 2026-08-13
**Source baseline:** public Desktop candidate at `a6c478c`
**Adoption:** `project-native`

> **Historical execution note:** delivery used the migration, runtime-depth,
> TUI-runtime, and iteration workflows after Task 0 admitted P51.2. The
> unchecked boxes below preserve the original test-first sequence; they are no
> longer live instructions or queue state.
>
> **Ownership:** executable TDD sequence for the public proof-bound Auto Bash
> admission, runtime, client propagation, Darwin evidence, and closeout

**Goal:** Forward-port proof-bound Auto Bash into public YHC so routine
canonical Bash can run without a permission request only under complete Darwin
Guest proof, while narrow critical literal `rm`/`rmdir` invocations require a
fresh live `AllowOnce` across every supported entrypoint.

**Architecture:** QueryEngine owns one detached Guest execution identity, one
typed permission-decision constraint, and one final dispatch revalidation
path. ProjectGraph persists and digests the constraint; Plain, TUI, ACP,
AppServer, Desktop, and Web UI only project the allowed choices. The public
implementation is stacked on the public Desktop candidate at `a6c478c` and is
rebased or replayed onto public `origin/master` after that candidate merges.

**Tech Stack:** Go 1.26.5, Eino ProjectGraph HITL, Darwin Seatbelt,
ShellManager persistent Bash, Bubble Tea TUI, ACP, Go AppServer, browser-native
JavaScript modules, Node test runner, public migration queue, and publication
materialization gates.

## Global Constraints

- Public module and CLI paths remain `github.com/abietic/yhc` and `cmd/yhc`.
- Do not merge, cherry-pick, name, or publish a private commit, branch, URL,
  implementation diary, or build result.
- Preserve explicit deny and ask, Plan containment, tool selection, schema and
  custom validation ahead of the shortcut. Incomplete or unavailable Guest
  proof uses the existing Auto fallback and can never supply proof-bound
  authority.
- Only canonical built-in `Bash` in `ModeAuto` may use proof-bound admission.
- Complete proof requires filesystem read/write, network denial, root identity,
  descendant confinement/cleanup, wall time, and bounded output from the exact
  available Darwin Seatbelt `workspace-write` binding.
- Keep aggregate state `degraded`, credential mode `ambient-environment`,
  `os.Environ()`, and `TMPDIR` unchanged. Do not invent memory,
  file-descriptor, or process-count enforcement.
- Recognized critical Bash is the sole mode exception: Bypass requires a live
  `AllowOnce`; DontAsk denies because it cannot interact.
- Unknown critical syntax is a non-match, not a new sandbox deny or prompt.
- A PreToolUse rewrite rebuilds the entire action and repeats policy evaluation.
- Final drift returns exactly `sandbox_binding_expired` before registry acquire,
  persistent-shell stdin submission, or process start, with no ambient retry.
- `PermissionDecisionConstraint` is authority. `GrantScopes` is a bounded
  display projection and cannot authorize a result.
- Keep G28 open for ambient credentials, hooks/MCP, and hard resource axes.
- Work only in the isolated stacked worktree. Before push, refresh the public
  Desktop base, replay onto its merged public master, regenerate
  `PUBLICATION_MANIFEST.json`, and rerun all committed-tree evidence.

---

## File and interface map

| Unit | Responsibility | Primary files |
|---|---|---|
| Public admission | Make the reviewed outcome executable without claiming implementation | `docs/migration/queue.yaml`, `docs/migration/plans/p51-2-auto-containment-admission.md`, generated `PLAN.md` |
| Critical recognizer | Recognize only literal root/home/volume/all-entry `rm`/`rmdir` targets | `engine/permission/bash_critical_path.go` |
| Guest identity | Detach and revalidate exact binding/proof/root facts | `engine/containment/binding.go`, `tools/bash_shell.go`, `engine/permission_action.go` |
| Decision constraint | Reject persistent critical decisions and grant coalescing | `engine/permission_interaction.go`, `engine/permission_presentation.go` |
| Durable HITL | Preserve constraint through interrupt, restart, replay, and resume digests | `engine/events.go`, `engine/runtime_state.go`, `engine/graph_hitl.go`, `engine/graph_query_kernel.go` |
| Policy and dispatch | Order critical prompt and proof shortcut; enforce final zero-submission drift failure | `engine/engine.go`, `engine/tool_execution.go`, `tools/bash_shell.go` |
| Terminal clients | Render/submit only constrained choices | `cmd/yhc/cmd/root.go`, `internal/tui/`, `server/acp/agent.go` |
| Desktop clients | Digest and validate constraint; render server scopes | `server/appserver/permission_broker.go`, `internal/webui/assets/` |
| Closure evidence | Publish verified current behavior and retain G28 | architecture, guide, migration verification/history, manifest |

## Task 0: Admit one public-native P51.2 slice

**Files:**

- Create: `docs/migration/plans/p51-2-auto-containment-admission.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/STATUS.md`
- Modify: `docs/migration/plans/p42-host-execution-containment.md`
- Modify: `docs/migration/plans/README.md`
- Modify: `docs/superpowers/specs/2026-08-07-darwin-sandbox-auto-permission-design.md`
- Modify: `docs/superpowers/specs/README.md`
- Modify: `PUBLICATION_MANIFEST.json`

**Interfaces:**

- Consumes: the approved public forward-port design and implemented P51.1
  Darwin Guest binding.
- Produces: sole queue row `P51.2` in state `ready`, contract
  `plans/p51-2-auto-containment-admission.md#observable-contract`, and G28
  retained.

- [ ] **Step 1: Write the migration-native contract**

Create the contract with this non-current ownership block:

```markdown
# P51.2 Proof-Bound Auto Bash Admission

**Status:** active-plan
**Accepted:** 2026-08-13
**Adoption:** `project-native`
**Gap:** G28 remains open

> **Ownership:** executable contract for proof-bound ordinary Auto Bash,
> narrow critical live AllowOnce, durable constraint propagation, and final
> zero-submission revalidation

## Intake evidence

P51.1 supplies an available Darwin Guest binding with the accepted granular
axes. The public Desktop candidate supplies typed AppServer permission
presentation, so P51.2 includes AppServer and Web UI.
```

Copy the approved decision order, axes, literal critical corpus, entrypoint
matrix, failure results, rollback, and real Darwin promotion requirements from
the approved public design. Label all of them accepted future requirements.

- [ ] **Step 2: Make P51.2 the sole Ready row**

Replace `slices: []` with:

```yaml
slices:
  - id: P51.2
    state: ready
    priority: 170
    gaps: [G28]
    promotion:
      id: p51-2-public-proof-bound-auto-bash-intake
      state: satisfied
      label: public Guest proof and cross-entrypoint permission intake
      link: plans/p51-2-auto-containment-admission.md#intake-evidence
    contract: plans/p51-2-auto-containment-admission.md#observable-contract
    outcome: Auto-allow exact proof-bound Darwin Guest Bash while critical literal rm/rmdir requires fresh AllowOnce and no entrypoint can broaden it.
```

Keep P44 deferred unchanged and set `updated` to `"2026-08-13"`.

- [ ] **Step 3: Render and prove queue consistency**

```bash
go run ./scripts/migration_queue render
go run ./scripts/migration_queue check
```

Expected: one Ready slice, no cycle or second active slice, and PLAN links the
new contract.

- [ ] **Step 4: Update only admission facts**

Change `REMAINING.md`, `STATUS.md`, P42, and plan/spec indexes from “not
admitted” to “Ready but not implemented.” Do not edit current architecture or
the guide and do not create completion verification/history.

- [ ] **Step 5: Run the admission before/after oracle**

Before editing:

```bash
go run ./scripts/migration_queue check
rg -n 'P51\.2.*(not admitted|pending intake|no successor)' docs/migration docs/superpowers/specs
```

Expected before: zero active slices and old non-executable statements. After
Steps 1-4: one Ready row and no statement falsely says P51.2 is unadmitted.

- [ ] **Step 6: Commit the admission checkpoint**

```bash
REFERENCE_DIR="${REFERENCE_DIR:?set to the reviewed external reference parent}" \
  ITERATION_BASE=origin/codex/feat/desktop-workbench \
  ITERATION_SLICE_ID=P51.2 make change-plan
REFERENCE_DIR="${REFERENCE_DIR:?set to the reviewed external reference parent}" \
  ITERATION_BASE=origin/codex/feat/desktop-workbench \
  ITERATION_SLICE_ID=P51.2 make verify-focused
git add PUBLICATION_MANIFEST.json docs/migration docs/superpowers/specs
git commit -m "docs(permission): admit public proof-bound Auto Bash"
```

Regenerate the manifest from a clean materialized tree, then run merge evidence
for the same stacked base and slice ID.

## Task 1: Add the narrow literal critical-path recognizer

**Files:**

- Create: `engine/permission/bash_critical_path.go`
- Create: `engine/permission/bash_critical_path_test.go`

**Interfaces:**

```go
type BashCriticalPathDecision struct {
    Match      bool
    ReasonCode string
}

func ClassifyBashCriticalPath(command, cwd, home string) BashCriticalPathDecision
```

- [ ] **Step 1: Write the failing literal corpus**

Cover `/`, `/etc`, `/Volumes/Data`, `/Volumes/Data/child`, exact home, `~`,
`*`, `/tmp/*`, `../*`, and multiple simple commands. Add non-matches for
`echo rm /`, substitution, pipes, redirection, unsupported globs, malformed
quotes, and non-absolute CWD/home.

```go
func TestP512ClassifyBashCriticalPathLiteralCorpus(t *testing.T) {
    tests := []struct {
        name, command, cwd, home, reason string
        match                            bool
    }{
        {name: "root", command: "rm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
        {name: "parent entries", command: "rm -rf ../*", cwd: "/work/repo", home: "/home/u", match: true, reason: "all_entries"},
        {name: "unknown pipeline", command: "printf x | rm -rf /", cwd: "/work", home: "/home/u"},
    }
    for _, test := range tests {
        t.Run(test.name, func(t *testing.T) {
            got := ClassifyBashCriticalPath(test.command, test.cwd, test.home)
            if got.Match != test.match || got.ReasonCode != test.reason {
                t.Fatalf("decision = %#v, want match=%v reason=%q", got, test.match, test.reason)
            }
        })
    }
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./engine/permission -run '^TestP512ClassifyBashCriticalPath' -count=1
```

Expected: FAIL because the classifier is undefined.

- [ ] **Step 3: Implement the minimal tokenizer and classifier**

Accept literal words, ordinary quoting/escaping, `;`, newlines, `&&`, and
`||`. Return unknown for `$`, backticks, redirection, substitutions, single
pipe/ampersand, and malformed quoting.

```go
func ClassifyBashCriticalPath(command, cwd, home string) BashCriticalPathDecision {
    if !filepath.IsAbs(cwd) || !filepath.IsAbs(home) {
        return BashCriticalPathDecision{}
    }
    segments, ok := tokenizeCriticalPathCommand(command)
    if !ok {
        return BashCriticalPathDecision{}
    }
    for _, segment := range segments {
        if decision := classifyCriticalPathSegment(segment, filepath.Clean(cwd), filepath.Clean(home)); decision.Match {
            return decision
        }
    }
    return BashCriticalPathDecision{}
}
```

- [ ] **Step 4: Run GREEN and commit**

```bash
go test ./engine/permission -run '^TestP512ClassifyBashCriticalPath' -count=1
go test ./engine/permission -count=1
git add engine/permission/bash_critical_path.go engine/permission/bash_critical_path_test.go
git commit -m "feat(permission): recognize critical Bash paths"
```

## Task 2: Bind exact Guest execution identity to the action

**Files:**

- Modify: `engine/containment/binding.go`
- Modify: `engine/containment/binding_test.go`
- Modify: `tools/bash_shell.go`
- Modify: `tools/bash_shell_test.go`
- Modify: `engine/permission_action.go`
- Modify: `engine/permission_action_test.go`

**Interfaces:**

```go
type ExecutionIdentity struct {
    ProcessClass ProcessClass
    PolicyDigest, BindingDigest string
    Profile Profile
    State State
    Adapter AdapterFamily
    Network NetworkMode
    CredentialMode CredentialMode
    Availability BindingAvailability
    ReasonCode ReasonCode
    CapabilityGeneration string
    AdapterAxes, RuntimeAxes, Enforced EnforcementAxes
    Root RootIdentity
}

func ExecutionIdentityFor(*Binding, ExecutionProof) (ExecutionIdentity, error)
func (m *ShellManager) GuestExecutionIdentity() (containment.ExecutionIdentity, error)
func (m *ShellManager) RevalidateGuestExecutionIdentity() (containment.ExecutionIdentity, error)
```

- [ ] **Step 1: Write failing detached identity tests**

Test complete/unavailable Guest, ambient rejection, proof mismatch, and root
replacement. Assert diagnostics expose no path, device, inode, environment
value, or binding pointer.

```go
func TestP512ExecutionIdentityRequiresExactGuestProof(t *testing.T) {
    binding, proof := completeDarwinGuestFixture(t)
    got, err := containment.ExecutionIdentityFor(binding, proof)
    if err != nil || got.BindingDigest != binding.Digest() || got.Root.Path == "" {
        t.Fatalf("identity = %#v, err = %v", got, err)
    }
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./engine/containment ./tools ./engine -run '^TestP512.*(ExecutionIdentity|Descriptor)' -count=1
```

- [ ] **Step 3: Implement identity and action fields**

Add detached identity and ShellManager value methods. Extend
`PermissionActionDescriptor` with execution policy/binding digest, profile,
state, adapter, network, credential mode, availability/reason, adapter/runtime/
combined axes, adapter generation, Guest marker, and root identity. Keep the
tool-registry `CapabilityGeneration uint64` separate. Populate only from the
QueryEngine Guest matrix for registered selected built-in canonical `Bash`.

- [ ] **Step 4: Implement the pure proof predicate**

```go
func completeContainedAutoBashProof(action PermissionActionDescriptor) (bool, string)
```

Require exact Auto/built-in shell identity, available Darwin Seatbelt,
workspace-write/degraded/network-denied/ambient-environment facts, non-empty
matching digests/generation/root, exact source masks, and complete combined
mask.

- [ ] **Step 5: Run GREEN and commit**

```bash
go test ./engine/containment ./tools ./engine -run 'P512.*(ExecutionIdentity|Descriptor|Proof)|PermissionAction' -count=1
go test ./engine/containment ./tools -count=1
git add engine/containment/binding.go engine/containment/binding_test.go \
  tools/bash_shell.go tools/bash_shell_test.go \
  engine/permission_action.go engine/permission_action_test.go
git commit -m "feat(permission): bind Guest proof to Bash actions"
```

## Task 3: Enforce the engine-owned AllowOnce-only constraint

**Files:**

- Modify: `engine/permission_interaction.go`
- Modify: `engine/permission_interaction_test.go`
- Modify: `engine/permission_presentation.go`
- Modify: `engine/permission_presentation_test.go`

**Interfaces:**

```go
type PermissionDecisionConstraint string

const (
    PermissionDecisionUnconstrained PermissionDecisionConstraint = ""
    PermissionAllowOnceOnly PermissionDecisionConstraint = "allow_once_only"
)

func (c PermissionDecisionConstraint) valid() bool
func (c PermissionDecisionConstraint) permits(PermissionInteractionDecision) bool
```

`PermissionPromptRequest` gains `DecisionConstraint`. Presentation accepts the
constraint and derives normal three scopes or constrained `AllowOnce`.

- [ ] **Step 1: Write failing settlement tests**

Cover AllowOnce, deny, cancel, timeout, forged session/always, invalid
constraint, stored grant reuse, and concurrent coalescing.

```go
func TestP512AllowOnceConstraintRejectsPersistentDecision(t *testing.T) {
    result := normalizePermissionInteractionResultForConstraint(
        PermissionInteractionResult{Decision: PermissionAllowAlways},
        PermissionAllowOnceOnly,
    )
    if result.Decision != PermissionDeny ||
        result.Message != "permission decision is not allowed by request constraint" {
        t.Fatalf("result = %#v", result)
    }
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./engine -run '^TestP512.*(Constraint|Coalesc|Presentation)' -count=1
```

- [ ] **Step 3: Implement authoritative settlement**

Validate/clone the constraint, normalize every result through
`normalizePermissionInteractionResultForConstraint`, set
`pending.grantAllows = nil` when constrained, and reject persistent decisions
before grant/rule commit.

- [ ] **Step 4: Derive constrained presentation**

```go
func permissionPresentationForAction(
    kind string,
    action PermissionActionDescriptor,
    constraint PermissionDecisionConstraint,
) *PermissionPresentation
```

Allow ordinary available three-scope and constrained available one-scope
shapes. Cross-check presentation against the authoritative constraint.

- [ ] **Step 5: Run GREEN and commit**

```bash
go test ./engine -run 'P512.*(Constraint|Coalesc|Presentation)|PermissionInteraction|PermissionPresentation' -count=1
go test -race ./engine -run '^TestP512.*(Constraint|Coalesc)' -count=20
git add engine/permission_interaction.go engine/permission_interaction_test.go \
  engine/permission_presentation.go engine/permission_presentation_test.go
git commit -m "feat(permission): constrain critical decisions to AllowOnce"
```

## Task 4: Persist and digest the constraint through ProjectGraph

**Files:**

- Modify: `engine/events.go`
- Modify: `engine/runtime_state.go`
- Modify: `engine/runtime_state_test.go`
- Modify: `engine/graph_hitl.go`
- Modify: `engine/graph_hitl_test.go`
- Modify: `engine/graph_query_kernel.go`
- Modify: `engine/project_graph_round_history_test.go`

**Interfaces:**

- `DecisionConstraint` is present on `PermissionRequestEvent`,
  `projectGraphHITLRequest`, and `RuntimePermissionDecision`.
- Invocation and decision digests include the constraint.
- Test helper `newP512ProjectGraphConstraintFixture(t)` returns a fixture with
  `PersistInterrupt`, `RestoreEngine`, `PendingRequest`, `Resolve`, and
  `ExecuteCount` methods so the live and cold paths share one deterministic
  oracle rather than timing sleeps.

- [ ] **Step 1: Write failing live/restart tests**

Persist a constrained interrupt, reconstruct a fresh engine, assert the
reprojected event retains `PermissionAllowOnceOnly`, and reject forged
session/always before and after restart.

```go
func TestP512ProjectGraphColdRestartRetainsAllowOnceConstraint(t *testing.T) {
    fixture := newP512ProjectGraphConstraintFixture(t)
    fixture.PersistInterrupt(PermissionAllowOnceOnly)
    restored := fixture.RestoreEngine()
    request, ok := restored.PendingRequest()
    if !ok || request.DecisionConstraint != PermissionAllowOnceOnly {
        t.Fatalf("pending request = %#v, ok = %v", request, ok)
    }
    if restored.Resolve(PermissionAllowAlways) {
        t.Fatal("forged persistent decision unexpectedly settled")
    }
    if got := restored.ExecuteCount(); got != 0 {
        t.Fatalf("execute count = %d, want 0", got)
    }
}
```

- [ ] **Step 2: Run RED**

```bash
go test ./engine -run '^TestP512ProjectGraph.*Constraint' -count=1
```

- [ ] **Step 3: Add durable fields and validation**

```go
DecisionConstraint PermissionDecisionConstraint `json:"decision_constraint,omitempty"`
```

Add it to request/event/runtime values, cloning, checkpoint, snapshot, replay,
invocation digest, decision identity, and resume validation. Reject unknown
values.

- [ ] **Step 4: Fix both event producers**

Propagate the field through coordinator events, the first interrupt event in
`projectGraphQueryKernel`, and restored projection. Rebuild adapter requests
with kind, canonical tool, attempt, presentation, and constraint intact.

- [ ] **Step 5: Run GREEN and commit**

```bash
go test ./engine -run 'P512ProjectGraph|RuntimeState|GraphHITL' -count=1
go test -race ./engine -run '^TestP512ProjectGraph.*Constraint' -count=20
git add engine/events.go engine/runtime_state.go engine/runtime_state_test.go \
  engine/graph_hitl.go engine/graph_hitl_test.go engine/graph_query_kernel.go \
  engine/project_graph_round_history_test.go
git commit -m "feat(runtime): persist critical permission constraints"
```

## Task 5: Order critical prompting and proof-bound dispatch

**Files:**

- Modify: `engine/engine.go`
- Modify: `engine/tool_execution.go`
- Modify: `engine/query_engine_permission_test.go`
- Modify: `engine/permission_action_test.go`
- Modify: `tools/bash_shell.go`
- Modify: `tools/bash_shell_test.go`

**Interfaces:**

- Produces internal `permissionAdmissionContainedAutoBash`.
- Adds constrained `promptForCriticalBash`.
- Returns exact `sandbox_binding_expired` before acquire/submission drift.

- [ ] **Step 1: Write failing authority matrix**

Test critical Bash under Auto, Bypass, DontAsk, explicit deny/ask, session
grant, always rule, classifier allow, reviewer output, and coalescing. Require
one fresh prompt and AllowOnce for Auto/Bypass, zero-prompt denial for DontAsk,
and no stored authority satisfaction.

- [ ] **Step 2: Write ordinary proof/fallback tests**

Complete proof yields zero prompt/classifier for `git status` and
`go test ./...`. Missing axes enter existing Auto fallback; unavailable Guest
returns typed sandbox-unavailable.

- [ ] **Step 3: Run RED**

```bash
go test ./engine -run '^TestP512.*(Critical|Contained|Fallback)' -count=1
```

- [ ] **Step 4: Implement decision order**

Classify canonical Bash after construction/selection and before persistent
grants or `mode.ShouldAutoAllow`. A match calls `promptForCriticalBash` with
`PermissionAllowOnceOnly`. Non-critical complete proof is admitted immediately
before the expensive Auto capability/classifier path:

```go
if allowed, _ := completeContainedAutoBashProof(actionDescriptor); allowed {
    actionDescriptor.admission = permissionAdmissionContainedAutoBash
    setSettledPermissionAction(ctx, &actionDescriptor)
    return allowInvocationPolicy()
}
```

- [ ] **Step 5: Add two controllable drift barriers**

```go
var beforeContainedAutoAcquireForTest func()
var beforeGuestCommandSubmissionForTest func()
```

At the first barrier invalidate proof/root/binding/action/registry and require
exact error plus zero AcquireExecution/executor calls. At the second require
zero persistent-shell stdin writes and zero process starts.

- [ ] **Step 6: Implement final revalidation**

Before acquire, rebuild action, recapture root, call
`RevalidateGuestExecutionIdentity`, compare every bound field/input, and rerun
the proof predicate. Convert proof-bound acquire failure to exact
`sandbox_binding_expired`. Keep ShellManager's local check immediately before
stdin write/start.

- [ ] **Step 7: Prove hook rewrites restart policy**

Test routine-to-critical, routine-to-invalid, and routine-to-routine rewrites;
expect prompt, deny, and fresh proof-bound execution respectively.

- [ ] **Step 8: Run GREEN and commit**

```bash
go test ./engine ./tools -run '^TestP512.*(Critical|Contained|Fallback|Rewrite|Drift|Submission)' -count=1
go test -race ./engine ./tools -run '^TestP512.*(Drift|Submission|Rewrite)' -count=20
git add engine/engine.go engine/tool_execution.go \
  engine/query_engine_permission_test.go engine/permission_action_test.go \
  tools/bash_shell.go tools/bash_shell_test.go
git commit -m "feat(permission): dispatch proof-bound Auto Bash"
```

## Task 6: Constrain Plain, TUI, and ACP clients

**Files:**

- Modify: `cmd/yhc/cmd/root.go`
- Modify: `cmd/yhc/cmd/root_test.go`
- Modify: `cmd/yhc/cmd/headless_permission_test.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/dialog.go`
- Modify: `internal/tui/permission_prompt.go`
- Modify: `internal/tui/thread_attention.go`
- Modify: `internal/tui/permission_lifecycle_test.go`
- Modify: `internal/tui/permission_prompt_test.go`
- Modify: `internal/tui/thread_attention_test.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/agent_test.go`
- Modify: `server/acp/sandbox_test.go`

**Interfaces:**

- Clients consume engine-owned constraint and presentation.
- Clients never inspect command text to decide scope.

- [ ] **Step 1: Write failing Plain/headless tests**

For constrained live and first ProjectGraph interrupt paths, omit `[s]`/`[a]`,
reject those inputs, allow `y`, and preserve kind/canonical tool/attempt/
presentation/constraint.

- [ ] **Step 2: Write failing TUI tests**

Assert `threadAttentionRequest` retains the constraint, active
`PermissionDialog` omits session/always choices and accelerators, and forged
persistent responses fail engine settlement. Cover live, event, and cold
resume.

- [ ] **Step 3: Write failing ACP tests**

Advertise AllowOnce plus reject only; omit AllowAlways. Forge always and require
denial. Add real ordinary ACP Bash with complete proof and zero permission
request.

- [ ] **Step 4: Run RED**

```bash
go test ./cmd/yhc/cmd ./internal/tui ./server/acp -run '^TestP512' -count=1
```

- [ ] **Step 5: Implement projections**

Carry the constraint through Plain reconstruction, TUI messages/thread
attention/dialog/prompt, and both ACP request paths. Filter decisions using:

```go
func permissionChoiceAllowed(
    constraint engine.PermissionDecisionConstraint,
    decision engine.PermissionInteractionDecision,
) bool {
    return constraint != engine.PermissionAllowOnceOnly ||
        decision == engine.PermissionAllowOnce
}
```

Deny/cancel remain separate terminal controls.

- [ ] **Step 6: Run GREEN and commit**

```bash
go test ./cmd/yhc/cmd ./internal/tui ./server/acp -run '^TestP512' -count=1
go test -race ./engine ./internal/tui ./server/acp -run '^TestP512.*(Constraint|Permission)' -count=10
git add cmd/yhc/cmd/root.go cmd/yhc/cmd/root_test.go \
  cmd/yhc/cmd/headless_permission_test.go internal/tui server/acp
git commit -m "feat(permission): constrain terminal permission clients"
```

## Task 7: Bind AppServer and Web UI to the constraint

**Files:**

- Modify: `server/appserver/permission_broker.go`
- Modify: `server/appserver/permission_broker_test.go`
- Modify: `server/appserver/session.go`
- Modify: `server/appserver/interaction_routes_test.go`
- Modify: `server/appserver/protocol.go`
- Modify: `internal/webui/assets/view_models.mjs`
- Modify: `internal/webui/assets/app.mjs`
- Modify: `desktop/test/view_models.test.mjs`
- Modify: `desktop/test/interactions.test.mjs`

**Interfaces:**

- Broker digest/clone/settlement includes authoritative constraint.
- Web UI accepts ordinary three-scope or constrained one-scope available shape.

- [ ] **Step 1: Write failing broker tests**

Same outward request with constrained/unconstrained values must produce
different digests and conflict rather than coalesce. Forged session/always must
not call `ResolvePermissionInteraction`.

- [ ] **Step 2: Write failing browser tests**

Accept `grantScopes: ["allow_once"]` only for a valid available constrained
presentation; render once plus deny. Reject reordered, duplicate, persistent,
empty, and unknown scopes.

- [ ] **Step 3: Run RED**

```bash
go test ./server/appserver -run '^TestP512' -count=1
node --test desktop/test/view_models.test.mjs desktop/test/interactions.test.mjs
```

- [ ] **Step 4: Implement broker checks**

Clone/digest the constraint and cross-check response against both constraint
and projection:

```go
if request.DecisionConstraint == engine.PermissionAllowOnceOnly &&
    input.Permission.Decision != engine.PermissionAllowOnce {
    return interactionResolveInvalid
}
```

Do not expose raw command or sandbox proof through the public protocol.

- [ ] **Step 5: Implement Web UI shape**

```js
const ordinaryGrantScopes = ["allow_once", "allow_session", "allow_always"];
const constrainedGrantScopes = ["allow_once"];
```

Validate one exact shape, then keep button rendering data-driven and deny
separate.

- [ ] **Step 6: Run GREEN and commit**

```bash
go test ./server/appserver -run '^TestP512|PermissionBroker|ResolveInteraction' -count=1
go test -race ./server/appserver -run '^TestP512' -count=20
node --test desktop/test/view_models.test.mjs desktop/test/interactions.test.mjs
make desktop-test
git add server/appserver internal/webui/assets/view_models.mjs \
  internal/webui/assets/app.mjs desktop/test/view_models.test.mjs \
  desktop/test/interactions.test.mjs
git commit -m "feat(desktop): enforce constrained permission scopes"
```

## Task 8: Prove Child Agent and real Darwin containment

**Files:**

- Modify: `engine/foreground_child_graph_test.go`
- Modify: `engine/query_engine_permission_test.go`
- Modify: `engine/execution_policy_test.go`
- Modify: `server/acp/sandbox_test.go`
- Modify: `cmd/yhc/cmd/root_test.go`

**Interfaces:**

- Consumes completed runtime and adapters.
- Produces real inside-root execution and outside-root denial evidence.

- [ ] **Step 1: Add foreground/background Child tests**

Use a parent workspace and narrower child workspace. Child ordinary Bash writes
without prompting; parent-root escape is denied. Background child retains the
same child binding/generation.

- [ ] **Step 2: Add real root-replacement test**

Skip only when Darwin Seatbelt is unavailable. Obtain contained admission,
rename/recreate root before acquire, and require exact
`sandbox_binding_expired`, zero counted Bash executor calls, and no output file.

- [ ] **Step 3: Add real Plain and ACP oracles**

Inside-root file writes succeed without permission request; outside-root and
network operations fail. Preserve environment/TMPDIR byte-for-byte assertions.

- [ ] **Step 4: Run and commit**

```bash
go test ./engine ./cmd/yhc/cmd ./server/acp -run '^TestP512' -count=1
go test ./engine -run '^TestP512AutoContainmentCorpus$' -count=100
go test -race ./engine ./tools ./server/appserver -run '^TestP512' -count=20
git add engine/foreground_child_graph_test.go \
  engine/query_engine_permission_test.go engine/execution_policy_test.go \
  server/acp/sandbox_test.go cmd/yhc/cmd/root_test.go
git commit -m "test(permission): prove proof-bound Bash across entrypoints"
```

## Task 9: Close P51.2 and public evidence

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
- Modify: `docs/migration/STATUS.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/plans/p51-2-auto-containment-admission.md`
- Modify: `docs/migration/plans/p42-host-execution-containment.md`
- Modify: `docs/migration/plans/p22-auto-permission-review.md`
- Modify: this implementation plan and its index
- Modify: the approved forward-port design and its index
- Modify: `PUBLICATION_MANIFEST.json`

**Interfaces:**

- Produces current architecture/operator facts, public verification/history,
  empty active queue, G28 retained, and self-consistent public manifest.

- [ ] **Step 1: Map acceptance to evidence**

The verification record contains:

```markdown
| Contract | Source owner | Test or command | Result class |
|---|---|---|---|
| Complete proof only | `completeContainedAutoBashProof` | `TestP512ContainedAutoBashProofMatrix` | deterministic |
| Critical live AllowOnce | `promptForCriticalBash` | `TestP512CriticalBashRequiresFreshAllowOnceAcrossAuthorities` | policy |
| Durable constraint | `projectGraphHITLRequest` | `TestP512ProjectGraphColdRestartRetainsAllowOnceConstraint` | restart |
| Final zero submission | `toolExecutor`, `ShellManager` | `TestP512ContainedAutoBashDispatchRejectsReplacedRootBeforeExecution` and `TestP512GuestSubmissionRejectsExpiredIdentity` | concurrency |
| Client scopes | adapters and broker | `TestP512` package fixtures in Plain, TUI, ACP, and AppServer | entrypoint |
| Real containment | Darwin Guest fixture | `TestP512` Plain and ACP sandbox oracles | platform |
```

Record environment, commands, pass/fail rule, skips, and limitations. Do not
claim universal safety, reviewer accuracy, cross-platform containment, or
credential/resource isolation.

- [ ] **Step 2: Update current facts and retain G28**

Document implemented order/failures in architecture and operator behavior in
the guide. Remove P51.2 from queue and render PLAN. Mark contract/plan/design
historical and add one history record. G28 remains open.

- [ ] **Step 3: Run source gates**

```bash
make fmt
REFERENCE_DIR="${REFERENCE_DIR:?set to the reviewed external reference parent}" \
  ITERATION_BASE=origin/codex/feat/desktop-workbench \
  ITERATION_SLICE_ID=P51.2 make change-plan
REFERENCE_DIR="${REFERENCE_DIR:?set to the reviewed external reference parent}" \
  ITERATION_BASE=origin/codex/feat/desktop-workbench \
  ITERATION_SLICE_ID=P51.2 make verify-focused
make lint
make test
make build
make desktop-check
git diff --check
```

- [ ] **Step 4: Regenerate publication identity**

Commit candidate source/docs, materialize that exact clean commit into a new
`0700` sibling tree, generate the manifest there, copy only the generated
manifest back, amend, and run:

```bash
make publication-check-tree PUBLICATION_ROOT=/absolute/materialized/tree
make publication-scan-expression PUBLICATION_ROOT=/absolute/materialized/tree
make publication-check-policy
make verify-publication
```

- [ ] **Step 5: Run committed-tree evidence**

```bash
REFERENCE_DIR="${REFERENCE_DIR:?set to the reviewed external reference parent}" \
  ITERATION_BASE=origin/codex/feat/desktop-workbench \
  ITERATION_SLICE_ID=P51.2 make verify-merge
REFERENCE_DIR="${REFERENCE_DIR:?set to the reviewed external reference parent}" \
  ITERATION_BASE=origin/codex/feat/desktop-workbench \
  ITERATION_SLICE_ID=P51.2 make change-evidence-ready
```

Expected: `evidence_ready`. Report remote CI, real Darwin, PTY, Desktop package
smoke, and physical UI acceptance separately.

- [ ] **Step 6: Replay onto public master before push**

Fetch public origin, verify the Desktop candidate's final tree is in
`origin/master`, replay only this branch's public commits, regenerate manifest,
and rerun Tasks 8-9. Stop if Desktop is not merged or replay changes a
permission owner without fresh review. Push only the topic branch and open a PR
against protected public master.
