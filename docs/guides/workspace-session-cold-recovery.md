# Verify a Workspace, Native Session and Usage After Cold Restore

**Status:** current
**Last verified:** 2026-10-26

> **Ownership:** the operator-only file/native/usage recovery bundle, its
> binding to cooperative service proofs, and its remaining execution boundary

This workflow restores a supported workspace into fresh storage, loads its
Session through the production QueryEngine administration path, reconstructs
historical usage and verifies cooperative services under one capture identity.
The receipt is `restored_components_verified`. It always keeps
`can_resume_agent: false`: automatic execution handoff and a new finite spending
authorization are separate, unimplemented gates here. No Agent or provider
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
| `session_id` | Exact expected Session UUID from the launcher |
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
restored by loading messages. Shell, Agent and MCP state, external file targets,
arbitrary interrupted side effects, unknown charges and a new spending grant
remain outside this proof. Ordinary resume is unchanged. No capture, restore
or proof inspection starts a model request or reopens the old checkpoint writer.

## Reproduce without a paid provider

```bash
python3 -m unittest scripts.terminal_bench.test_recovery_bundle.HistoryTests
python3 -m unittest scripts.terminal_bench.test_paused_recovery
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

The positive fixture destroys original containers and volumes, checks Git
staged/unstaged state and file metadata, loads committed tool history without
replaying its effect, and independently queries restored service state. A
damaged native record must quarantine both target groups before service
activation. These fixtures establish recovery mechanics, not a benchmark
reward or live-provider reliability claim.

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
