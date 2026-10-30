# Isolated Benchmark Verification

**Status:** current
**Last verified:** 2026-10-30

> **Ownership:** operator admission, network isolation and terminal classification for the opt-in Harbor evaluator

The bounded evaluator uses a separate, disconnected container to grade copied
agent artifacts. This policy belongs to the evaluation controller; it does not
change `QueryEngine`, tool guidance or ordinary agent execution. The standard
Harbor CLI does not automatically install this policy.

## Configure the evaluator

Install the pinned [requirements](../../scripts/terminal_bench/requirements.txt).
Use `IsolatedVerifierTrial.create(config)` with an environment whose import path
is `scripts.terminal_bench.harbor_trial:IsolatedDockerEnvironment`, or a subclass
that preserves its checks. Pass an absolute host-only `verifier_proof_dir` in
`config.environment.kwargs`. Keep this directory outside container log mounts.
The adapter checks it against every agent and sidecar bind before startup;
external or opaque volume drivers remain unqualified because they can hide
host bindings. Proof lookup uses Harbor's actual session IDs, including its
shortened IDs for long trial names.

Each effective task or step verifier must declare a separate Linux environment
with an immutable, already cached `sha256:` image ID or `repository@sha256:`
reference (optionally including a tag before `@`). Its image must contain
the official tests and their dependencies. Shared verifiers, image builds,
Windows graders, sidecars and additional verifier mounts remain unqualified.
Tasks that require these need their own verified isolation contract before use.
Source-trial recovery is also blocked; this factory admits fresh trials only.

[`isolated_task`](../../scripts/terminal_bench/harbor_trial.py) copies the loaded
runtime configuration and makes each verifier's baseline and phase policies
`no-network`. It preserves the task files, instructions, artifacts and agent
network policy. [`IsolatedDockerEnvironment`](../../scripts/terminal_bench/harbor_trial.py)
then performs these checks in order:

1. Inspect the exact cached reference and resolve its Linux content ID; do not
   pull or build a grading image at dispatch.
2. Append a native Docker `network_mode: none` override and render the complete
   Compose configuration. Admit only the standalone grader and its own log bind.
3. Start the container and compare its actual image with the resolved content
   ID, independently checking networking, privileges and mounts. A repository
   manifest digest can differ from that content ID; record both reference and ID.
4. Write an exclusive host-owned proof before `start()` returns. Harbor can
   upload model-produced artifacts only after this point.

Network changes from this static environment are rejected. The container can
write its logs, but cannot access the host proof directory. A missing, stale or
conflicting proof blocks evaluation acceptance; keeping a container or archive
does not authorize resuming the agent.

## Classify closure independently of reward

Call [`classify_trial`](../../scripts/terminal_bench/trial_policy.py) using
host-owned accounting, native completion, checkpoint, deadline, isolation and
Harbor result receipts. `budget_denied` must reflect an actual controller or
native usage-ledger denial, never model text or an inferred shortage.

| Evidence | Ledger status |
|---|---|
| Closed known usage, native completion, verified capture and isolation, finite reward (including zero) | `completed` |
| Explicit budget denial with closed usage, capture and verified grading, without an independent controller/grader failure | `budget_paused` |
| Grading exception, timeout, missing reward, invalid capture or missing isolation proof | `failed` |
| Unknown, outstanding, missing or invalid accounting | `unknown_usage` |

Unknown accounting takes precedence and retains reservations. The result also
records a detailed reason so a grading failure cannot be presented as budget
exhaustion. Harbor can retain an earlier native exception while hiding a later
grader exception, so a missing grade still fails even when budget denial is
known. Every outcome keeps `can_resume_agent=false`; joint recovery proof
and new dispatch admission remain separate requirements. This helper neither
allocates money nor releases reservations.

## Reproduce offline checks

Run the pure classification checks with the system Python. Run the actual
Harbor factory checks in the virtualenv containing the pinned requirements:

```bash
python3 -m unittest scripts.terminal_bench.test_trial_policy
python -m unittest scripts.terminal_bench.test_harbor_trial
```

The Docker check is explicitly opt-in and requires a locally cached Linux image
with `/bin/sh`, `sleep`, and the normal Unix tools. It creates a trusted synthetic
test image from a never-started container; it never pulls, installs dependencies
or calls a model. Supply its immutable base image ID:

```bash
YHC_BENCH_DOCKER_TEST=1 YHC_BENCH_CACHED_IMAGE="$CACHED_FIXTURE_IMAGE_ID" \
  python -m unittest scripts.terminal_bench.test_harbor_trial_docker
```

To also exercise repository-digest resolution, supply an already cached
`YHC_BENCH_CACHED_REFERENCE=repository@sha256:...`. This additional case starts
and inspects the disconnected environment, comparing the reference with the
resolved content ID; it does not run that image's grader or install its tests.

The check runs Harbor's actual trial, artifact upload and grading path for
reward one, reward zero and a failed grader. At upload it verifies that the
host proof already exists, Docker reports `none`, and the artifact is absent
until that upload. It cleans only its own UUID-labelled resources. These are
offline execution proofs, not provider-quality, account-coverage, spending
authorization or cold-recovery evidence.
