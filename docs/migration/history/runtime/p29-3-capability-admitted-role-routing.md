# P29.3 Capability-Admitted Agent-Role Routing

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for P29.3 fixed role selection,
> selected-profile admission, child model identity and recovery, root-only
> best-effort summaries, provider-neutral reasoning lowering, and bounded
> usage projection.

## Outcome

P29.3 completed the frozen `combine` slice. The portfolio now retains whether
an optional role was explicitly configured instead of materializing absence
as startup-main identity. `engine/provider.Runtime` admits one detached
side-effect-free call snapshot for `main`, `explore`, `plan`, `general`, or
`summary`. It requires authoritative selected-profile metadata for static and
dynamic needs before route construction.

Root calls use the admitted P29.2 main binding. Explore, Plan, and all other
Agents consume their fixed role; absent roles dynamically inherit the current
main binding. A truthful trusted side-model injection retains its documented
narrow precedence, while Agent-definition and tool-input model strings remain
non-authoritative.

The existing AgentRunner assigns identity and worktree scope first. Durable
execution admission then commits the original Agent name/type, fixed
`model_role`, and exact P29.2 binding before executor entry. Record and Execute
share the same frozen process-local model call. Resume re-admits the persisted
binding and role without current-policy reinterpretation. Old child Sessions
with neither field keep legacy parent inheritance and upgrade on the next
admitted execution; partial new-format identity fails closed without transcript
repair.

Only the existing enabled root best-effort tool-use summary consumes
`summary`. Child summaries remain disabled. Authoritative compaction,
memory/dream, WebFetch, permission classifiers/explainers, callback summaries,
and the P22 reviewer retain their previous model owners.

## Provider And Usage Contract

Profile defaults and explicit overrides lower through exact provider options:
Claude `output_config.effort`, typed OpenAI Responses reasoning, typed Ark
Responses reasoning, and typed Gemini thinking level. DeepSeek and Qwen remain
provider-default only. Metadata and adapter support must agree; unsupported or
unknown values fail before provider-usage admission or provider dispatch.

The exact role, profile, and applied effort enter the bounded provider-usage
descriptor. Session and runtime projections retain only safe model identity;
account, endpoint, authentication data, raw metadata, and route health remain
excluded.

## Compatibility, Verification, And Rollback

Explore/Plan tools, permissions, file state, ProjectGraph stage, worktree,
parent cancellation, Goal generation/usage, transcript, retry, streamed-tool
commitment, and terminal lifecycle remain under their pre-P29.3 owners. The
fixed Haiku selection function was removed after all production callers moved
to the role/compatibility resolver.

Focused role, capability, media/PDF/context, child recovery, summary, typed
provider, usage, source-owner, and race checks plus all repository,
documentation, and manifest gates are recorded in
[`p29-3-capability-admitted-role-routing.md`](../../verification/p29-3-capability-admitted-role-routing.md).

Rollback stops role resolution and provider-neutral effort lowering, restores
current-main inheritance plus trusted side-model compatibility, and stops new
`model_role` writes. It keeps P29.1-P29.2 portfolio and binding records
readable and never reinterprets an existing child binding. P29.4-P29.5 remain
queued; no failover or adaptive-health authority was promoted.
