# Permissions

**Status:** current
**Last verified:** 2026-08-19

> **Ownership:** QueryEngine permission coordinator; `engine/permission` primitives

## Current Permission Pipeline

Permissions are invocation policy, not merely tool visibility. QueryEngine
wraps the transport callback and coordinates rules, modes, scoped approvals,
working-directory access, auto classification, and interactive prompting. The
canonical check occurs after pre-tool hooks and before execution.

Plan phase has a stricter owner than the generic permission modes.
`QueryEngine.PlanState` is authoritative for Inactive, Active, and
AwaitingApproval state, exact plan identity, return-mode context, live approval
request, initial exact-byte digest, and revision. Active-turn `ToolUseContext`,
session metadata, runtime reducer state, ACP, and TUI are snapshots or
projections. Tool and external transitions use the same serialized engine
path; an external change fails without mutation while a turn or
AwaitingApproval owns the boundary.

## Decision order

1. Reject tools excluded by explicit built-in selection.
2. Enforce the central Plan capability boundary. Explicit allow rules, bypass,
   approvals, safe-path shortcuts, and a misleading `IsReadOnly` flag cannot
   admit a capability that Active Plan Mode excludes.
3. Route Active `ExitPlanMode` through one exact typed approval before generic
   rules, grants, hook/yolo shortcuts, or positive permission coalescing.
4. Evaluate explicit rule denials. A registered Write/Edit alias also checks
   the canonical tool-name denial; neither spelling can bypass a deny.
5. Allow a runtime-validated exact Plan-file Write/Edit capability. This
   typed decision is issued only after active phase, canonical registered tool
   identity, absolute clean exact session/Agent path, and symlink containment
   succeed. It bypasses ordinary allow/ask, modes, grants, prompting,
   classifier, coalescing, and denial tracking.
6. Before any positive rule, grant, Bypass, classifier, reviewer, or
   coalescing authority, recognize the narrow literal critical Bash subset.
   A match requires a fresh engine-owned `AllowOnce`; DontAsk denies it
   without prompting. Explicit deny remains authoritative ahead of this guard.
7. Evaluate ordinary explicit `ask` and `allow` rules for non-capability
   operations. Outside Auto, existing allow compatibility remains. In Auto, an
   allow can become deterministic authority only when the winning local/user
   rule is exact for the canonical action and input; project, tool-wide, or
   wildcard allows fall through. This is explicit human authority, not the
   P51.2 automatic proof-bound path, and it never receives that admission
   marker.
8. Apply bypass mode behavior for non-critical invocations.
9. Auto-allow a registry contract explicitly marked safe from interactive
   permission, currently TodoWrite's built-in host-owned runtime-state update.
10. Apply don't-ask behavior to every still-unmatched invocation.
11. Check typed root-session approvals. Auto accepts exact command/path/input
    identity or a contained Read/Grep/Glob root scope, not broad text matching.
12. Apply safe memory/working-directory reads and the bounded memory,
    accept-edits, and plan-file write contracts.
13. In Auto, admit an exact non-critical canonical built-in Bash action when
    it carries the complete available Darwin Seatbelt Guest proof. This path
    skips only the ordinary permission prompt and classifier.
14. In Auto, route remaining missing capability facts, MCP/app/dynamic origins, Agent/
    child, network, user-interaction, and incompletely represented shell
    actions to a person before classifier work. Without an interactive adapter,
    this is a stable denial.
15. Run the existing primary-model classifier only for a remaining complete
    built-in action while denial limits permit.
16. On supported entrypoints, prompt through the shared coordinator or use the
    installed fail-closed callback when interaction is unavailable.

The execution boundary adapts this result through `permission.Checker` and
turns any unresolved `ask` into a denial. A pre-tool hook `allow` can skip the
interactive prompt but cannot bypass explicit deny rules. Pre-tool
stop/deny/permission-deny runs before capability use. Hook input is validated
before policy. One permission result may replace input once; QueryEngine then
rebuilds the registered descriptor and rechecks selection, validation, Plan,
deny, rules, grants, mode, and Auto before committing an exact session/always
grant. Only the final canonical JSON is accounted and dispatched.
If an interactive result rewrites an ordinary Bash request into the critical
corpus, settlement denies it before committing a session or persistent grant;
the critical final invocation needs a new live constrained request.

Critical requests carry an additive `allow_once_only` decision constraint.
ProjectGraph includes it in request/decision identity and durable replay;
Plain, TUI, and ACP project only permitted choices, while engine settlement
independently rejects a forged persistent result. The zero constraint preserves
all pre-P51.2 requests.

The settled descriptor binds requested/canonical tool identity, registry
capability generation, detached input, resolved path/root facts, CWD and
working roots, session/Agent/entrypoint, Plan identity, and effective-policy
identity. Policy or action drift rejects settlement. Resolved-path drift,
including a symlink ancestor replacement, rejects dispatch. A registry read
lease linearizes the final canonical/generation check with dispatch so a
concurrent disable, unregister, or implementation replacement cannot cross the
authorized boundary.

ProjectGraph resumes ordinary decisions from one immutable base policy
revision through a batch-local settlement chain. The batch serializes action
rebuild and settlement. After the live revision check, the rebuilt action must
still carry the chain's current revision before settlement can persist a grant;
a mismatch invalidates the batch. The chain then advances only to the exact
post-policy revision owned by a successful settled action. This lets distinct
exact `allow_always` decisions reach the existing settings writer without
accepting an arbitrary policy change. External or unexplained revision drift
expires the current and remaining decisions; Plan approval remains bound to
its original revision. Final action/path/registry validation still occurs
after the chain.

The fail-closed statement above is an entrypoint contract, not a zero-config
library guarantee. A caller that constructs `QueryEngineConfig` with both
`CanUseTool` and `PermissionPrompt` unset installs no invocation-level
permission check; embedded callers must supply an explicit policy callback.
P22.1a records this as the `NoInvocationPolicyInstalled` caller-authoritative
boundary, outside authorization decisions; it is not an implicit allow.

The internal QueryEngine seam now projects the existing branches without
changing their result or order:

| Decision | Existing branch family |
|---|---|
| `Allow` | Exact Plan-file capability, explicit allow, bypass, registry safe defaults, scoped grants, contained reads/writes, and current Auto safe fast paths. |
| `Deny` | Tool selection, Plan hard denial, explicit deny, and non-interactive `dontAsk` refusal. |
| `RequireHuman` | Exact Exit approval, explicit ask, and otherwise unmatched prompt or external-callback handling. |
| `Review` | The existing Auto classifier-eligible path, including its current allow, deny, and error-to-prompt behavior. |

Pre-tool stop, `DenyReason`, and hook permission denial remain outer execution
guards before this seam. `wrapCanUseTool` still exposes the same bool/reason
callback, so the projection adds no new authority and no user-visible outcome.

QueryEngine also freezes one deterministic effective-policy snapshot for
ProjectGraph drift detection. Its identity covers rules with provenance,
canonical scoped grants, effective mode, Plan phase/revision/file identity,
root session, CWD and additional roots, and tool selection. Capability
generation, reviewer-policy version, and child scope use a fixed
omitted/unpopulated representation. The canonical encoding preserves the
pre-P22.1a ProjectGraph revision bytes for unchanged policy, so pending
persisted HITL decisions remain valid across the upgrade. ProjectGraph derives
its policy revision from this snapshot instead of maintaining a second hash
owner.

## Modes

| Mode | Unmatched invocation behavior |
|---|---|
| `default` | Prompt when no safe fast path applies. |
| `plan` | Only the explicit exploration/clarification capability set, TodoWrite, exact session/Agent plan-file Write/Edit, and Exit are admitted. |
| `acceptEdits` | Write/Edit inside allowed roots are auto-approved. Bash has no command-name fast path and returns to the existing interactive prompt or fail-closed callback boundary. |
| `bypassPermissions` | Auto-allow after explicit rule handling, except the narrow critical Bash subset still requires live `AllowOnce`. |
| `dontAsk` | Deny instead of prompting. |
| `auto` | Apply exact user authority and typed contained/default-safe paths; ordinary canonical Bash may also use the complete Guest-proof path. Remaining incomplete shell, Agent/child, network, MCP/app/dynamic, and user-interaction actions require a person. Complete built-ins may use the primary model classifier. Model error/uncertainty and denial thresholds fall back to prompting. G14 limits the current reviewer claim. |
| `bubble` | Surface child-agent prompts to the parent interaction. |

`bubble` is an internal child-to-parent coordination mode and is not accepted
as a user-selected mode. `auto` is addressable through settings, the runtime
flag value, session restore, and the typed slash command even though the
legacy `ValidModes` helper/comment still describes it as internal. The
canonical runtime control schema is:

```text
/permissions
/permissions mode <default|plan|acceptEdits|dontAsk|auto>
/permissions bypass confirm
/permissions rules list
/permissions rules add <allow|ask|deny> <rule> [--local|--project|--user]
/permissions rules remove <allow|ask|deny> <rule> [--local|--project|--user]
```

`/mode`, top-level `/bypass`, and `/yolo` remain hidden and unavailable. Bypass
requires the exact confirmation token; `SetPermissionMode`, ACP protocol mode
selection, and malformed slash commands cannot enter it. The TUI confirmation
dialog and the command executor use the same explicitly confirmed engine
method. Direct TUI/ACP transitions fail without mutation while a turn owns the
serialized boundary, while the admitted slash command may mutate only for its
exact owning command turn. ACP projects successful transitions as a current
mode update.

Entering Plan preserves the complete previous non-Plan mode, including
user-addressable `auto` and internal `bubble` contexts. An explicit idle user
abandon restores that exact mode; the requested external target cannot smuggle
a different post-Plan permission mode. Model-initiated Exit remains a typed
approval.

The TUI's Shift+Tab interaction is a separate user execution-control path:
Default enters Plan; Plan opens the bypass-risk dialog without leaving Plan;
an explicit confirmation commits `user_confirmed` Plan → Bypass; cancel keeps
Plan; and Bypass returns to Default. The confirmed path still loses to an
active turn or AwaitingApproval owner. It cannot be invoked by a model tool and
does not replace reviewed-byte `ExitPlanMode` approval.

## Current Auto implementation and safety boundary

The authoritative Auto path remains synchronous and shares the turn context:

1. QueryEngine parses and validates one detached input, resolves the canonical
   registry identity, and constructs the host-owned action/capability
   descriptor before deterministic policy.
2. Exact local/user rules, exact typed session grants, contained
   Read/Grep/Glob, contained Write/Edit, and explicitly declared built-in
   internal-state defaults may admit their bounded action. An exact local/user
   rule is independent explicit authority; it does not assert or receive
   proof-bound admission. Duplicate name-only production allowlists no longer
   authorize Auto.
3. A non-critical canonical built-in Bash action skips the ordinary prompt
   through P51.2's automatic path only when its exact descriptor carries the
   complete available Darwin Guest proof. Incomplete or unavailable proof
   falls through; aggregate `degraded` state alone is not authority.
4. The narrow literal critical `rm`/`rmdir` corpus always requires one fresh
   `AllowOnce`. Exact rules, session/always responses, grants, Bypass,
   classifier, reviewer, and coalescing cannot authorize it; DontAsk denies.
5. Other incomplete shell actions, Agent/child, WebFetch/WebSearch,
   MCP/app/dynamic, network, and user-interaction actions require a person
   unless exact user authority covers the current action.
6. A remaining complete built-in reaches the same primary `ChatModel`, which
   receives raw tool input and the last five non-empty message contents,
   including assistant or tool-role prose. The call has a 256-token output
   budget but no reviewer-specific model route or deadline.
7. After complete or unterminated thinking sections are stripped, exactly one
   detectable `<allow/>` or `<block/>` tag selects allow or deny. Empty,
   malformed, or ambiguous output denies.
8. Model error or `ask` clears classifier status and reaches the existing
   prompt. After 3 consecutive or 20 total denials, interactive Auto falls back
   to prompting; a prompt-unavailable entrypoint denies.

P22.2a adds a second, explicitly opt-in shadow beside that path:

1. Only a remaining complete main-agent built-in that reaches the legacy
   classifier is eligible. Human-required shell, Agent/child, network,
   MCP/app/dynamic, direct-interaction, and ProjectGraph-probe paths do not
   start a reviewer.
2. QueryEngine projects one `permission_review_v1` request from the detached
   canonical action, host policy facts, and at most the last three process-local
   public user-submission presence records owned by QueryEngine. User text is
   never forwarded. Typed action leaves retain only bounded shape, byte-count,
   relative/root path labels, or a secret-redaction marker;
   historical/synthetic user-role messages, assistant, tool, repository, web,
   MCP, and child evidence are excluded.
3. A provider-owned factory constructs the explicitly named reviewer
   provider/model with its own deadline. It does not fall back to the actor
   model or consume generic actor `PROV_*` routing values.
4. The reviewer must return one strict bounded JSON result. QueryEngine accepts
   it only while request, tool call, nonce, canonical action, action digest,
   effective policy, registry generation, roots, runtime identity, and
   data-boundary version still match a freshly rebuilt descriptor.
5. Checking, completed, and unavailable events carry bounded status only.
   Pending requests and results are process-local and are cancelled and joined
   at engine close; they are neither checkpointed nor replayed.

The shadow never changes deterministic policy, the legacy classifier result,
prompting, grant/rule persistence, denial accounting, or dispatch. TUI, plain,
headless, and ACP project only bounded advisory status; none presents reviewer
approval controls.

P22.2b adds a separately opted-in local measurement consumer:

1. QueryEngine creates a fresh 32-hex audit event ID after one exact action
   becomes reviewer-eligible. The request ID, tool-call ID, binding nonce,
   action/policy digest, raw input, absolute path, rationale, credentials,
   transcript, Session, Agent, and CWD remain process-local or absent.
2. Typed `eligible`, `attempt`, `terminal`, comparison, and dispatcher
   diagnostic records correlate the validated shadow lifecycle. Each
   QueryEngine with an audit sink owns one capacity-128 single-writer queue;
   permission and reviewer paths only attempt a non-blocking enqueue. Queue
   pressure, sink latency, errors, and panics cannot delay or change a
   permission result.
3. A human label is recorded only for the direct structured adapter response
   on the same unchanged settled action. Coalesced, context, invalid-response,
   fail-closed rewrite, binding-drift, and changed-input paths are excluded.
   The runtime can record the comparable legacy classifier result but never
   fabricates versioned-corpus truth.
4. CLI, plain, headless, and ACP runtime flags reuse one secure local store per
   runtime owner. ACP shares one Agent-owned store across sessions. Standalone
   MCP and child reviewers remain outside P22.
5. The store uses a pinned `0700` directory root, identity-checked `0600`
   segment and coordination-file handles, and an OS-locked O_EXCL sentinel with
   bounded stale recovery. It owns 1 MiB segments, eight retained segment
   names, size-window rotation, strict non-null typed JSONL decoding, visible
   corruption/partial-tail counters, and typed tail recovery. It makes no
   age-retention claim.
6. Engine close first cancels and joins reviewer producers, closes audit
   admission, and waits at most 250ms for that queue to drain. A sink that
   ignores context may leave its one writer goroutine blocked after engine
   close, because Go cannot forcibly stop it; it cannot hold permission or
   engine shutdown. Full-queue drops, sink failures, flush expiry, and
   enqueue-after-close attempts remain available in the dispatcher in-memory
   snapshot. The writer coalesces typed deltas into the same journal when the
   sink can recover, but no durable record is claimed when that sink remains
   unavailable.
7. The provider-free administration command reports `no_data`,
   `retained_window`, or `partial`, separate source denominators, nearest-rank
   p50/p95 latency, unavailable/escalation/disagreement totals, and every
   retained false allow. Reviewer latency admits only retained event groups
   that contain both an `attempt` and a `terminal` record. A terminal-only
   setup or projection failure remains an outcome and lifecycle diagnostic but
   contributes no latency sample; with no retained pair, latency is unavailable
   and the percentile fields are omitted. Any retained dispatcher diagnostic
   makes the report partial without reconstructing missing lifecycle records.
   Delete requires explicit confirmation and removes only owned segment names
   without revealing the configured path.

The measurement store is not a transcript, approval ledger, telemetry
uploader, policy input, or enforcement gate. Missing, corrupt, rotated, or
disabled evidence is reported as unavailable/partial retained-window evidence,
never as a zero-error claim.

The authoritative path still has useful fail-closed output parsing and
lifecycle events, but Auto is not yet a separated model-review enforcement
boundary:

- P22.H0 closed G13 by deleting the prefix-only Bash fast path from both
  `acceptEdits` and `auto`.
- P22.1b closed the missing canonical-action, capability, rewrite, exact-grant,
  and dispatch-binding boundaries.
- P22.2a supplies a minimized versioned projection, separately routed reviewer,
  strict result/deadline, and exact process-local shadow binding.
- P22.2b supplies bounded local redacted measurement and deterministic
  aggregation, but deliberately adds no enforcement, measured promotion
  threshold, promoted default, or classifier replacement, so G14 remains
  open.
- The package's `Evaluator`, `ToolRiskClassifier`, speculative classifier, and
  cache primitives are not the production authority and do not close G14.
- Selecting Auto does not establish or broaden an OS sandbox. P51.1 separately
  defaults Darwin Guest Bash to a real Seatbelt `workspace-write` binding for
  every permission mode, including bypass. Shell hooks and configured stdio
  MCP remain ambient; unsupported or failed Guest enforcement fails before
  spawn. P51.2 Core consumes only the complete exact Guest proof for ordinary
  canonical Bash; it does not infer safety from the `degraded` aggregate.

The accepted policy-first target and ordered repair are
[`P22 Auto Permission Review`](../../migration/plans/p22-auto-permission-review.md);
the dated cross-agent evidence is the
[`Auto Permission audit`](../../migration/reference/runtime/auto-permission-review-audit.md).
P22.H0 removed the command-prefix Bash bypass. P51.2 adds a different,
proof-bound automatic path: canonical input, registry generation,
policy/binding digest, adapter generation, exact adapter/runtime axes, and root
identity are compared again immediately before acquisition and submission.
Drift returns `sandbox_binding_expired`. Unknown shell syntax is a
critical-classifier non-match and follows the ordinary proof/fallback path
rather than becoming a new denial. Exact local/user rules remain a separate
explicit authorization path; critical Bash is the deliberate exception and
cannot use them.
P22.1a delivery evidence is retained in the
[`permission decision snapshot record`](../../migration/history/runtime/p22-1a-permission-decision-snapshot.md).
P22.1b delivery evidence is retained in the
[`canonical action policy record`](../../migration/history/runtime/p22-1b-canonical-action-policy.md).
P22.2a delivery evidence is retained in the
[`permission reviewer shadow record`](../../migration/history/runtime/p22-2a-permission-review-shadow.md).
P22.2b delivery evidence is retained in the
[`permission review audit record`](../../migration/history/runtime/p22-2b-permission-review-audit.md).

## Plan approval entrypoints

All conversation entrypoints share the ProjectGraph interrupt and engine
settlement owner. Engine request/revision/path/digest checks remain canonical,
and every interactive adapter now supplies explicit two-step bypass evidence:

| Entry point | Plan interaction contract |
|---|---|
| TUI | `EventPermissionRequest` enters the owner-thread `PlanDialog`; only a submitted terminal Plan result becomes the targeted permission `RuntimeItem`. Bypass selection renders a distinct risk frame with explicit No/Yes choices and defaults to No; only a second explicit Yes sets `Confirmed=true`. No/Esc returns to Actions with selection, viewport, draft, cursor, and undo intact; Actions/Review Esc and force-close emit typed `Cancel` with empty feedback. The resumed Graph then revalidates request, revision, exact file, reviewed digest, target, and confirmation. |
| Plain REPL | The event driver renders the same exact Plan bytes, exposes one action per previous/accept-edits/bypass target, and continues the same turn after targeted settlement. Every bypass target—including a previous bypass mode—opens a second prompt; only exact `BYPASS` confirms it, a negative answer returns to targets, and input loss emits typed `Cancel`. A resumed pending request is handled before a new prompt. |
| Headless `exec` / `--print` | No interactive prompt is installed. A Plan exit that needs interaction ends at `waiting_input`/fail-closed handling; bypass mode cannot fabricate approval. |
| ACP | The first protocol request exposes unique target modes. Selecting any bypass target issues a second Confirm/Back request; Back reissues the original targets. Initial choice, Back retry, and bypass confirmation all reuse the engine `ToolUseID` already visible in the invocation's start and single terminal update. All rounds reuse one absolute deadline, while Plan RequestID/revision/reviewed digest remain distinct engine identities. A blank Plan tool ID fails before snapshot I/O or client permission I/O. Timeout, cancellation, transport loss, missing connection, unknown response, and incomplete confirmation emit typed `Cancel`/deny without a single-action fallback; the targeted item resumes the same Graph turn only after settlement. |
| Standalone MCP server | This surface has no QueryEngine or interactive Plan owner, so tools marked `IsPlanModeTransition` are not registered. |

Adapters emit `Approve`, `Revise`, or `Cancel` intent. Exact engine settlement
alone adds the process-local, request-bound capability consumed by
`ExitPlanMode`; raw typed data, replayed JSON, a generic allow, and a decision
for another tool-use ID cannot pass the execution gate. Non-bypass adapters
keep `Confirmed=false`; only the completed second bypass action sets it true.
G10 is closed after final-cell cursor repair plus the consolidated
cross-entrypoint, recovery, permission-race, and repeated external-editor PTY
matrix; see the
[`P20 closeout`](../../migration/history/runtime/p20-r3-plan-interaction-closeout.md).
The deprecated `Approved` field remains accepted only as a one-release legacy
input for unchanged initial bytes. Current adapters never emit it,
normalization clears it, and serialized canonical results omit it. Plan
outcomes never create session or always-allow grants.

## Invariants and edge cases

- Approvals are scoped by tool plus exact command/path/input fingerprint and
  root session; contained Read/Grep/Glob may retain an explicit recursive root
  scope. A bare tool name is not a persistent invocation approval in Auto.
- Settings-file rule add/remove operations serialize their process-local
  read-merge-write cycle, so writers that reach it merge distinct rules.
  ProjectGraph additionally serializes one resume batch's action rebuilds and
  requires each rebuilt action to retain the chain's checked revision before
  settlement. It advances only through grant-owned policy revisions; external
  drift expires the current and later decisions. This is not a global policy
  lock or cross-process file-lock claim.
- Permission input may be changed by a pre-tool hook or interactive decision;
  the encoded input executed by the tool must reflect that update. The
  permission result may rewrite once, and the final descriptor re-enters the
  complete policy cycle before an exact grant is committed.
- Plan projection may expose Write/Edit while Active, but projection never
  issues an execution capability. Only the runtime decision for the exact
  current Plan identity carries that class.
- Path checks resolve roots and symlinks; do not replace them with string-prefix
  checks. The resolved representations and containment result remain bound
  through dispatch, so replacing an approved ancestor with a symlink fails
  closed.
- A worktree-isolated child loads project rules from its worktree CWD and uses
  that worktree as `PermissionProjectRoot`. The parent prompt adapter and root
  session identity may be shared, but they do not widen child filesystem
  containment back to the source checkout.
- Semantic `allowedPrompts` on Exit are requested implementation descriptions,
  not permission rules or durable grants.
- Exit approval is a typed request/revision exchange. Active becomes
  AwaitingApproval before presentation; every terminal claim returns to Active.
  The canonical outcomes are Approve, Revise, and Cancel. Only Approve whose
  target and reviewed bytes validate, and whose bypass target is explicitly
  confirmed, may let the canonical Tool result later commit Inactive. Revise
  returns feedback while Plan remains Active; Cancel carries no implementation
  authority.
- An active `ExitPlanMode` prompt is routed directly to typed Plan settlement.
  Revise feedback does not append generic denial history or increment generic
  tool-denial counters; those records describe ordinary permission rejection,
  not a request to continue planning.
- The request snapshots `InitialPlanDigest`, and each supported adapter reports
  `ReviewedPlanDigest` for the exact bytes it rendered or reloaded. Both use
  `sha256:<lowercase hex>`. Settlement re-reads the exact path and rejects a
  mismatch as stale. TUI, plain, ACP, ProjectGraph HITL, runtime replay, and
  the additive version-1 checkpoint preserve the same request identity.
- Generic allow/session/always decisions cannot approve Exit, and Plan requests
  are not positive-coalescing candidates. Raw `Approve` is adapter intent, not
  execution authority; engine settlement binds the capability to the exact
  request. A legacy bool approval can approve only unchanged initial bytes
  during its compatibility window.
- [`PlanApprovalTargetModes`](../../../engine/permission_interaction.go)
  is the shared unique-target owner for TUI, plain, and ACP. It does not grant
  a mode; each adapter still has to produce an explicit terminal Plan result.
- A successful Enter/Exit changes the authoritative Plan phase only after its
  canonical Tool result is accepted. Its typed state event is published after
  that result, and the next model-visible surface refresh occurs only between
  completed rounds.
- Failed, denied, cancelled, repeated, stale-owner, or losing external tool
  transitions do not leave Plan Mode. Approval request/result transitions do
  advance the revision while returning the phase to Active; they never grant
  the eventual Exit by themselves.
- Headless mode has no interactive prompt. Explicit allow rules, scoped
  approvals, safe read/write paths, and mode-specific fast paths still apply;
  only an invocation that reaches the prompt-required fallback is denied.
- TodoWrite is explicitly default-allowed but is not read-only: QueryEngine
  routes its trusted `(SessionID, AgentID)` partition to the durable WorkBoard
  runtime-state owner. Default admission requires declared built-in capability
  metadata and the registry's explicit default-allow contract. Explicit deny
  and ask rules still win, and the independent MCP server's strict read-only
  mode still rejects it.
- The independent MCP server has its own `MCP_PERMISSION_MODE` and bypasses this
  coordinator; see [`mcp.md`](mcp.md).
- Rule mutations validate one quote-aware rule value and one explicit scope
  before persistence; unknown option-looking arguments are rejected rather
  than treated as rule text.

## Code references

- [`QueryEngine.wrapCanUseTool`](../../../engine/engine.go)
- [Canonical permission action](../../../engine/permission_action.go)
- [Registry capability and execution lease](../../../tools/registry.go)
- [`PlanState` and serialized transitions](../../../engine/plan_state.go)
- [Exact Plan review digest](../../../engine/plan_digest.go)
- [Central Plan tool policy](../../../engine/plan_tool_policy.go)
- [`QueryEngine.promptForTool`](../../../engine/engine.go)
- [`normalizePlanApprovalDecision`](../../../engine/plan_state.go)
- [`planApprovalAllowsExit`](../../../engine/tool_execution.go)
- [Plain ProjectGraph event driver](../../../cmd/yhc/cmd/root.go)
- [ACP ProjectGraph permission resolver](../../../server/acp/agent.go)
- [ACP Plan permission identity adapter](../../../server/acp/agent.go)
- [ProjectGraph permission settlement chain](../../../engine/graph_hitl.go)
- [`Mode`](../../../engine/permission/mode.go)
- [`RulesEngine.EvaluateMatch`](../../../engine/permission/rules.go)
- [`PersistPermissionRules`](../../../engine/permission/persist.go)
- [`ApprovalTracker.IsApprovedInvocation`](../../../engine/permission/approvals.go)
- [`Checker.Check`](../../../engine/permission/check.go)
- [`AcceptEditsCheck`](../../../engine/permission/accept_edits.go)
- [`ClassifyToolUse`](../../../engine/permission/classifier.go)
- [`extractRecentContext`](../../../engine/permission/classifier.go)
- [Reviewer request, projection, and result contract](../../../engine/permission/reviewer.go)
- [QueryEngine reviewer shadow owner](../../../engine/permission_review.go)
- [Separate reviewer provider factory](../../../engine/provider/reviewer.go)
- [`DenialTrackingState.ShouldFallbackToPrompting`](../../../engine/permission/denial_tracking.go)
- [`executeToolCall` permission seam](../../../engine/tool_execution.go)
- [`TodoWriteTool` scoped state](../../../tools/todo_write.go)
- [`SubAgentExecutor.ExecuteAgent` worktree permission root](../../../engine/subagent.go)
- [Guest execution binding matrix](../../../engine/execution_policy.go)
- [Immutable containment binding and proof](../../../engine/containment/binding.go)
- [`QueryEngine.SetPermissionModeConfirmed`](../../../engine/execution_controls.go)
- [Typed `/permissions` schema](../../../engine/commands/cmd_permissions.go)
- [ACP mode and configuration projection](../../../server/acp/agent.go)

## Related tracking

Keep migration decisions in [`PLAN.md`](../../migration/PLAN.md) and reference
evidence in [`migration/reference/`](../../migration/reference/README.md).
