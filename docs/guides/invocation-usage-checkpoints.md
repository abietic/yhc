# Retain Invocation Usage Across Process Exit

**Status:** current
**Last verified:** 2026-10-26

> **Ownership:** opting headless invocations into durable usage records and
> inspecting their accounting without activating an Agent or spending grant

`yhc exec --usage-checkpoint-dir` writes invocation accounting before every
provider dispatch and after every settlement. It is off by default. Budget
limits remain separate, optional flags; enabling persistence does not add a
limit or model-visible reminder.

This records money and token facts, not an executable task checkpoint. It does
not reopen a previous collector, import its budget, retry an uncertain request
or resume a Session. See [Workspace and Session cold recovery](workspace-session-cold-recovery.md)
for the separate file, native Session, service and clock proofs.

## Enable it for one new invocation

Use Linux or macOS and a local filesystem supporting file and directory sync.
The parent directory must already exist, belong to the current user, be mode
`0700`, and have a canonical absolute path without symlink components. Supply
a fresh child directory for each invocation. Existing nonempty namespaces are
rejected before provider construction.

```bash
mkdir -m 700 /absolute/private-usage
yhc exec 'implement the requested change' --output-format json \
  --usage-checkpoint-dir /absolute/private-usage/invocation-001
```

Use the normal provider configuration. With `--price-card`, the record includes
the original currency, decimal rates and estimates. Without a price card,
token facts can be complete while monetary coverage remains incomplete. No
exchange rate or vendor invoice amount is inferred.

All calls sharing the invocation's `RunUsage`, including children and auxiliary
calls, use the same owner. Each admission and terminal receipt synchronously
publishes a bounded snapshot using a synced temporary file, atomic replacement
and directory sync. In reservation mode, the admitted snapshot contains the
full existing worst-case reservation before a provider capability is returned.
A successful terminal write precedes execution of tools from that response.

The terminal result adds `usage.checkpoint` with `version`, the last confirmed
`sequence`, and `failed`. Existing results omit that field when disabled.
Snapshots contain bounded accounting labels and numeric facts, not prompts,
responses, raw provider errors or credentials. The directory is OS-private;
it does not protect against a hostile process running as the same user. Keep
it in trusted control storage rather than an untrusted task workspace.

## Inspect from a new process

Retain the invocation's `usage.call_ledger.segment_id` independently. If the
process exited before emitting a terminal result, retain the generated identity
from `owner.json` through the trusted control plane. Then run:

```bash
yhc usage inspect --checkpoint-dir /absolute/private-usage/invocation-001 \
  --segment-id EXPECTED_INVOCATION_UUID
```

This provider-free command checks the private namespace, exact invocation ID,
schema, checksum, fixed metadata, call identities, totals, prices and exposure
arithmetic. It prints the retained snapshot plus these distinct facts:

| Field | Meaning |
|---|---|
| `admission_closed` | No further admission was allowed before the retained write. |
| `token_usage_complete` | No in-flight, unknown or untracked usage remains. |
| `cost_complete` | The original price card covers the observed usage. |
| `accounting_complete` | Admission is closed and both usage and cost coverage are complete. |
| `can_resume_agent`, `new_spending_authorized` | Always false. |

A valid pending record can be inspected successfully while remaining incomplete.
A missing, malformed, replaced or unexpected file fails inspection. Checksum
and semantic checks detect corruption; they are not external authorization or
protection from a same-user rewrite of the entire namespace.

## Handle uncertain outcomes

Any checkpoint I/O failure permanently fences this collector with
`run_usage_unknown`. It cannot fall back to memory-only accounting, retry a
provider or execute tools from a response whose settlement was not persisted.
If vendor usage was already confirmed, the terminal result retains those token
and cost facts while marking `usage.checkpoint.failed: true`.

The recovery
manifest's raw per-invocation `usage_history.segments` retains that flag and
sequence for audit. Aggregate `totals` compresses persistence status to
`checkpoint_complete: false`; it is not the source for identifying the failed
segment and cannot promote the history into complete coverage.

A failed atomic publication can leave either the previous snapshot or a newly
visible snapshot, especially when rename succeeded but directory sync failed.
Do not interpret a write error as proof that nothing committed. Keep the
namespace and its original reservation; reconcile it before authorizing new
spending. Unrecognized temporary files also block inspection rather than being
silently removed.

After a crash, a settled-but-unclosed record proves the recorded receipts, not
completion of the invocation or its tool effects. A record closed with an
in-flight call remains incomplete. Closing the collector cannot cancel a
provider request or recover late usage: owners must stop children first, and
late settlement after storage closure fails closed. The opt-in collector also
refuses new admission at its bounded call-record capacity instead of discarding
history. Windows opt-in is rejected; default memory-only execution is unchanged.

## Maintainer evidence

- [`runHeadless`](../../cmd/yhc/cmd/headless.go) owns the opt-in and closes usage
  after engine shutdown; [`newUsageCommand`](../../cmd/yhc/cmd/usage.go) inspects
  records without constructing a provider or QueryEngine.
- [`RunUsage.EnableCheckpointDir`](../../engine/execution/run_usage_checkpoint.go)
  claims a fresh collector; `ReadRunUsageCheckpointDir` is validation-only.
- [`RunUsage.AdmitProviderUsage`](../../engine/execution/run_usage.go) and
  `runUsageCall.settleRequest` place persistence around the provider capability.
- [`runUsageCheckpointFileStore`](../../engine/execution/run_usage_checkpoint_store.go)
  owns rooted private I/O; the [semantic validator](../../engine/execution/run_usage_checkpoint_validate.go)
  independently checks retained arithmetic.
- [Execution tests](../../engine/execution/run_usage_checkpoint_test.go) cover
  process exit, injected failures, corruption and concurrent settlement;
  [CLI tests](../../cmd/yhc/cmd/headless_usage_checkpoint_test.go) verify durable
  admission before HTTP and prevent tool execution after terminal-write failure.

```bash
go test ./engine/execution -run TestRunUsageCheckpoint -count=1
go test ./cmd/yhc/cmd -run TestExecUsageCheckpoint -count=1
```

These tests use synthetic usage and a local fake provider. They make no paid
request and establish no benchmark reward or live-provider reliability.
