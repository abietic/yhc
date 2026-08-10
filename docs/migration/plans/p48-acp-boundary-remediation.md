# P48 ACP v1 Cross-Boundary Remediation

**Status:** historical
**Created:** 2026-08-07
**Approval:** approved by the user on 2026-08-07
**Completed:** 2026-08-07
**Execution state:** P48.1-P48.5 complete; G42-G46 closed

> **Ownership:** accepted target behavior, slice boundaries, invariants, and
> rollback gates for G42-G46. Root [`queue.yaml`](../queue.yaml) alone decides
> whether a slice is executable. Current behavior remains owned by
> [`acp-adapter.md`](../../architecture/platform/acp-adapter.md),
> [`permissions.md`](../../architecture/capabilities/permissions.md), and the
> MCP/runtime architecture.

## Decision

P48 accepts the reviewed
[`ACP v1 Cross-Boundary Remediation design`](../../superpowers/specs/2026-08-07-acp-boundary-remediation-design.md)
and delivers it as five independent pull requests:

1. `project-native` process-local Session-root correlation for delete;
2. `preserve` one engine tool identity through ACP Plan permission;
3. `preserve` string-valued live/replay tool `rawOutput`;
4. `adapt` one OS-aware MCP environment-key identity; and
5. `reject` the unsafe private Session import/export surface.

The order limits simultaneous compatibility changes at the ACP boundary. Each
slice has one public-seam negative oracle, one rollback boundary, and its own
history and verification closeout. P48 does not introduce ACP v2, a second
runtime owner, or a durable global Session catalog.

## Current Evidence

- P48.1 replaced default-only inactive deletion with process-local canonical
  root observations from successful creation, restore, fork, and returned list
  rows; ambiguity now fails before filesystem mutation.
- P48.2 now keeps the engine `ToolUseID` across Plan start, initial choice,
  Back retries, bypass confirmation, and terminal delivery without replacing
  Plan request/revision identity or its shared deadline.
- P48.3 now keeps exact redacted tool-result text string-valued on live and
  replay paths without treating JSON-looking bytes as a result schema.
- ACP MCP admission, setup fingerprints, and process overlay share one
  OS-aware environment-key identity while retaining original key spelling and
  values for process construction.
- `_session/export` and `_session/import` are no longer recognized extension
  methods; both return ordinary SDK MethodNotFound without construction,
  registration, or filesystem mutation.

The
[P48.1 history](../history/runtime/p48-1-acp-session-root-delete.md),
[P48.2 history](../history/runtime/p48-2-acp-plan-tool-identity.md),
[P48.3 history](../history/runtime/p48-3-acp-string-raw-output.md), and
[P48.4 history](../history/runtime/p48-4-mcp-environment-identity.md), and
[P48.5 history](../history/runtime/p48-5-remove-private-session-migration.md)
retain the closed G42-G46 reproduction and delivery evidence.

## Frozen Cross-Slice Invariants

1. `QueryEngine` remains the model, tool, permission, and Plan execution owner.
2. `engine/session` remains the durable transcript, restore, fork,
   containment, and deletion owner.
3. `server/acp` owns binding, client interaction, wire projection, and only the
   minimal process-local root locator added by P48.1.
4. Assistant chunks remain byte-exact. These slices do not trim, join, or
   synthesize whitespace.
5. Late complete tool `rawInput`, registry-backed command snapshots, Goal,
   public load/resume/fork/delete, and `_session/status` remain unchanged.
6. No prompt, transcript content, environment value, credential, or provider
   secret enters an error or diagnostic.
7. Every slice starts from then-current `origin/master`, uses one short-lived
   branch and PR, and updates only fact owners changed by that slice.

## P48.1 Observed Session-Root Delete

**Status:** complete on 2026-08-07
**Closes:** G42

**Observable contract:** After new, load, resume, fork, or list has observed a
Session ID at one canonical project root, inactive delete removes that exact
Session's transcript and owned sidecars. Close retains the observation so the
normal close-then-delete sequence works. Successful or idempotent deletion
forgets only the exact locator entry it used.

The same ID observed under two canonical roots becomes ambiguous for the
process lifetime. Delete returns a typed Session conflict and mutates neither
root. An unobserved ID retains default-CWD fallback because ACP v1 delete has
no CWD field. Locator synchronization does not replace `sessionLifecycleMu` or
become a second durable lifecycle owner.

**Implementation boundary:** `server/acp` Session lifecycle registration and
delete targeting plus focused public ACP tests.

**Required evidence:** cross-CWD new/close/delete; fresh-agent list/rebuild;
same-ID/two-root conflict with both trees unchanged; focused race and Session
contract packs.

**Rollback:** remove the process-local locator and registrations. No durable
schema or migration remains.

## P48.2 Plan Tool-Call Identity

**Status:** complete on 2026-08-07
**Closes:** G43

**Observable contract:** `PermissionPromptRequest.ToolUseID` is the only ACP
`toolCallId` for one `ExitPlanMode` invocation. Tool start, initial Plan
choice, bypass confirmation when needed, and exactly one terminal update all
use that value. Plan request/revision identity remains separate. A blank tool
identity fails closed before any client permission request.

**Implementation boundary:** the ACP permission adapter and lifecycle ledger;
engine Plan authorization and deadlines remain unchanged.

**Required evidence:** a real engine-to-ACP trace with one exact identity and
start-before-permission-before-terminal ordering; back/bypass, reject,
timeout, cancellation, and blank-ID cases; focused race and SDK-wire packs.

**Rollback:** restore only the synthetic ACP permission IDs without changing
engine Plan policy or persisted state.

## P48.3 String Tool Raw Output

**Status:** complete on 2026-08-07
**Closes:** G44

**Observable contract:** ACP `rawOutput` is the exact redacted tool-result
text on live and replay paths. JSON-looking content including objects, arrays,
numbers, booleans, null, quoted strings, and empty text remains a string. No
content-shape inference or typed result schema is introduced.

**Implementation boundary:** ACP replay projection only. The engine canonical
terminal producer and live decoder remain unchanged.

**Required evidence:** table-driven live/replay wire checks that assert dynamic
type `string` and exact bytes, plus existing replay order and SDK-wire packs.

**Rollback:** restore replay JSON inference only; no durable data changes.

## P48.4 OS-Aware MCP Environment Identity

**Status:** complete on 2026-08-07
**Closes:** G45

**Observable contract:** one `engine/mcp` helper defines environment-key
identity for ACP duplicate admission, setup fingerprints, and inherited
process overlay. Windows folds admitted names to uppercase; non-Windows keeps
exact bytes. Values and Unix behavior remain unchanged. A Windows descriptor
containing `Path` and `PATH` fails before process creation, while otherwise
equivalent spellings produce the same fingerprint.

**Implementation boundary:** shared engine/MCP identity helper, ACP validation
and fingerprinting, and focused platform tests.

**Required evidence:** pure Windows/Unix helper tests, Windows-tagged ACP
duplicate/fingerprint tests, non-Windows compatibility tests, cross-platform
build, and an honest separation between cross-compilation and a real Windows
process run.

**Rollback:** restore exact-key ACP admission/fingerprinting and the
launcher-local Windows fold as one unit.

## P48.5 Remove Private Session Migration

**Status:** complete on 2026-08-07
**Closes:** G46

**Observable contract:** `_session/export` and `_session/import` are no longer
recognized extension methods and return the same MethodNotFound response as
other unknown extensions. No engine construction, Session registration,
filesystem mutation, or compatibility alias occurs.

**Implementation boundary:** remove only the ACP-private migration token,
handlers, errors, hook, dispatcher cases, tests, and current architecture
claims. Keep public Session load/resume/fork/delete, engine sanitized export,
Goal, `_session/status`, and shared Session conflict semantics.

**Required evidence:** real dispatcher/wire calls for both removed names,
unchanged retained extension tests, production/current-doc source scan, and
ACP SDK verification.

**Rollback:** restore the private surface as one unit only if new compatibility
evidence reverses the accepted `reject` decision.

## Promotion Gate

The written P48 contract approved all five independent rows. P48.1 completed
process-local observed-root deletion and closed G42; P48.2 preserved one
engine identity through ACP Plan permission and closed G43; P48.3 preserved
exact string-valued tool output across live and replay and closed G44; P48.4
shared one OS-aware environment identity across ACP admission, setup
fingerprints, and process launch and closed G45; P48.5 removed the rejected
private migration surface and closed G46.

Each slice merged and passed source-backed verification before the next queue
promotion. That delivery order controlled rollout and review scope; it does
not imply that one defect caused another. No P48 row remains in the queue.

## Closeout Ownership

Every P48 implementation PR must:

1. close only its named gap;
2. update current architecture and `STATUS.md` only for delivered behavior;
3. create one runtime history and one verification artifact;
4. remove its queue row and explicitly leave or promote the successor;
5. run focused, contract, race, SDK-wire, platform, and repository gates
   selected by the changed boundary; and
6. report local automation, remote CI, cross-build, and real-platform evidence
   separately.

P48 is complete: P48.5 closed and no P48 row remains in the queue. The approved
design and this contract are retained historical planning evidence; current
runtime capability belongs in architecture and `STATUS.md`.
