# Verify a Workspace, Native Session and Usage After Cold Restore

**Status:** current
**Last verified:** 2026-10-26

> **Ownership:** the operator-only file/native/usage recovery bundle, its
> binding to cooperative service proofs, and explicitly funded benchmark dispatch

This workflow restores a supported workspace into fresh storage, loads its
Session through the production QueryEngine administration path, reconstructs
historical usage and verifies cooperative services under one capture identity.
The receipt is `restored_components_verified`. It always keeps
`can_resume_agent: false`: the explicit dispatch operation below needs a separate
finite grant and fresh joined checks. No Agent or provider
request is started by capture or restore.

## Qualify the capture

Start with a fresh [cooperative service snapshot](service-cold-recovery.md).
Its original services must remain quiescent while the workspace is captured.
The coordinator probes their actual state both before and after collecting
files; matching names or an old retained-container report are insufficient.

The workspace must be one explicitly owned passive container with:

- An immutable image containing the current YHC binary at `/opt/yhc/yhc` and
  [`workspace_io.py`](../../scripts/terminal_bench/workspace_io.py) at
  `/opt/checkpoint/workspace_io.py`, with `/usr/local/bin/python3` available.
- A `/bin/sleep infinity` keeper and no other running process. An active Agent,
  background command or arbitrary process heap is not checkpointed.
- Read-only root storage, dropped capabilities, private PID/IPC state, no
  networking or published ports, and one exclusive, owned local data volume.
- Labels `io.yhc.workspace-role=idle-native-workspace` and
  `io.yhc.workspace-cohort=COHORT_UUID`. Fresh targets also carry
  `io.yhc.restore-attempt=ATTEMPT_UUID`.

The data volume contains the entire workspace, including canonical
`.yhc/transcripts` state. The workspace path, image, numeric user identity and
Docker daemon must match on source and target; container and volume identities
must differ. Global state outside the workspace and path remapping are
unsupported in this version.

Files are bounded to 4 MiB each, 64 MiB total and 4096 tree entries. Bytes,
ordinary permission bits, executable bits, empty directories and modification
times are preserved. Links, special files, extended attributes, special mode
bits and mixed owners are rejected. New inodes and creation/change times are
not preserved. The host stores numbered opaque blobs; it never extracts task
paths or executes workspace programs.

## Observe a real native load

For a local, explicitly selected Session, the opt-in command is:

```bash
yhc sessions resume SESSION_ID --recovery-proof --output-format json
```

It uses the same canonical admission and `SessionService.ResumeInfo` path as
ordinary Session administration. The new field contains a fingerprint of
actual loaded engine messages, the restored native usage summary, identity,
loader nonce and finite blockers. It contains no prompt or response text.
Without the flag, the existing output is unchanged.

This is a real restore and may persist canonical recovery state; it is not a
read-only JSON inspection. The bundle runs it only inside its isolated owned
container. It rejects warnings, unknown usage coverage, Goal or graph-interrupt
state, pending inputs/callbacks, child lineage and external workspace state
that this version cannot completely verify. Passive file retention never
replaces this load test.

The opt-in proof also requires a closed native conversation turn. A transcript
ending at a user prompt, an unresolved tool call, or matched tool results without
a final assistant response reports `native_turn_incomplete`, even when all
reported usage so far is known. Metadata and API-error diagnostics do not
substitute for that final response. This stricter proof does not change
ordinary Session resume.

The optional `tool_boundary` is an observation of a committed tool-result
suffix, with a digest rather than arguments or output text. It rejects missing,
orphaned or reused tool identities, including unresolved earlier rounds.
`workspace_local` requires all historical calls to be `Read`, `Write` or `Edit`
with declared targets inside the captured root. File transport independently
rejects links and external mounts. This is evidence of declared file targets
and committed results; it does not attest to a past filesystem resolution or
restore process-local guards.

Native transcript usage and invocation usage have different coverage. Both
must retain their original facts; their totals are not forced equal. The
provider-free loader is not appended as another paid invocation.

## Capture one bound bundle

Use private host directories and the same trusted control registry as the
service snapshot. Supply the complete original invocation history as private
JSON with `version: 1`, `session_id`, ordered `segment_ids`, and `segments`.
Each segment has its one-based `ordinal`, the same `session_id`, and its raw
`usage` receipt with a single invocation `call_ledger`.

In this bundle protocol, `SESSION_ID` is the exact native Session name: 1–128
ASCII letters, digits, underscores, dots or hyphens, beginning with a letter or
digit. The shared [`session_identity`](../../scripts/terminal_bench/workspace_io.py)
validator accepts both production `session-<Unix-nanoseconds>` names and UUID
names; no layer renames, trims or normalizes them. Cohort, restore-attempt,
dispatch, reservation, invocation-segment and call identities remain canonical
UUIDs. A valid Session name never substitutes for any of those identities.

```bash
python3 -m scripts.terminal_bench.recovery_bundle capture \
  --service-contract /absolute/private-dir/service-contract.json \
  --service-definition /absolute/private-dir/source-services.json \
  --service-project source-project \
  --service-snapshot /absolute/private-dir/service-snapshot \
  --service-sha256 SERVICE_CAPTURE_SHA256 \
  --workspace-container SOURCE_CONTAINER_ID --cwd /workspace \
  --cohort-id COHORT_UUID --session-id SESSION_ID \
  --usage-history /absolute/private-dir/usage-history.json \
  --snapshot /absolute/private-dir/bundle \
  --control-dir /absolute/private-control-dir
```

Retain the returned `bundle_sha256` independently. The sealed bundle binds the
service snapshot digest, cohort/barrier, source workspace generation, every
file, actual native-load proof and complete supplied history. Its authority is
the external digest and private capture record, rather than a self-rewritten
manifest. Original monetary currencies, price snapshots and reservations are
retained; no new price or remaining allowance is inferred.

Missing, reordered or repeated invocation identities, duplicate call IDs and
cross-Session segments fail validation. Valid unknown/in-flight usage or
retained reservations can still be preserved in a capture, but restoration is
blocked before any target mutation. Invalid receipts remain at their original
source; capture never overwrites historical logs or frees reservations.

## Restore and check the combined receipt

After preserving the sealed capture, remove the source containers. Provision
fresh service and workspace targets with one shared attempt ID and independent
storage. The workspace volume must be empty.

```bash
python3 -m scripts.terminal_bench.recovery_bundle restore \
  --service-contract /absolute/private-dir/service-contract.json \
  --service-definition /absolute/private-dir/target-services.json \
  --service-project target-project \
  --service-snapshot /absolute/private-dir/service-snapshot \
  --workspace-container TARGET_CONTAINER_ID --cwd /workspace \
  --cohort-id COHORT_UUID --attempt-id ATTEMPT_UUID \
  --snapshot /absolute/private-dir/bundle --bundle-sha256 BUNDLE_SHA256 \
  --control-dir /absolute/private-control-dir
```

The coordinator restores and checks the full file tree first, then runs a new
native loader and compares its semantic fingerprint and native usage to the
capture. User files must remain unchanged. Canonical state writes inside
`.yhc` receive a separate post-load tree digest. Only after file, native and
financial coverage checks pass are services restored and activated.

The durable combined receipt is `bundle-receipt.BUNDLE_SHA256.json` in the
control registry. It ties the target generations, workspace digests, actual
native proof, cumulative usage and service state/clocks to the same cohort,
barrier and attempt. Both its `can_resume_agent` and the accounting record's
`new_spending_authorized` remain false.

A partial import, loader failure, proof mismatch or publication failure stops
only the admitted target groups and leaves the single-use claim consumed.
This is quarantine, not rollback. Retain the capture and history, inspect the
failure, and explicitly reconcile the attempt; do not clear claims or
automatically retry a budget- or infrastructure-stopped task.

## Join a clean tool pause to closed accounting

The opt-in v2 bundle can preserve and verify a budget pause after a committed
workspace file-tool round. The native proof itself still reports
`native_turn_incomplete`. The joint protocol accepts that one blocker only
when an externally recorded invocation receipt and the exact checkpoint agree.
Every other native blocker remains fatal.

The trusted launcher must record the terminal JSON result and a subsequent
provider-free native proof outside the task workspace. Assemble one private
JSON record with these fields, then retain its SHA-256 independently:

| Field | Required source |
|---|---|
| `version`, `scope` | `1`, `yhc-operator-tool-pause-v1` |
| `session_id` | Exact expected native Session name from the launcher, using the Session-name contract above |
| `segment_id` | Exact invocation UUID from that terminal receipt |
| `checkpoint_path` | Relative path under `.yhc/usage-checkpoints/` captured in the workspace |
| `terminal` | Original `yhc exec --output-format json` result, including raw `usage` |
| `native_proof` | Original `sessions resume --recovery-proof` result's `recovery_proof` field |

The native messages, native usage and tool-boundary digest must match a fresh
source load. The terminal result must be a `run_budget_exceeded` failure with a
real denied admission and at least one known call. Its usage must be the last
segment of the complete supplied history. The immutable image's `yhc usage
inspect` then reads the expected segment: admission must be closed, phase
`sealed`, tokens and cost complete, and all reservations reconciled. The
terminal persistence sequence must match. The receipt and stored snapshot bind
every field apart from the later terminal elapsed time, its persistence-status
wrapper, and an unspecified route effort's documented `""` to `"unknown"`
representation. Native and invocation token totals still retain their different
coverage.

Add these arguments to the capture command above:

```bash
  --pause-record /absolute/private-dir/operator-pause.json \
  --pause-sha256 PAUSE_RECORD_SHA256
```

The digest uses the same canonical JSON encoding as
[`service_snapshot.digest`](../../scripts/terminal_bench/service_snapshot.py).
This external record is a trusted operator assertion of which invocation
belongs to which Session. Checkpoint checksums alone do not authenticate that
relationship. Do not generate it from task-authored JSON or infer it by matching
token totals. Existing v1 captures are not upgraded.

Capture seals both checkpoint files, the host record, source native observation
and joint proof with the workspace/service bundle. Restore uses the same command
as v1. After importing the files it repeats the actual native load and checkpoint
inspection in fresh processes, compares the exact joint proof, and activates
services last. A mismatch quarantines both target groups and consumes the same
single-use restore claim. An open-but-fully-priced checkpoint remains rejected.

The receipt's `paused_joint_proof` verifies this component boundary, not a
ready-to-run Agent. It keeps `tool_runtime_restored: false`,
`can_resume_agent: false` and `new_spending_authorized: false`; the outer receipt
also reports `tool_runtime_state_not_integrated`. In particular, the
[`file-read guard`](../../tools/read_state.go) is process-local and is not
restored by loading messages alone. The explicit launch below rehydrates a
separately captured, verified owner in the dispatch process. Shell, Agent and MCP state, external file targets,
arbitrary interrupted side effects, unknown charges and a new spending grant
remain outside this proof. Ordinary resume is unchanged. No capture, restore
or proof inspection starts a model request or reopens the old checkpoint writer.

## Restore file guards and explicitly dispatch a new turn

Add `--capture-read-guards` to the source `yhc exec --output-format json`
invocation. It creates an isolated guard owner and emits `read_guards` in the
terminal JSON. `complete: true` means the saved hashes still match actual
file-tool observations. A failed capture reports `complete: false`; it preserves
the ordinary result and accounting but cannot supply a cold handoff. Old
captures without this field cannot be upgraded by guessing read credit.

After restoring the v2 bundle, the trusted host can call
[`cold_tool_handoff`](../../scripts/terminal_bench/paused_recovery.py) with the
anchored operator record, current native proof, retained checkpoint and original
ordered history. It repeats the joint checks and prepares a bounded handoff
object; it does not start execution or authorize spending. Serialize it into a
private host-owned regular file in a private directory and independently retain
the SHA-256 of those exact file bytes. Transfer it through the trusted control
channel, outside the task's editable inputs.

The host still owns cumulative usage, fresh service/clock and workspace proofs,
the immutable image, route/configuration identity and a separate finite spending
authorization. The CLI handoff verifies the exact native state and source
invocation checkpoint; it does not replace these host-level gates. Do not build
this input from task-authored JSON or treat its checksum as authentication of
an untrusted launcher.

An explicitly approved new turn can then use this shape; replace the uppercase
identifiers and paths with checked inputs, including the separately authorized
limit in the current price card's native currency:

```bash
yhc exec "Continue the saved task." --resume SESSION_ID \
  --output-format json \
  --cold-tool-handoff /private/control/handoff.json \
  --cold-tool-handoff-sha256 HANDOFF_FILE_SHA256 \
  --usage-checkpoint-dir /workspace/.yhc/usage-checkpoints/NEW_INVOCATION \
  --timeout 900s --max-provider-calls 20 \
  --price-card /private/control/current-prices.json \
  --reserve-cost-before-dispatch --max-output-tokens-per-call 16384 \
  --max-estimated-cost NEW_NATIVE_CURRENCY_LIMIT
```

Capture and cold handoff currently require JSON output; JSONL/TUI and automatic
benchmark dispatch are not integrated. The operator launcher below is a separate
explicit operation. These options are off by default. Normal
resume and default budget behavior are unchanged. The cold path requires an
explicit resume, a fresh checkpoint namespace, finite timeout/call limits and
cost reservation; it never imports old limits or reopens the old writer.

Before the shared agent traversal starts, the CLI reads the handoff against its
external digest, validates the actual closed source checkpoint, checks loaded
native state and recreates file read credit against current bytes. The
source-invocation lock and existing-claim check precede native resume; an
exclusive claim is fsynced under `.yhc/cold-dispatch-claims/` before dispatch.
Changing JSON whitespace or choosing another new usage segment cannot reopen
that source claim. Failures after the claim, including route-revision rejection,
denied reservation or process death, keep it consumed. Preserve the records and
reconcile explicitly; do not delete claims to retry automatically.

`cold_recovery` in the terminal result identifies the exact old and new segments,
native digest, restored guard count and consumed claim. Its scope is file-tool
state restoration for an **explicit new user turn**, not resumption of an old
Graph frame or tool replay. New usage is counted in the new invocation. The
source checkpoint and earlier history remain unchanged, and original component
receipts continue to report `can_resume_agent: false`.

## Dispatch through the benchmark recovery launcher

[`ColdBenchmarkLauncher.dispatch`](../../scripts/terminal_bench/cold_dispatch.py)
connects a restored v2 bundle to the same production JSON CLI handoff. Capture
and restore still start no Agent. The launcher starts at most one explicit new
turn per source invocation; it neither loops through grants nor changes the
solver. The ordinary Harbor adapter continues to require same-live-runtime
continuity. Historical sealed batches and legacy retained containers do not
gain this capability.

Before dispatch, the launcher reloads the anchored bundle and private restore
claim/receipt. It checks the current workspace generation and complete file
tree, performs a fresh native load and closed-checkpoint inspection, and
re-probes every cooperative service's state, process identity and advancing
clock. The activation epoch and service instances must match the original
host claim. Older receipts without those observations are blocked. Observation
timestamps may change; container/process/image/storage identity may not.

The operator must provide a private, independently anchored JSON grant with
exactly the fields accepted by `validate_grant`:

| Fields | Required meaning |
|---|---|
| `version`, `scope` | `1`, `benchmark-cold-dispatch-v1` |
| `dispatch_id` | Fresh canonical UUID; also names the fresh checkpoint namespace |
| `bundle_sha256`, `attempt_id` | The original bundle digest and exact completed restore attempt |
| `usage_history_sha256` | Canonical digest of the complete original ordered history |
| `expires_unix` | Integer UTC epoch seconds; enough validity for the entire new timeout |
| `currency`, `max_cumulative_estimated_cost` | Native currency and cumulative Session envelope, including historical estimates |
| `binary_sha256` | Independently verified immutable `/opt/yhc/yhc` artifact |
| `price_card` | Full current upper-bound card, with explicit API models and the same native currency |
| `route` | Exactly `provider`, `model`, `base_url`; the existing native route guard also applies |
| `tools` | Explicit list of tool names; an empty list keeps CLI defaults |
| `limits` | Exactly `max_provider_calls`, `max_total_tokens`, `max_output_tokens_per_call`, `max_estimated_cost`, `timeout_sec` |

The grant digest uses the same canonical encoding as the bundle digest,
not a SHA of arbitrary JSON whitespace. Obtain it from the trusted authorizer;
hashing task-authored JSON does not turn it into permission. Calls must be
1–200, output tokens 1–65536, and timeout 1–900 seconds. A zero total-token
threshold disables only that threshold. The new cost cap must be positive.

The current keeper protocol requires Docker `network_mode: none`. Before any
native probe, source claim or cohort allocation, `validate_gateway_transport`
therefore requires a literal loopback IP in `route.base_url`, HTTP(S), and a
valid port (1–65535 when explicit). Host/external addresses, DNS names including
`localhost`, scoped or IPv4-mapped IPv6 and ambiguous shorthand addresses are blocked without
DNS lookup or a connection attempt. The optional host transport below adds a
bounded pipe while keeping this network profile; without it the route must
already be reachable inside the isolated container. Neither choice proves real
gateway account coverage or provides a spending grant.

The optional [host-pipe fixture](../../scripts/terminal_bench/test_host_gateway_pipe_docker.py)
keeps an independent synthetic-only byte-channel calibration. Its immutable
[test helper](../../scripts/terminal_bench/testdata/host_gateway_pipe.py) forwards
opaque JSON request and response-body bytes over bounded, nonce-bound standard
input/output frames to an
owned host-loopback fake provider. It supports only a literal synthetic
credential, the fixture's Responses path and fixed headers; it reconstructs
HTTP framing rather than preserving every HTTP header. It performs no DNS,
proxy lookup,
redirect following or automatic retry. An absolute deadline also bounds slow
headers; frame, request and response sizes are capped. It verifies byte counts,
checksums and request sequence, without interpreting provider usage.

The fixture inserts the bridge only after production cohort admission, then
removes it before the workspace's post-dispatch process inventory. It exercises
actual native pause/destroy/restore/dispatch, read guards and closed cumulative
usage. The host checks each real CLI admission checkpoint and the cohort claim
before replying. A truncated HTTP stream must retain unknown usage and the
full new cohort allocation; a denied shared budget must start no bridge or
request. User-file progress and cooperative service state are checked again.
A helper that exits during startup must be reaped with closed pipes and no
relay thread, while the consumed claim and complete allocation remain retained
and the owned workspace is quarantined with its data volume intact.

This original fixture remains test-only. The separate operator transport below
uses the production launcher and keeper. Successful byte transfer grants neither
resume authority nor spending authority and is not provider-usage closure.
Real funding/account coverage and provider billing closure remain separate
unverified requirements. Do not use the fixture helper for paid traffic.

A loopback address is only a necessary transport condition. It does not prove
that a trusted gateway is ready or grant spending permission. The independently
anchored grant, token, joined restore proof and budget gates still apply.
Historical grants and closed cohort usage retain their original route metadata;
the new-dispatch transport policy does not rewrite or invalidate their ledger
replay.

All historical usage must be complete, uniquely identified and free of retained
or outstanding reservations. The launcher adds its known native estimates
without rewriting earlier rows, then reserves the full new cap inside the
cumulative envelope. Different currencies are blocked; no exchange rate is
guessed. The launcher additionally requires the shared cohort ledger below.
Neither envelope is an account balance or an invoice. A funding controller must
still check account coverage, price freshness and the gateway's per-request
reservation policy, then provide an opaque gateway token. The launcher reads
only `YHC_BENCH_GATEWAY_TOKEN`; it does not fall back to host provider keys.

### Admit spending across Sessions

[`CohortAdmission`](../../scripts/terminal_bench/cohort_admission.py) owns a
private host ledger outside every task mount. The trusted authorizer supplies
one plan and its independent canonical digest. Its exact schema is:

| Field | Meaning |
|---|---|
| `version`, `scope` | `1`, `benchmark-cohort-admission-v1` |
| `cohort_id`, `currency` | The captured cohort UUID and one native settlement currency |
| `max_estimated_cost`, `expires_unix` | Shared positive decimal ceiling and UTC expiry covering the entire new timeout |
| `sessions` | 1–128 objects, each with exactly `session_id`, `max_estimated_cost`, `usage_history` |
| `external_reservations` | Up to 128 objects with exactly `reservation_id` (UUID), positive `amount`, and `state` (`outstanding` or `retained`) |

Each Session history uses the complete ordered history format above. Invocation
and call UUIDs must be unique across the entire inventory. Every Session's grant
must fit both its plan ceiling and the shared ceiling. The plan attests the
supplied cohort inventory; it cannot discover omitted spending or operations
through another launcher. Keep all work against this budget on this single
ledger. Never clone, rewind, delete or initialize another ledger to represent
the same funds. The host operator and independent anchors remain trusted.

Initialize once, using the authorizer's private plan, then reuse the exact
ledger path and digest for every participating Session:

```bash
python3 -m scripts.terminal_bench.cohort_admission init \
  --plan /absolute/private-control-dir/cohort-plan.json \
  --plan-sha256 COHORT_PLAN_CANONICAL_SHA256 \
  --ledger-dir /absolute/private-control-dir/cohort-ledger

python3 -m scripts.terminal_bench.cohort_admission inspect \
  --plan-sha256 COHORT_PLAN_CANONICAL_SHA256 \
  --ledger-dir /absolute/private-control-dir/cohort-ledger
```

Initialization requires a new directory and never resets an existing ledger.
An imported incomplete history or external hold is conserved and blocks all
new dispatch. Mixed currencies, stale/reset histories, unknown Sessions, reused
source/dispatch identities, and unverified accounting are also blocked. There
is no automatic migration, hold release, top-up or retry command.

One nonblocking POSIX lock spans the whole dispatch, so this entrypoint runs
one Session per cohort at a time. A competing process fails before native
probes. With verified accounting, the gate requires enough headroom for the
**full new cost cap**, counting every Session's known estimates and all holds.
Insufficient funds leave the journal, histories and workspace progress intact;
no source claim is consumed. A checked admission is repeated immediately before
the durable reservation and single-use dispatch claim.

The append-only, hash-chained journal syncs a high-water intent, event and
directory before acknowledging a reservation. Missing, changed, torn or
unsealed files fail closed. After execution, the launcher reconciles the actual
checkpoint and terminal usage again before replacing that reservation with the
new verified cost. Unused allocation becomes available to other Sessions.
Unknown/partial accounting, a crash or a failed settlement retains the full
allocation; any verified partial cost is counted within it, never added twice.
An abandoned reservation is retained on restart even when its process lock has
disappeared. Existing closed history rows remain unchanged. Explicit inspection
reports known estimates, outstanding/retained amounts, total exposure and the
updated per-Session histories; it authorizes no new call.

With a checked grant and token in the operator environment, use the target's
service project/definition and the original capture identity:

```bash
python3 -m scripts.terminal_bench.recovery_bundle dispatch \
  --service-contract /absolute/private-dir/service-contract.json \
  --service-definition /absolute/private-dir/target-services.json \
  --service-project target-project \
  --service-snapshot /absolute/private-dir/service-snapshot \
  --workspace-container TARGET_CONTAINER_ID --cwd /workspace \
  --cohort-id COHORT_UUID --attempt-id ATTEMPT_UUID \
  --snapshot /absolute/private-dir/bundle --bundle-sha256 BUNDLE_SHA256 \
  --dispatch-grant /absolute/private-control-dir/grant.json \
  --dispatch-grant-sha256 GRANT_CANONICAL_SHA256 \
  --cohort-ledger /absolute/private-control-dir/cohort-ledger \
  --cohort-plan-sha256 COHORT_PLAN_CANONICAL_SHA256 \
  --control-dir /absolute/private-control-dir
```

A host source-invocation lock precedes native probes. The launcher syncs its
single-use claim, complete original history and new exposure before dispatch.
The immutable container helper rechecks the tree and binary, passes the opaque
token through process memory, and starts the existing CLI with a new checkpoint
namespace. The Go handoff independently revalidates native/accounting/file
guards and consumes its own source claim. No task executable runs on the host.

The host retains an observation and reconciles the actual new checkpoint with
the terminal result before appending its invocation to cumulative history.
The container reader caps stdout at 8 MiB and stderr at 8 KiB while the child
runs. Output overflow, deadline or pipe failure kills the owned process group,
including pipe-holding descendants after the leader exits. A transport failure
or leftover process also quarantines the exact owned workspace and confirms it
stopped before returning. This container boundary covers descendants that leave
the original process group. Admission and quarantine require the explicit
Docker restart policy `no`; missing policy evidence is blocked. These
failures cannot produce a completion receipt; the data volume and claim remain.
Unknown usage, an open checkpoint, a transport failure, changed route or denied
reservation cannot reopen a consumed claim. Preserve the journal and new
checkpoint for explicit reconciliation. A successful receipt authorizes no
later retry; `automatic_paid_resume` remains false.

Budget exhaustion preserves progress but does not itself prove another cold
handoff is possible: the next source must still satisfy the supported paused
Session, tool, service, clock and closed-usage contract. Unsupported states stay
blocked even when funds remain. A source claim cannot be reopened with a new
grant or a different control directory; the shared ledger also remembers it.

This remains the explicitly cooperative, isolated keeper protocol above.
Service/workspace observations are point-in-time checks within that closed
world, not transactions against arbitrary external host actors. Network or
environment transitions, unsupported tool/process state and ordinary Harbor
cold recovery remain blocked. Offline fixtures exercise synthetic loopback
providers; they prove no paid gateway or model capability.

### Opt in to the host gateway transport

The optional
[`HostGatewayTransport`](../../scripts/terminal_bench/host_gateway_transport.py)
connects the existing CLI to a trusted host-owned HTTP gateway through the
keeper RPC's standard input/output. It is disabled when its two options are
absent. It currently supports the DeepSeek Responses route, an explicit numeric
loopback HTTP endpoint on both sides, and the captured native route/token
identity. DNS, proxies, redirects, other API protocols and network-profile
changes remain unsupported. The gateway owns provider credentials, account
coverage and its reservation policy; this transport reads no provider key and
implements no replacement budget or billing system.

Before capture, install the repository's `workspace_io.py` and
`host_gateway_transport.py` at their fixed `/opt/checkpoint/` paths in the same
immutable image as the CLI. Capture and restore must use that exact image.
Prepare a private host-owned JSON binding with exactly these fields:

| Field | Required value |
|---|---|
| `version`, `scope` | `1`, `benchmark-host-gateway-transport-v1` |
| `grant_sha256` | The separately anchored dispatch grant's canonical digest |
| `workspace_container_id` | Exact target container ID |
| `upstream_base_url` | Explicit numeric host-loopback HTTP URL and port, without credentials, query or path prefix |
| `gateway_token_sha256` | SHA-256 of the existing opaque `YHC_BENCH_GATEWAY_TOKEN`, never the provider key |
| `expires_unix` | UTC expiry covering the full granted timeout |

Anchor that binding's canonical digest independently, then add these options to
the dispatch command above:

```bash
  --host-gateway-transport /absolute/private-control-dir/transport.json \
  --host-gateway-transport-sha256 TRANSPORT_CANONICAL_SHA256
```

Pure binding and token checks precede the source lock. The existing consumed
check precedes every readiness probe; a losing/replayed dispatcher cannot probe
or change the winner's Session. Workspace inventory and the anchored image are
verified before invoking the immutable helper. Helper bytes and local port
availability are checked, followed by a host TCP readiness check with no HTTP
request. These checks precede cohort admission and are repeated before its
durable allocation. They establish point-in-time transport readiness, not
account, provider, price or billing verification.

The claim and complete new allocation remain durable before the CLI starts.
Nonce, sequence, integrity, route/model, output cap, actual call count and
expiry checks bound each forwarded request. Framing is capped at 8 MiB per
frame, requests at 4 MiB, and total response bytes at 8 MiB. The grant's call
window cannot be extended by the 15-second transport shutdown grace. After a
stream fault or non-2xx response, no later request is forwarded; only terminal
checkpoint readback can close the channel. Unknown usage retains its allocation.

The container binds a loopback server only while the existing immutable CLI
operation runs. Its task environment receives the opaque gateway token; the
host Docker client does not inherit provider-key or proxy variables. The
normal post-dispatch inventory still requires a passive keeper. A broken pipe,
unclosed server or leftover process retains the claim and allocation and
quarantines the owned workspace. Neither that failure nor a successful receipt
allows automatic paid retry or reopens the old source.

Captured change-review journals are a separate observability component. The
[revision journal](../../engine/filehistory/revisions.go) binds its frames to
the original filesystem root identity; copying its bytes to a new volume does
not make it replayable. The current
[review owner](../../engine/workspace_revisions.go) preserves an incompatible
journal and reports history unavailable. This file-tool dispatch proof does
not rebind that history or certify its restoration. Do not weaken that owner
check or describe copied review files as recovered review capability.

## Reproduce without a paid provider

```bash
python3 -m unittest scripts.terminal_bench.test_recovery_bundle.HistoryTests
python3 -m unittest scripts.terminal_bench.test_recovery_bundle.SessionIdentityTests
python3 -m unittest scripts.terminal_bench.test_paused_recovery
python3 -m unittest scripts.terminal_bench.test_cohort_admission
go test ./engine/session ./engine ./cmd/yhc/cmd \
  -run 'Test(ValidateHistory|ResumeInfoWithRecoveryProof|RecoveryProof|SessionsResumeRecoveryProof)'
```

The file-transport tests use Linux extended-attribute APIs. On a macOS host,
the real Docker round trip supplies that evidence. Build a Linux YHC binary
and the owned `testdata/native_session_seed` fixture for the cached image's
architecture, then set `YHC_NATIVE_RECOVERY_BINARY`, `YHC_NATIVE_RECOVERY_SEED`
and `YHC_NATIVE_RECOVERY_DOCKER_TEST=1` before running
`python3 -m unittest scripts.terminal_bench.test_recovery_bundle_docker`.

The default cached fixture is `yhc-generic-concurrency:20261020-arm64`; override
`YHC_NATIVE_RECOVERY_BASE` only with an already cached matching Git/Python image.
The test installs no packages and pulls no images.

For the host-pipe proof, set both `YHC_NATIVE_RECOVERY_DOCKER_TEST=1` and
`YHC_HOST_GATEWAY_PIPE_DOCKER_TEST=1` and run
`python3 -m unittest scripts.terminal_bench.test_host_gateway_pipe_docker`.
It reuses the same cached image, Linux binaries and private evidence-directory
variables. The host requires POSIX descriptors. The module selects only its
host-pipe cases, rather than rerunning inherited native-only cases. Pure
framing, deadline, endpoint and disconnect checks run with
`python3 -m unittest scripts.terminal_bench.test_host_gateway_pipe` and are part
of `make test-provider-costs`.

For the production transport, use the same cached image/binary/proof-directory
setup with `YHC_NATIVE_RECOVERY_DOCKER_TEST=1` and
`YHC_HOST_GATEWAY_TRANSPORT_DOCKER_TEST=1`, then run
`python3 -m unittest scripts.terminal_bench.test_host_gateway_transport_docker`.
These cases use the actual optional launcher path without an injected RPC
wrapper, with known settlement, truncated unknown usage, budget denial,
unready gateway and killed-helper quarantine. Fake host-key canaries are
checked against restored workspace files. Pure framing, admission, fault and
process-cleanup checks run through
`python3 -m unittest scripts.terminal_bench.test_host_gateway_transport` and
`make test-provider-costs`. This proves offline wiring, not real funding,
provider billing, benchmark reward or model quality.

The positive fixture destroys original containers and volumes, checks Git
staged/unstaged state and file metadata, loads committed tool history without
replaying its effect, and independently queries restored service state. A
damaged native record must quarantine both target groups before service
activation. These fixtures establish recovery mechanics, not a benchmark
reward or live-provider reliability claim.

Additional launcher cases obtain a default Session ID from the production
QueryEngine constructor, rather than a test-authored UUID. They exercise a
closed cumulative settlement and ledger reopen after cold dispatch, as well as
unknown-usage retention and shared-budget exhaustion with progress preserved.
UUID-based cases remain as compatibility coverage. These checks do not make
an old capture with incomplete state or accounting eligible for continuation.

### Exercise the first production turn offline

The same opt-in Docker module also runs the public `yhc exec --resume` command
after destroying the source and restoring its workspace, native Session,
services and usage history. Its immutable fixture starts a deterministic
provider on the target container's loopback interface. The container keeps
`network_mode: none`; the child CLI receives only an allowlisted environment
and a literal fake credential. No real provider, host key or workspace program
is used.

The fake provider compares the exact ordered native history plus one new
prompt, including message roles and tool arguments/results. The fixture
separately validates the leading invocation-budget and date/CWD/platform
reminders, and the Responses bridge's exact blank assistant placeholder.
Leading system/developer context is outside the transcript projection; extra
or reordered conversation items are rejected.

The tests independently check unchanged user files and Git state, no repeated tool effect,
the persisted native usage increment, and assembly of the actual new invocation
receipt with the original ordered history. That combined test receipt is
written to a private control file and revalidated in a new trusted process
against its expected digest, bundle and attempt. This verifies fixture receipt
readback, not a production cumulative-ledger write transaction.

Service
memory, durable state, random state and advancing logical clocks are queried
again after the CLI exits. Appending the same new receipt twice must fail.

The cases distinguish execution from financial admission:

| Case | Required observation |
|---|---|
| Known usage | One synthetic request completes; native and invocation usage each gain their own reported increment. |
| New tool round | Two synthetic requests create one new file after cold restore; committed old effects stay unchanged and each new admission is read from the production usage checkpoint before HTTP response. |
| Unknown usage | One synthetic 503 response stops without a retry; the financial reservation remains retained and coverage stays incomplete. |
| Reservation denied | No provider request occurs; earlier work and accounting survive the budget stop. |
| Missing model identity | Component loading alone cannot identify a pricing route; execution stops with zero requests and `run_usage_unknown`. |
| Crash before response | The fixture SIGKILLs the CLI after durable admission; the full outstanding reservation and unfinished native user turn survive. |
| Crash after tool result | A new file and its native tool result commit once; SIGKILL during the following request leaves that request reserved and the native turn incomplete. The old restore claim cannot be reused. |

Use the binary, seed and Docker opt-in variables above with
`python3 -m unittest scripts.terminal_bench.test_recovery_bundle_docker`.
`YHC_NATIVE_RECOVERY_PROOF_DIR` optionally writes separate component and
offline-execution receipts; it must name an existing private, host-owned
absolute directory. This is checked before Docker provisioning. The history oracle can also be calibrated without
Docker using `python3 -m unittest scripts.terminal_bench.test_native_handoff_fixture`.

The new-tool fixture explicitly seeds edit permission in its owned Session;
it does not broaden restore permissions. It checks the exact new call/result
suffix in the second request, original files and Git state, the new native
usage increment, and a closed production invocation checkpoint with the same
call ledger and cost facts as the terminal receipt. See
[Invocation usage checkpoints](invocation-usage-checkpoints.md) for the opt-in
storage contract and failure handling. The earlier cumulative history readback
remains a fixture receipt, not a production cumulative-ledger transaction.

This proves controlled text and tool turns through the existing production
entrypoint. It does not integrate an automatic bundle dispatcher, reconcile
an interrupted provider call, authorize real spending or validate
arbitrary pending runtime state. The bundle and offline receipts continue to
report `can_resume_agent: false`; their histories and original claims are
retained. An old stopped benchmark is not automatically resumed.

The crash cases use a real killed CLI process, not a fabricated error receipt.
They inspect production accounting and reload the native Session in a fresh
process. Their deliberately incomplete history remains blocked; knowing an
earlier response's token count never proves that a later request or the current
turn finished. They do not establish reconciliation of unknown provider charges
or interruption in the middle of an arbitrary tool's external side effect.

The paused Docker cases run a real public CLI to its call-budget stop, inspect
its closed checkpoint, and destroy the original containers and volumes. They
restore its exact tool history, single file effect, accounting and service
state/clocks without dispatching again. A separate fault reopens the imported
checkpoint while retaining known usage; it must quarantine before services
activate. These are offline recovery checks and supply no benchmark reward
or paid-provider evidence.

The cold-launch cases additionally recreate the same synthetic model endpoint
after source destruction, restore the guards inside the real CLI process and
perform an Edit without another Read or replayed Write. They verify rejection
of changed bytes, open source accounting and a changed model route; budget
denial, unknown usage and SIGKILL retain the appropriate claims and liability.
A competing process runs while the winner is blocked in its first provider
request; the loser must leave the winner's transcript and claim byte-for-byte
unchanged. Traffic stays on the isolated container's loopback interface.
