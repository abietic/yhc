# Verify a Workspace, Native Session and Usage After Cold Restore

**Status:** current
**Last verified:** 2026-10-25

> **Ownership:** the operator-only file/native/usage recovery bundle, its
> binding to cooperative service proofs, and its remaining execution boundary

This workflow restores a supported workspace into fresh storage, loads its
Session through the production QueryEngine administration path, reconstructs
historical usage and verifies cooperative services under one capture identity.
The receipt is `restored_components_verified`. It always keeps
`can_resume_agent: false`: native execution handoff and a new finite spending
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

## Reproduce without a paid provider

```bash
python3 -m unittest scripts.terminal_bench.test_recovery_bundle.HistoryTests
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
