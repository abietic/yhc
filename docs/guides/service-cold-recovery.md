# Restore Cooperative Services After Container Teardown

**Status:** current
**Last verified:** 2026-10-25

> **Ownership:** the operator-only service checkpoint protocol, logical clock
> policy, cold restore verification and remaining agent continuation gates

The service coordinator can export a quiescent group, reconstruct its state in
new containers and verify its clocks before activation. It requires services
that explicitly implement the checkpoint protocol. The ordinary YHC solver,
task prompts and model tools do not invoke this protocol.

**A verified service restore does not authorize an agent continuation.** Every
receipt still contains `can_resume_agent: false`: workspace restoration, native
session loading, cumulative provider usage and a new finite spending allowance
need separate proofs. Existing benchmark captures without service exports and
clock state remain unsupported, including captures made before this protocol.

## Qualify the services first

This version supports a closed, quiescent service group with no uncheckpointed
background execution, external effects, shared volumes or in-flight channels.
The trusted operator contract must enumerate every declared service and all six
state domains: `durable`, `memory`, `rng`, `clock`, `background`, `external`.
Each domain is explicitly `snapshot` or `absent`; missing/unsupported domains
block capture. `background` and `external` must be `absent` in this version.
These are audited service contracts, not conclusions inferred from files.

The Docker backend also requires:

- One live container per declared service, a pinned image identity and a fresh
  process identity on the restore target; source and target images must match.
- Immutable root filesystems, dropped capabilities, private PID/IPC state,
  isolated networking without published ports, and no host bind mounts.
- Separate project-owned data volumes, or private tmpfs; source volumes cannot
  be reused as target volumes.
- The labels `io.yhc.checkpoint-cohort=UUID` and
  `io.yhc.checkpoint-role=owned-service` on every participant; fresh targets
  additionally carry `io.yhc.restore-attempt=UUID`.

The role is for operator-owned services whose control hooks are inaccessible
to the task agent. An agent-writable program or agent container is not made
trustworthy by adding this label. Do not expose privileged import/clock control
through ordinary task APIs. No hooks are injected into benchmark services.
Different Docker daemons and arbitrary process-memory recovery are unsupported.

## Choose the clock's actual semantics

Every participant exports its application clock, including its rational rate.
The operator fixes its policy before capture; the agent cannot choose one while
restoring. A separate clock used only by a probe is insufficient.

| Policy | Frozen restore value | Activation behavior |
|---|---|---|
| `logical_pause` | Preserve the captured logical value | Advance from a shared future activation instant |
| `logical_elapsed` | Add elapsed wall time since capture at the captured rate | Also account for time spent verifying and preparing activation |

Restored clocks stay frozen until all services pass their probes. Activation
uses a common future instant and subsequent progression uses each new
process's monotonic clock. No OS clock is changed. Unsupported policies,
backwards time, configuration/rate mismatch, excessive downtime and overflow
fail closed. Activation also has a monotonic deadline: a stalled or reversed
wall clock quarantines the target group and leaves the claim consumed.
This does not pause or rebase an existing task's wall clock unless
its original trusted service contract explicitly defines that behavior.

## Capture, reconstruct, verify

Use private POSIX host input and control directories with mode `0700`; input
and control files use `0600`. Keep them outside task mounts. Captured payloads
and manifest are sealed `0400` in a `0500` capsule directory. The trusted control
registry must be outside that capsule and must not be writable by its importer.
The definition is JSON data used for complete service enumeration; the tool
does not execute Compose includes, create containers or pull images.

The operator provisions source containers with the required labels and their
audited control hooks, then runs:

```bash
python3 -m scripts.terminal_bench.cold_recovery capture \
  --contract /absolute/private-dir/contract.json \
  --definition /absolute/private-dir/source-definition.json \
  --project source-project --cohort-id COHORT_UUID \
  --snapshot /absolute/private-dir/snapshot \
  --control-dir /absolute/private-control-dir
```

All participants first drain and freeze at one barrier identity. The
coordinator then exports state and queries independent actual-state probes.
It writes, flushes and seals every payload, the manifest and capsule directory
before registering the digest outside the capsule. Restore also verifies the
sealed file/directory modes, so an interrupted seal cannot qualify.

Retain the returned `snapshot_sha256` independently; restore requires this exact operator-supplied
digest and the trusted capture record. Rewriting a payload and its manifest
hash together cannot replace that binding. A partial capture has no complete
trusted record and does not imply resumability. Capture leaves the sources
quiescent, rather than silently
restarting them after a failure.

After preserving the snapshot, the operator removes the old group and
provisions fresh target containers with separate storage and a new attempt ID:

```bash
python3 -m scripts.terminal_bench.cold_recovery restore \
  --contract /absolute/private-dir/contract.json \
  --definition /absolute/private-dir/target-definition.json \
  --project target-project --attempt-id ATTEMPT_UUID \
  --snapshot /absolute/private-dir/snapshot \
  --control-dir /absolute/private-control-dir --snapshot-sha256 CAPTURE_SHA256 \
  --receipt /absolute/private-dir/restore-receipt.json --activate
```

Without `--activate`, the result is `services_restored_quiescent`. With it,
every component must first match its captured state digest and expected frozen
clock. Before release, the old containers and other instances of this cohort
must be absent from the same daemon. A durable local activation claim is
consumed once per captured digest in the trusted control registry before any
release. Copying the capsule does not create another activation allowance in
that registry. It is not a distributed lease; control records must not be
copied/cleared to retry a partial release.

After scheduled activation, actual state and advancing clock bounds are
queried again. Only then can the result be `services_active_verified`. A
partial import, generation change or failed activation quarantines the admitted
fresh target group. Quarantine is not rollback of side effects; a failed or
interrupted claim remains consumed and requires explicit reconciliation. No
model request is made in any of these steps.

## Implement the control adapter

The private host contract has `version: 1`,
`consistency: quiescent-closed-world`, positive `max_downtime_ns`, bounded
`clock_skew_ns`, and a `services` map. Each service specifies `schema`, its
six-domain `coverage`, `clock_policy`, `rate_num`, `rate_den`, and a literal
`command` argv beginning with an absolute executable path in its immutable
image. Commands run inside owned containers, never as host shell code.

Requests carry `protocol: yhc-service-checkpoint-v1`, a fresh `request_id`,
the cohort's `barrier_id`, and an `operation`. Replies echo protocol/request ID,
carry a process-local `instance_id`, `ok`, and a `body`:

| Operation | Required observation or effect |
|---|---|
| `capabilities` | Schema, exact coverage/policy/rate, phase, drained count and pristine-target status |
| `quiesce` | Reject new mutations, drain current work, freeze the actual clock and bind the barrier |
| `export` | Complete JSON state for every snapshotted non-clock domain plus the frozen clock snapshot |
| `import` | Restore into a pristine frozen target; retain its new process identity |
| `probe` | Digests computed from actual restored components, barrier/phase/drained count and actual clock value |
| `activate` | Schedule release at the supplied future wall instant |

Probe digests must use canonical JSON: sorted keys, ASCII escaping, compact
separators and no nonfinite numbers. Do not merely echo the imported checkpoint
or its expected digests. The clock snapshot schema and bounds belong to
[`RecoverableClock`](../../scripts/terminal_bench/service_clock.py).

## Reproduce the independent evidence

```bash
python3 -m unittest scripts.terminal_bench.test_service_clock \
  scripts.terminal_bench.test_service_snapshot

YHC_COLD_RECOVERY_DOCKER_TEST=1 python -m unittest \
  scripts.terminal_bench.test_service_snapshot_docker
```

The Docker test requires a locally cached Python image; set
`YHC_RECOVERY_DOCKER_IMAGE` to select one. It constructs only an owned fixture
image without registry lookup, destroys the original containers and volumes,
then verifies SQLite values, heap counters, the next RNG draw and both clock
policies through ordinary business APIs. Separate faulty importers omit RAM,
RNG or clock restoration and must stop before release. The fixture is not a
claim that unmodified benchmark services implement this protocol.

[`ServiceSnapshots`](../../scripts/terminal_bench/service_snapshot.py) owns the
capture/import/probe barrier and local activation claim;
[`DockerServices`](../../scripts/terminal_bench/cold_recovery.py) owns isolated
Docker transport and resource scope. The
[legacy retention assessor](../../scripts/terminal_bench/recovery.py) continues
to report old file/container captures as insufficient for agent cold resume.
