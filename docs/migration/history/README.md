# Runtime Migration History

**Status:** historical
**Last verified:** 2026-08-26

> **Ownership:** this file indexes completed migration stages and their closeout
> evidence. It does not own current status, active order, or backlog. Current
> facts belong in [`migration/STATUS.md`](../STATUS.md); accepted future work belongs in
> [`migration/PLAN.md`](../PLAN.md); unresolved gaps belong in
> [`migration/REMAINING.md`](../REMAINING.md).

## Closed Root-Gap Metadata

When a closeout resolves a root Gap, declare its canonical identity within the
first 30 lines, for example `**Closed gaps:** G22`. Multiple identities use
`**Closed gaps:** G6, G7`; they are numerically ordered and unique across this
history tree. Closeouts that resolve no root Gap omit the field. Sub-program
identities such as `G11.F2` remain narrative text.

## 2026-08-13 Desktop Workbench Forward-Port

[`2026-08-13-desktop-workbench-forward-port.md`](2026-08-13-desktop-workbench-forward-port.md)
records the local delivery boundary for the Electron workbench and loopback
app-server. Current behavior is owned by the
[Desktop workbench architecture](../../architecture/desktop-workbench.md), not
by that record.

## 2026-07-23 Runtime Hardening

[`2026-07-23-runtime-hardening.md`](runtime/2026-07-23-runtime-hardening.md)
records three independently merged fixes after P13-P18 completion:
descriptor-preserving bounded TUI output, exact ProjectGraph interrupt queue
admission, and default-allowed Session/Agent-scoped TodoWrite state. It owns
delivery evidence only; current behavior remains in architecture and
`STATUS.md`.

## P20 Plan Interaction And Permission Coherence

[`p20-h0-plan-capability-precedence.md`](runtime/p20-h0-plan-capability-precedence.md)
records the first independently merged P20 boundary: explicit deny remains
authoritative, while a runtime-proven exact Plan Write/Edit capability no
longer enters ordinary permission prompting or bookkeeping.
[`p20-0-reviewed-plan-approval.md`](runtime/p20-0-reviewed-plan-approval.md)
records the second boundary: typed outcomes, exact reviewed-byte identity,
complete previous-mode restoration, and cross-entrypoint/recovery projection.
[`p20-1-plan-review-state.md`](runtime/p20-1-plan-review-state.md) records the
third boundary: explicit Review/Actions/Feedback focus, bounded coordinate
geometry, sticky de-duplicated actions, and responsive state preservation.
[`p20-2-plan-feedback-editor.md`](runtime/p20-2-plan-feedback-editor.md) records
the fourth boundary: shared bounded textarea/cursor/undo mechanics, independent
multiline feedback state, effective-key hints, semantic input styles, and
generic-denial isolation.
[`p20-3-plan-editor-terminal-round-trip.md`](runtime/p20-3-plan-editor-terminal-round-trip.md)
records the fifth boundary: shared `VISUAL`/`EDITOR` resolution,
identity-bound Plan callbacks, exact presentation restoration, ordered
terminal capability reacquisition, and a real repeated fake-Vim PTY round
trip.
[`p20-4-plan-entrypoint-closeout.md`](runtime/p20-4-plan-entrypoint-closeout.md)
records the final boundary: request-bound engine approval authority, TUI/plain/
ACP ProjectGraph convergence, headless and standalone-MCP fail-closed behavior,
cold recovery proof, and one-release read-only bool compatibility. Its
single-action adapter completion claim was later superseded.
[`p20-r1-plan-authorization.md`](runtime/p20-r1-plan-authorization.md) records
the corrective authorization boundary: explicit TUI terminal intent, unique
TUI/plain/ACP targets, mandatory two-step bypass, and one-deadline ACP Back
loops.
[`p20-r2-plan-feedback-cursor.md`](runtime/p20-r2-plan-feedback-cursor.md)
records the corrective presentation boundary: Bubbles pre-reversal cursor
semantics, App-selected render-only no-color caret, final-cell/golden evidence,
and real PTY verification.
[`p20-r3-plan-interaction-closeout.md`](runtime/p20-r3-plan-interaction-closeout.md)
records the corrected final boundary: visible default-No TUI bypass
confirmation plus the consolidated cross-entrypoint, recovery, permission-race,
and repeated external-editor PTY matrix that closes G10.

## P21 Command Surface Simplification

[`p21-command-surface-simplification.md`](runtime/p21-command-surface-simplification.md)
records the completed `combine` program: 39 active core commands, 27
tombstones reserving 33 keys, layered phase-aware discovery, two bundled
workflows, and the cross-entrypoint closeout matrix. Current behavior remains
owned by the command architecture and interaction guide.

## P22 Auto Permission Review

[`p22-h0-bash-containment.md`](runtime/p22-h0-bash-containment.md) records the
completed P22.H0 `combine` boundary: Bash no longer inherits `acceptEdits` or
`auto` authorization from its first token, contained Write/Edit behavior is
unchanged, and later classifier/prompt/fail-closed handling remains owned by
QueryEngine.
[`p22-1a-permission-decision-snapshot.md`](runtime/p22-1a-permission-decision-snapshot.md)
records the completed P22.1a `combine` boundary: existing permission branches
project through one QueryEngine four-decision seam, ProjectGraph derives its
revision from the shared immutable effective-policy identity, and direct
nil/nil library construction remains caller-authoritative.
[`p22-1b-canonical-action-policy.md`](runtime/p22-1b-canonical-action-policy.md)
records the completed P22.1b `combine` boundary: one registry-aware canonical
action and exact authority owner replaces name-only Auto admission, one
permission-result rewrite re-enters the complete policy cycle, and final
input, policy, resolved path, and registry identity remain bound through
dispatch.
[`p22-2a-permission-review-shadow.md`](runtime/p22-2a-permission-review-shadow.md)
records the completed P22.2a `combine` boundary: an off-by-default
data-minimized reviewer uses an explicit separate provider route, strict
result/deadline, and fresh process-local binding while leaving the legacy
classifier, interaction, grants, denial accounting, and dispatch authoritative.
[`p22-2b-permission-review-audit.md`](runtime/p22-2b-permission-review-audit.md)
records the completed P22.2b `combine` boundary: an independently opted-in
secure local redacted journal correlates typed reviewer facts through one
opaque event ID, and a provider-free report preserves source-specific
denominators, corruption evidence, and every retained false allow without
changing permission authority.

## P23 ACP Adapter Hardening

[`p23-h0-session-deletion-containment.md`](runtime/p23-h0-session-deletion-containment.md)
records the completed P23.H0 `combine` boundary: the engine session service
preflights one resolved-root owned deletion set, while ACP rejects active
targets and delegates inactive deletion without mutating the session registry
on rejection.
[`p23-h1-acp-capability-truth.md`](runtime/p23-h1-acp-capability-truth.md)
records the completed P23.H1 `combine` boundary: load is unadvertised, agent
identity and ordered Text/ResourceLink ingestion are truthful, and unsupported
rich/setup/cursor input fails before model or session mutation.
[`p23-1-acp-sdk-envelope.md`](runtime/p23-1-acp-sdk-envelope.md)
records the completed P23.1 `combine` boundary: the production Go SDK's
v1/version-2 fallback, errors, request cancellation, notification ordering,
current fragmented/interleaved wire, exact projector bytes, delivery failures,
and session isolation are pinned while one inactive validated canonical
lifecycle envelope changes no client output.
[`p23-2-acp-tool-lifecycle.md`](runtime/p23-2-acp-tool-lifecycle.md)
records the completed P23.2 `combine` boundary: one engine-owned redacting
producer and prompt-scoped ACP ledger now project start-before-permission,
final input, replacement progress, and exactly one completed/failed terminal
while deleting the old stateless inference path.
[`p23-3-acp-assistant-commands.md`](runtime/p23-3-acp-assistant-commands.md)
records the completed P23.3 `combine` boundary: one persisted engine logical
assistant UUID drives exact canonical ACP chunks, while the shared command
registry supplies complete changed-only discovery snapshots with explicit
delivery settlement.
[`p23-4a-replay-restore-staging.md`](runtime/p23-4a-replay-restore-staging.md)
records the completed P23.4a `project-native` prerequisite: one immutable
revision-bound replay snapshot retains validated durable identity without
writes, while a preselected restore-staging owner defers persistence and
runtime activation until commit and aborts without durable mutation.
[`p23-4b-acp-replay-bounded-listing.md`](runtime/p23-4b-acp-replay-bounded-listing.md)
records the completed P23.4b `combine` boundary: ACP validates and delivers
durable replay before staging commit/registration, advertises truthful load,
and delegates bounded durable-plus-active pagination to one generation-bound
engine/session cursor owner.
[`p23-5-transactional-stdio-mcp.md`](runtime/p23-5-transactional-stdio-mcp.md)
records the completed P23.5 `combine` boundary: ACP validates and prepares one
session-owned stdio MCP set across new/load/resume, publishes dynamic tools
through atomic owner generations, adopts the exact manager through restore
staging, and closes the complete process tree on failure or session shutdown.

## P24 Durable Goal Lifecycle

[`p24-1-durable-goal-state.md`](runtime/p24-1-durable-goal-state.md)
records the completed P24.1 `adapt` prerequisite: one saved root Session owns
an additive versioned Goal record and one QueryEngine transition service;
checkpoint-before-publish, Plan exclusion, fork isolation, fail-closed cold
normalization, and retry-only restore commit are verified without adding
events, commands, accounting, continuation, transports, or UI.
[`p24-2a-goal-lifecycle-projection.md`](runtime/p24-2a-goal-lifecycle-projection.md)
records the completed P24.2a `adapt` boundary: one ordered engine lifecycle
projection binds exact root and descendant generations, accounts root active
time, retains pending completion intent, and enforces distinct-turn blocker
evidence without adding continuation, provider accounting, commands,
transports, or UI.
[`p24-2b-goal-provider-accounting.md`](runtime/p24-2b-goal-provider-accounting.md)
records the completed P24.2b `adapt` boundary: one root-scoped admission gate,
append-only exact provider-usage ledger, crash recovery, and aggregate budget
transition close accounting without adding automatic continuation or a user
surface.
[`p24-3-durable-goal-continuation.md`](runtime/p24-3-durable-goal-continuation.md)
records the completed P24.3 `adapt` boundary: one versioned Goal cursor and
dormant deterministic runtime item now survive checkpoint, queue, receipt,
rejection, and restart windows while remaining unclaimable and unsignalled for
all current production transports.
[`p24-4-tui-goal-workflow.md`](runtime/p24-4-tui-goal-workflow.md)
records the completed P24.4 `adapt` boundary: an off-by-default saved-root TUI
now owns typed Goal controls, dynamic root-turn tools, reducer progress, and a
dedicated continuation consumer while generic and unsupported entrypoint paths
remain excluded.
[`p24-5a-plain-goal-consumer.md`](runtime/p24-5a-plain-goal-consumer.md)
records the completed P24.5a `adapt` boundary: saved-root Plain shares the same
engine Goal authority through one process-lifetime stdin broker, explicit
input/permission precedence, bounded lifecycle output, and durable EOF or
cancellation shutdown without widening generic runtime items.
[`p24-5b-bounded-headless-goal.md`](runtime/p24-5b-bounded-headless-goal.md)
records the completed P24.5b entrypoint-local `combine` boundary: one explicit
bounded process resumes an existing saved Goal, consumes only its exact durable
continuation cursor, emits one final versioned result, and leaves ordinary
headless, slash commands, ACP, and generic runtime input unchanged.
[`p24-5c-negotiated-acp-goal.md`](runtime/p24-5c-negotiated-acp-goal.md)
records the completed P24.5c `combine` boundary: a connection-negotiated
version-1 private ACP surface maps exact typed control and explicit
continuation to existing QueryEngine owners while unsupported clients, slash
commands, generic claims, and server-originated prompts remain unchanged.

## P49 Default-Enabled Budget-Optional Goal

[`p49-goal-default-unbudgeted.md`](runtime/p49-goal-default-unbudgeted.md)
records the completed P49 `adapt` boundary: supported saved-root composition
roots default-enable Goal without an inferred token cap, explicit unbudgeted
create/resume continues durably, exact provider-attempt admission survives
restart, and rollback quiescence leaves zero wake, claim, or dispatch. G21 and
G47 are closed.

## P25 Agentic Provider Input Fidelity

[`p25-agentic-provider-input-fidelity.md`](runtime/p25-agentic-provider-input-fidelity.md)
records the completed P25.1 `adapt` boundary: ordered classic user text and
media now reach typed Eino Agentic provider blocks losslessly, invalid input
fails before provider or persistence mutation, and classic ProjectGraph,
transcript, retry, and runtime-input ownership remains unchanged.

## P26 Canonical Model-Round Owner Cleanup

[`p26-canonical-model-round-owner-cleanup.md`](runtime/p26-canonical-model-round-owner-cleanup.md)
records the completed P26.1 `project-native` boundary: the canonical model
round now owns complete-stream classification only, while
`runCanonicalToolRound` remains the sole committed-call execution owner.

## P28 Standalone MCP Permission Policy

[`p28-h0-standalone-mcp-permission-policy.md`](runtime/p28-h0-standalone-mcp-permission-policy.md)
records the completed P28.H0 `adapt` boundary: standalone MCP parses one exact
startup policy before registry construction, preserves empty/`open`/`strict`
compatibility, and rejects every other value without hooks, tool execution, or
transport startup.

## P30 Cross-Entrypoint Multimodal Input

[`p30-0-multimodal-characterization.md`](runtime/p30-0-multimodal-characterization.md)
records the completed P30.0 `combine` characterization boundary: current TUI,
ACP, engine, transcript, queue, session, recovery, capability, and provider
owners remain unchanged while focused fixtures and one complete owner
inventory prove the accepted mismatch and test-only target seam.

[`p30-1a-terminal-media-safety.md`](runtime/p30-1a-terminal-media-safety.md)
records the completed P30.1a `project-native` safety boundary: the first
`media_size` failure now terminates through the canonical after-model owner
without compaction, message replacement, or a retry model call.

[`p30-1b-strict-image-admission.md`](runtime/p30-1b-strict-image-admission.md)
records the completed P30.1b `project-native` admission boundary: direct,
queued, and recovered legacy image input shares one strict bounded engine
validator, while caller path/name provenance is removed before durable or
model-visible mutation.

[`p30-1c-ordered-prompt-admission.md`](runtime/p30-1c-ordered-prompt-admission.md)
records the completed P30.1c `project-native` boundary: one immediate literal
ordered text/image API binds selected-route capability, turn-local refs, Hook
projection, and provider preparation without adding durable rich storage.

[`p30-2a-durable-media-store.md`](runtime/p30-2a-durable-media-store.md)
records the completed P30.2a `project-native` boundary: immediate rich turns
publish Session-private bytes and one ref-only transcript prompt before user
visibility or provider entry, with strict restart and contained deletion.

[`p30-2b-runtime-media-refs.md`](runtime/p30-2b-runtime-media-refs.md)
records the completed P30.2b `project-native` boundary: saved rich queue items
reuse the private store, retain exact queue/turn identity, and transfer
reachability to a flushed transcript before settlement.

[`p30-2c-session-media-lifecycle.md`](runtime/p30-2c-session-media-lifecycle.md)
records the completed P30.2c `project-native` boundary: ref-only lifecycle
projections, independent rich branching, sanitized presentation, explicit
private-migration rejection, and active-owner manual collection preserve the
same ref contract without adding automatic GC or portable media.

[`p30-3-media-size-recovery.md`](runtime/p30-3-media-size-recovery.md)
records the completed P30.3 `project-native` boundary: exact prompt-record
identity permits only historical in-position omission, Recovery Profile v1
derivatives remain attempt-local, one fsynced active-context boundary precedes
retry, and a distinct rich fallback requires fresh exact admission.

[`p30-4-tui-media-projection.md`](tui/p30-4-tui-media-projection.md)
records the completed P30.4 `project-native` boundary: one App-owned
active-draft media table, generation-fenced capture, literal ordered engine
admission, ref-backed busy input, atomic queue edit, sanitized history and
preview, and deterministic cross-platform clipboard capture replace every
byte/path-bearing TUI projection.

[`p30-5a-acp-rich-ingress.md`](runtime/p30-5a-acp-rich-ingress.md)
records the completed P30.5a `project-native` boundary: ACP v1 advertises and
admits ordered live ResourceLink, safe-raster image, and embedded context
through the existing QueryEngine, Session-private MediaStore, strict
versioned prompt-record, and lifecycle owners while rich historical load
remains fail-before-update for P30.5b.

[`p30-5b-acp-rich-load-replay.md`](runtime/p30-5b-acp-rich-load-replay.md)
records the completed P30.5b `project-native` boundary: one exact versioned
prompt-record binding projects neutral logical rich user parts through the
existing immutable Session snapshot and P23.4b ordered ACP load/staging owner,
with strict pre-update failure and no-replay resume. Provider-rich assistant
replay remains G20.

[`p30-6-multimodal-program-closeout.md`](runtime/p30-6-multimodal-program-closeout.md)
records the completed P30.6 `preserve` boundary: every new rich writer now
enters typed selected-route admission, durable queue publication is ref-only,
legacy inline fields are decode-only, proved transitional TUI and durable
writers are deleted, lifecycle and bounded-cost proof is complete, and G32 is
closed. Provider-rich assistant replay remains G20.

[`p29-0-route-characterization.md`](runtime/p29-0-route-characterization.md)
records the completed P29.0 test-only `combine` boundary: current legacy
resolution and provider-only cache limitations, target route-identity
equality/isolation, secret redaction, six provider constructors, concurrency,
and CLI/ACP composition roots now have executable fixtures.

[`p29-1-trusted-portfolio-compiler.md`](runtime/p29-1-trusted-portfolio-compiler.md)
records the completed P29.1 `combine` boundary: one source-aware, strict,
redacted compiler preserves user/project authority; named credentials resolve
only during selected-route construction; complete route identity owns lazy
cache equality and isolation; and CLI/ACP startup shares the configured
runtime.

[`p29-2-shared-inventory-model-binding.md`](runtime/p29-2-shared-inventory-model-binding.md)
records the completed P29.2 `combine` boundary: the provider runtime owns one
detached non-secret inventory; QueryEngine owns durable main-route switching,
resume admission, and the pre-dispatch guard; and commands, TUI,
plain/headless startup, ACP, fork, listing, and export consume safe shared
projections.

[`p29-3-capability-admitted-role-routing.md`](runtime/p29-3-capability-admitted-role-routing.md)
records the completed P29.3 `combine` boundary: fixed role snapshots preserve
explicit presence and dynamic main inheritance; child Sessions persist exact
Agent/model identity before execution and recover without current-policy
reinterpretation; enabled root tool summaries alone consume `summary`; and
provider adapters lower exact supported effort values with bounded
role/profile/effort attribution.

[`p29-4-bounded-overload-failover.md`](runtime/p29-4-bounded-overload-failover.md)
records the completed P29.4 `combine` boundary: one project-owned coordinator
separates same-route retry from overload-only profile switches, shares
provider-call/switch/deadline budgets, restores immutable provider input,
discards failed output without tool side effects, attributes attempt usage,
and enforces exact TUI versus conservative non-retractable entrypoint
commitment. The later
[`P29.5 Defer Decision`](../plans/p29-model-portfolio-routing.md#p295-defer-decision)
accepted no adaptive-health behavior.

## P31 Durable Task/Todo Explorer

[`p31-1a-reversible-workboard-shadow.md`](runtime/p31-1a-reversible-workboard-shadow.md)
records the completed reversible comparison-only WorkBoard shadow.
[`p31-1b-authoritative-workboard.md`](runtime/p31-1b-authoritative-workboard.md)
records the completed forward-only authority boundary: one root-lineage
adapter, marker-last WorkBoard v2 cutover, exact Task/Todo compatibility,
Session lifecycle containment, persistent uncertainty quarantine, and exact
local backup recovery.
[`p31-2-canonical-explorer-snapshot.md`](runtime/p31-2-canonical-explorer-snapshot.md)
records the completed process-local read-model boundary: revision-reserved
WorkBoard projection, cold bootstrap, immutable execution generations,
deterministic bounded selector, explicit fixture-link diagnostics, and exact
old-view compatibility.
[`p31-3-read-only-task-explorer.md`](tui/p31-3-read-only-task-explorer.md)
records the completed responsive presentation boundary and exact-generation
compatibility fence.
[`p31-4-fenced-execution-relations.md`](runtime/p31-4-fenced-execution-relations.md)
records the completed marker-last v3 relation boundary, ordered Agent
admission, exact-generation engine controls, settlement and deletion fences,
and immutable rollback floor.
[`p31-5-old-owner-closeout.md`](runtime/p31-5-old-owner-closeout.md) records
explicit QueryEngine and standalone owners, package/AppState fallback
deletion, canonical task-facing projections, exact queued-input cancellation,
entrypoint labels, and G33 closeout.

## P32 Plugin File Authority

[`p32-1-plugin-file-authority.md`](runtime/p32-1-plugin-file-authority.md)
records the completed `project-native` authority boundary: configured-root and
child-directory identity, descriptor-relative regular-file reads, materialized
prompt bytes, portable path admission, atomic skill registration, retained
generation rollback, unchanged entrypoint exclusions, and G4 closeout.

## P33 MCP Live Tool Generation

[`p33-1-mcp-live-tool-generation.md`](runtime/p33-1-mcp-live-tool-generation.md)
records the completed `adapt` boundary: exact manager and client generations,
complete owned registry publication, list-change/reconnect/close convergence,
permission-generation rejection, lease-pinned dispatch, hook-safe lifecycle
serialization, retained entrypoint scope, and G5 closeout.

## P34 File-State Checkpoint Repair

[`p34-1-file-state-checkpoint-repair.md`](runtime/p34-1-file-state-checkpoint-repair.md)
records the completed `preserve` boundary: checked incremental file-state
snapshot persistence, one turn-local complete checkpoint repair, cumulative
concurrent state, terminal persistence truth, restart/resume recovery, and G1
closeout without changing successful file-tool semantics.

## P35 TUI Notification Lifecycle

[`p35-1-tui-notification-lifecycle.md`](tui/p35-1-tui-notification-lifecycle.md)
records the completed `combine` boundary: bounded latest-three engine ingress,
one lock-free-across-Send pump, typed `App.Update` delivery, deterministic
generation-fenced idle expiry, pure stack reads/rendering, ordered
program-stop/adapter-close/terminal-close teardown, unchanged non-TUI handlers,
and G8 closeout.

## P37 ProjectGraph Permission Settlement Chain

[`p37-1-project-graph-permission-settlement-chain.md`](runtime/p37-1-project-graph-permission-settlement-chain.md)
records the completed `preserve` boundary: one base policy revision per resume
batch, serialized ordinary action rebuild and settlement, exact successful
post-revision chaining, external-drift expiry, unchanged Plan and final
dispatch fences, and G35 closeout.

## P38 Provider Reasoning Origin

[`p38-0-provider-reasoning-origin.md`](runtime/p38-0-provider-reasoning-origin.md)
records the completed `adapt` boundary: a transcript-private physical/message
origin binding, account-scoped monotonic route publication, exact
pre-conversion client proof, conservative stripping, public projection
exclusion, credential-rotation and recovery fences, nil-map panic containment,
and G34 closure.

## P39 Workspace Recovery Contract

[`p39-0-workspace-recovery-contract.md`](runtime/p39-0-workspace-recovery-contract.md)
records the completed `project-native` characterization boundary: complete
mutation capture, logical snapshot/operation identities, external-edit and
root/link conflict fencing, confirmation/permission separation, deterministic
partial retry, entrypoint exclusions, and explicit physical schema/migration/
retention/deletion gates are frozen without adding a production writer,
command, or automatic restoration. G2 remains open.

## P40 Startup Theme Polarity

[`p40-1-startup-theme-polarity.md`](tui/p40-1-startup-theme-polarity.md)
records the completed `adapt` boundary: explicit startup-only daltonized-name
mapping preserves polarity without claiming palette parity, invalid source
values produce bounded typed `App.Update` warnings, explicit runtime theme
selection remains unchanged, and G12 is closed.

## P41.1 Fixed-Size Geometry Owner

[`p41-1-fixed-size-geometry-owner.md`](tui/p41-1-fixed-size-geometry-owner.md)
records the completed `project-native` boundary: one App-profile-owned
fixed-box projection returns exact rows and inner/outer rectangles, expands
tabs at their aligned origin, preserves whole clusters, generates padding and
borders before decoration, binds interaction placement to the same rows,
deletes the two Lip Gloss fixed-size exceptions, and closes G25.

## P41.2 Bounded Markdown Renderer Pool

[`p41-2-bounded-markdown-renderer-pool.md`](tui/p41-2-bounded-markdown-renderer-pool.md)
records the completed `project-native` boundary: one App-owned capacity-32
strict-LRU pool, atomic exact-key renderer/mutex entries, uncached construction
failure, retained-pointer in-flight eviction safety, private compatibility
pools, and G26 closure without changing Markdown or geometry semantics.

## P42.0 Execution Policy Snapshot

[`p42-0-execution-policy-snapshot.md`](runtime/p42-0-execution-policy-snapshot.md)
records the completed `project-native` identity seam: one immutable
cross-entrypoint execution-policy snapshot and truthful disabled ambient-host
adapter are pinned before Bash, shell hooks, configured stdio MCP, and child
spawn. It changes no process authority and therefore leaves G28 open.

## P43.0 Real-Repository Evaluation Baseline

[`p43-0-real-repository-evaluation.md`](runtime/p43-0-real-repository-evaluation.md)
records the completed `combine` boundary: one opt-in standalone harness drives
the public external headless executable across two fresh private repositories,
grades one exact Write-only task and escape probe, emits one bounded redacted
no-replace report after cleanup, states unevaluated isolation axes honestly,
and closes G29 without becoming a CI or release gate.

## P46.1 Complete Prompt Footprint Admission

[`p46-1-complete-prompt-footprint.md`](runtime/p46-1-complete-prompt-footprint.md)
records the completed `preserve` repair: the canonical failover coordinator
now admits every candidate against one overflow-safe estimate of immutable
messages, system prompt, and complete tool definitions before route
construction. System/tool-heavy smaller-context candidates consume no attempt,
switch, provider call, or wait; G36 is closed while P46.2 becomes `Ready`.

## P46.2 Observable Attempt Discard And Switch

[`p46-2-observable-failover.md`](runtime/p46-2-observable-failover.md) records
the completed `preserve` repair: a constructable overload fallback now emits
explicit discarded truth, retracts only exact TUI attempt output, and projects
one bounded profile/switch notice through TUI, Plain, Headless, and ACP without
polluting library output, canonical assistant history, transcripts, or
structured results. G37 and the P46 repair are closed.

## P48.1 ACP Observed Session-Root Delete

[`p48-1-acp-session-root-delete.md`](runtime/p48-1-acp-session-root-delete.md)
records the completed `project-native` boundary: successful ACP lifecycle and
list observations retain one canonical process-local root per Session ID,
inactive delete delegates that exact root to the shared contained deletion
owner, multi-root ambiguity fails without mutation, and G42 is closed.

## P48.2 ACP Plan Tool-Call Identity

[`p48-2-acp-plan-tool-identity.md`](runtime/p48-2-acp-plan-tool-identity.md)
records the completed `preserve` boundary: one engine `ToolUseID` now spans
ACP Plan start, every choice and Back/bypass round, and the single terminal
update while Plan request/revision identity and authorization remain separate;
blank identity fails before client I/O, and G43 is closed.

## P48.3 ACP String Raw Output

[`p48-3-acp-string-raw-output.md`](runtime/p48-3-acp-string-raw-output.md)
records the completed `preserve` boundary: exact redacted tool-result text now
stays string-valued across independent live and replay ACP wire paths without
JSON-shape inference, transcript or lifecycle changes, and G44 is closed.

## P48.4 MCP Environment Identity

[`p48-4-mcp-environment-identity.md`](runtime/p48-4-mcp-environment-identity.md)
records the completed `adapt` boundary: ACP duplicate admission, setup
fingerprints, and stdio process overlay now share Windows-folded and
non-Windows-exact environment-key identity while preserving original admitted
keys and values, and G45 is closed.

## P48.5 Remove Private Session Migration

[`p48-5-remove-private-session-migration.md`](runtime/p48-5-remove-private-session-migration.md)
records the completed `reject` boundary: the ACP dispatcher no longer
recognizes `_session/export` or `_session/import`, both names return ordinary
MethodNotFound without Session or filesystem mutation, retained public Session
and presentation-export owners remain unchanged, and G46/P48 are closed.

## P50.1 ProjectGraph Rebuilt-Revision Fence

[`p50-1-project-graph-revision-fence.md`](runtime/p50-1-project-graph-revision-fence.md)
records the completed `project-native` boundary: a canonical action rebuilt
after the live policy check must retain the batch's current revision before
settlement; mismatch invalidates the batch without rule persistence or tool
dispatch, external writers remain independent, and G48 is closed.

## P50.2 Reviewer Attempt-Latency Denominator

[`p50-2-reviewer-latency-denominator.md`](runtime/p50-2-reviewer-latency-denominator.md)
records the completed `project-native` measurement correction: reviewer
latency admits only retained attempt-terminal pairs, terminal-only failures
remain outcome evidence, zero-pair reports stay unavailable, and G49 is closed
without changing reviewer or permission authority.

## P50.3 Non-Blocking Reviewer-Audit Dispatcher

[`p50-3-review-audit-dispatcher.md`](runtime/p50-3-review-audit-dispatcher.md)
records the completed `project-native` isolation boundary: permission and
reviewer producers only attempt bounded audit admission, one writer contains
sink latency/error/panic, QueryEngine close bounds audit flush at 250ms, typed
diagnostics expose evidence loss without reconstructing records, and G50/P50
are closed without changing reviewer or permission authority.

## P51.1 Darwin Guest Seatbelt

[`p51-1-darwin-guest-seatbelt.md`](runtime/p51-1-darwin-guest-seatbelt.md)
records the completed `project-native` Darwin Guest subset: the fixed Seatbelt
adapter, immutable process-class bindings, user-only selection, fail-closed
unavailability, ShellManager lifecycle, restore/child re-probe, and real
escape/product evidence land without changing permission outcomes. G28 remains
open for ambient credentials, hooks/MCP, and hard resource limits. P51.2 Core
later consumed the exact proof without changing this containment baseline.

## P51.2 Proof-Bound Auto Bash Core

[`p51-2-auto-containment-admission.md`](runtime/p51-2-auto-containment-admission.md)
records the completed master-native Core boundary: ordinary canonical Auto Bash
uses the exact complete Guest proof, exact local/user rules remain separate
explicit authority, critical literal `rm`/`rmdir` requires fresh `AllowOnce`,
ProjectGraph and supported Core clients preserve the constraint, and final
dispatch/submission drift fails closed. AppServer, Desktop, and Web UI
projection remains deferred, and G28 stays open.

## P51.3 Linux Guest Bubblewrap

[`p51-3-linux-guest-bubblewrap.md`](runtime/p51-3-linux-guest-bubblewrap.md)
records the completed `adapt` Linux Guest subset: fixed bubblewrap, strict
mount projection, namespace and seccomp network denial, immutable root and
child/restore binding, existing control-plane overlays, fail-closed
unavailability, and a required real Ubuntu integration. Linux proof remains
outside automatic Bash admission, and G28 stays open.

## P52.1 Versioned Headless JSONL Lifecycle

[`p52-1-headless-jsonl-lifecycle.md`](runtime/p52-1-headless-jsonl-lifecycle.md)
records the completed `adapt` boundary: `exec` and its `-p` compatibility route
project validated committed lifecycle facts as versioned JSONL, omit the
engine terminal duplicate, and close a writable stream with one classified
result while preserving text/JSON, Session, ACP, and permission authority.

## P36 ACP Provider-Rich Assistant Replay

[`p36-1-acp-rich-assistant-replay.md`](runtime/p36-1-acp-rich-assistant-replay.md)
records the completed P36.1 `combine` boundary: one Session-owned immutable
public assistant presentation validates exact provider text/reasoning shape,
ACP v1 replays every text part under one logical ID without exposing private
continuation material, unsupported shapes fail before delivery, and the
official SDK plus a real Zed restart close G20.

## Structural depth queue (P1-P8)

[`migration/history/runtime/p1-p8.md`](runtime/p1-p8.md) records the completed P1-P8 execution
contracts and evidence.

| Stage | Slice | Closeout summary |
|---|---|---|
| P1 | Tool-pool assembly and filtering | One model-visible assembly boundary, runtime deny defense, cross-entrypoint wiring. |
| P2 | Provider selection and fallback depth | Unified resolution, lazy cross-provider routing, fallback entrypoints, optional preflight. |
| P3 | Recovery cascade depth | Ordered preparation, bounded retries, message integrity, TUI feedback, ACP entrypoint evidence. |
| P4 | Hook and service lifecycle depth | Shell/HTTP hooks, async result routing, plugin commands, long-session services. |
| P5 | MCP and subagent isolation | Engine-owned runtime dependencies, inherited child scope, proxy behavior, ACP goldens. |
| P6 | Ledger and cross-entrypoint verification | All reference files classified; no `implemented_unverified` mappings remain. |
| P7 | Permission filesystem and classifier evidence | Shared path representations, synchronous auto-classifier lifecycle. |
| P8 | Ledger-exposed adapted depth | Team/private memory, Agent memory snapshots, PDF handling as separate verified slices. |

## Post-parity iterations

[`migration/history/runtime/post-parity.md`](runtime/post-parity.md) records H0,
P9.1, P9.2A, P9.2B, P10, P11, P12, P13.H0-P13.10b, P14.0-P14.3,
P15.0-P15.1, P16.H0-P16.7c, P17.H0-P17.2, and P18.H0-P18.2.

| Stage | Closeout summary |
|---|---|
| H0 Build dependency coverage | `Makefile` release/debug targets track `cmd`, `engine`, `internal`, `server`, and `tools` sources; `scripts/build_dependencies_test.go` validates dependency tracking. |
| P9.1 Repeated-identical-tool circuit breaker | Query-owned guard; streaming and batch paths reserve tickets in model order; third identical valid call stops before hooks, permission, or execution. |
| P9.2A Canonical permission interaction lifecycle | Project-scoped `PermissionCoordinator` owns request identity, atomic terminal claims, grant commit, ordered resolution events, and waiter delivery. |
| P9.2B Exact-scope positive permission coalescing | Session/project candidates coalesced after durable grant without broadening authority or moving authorization into the TUI. |
| P10 Trace and session workflow convergence | Real PTY workflow, 20-Agent attention search, 256-message transcript p95 gates, process-restart replay-only recovery. |
| P11 Core-surface slimming audit | Identified 29 zero-production-import packages: 11 under `engine/*` and 18 under `internal/tui/*`. |
| P12 Disconnected TUI scaffold removal | Deleted 18 `internal/tui` packages (19 files, 3,602 lines); manifest evidence reconciled; release binaries and PTY behavior unchanged. |
| P13.H0 Fail-closed streamed-tool commit | Pending streamed calls cross the execution boundary only after shared terminal classification; truncation, unknown terminals, stream errors, and pre-commit cancellation execute no tool. |
| P13.0 Canonical behavioral compatibility suite | Eleven deterministic test-only traces categorize model, stream, tool, event, state-boundary, terminal, queue, recovery, cancellation, and entrypoint behavior without changing production ownership. |
| P13.1 Stable Eino v0.9.12 baseline | Upgraded only the core Eino dependency; canonical traces and focused provider/execution behavior remain exact, with no compatibility code or production ownership transfer. |
| P13.2 ADK compatibility layer | Added fixture-only ADK construction, adapters, events, checkpoint/runtime-item codecs, and a production selector fixed to Legacy. |
| P13.3 Scheduler and continuation proof | Proved Eino `ToolsNode` stable batches, checkpoint reconstruction, and typed complete-round decisions while recording two P13.5 SDK gates. |
| P13.4 Model-attempt and recovery proof | Proved real Eino retry/failover execution with project route, backoff, recovery, event, history, and terminal policy retained. |
| P13.5c3 Complete ProjectGraph inner kernel | Shared preparation/recovery/safe-point/finalization owners, all canonical direct and QueryEngine traces, deferred rejection parity, and concurrent invocation isolation. |
| P13.6a ProjectGraph new-session canary | Added one durable default-off kernel pin across QueryEngine/ACP/child entrypoints with fail-closed cohort checks and no Legacy replay. |
| P13.6b ADK compatibility retirement | Deleted unused ADK construction/attempt/resume/checkpoint/ToolsNode owners and retained only the project-named live schedule and typed decision. |
| P13.7 Project input coordinator | Replaced the live queue with one durable typed owner for priority, scope, steering, stop, idle wake, settlement, and restart replay. |
| P13.8 Durable Compose Graph HITL | Added public-Eino StatefulInterrupt/checkpoint resume with a protected opaque sidecar, targeted durable decisions, live-policy revalidation, and cold TUI/ACP continuation. |
| P13.9a Foreground child ProjectGraph kernel | No-clobber pinned synchronous child Sessions to the existing Graph before executor entry while preserving AgentRunner lifecycle/worktree ownership, coordinator attention, cancellation, background selection, and one terminal generation. |
| P13.9b Background child ProjectGraph supervision | Pinned new asynchronous child Sessions before executor entry while preserving historical kernel selections and AgentRunner parent-cancel, targeted abort, steering, owned/shared Close, bounded join, and one-terminal ownership. |
| P13.9c Durable child terminal replay | Reconciled Agent JSON with reachable child Session lineage, required exact live generation ownership, restored terminal generations idempotently, and converged missing-Agent-JSON admission to inert `project_graph_orphan` replay without dispatch. |
| P13.9d Current child TUI parity | Proved live/restart/replay/orphan/evicted ProjectGraph selector projection and changed thread switching to suspend modal presentation without implicitly resolving runtime attention. |
| P13.10a ProjectGraph default cutover | Made ProjectGraph the only execution owner, added unrestricted `project_graph/v1/full` roots, deleted Legacy traversal and the process canary, and made retired/unpinned continuation fail without execution or transcript rewrite. |
| P13.10b ProjectGraph final hardening | Retired active rollout vocabulary and the test-only stage override, preserved the historical stage JSON key, closed the full entrypoint/race/PTY/golden/performance/checkpoint/repair matrix, and proved pre-cutover rollback fails closed without transcript rewrite. |
| P14.0 Explicit foreground detach | Added one owner/generation-scoped foreground wait lease that returns `backgrounded` while the same ProjectGraph child continues under existing abort, Close, and terminal ownership. |
| P14.1 Durable idempotent completion delivery | Persisted terminal generation and snapshot before publication, reconstructed only exact-parent completions, and committed a versioned parent receipt before at-least-once notification settlement. |
| P14.2a Durable transcript entry identity | Added stable versioned physical-record IDs, exact-byte revisions, revision-scoped legacy fallback, and rewrite preservation for proven retained records without backfill. |
| P14.2b Bounded durable child transcript selector | Added generation-bound opaque paging over a frozen transcript prefix with exact identity-only live merge and zero model/tool/queue/callback dispatch. |
| P14.2c Existing child detail projection | Wired bounded asynchronous pages into thread, Ctrl+B, and Teams views with stale-result rejection, stable physical identity, preserved view state, and inert replay/evicted controls. |
| P14.3 Compact multi-Agent monitor and read-only peek | Replaced `/team` mutation controls with a responsive canonical-selector monitor, bounded stale-safe peek, and existing-navigation switching while preserving draft/focus and running-request access. |
| P15.0 Deterministic terminal stress baseline | Reproduced blocked-writer teardown and silent writer errors while proving bounded backpressure, restore ordering, PTY shell recovery, late-send rejection, and lossless runtime truth. |
| P15.1 Bounded terminal output ownership | Added one synchronously acknowledged writer with typed write/drain/interrupt bounds, platform interruption, one surfaced failure, and restore-after-stop ordering. |
| P16.H0 Unowned credential deletion containment | Removed project/user credential-file deletion, made `/logout` informational with no action, and proved byte-identical credential trees across direct and cancelled dispatch. |
| P16.0 Plain and parser correctness | Routed plain clear/compact through the engine owner once, fixed fork result identity, made undo explicitly non-mutating, and installed quote-aware validated production dispatch. |
| P16.1 Truthful visibility and alias cleanup | Froze 46 visible/20 hidden built-ins, removed `/new -> /clear`, made hidden unsupported dispatch non-mutating and explicit, and removed hidden TUI bypasses. |
| P16.2 Canonical command contract | Installed one typed entrypoint/availability record, one strict context-aware dispatch boundary, normalized collision rejection, immutable filtered discovery, and explicit TUI-local command scope. |
| P16.3 Single action owner and typed projection | Centralized engine-owned runtime/session mutations in one executor, made handlers return validated intents, and projected source-bound typed outcomes through every supported renderer and replay. |
| P16.4a1 Durable core session lifecycle | Added append-only lifecycle commits for true new, clear, compact, and direct resume; restart restores the selected active context while preserving prior transcript audit records. |
| P16.4a2 Consolidated session service | Added canonical `/sessions`, routed discovery/resume/rename/export and the TUI picker through one source-preserving service, and replaced the TUI-local exporter with atomic persisted export. |
| P16.4b Durable fork lifecycle | Added one idempotent engine-owned child commit with complete state/lineage, synced no-clobber visibility, activate-after-commit compensation, and ACP restore-before-handle registration. |
| P16.5a Unified execution and safety controls | Added contextual model/effort capability admission, one engine-owned model/effort/plan/permission mutation boundary, explicit bypass confirmation, and TUI/plain/headless/ACP effective-state projection. |
| P16.5b Truthful diagnostics | Added one four-state engine snapshot for status/context/usage/config/doctor, cumulative provider-usage coverage, secret-safe provider/config checks, portable entrypoint projection, and a provider-only TUI status without generic prices. |
| P16.5c Workspace and terminal convergence | Added engine-owned Git/root/file boundaries, capability-gated committed copy, project-native permission-mediated workflows, and explicit TUI-only discovery. |
| P16.5d Extensibility inspection | Added one detached runtime inspection snapshot and atomic configured plugin command generations with source health, collisions, and retained-failure diagnostics. |
| P16.6 Bundled workflow commands | Moved seven prompt workflows into a versioned embedded pack sharing one attributed, collision-safe, atomic generation with configured plugins. |
| P16.7a CLI foundations | Added explicit exec, protocol-local flags, one text/JSON stdout owner, stable exits/cancellation, shared build identity, shell completion, and safe default MCP diagnostics without another runtime owner. |
| P16.7b Sessions CLI projection | Added provider-free list/resume/rename/export/fork administration over the engine-owned session service, preserved durable fork compensation, removed superseded hidden shortcuts, and kept archive/delete absent. |
| P16.7c Diagnostics and extension CLI projection | Added provider-free config/doctor, configured/unprobed MCP inspection, non-mutating plugin validation, and inspection-process-local atomic reload over existing owners while keeping unsupported mutations absent. |
| P17.H0 Fail-closed Plan admission | Added one model/runtime Plan capability decision, exact non-symlink plan-file identity, inactive Exit containment, and truthful semantic-prompt output. |
| P17.0 Engine-owned Plan state | Added one serialized QueryEngine Plan state, result-before-transition publication, safe-boundary tool refresh, reducer replay, and shared Legacy/Graph transition consumption. |
| P17.1 Structured Exit approval | Added exact typed request/revision settlement, explicit target modes across TUI/plain/ACP, cancellation-first exactly-once behavior, and shared Legacy/Graph result commit. |
| P17.2 Durable Plan replay | Added versioned phase/file/revision checkpoints, cold approval normalization, exact live-owner reprojection, restored tool/compaction capability, and real ACP Resume/Load convergence. |
| P18.H0 Process-global worktree containment | Removed Enter/ExitWorktree from all default registries, reserved both names against re-registration, rejected old calls before execution, and retained side-effect-free compatibility stubs plus Agent isolation. |
| P18.0 Durable worktree lifecycle service | Added versioned project-local ownership, context-aware bounded Git, strict identity/ref/base admission, non-force cleanup, raced-commit restoration, and reducer-only replay. |
| P18.1 Agent worktree binding and handoff | Bound Agent launch/cleanup to the engine service, added engine-scoped tool/shell CWD, explicit dirty-source admission, stable memory/transcript owners, and durable bounded retained-work handoff. |

The completed detailed P15 contract is retained in
[`p15-terminal-output-resilience.md`](runtime/p15-terminal-output-resilience.md).
The completed Plan Mode contracts and rollback boundaries are retained in
[`p17-plan-mode-runtime.md`](runtime/p17-plan-mode-runtime.md); delivery
evidence is summarized in
[`post-parity.md`](runtime/post-parity.md#p172-persistence-replay-and-entrypoint-convergence).

## TUI history

TUI M0-M7 modernization, P19 slice history, the completed P27
selection/clipboard program, and the G24/G27 presentation-truth repairs are
indexed separately in
[`migration/history/tui/README.md`](tui/README.md). The 2026-07-25
dirty-worktree recovery records the audited recovery of missing runtime/TUI
behavior and G9 evidence without replacing evolved P19/P20 owners.

## Reference evidence

Detailed source-backed comparison for closed slices is in
[`../reference/`](../reference/):

- P7: [`migration/reference/parity/permission-filesystem.md`](../reference/parity/permission-filesystem.md) and [`migration/reference/parity/permission-classifier.md`](../reference/parity/permission-classifier.md)
- P8: [`migration/reference/parity/team-private-memory.md`](../reference/parity/team-private-memory.md), [`migration/reference/parity/agent-memory-snapshot.md`](../reference/parity/agent-memory-snapshot.md), and [`migration/reference/parity/pdf-read.md`](../reference/parity/pdf-read.md)
- P9.2: [`migration/reference/parity/permission-coalescing.md`](../reference/parity/permission-coalescing.md)
- P10: [`migration/reference/runtime/trace-session-workflow-convergence-audit.md`](../reference/runtime/trace-session-workflow-convergence-audit.md)
- P11/P12: [`migration/reference/runtime/module-criticality-and-slimming.md`](../reference/runtime/module-criticality-and-slimming.md)

## Current replacements

Historical numbers are closeout snapshots only. Current architecture and
contracts are in:

- [`architecture/README.md`](../../architecture/README.md)
- [`architecture/runtime/query-engine.md`](../../architecture/runtime/query-engine.md)
- [`architecture/runtime/tasks-and-agents.md`](../../architecture/runtime/tasks-and-agents.md)
- [`architecture/tui/README.md`](../../architecture/tui/README.md)
