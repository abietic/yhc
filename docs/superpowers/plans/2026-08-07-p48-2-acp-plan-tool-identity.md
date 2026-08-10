# ACP Plan Tool-Call Identity Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical

> **Ownership:** test-first implementation and closeout steps for P48.2/G43;
> root migration queue state remains authoritative.

**Goal:** Correlate ACP Plan tool start, every permission request, and the
single terminal update with the engine-issued tool-use identity.

**Architecture:** The engine `PermissionPromptRequest.ToolUseID` crosses the
ACP adapter unchanged. `makeACPPermissionPrompt` preserves start-before-
permission ordering; `requestACPPlanApproval` and bypass confirmation reuse one
typed `ToolCallId` while retaining distinct Plan request/revision values and
the existing shared deadline. Blank identity fails closed before client I/O.

**Tech Stack:** Go 1.26.5, ACP Go SDK v1, QueryEngine Plan approval,
`acpToolLifecycleLedger`, white-box and real-wire protocol tests, race detector,
SDK verification, migration queue, and Makefile gates.

## Global Constraints

- Execute only when P48.2 is `Ready` and P48.1 has completed; do not infer
  execution authority from the satisfied written-contract gate.
- Close only G43. Do not change Plan target modes, option labels, reviewed-plan
  digest, permission deadline, or engine authorization.
- `PlanApprovalRequest.RequestID` and Plan revision remain Plan identities;
  they must not replace or be derived into the ACP tool identity.
- Preserve exact order: tool start, one or more permission requests, Plan
  settlement, exactly one terminal tool update.
- Blank `ToolUseID` fails closed and makes zero `RequestPermission` calls. Do
  not synthesize a replacement for Plan.
- Preserve non-Plan permission compatibility outside this slice.

---

## Task 1: Pin one identity through the full Plan trace

**Files:**

- Modify: `server/acp/agent_protocol_test.go`

**Interfaces:**

- Exercises: production `QueryEngine` Plan resolver, ACP session update wire,
  `Agent.makeACPPermissionPrompt`, `requestACPPlanApproval`, and lifecycle
  terminal projection.
- Reuses: `planSelectingPermissionClient` and the existing ProjectGraph Plan
  decision fixture.

- [x] **Step 1: Extend the production resolver fixture**

Enhance `TestACPProjectGraphPlanDecisionUsesProductionResolver` so its client
records ordered markers:

```text
start:<tool-id>
permission:<tool-id>
permission:<tool-id>   # only when bypass confirmation is exercised
terminal:<tool-id>
```

Use the model-emitted tool call ID as the expected value. Route QueryEngine
events through `agent.streamEvent` so start and terminal assertions cover the
real ACP wire, not only a helper.

- [x] **Step 2: Replace the synthetic-ID expectation**

Change `TestACPPlanApprovalUsesDistinctStructuredTargets` to retain its title,
content, and option assertions but require every captured `ToolCallId` to equal
`"plan-1"`. Rename it to
`TestACPPlanApprovalReusesToolIdentityAcrossStructuredTargets`.

- [x] **Step 3: Add a blank-identity fail-closed test**

Create `TestACPPlanApprovalRejectsMissingToolIdentity`. Install
`planPermissionRequestFn` that increments a counter, invoke the production
permission prompt with a Plan request and blank `ToolUseID`, and assert a deny
or cancellation-shaped Plan terminal result plus zero client calls.

- [x] **Step 4: Run focused red**

```bash
go test ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApprovalReusesToolIdentityAcrossStructuredTargets|TestACPPlanApprovalRejectsMissingToolIdentity)$' -count=1
```

Expected: FAIL because current Plan approval and bypass confirmation allocate
different synthetic IDs.

## Task 2: Thread the engine tool ID through Plan permission

**Files:**

- Modify: `server/acp/agent.go`
- Modify: `server/acp/agent_protocol_test.go`

**Interfaces:**

- Consumes: `engine.PermissionPromptRequest.ToolUseID`.
- Produces: one `acpsdk.ToolCallId` reused by both permission request shapes.

- [x] **Step 1: Fail closed before reading or prompting**

At the start of the Plan branch, reject whitespace-only `ToolUseID` before
reading Plan bytes or calling `requestPlanPermission`. Keep the existing
start-before-permission call in `makeACPPermissionPrompt`; its blank-ID error
must remain the earliest public boundary.

- [x] **Step 2: Create the ID once and pass it explicitly**

Use this shape:

```go
callID := acpsdk.ToolCallId(request.ToolUseID)

func (a *Agent) acpPlanBypassConfirmation(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	callID acpsdk.ToolCallId,
	request engine.PermissionPromptRequest,
) (confirmed bool, back bool, terminal engine.PermissionInteractionResult)
```

Remove `plan_approval_N` and `plan_bypass_confirm_N`. Reuse `callID` on every
loop iteration and confirmation request. Keep the original `deadlineCtx` so a
Back loop cannot reset the permission budget.

- [x] **Step 3: Repair direct helper fixtures**

Every direct test of `requestACPPlanApproval` or
`acpPlanBypassConfirmation` must supply an explicit tool ID. Do not weaken the
production blank-ID contract to keep old helper fixtures green.

- [x] **Step 4: Run focused green, lifecycle, and race tests**

```bash
go test ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApproval|TestACPPlanBypass)' -count=1
go test -race ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApproval|TestACPPlanBypass)' -count=1
```

- [x] **Step 5: Commit the green behavior**

```bash
git add server/acp/agent.go server/acp/agent_protocol_test.go
git commit -m "fix(acp): preserve Plan tool-call identity"
```

## Task 3: Close G43 and record the wire contract

**Files:**

- Modify: `docs/architecture/platform/acp-adapter.md`
- Modify: `docs/architecture/capabilities/permissions.md`
- Create: `docs/migration/verification/p48-2-acp-plan-tool-identity.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p48-2-acp-plan-tool-identity.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p48-acp-boundary-remediation.md`

- [x] **Step 1: Update only current delivered facts**

Document one engine invocation identity across ACP start, permission, and
terminal updates. Remove G43 and P48.2, retain P48.3 as queued unless root
governance promotes it, and do not claim that ACP identity replaces Plan
revision or authorization.

- [x] **Step 2: Run complete permission/wire closeout**

```bash
go test ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApproval|TestACPPlanBypass)' -count=1
go test -race ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApproval|TestACPPlanBypass)' -count=1
make test-contract
make test-race
./scripts/verify-p23-5-acp-sdk.sh
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit closeout and open one atomic PR**

```bash
git add docs/architecture docs/migration
git commit -m "docs: close P48.2 ACP Plan identity"
```

The PR must state the `preserve` decision, blank-ID fail-closed behavior,
deadline compatibility, rollback, local evidence, and remote-CI state.
