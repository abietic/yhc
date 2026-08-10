# Headless Goal Execution Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-29; Eino-Agent `ee49540ff719`, Codex
`66bd101fff6f`, Claude Code Ripe `4b9d30f79532`, OpenCode
`411eff73f026`, and Crush `2af939d8e900`
**Last verified:** 2026-07-29

> **Ownership:** source-backed evidence for one explicit bounded headless Goal
> process contract. This report does not own current behavior or execution
> order. Root [`migration/PLAN.md`](../../PLAN.md) owns promotion.

## Conclusion

Eino-Agent needs a separate headless Goal runner, not a loop added to ordinary
`exec` or compatibility `-p`. The runner should resume one saved root Session,
consume only its existing durable Goal continuation, and stop on a durable
Goal halt, an explicit process limit, cancellation, or failure.

The recommendation is `combine` within P24's accepted `adapt` program:

- preserve Eino-Agent's Goal state, cursor, exact usage ledger, budget,
  permission, and final-admission owners;
- adapt Claude Code Ripe's explicit turn and budget bounds;
- adapt Codex's exact turn-terminal and nonzero-failure process discipline;
- adapt Crush's run-correlated authoritative terminal signal; and
- keep OpenCode's ordered JSON records as evidence for machine cleanliness,
  without adopting session-idle as Goal completion.

The smallest safe surface is `eino-agent goal run`. It is an execution plane
for an already configured Goal, not another Goal control plane. It requires an
exact saved Session ID and a positive continuation limit. Only a durable
`complete` Goal is process success.

## Frozen Question

> How should explicit bounded headless Goal execution own process lifetime and
> terminal JSON or exit semantics without changing ordinary `exec` or `-p`?

The comparison covers process ownership, terminal identity, output purity,
cancellation, permissions, persistence, and exit status. It excludes Goal
creation/editing, daemonization, ACP negotiation, provider selection, and
default-on rollout.

## Current Eino-Agent Boundary

### One-shot headless behavior

[`headless.go`](../../../../cmd/yhc/cmd/headless.go) defines
`newExecCommand` and `runHeadless`. They resolve one prompt, construct one
`QueryEngine` with `commands.EntrypointHeadless`, optionally resume a Session,
call `SubmitMessage` once, drain one event stream, render one result, and exit.
Root `-p` calls the same function.

The current version-1 JSON result already separates `status`, `output`,
`session_id`, `terminal_reason`, `exit_code`, and a bounded error object.
`ExitCode` maps success to `0`, general failure to `1`, usage errors to `2`,
and cancellation to `130`. Headless permission checks have no prompt owner:
they fail closed unless the caller explicitly selects bypass.

These are ordinary one-shot contracts. A resumed Session may contain an active
or paused Goal, but `runHeadless` neither claims a Goal continuation nor stays
alive for one. Changing that behavior would make an existing automation
command unexpectedly spend more tokens and own a longer process lifetime.

### Goal runtime behavior

[`goal_runtime.go`](../../../../engine/goal_runtime.go) exposes a detached
`GoalSnapshot` with exact Goal and objective revisions, lifecycle status and
reason, usage coverage, token budget and usage, continuation ordinal, last
Goal turn, blocker evidence, and durability availability.

[`goal_capability.go`](../../../../engine/goal_capability.go) currently enables
the Goal workflow only for explicitly enabled saved root TUI or Plain
Sessions. [`queued_input.go`](../../../../engine/queued_input.go) exposes a
dedicated Goal subscription and claim without widening generic runtime-item
selection. [`goal_continuation.go`](../../../../engine/goal_continuation.go)
revalidates the exact durable cursor before provider entry and submits it
through the canonical QueryEngine turn path.

The durable Goal statuses are `active`, `paused`, `blocked`,
`usage_limited`, `budget_limited`, and `complete`. An `active` Goal can still
be unable to run because an exact permission or question owns input, no
eligible cursor exists, persistence is uncertain, or a process-level bound has
been reached. A process result therefore cannot treat `active` as success or
invent a lifecycle transition.

## Reference Evidence

### Codex: exact turn completion and process failure

Codex's explicit `exec` surface is implemented by
`.reference/codex/codex-rs/exec/src/lib.rs` and
`event_processor_with_jsonl_output.rs`. `run_exec_session` waits for the exact
primary thread and turn to publish `TurnCompleted`; failed, interrupted, or
server-error terminals produce process failure. It unsubscribes and shuts down
the client before returning.

The JSONL projector emits typed thread, turn, and item records. Focused
`server_error_exit`, `mcp_required_exit`, and `ephemeral` tests prove nonzero
failure and explicit persistence opt-out. This is strong evidence for exact
terminal identity and orderly shutdown, but Codex `exec` still owns one turn,
not a durable Goal lifecycle.

### Claude Code Ripe: explicit long-run bounds and typed result

`.reference/claude-code-ripe/src/cli/print.ts` accepts explicit maximum turns
and budget, owns SIGINT cancellation, drains its active query, and emits one
typed final result. Its SDK schemas distinguish success from execution,
maximum-turn, and maximum-budget failures and report turns, usage, cost, and
Session identity.

This supports a bounded process contract and a structured final result. It
does not justify copying a vendor USD budget, proactive/background-agent drain
loop, or stream-JSON protocol. Eino-Agent already owns provider-neutral token
accounting and a durable Goal cursor.

### OpenCode: clean ordered records, but session idle is too weak

`.reference/opencode/packages/opencode/src/cli/cmd/run.ts` exposes explicit
`run`, resume flags, and `--format json`. JSON mode writes one object per line
with type, timestamp, Session identity, and typed payload. The subprocess tests
prove parseable newline-delimited records, pure error output for a rejected
request, ordered reasoning/tool/text records, prompt failure as nonzero, and
bounded SIGINT exit.

The loop stops when the Session reports `idle`. Its tests also preserve exit
zero for an unknown stream finish after partial output. Both choices are
insufficient for Goal automation: idle does not mean the objective is
complete, and an ambiguous provider finish cannot be reported as success when
durable accounting or continuation state is uncertain.

### Crush: authoritative exact-run terminal

`.reference/crush/internal/cmd/run.go` mints a per-call `RunID`, subscribes
before sending the prompt, ignores foreign events on the same Session, and
exits only on the matching `RunComplete`. The terminal event carries final
assistant text so stdout can be reconciled even when cross-broker delivery is
out of order.

`run_stream_test.go` proves that tool-use and end-turn message parts do not
terminate the process, foreign RunIDs cannot contaminate stdout, partial
output is reconciled once, and a matching error returns failure. This is the
right identity lesson. Eino-Agent should use its existing exact Goal cursor,
Goal-turn identity, and durable terminal state rather than add Crush's
client/server protocol or a second RunID store.

## Evidence Matrix

| Concern | Verified evidence | Project decision |
|---|---|---|
| Invocation | Eino-Agent `exec` and `-p` are one-shot; all references make non-interactive execution explicit. | Add distinct `goal run`; do not add a Goal flag or loop to ordinary one-shot commands. |
| Durable target | Only Eino-Agent already has the accepted Goal state, exact cursor, usage ledger, and recovery owner. | Resume an existing saved root Goal; do not create, edit, resume, clear, or budget it in this slice. |
| Process bound | Claude exposes explicit turn/budget bounds; Eino-Agent already requires a positive Goal token budget. | Require positive `--max-continuations`; keep the durable token budget authoritative. No implicit infinite loop or daemon. |
| Terminal identity | Codex waits for the exact turn; Crush filters by exact RunID and one authoritative completion event. | Drive only the exact claimed Goal item and decide process completion from the post-turn durable Goal snapshot. |
| Machine output | Eino-Agent has a versioned final JSON envelope; OpenCode proves newline-clean structured stdout. | Emit one versioned final Goal result on stdout. Send progress and tool diagnostics to stderr. Do not add JSONL in P24.5b. |
| Success | Codex errors are nonzero; Goal completion is stricter than Session idle or turn completion. | Exit `0` only for durable `complete`; non-complete halts and process limits exit `1`. |
| Cancellation | Existing CLI uses `130`; references cancel their owned run context. | Stop the admitted turn, let QueryEngine persist the cancellation outcome, emit a final cancelled result, and exit `130`. |
| Permissions | Current headless has no interactive prompt owner. | Preserve fail-closed permission behavior. Waiting input is a nonzero resumable outcome, never an auto-approval. |
| Recovery | Eino-Agent already reconciles cursor, receipt, rejection, and usage uncertainty. | Resume first, inspect exact state, and consume only an eligible recovered cursor. Never infer work from transcript prose. |

## Recommended Observable Contract

### Invocation

```text
eino-agent goal run \
  --resume <saved-session-id> \
  --max-continuations <positive> \
  [--output-format text|json] \
  [runtime flags]
```

`--resume` and `--max-continuations` are required. The command accepts no
prompt and never reads stdin as an objective. It does not mutate Goal controls.
The Session must contain a valid Goal and the existing Goal feature must be
enabled.

The command installs a distinct internal
`commands.EntrypointHeadlessGoal` composition identity that is absent from
ordinary `exec` and `-p`. It may expose the existing root-turn `get_goal` and
`update_goal` tools only during the exact Goal continuation; it does not expose
slash-command control or widen child/review, ACP, administration, or
standalone-MCP behavior.

### Process loop

1. Build the canonical QueryEngine, preserve headless permission policy, and
   resume the exact Session.
2. Inspect the durable Goal before any claim. Return immediately for a missing,
   unavailable, paused, blocked, limited, or complete Goal.
3. For an active Goal, claim only the exact dedicated Goal continuation and
   submit it through `SubmitGoalContinuation`.
4. Drain the whole canonical turn to its authoritative terminal, then re-read
   the durable Goal snapshot. A turn terminal alone is never Goal success.
5. Continue only while the Goal remains active, another exact cursor is
   eligible, context is live, persistence is certain, and the explicit
   continuation count remains below its limit.
6. On a process limit, emit a resumable nonzero result without changing the
   active Goal. On cancellation, stop admission and wait for the engine-owned
   durable pause/settlement boundary before rendering the final result.

An active Goal with no claimable cursor is reported as `waiting_input` or
`not_runnable` from observed engine state; it is not polled and does not keep a
daemon alive.

### Final result and exit mapping

JSON stdout is one object with:

- `schema_version` and `kind: "goal_run"`;
- run `status`, `terminal_reason`, and `exit_code`;
- exact `session_id`;
- a detached Goal projection containing identity/revisions, lifecycle status
  and reason, token budget/usage, usage coverage, and continuation ordinal;
- `continuations` and `max_continuations`;
- the final continuation's assistant `output`, if any; and
- a bounded redacted `error` object when applicable.

The closed run-status vocabulary is `complete`, `paused`, `blocked`,
`budget_limited`, `usage_limited`, `waiting_input`, `not_runnable`,
`continuation_limited`, `cancelled`, and `failed`. A just-driven canonical turn
that emits its waiting-input terminal maps to `waiting_input`; an active Goal
with no initial or post-turn eligible cursor and no just-observed waiting
terminal maps to `not_runnable`. Missing, unavailable, corrupt, provider, and
persistence failures use `failed` with a stable bounded error code rather than
inventing a Goal lifecycle status.

The stable mapping is:

| Result | Exit |
|---|---:|
| Durable Goal `complete`, including already complete before provider entry | `0` |
| `paused`, `blocked`, `budget_limited`, `usage_limited`, `waiting_input`, `not_runnable`, or `continuation_limited` | `1` |
| Runtime, provider, persistence, invalid/corrupt Goal, or render failure | `1` |
| CLI usage or invalid flag combination | `2` |
| Process cancellation after durable stop handling | `130` |

Text mode may be human-readable, but it must describe the same outcome and
must not claim completion for a merely completed turn. JSON stdout contains no
progress lines or resume hints.

## Rejected Alternatives

- **Loop ordinary `exec` after a resumed active Goal:** rejected because it
  changes token spend and process lifetime for existing automation.
- **Use `--goal` on `exec`:** rejected because flag combinations obscure the
  separate execution and control contract.
- **Create or resume Goal lifecycle state from `goal run`:** deferred; this
  slice is a bounded runner over existing explicit user/host configuration.
- **Stop on Session idle or assistant end-turn:** rejected because neither is
  durable Goal completion and both can occur between tool or continuation
  steps.
- **Stream JSONL immediately:** deferred; P24.5b needs one stable terminal
  result, not a second event protocol.
- **Exit zero for paused or limited state:** rejected because automation would
  mistake incomplete work for success.
- **Retry persistence uncertainty:** rejected because another provider call
  could duplicate cost or work without an authoritative cursor/usage record.

## Promotion Gate

Implementation must prove exact CLI validation, no change to ordinary `exec`
or `-p`, already-terminal no-provider behavior, multi-continuation success,
process-limit resumability, waiting-input fail-closed behavior, stable
text/JSON output, redaction, exit `0/1/2/130`, cancellation settlement,
persistence failure, exact-session isolation, and no unsupported entrypoint
capability. Deterministic process fixtures and focused race tests must pass
with the repository and documentation gates.
