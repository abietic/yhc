# YHC Protocol Compatibility Implementation Plan

> **Historical execution note:** This plan records the completed ACP and MCP
> compatibility migration. Future protocol changes follow the current
> repository workflow; no live worker instruction remains.

**Goal:** Advertise YHC over ACP and MCP, negotiate exactly one canonical or
legacy ACP Goal namespace per connection, and keep all Goal/MCP runtime
semantics unchanged behind the identity boundary.

**Architecture:** ACP replaces its global Goal constants and integer capability
state with one immutable namespace descriptor selected during the first
successful Initialize. The descriptor owns capability key, three request
methods, and update notification; both namespaces dispatch to the existing
single handler. MCP changes only the two local `Implementation.Name`
declarations and verifies handshake metadata while leaving config, transport,
tool names, authorization, and lifecycle untouched.

**Tech Stack:** Go 1.26.5, ACP Go SDK extension metadata and request errors, MCP
Go SDK initialize handshakes over production stdio paths, concurrency tests,
and existing Goal durability/revision/delivery-failure oracles.

**Status:** historical
**Created:** 2026-08-09
**Completed:** 2026-08-11
**Plan state:** Completed; protocol acceptance and public-release clearance passed

> **Ownership:** ACP Goal and MCP declaration rows of the
> [YHC protocol design](../specs/2026-08-09-yhc-public-release-design.md#acp-selects-one-goal-namespace-per-connection).

## Global Constraints

- ACP agent name is `yhc` and title is
  `YHC — Yet Hooked on Coding`.
- Canonical Goal surface is `yhc.goal` with
  `_yhc/goal/{get,control,continue,updated}`. Legacy surface remains
  `eino-agent.goal` with `_eino/goal/*`.
- Select one namespace for the lifetime of one ACP connection. Production
  stdio continues to create one Agent and one connection; do not make one
  Agent support multiple concurrent connections.
- Canonical-only valid selects canonical. Legacy-only valid selects legacy.
  Both valid with the same supported version select canonical. Both present
  with different versions or either malformed fail Initialize, install no
  namespace, and permit a clean retry.
- A single present malformed offer preserves current behavior: Initialize
  succeeds without Goal capability and installs no namespace. This is the
  behavior-preserving interpretation of the approved matrix; it is not a
  fallback to the other absent key.
- Neither offer succeeds without Goal. Unselected/absent namespace methods
  return method-not-found and never send a notification.
- Both namespaces call the same existing Goal handler and preserve strict
  schemas, authorization, revision fencing, durable transitions, cursor
  consumption, response IDs, error codes, and delivery-failure behavior.
- Initialization failure must leave `initialized=false` and namespace nil.
  Repeated successful Initialize replays the original immutable capability and
  cannot switch namespaces.
- MCP has no inbound server-name method namespace. Do not add an old-name alias,
  config migration, transport branch, tool-name alias, or second runtime.
- Preserve `.mcp.json`, project/user `.claude/mcp_servers.json`, OAuth token
  path, stdio/HTTP/SSE behavior, exact tool allowlist, permission policy, and
  fresh-runtime-per-serve lifecycle.

---

## Locked ACP Interface

```go
type acpGoalNamespace struct {
	capabilityKey string
	getMethod     string
	controlMethod string
	continueMethod string
	updatedMethod string
	version       int
}

var canonicalACPGoalNamespace = acpGoalNamespace{
	capabilityKey: "yhc.goal",
	getMethod: "_yhc/goal/get",
	controlMethod: "_yhc/goal/control",
	continueMethod: "_yhc/goal/continue",
	updatedMethod: "_yhc/goal/updated",
	version: 1,
}

var legacyACPGoalNamespace = acpGoalNamespace{
	capabilityKey: "eino-agent.goal",
	getMethod: "_eino/goal/get",
	controlMethod: "_eino/goal/control",
	continueMethod: "_eino/goal/continue",
	updatedMethod: "_eino/goal/updated",
	version: 1,
}

type acpGoalNegotiation struct {
	namespace *acpGoalNamespace
	offered   bool
}

func negotiateACPGoalNamespace(meta map[string]any) (acpGoalNegotiation, error)
```

The descriptor values are immutable package data. `Agent` stores only the
selected descriptor pointer after successful initialization.

## Task 1: Freeze Existing Goal And MCP Semantics

**Files:**

- Modify: `server/acp/goal_extension_test.go`
- Modify: `server/acp/agent_protocol_test.go`
- Modify: `server/mcp/server_test.go`
- Create: `engine/mcp/sdk_client_identity_test.go`
- Modify: `engine/mcp/client_test.go`

- [x] **Step 1: Add behavior-preservation characterization**

Before renaming, extend existing tests to record:

- Goal get/control/continue response schemas and error codes;
- one durable transition per control;
- exact revision conflict behavior;
- one cursor consumption per continue;
- no replay after notification/delivery failure;
- MCP exact explicit allowlist and one fresh runtime per serve;
- MCP config merge/precedence and all transports; and
- client ListTools/CallTool success independent of peer implementation name.

Name the aggregate test
`TestYHCProtocolMigrationCharacterizesIdentityIndependentBehavior`.

- [x] **Step 2: Prove the characterization can fail**

Run once with a test-local perturbed method/notification or MCP tool name and
observe failure, then restore the fixture.

```bash
go test ./server/acp ./server/mcp ./engine/mcp -run '^TestYHCProtocolMigrationCharacterizesIdentityIndependentBehavior$' -count=1
```

Expected after restoring the fixture: PASS on old identity.

- [x] **Step 3: Commit characterization only**

```bash
git add server/acp/goal_extension_test.go server/acp/agent_protocol_test.go server/mcp/server_test.go engine/mcp/sdk_client_identity_test.go engine/mcp/client_test.go
git commit -m "test(protocol): freeze pre-YHC behavior"
```

## Task 2: Negotiate One ACP Goal Namespace

**Files:**

- Modify: `server/acp/agent.go`
- Modify: `server/acp/goal_extension.go`
- Modify: `server/acp/streaming.go`
- Modify: `server/acp/goal_extension_test.go`
- Modify: `server/acp/agent_protocol_test.go`
- Modify: `server/acp/extensions_golden_test.go`
- Modify: `server/acp/testdata/hook_permission_extensions.golden.json` only if
  it contains the advertised agent/capability identity

- [x] **Step 1: Add the red negotiation matrix**

Create `TestYHCACPGoalNamespaceNegotiationMatrix` with:

| Offer | Result |
|---|---|
| valid canonical only | returns only `yhc.goal` |
| valid legacy only | returns only `eino-agent.goal` |
| valid matching dual | returns canonical only |
| valid different dual | negotiation error, no state |
| malformed canonical plus valid legacy | negotiation error, no state |
| valid canonical plus malformed legacy | negotiation error, no state |
| malformed single canonical | success, no Goal |
| malformed single legacy | success, no Goal |
| neither | success, no Goal |

Assert failed dual initialization leaves `initialized=false` and allows a later
valid Initialize.

- [x] **Step 2: Add red pairing/isolation tests**

Create `TestYHCACPGoalNamespaceRequestAndNotificationPairing`,
`TestYHCACPGoalDualOfferFailureExposesNoGoalSurface`,
`TestYHCACPGoalNamespaceConnectionIsolation`, and
`TestYHCACPGoalSelectionIsImmutableAcrossRepeatedInitialize`.

For each selected namespace, invoke all three selected methods, reject all
three unselected methods with `-32601`, and capture exactly one matching update
notification without payload changes. Run two independent Agent/connection
pairs concurrently and prove no cross-acceptance or notification.

- [x] **Step 3: Run red**

```bash
go test ./server/acp -run 'Test(YHCACP|P245c)' -count=1
```

Expected: new tests fail on the single legacy namespace.

- [x] **Step 4: Implement descriptor selection**

Strictly parse each present offer with the existing supported-version and
`notifications:true` rules. Build Initialize metadata from the selected
descriptor. Replace `goalCapabilityVersion int` with
`goalNamespace *acpGoalNamespace`; assign it only after all Initialize
validation succeeds.

`HandleExtensionMethod` compares only the selected descriptor's three request
methods and calls the existing handler. `notifyACPGoalUpdated` reads only the
selected `updatedMethod`.

- [x] **Step 5: Run green, race, and commit**

```bash
go test ./server/acp -run 'Test(YHCACP|P245c|Goal)' -count=1
go test -race ./server/acp -run 'TestYHCACPGoalNamespaceConnectionIsolation' -count=1
git add server/acp
git commit -m "feat(acp): negotiate YHC Goal namespace"
```

## Task 3: Rename MCP Declarations Only

**Files:**

- Modify: `server/mcp/server.go`
- Modify: `server/mcp/server_test.go`
- Modify: `engine/mcp/sdk_client.go`
- Create: `engine/mcp/sdk_client_identity_test.go`
- Verify unchanged: `cmd/yhc/cmd/serve_mcp.go`
- Verify unchanged: `cmd/yhc/cmd/serve_mcp_test.go`

- [x] **Step 1: Add handshake red tests**

Create `TestMCPServerInitializeDeclaresYHC` and
`TestMCPClientInitializeDeclaresYHC`. Use test subprocesses and real stdio SDK
handshakes so the assertions execute production `Serve` and
`MCPClient.Connect`, capture Initialize metadata, and assert exact name `yhc`
without inspecting logs or adding a production construction seam.

- [x] **Step 2: Run red**

```bash
go test ./server/mcp ./engine/mcp -run 'TestMCP(Server|Client)InitializeDeclaresYHC' -count=1
```

- [x] **Step 3: Change the two declarations**

Set `sdkmcp.Implementation.Name` to `identity.CommandName` in
`mcp.Serve` and `MCPClient.Connect`. Keep versions and all server/client
options unchanged. Existing YHC serve diagnostics remain unchanged, including
the rule that a failed permission mode cannot claim startup.

- [x] **Step 4: Run handshake and lifecycle/config regression**

```bash
go test ./server/mcp -run 'Test(MCPServerInitializeDeclaresYHC|StandaloneMCP)' -count=1
go test ./engine/mcp -run 'Test(MCPClientInitializeDeclaresYHC|MCPClient|LoadMCPConfig|FindMCPJSONFile)' -count=1
go test ./cmd/yhc/cmd -run 'TestRunServeMCP' -count=1
```

Expected: declaration tests pass; allowlist, fresh runtime, config merge,
transport, ListTools/CallTool, and invalid-startup tests remain green.

- [x] **Step 5: Prove no inbound alias was introduced and commit**

```bash
git diff -- server/mcp engine/mcp cmd/yhc/cmd/serve_mcp.go
git add server/mcp engine/mcp/sdk_client.go engine/mcp/sdk_client_identity_test.go cmd/yhc/cmd/serve_mcp.go cmd/yhc/cmd/serve_mcp_test.go
git commit -m "feat(mcp): declare YHC implementation identity"
```

Review the diff: no change is permitted in `engine/mcp/config.go`, OAuth,
transport selection, tool names, permission policy, or runtime construction.

## Task 4: Run Protocol Acceptance

- [x] Run complete focused suites:

```bash
go test ./server/acp -run 'Test(YHCACP|P245c|Goal|Initialize|Extension)' -count=1
go test ./server/mcp -run 'Test(MCPServerInitializeDeclaresYHC|StandaloneMCP)' -count=1
go test ./engine/mcp -run 'Test(MCPClientInitializeDeclaresYHC|MCPClient|LoadMCPConfig|FindMCPJSONFile|OAuth)' -count=1
go test ./cmd/yhc/cmd -run 'TestRunServe(ACP|MCP)' -count=1
```

- [x] Run SDK and repository gates that establish protocol correctness:

```bash
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make fmt
make lint
make test
make build
make docs-check
git diff --check
```

- [x] Clear the downstream public-release policy gate:

```bash
make publication-check-policy
```

The protocol diff did not introduce the original failure. Publication
Readiness later cleared the tracked `.agents/skill-runtime/skill_log.py`
classification, and the gate passed before the public root was created.

- [x] Inspect wire evidence from canonical-only, legacy-only, matching dual,
  conflicting dual, malformed single, and absent offers. Record capability key,
  accepted method family, notification method, and error code only; do not
  record prompts or session content.

The plan is incomplete if either namespace has its own Goal engine/transaction
path, if an unselected method executes, or if MCP identity changes any config or
lifecycle behavior.

## Closeout Evidence

The implementation keeps one Goal engine and transaction path. Namespace
selection changes only the capability and method descriptor used at the ACP
boundary; MCP changes only the two Initialize declarations.

| Client offer | Advertised capability | Accepted request family | Update notification | Initialize outcome |
|---|---|---|---|---|
| canonical only | `yhc.goal` | `_yhc/goal/{get,control,continue}` | `_yhc/goal/updated` | success |
| legacy only | `eino-agent.goal` | `_eino/goal/{get,control,continue}` | `_eino/goal/updated` | success |
| matching dual | `yhc.goal` | `_yhc/goal/{get,control,continue}` | `_yhc/goal/updated` | success |
| conflicting dual | none | none | none | `-32602`; later valid Initialize succeeds |
| malformed single | none | none | none | success without Goal |
| absent | none | none | none | success without Goal |

Implementation commits:

- `2609457c` — identity-independent characterization and mutation proof;
- `cf53c80a` — immutable ACP namespace negotiation and selected-only routing;
- `d0787bd2` — declaration-only MCP YHC identity;
- `4723a33b` — official ACP SDK harness reads the canonical `.yhc` transcript
  root exposed by acceptance.

Acceptance evidence:

- ACP focused and connection-isolation race suites passed;
- MCP production stdio handshake tests passed repeatedly and under the race
  detector;
- official TypeScript ACP SDK `1.3.0` verification and `make test-contract`
  passed;
- `make fmt`, `make lint`, `make test` (8104 tests, 3 skipped), `make build`,
  `make docs-check`, and `git diff --check` passed;
- `make publication-check-policy` passed after the downstream publication
  classification was resolved.
