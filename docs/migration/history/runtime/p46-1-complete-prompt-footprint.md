# P46.1 Complete Prompt Footprint Admission

**Status:** historical
**Closed gaps:** G36
**Completed:** 2026-08-06
**Adoption:** `preserve`

> **Ownership:** completion record for P46.1/G36; current behavior belongs in
> [`model-providers.md`](../../../architecture/platform/model-providers.md) and
> the accepted remaining repair belongs in
> [`p46-model-failover-repair.md`](../../plans/p46-model-failover-repair.md)

## Outcome

`runCanonicalModelRound` now passes one grouped immutable request to the
existing model-attempt coordinator after messages, system prompt, and tool
definitions are cloned. `modelFailoverRequirements` uses the established
message heuristic for messages/system, the complete detached tool-list JSON
including serializable `ToolInfo.Extra`, and overflow-safe saturating addition.
Only the final count reaches role resolution; schemas and request content do
not enter attempt events or logs.

The existing resolver remains the context-window authority. An insufficient
known alternate therefore retains `candidate_skipped/context_window` before
route construction. It consumes no attempt, switch, provider call,
provider-usage admission, or wait, and a later sufficiently large alternate
can still own the first switch.

## Compatibility And Rollback

The repair changes no configuration, portfolio metadata, provider adapter,
candidate order, retry/switch/deadline budget, error taxonomy, entrypoint
projection, public API, or durable Session schema. It does not add output-token
reservation or provider billing tokenization.

Rollback restores message-only requirements in the coordinator constructor. It
requires no data migration but reopens G36 and can again waste a switch/provider
call on a system- or tool-heavy smaller-context alternate.

## Evidence

The production-path fixture separately proves system-prompt and complete tool
definition admission, including `Extra`; it pins no route construction or
dispatch for the smaller candidate and later-alternate completion. Existing
P29.4/provider tests, the engine package, the focused race pack, all four
Makefile gates, docs/queue/manifest checks, and `git diff --check` pass on the
closeout tree. The detailed commands and limitations are in the
[verification record](../../verification/p46-1-complete-prompt-footprint.md).

No live-provider, physical-terminal, or remote-CI claim is made.
