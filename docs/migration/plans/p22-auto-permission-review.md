# P22 Auto Permission Review

**Status:** historical
**Current execution:** P22.H0-P22.2b complete; P22.1c and P22.3a-P22.6 deferred
**Created:** 2026-07-26
**Last updated:** 2026-08-01

> **Ownership:** completed P22 core contract, historical slice decisions,
> invariants, entrypoint behavior, future re-entry gates, and rollback
> boundaries for policy-first model review. Root
> [`migration/PLAN.md`](../PLAN.md) alone owns executable order and state.

Comparative evidence is frozen in
[`auto-permission-review-audit.md`](../reference/runtime/auto-permission-review-audit.md).
Current behavior belongs in
[`permissions.md`](../../architecture/capabilities/permissions.md); unresolved
evidence remains in [`REMAINING.md`](../REMAINING.md).

## User outcome

In `auto` mode, routine actions proceed without unnecessary prompts while the
system still:

- enforces explicit deny, protected resources, Plan containment, sandbox
  boundaries, and human-required actions deterministically;
- asks a separate reviewer model only about one immutable, reviewable action;
- binds approval to the exact action and current policy;
- never executes because the reviewer timed out, failed, returned malformed
  output, or saw injected instructions; and
- explains denials and safe alternatives without teaching the actor how to
  circumvent policy.

The product promise is reduced prompt fatigue under a smaller deterministic
authority envelope. It is not autonomous access to every installed tool.

## Decision

P22 is `combine`:

- `preserve` QueryEngine as the single permission coordinator, explicit rule
  precedence, Plan capability/approval, scoped grants, and supported
  interaction adapters;
- `adapt` Claude Code's dedicated classifier and trust-separated context;
- `adapt` Codex Auto-review's reviewer-swap boundary, exact request,
  structured response, timeout, and sandbox separation;
- `preserve` deterministic allow/ask/deny policy from OpenCode/Copilot; and
- use a `project-native` action descriptor, policy snapshot identity,
  process-local binding, redacted audit vocabulary, and staged promotion.

P22 rejects a second runtime permission owner. A pure policy evaluator and a
reviewer adapter are dependencies of `QueryEngine.wrapCanUseTool`, not parallel
coordinators.

## Scope and non-goals

P22 owns:

- current `acceptEdits` and `auto` fast-path safety;
- canonical permission action description and deterministic decision class;
- one optional separate approval-reviewer interface and model adapter;
- a data-minimized reviewer projection, structured decision parsing, exact
  process-local binding, and revalidation;
- denial, escalation, timeout, cancellation, and one-shot override semantics;
- TUI, plain, headless, ACP, and child-agent projection;
- security evaluation, shadow mode, rollout gates, and old-owner cleanup; and
- architecture, guide, status, gap, reference, plan, and history
  synchronization.

P22 does not:

- make `bypassPermissions` part of Auto;
- create an OS sandbox or claim one exists;
- make standalone MCP inherit QueryEngine permission behavior;
- let project instructions, assistant prose, tool results, web content, or MCP
  metadata become user authority;
- redesign Plan Mode or its exact approval capability;
- persist model approvals as session/always rules;
- add broad decision caching or speculative execution;
- guarantee safety from classifier quality alone; or
- copy Anthropic/OpenAI private prompts or provider-specific wire formats.

## P22.1a completed boundary

**State:** completed 2026-07-27

**Delivery:** [`p22-1a-permission-decision-snapshot.md`](../history/runtime/p22-1a-permission-decision-snapshot.md)

P22.1a answered one narrow question: the existing QueryEngine invocation
policy can expose one stable decision seam and one shared immutable policy
identity without altering what a user sees after P22.H0. The delivered
boundary is behavior-preserving; it is not reviewer enforcement.

### Frozen outcome and boundary

- `QueryEngine` remains the only user-visible invocation-policy coordinator.
  Its existing ordering and legacy classifier continue to produce the same
  actual Allow/Deny/prompt/classifier results as P22.H0.
- The seam projects existing branches into exactly `Allow`, `Deny`,
  `RequireHuman`, or `Review`. P22.1a delegates `Review` to the legacy
  classifier; it does not install a separate reviewer, alter classifier
  parsing, tune allowlists, or delete `Evaluator`, classifier, or allowlist
  code.
- `projectGraphPolicyRevision` becomes a derived identity of the same immutable
  QueryEngine snapshot, not a second counter or independently maintained hash.
  ProjectGraph does not become a policy coordinator.
- Direct library construction with both `CanUseTool` and `PermissionPrompt`
  nil remains the caller-authoritative trust boundary. Record it as
  `CallerAuthoritative` / `NoInvocationPolicyInstalled`, outside the four
  authorization decisions. TUI, plain, headless, ACP, and child composition
  roots must each prove they do not enter that state.

The projection is a characterization of current behavior, not a new ordering.
The first row remains an outer execution guard and does not move hook
authority into the QueryEngine seam:

| Current branch or outer guard | P22.1a projection | Compatibility requirement |
|---|---|---|
| Pre-tool hook `DenyReason` or `HookPermissionDeny` | `Deny` (outer guard) | Preserve its position before the seam, emitted tool-result shape, and absence of prompt/classifier work. |
| Tool-selection or Plan hard denial, explicit deny, and non-interactive refusal | `Deny` | Preserve the current reason, denial, and error behavior. |
| Exact Plan capability, explicit allow, bypass mode, current safe defaults, scoped grants, contained-path allowances, and current Auto safe fast paths | `Allow` | Preserve the current admission result; do not widen any family. |
| Explicit ask and otherwise unmatched branches that use the installed prompt or external permission callback | `RequireHuman` | Preserve callback ordering, updated-input handling, cancellation, and unavailable-interaction behavior. |
| Existing Auto classifier-eligible branch | `Review` | Invoke the same legacy classifier and preserve all parse, error, ask, allow, deny, and event results. |
| Direct nil/nil embedded construction | `CallerAuthoritative` / `NoInvocationPolicyInstalled` | Preserve the documented library boundary; never report it as `Allow`, and prove supported product roots do not use it. |

The existing `permission.Evaluator` remains disconnected from production
invocation ownership in this slice. P22.1a neither promotes it into the seam
nor treats it as a second authority.

### Snapshot input contract

The snapshot has deterministic encoding, stable ordering, and is immutable
once observed.

| Input family | P22.1a identity input |
|---|---|
| Rules | Effective rules and their provenance |
| Grants | Canonically ordered approvals and scoped grants |
| Mode | Effective permission mode |
| Plan | Phase, revision, and plan-file identity |
| Session | Root session ID |
| Filesystem | Working roots and current working directory |
| Tools | Effective tool selection |
| Reserved | Capability generation, reviewer-policy version, and child scope use a fixed unpopulated representation |

Every populated input mutation must change the identity; unchanged inputs must
produce the same identity. Reserved fields are not current policy inputs or
promotion claims until their owning slices establish them.

### Explicit exclusions, scope, and acceptance evidence

P22.1a did not introduce a canonical action descriptor, capability policy,
complete updated-input re-evaluation, separate reviewer route, review request
binding, shadow audit, or enforcement. P22.1b owns the canonical descriptor
and complete updated-input decision cycle; P22.2a owns the separate reviewer
and its exact binding.

Production changes are limited to permission decision/snapshot types and the
QueryEngine and ProjectGraph seams under `engine/`. Production changes under
`cmd/`, `server/`, and `tools/` are excluded. Focused characterization tests
may be added under `engine/`, `cmd/eino-agent/cmd/`, and `server/acp/` to guard
their real composition roots. The implementation must show every branch and
outer guard above, each populated snapshot input, stable identity,
ProjectGraph consumption of that same identity, and installed policy at all
supported composition roots.

| Evidence boundary | Current source evidence | P22.1a consequence |
|---|---|---|
| Invocation policy | [`QueryEngine.wrapCanUseTool`](../../../engine/engine.go) | Keep QueryEngine as the sole user-visible coordinator. |
| Dispatch | [`executeToolCall`](../../../engine/tool_execution.go) | Preserve the current callback/result path; no new post-settlement behavior. |
| Graph identity | [`projectGraphPolicyRevision`](../../../engine/graph_hitl.go) | Derive Graph identity from the shared snapshot. |
| Existing evaluator | [`Evaluator`](../../../engine/permission/evaluator.go) | Retain; it is neither promoted nor removed by this slice. |
| Composition roots | [`root.go`](../../../cmd/yhc/cmd/root.go), [`headless.go`](../../../cmd/yhc/cmd/headless.go), [`agent.go`](../../../server/acp/agent.go), [`subagent.go`](../../../engine/subagent.go) | Prove supported roots never use `NoInvocationPolicyInstalled`. |
| Library boundary | [`permissions.md`](../../architecture/capabilities/permissions.md#decision-order) | Preserve caller-authoritative direct construction. |

## P22.1b promotion contract

**State:** completed 2026-07-27; delivery evidence is
[`p22-1b-canonical-action-policy.md`](../history/runtime/p22-1b-canonical-action-policy.md)

**Reader question:** which exact registered action is Auto admitting after
every input mutation, which host facts make that action complete, and what
authority may settle it?

P22.1b answers that question without adding a model reviewer. QueryEngine
remains the only invocation-policy coordinator. It consumes one pure,
registry-aware action descriptor and one typed rule decision; it does not add
an adapter-local policy loop.

### Reproduced current failures

| Current source fact | Failure | P22.1b boundary |
|---|---|---|
| [`SafeAutoModeAllowlistedTools`](../../../engine/permission/accept_edits.go) and [`SafeToolAllowlist`](../../../engine/permission/classifier.go) are separate name lists; the latter additionally contains `Agent`, `WebFetch`, and `WebSearch`. | The same registered action can bypass or reach the classifier according to a second, name-only owner. A read-only flag cannot describe network or child effects. | Delete the duplicate production admission paths or prove one is unreachable; one descriptor/capability policy decides deterministic Auto admission. |
| [`ToolImpl`](../../../tools/registry.go) exposes execution flags but not a complete origin, network, child, dynamic, user-interaction, or shell-completeness contract; [`RegisterToolsInRegistry`](../../../tools/mcp_tool.go) cannot supply those facts. | QueryEngine cannot distinguish a process-local built-in from a dynamic or network-capable action using registry evidence alone. | Extend the existing registry contract with host-owned capability facts. Registration metadata is evidence, not user authority. |
| [`executeToolCall`](../../../engine/tool_execution.go) applies a permission result's `UpdatedInput` after QueryEngine has evaluated rules, grants, mode, Auto, and the classifier, then rechecks only Plan. [`commitPermissionInteraction`](../../../engine/engine.go) records session/always permission against the old input before the rewrite is applied. | One action can be approved or persisted while a different action reaches dispatch. Rewritten input also skips schema/custom validation and capability reconstruction. | Stage the interaction result, rebuild and re-evaluate the rewritten action, and commit only against the final settled descriptor/input. |
| [`wrapCanUseTool`](../../../engine/engine.go) returns no callback when both permission dependencies are nil. | That is a valid caller-authoritative embedding boundary, but a supported Auto composition root must not accidentally use it as an implicit allow. | Preserve direct nil/nil library construction; prove every supported Auto root installs engine-owned invocation policy even when it cannot interact. |

These are current-source failures, not reference parity claims. The accepted
`combine` decision selects the smallest owner that closes them; P22.2a remains
the first reviewer slice.

### Canonical action and capability owner

One pure builder creates a detached `PermissionActionDescriptor` after
pre-tool normalization and before deterministic policy. It is rebuilt from the
current candidate after an accepted permission-result rewrite. The concrete Go
shape may change, but the value must contain:

| Fact family | Required descriptor facts |
|---|---|
| Registry identity | requested and canonical registered tool identity, selected/enabled/registered state, built-in/MCP/app/dynamic origin, and capability generation |
| Input | detached canonical JSON, schema/custom-validation result, action kind, and facts used by permission matching |
| Capability | read/write/destructive, process-local default-safe, network, child/`Agent`, dynamic, and user-interaction requirements |
| Filesystem and shell | resolved path/root relationships, protected-resource facts, and complete/incomplete shell representation |
| Runtime binding | root session, Agent, entrypoint, CWD/working roots, Plan identity, and the P22.1a effective-policy snapshot identity |

Raw canonical input stays host-local. This descriptor is not the future
model-safe projection and must not be sent to a reviewer.

The fail-closed classification is:

- an unknown, unavailable, disabled, selected-out, malformed, schema-invalid,
  or custom-validation-invalid action is denied before interaction or
  dispatch;
- absent an exact authority-bearing rule or grant for the canonical current
  action, missing capability facts, MCP/app/dynamic origin, `Agent`, network
  capability, and incomplete shell facts are `RequireHuman` in Auto;
- `WebFetch` and `WebSearch` are network-capable even if their current
  implementation also says read-only;
- `DefaultPermissionAllowed` may preserve only an explicitly declared
  built-in, process-local safe operation after selection, Plan, and hard
  policy. It is not a general Auto capability;
- contained read/search and the existing contained Write/Edit mode behavior
  remain typed capability decisions rather than entries in a name allowlist;
  and
- P22.1b makes no shell, child, network, or dynamic action reviewer-eligible.
  P22.1c and P22.5 retain those later gates.

### Rule authority and narrowness

Deny and ask rules remain authoritative from every loaded source. Auto may
consume allow authority only through a typed winning-match result that carries
the selected rule, provenance, and exactness; returning only
`(action, matched)` is insufficient.

- Current `user-settings` and git-ignored per-developer `local-settings` are
  user-authority sources. Checked-in `project-settings` is repository content
  and cannot widen Auto. The repository has no managed-policy source today;
  P22.1b does not invent one.
- A rule-based Auto `Allow` requires an authority-bearing source and an exact,
  non-wildcard match to the canonical current action whose capability policy
  accepts that authority. Tool-wide rules, prefix/directory globs, and generic
  glob-specificity scores are not narrowness proof.
- Current “always allow” construction deliberately emits a first-command-word
  wildcard for multiword Bash, a directory wildcard for file/search tools, or
  a bare tool name for other tools. Those persisted forms therefore do not
  become deterministic Auto allow merely because they matched.
- To keep “always allow” truthful after that tightening, P22.1b replaces its
  persistence path with a lossless exact-rule encoder over the final
  descriptor: exact command, exact resolved path, or canonical JSON input,
  with rule metacharacters escaped. The existing rule schema is sufficient;
  no new durable format is introduced. If an action cannot round-trip to the
  same descriptor, “always” fails with a stable explanation instead of
  falling back to a broad rule.
- Existing broad allow rules keep their non-Auto compatibility but do not gain
  narrow Auto authority. The compatibility consequence is intentional: a
  previously persisted command-prefix, directory-wildcard, or tool-wide rule
  may prompt in Auto until the user records an exact final action.
- A process-local session grant may qualify only through its typed scope:
  exact command/path/input identity, or a contained read/search scope whose
  descriptor proves the same root boundary. Broad text matching is not a
  substitute.
- An active permission interaction can authorize its exact final action, but
  repository content, assistant prose, tool results, web/MCP content, and
  registration metadata cannot manufacture user authority.

Outside Auto, existing rule and mode compatibility remains unless the
full-cycle rewrite checks below close a selection, validation, Plan, or hard
policy bypass. `bypassPermissions` remains a separate explicit mode and is not
renamed or treated as reviewer Auto.

### Updated-input evaluation and settlement

The invocation has one current candidate and at most one
permission-result rewrite:

1. Parse and validate the model input, run the pre-tool hook once, and replace
   the candidate with a detached hook rewrite. Hook stop/deny remains final.
2. Starting from the current candidate, resolve registry identity and
   selection, rerun schema/custom validation, build the descriptor, then
   evaluate Plan, hard policy, typed rule decision, existing grants, mode, and
   Auto eligibility in that order.
3. Only a settled `RequireHuman` branch may enter the existing interaction.
   The result is staged; session/always mutation is not committed yet.
4. If the result carries `UpdatedInput`, accept one detached rewrite and return
   to step 2 without rerunning the pre-tool hook. A user allow is represented
   as an exact staged decision for the rebuilt action; it cannot bypass
   selection, validation, Plan, hard policy, or deny. An ask rule is satisfied
   only by that same active interaction bound to the final action; a stored
   grant cannot skip it.
5. Settle allow-once, or commit session/always permission, only against the
   final descriptor and input after the cycle settles, then dispatch those
   exact bytes. A durable or session grant derived from the old input is
   forbidden.

A second permission-result rewrite, invalid or unrepresentable input, action
or policy-identity drift, stale interaction result, or failure to rebuild the
descriptor denies/aborts. Re-entry must not duplicate the prompt, classifier
call, permission events, or persistence mutation. Success/denial accounting
records only the final settled outcome.

### Entrypoints, scope, and acceptance evidence

TUI, plain, headless, ACP, and child composition tests must prove the same
descriptor and decision owner. In supported Auto without an interactive
callback, deterministic `Allow` may proceed while `RequireHuman` becomes one
stable deny/abort; the runtime must not fabricate a prompt. Direct embedded
construction with both callbacks nil remains
`CallerAuthoritative`/`NoInvocationPolicyInstalled`. Standalone MCP remains
excluded.

Production scope is limited to descriptor/rule-decision code under
`engine/permission`, the QueryEngine policy and execution seams under
`engine/`, `tools/registry.go`, `tools/mcp_tool.go`, and existing built-in
constructors only where capability annotations are required. Focused tests may
cover real roots under `engine/`, `tools/`, `cmd/eino-agent/cmd/`, and
`server/acp/`. Provider routing, reviewer requests/results, shadow audit,
reviewer UI, shell parsing, child enforcement, standalone MCP behavior, and
new durable schema are excluded.

Acceptance evidence must cover:

- registered/unknown/disabled/selected-out/aliased built-ins and MCP tools;
- `Agent`, network, dynamic, incomplete-shell, process-local safe, contained
  read/search, and contained Write/Edit classifications;
- deny/ask precedence plus exact local/user allow, project allow, broad allow,
  and typed session-grant cases;
- hook rewrite and permission-result rewrite attacks across paths, Plan,
  rules, grants, mode, capability, schema, and custom validation;
- old-input versus final-input session/always persistence, one-rewrite
  exhaustion, exact-rule round trips with rule metacharacters, rejection of
  unrepresentable “always” decisions, drift, cancellation, and exactly-once
  prompt/event/accounting;
- supported interactive and non-interactive roots plus the caller-authoritative
  library boundary; and
- race validation, repeated deterministic runs, all repository gates, and an
  independent security review before closeout.

## Historical Core Model And Deferred Enforcement Target

This section preserves the design boundary that produced P22.1a-P22.2b and
the later enforcement target that was never promoted. Current behavior stops
at an advisory shadow: reviewer `Approve`, `Deny`, `Escalate`, timeout, error,
or binding drift never changes, delays, or settles the legacy QueryEngine
permission outcome. Any statement below in which a reviewer result controls
execution, re-review, circuit breaking, or child authority is a deferred
P22.3a-P22.6 target, not current runtime behavior.

### Decision vocabulary

The deterministic stage returns exactly one class:

```text
PolicyDecision =
  Allow            // host policy grants this exact action
  Deny             // host policy forbids this exact action
  RequireHuman     // reviewer may not approve it
  Review           // eligible for the separate reviewer
```

The reviewer returns:

```text
ReviewDecision =
  Approve
  Deny
  Escalate
```

`Escalate`, timeout, cancellation, transport/model error, schema mismatch,
ambiguous output, stale identity, or late output becomes a human request on an
interactive entrypoint and deny/abort when interaction is unavailable. None is
an implicit approval.

### Deferred enforcement evaluation order

| Order | Owner | Required behavior |
|---:|---|---|
| 1 | Tool registry and selection | Unknown, unavailable, or excluded tools fail before review. Canonical alias resolution cannot bypass policy. |
| 2 | Plan capability | Active Plan containment and exact `ExitPlanMode` approval remain stronger than Auto. |
| 3 | Hard policy | Explicit deny, protected paths/resources, destructive circuit breakers, and tools marked `requiresUserInteraction` return `Deny` or `RequireHuman`. |
| 4 | Deterministic allow | A narrow explicit allow from an authority-bearing source, registry-declared process-local safe operations, contained read/search, and proven typed file capabilities may return `Allow`. No broad name list or repository-controlled allow grants unknown authority. |
| 5 | Existing grants/mode policy | Exact current-root/session grants and non-Auto modes retain current semantics. |
| 6 | Auto eligibility | Only an action whose canonical descriptor is complete and whose capability policy says `Review` reaches the reviewer. Missing facts escalate. |
| 7 | Reviewer | A separate reviewer evaluates trusted intent against the immutable request and returns structured `Approve`, `Deny`, or `Escalate`. |
| 8 | Settlement | QueryEngine compares the active request identity and process-local action digest, then rebuilds the effective policy snapshot immediately before execution. Any drift restarts policy or fails closed. |

Pre-tool hooks may normalize or rewrite input only before a future review
request is frozen. Complete updated-input re-evaluation and canonical action
rebuild are P22.1b work; P22.2a later binds reviewer results to that exact
request. Neither is P22.1a current behavior. Hook deny remains authoritative;
hook allow cannot cross Plan, protected-resource, or human-required
boundaries.

### Rule provenance and v1 review envelope

- Deny and ask rules remain authoritative across loaded scopes.
- In Auto, an allow rule can return `Allow` only when its provenance is
  authority-bearing and its action scope is narrow. Current user/local policy
  and an explicit typed current-session user grant may qualify;
  repository-controlled policy cannot widen its own authority merely by being
  checked out. No managed-policy source currently exists.
- Blanket code-execution, interpreter, package-runner, `Agent`, network, or
  dynamic-tool allows normalize to `Review` or `RequireHuman`, according to
  capability policy. They do not skip the reviewer because their text matched.
- A conversation boundary is reviewer context, not a hard rule. Compaction or
  truncation cannot erase deterministic policy; a boundary that must survive
  belongs in an explicit deny/ask rule.
- Without an OS sandbox, a shell action is `Review`-eligible only when the
  canonical descriptor accounts for every parsed subcommand, wrapper,
  substitution, redirection, path effect, and network capability required by
  the policy. Parse uncertainty returns `RequireHuman`. P22 does not infer
  containment from a command prefix.
- Production/IAM/deployment, credential or secret movement, protected paths,
  destructive history/filesystem operations, unknown external destinations,
  and connector/app consent remain `RequireHuman` or `Deny` in the first
  enforcement envelope.

### Single policy and action owners

QueryEngine remains the only coordinator, but it consumes two pure,
project-owned values:

- `PermissionActionDescriptor` is built once by one registry-aware descriptor
  builder after alias resolution and pre-tool rewrites. It owns canonical tool,
  action kind, exact normalized input, resolved path/capability facts, and
  parse completeness. Built-ins and dynamic MCP/app registrations feed the
  same builder; a missing annotation or incomplete parse is
  `RequireHuman`, never an adapter-local guess.
- `PermissionPolicySnapshot` is built by one QueryEngine-owned policy
  evaluator. Its deterministic `SnapshotID` covers effective rules and
  provenance, permission mode, Plan state/revision, applicable grants,
  protected-resource policy version, working-root/CWD identity, tool selection
  and capability generation, reviewer-eligibility policy version, and the
  child execution scope. A plain `RulesEngine.Snapshot()` or an independently
  maintained counter is not sufficient.

The previous ProjectGraph policy digest was an adaptation candidate, not a
second owner. P22.1a made ProjectGraph consume the QueryEngine snapshot
identity and placed the legacy Auto classifier behind the same decision seam.
P22.2a binds its advisory reviewer request to that policy identity plus the
canonical action. A future enforcement proposal would additionally have to
revalidate rules, roots, capabilities, and grants before any reviewer result
could affect execution.

### Internal action and model-safe review request

Raw normalized arguments and absolute host paths remain inside QueryEngine.
They are never passed through the reviewer interface. The implementation may
refine Go names, but must preserve this split:

```go
type pendingPermissionReview struct {
    Request          PermissionReviewRequest
    SessionID        string
    AgentID          string
    EntryPoint       string
    Action           PermissionActionDescriptor // raw, host-local
    ActionDigest     [32]byte                   // raw action binding; never exported
    PolicySnapshotID string
}

type PermissionReviewRequest struct {
    SchemaVersion uint16
    RequestID     string
    ToolCallID    string
    BindingNonce  string
    Projection    PermissionReviewProjection
}

type PermissionReviewProjection struct {
    CanonicalTool string
    ActionKind    string
    RedactedArgs  json.RawMessage
    RootFacts     []RootFact
    RiskFacts     []RiskFact
    TrustedIntent []IntentRecord
}
```

The process-local digest covers schema version, canonical tool/action, exact
normalized arguments, relevant roots/CWD, Session/Agent identity, entrypoint,
and policy snapshot identity. `BindingNonce` is a random, request-scoped opaque
value; it is not derived from secrets or action bytes. The model-safe
projection uses workspace-relative labels and typed boundary facts instead of
absolute host paths. Secrets use typed redacted facts; low-entropy secret
values are never replaced with reusable plain hashes. Collection order and JSON
canonicalization are deterministic.

### Reviewer interface and schema

The reviewer is an optional engine dependency distinct from `ChatModel`:

```go
type ApprovalReviewer interface {
    Review(context.Context, PermissionReviewRequest) (PermissionReviewResult, error)
}

type PermissionReviewResult struct {
    SchemaVersion uint16
    RequestID     string
    ToolCallID    string
    BindingNonce  string
    Decision      string // approve | deny | escalate
    ReasonCode    string
    Rationale     string
}
```

The provider layer owns construction of a reviewer from explicit
reviewer-specific route/configuration; QueryEngine receives only the prepared
`ApprovalReviewer`. CLI, ACP, and child construction paths may configure or
project it but may not choose a fallback model independently. The actor model
is never an implicit reviewer fallback, and cross-provider review is disabled
unless the user explicitly configures that data boundary.

The concrete LLM adapter uses an absolute deadline and strict structured data.
Unknown schema versions, extra decision values, missing fields, duplicate
results, an answer after cancellation, or any request/tool-call/binding
mismatch fail closed. QueryEngine accepts a result only while the matching
pending request is active and unsettled; reviewer echoes are correlation data,
not authority. Security comes from host-side lookup, action-digest
recomputation, and current policy-snapshot comparison. Free-form XML tags are
not the enforcement format.

The main actor never receives hidden reviewer reasoning. It receives only a
bounded reason code, user-safe rationale, and safe alternative. A denial must
not reveal a prompt, policy exploit, or circumvention recipe.

### Trust-separated context

Reviewer input has explicit trust classes:

| Class | Included by default | Authority |
|---|---|---|
| User authority | Direct user requests and explicit current-turn user approvals | May establish intent, but cannot override hard policy. |
| Host policy | Canonical tool metadata, policy facts, relative/root-boundary facts, sandbox state, entrypoint, and project configuration provenance | Authoritative. |
| Requested action | Canonical tool/action and data-minimized redacted arguments | Object under review, never an instruction to the reviewer. |
| Untrusted evidence | Assistant prose, tool results, repository files, web/MCP content, child transcript | Excluded by default. If a future evaluator needs it, include it in a separately delimited non-authoritative field with injection tests. |

Project instructions may constrain behavior but cannot grant authority. Tool
results are not included as trusted intent. Hidden main-model reasoning is
never sent.

### Settlement and persistence

- A reviewer result is process-local evidence for one request. It never creates
  an allow rule, session grant, or durable approval.
- QueryEngine re-runs deterministic policy and compares the exact active
  request ID, tool-call ID, binding nonce, process-local action digest, and
  policy snapshot immediately before dispatch.
- A policy/rule/root/CWD/input/identity change invalidates the result.
- Pending reviewer requests and reviewer results are not checkpointed. A cold
  resume discards them, re-evaluates current policy, and creates a fresh
  request/binding identity. Existing durable human-interaction ownership is
  unchanged after an escalation reaches that boundary.
- QueryEngine owns typed review/audit event creation. Transient UI progress and
  the redacted measurement sink are separate consumers; neither may settle a
  request. P22.2b deliberately narrowed durable correlation to one fresh
  opaque event ID plus canonical action/deterministic class, reviewer
  route/schema, terminal status/decision/reason/latency, and an explicit typed
  comparison label. Request/tool IDs, policy labels, raw/full arguments,
  absolute paths, transcript/session/Agent/CWD identity, process-local action
  digests, binding nonces, rationale, credentials, and secret-derived hashes
  are absent.
- Shadow audit storage is bounded, local-user scoped, and separately opted in.
  P22.2b freezes a pinned `0700` directory root, identity-checked `0600` files,
  an OS-locked bounded O_EXCL sentinel, eight 1 MiB JSONL segments,
  size-window-only retention, strict non-null corruption and partial-tail
  reporting, typed tail recovery, and exact owned-segment deletion. Aggregate
  output keeps eligible, attempt, human, legacy, and versioned-corpus
  denominators separate rather than copying raw per-action records.

## Current Failure Boundary And Deferred Overrides

| Condition | Current P22.H0-P22.2b | Deferred P22.3+ target |
|---|---|---|
| Deterministic deny | QueryEngine denies; reviewer shadow cannot override it. | Preserve the same denial and bounded safe result. |
| Human-required action | Interactive entrypoints ask the person; non-interactive entrypoints deny/abort. | Preserve the human/fail-closed boundary. |
| Reviewer approve | Advisory event only; the legacy QueryEngine outcome proceeds unchanged. | Revalidate the exact action and policy before execution. |
| Reviewer deny or escalate | Advisory event only; it neither denies nor opens a second prompt. | Deny or ask the person under one exact settlement owner. |
| Timeout, error, malformed, or late result | Record unavailable state without delaying or changing the legacy outcome. | Ask the person when interaction exists; otherwise deny/abort. |
| Request, action, or policy mismatch | Discard the advisory result; it has no execution authority. | Restart exact policy once or fail closed without late execution. |

P22.3a would have added one exact one-shot re-review of a reviewer denial. That
override is deferred and does not exist today. Its frozen target bound the
signal to one active process-local action, allowed no direct execution grant,
and could not override deterministic deny, Plan containment, protected
resources, or a human-required classification.

P22.4 would have added a compatibility circuit breaker after 3 consecutive or
20 total reviewer denials. That tuning is also deferred; those values do not
control the current advisory shadow.

## Current Entrypoints And Deferred Enforcement

| Entrypoint | Current P22.H0-P22.2b | Deferred enforcement target |
|---|---|---|
| TUI | Projects bounded checking/completed/unavailable status; reviewer results never settle permission. Existing human interaction remains QueryEngine-owned. | Apply an exact reviewer settlement without blocking or duplicating the existing interaction owner. |
| Plain REPL | Prints bounded advisory status/result; legacy permission and prompt behavior remain authoritative. | Consume the same exact reviewer settlement as TUI. |
| Headless `exec` / `--print` | Auto installs engine-owned policy and never fabricates interaction. Reviewer results are advisory only. | Allow only a freshly revalidated reviewer approval; every unavailable or human-required result fails closed. |
| ACP | Projects advisory status through its existing transport; reviewer results do not authorize execution. | Bind settlement to unique transport identity and reject disconnect, timeout, unknown, stale, or late results. |
| Child Agent | `Agent` spawn stays `RequireHuman`; reviewer enforcement is disabled inside children. | Give each child QueryEngine an independent exact policy/review identity while the parent remains presentation/transport only. |
| Standalone MCP | Excluded; it has no QueryEngine reviewer owner and advertises no P22 behavior. | Remain excluded. |

## Ordered slices

Only a row marked `Ready` in root [`PLAN.md`](../PLAN.md) is executable. The
historical dependency chain was P22.H0 → P22.1a → P22.1b → P22.2a → P22.2b →
P22.3a → P22.3b → P22.4 → P22.5 → P22.6. P22.1c was an optional branch from
P22.1b and gated only shell review eligibility. The remaining branches are now
deferred; shell actions stay human-required and no enforcement successor is
accepted.

| Slice | Outcome | Allowed scope | Promotion gate |
|---|---|---|---|
| P22.H0 | Remove the active Bash authorization bypass. `acceptEdits` and `auto` no longer auto-allow any Bash invocation merely because its first token is a filesystem command. | `engine/permission/accept_edits.go`, the two QueryEngine call sites only if required, and focused permission tests. No classifier or UI change. | Negative tests cover root/outside-root paths, compound commands, substitutions, wrappers, redirection, symlink/protected paths, and unchanged contained Write/Edit behavior. Four Makefile code gates plus docs/manifest/diff gates pass. |
| P22.1a | Introduce one QueryEngine-owned decision seam and one immutable effective-policy snapshot without changing post-H0 actual Allow/Deny/prompt/classifier outcomes. Make ProjectGraph revision a derived shared-snapshot identity. | Permission seam/snapshot types, QueryEngine and ProjectGraph identity seams, and focused characterization tests. Retain legacy classifier/allowlists/`Evaluator`; no reviewer or descriptor work. | P22.H0 closed; rules+provenance, grants, mode, Plan phase/revision/file, root session, roots/CWD, and tool selection deterministically affect identity; reserved generation/version/child fields are not claimed as current inputs; TUI/plain/headless/ACP/child roots install policy; direct nil/nil construction is explicitly CallerAuthoritative. |
| P22.1b | Introduce one registry-aware canonical action descriptor/capability owner, remove duplicated name-based Auto admission, and re-evaluate one permission-result input rewrite before settling permission or dispatch. | Descriptor builder, typed winning-rule provenance/exactness, registry capability annotations, QueryEngine/execution seams, and focused built-in/MCP/Agent/entrypoint tests. No reviewer, provider, UI, or shell-parser work. | Repository/broad rules cannot self-expand Auto; missing/dynamic capability metadata, `Agent`, network, and incomplete shell facts require a person absent exact user authority; old-input grants cannot authorize rewritten input; duplicate production allowlists and unreachable policy owners are deleted or proven non-production. Auto installs the policy check even without an interactive callback while direct nil/nil embedding remains caller-authoritative. Concurrent exact persistence cannot lose a distinct rule in one process, and engine metadata updates cannot mutate another registry generation. |
| P22.1c | **Deferred.** Add review eligibility for a bounded, fully described shell subset without pretending a prefix splitter is a shell security boundary. | Shell descriptor/parser integration and fixtures only; no provider/UI work. | Every accepted subcommand, wrapper, substitution, redirection, path and network effect is represented; ambiguity, unsupported syntax, aliases, and descriptor drift require a person. |
| P22.2a | Add the model-safe request projection, strict result schema, separate provider-owned reviewer factory/route, timeout, and process-local exact binding in non-authoritative shadow mode. Shadow is off by default and never executes an action or suppresses a prompt. | `engine/permission`, narrow provider/configuration wiring, QueryEngine events, provider fixtures. | Explicit opt-in and diagnostics name provider/model/data boundary; actor fallback and implicit cross-provider routing are absent; injection, redaction, schema, timeout/cancel/late/duplicate/cross-delivery, resume, and exact binding tests pass. |
| P22.2b | Add one bounded redacted audit sink and produce decision-ready shadow measurements. | Typed audit events/sink, rotation/retention/deletion, aggregate report, fixtures. | Owner, storage location, permissions, retention, and deletion are frozen; denominators distinguish eligible actions, reviewer attempts, comparable human decisions, and corpus ground truth; p50/p95 latency, unavailable/escalation, disagreement, and every false allow are recorded without raw inputs, absolute paths, digest/nonce, or secrets. |
| P22.3a | **Deferred.** Enable opt-in reviewer enforcement plus the minimum exact one-shot re-review action for eligible built-in, non-shell actions on TUI/plain. `Agent`, child execution, dynamic/network tools, and incomplete shell actions remain human-required. | QueryEngine settlement, TUI/plain adapters, exact override marker, configuration migration, lifecycle projection. | No hard-boundary approval, changed-action execution, updated-input reuse, failure-path execution, duplicate settlement, late approval, or restart replay in the full corpus. Re-review is bound to the active request/action, expires after one attempt, still passes reviewer policy, and cannot override a hard decision. |
| P22.3b | **Deferred.** Extend the same enforcement contract to ACP and headless only after their failure matrices pass. | Existing ACP/headless adapters and machine-readable outcomes; no child/dynamic expansion. | No-callback Auto is engine-owned and fail-closed; ACP identity/disconnect and headless unavailable-interaction tests prove no fabricated prompt, actor fallback, late settlement, or behavior split. Root PLAN records a measured latency/error budget before promotion; no threshold is invented in advance. |
| P22.4 | **Deferred.** Add bounded denial explanation, denial inspection/retry UX, and measured circuit-breaker tuning without changing the P22.3 authority boundary. | Existing permission interactions, reviewer-specific denial accounting, user-safe result projection. | Reviewer denials are not inserted into generic denial history before human fallback settlement; user-safe rationale/alternative and 3/20 compatibility behavior have TUI/plain/headless/ACP tests; the P22.3 exact re-review binding remains unchanged. |
| P22.5 | **Deferred.** Close child-agent and dynamic-tool authority gaps, then make only proven capability classes review-eligible. | Agent spawn/action/return admission, child-owned policy/review identity, worktree roots, MCP/app capability metadata used by QueryEngine. Standalone MCP remains excluded. | Parent `Agent` spawn and every child action have independent exact identities; parent is presentation/transport only; network/dynamic tools have trusted capability metadata; spawn, each action, return injection, parent/child cancellation, no-child-enforcement fallback, and stale-root tests pass before removing `RequireHuman`. |
| P22.6 | **Deferred.** Promote from opt-in, delete superseded classifier/evaluator/speculation/cache owners, and close G14. | Configuration default only after evidence; dead-owner deletion; docs/status/history. | Independent security review, full Makefile gates, race tests, supported-entrypoint matrix, recorded evaluation denominators, rollback rehearsal, and zero observed hard-boundary or action-binding violations. |

P22.H0 completed on 2026-07-26 and is recorded in
[`p22-h0-bash-containment.md`](../history/runtime/p22-h0-bash-containment.md).
P22.1a completed on 2026-07-27 and is recorded in
[`p22-1a-permission-decision-snapshot.md`](../history/runtime/p22-1a-permission-decision-snapshot.md).
P22.1b completed on 2026-07-27 and is recorded in
[`p22-1b-canonical-action-policy.md`](../history/runtime/p22-1b-canonical-action-policy.md).
P22.2a completed on 2026-07-27 and is recorded in
[`p22-2a-permission-review-shadow.md`](../history/runtime/p22-2a-permission-review-shadow.md).
P22.2b completed on 2026-07-27 and is recorded in
[`p22-2b-permission-review-audit.md`](../history/runtime/p22-2b-permission-review-audit.md).
## P22 Remaining-Slice Defer Decision

**Decision:** `defer` on 2026-08-01.

P22.1c and P22.3a-P22.6 close without an implementation successor. The
[`readiness evidence`](../verification/p22-enforcement-promotion-readiness.md)
records a provider-free retained-window report with no eligible actions,
reviewer attempts, terminal results, latency samples, direct-human labels, or
versioned-corpus denominator. Missing data cannot prove reduced prompt
friction, acceptable latency/error/cost, or zero safety violations.

P22.1c is deferred separately because a shell parser and effect descriptor do
not create representative evidence or user value while reviewer enforcement
itself is unpromoted. Ambiguous or incomplete shell actions remain
human-required. The P22.3a-P22.6 chain is deferred together because every
later entrypoint, capability, circuit-breaker, default, old-owner deletion, and
G14 closure depends on the unproved enforcement boundary.

The user-visible contract remains P22.H0-P22.2b. QueryEngine is authoritative;
the reviewer shadow and audit sink remain explicit opt-ins and
non-authoritative; `Agent`/child, network, dynamic, user-interaction, and
incomplete shell actions do not gain reviewer authority. No configuration,
provider route, persistence schema, permission ordering, interaction adapter,
or compatibility owner changes through this decision.

Future reviewer enforcement or shell eligibility requires new intake and the
complete non-zero measurement, safety corpus, privacy, budget, independent
security review, and rollback evidence in the readiness document. It does not
reopen these slice IDs automatically.

P22.H0 is intentionally a small rollback boundary. It reduces convenience for
Bash in `acceptEdits`/`auto`, but returns those invocations to the existing
prompt/fail-closed path. It does not wait for reviewer architecture.

## Verification matrix

### Deterministic policy

- explicit deny beats rules, modes, reviewer, grants, hook allow, and cached
  state;
- Plan containment and exact Exit approval remain unchanged;
- contained Write/Edit positive cases and traversal/symlink negative cases;
- Bash root deletion, outside-root copy/move, compound commands, substitutions,
  wrappers, redirections, aliases, shell options, and protected paths;
- capability metadata for network, connector, MCP/app, user-interaction,
  persistent-state, child-agent, and destructive actions;
- unknown tool, alias, missing metadata, and schema-normalization failures.

### Reviewer isolation

- assistant prose, tool results, repository text, web content, MCP metadata, and
  child returns cannot appear as user authority;
- user intent ordering, bounded truncation, redaction, and canonical action
  projection;
- separate model selection and unavailable-model behavior;
- malformed JSON, unknown version/decision/reason, duplicate and conflicting
  output, same-action cross-request/tool-call delivery, provider tool-call
  drift, and prompt leakage;
- timeout, context cancellation, late result, retry boundary, and no goroutine
  or event leak.

### Binding and concurrency

- input, tool, CWD, root, Session, Agent, entrypoint, and policy changes all
  invalidate approval;
- pre-tool rewrite precedes digest; updated input returns to the complete
  deterministic-policy/reviewer cycle and never reuses a prior result;
- exact once settlement under cancellation, prompt/reviewer race, Graph HITL
  resume, cold restart with a fresh request identity, ACP disconnect, and child
  detach;
- no duplicate model review, human prompt, tool dispatch, or audit event.

### Evaluation and rollout

The shadow corpus records:

- deterministic class and human decision;
- reviewer decision/reason;
- hard-boundary violation count;
- action-binding violation count;
- approval, denial, escalation, timeout, and error denominators;
- human override/disagreement denominator;
- p50/p95 reviewer latency and incremental turn latency; and
- cost by reviewer model/provider.

Any observed approval of a deterministic deny/human-required case, execution
after identity drift, or execution on reviewer failure blocks enforcement.
False escalation and latency are product-quality measures; they may tune model,
prompt, and eligibility, but cannot weaken hard policy.

“Zero observed violations” means zero across both the named versioned security
corpus and all recorded shadow-eligible events, with both denominators present
in the promotion report. Missing, expired, or disabled audit data is not a zero.

## Rollback

- P22.H0 rollback restores the unsafe Bash fast path and is therefore allowed
  only to diagnose behavior, not as a production safety response.
- P22.1 keeps QueryEngine as the last safe owner; reverting restores the
  previous decision implementation without schema migration.
- P22.2 shadow data is additive and non-authoritative. Disabling the reviewer
  returns every reviewable action to the existing human/fail-closed path.
- P22.3-P22.5 retain a configuration kill switch that disables enforcement
  without selecting bypass. Pending reviewer requests are process-local,
  disappear on restart, and cannot replay as grants.
- No slice writes durable reviewer requests, reviewer results, or approval
  grants. Existing durable human-interaction schemas remain separately owned.
- P22.6 may delete old owners only after rollback uses the new deterministic
  policy plus human/fail-closed path, not the retired classifier.

## Documentation closeout

Each behavior becomes current only when its slice updates:

- [`architecture/capabilities/permissions.md`](../../architecture/capabilities/permissions.md);
- [`guides/permissions-and-safety.md`](../../guides/permissions-and-safety.md)
  for user-visible behavior;
- [`STATUS.md`](../STATUS.md);
- G14 in [`REMAINING.md`](../REMAINING.md);
- one delivery record indexed by
  [`migration/history/README.md`](../history/README.md); and
- [`manifest.yaml`](../manifest.yaml) when classified evidence changes.

The reference audit remains a dated comparison and never owns completion.
