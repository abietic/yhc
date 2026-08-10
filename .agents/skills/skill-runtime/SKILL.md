---
name: skill-runtime
description: Apply admission-gated, privacy-conscious telemetry to YHC project skills, including operational lifecycle, Terra delegation adoption, explicit ROI experiments, terminal closure, and focused reports. Use when another project skill crosses a logging trigger or when maintaining skill telemetry. Do not use as a standalone product workflow.
---

# Skill Runtime

Own telemetry mechanics and data minimization for a calling project skill. The
caller owns task behavior, evidence, and terminal meaning. Never start a run for
`skill-runtime` itself.

Codex hooks do not provide a verified per-skill `finally`. Treat every command
below as an explicit protocol; open runs diagnose missing closure rather than
proving that work is still active.

## Admit Logging Before Work

Start one audit run before the first action that meets any trigger:

- spawn a Terra child;
- modify repository or external state;
- run final repository gates or the iteration workflow;
- execute a long or risk-bearing runtime, migration, security, concurrency,
  persistence, recovery, or compatibility workflow;
- satisfy an explicit telemetry request.

Skip logging when the invocation remains short, local, read-only, has no Terra
delegation, performs no final gate, and is not a measurement experiment. If the
task later crosses a trigger, start immediately before that action. Never create
a retrospective run or claim that unlogged work was measured.

```bash
python3 .agents/skill-runtime/skill_log.py start \
  --skill <calling-skill> \
  --kind <stable-task-kind> \
  --scope <path-or-contract-id> \
  --measurement-mode audit
```

Keep the returned `run_id` and `log_file`. Nested skills share the caller run
unless they are genuinely separate measured work; use `--parent-run-id` and
`--root-run-id` only for that case.

## Keep ROI Experimental

Use ROI mode only for a predeclared cohort with a measured historical or
parallel-control baseline and available task/parent token counters. Audit mode
cannot be relabeled as ROI after completion.

```bash
python3 .agents/skill-runtime/skill_log.py start \
  --skill <calling-skill> \
  --kind <stable-task-kind> \
  --scope <experiment-scope> \
  --measurement-mode roi \
  --experiment-id <stable-experiment-id>
```

Missing price data keeps cost ROI unavailable even when benefit comparison is
ready. Never infer tokens, duration, price, or savings.

## Record Only Decision-Bearing Milestones

Use `skill_log.py event` only when an event changes routing, acceptance, a gate,
or the terminal diagnosis. A short audited run may need no milestone between
start and finish. Do not emit keep-alive, narration, duplicated phase progress,
or the same result again at finish.

Allow only structured IDs, paths, statuses, counts, gate results, and finite
metrics. Never encode prompts, prose, source, diffs, transcripts, argv, command
output, credentials, cookies, private context, or secret-derived values.

## Account for Terra Delegation

Before spawning, run `skill_log.py delegate-start` with executor, stable
`task-kind`, low-cardinality `task-family`, mode, scope, and available child task
ID. Use one of `exploration`, `review`, `implementation`, `testing`,
`documentation`, or `other` for the family.

After the child terminates, run `skill_log.py delegate-finish` with its observed
outcome and only runtime-provided metrics. After parent review, run
`skill_log.py delegate-assess` with `accepted`, `partial`, `rejected`, or
`not_actionable`. Child completion, parent adoption, caller-worktree validation,
repository gates, and final merge are separate facts.

## Use Focused Reports

```bash
python3 .agents/skill-runtime/skill_log.py report --profile operational --format markdown
python3 .agents/skill-runtime/skill_log.py report --profile delegation --format markdown
python3 .agents/skill-runtime/skill_log.py report --profile roi \
  --experiment-id <stable-experiment-id> --format markdown
```

Use `--profile full` only for forensic detail. Operational and delegation
profiles intentionally group by stable skill and task family instead of raw,
high-cardinality task/category strings.

## Finish Every Started Run

After all delegation assessments and the final decision-bearing event, run:

```bash
python3 .agents/skill-runtime/skill_log.py finish \
  --log-file <log-file> \
  --run-id <run-id> \
  --outcome <success|failure|blocked|cancelled> \
  --category <stable-terminal-category>
```

If logging fails, restore safety-critical terminal or temporary state first,
report `logging_failed`, and make no measured-efficiency claim.
