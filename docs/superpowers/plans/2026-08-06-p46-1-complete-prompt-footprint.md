# P46.1 Complete Prompt Footprint Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-06
**Completed:** 2026-08-06

> **Ownership:** test-first implementation steps for the accepted P46.1
> complete-request context-admission slice

**Goal:** Skip failover candidates whose authoritative context window cannot
fit the complete immutable messages, system prompt, and tool definitions before
route construction or dispatch.

**Architecture:** `runCanonicalModelRound` continues to own the only immutable
provider request snapshot. It passes one grouped request-footprint value to the
existing attempt coordinator, which derives the existing provider-neutral
`RoleRequirements` and calls the unchanged role resolver. The resolver,
candidate ordering, lazy route construction, budgets, and entrypoint policies
remain the same.

**Tech Stack:** Go 1.26.5, Eino `schema.Message`/`schema.ToolInfo`, the existing
compact token heuristic, white-box engine tests, migration queue and Makefile
gates.

## Global Constraints

- Execute only P46.1/G36; P46.2/G37 remains queued.
- Adoption is `preserve`: do not add adaptive health, scoring, retries, or a
  second failover owner.
- Candidate admission must remain detached: no credential lookup, route
  construction, provider-usage admission, dispatch, switch, or wait for a
  skipped candidate.
- Count normalized messages after user-context prepend, the exact cloned system
  prompt, and the exact cloned tool definitions including serializable
  `ToolInfo.Extra`.
- Use a provider-neutral input estimate only; do not reserve output tokens or
  claim billing-token accuracy.
- Saturate arithmetic at `math.MaxInt`; never wrap to a smaller or negative
  context requirement.
- Preserve unrelated `PROJECT_GUIDE.md` and `artifacts/` worktree content.
- Final code verification must use `make fmt`, `make lint`, `make test`, and
  `make build` plus documentation, manifest, and diff gates.

---

## File Structure

| File | Responsibility in this slice |
|---|---|
| `engine/model_failover.go` | Group the immutable failover request inputs and derive one complete, overflow-safe `PromptTokens` requirement. |
| `engine/model_round.go` | Pass the already-cloned messages, system prompt, and tools to the attempt coordinator. |
| `engine/model_failover_test.go` | Keep coordinator-focused fixtures compiling against the grouped request input without weakening their assertions. |
| `engine/query_fallback_test.go` | Prove the production query path skips system-heavy and tool-heavy smaller-context candidates without route construction or dispatch, then reaches a larger alternate. |
| `docs/architecture/platform/model-providers.md` | Describe complete-request context admission only after tests prove it. |
| `docs/migration/verification/p46-1-complete-prompt-footprint.md` | Own reproducible P46.1 evidence and limitations. |
| `docs/migration/history/runtime/p46-1-complete-prompt-footprint.md` | Record the completed slice after all gates pass. |
| `docs/migration/REMAINING.md`, `docs/migration/queue.yaml`, generated `docs/migration/PLAN.md`, and their indexes | Close G36, remove P46.1, and promote P46.2 as the sole `Ready` slice. |

### Task 1: Drive complete footprint admission through the production query seam

**Files:**

- Modify: `engine/query_fallback_test.go`
- Modify: `engine/model_failover.go`
- Modify: `engine/model_round.go`
- Modify: `engine/model_failover_test.go`

**Interfaces:**

- Consumes: `runCanonicalModelRound`, `runtimeModelFailover.ResolveFailoverChain`,
  `provider.RoleResolutionInput`, `p294FailoverResolver`, and `collectEvents`.
- Produces: unexported `modelFailoverRequest` and
  `modelFailoverRequirements(modelFailoverRequest) (provider.RoleRequirements, error)`;
  no public API or durable schema changes.

- [x] **Step 1: Add a resolver hook used only by production-seam tests**

Extend the existing test resolver without changing its default behavior:

```go
type p294FailoverResolver struct {
    mu           sync.Mutex
    chain        provider.FailoverChainSnapshot
    prepared     []string
    prepareErr   map[string]error
    resolveChain func(provider.RoleResolutionInput) provider.FailoverChainSnapshot
}

func (r *p294FailoverResolver) ResolveFailoverChain(
    input provider.RoleResolutionInput,
) (provider.FailoverChainSnapshot, error) {
    if r.resolveChain != nil {
        return r.resolveChain(input), nil
    }
    return r.chain, nil
}
```

- [x] **Step 2: Add the failing table-driven production-path test**

Add `TestP461CompletePromptFootprintSkipsSmallerContextCandidates` with two
cases. One supplies a long system prompt; the other supplies a tool whose
otherwise-small definition contains a long serializable `Extra` value:

```go
tests := []struct {
    name      string
    configure func(*QueryParams)
}{
    {
        name: "system prompt",
        configure: func(params *QueryParams) {
            params.SystemPrompt = &schema.Message{
                Role: schema.System, Content: strings.Repeat("system ", 256),
            }
        },
    },
    {
        name: "tool definition including extra",
        configure: func(params *QueryParams) {
            params.ToolUseContext.Options.Tools = []*schema.ToolInfo{{
                Name: "probe", Desc: "probe",
                Extra: map[string]any{
                    "context_probe": strings.Repeat("x", 2048),
                },
            }}
        },
    },
}
```

For each case, configure ordered `small` and `large` alternates with literal
context limits of 64 and 4096 tokens. The resolver hook applies only the public
threshold contract:

```go
resolver.resolveChain = func(input provider.RoleResolutionInput) provider.FailoverChainSnapshot {
    observedTokens = input.Requirements.PromptTokens
    admitted := chain
    for index := range admitted.Alternates {
        limit := admitted.Alternates[index].Call.ContextWindowTokens
        if limit != nil && input.Requirements.PromptTokens > *limit {
            admitted.Alternates[index].AdmissionCode = "context_window"
        }
    }
    return admitted
}
```

Assert these observable outcomes with literals rather than calling the
estimator from the test:

```go
if observedTokens <= 64 || observedTokens > 4096 {
    t.Fatalf("complete prompt tokens = %d, want 65..4096", observedTokens)
}
if !equalStrings(models, []string{"primary", "primary", "primary", "large"}) {
    t.Fatalf("models = %#v", models)
}
if !equalStrings(resolver.preparedModels(), []string{"primary", "large"}) {
    t.Fatalf("prepared models = %#v", resolver.preparedModels())
}
```

Also assert one `candidate_skipped/context_window` event for `small`, no
`started` event for `small`, and a `started` event for `large` with attempt and
switch index `1`. This proves the skip consumes neither an attempt nor a switch;
the absence of `small` from prepared models and dispatch models proves no route
construction or provider call.

- [x] **Step 3: Run the focused test and verify red**

Run:

```bash
go test ./engine/ -run '^TestP461CompletePromptFootprintSkipsSmallerContextCandidates$' -count=1
```

Expected: FAIL because the resolver observes only the short user message,
admits `small`, and the recorded dispatch/preparation order differs from the
expected `primary -> large` path.

- [x] **Step 4: Group the exact immutable request inputs**

In `engine/model_failover.go`, replace the message-only coordinator input with:

```go
type modelFailoverRequest struct {
    messages     []*schema.Message
    systemPrompt *schema.Message
    toolInfos    []*schema.ToolInfo
}
```

Change `newModelAttemptCoordinator` to accept `modelFailoverRequest` and make
`modelFailoverRequirements` return `(provider.RoleRequirements, error)`.
Preserve modality/PDF/reasoning detection over `request.messages`.

Update `newP294Coordinator` in `engine/model_failover_test.go` mechanically so
its existing message fixture is passed as
`modelFailoverRequest{messages: messages}`. Do not change its current
coordinator assertions.

- [x] **Step 5: Implement the minimal complete estimator**

Use the existing message estimator for messages and the system prompt. When the
cloned tool list is non-empty, encode the complete list with `json.Marshal` so
array framing, every tool, and serializable `Extra` all contribute; add
`ceil(len(encoded)/4)` without `len+3` overflow, and saturate every addition:

```go
func addPromptTokenEstimate(total, additional int) int {
    if additional <= 0 {
        return total
    }
    if total > math.MaxInt-additional {
        return math.MaxInt
    }
    return total + additional
}

func bytesTokenEstimate(encoded []byte) int {
    tokens := len(encoded) / 4
    if len(encoded)%4 != 0 {
        tokens++
    }
    return tokens
}
```

Return `fmt.Errorf("encode model tools for context admission: %w", err)` on
JSON failure. Do not retain or emit the encoded schema.

- [x] **Step 6: Pass the already-frozen request from the canonical owner**

In `engine/model_round.go`, construct the coordinator input only after all
three clones succeed:

```go
coordinator, coordinatorErr := newModelAttemptCoordinator(
    input.params,
    modelFailoverRequest{
        messages:     immutablePreparedMessages,
        systemPrompt: immutableSystemPrompt,
        toolInfos:    immutableToolInfos,
    },
    callOpts.UsageLogicalRoundID,
    input.deps.UUID,
)
```

If requirement construction fails, retain the existing pre-dispatch
`TerminalPromptInputError` path and ensure the resolver is not called.

- [x] **Step 7: Run focused green and regression tests**

Run:

```bash
go test ./engine/ -run '^(TestP461CompletePromptFootprintSkipsSmallerContextCandidates|TestP294)' -count=1
go test ./engine/provider/ -run '^(TestResolveFailoverChainIsDetachedOrderedAndAdmissionAware|TestP294FailoverCandidateAdmissionCodesAreStableAndNoCall)$' -count=1
go test -race ./engine/ -run '^(TestP461CompletePromptFootprintSkipsSmallerContextCandidates|TestP294)' -count=1
```

Expected: PASS. Existing P29.4 retry, switch, immutable replay, capability,
reasoning, and budget behavior remains unchanged.

### Task 2: Close P46.1 with current owners and repository gates

**Files:**

- Modify: `docs/architecture/platform/model-providers.md`
- Create: `docs/migration/verification/p46-1-complete-prompt-footprint.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p46-1-complete-prompt-footprint.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p46-model-failover-repair.md`
- Modify: `docs/migration/plans/README.md`

**Interfaces:**

- Consumes: current passing source/tests from Task 1 and the documentation
  ownership policy.
- Produces: closed G36/P46.1 evidence and P46.2 as the only `Ready` slice.

- [x] **Step 1: Synchronize only changed fact owners**

Describe complete-request context admission in the current provider
architecture. Add one verification record with the focused commands and
limitations, one past-tense history record, remove G36 from `REMAINING.md`,
  remove P46.1 and P46.2's completed dependency from `queue.yaml`, and set
  P46.2 to `ready` with its satisfied contract-approval gate. Run the queue
  renderer:

```bash
go run ./scripts/migration_queue render
```

Do not claim live-provider, physical-terminal, or remote-CI acceptance.

- [x] **Step 2: Invoke iteration closeout and run all final gates**

Run after the last source or documentation edit:

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

Expected: every local gate passes. A remote CI usage-limit failure may be
recorded under the user's explicit exception but must not be called green.

- [x] **Step 3: Inspect and commit the atomic P46.1 slice**

Stage only the source, tests, plan trackers, architecture, verification, and
history files listed above. Confirm `PROJECT_GUIDE.md` and `artifacts/` remain
untracked and unstaged, then commit:

```bash
git commit -m "fix: admit failover against complete prompt footprint"
```

- [x] **Step 4: Push, review, and merge through the protected branch**

Push the short-lived P46.1 branch, create one ready PR describing the user
problem, `preserve` decision, compatibility, rollback, and local gate evidence,
then squash-merge it. Do not combine P46.2 implementation into this PR.
