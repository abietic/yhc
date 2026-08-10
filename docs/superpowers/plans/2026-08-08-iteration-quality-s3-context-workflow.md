# Iteration Quality S3 Context And Workflow Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-08
**Completed:** 2026-08-09
**Plan state:** Executed; shared workflow, optional hooks, pre-push evidence, and retired compatibility alias

> **Ownership:** third executable stage of the accepted
> [Iteration Quality Kernel design](../specs/2026-08-08-iteration-quality-kernel-design.md),
> consuming the evidence contract from
> [S1](2026-08-08-iteration-quality-s1-policy-planner.md) and the verification
> runner from [S2](2026-08-08-iteration-quality-s2-verification-e2e.md)

**Goal:** Reduce repeated agent instructions and phase declarations by routing
all iterations through one tracked workflow skill, injecting only small dynamic
state through trust-gated Codex hooks, and preventing pushes whose committed
diff lacks current merge evidence.

**Architecture:** `iteration-workflow` becomes the reasoning owner for
plan-to-evidence execution. Domain skills retain only their domain-specific
invariants and call the shared workflow. A project `.codex/hooks.json` invokes
one small repository adapter that ignores raw hook payloads, reads the S1/S2
structured state, invalidates stale evidence after tracked mutations, records
mechanical child lifecycle, and allows at most one stop continuation. The
existing Git pre-push hook consumes committed-tree `evidence_ready`; it does not
rerun repository gates.

**Tech Stack:** Tracked Markdown skills, Go 1.26.5 hook adapter code in
`scripts/iteration`, project `.codex/hooks.json`, Bash pre-push hook, the
existing skill-runtime validator/tests, and the documented Codex command-hook
JSON protocol.

## Global Constraints

- Start only after S2 has merged and a clean committed branch can produce
  `evidence_ready` through `make verify-merge`.
- Project hooks are trust-gated and bypassable. They are context and lifecycle
  automation, not a security boundary and not proof that an agent followed a
  skill.
- Use only `SessionStart`, `PostToolUse`, `SubagentStart`, `SubagentStop`,
  `Stop`, and `SessionEnd`. Do not configure `UserPromptSubmit`,
  `PreToolUse`, `PermissionRequest`, or transcript parsing for this workflow.
- Hook input may contain prompt, transcript path, tool input, tool response,
  argv, source, credentials, and assistant text. Decode only allowlisted
  structural fields and ignore all other keys. Never echo or persist discarded
  values.
- Persist only hashed session filenames, stable session/turn/child IDs, agent
  type, event, repository-relative owner paths, branch/base/head/diff identity,
  gate status, duration/counts, and bounded booleans. Store files under the
  ignored `build/iteration/hooks/` tree with mode `0600`.
- `SessionStart` returns no output when nothing is actionable. Otherwise its
  `additionalContext` is at most 2,048 UTF-8 bytes even though Codex also has an
  approximate token spill threshold.
- `PostToolUse` never uses `tool_input` or `tool_response` to infer mutation. It
  recomputes the S1 tracked diff and emits no model-visible output.
- A pre-existing tracked diff is not attributed to the current session. Record
  the initial digest at `SessionStart`; set `created_tracked_change` only after
  a later digest differs.
- `Stop` continues at most once, only when this session created a tracked change
  and current evidence is stale. Honor both the hook's persisted one-shot bit
  and Codex's `stop_hook_active` input.
- `SessionEnd` is advisory and cannot keep a task alive. Mark an open local
  record incomplete and return no output.
- Hooks cannot decide skill telemetry admission, parent adoption, or a verified
  per-skill `finally`. Keep those judgments in `skill-runtime`.
- Preserve operation with hooks disabled or untrusted. The public Make commands
  and skills remain complete without hook execution.
- Preserve the current `EINO_ALLOW_MASTER_PUSH=1` recovery behavior. Add a
  separate, explicit stale-evidence recovery variable; never overload the
  protected-master bypass.
- Do not copy personal preferences or conversation history into repository
  instructions.

---

## Locked Hook Interfaces

The adapter accepts one JSON document on stdin. It intentionally does not use
`json.Decoder.DisallowUnknownFields`, because real hook inputs contain sensitive
fields that this program must ignore rather than retain.

```go
type HookInput struct {
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"`
	AgentID        string `json:"agent_id"`
	AgentType      string `json:"agent_type"`
	StopHookActive bool   `json:"stop_hook_active"`
	Reason         string `json:"reason"`
}

type HookSessionState struct {
	SchemaVersion        int               `json:"schema_version"`
	SessionID            string            `json:"session_id"`
	InitialDiffDigest    string            `json:"initial_diff_digest"`
	CurrentDiffDigest    string            `json:"current_diff_digest"`
	Branch               string            `json:"branch"`
	Base                 string            `json:"base"`
	Head                 string            `json:"head"`
	PlanState            string            `json:"plan_state"`
	CreatedTrackedChange bool              `json:"created_tracked_change"`
	StopContinued        bool              `json:"stop_continued"`
	Open                 bool              `json:"open"`
	Children             map[string]string `json:"children"`
}
```

Hook subcommands are exact:

```text
iteration hook session-start
iteration hook post-tool-use
iteration hook subagent-start
iteration hook subagent-stop
iteration hook stop
iteration hook session-end
```

The adapter rejects an event/subcommand mismatch, more than one JSON document,
input larger than 256 KiB, a CWD outside the current repository root, invalid
IDs, or a state-file symlink. Invalid hook input fails locally without exposing
the input value.

## File Structure

| File | Responsibility in this stage |
|---|---|
| `.agents/skills/iteration-workflow/SKILL.md` | Shared reasoning path from plan through committed evidence. |
| `.agents/skills/iteration-closeout/SKILL.md` | Temporary compatibility wrapper, then deletion after the canary gate. |
| `.agents/skills/*/SKILL.md` | Remove duplicated repository-gate prose and route to the shared workflow. |
| `AGENTS.md` | Keep only stable product, safety, architecture, public command, and routing rules. |
| `scripts/iteration/hook.go` | Allowlisted hook decoding, state transitions, and output. |
| `scripts/iteration/hook_test.go` | Lifecycle, privacy, size, invalidation, and disabled-hook tests. |
| `.codex/hooks.json` | Trust-gated project lifecycle configuration. |
| `.codex/hooks/iteration.sh` | Resolve Git root and invoke the versioned Go adapter. |
| `.gitignore` | Track the project hook config/adapter while retaining other local Codex state ignores. |
| `.githooks/pre-push` | Preserve protected-master rule and require current committed evidence. |
| `.githooks/pre-push_test.sh` | Deterministic stdin/ref/evidence/bypass tests. |
| `docs/contributing/verification.md` | Public workflow and bypass evidence semantics. |
| `docs/contributing/documentation-policy.md` | Root-instruction and owner-doc boundaries. |

### Task 1: Introduce the tracked shared iteration workflow

**Files:**

- Create: `.agents/skills/iteration-workflow/SKILL.md`
- Modify: `.agents/skill-runtime/validate_skills.py`
- Modify: `.agents/skill-runtime/test_skill_log.py`

**Interfaces:**

- Produces skill `iteration-workflow` for any implementation, documentation, or
  defect-fix iteration that changes repository state or needs final evidence.
- Consumes `make change-plan`, `make verify-focused`, `make verify-merge`, and
  `make change-evidence` only; it does not reconstruct gate selection.
- Keeps `skill-runtime` as the telemetry mechanics owner.

- [ ] **Step 1: Add a validator test for the new skill contract**

Extend the skill validator fixtures to require:

- valid YAML frontmatter with exact name;
- explicit `skill-runtime` admission;
- the four public Make commands;
- clean committed-tree merge verification before push;
- blocked/not-applicable reporting; and
- an explicit statement that a child result is not parent acceptance.

Reject duplicate raw lists of `make fmt`, `make lint`, `make test`, and
`make build` outside the shared skill after caller migration.

- [ ] **Step 2: Run the validator and verify red**

```bash
python3 .agents/skill-runtime/validate_skills.py
python3 -m unittest .agents/skill-runtime/test_skill_log.py
```

Expected: validator FAIL because `iteration-workflow` is absent; telemetry
tests still pass.

- [ ] **Step 3: Create the concise skill**

Use this frontmatter and section contract:

```markdown
---
name: iteration-workflow
description: Plan, implement, verify, and hand off one evidence-bound eino-agent repository iteration. Use for any code, test, documentation, or defect-fix change that needs risk-selected checks and current committed-tree merge evidence. Do not choose product scope, replace domain skills, or treat blocked checks as success.
---

# Iteration Workflow

## Admit and scope

Apply `$skill-runtime` before the first write, delegation, or final gate. Inspect
status, preserve unrelated changes, and run `make change-plan`. If a migration
slice is accepted, pass only its active `slice_id`; an empty queue is valid.

## Deliver one vertical slice

Use the applicable domain skill, add the lowest stable failing test first, make
the smallest cohesive change, and run `make verify-focused` after each coherent
step. A required blocked or failing check stops completion.

## Commit, then verify merge evidence

Format and inspect the scoped diff, commit explicit paths on a topic branch,
then run `make verify-merge` on the clean committed tree. If formatting changes
the tree, commit and rerun. Run `make change-evidence` and require current
`evidence_ready` before push.

## Hand off honestly

Report local gates, applicable risk packs, blocked/not-applicable checks,
remote CI, live-provider, PTY, and physical UI evidence separately. Child
completion or review is not parent acceptance or proof for the caller tree.
```

Do not repeat Terra field allowlists or all Make recipes; link to
`skill-runtime`, `AGENTS.md`, and the verification documentation.

- [ ] **Step 4: Validate and commit**

```bash
python3 .agents/skill-runtime/validate_skills.py
python3 -m unittest .agents/skill-runtime/test_skill_log.py
git add .agents/skills/iteration-workflow/SKILL.md .agents/skill-runtime/validate_skills.py .agents/skill-runtime/test_skill_log.py
git commit -m "feat(skills): add shared iteration workflow"
```

### Task 2: Migrate domain skills and slim root instructions

**Files:**

- Modify: `.agents/skills/iteration-closeout/SKILL.md`
- Modify: `.agents/skills/migration-loop/SKILL.md`
- Modify: `.agents/skills/migration-slice/SKILL.md`
- Modify: `.agents/skills/reference-parity-audit/SKILL.md`
- Modify: `.agents/skills/runtime-depth-change/SKILL.md`
- Modify: `.agents/skills/tui-runtime-change/SKILL.md`
- Modify: `.agents/skills/write-docs/SKILL.md`
- Modify: `.agents/skills/defect-investigation/SKILL.md`
- Modify: `.agents/skills/skill-runtime/SKILL.md`
- Modify: `AGENTS.md`
- Modify: `scripts/docs_check/main_test.go`

**Interfaces:**

- Every domain skill calls `$iteration-workflow` for shared iteration mechanics.
- `iteration-closeout` becomes a compatibility alias with no private gate list.
- Root instructions retain the current docs-check ownership anchors:
  `QueryEngine`, `projectGraphQueryKernel`, and the Query Engine architecture
  link.

- [ ] **Step 1: Add migration tests before editing prose**

Extend skill validation and docs-check tests to assert:

- all prior `$iteration-closeout` callers now name `$iteration-workflow`;
- only the compatibility wrapper may mention `$iteration-closeout`;
- domain skills still retain their unique invariants and trigger boundaries;
- root `AGENTS.md` still contains the three current runtime-owner anchors;
- root instructions name `make change-plan`, `make verify-focused`,
  `make verify-merge`, and `make change-evidence`; and
- root instructions do not duplicate the telemetry allowlist or individual
  risk-pack selector lists.

- [ ] **Step 2: Replace `iteration-closeout` with a wrapper**

Its body becomes:

```markdown
# Iteration Closeout Compatibility

This name is retained temporarily for existing callers. Apply
`$iteration-workflow` from its “Commit, then verify merge evidence” section.
Do not run a second gate list or open a second telemetry run. Remove this
wrapper after all tracked callers are migrated and one real PR has completed
the new workflow canary.
```

Keep frontmatter name/description valid and mark the skill as compatibility,
not final authority.

- [ ] **Step 3: Narrow each caller**

Apply these exact ownership rules:

| Skill | Retains | Removes/delegates |
|---|---|---|
| `migration-loop` | align/plan/execute phase routing and queue semantics | repository gate checklist |
| `migration-slice` | accepted Ready slice, adoption decision, compatibility | final gate mechanics |
| `reference-parity-audit` | comparative evidence and adoption recommendation | changed-tree closeout |
| `runtime-depth-change` | ordering, cancellation, fallback, recovery, lifecycle invariants | final gates |
| `tui-runtime-change` | reducer, queue, projection, PTY, terminal lifecycle invariants | final gates |
| `write-docs` | source-backed ownership, lifecycle, examples, diagrams | repository gate mechanics |
| `defect-investigation` | reproduction, causal diagnosis, regression-test placement | final evidence promotion |
| `skill-runtime` | logging admission, minimization, delegation accounting, terminal finish | iteration phase or gate selection |

Each caller ends with one short `$iteration-workflow` handoff and does not copy
the four Make commands.

- [ ] **Step 4: Replace root `AGENTS.md` with a stable routing document**

Keep these sections only:

```text
project direction and current architecture owners
language/build basics and flat tools invariant
protected-master and explicit-path commit safety
public iteration commands and required clean committed merge evidence
domain-skill routing and Terra role summary
reference-adoption vocabulary with PROJECT_DIRECTION.md as owner
```

Move detailed test taxonomy to `docs/contributing/testing-strategy.md`,
documentation lifecycle to `docs/contributing/documentation-policy.md`, Terra
accounting fields to `skill-runtime`, and target selection to
`quality/iteration.yaml`. Do not remove the current `make verify` definition or
claim hooks are mandatory.

- [ ] **Step 5: Run validators and commit**

```bash
python3 .agents/skill-runtime/validate_skills.py
python3 -m unittest .agents/skill-runtime/test_skill_log.py
go test ./scripts/docs_check -run 'AgentRuntimeOwnership|AgentInstructions' -count=1
rg -n '\$iteration-closeout|make fmt|make lint|make test|make build' .agents/skills -g 'SKILL.md'
git add AGENTS.md .agents/skills .agents/skill-runtime/validate_skills.py scripts/docs_check/main_test.go
git commit -m "docs(agent): consolidate iteration context"
```

Expected: only the compatibility wrapper contains `$iteration-closeout`; raw
four-gate prose lives in the shared workflow/verification owners, not every
domain skill.

### Task 3: Implement the privacy-bounded hook state machine

**Files:**

- Create: `scripts/iteration/hook.go`
- Create: `scripts/iteration/hook_test.go`
- Modify: `scripts/iteration/main.go`
- Modify: `scripts/iteration/main_test.go`

**Interfaces:**

- Produces the six locked `iteration hook` subcommands.
- Consumes S1 plan and S2 evidence through Go functions, not Markdown or shell
  output parsing.
- Persists one state file named by
  `sha256(session_id) + ".json"` under `build/iteration/hooks/`.

- [ ] **Step 1: Add event transition tests**

Use fake planner/store dependencies and assert:

1. session start with no diff and no pending check emits nothing;
2. session start with a changed diff emits concise branch/base/state/pending
   targets;
3. post-tool-use with unchanged digest changes nothing;
4. post-tool-use after a new tracked digest sets
   `created_tracked_change=true` and updates current digest;
5. a pre-existing changed digest alone never sets that bit;
6. subagent start records `agent_id -> running`; stop records `stopped`;
7. first stop with session-created change and stale evidence returns one block
   decision and sets the one-shot bit;
8. second stop or `stop_hook_active=true` returns no output; and
9. session end marks `open=false` and incomplete state without stdout.

- [ ] **Step 2: Add adversarial privacy tests for every event**

For each supported event, include all of these unknown fields in stdin:

```json
{
  "transcript_path": "TRANSCRIPT_SECRET_MARKER",
  "prompt": "PROMPT_SECRET_MARKER",
  "tool_input": {"command": "ARGV_SECRET_MARKER"},
  "tool_response": "COMMAND_OUTPUT_SECRET_MARKER",
  "last_assistant_message": "SOURCE_SECRET_MARKER",
  "credential": "CREDENTIAL_SECRET_MARKER"
}
```

Merge the fields with the valid structural event fixture. Assert none of the
markers occurs in stdout, stderr, state files, plan/evidence files, or failure
diagnostics. Also prove a second JSON document and a 256-KiB-plus-one input fail
without reflecting content.

- [ ] **Step 3: Add UTF-8 output budget tests**

Generate long ASCII and multi-byte owner/target summaries. Assert serialized
`additionalContext` is valid UTF-8, at most 2,048 bytes, preserves branch/base
and state first, truncates owner/target detail last, and returns no spill-prone
raw source.

- [ ] **Step 4: Implement allowlisted decoding and state transitions**

Use a limited reader, decode only `HookInput`, deliberately allow unknown keys,
and require EOF. Validate the command event against `HookEventName`. Use
confined atomic writes equivalent to the S2 evidence store.

Session-start JSON has this exact shape:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "Iteration: branch=... base=... state=... pending=..."
  }
}
```

Stop continuation has this exact shape:

```json
{
  "decision": "block",
  "reason": "This session created a tracked change and current iteration evidence is stale. Run the risk-selected verification or record the blocking gate before stopping."
}
```

All other successful no-context events write zero stdout bytes.

- [ ] **Step 5: Run tests and commit**

```bash
go test ./scripts/iteration -run 'Hook|SessionStartBudget|PrivacyMarker' -count=1
git add scripts/iteration/hook.go scripts/iteration/hook_test.go scripts/iteration/main.go scripts/iteration/main_test.go
git commit -m "feat(agent): add bounded iteration hook adapter"
```

### Task 4: Track and configure the project hooks

**Files:**

- Create: `.codex/hooks.json`
- Create: `.codex/hooks/iteration.sh`
- Create: `.codex/hooks/iteration_test.sh`
- Modify: `.gitignore`
- Modify: `docs/contributing/verification.md`

**Interfaces:**

- Uses Codex command-hook fields `type`, `command`, `timeout`, and
  `additionalContextLimit` only.
- Project hooks run only after Codex trust review and remain optional when the
  `hooks` feature is disabled.

- [ ] **Step 1: Add Git ignore exceptions before creating hook files**

Replace the current `.codex/*` exception block with:

```gitignore
.codex/*
!.codex/agents/
!.codex/agents/*.toml
!.codex/hooks.json
!.codex/hooks/
!.codex/hooks/iteration.sh
!.codex/hooks/iteration_test.sh
```

Do not unignore arbitrary `.codex` runtime state.

- [ ] **Step 2: Add the root-resolving adapter**

```bash
#!/usr/bin/env bash
set -euo pipefail

readonly repository_root="$(git rev-parse --show-toplevel)"
exec go -C "$repository_root" run ./scripts/iteration hook "$@"
```

The script writes nothing itself and passes hook stdin through to the Go
process. `iteration_test.sh` feeds isolated fixture JSON to each subcommand and
asserts exit/output behavior without using a real transcript.

- [ ] **Step 3: Add the exact project configuration**

```json
{
  "description": "Optional evidence-bound iteration lifecycle for this repository.",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "^(startup|resume|clear|compact)$",
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$(git rev-parse --show-toplevel)/.codex/hooks/iteration.sh\" session-start",
            "timeout": 15,
            "additionalContextLimit": 700
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$(git rev-parse --show-toplevel)/.codex/hooks/iteration.sh\" post-tool-use",
            "timeout": 15
          }
        ]
      }
    ],
    "SubagentStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$(git rev-parse --show-toplevel)/.codex/hooks/iteration.sh\" subagent-start",
            "timeout": 10
          }
        ]
      }
    ],
    "SubagentStop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$(git rev-parse --show-toplevel)/.codex/hooks/iteration.sh\" subagent-stop",
            "timeout": 10
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$(git rev-parse --show-toplevel)/.codex/hooks/iteration.sh\" stop",
            "timeout": 10
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$(git rev-parse --show-toplevel)/.codex/hooks/iteration.sh\" session-end",
            "timeout": 3
          }
        ]
      }
    ]
  }
}
```

Do not add status messages; they create repeated UI noise for mechanical state
maintenance.

- [ ] **Step 4: Test trusted, disabled, and absent-hook modes**

```bash
bash .codex/hooks/iteration_test.sh
for path in .codex/hooks.json .codex/hooks/iteration.sh .codex/hooks/iteration_test.sh; do
  if git check-ignore -q "$path"; then
    echo "$path is still ignored" >&2
    exit 1
  fi
done
```

Expected: hook tests pass and `git check-ignore` reports each file as unignored.
Run the repository workflow once with hooks enabled/trusted and once with
`[features] hooks = false`; both paths must reach the same Make-command
evidence, while only the enabled path gets dynamic context/invalidation.

- [ ] **Step 5: Document trust and non-security boundaries, then commit**

State that project hooks require exact-definition trust, matching hooks may run
concurrently, and hook disablement does not bypass the pre-push or manual
workflow. Link the
[official Codex hook documentation](https://learn.chatgpt.com/docs/hooks.md)
from the verification owner.

```bash
git add .gitignore .codex/hooks.json .codex/hooks/iteration.sh .codex/hooks/iteration_test.sh docs/contributing/verification.md
git commit -m "feat(agent): configure optional iteration hooks"
```

### Task 5: Require committed-tree evidence before feature-branch push

**Files:**

- Modify: `.githooks/pre-push`
- Create: `.githooks/pre-push_test.sh`
- Modify: `scripts/iteration/main.go`
- Modify: `scripts/iteration/main_test.go`
- Modify: `docs/contributing/verification.md`

**Interfaces:**

- Produces `iteration evidence --require-ready --head <sha> --base <ref>`.
- Adds recovery variable `EINO_ALLOW_STALE_EVIDENCE=1`.
- Preserves `EINO_ALLOW_MASTER_PUSH=1` only for the existing protected-master
  rule.

- [ ] **Step 1: Add committed-evidence CLI tests**

Assert `--require-ready` exits:

- 0 only when the stored plan base/head/digest exactly match the requested
  committed tree and state is `evidence_ready`;
- 1 for missing evidence, stale base, stale head, stale digest, required
  blocked/fail gate, malformed state, or a dirty current tree when `--head HEAD`
  is requested; and
- 2 when `--require-ready` is used without explicit `--head`.

- [ ] **Step 2: Add shell-hook fixtures**

`pre-push_test.sh` creates a temporary repository and a fake `go` executable
that journals only stable argv tokens. Feed exact pre-push stdin records and
prove:

1. direct master push still fails before evidence lookup;
2. feature branch with matching ready evidence passes;
3. feature branch with missing/stale/blocked evidence fails;
4. deletion ref with all-zero local SHA skips evidence lookup;
5. two pushed feature refs check both SHAs;
6. `EINO_ALLOW_MASTER_PUSH=1` bypasses only master protection, then still checks
   evidence; and
7. `EINO_ALLOW_STALE_EVIDENCE=1` prints a warning and bypasses only evidence,
   never master protection.

- [ ] **Step 3: Extend the existing hook after its protected-ref loop**

Collect non-deletion local SHAs during the current stdin loop. After every
protected-ref check has passed, execute for each SHA:

```bash
go run ./scripts/iteration \
  --base "${EINO_ITERATION_BASE:-origin/master}" \
  --head "$local_sha" \
  evidence --require-ready
```

Quote every value, reject malformed SHAs before invocation, and do not rerun
full gates inside pre-push. If the explicit stale-evidence bypass is set, print
one warning to stderr so final handoff can report the bypass.

- [ ] **Step 4: Run tests and commit**

```bash
go test ./scripts/iteration -run 'RequireReady|CommittedEvidence' -count=1
bash .githooks/pre-push_test.sh
git add .githooks/pre-push .githooks/pre-push_test.sh scripts/iteration/main.go scripts/iteration/main_test.go docs/contributing/verification.md
git commit -m "feat(git): require fresh iteration evidence before push"
```

### Task 6: Remove the compatibility wrapper after a real canary

**Files:**

- Delete: `.agents/skills/iteration-closeout/SKILL.md`
- Modify: `.agents/skill-runtime/validate_skills.py`
- Modify: `docs/contributing/documentation-policy.md`
- Modify: `docs/contributing/verification.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- Removes only the retired skill name after all tracked callers and one real
  merged PR use `iteration-workflow` successfully.
- Does not remove S1/S2 commands, manual operation, or hooks-disabled support.

- [ ] **Step 1: Execute the deterministic canary gate**

Use the hook/privacy or pre-push change itself as the canary PR. Require:

- `make change-plan` selected governance owners;
- `make verify-focused` passed;
- the committed candidate passed `make verify-merge`;
- `make change-evidence` reported current `evidence_ready`;
- pre-push accepted the exact SHA;
- remote CI status was reported separately; and
- any hook-disabled run reached identical repository evidence.

Do not remove the wrapper before that PR is merged and read back from
`origin/master`.

- [ ] **Step 2: Prove there are no tracked callers**

```bash
rg -n '\$iteration-closeout|iteration-closeout' AGENTS.md .agents docs Makefile .codex .githooks
```

Expected: only the wrapper, this historical implementation plan, and any
explicit migration history reference remain. Active instructions and current
owner docs have zero callers.

- [ ] **Step 3: Delete the wrapper and update validators/docs**

Remove the skill directory, change validators to reject new active references,
and document `iteration-workflow` as the only shared owner. Historical plan
mentions remain historical and do not trigger skill routing.

- [ ] **Step 4: Run final validation**

```bash
python3 .agents/skill-runtime/validate_skills.py
python3 -m unittest .agents/skill-runtime/test_skill_log.py
go test ./scripts/iteration ./scripts/docs_check -count=1
bash .codex/hooks/iteration_test.sh
bash .githooks/pre-push_test.sh
make docs-check
make fmt
make lint
make test
make build
git diff --check
```

Expected: all applicable commands pass. Run the synthetic privacy marker suite
with hooks enabled and verify every marker remains absent from stdout and
persisted state.

- [ ] **Step 5: Commit retirement and prepare the S3 PR**

```bash
git add -A .agents/skills/iteration-closeout .agents/skill-runtime/validate_skills.py docs/contributing/documentation-policy.md docs/contributing/verification.md docs/superpowers/plans/README.md
git commit -m "chore(skills): retire iteration closeout alias"
```

The final S3 handoff must report the canary PR, exact hook trust state, enabled
and disabled-path results, privacy-marker results, SessionStart byte maximum,
pre-push evidence behavior, explicit bypasses, local Make gates, remote CI, and
the fact that hooks still do not replace skill-runtime judgments.
