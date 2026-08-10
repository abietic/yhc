# P33.1 MCP Live Tool Generation Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P33.1 only

> **Ownership:** reproducible acceptance evidence for exact MCP connection
> generations, complete owned registry publication, permission and execution
> pinning, lifecycle convergence, entrypoint preservation, and G5 closure.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Initial publication | A normal project-configured server publishes one complete owned registry contribution only while its exact client is live. A close before publication leaves no manager client or registry row. |
| List change | Add, replace, and valid empty results replace the same owner's complete contribution in one registry generation. List, identity, collision, or generation failure removes stale rows without partial publication. |
| Reconnect | Manual reconnect creates a new client, manager generation, client generation, and owner, then atomically replaces the retired owner. A close before publication fails closed. |
| Close and isolation | Explicit disconnect and unexpected close remove only the matching owner. Late callbacks from a retired generation cannot delete its replacement, and another MCP server plus built-ins survive. |
| Permission and dispatch | Permission settled before replacement fails its generation check. An acquired lease captures the old implementation, blocks replacement until dispatch or cancellation, and never routes to the replacement client. |
| Hook reentry | Registry batch mutation captures ordered synchronous hooks, releases the manager lifecycle lock, and then dispatches each hook exactly once. A hook can reenter `DisconnectAll` without deadlock. |
| Entrypoints | Normal QueryEngine managers use owned publication, ACP retains combined atomic setup and cleanup, inherited subagents keep the same manager authority, and standalone MCP remains excluded. |
| Recovery boundary | `MCPToolManager` does not enable SDK automatic reconnect. Explicit manager reconnect owns new-generation publication; persisted configuration mutation remains restart-based. |
| Review | Independent concurrency review found the hook-reentry deadlock. The deferred-delivery fix and added regression were re-reviewed against lock ordering and exact-generation invariants. |

## Source Gates

```text
test -z "$(rg -n '\\.RegisterToolsInRegistry\\(' --glob='*.go' || true)"
test -z "$(rg -n 'registry\\.(ReplaceOwnedTools|RemoveOwnedTools)' tools/mcp_tool.go tools/mcp_session_setup.go || true)"
test -z "$(rg -n '\\.EnableReconnect\\(' --glob='*.go' --glob='!*_test.go' || true)"
```

These gates keep the production runtime off the legacy batch adapter, require
manager lifecycle code to use deferred registry hook delivery, and prevent
library-level automatic reconnect from bypassing manager-owned generations.

## Focused Commands

```text
go test ./engine/mcp ./tools -run 'TestMCP(Client|ToolCallTarget)|TestInitMCPManager|TestReconnectServer|TestOwnedMCPRefreshGenerationRace|TestMCPRegistryHooksCanReenterManagerLifecycle' -count=1
go test -race ./engine/mcp ./tools -run 'TestMCP(Client|ToolCallTarget)|TestInitMCPManager|TestReconnectServer|TestOwnedMCPRefreshGenerationRace|TestMCPRegistryHooksCanReenterManagerLifecycle' -count=10 -timeout=180s
go test -race ./tools -run 'TestReconnect(ServerRotates|WaitsForLease)|TestConnectServerRejectsCloseBeforePublication|TestReconnectServerRejectsCloseBeforePublication|TestMCPRegistryHooksCanReenterManagerLifecycle' -count=20 -timeout=180s
go test ./server/acp -run '^TestP235ACPStdio(NewInvokeCloseAndPrivacy|MultiServerFailureRollsBackEveryChild|DiscoveredCollisionAbortsBeforeSessionVisibility|SourceCollisionFailsBeforeLaunch|SetupCancellationAndTimeoutCleanChildren|LoadDeliveryFailureAbortsPreparedGeneration|ResumeDeliveryFailureAbortsPreparedGeneration|LoadResumeAndExactActiveReconnect|DynamicCollisionRemovesWholeServerGeneration)$' -count=3 -timeout=180s
```

## Repository Closeout

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands passed. GitHub Actions billing or usage failures may be waived
only after the exact job annotation proves that no runner started; they are
never described as green CI.

During closeout, one broad entrypoint run reproduced the pre-existing
concurrent `allow_always` persistence flake, and the first full test run hit
the external `pdfinfo` ten-second timeout. The permission test then passed 20
isolated repetitions, the PDF test passed 10, and two later complete
`make test` runs passed. Neither fluctuation touched the P33.1 packages or
changed its acceptance boundary.
