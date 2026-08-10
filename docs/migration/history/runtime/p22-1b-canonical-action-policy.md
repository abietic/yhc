# P22.1b Canonical Action Policy

**Status:** historical
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for the registry-aware canonical permission
> action, exact Auto authority, full permission-result rewrite cycle, and final
> dispatch binding. Current behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md);
> remaining reviewer work and executable order belong in
> [`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P22.1b was delivered under the **`combine`** decision:

- preserve QueryEngine as the only invocation-policy and interaction owner;
- preserve explicit deny, typed Plan capability/approval, mode, contained
  read/write, prompt, event, and denial-accounting contracts;
- combine registry identity, host-owned capability facts, rule provenance,
  exact user authority, resolved path, runtime identity, and the P22.1a policy
  snapshot into one project-owned action descriptor;
- replace broad interactive persistence with a lossless exact rule for the
  final action; and
- make one permission-result input rewrite re-enter the complete policy cycle
  before grant mutation or dispatch.

This slice adds no reviewer model, provider route, reviewer request, UI,
durable schema, shell parser, OS sandbox, or standalone MCP behavior.

## Canonical Action And Capability Boundary

[`QueryEngine.buildPermissionActionDescriptor`](../../../../engine/permission_action.go)
constructs one detached value after schema coercion and schema/custom
validation. It records:

| Fact family | Bound evidence |
|---|---|
| Registry | Requested and canonical name, registered/enabled/selected state, origin, and mutation generation. |
| Input | Detached map, canonical JSON, validation completion, action kind, and destructive/read/write/default-safe facts. |
| Capability | Network, child, dynamic, user-interaction, and shell-completeness declarations supplied by the host registry. |
| Filesystem | Logical and resolved path representations, unsafe state, working roots, containment result, and CWD. |
| Runtime | Root session, session/thread/Agent, entrypoint, permission mode, Plan phase/revision/file identity, and effective-policy identity. |

Every default built-in now has an explicit capability record.
`WebFetch`/`WebSearch` are network-capable, Agent/Task/team/send operations are
child-capable, MCP/app registrations are dynamic/network-capable, and Bash
remains shell-incomplete until P22.1c proves a bounded parser. Registration
metadata describes effects; it does not create user authority.

Unknown, reserved-unavailable, disabled, selected-out, non-durable,
schema-invalid, or custom-validation-invalid actions stop before interaction
or dispatch. A supported Auto composition root creates the default registry
and installs this policy even when no prompt adapter exists. A direct embedded
engine with both permission callbacks nil remains the explicit
caller-authoritative boundary.

## Auto Authority And Exact Persistence

Explicit deny and ask remain authoritative from every source. Outside Auto,
existing allow compatibility remains. In Auto:

- only a winning exact, non-wildcard local/user rule may provide rule
  authority;
- a session grant must carry exact command, resolved path, or canonical input
  identity, except for an explicitly contained Read/Grep/Glob root scope;
- a project, tool-wide, command-prefix, directory-wildcard, or other broad
  allow cannot widen unattended authority;
- TodoWrite remains the only built-in process-local default-safe action;
- contained Read/Grep/Glob and contained Write/Edit retain typed fast paths;
  and
- missing capability facts, MCP/app/dynamic origin, Agent/child, network,
  direct interaction, and incomplete shell actions require a person before
  classifier work unless exact user authority covers the current action.

The former QueryEngine and classifier name allowlists no longer decide
production admission. “Always” now calls
[`BuildExactRuleFromInvocation`](../../../../engine/permission/persist.go) for
the final action and encodes an exact command, resolved path, or canonical JSON
input with rule metacharacters escaped. An unrepresentable action fails rather
than falling back to a broader rule. Existing broad rules retain their
non-Auto compatibility but no longer become deterministic Auto authority.

## Rewrite, Settlement, And Dispatch

[`executeToolCall`](../../../../engine/tool_execution.go) runs the pre-tool hook
once, validates its detached candidate, and invokes QueryEngine policy. A
permission interaction may return one updated input. QueryEngine stages that
decision, rebuilds the descriptor, and rechecks selection, schema/custom
validation, Plan, explicit deny, rules, grants, mode, and Auto. It commits
allow-once/session/always only for that final descriptor and returns only its
canonical JSON for accounting and dispatch.

Settlement accepts no arbitrary policy mutation. Allow-once requires an
unchanged policy; session/always permits only the exact requested grant/rule
transition. A second concurrent mutation fails the current action closed even
when the user's exact grant was durably recorded for a later retry.
Settings add/remove operations serialize their process-local read-merge-write
cycle, so simultaneous distinct “always” decisions retain every exact rule.
No cross-process file-lock guarantee was added.

Final dispatch rebuilds the descriptor and compares canonical input, complete
resolved-path/containment facts, capability generation, selection, runtime,
Plan, and policy identity. Replacing an approved directory with a symlink to an
outside target is rejected. The registry then supplies one read lease that
verifies requested/canonical identity and generation at the dispatch
linearization point; concurrent disable, unregister, alias replacement, or
implementation update cannot cross it.
Dynamic Agent-description updates target the current engine registry. Creating
an unrelated engine can no longer mutate a process-wide compatibility target
and spuriously invalidate this action's generation.

## Entrypoint And Compatibility Evidence

| Boundary | Verified result |
|---|---|
| TUI and plain | The existing interactive adapters reach the same QueryEngine descriptor/settlement owner and retain one request/resolved event pair. |
| Headless | Supported Auto installs deterministic policy without a prompt; safe typed actions proceed and human-required actions return one stable denial. |
| ACP | New/load/resume construction reaches the same registry-aware owner; no adapter-local action policy is added. |
| Child Agent | The child retains its own CWD/roots/Agent identity while sharing only the established root-session interaction boundary. |
| Embedded library | Direct nil/nil construction remains caller-authoritative. |
| Standalone MCP | Excluded; its separate immutable server policy is unchanged. |

The exact Plan-file capability remains after explicit deny and before ordinary
ask/modes/grants, preserving P20 behavior. Pre-tool hooks run once. Permission
rewrite, prompt, classifier, request/resolved events, persistence, and
success/denial accounting do not duplicate.

## Verification

Focused and adversarial evidence covers:

- canonical alias resolution, every built-in capability, MCP origin, registry
  mutation generations, and lease/mutation serialization;
- unknown, unavailable, disabled, selected-out, malformed, schema/custom
  invalid, and non-durable inputs before interaction;
- TUI/plain/headless/ACP/child supported Auto roots plus direct embedding;
- exact local/user authority, broad/project rejection, exact command/path/input
  grants, and contained read roots;
- Agent, WebFetch, WebSearch, MCP/dynamic, and incomplete shell human-required
  behavior before the legacy classifier;
- one pre-hook and permission-result rewrite, final-only persistence,
  accounting and bytes, and rewrite attempts against deny/validation;
- policy, registry, and resolved symlink-path drift before dispatch; and
- concurrent exact grants without lost persistence, plus isolation of
  engine-owned Agent metadata from unrelated registries.

Closeout passed:

```text
go test ./engine ./tools -run 'TestP221b|TestPersistPermissionRulesConcurrentMergeDoesNotLoseExactRules|TestAgentDescriptionsStayScopedToEngineRegistry|TestRegistry(ResolveReturnsCanonicalAliasSnapshot|UnregisterByAliasRemovesCanonicalAndEveryAlias|GenerationIncrementsForMutations|ExecutionLease|RegisterDefaultsDeclaresCapabilitiesForEveryBuiltin)|TestMCPRegistrationDeclaresDynamicNetworkCapabilities' -count=20
go test -race ./engine ./tools -run 'TestP221b|TestPersistPermissionRulesConcurrentMergeDoesNotLoseExactRules|TestAgentDescriptionsStayScopedToEngineRegistry|TestRegistry(ResolveReturnsCanonicalAliasSnapshot|UnregisterByAliasRemovesCanonicalAndEveryAlias|GenerationIncrementsForMutations|ExecutionLease|RegisterDefaultsDeclaresCapabilitiesForEveryBuiltin)|TestMCPRegistrationDeclaresDynamicNetworkCapabilities|TestPermission.*Coalesc' -count=1
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check-ledger
git diff --check
```

An independent security/concurrency audit first found that settlement masked
unrelated policy drift and that registry mutation could cross the final
generation check. The accepted fixes admit only the exact grant-owned policy
transition and add the execution lease. A final reviewer then found that
resolved path facts were absent from the binding; the accepted fix binds the
complete `PathResolution` and containment result, and the symlink-ancestor
regression passed repeatedly. Full-suite validation subsequently exposed two
concurrency defects rather than suppressing them as flakes: settings
read-merge-write could lose one exact ACP grant, and process-global Agent
description updates could advance an unrelated registry generation. The
process-local persistence lock and engine-scoped registry update fixed both;
the second-line re-review reported no remaining finding.

## Compatibility, Exclusions, And Rollback

Compatibility changes are deliberate and narrow:

- existing broad allows still work outside Auto but may prompt in Auto;
- new session/always grants are exact and apply to the post-rewrite action;
- unmatched Bash, Agent/child, network, MCP/app/dynamic, and direct-interaction
  actions reach a person or fail closed before the legacy classifier; and
- supported noninteractive Auto roots no longer confuse absent interaction
  with absent policy.

No persisted rule schema changed. Rollback is one code/test/document unit and
restores the previous name-list admission, broad persistence, and
post-permission Plan-only rewrite check; because that reopens known
authorization gaps, rollback is diagnostic rather than the preferred safety
response.

P22.1b did not separate the actor and reviewer, minimize reviewer input, add a
strict response/deadline, record shadow data, or enforce a reviewer decision.
G14 therefore remains open.

## Next State

P22.2a is the sole `Ready` slice. It owns an off-by-default,
non-authoritative, separately routed reviewer shadow request with strict
projection/result/deadline and exact process-local binding. P22.1c remains an
optional bounded-shell branch; P22.2b-P22.6 remain queued behind their own
promotion gates.
