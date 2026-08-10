# P33 MCP Live Tool Generation

**Status:** historical
**Accepted:** 2026-07-31
**Completed:** 2026-07-31
**Adoption:** `adapt`

> **Ownership:** accepted atomic contract for making every QueryEngine-owned
> MCP connection generation agree with the model-visible and executable tool
> registry. Root [`PLAN.md`](../PLAN.md) alone owns live execution order.

## Outcome

P33.1 gave project-configured MCP tools one capability owner:
`tools.Registry`. Initial connect, `tools/list_changed`, reconnect, disconnect,
and unexpected close publish or remove one complete server generation through
that owner.

A user never sees or dispatches a stale tool merely because manager inventory
and registry rows diverged. A tool action that already crossed the registry
dispatch linearization point stays bound to the exact client generation it was
authorized for; it may fail if that connection closes, but it cannot jump to a
replacement connection.

The reproduced behavior and comparative decision are in
[`mcp-live-tool-generation-audit.md`](../reference/runtime/mcp-live-tool-generation-audit.md).
Current implementation behavior remains owned by
[`mcp.md`](../../architecture/capabilities/mcp.md) until closeout.

## Scope and non-goals

P33.1 changed the project-configured MCP lifecycle in `tools/`, the exact
client callback boundary in `engine/mcp/` when required, QueryEngine
construction proof in `engine/`, and the current MCP architecture and user
recovery guidance.

Supported consumers are:

| Entrypoint | P33.1 scope |
|---|---|
| TUI | Project-configured MCP manager and registry |
| Plain | Project-configured MCP manager and registry |
| Ordinary headless QueryEngine | Project-configured MCP manager and registry |
| ACP | Normal project manager without client descriptors; the existing combined owned session manager when descriptors are supplied |
| Subagents | Inherit the parent engine manager and registry; never publish a second generation |
| Standalone MCP server | Excluded; it is a tool server, not a model-runtime consumer |

P33.1 does not add transports, fetch resources, project MCP hot-config reload,
plugin-declared MCP activation, installation/update UX, OAuth behavior,
durable MCP state, or a new tool namespace. It does not change provider
selection, tool schemas, permission policy, prompt/resource APIs, or tolerant
startup behavior for an individual unavailable server.

## One owner and two projections

`MCPToolManager` owns connection lifecycle and a bounded operational snapshot.
`Registry` owns whether a tool is model-visible, permission-resolvable, and
executable.

Each live server record has:

- a manager-local opaque connection-generation token;
- one exact `MCPClient`;
- one owner ID unique to that connection generation;
- a complete validated tool inventory;
- the registry generation returned by successful publication;
- a bounded health category.

Manager inventory is a projection of a successful registry publication, not an
independent execution source. Resource and prompt calls may still use manager
lookup because P33 governs registered tool dispatch only.

## Frozen ordering

### Initial connect

1. Load project configuration and preserve the current nonfatal per-server
   failure policy.
2. Connect and list one server without publishing partial state.
3. Validate all normalized registered names and schemas, construct exact-client
   implementations, and allocate a connection-generation owner.
4. Atomically publish that complete contribution through
   `Registry.ReplaceOwnedTools`.
5. Only after publication succeeds, expose the matching healthy manager
   inventory and install callbacks bound to the exact connection generation.
6. On failure, remove only candidate/retired owner rows, close the candidate,
   and record a bounded failure category; continue other configured servers.

The legacy row-by-row `RegisterToolsInRegistry` path no longer publishes
dynamic runtime tools.

### `tools/list_changed`

1. A callback identifies the exact connection generation that emitted it.
2. List and validate a complete candidate outside manager state locks.
3. Recheck that the generation is still current.
4. Replace that owner's complete registry contribution in one compare-and-
   replace commit.
5. After success, replace the manager inventory and healthy revision.

If listing, validation, collision checking, or generation comparison fails,
remove the old owner, clear only that server's inventory, record
`tool_refresh_failed`, and close the exact client. Do not retry blindly against
a newer unrelated registry generation and do not retain the stale rows.

### Reconnect

Manual reconnect prepares a new connection generation before replacing the old
one. Publication removes the retired owner and adds the new owner's complete
contribution atomically. The successful commit precedes the manager swap and
new callback installation.

A late refresh or close from the retired generation may address only its own
owner. It cannot remove the current rows, mark the new generation failed, or
route an old execution lease to the new client.

### Disconnect and unexpected close

Close removes the exact connection owner's registry rows without requiring a
global generation comparison. The manager then clears that generation's tool
inventory and records `connection_closed` or the explicit disconnect state.

New resolution and permission fail after removal. An execution lease already
acquired before removal reaches the dispatch point first because the registry
writer waits for lease consumption. Its captured implementation calls only the
old exact client; transport closure may return an error.

## Frozen concurrency and identity invariants

1. One manager lifecycle boundary serializes publication-affecting connect,
   refresh, reconnect, disconnect, and close transitions.
2. Network connect/list/call work never runs while holding the manager state
   lock.
3. Registry mutation never relies on server name or current manager lookup to
   identify the rows it removes.
4. Registered tool implementations capture the exact client and connection
   generation. `ExecuteCtx` does not re-resolve that target through a mutable
   manager.
5. The registry lease releases at the existing dispatch linearization point;
   P33 does not hold a registry lock across a network call.
6. A stale callback or publication candidate compares its exact token before
   changing manager state. Its owner remains safe to remove even after a newer
   generation exists.
7. One complete server contribution increments the registry generation once.
   Empty tool lists are valid complete replacements and remove the old rows.
8. Normalized registered-name collision, duplicate identity, invalid schema,
   and reserved-name failure are whole-candidate failures.
9. Other servers, built-ins, aliases, disabled state, hooks, metadata, and
   registration order outside the replaced owner remain unchanged.

Implementation may use one manager-wide lifecycle mutex or exact per-server
serialization, but the proof must show the invariants above and contain no
manager/registry lock cycle.

## Permission and model-turn contract

- `QueryEngine.modelVisibleTools` continues to read `Registry.List`.
- A successful dynamic publication affects the next model tool assembly. It
  never mutates an already submitted model request.
- A tool call returned by an in-flight model request resolves against the
  current registry. If the name disappeared, it fails unavailable.
- A permission result binds requested name, canonical name, action digest, and
  registry generation. Replacement after permission but before
  `AcquireExecution` fails closed and re-enters the normal policy cycle only
  through a new action.
- Replacement after lease acquisition waits until `Execute` or `Cancel`.
  `Execute` dispatches the captured old target once; `Cancel` dispatches
  nothing.
- Reusing the same normalized name in a new generation never inherits an old
  exact permission or execution target.

## Failure, cancellation, persistence, and recovery

MCP connect, list, and call retain their existing contexts and SDK timeouts.
Lifecycle cleanup is idempotent and exact-owner scoped. Closing one server does
not cancel unrelated MCP calls or QueryEngine work.

P33 creates no durable schema, transcript event, Session sidecar, background
watcher, or recovery queue. Process restart rebuilds project MCP from current
configuration. Within a process, recovery is an explicit reconnect or a later
connection event that publishes a new complete generation.

Diagnostics expose server name and bounded categories, never tool inputs,
results, headers, environment values, credentials, or connection material.

## P33.1 atomic slice

**State:** `Complete`

P33.1 is one implementation PR because splitting publication from
exact-client dispatch would temporarily make dynamic rows fresh while allowing
old leases to cross connection generations.

The slice must:

1. promote normal `InitMCPManager` to owned atomic publication;
2. make initial connect, list change, reconnect, disconnect, and close use one
   connection-generation lifecycle;
3. pin registered tool execution to the exact client generation;
4. retire dynamic use of `RegisterToolsInRegistry`;
5. preserve ACP session setup and unify duplicated lifecycle logic where that
   does not widen the public API;
6. update current MCP architecture, user recovery guidance, `STATUS.md`,
   `REMAINING.md`, root `PLAN.md`, and one history/verification record.

## Deterministic proof

### Focused behavior

- initial project MCP publication owns every registry row;
- a list-change replacement adds, removes, changes, and empties one server's
  complete tool set in one registry generation;
- relist error, invalid normalized identity, duplicate, reserved collision,
  and unrelated registry generation race remove stale rows without partial
  publication;
- explicit disconnect and unexpected close remove only the exact connection
  owner;
- reconnect rotates owner/client, replaces rows once, and rejects late
  callbacks from the retired generation;
- another MCP server and built-ins survive every success and failure path;
- manager inventory and registry rows agree after each settled transition.

### Permission and dispatch races

- permission settled at generation N cannot invoke after generation N+1;
- a lease acquired at N dispatches the captured N client exactly once even
  when reconnect publishes N+1 before the network call completes;
- canceling that lease permits replacement and invokes neither client;
- same-name replacement cannot receive the old client's call;
- repeated refresh/close/reconnect under `go test -race` has no data race,
  deadlock, leaked callback, or cross-generation deletion.

### Entrypoints and gates

- TUI, Plain, ordinary headless, and ACP QueryEngine construction use the
  registry-backed manager;
- ACP client descriptors retain transactional prepare/commit and process-tree
  cleanup;
- subagents retain inherited authority;
- standalone MCP remains outside the model runtime.

Final verification requires:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
git diff --check
```

Focused tests may run with raw `go test` while iterating, including
`go test -race ./tools ./engine/mcp -count=1`. Final completion still requires
the Makefile gates.

## Compatibility and rollback

The intentional behavior change is fail-closed convergence. A disconnected,
invalidly refreshed, colliding, or generation-raced server no longer leaves
its last registry tools executable. Valid tool names, input schemas,
capability metadata, permission prompts, and next-turn visibility remain
compatible.

P33.1 has no durable migration. A squash revert restores the legacy split
owner, but reopens G5 and the cross-generation dispatch ambiguity. Operational
rollback without code revert is to disable the affected configured MCP server
and restart the engine; no stale row may be retained as a fallback.

## Documentation owners at closeout

| Fact | Owner to update |
|---|---|
| Current manager, registry, and entrypoint behavior | [`mcp.md`](../../architecture/capabilities/mcp.md) |
| User configuration, refresh, and recovery | [`extensions-mcp-skills-plugins.md`](../../guides/extensions-mcp-skills-plugins.md) |
| Verified delivered facts | [`STATUS.md`](../STATUS.md) |
| G5 closure | [`REMAINING.md`](../REMAINING.md) |
| Queue and completed slice | [`PLAN.md`](../PLAN.md) |
| Comparative decision | [`mcp-live-tool-generation-audit.md`](../reference/runtime/mcp-live-tool-generation-audit.md) |
| Reproduction commands | `docs/migration/verification/p33-1-mcp-live-tool-generation.md` |
| Delivery, review, compatibility, and rollback | `docs/migration/history/runtime/p33-1-mcp-live-tool-generation.md` |

Closeout moved this contract to `historical`, removed P33.1 from the live
queue, closed G5, and returned root PLAN to intake. Completion evidence is in
[`p33-1-mcp-live-tool-generation.md`](../history/runtime/p33-1-mcp-live-tool-generation.md).
No later slice was promoted automatically.
