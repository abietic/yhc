# ACP v1 Cross-Boundary Remediation Design

**Status:** historical
**Last verified:** 2026-08-07
**Completed:** 2026-08-07
**Source snapshot:** `origin/master` at `b2c4c67b1bc7a6cdb454bccf58c2a3b2d063a1df`

> **Ownership:** retained source-snapshot design and decomposition for the five
> completed ACP v1 boundary repairs; current behavior remains owned by
> [`acp-adapter.md`](../../architecture/platform/acp-adapter.md), reproduced
> gaps by [`REMAINING.md`](../../migration/REMAINING.md), and accepted execution
> order by [`PLAN.md`](../../migration/PLAN.md)

This document lets an ACP maintainer decide what must change, what must remain
stable, and how each repair can be verified independently. It must be refreshed
if the ACP SDK binding, session lifecycle, permission identity, canonical tool
projection, stdio environment overlay, or extension dispatcher changes before
the corresponding slice starts.

## Decision

Repair five observable ACP v1 boundary defects without introducing a second
runtime owner or starting an ACP v2 migration:

1. retain the canonical project root associated with every Session ID observed
   by the ACP agent, so delete does not silently target the process default
   project;
2. keep the engine-issued tool-use ID across Plan permission requests and the
   tool lifecycle;
3. project live and replayed tool `rawOutput` as the same string value, even
   when the string happens to contain valid JSON;
4. use one OS-aware environment-key identity for ACP admission, setup
   fingerprints, and stdio process overlay; and
5. remove the unadvertised `_session/export` and `_session/import` extensions
   because their token is not a portable Session archive and import bypasses
   the supported restore lifecycle.

The five changes share an ACP compatibility goal but have independent owners,
tests, and rollback boundaries. They therefore ship as five short-lived
branches and pull requests, not as one broad adapter rewrite.

## Why the Recorded Implementation Was Unsafe

The recorded source snapshot already contained the earlier stream-byte, late
`rawInput`, command-snapshot, load/replay, and stdio-MCP hardening. Its five
defects crossed different boundaries:

| Boundary | Behavior at the recorded source snapshot | Consequence |
|---|---|---|
| Session delete | [`Agent.UnstableDeleteSession`](../../../server/acp/agent.go) always derived the transcript directory from `a.config.CWD`, while new, load, resume, fork, and list accepted or revealed another CWD. Missing files were treated as successful idempotent deletion. | Closing a Session created in another CWD and then deleting its ID could return success while leaving all durable state intact. |
| Plan permission identity | [`Agent.requestACPPlanApproval`](../../../server/acp/agent.go) created `plan_approval_N`; [`Agent.acpPlanBypassConfirmation`](../../../server/acp/agent.go) created another `plan_bypass_confirm_N`, after the tool start was sent under the engine `ToolUseID`. | One engine invocation appeared as unrelated client identities, breaking tool/permission correlation and lifecycle reasoning. |
| Tool replay output | Live terminal projection JSON-encoded the redacted result string in [`buildCanonicalToolTerminalProjection`](../../../engine/projection_lifecycle.go), then ACP decoded it back to a string. Replay used [`decodeACPReplayRawOutput`](../../../server/acp/replay.go), which parsed JSON-looking transcript text into objects, arrays, numbers, booleans, or null. | The same durable tool result changed wire type between the live turn and `session/load`. |
| Windows stdio environment | At the recorded source snapshot, ACP duplicate admission and [`fingerprintACPMCPServers`](../../../server/acp/mcp_setup.go) compared exact key spelling. The [stdio launcher](../../../engine/mcp/stdio_transport.go) used a local case-insensitive Windows key identity. | `Path` and `PATH` could be admitted or fingerprinted as distinct even though the launched process treated them as the same variable. |
| Private Session migration | [`Agent.ImportSession`](../../../server/acp/streaming.go) constructed a `Session` directly and registered it without `newSession`, restore staging, command publication, hook startup, or normal commit ordering. Its token contained metadata, not transcript bytes. | The extension promised portability it could not deliver and created a partially initialized active Session. |

These were source-snapshot gaps, not evidence that ACP v1 as a whole was naive.
The repairs preserved already verified behavior outside these seams.

## Reference and Adoption Decisions

ACP v1 wire semantics remain the protocol authority. The official TypeScript
SDK and first-party adapters remain schema and lifecycle evidence; the current
Go SDK dispatcher remains the production wire owner. No SDK artifact version
is treated as a negotiated wire version.

| Repair | Decision | Reason and compatibility consequence |
|---|---|---|
| Session root correlation | `project-native` | ACP delete carries only a Session ID, while Eino-Agent stores transcripts per project. Add the smallest process-local correlation needed by this adapter; do not invent a second durable Session catalog. |
| Plan permission identity | `preserve` | ACP tool updates and permission requests correlate by one `toolCallId`. Reuse the engine invocation identity throughout; keep Plan request/revision identity separate. |
| String `rawOutput` | `preserve` | The engine result is text and live ACP already exposes it as text. Replay must preserve that value instead of guessing a JSON schema from its bytes. |
| Windows environment identity | `adapt` | Apply Windows process semantics through a Go-owned helper shared by admission, fingerprinting, and launch overlay. Unix remains case-sensitive. |
| Private import/export | `reject` | The unadvertised token has no complete durable payload and import violates supported restore ordering. Return MethodNotFound instead of retaining a misleading compatibility surface. |

ACP v2, a Rust or TypeScript SDK rewrite, reference-specific runtime stores,
and client-specific presentation work are outside this repair program.

## Repair 1: Resolve Session Delete Against an Observed Root

### How deletion chooses a root

`Agent` owns a process-local locator from `SessionId` to a canonical Session
CWD. A locator entry has one of two states: one exact root or ambiguous. Root
canonicalization uses the existing ACP absolute, clean, and best-effort
symlink resolution rule.

The agent records a root only after a lifecycle operation has successfully
established or returned that identity:

- `session/new`, `session/load`, and `session/resume` record the engine's
  effective CWD at their successful commit/response boundary;
- unstable fork records the child engine's effective CWD only after the child
  is fully registered and its command snapshot was delivered; and
- every returned `session/list` row records the row's canonical CWD, allowing a
  new ACP process to rebuild correlation before deletion.

Closing an active Session removes only the live engine owner. It retains the
locator because delete normally follows close. A successful delete, including
an idempotent missing target, removes the exact locator entry it used.

If the same Session ID is observed under two distinct canonical roots, the
locator becomes permanently ambiguous for that agent process. Delete then
fails with a typed Session conflict and does not touch either root. It never
selects whichever observation happened last.

If an ID has never been observed, delete retains the existing default-CWD
fallback and idempotent missing behavior. This is the only backward-compatible
choice because the ACP v1 delete request contains no CWD. Cold-start deletion
of a cross-project ID is supported after `session/list`, load, or resume has
established its root; a durable global ID-to-root index is deliberately not
added.

### Failure and concurrency rules

- Active Sessions remain non-deletable.
- Unsafe IDs continue to fail inside the contained `session.DeleteSession`
  owner before mutation.
- Locator access is synchronized; registration never overwrites ambiguity.
- `sessionLifecycleMu` continues to serialize active-owner transitions and
  durable deletion. Locator state does not become a second Session lifecycle
  lock.
- Filesystem and containment errors remain visible. Only `os.ErrNotExist`
  retains idempotent success.

### How deletion is proved

An ACP scenario creates a Session in project B while the agent default is
project A, closes it, deletes by ID, and proves project B's transcript and
owned sidecars are gone. A second scenario rebuilds the mapping through
`session/list` on a fresh agent. A duplicate-ID fixture across two roots must
return a conflict and prove neither tree changed.

## Repair 2: Keep Plan Permission on the Original Tool Identity

### How one tool ID survives every Plan decision

`PermissionPromptRequest.ToolUseID` is the only ACP `toolCallId` for an
`ExitPlanMode` invocation. The initial Plan choice, a return from bypass
confirmation, and the bypass confirmation itself all send
`RequestPermission` with that same ID. Titles, content, and option sets may
change between rounds; identity may not.

The existing order remains:

1. synchronously deliver or de-duplicate the pending tool start under the
   engine ID;
2. request the Plan decision under that same ID;
3. when required, request the second bypass confirmation under that ID and the
   original deadline;
4. settle engine Plan state; and
5. deliver exactly one terminal tool update under that ID when execution
   finishes.

The adapter removes synthetic `plan_approval_N` and
`plan_bypass_confirm_N` identifiers. It does not merge the distinct engine
`PlanApprovalRequest.RequestID` or Plan revision into tool identity. A missing
or blank engine `ToolUseID` fails closed before any permission request; no
adapter-generated replacement is allowed.

### How Plan identity is proved

A real ACP client/server wire fixture records the tool start, every permission
request in a bypass/back path, and the terminal update. It asserts one exact
ID and ordering across the whole trace. Reject, timeout, cancellation, and
blank-ID paths must produce no unrelated tool identity and must not broaden
permission.

## Repair 3: Preserve Tool Raw Output as Text Across Replay

### What `rawOutput` means

ACP `rawOutput` for Eino-Agent tools is the redacted actual result string. The
live canonical projection continues to store that string as valid JSON inside
`CanonicalToolPayload.RawOutput`; the ACP lifecycle decoder continues to
produce the string value. Replay passes the durable tool-result content as a
string directly and removes JSON-shape inference.

Consequently, each of these transcript texts remains a string on live and
load/replay paths: `{"ok":true}`, `[1]`, `null`, `1`, `true`, and a quoted JSON
string. Empty output is also an empty string. `content` and `rawOutput` continue
to carry the same redacted text; this repair does not introduce a typed tool
result schema.

Malformed transcript structure still fails closed in the replay builder. Only
the interpretation of a valid durable tool-result content field changes.

### How string replay is proved

A table-driven contract runs JSON-looking outputs through both the live
canonical projection/ACP lifecycle and `session/load` replay wire. Each
terminal update must expose `RawOutput` with Go dynamic type `string` and exact
bytes. Existing object-expecting replay assertions are replaced, not retained
as a compatibility mode.

## Repair 4: Give MCP Environment Keys One OS Identity

### How environment keys are canonicalized

`engine/mcp` owns one environment-key canonicalization function used by all
three semantic decisions:

1. ACP descriptor admission detects duplicate keys using the canonical key;
2. ACP setup fingerprints sort and encode canonical keys with their exact
   values; and
3. stdio inherited-environment overlay removes and replaces inherited entries
   using the same canonical key.

On Windows the canonical key is uppercase ASCII for the admitted environment
name grammar. On non-Windows systems it is the original key. Values, commands,
arguments, server names, and Unix key identity remain byte-exact.

Two Windows setup requests that differ only by `Path` versus `PATH` therefore
have the same fingerprint when their values match. A single descriptor that
contains both spellings is rejected as `environment_name_duplicate` before
engine or process creation. The launched environment contains one semantic
key after overlay.

### Platform proof

Pure helper tests exercise Windows and Unix modes on the current development
platform. ACP validation and fingerprint tests use the production canonical
key owner through a narrow injected seam or a Windows-tagged fixture; they do
not duplicate the normalization algorithm in their oracle. The repository
cross-platform build is required. A Windows-only process test remains the
runtime acceptance for actual `exec.Cmd` behavior; without a Windows runner,
closeout must label that evidence unavailable rather than calling a cross-build
a runtime pass.

## Repair 5: Remove the Unsafe Private Migration Extensions

### What is removed

The extension dispatcher retains negotiated Goal methods and
`_session/status`. It no longer recognizes `_session/export` or
`_session/import`; both return the SDK's MethodNotFound response exactly like
any other unknown extension.

Remove only the ACP-private migration implementation and its exclusive surface:

- `SessionMigrationToken`;
- `Agent.ExportSession` and `Agent.ImportSession`;
- migration checksum, migration-only errors, handlers, parameter types, test
  hooks, and direct tests; and
- current architecture claims that describe the private migration surface.

Do not remove engine/session's sanitized presentation export, protocol
load/resume/fork/delete, `_session/status`, or the shared Session conflict code
used by other ACP operations. Historical documents remain historical; they
are not rewritten to pretend the extension never existed.

### How removal is proved

One real dispatcher/wire test calls both removed method names and asserts
MethodNotFound with no engine construction, Session registration, filesystem
mutation, or alternate compatibility handler. Existing import/export success
tests are deleted because retaining them would assert the rejected contract.

## Cross-Slice Invariants

Every slice preserves these boundaries:

- `QueryEngine` remains the model/tool/permission/Plan execution owner;
- `engine/session` remains the transcript, restore, fork, containment, and
  durable deletion owner;
- `server/acp` owns only session binding, client interaction, wire projection,
  and the new process-local root correlation;
- assistant chunks remain byte-exact; no trimming, joining, or newline repair
  belongs in these changes;
- late complete `rawInput` and complete registry-backed command snapshots
  remain unchanged;
- ACP protocol v1 and the pinned Go SDK wire dispatcher remain in place;
- no new durable schema, home-directory scan, network transport, or global
  mutable registry is introduced; and
- errors and diagnostics must not include prompts, transcript content,
  environment values, or credentials.

## Delivery Decomposition

The implementation-planning stage creates five independent test-first plans in
this order:

| Order | Slice | Primary owner | Rollback boundary |
|---:|---|---|---|
| 1 | Cross-CWD Session delete locator | `server/acp` Session lifecycle | Remove the process-local locator and its registrations |
| 2 | Plan permission identity | `server/acp` permission adapter | Restore synthetic request IDs without touching engine Plan policy |
| 3 | String replay `rawOutput` | `server/acp` replay projection | Restore replay JSON inference only |
| 4 | Windows MCP environment identity | `engine/mcp` plus ACP admission/fingerprint | Restore exact-key admission/fingerprint and launcher-local folding |
| 5 | Private migration removal | ACP extension dispatcher | Restore the removed private surface as one unit if compatibility evidence reverses the `reject` decision |

Each slice starts from the then-current `origin/master`, maps to one
independently reviewable pull request, and updates only the fact owners changed
by that slice. Migration intake and the one-Ready-slice limit remain controlled
by `docs/migration/queue.yaml` and `docs/migration/PLAN.md`; this design artifact
does not promote itself.

## Verification and Completion Boundary

Each implementation slice begins with a failing regression at the public seam,
then runs the smallest applicable focused and risk-specific pack. ACP wire,
session, permission, replay, or lifecycle changes require `make test-contract`;
shared mutable lifecycle and permission changes also require `make test-race`.
SDK-facing changes run `./scripts/verify-p23-5-acp-sdk.sh`.

After the last edit in every code pull request, completion requires:

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

Local gates, remote CI, cross-compilation, and real Windows runtime evidence
are reported separately. A green Unix build cannot be described as a Windows
runtime test, and ignored CI cannot be described as green.

## Recorded Source Owners

The exact historical contents are bound by the source-snapshot commit in this
document's metadata. Links intentionally target files rather than mutable line
numbers.

| Boundary | Source owner at design time | Why it mattered |
|---|---|---|
| ACP Session lifecycle and delete | [`Agent.NewSession`](../../../server/acp/agent.go), [`Agent.ResumeSession`](../../../server/acp/agent.go), [`Agent.ListSessions`](../../../server/acp/agent.go), [`Agent.LoadSession`](../../../server/acp/agent.go), and [`Agent.UnstableDeleteSession`](../../../server/acp/agent.go) | Established where Session ID, effective CWD, active ownership, and delete target met |
| ACP fork lifecycle | [`Agent.UnstableForkSession`](../../../server/acp/streaming.go) | Established the child identity only after durable fork and restore |
| ACP tool/permission ordering | [`Agent.makeACPPermissionPrompt`](../../../server/acp/agent.go) and [`acpToolLifecycleLedger.ensurePermissionVisible`](../../../server/acp/tool_lifecycle.go) | Owned start-before-permission ordering and stable invocation identity |
| Canonical tool terminal | [`buildCanonicalToolTerminalProjection`](../../../engine/projection_lifecycle.go) | Owned the redacted live result string before entrypoint projection |
| ACP load replay | [`buildACPReplayProjection`](../../../server/acp/replay.go) | Owned provider-free durable history projection |
| ACP stdio setup | [`validateACPSessionMCPSetup`](../../../server/acp/mcp_setup.go) | Owned request validation and setup fingerprint construction |
| Stdio process environment | [`inheritedEnvironmentWithOverlay`](../../../engine/mcp/stdio_transport.go) | Owned the exact environment given to the child process |
| Private extension dispatch | [`Agent.HandleExtensionMethod`](../../../server/acp/streaming.go) | Owned which unadvertised extension names were callable |

## Non-Goals

- Diagnosing or rewriting provider-generated assistant whitespace.
- Changing tool `rawInput`, command discovery, public Session replay, Goal,
  rich prompt ingress, or MCP transport support already implemented elsewhere.
- Adding ACP v2 negotiation, an official SDK migration, or client-specific UI
  behavior.
- Persisting a new global Session-ID catalog or scanning arbitrary user paths.
- Turning textual tool output into typed JSON based on content inspection.
- Preserving private import/export under a feature flag, alias, or deprecation
  stub without new product evidence.
