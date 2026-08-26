# Permissions and Safety

**Status:** current
**Last verified:** 2026-08-24

> **Ownership:** operator-facing permission modes, rules, approval scope, and unattended safety boundaries

## Choose the narrowest mode that works

Set the initial mode with `--permission-mode`, `permission_mode`, or `-y`;
change it during a conversation with `/permissions mode MODE` or `/plan`.

| External mode | Unmatched tool behavior |
|---|---|
| `default` | Apply deterministic safe paths, including proof-bound ordinary Bash; ask for the remainder |
| `plan` | Deny state-changing tools; allow read-only exploration and the plan-file path |
| `acceptEdits` | Auto-allow bounded Write/Edit paths; Bash still requires the ordinary interactive or fail-closed decision |
| `bypassPermissions` | Auto-allow unmatched uses; explicit deny rules and narrow critical Bash live confirmation still win |
| `dontAsk` | Deny unmatched uses and convert asks to denies |
| `auto` | Apply exact user authority and typed bounded fast paths; incomplete shell, Agent/child, network, MCP/app/dynamic, and direct-interaction actions require a person; remaining complete built-ins may reach the primary model classifier |

`-y`/`--yolo` selects `bypassPermissions`. It is suitable only for an already
isolated, trusted workspace.

`auto` is selectable with:

```text
/permissions mode auto
```

It is not equivalent to bypass: explicit rules, Plan containment, exact grants,
typed fast paths, classification, and prompt fallback still run. P22.H0
removed the old command-prefix Bash shortcut. P51.2 Core now admits ordinary
canonical built-in Bash without a prompt in Default or Auto only when the exact
action carries a complete available Darwin Guest proof. Missing or incomplete
proof follows Default's prompt/fail-closed path or Auto's existing
prompt/classifier/fail-closed path. An exact local/user rule remains separate
explicit user authority; it is not proof-bound admission.

A narrow literal critical `rm`/`rmdir` subset is handled earlier. It always
requires one fresh live `AllowOnce`, including in `bypassPermissions`;
`dontAsk` denies it. Exact rules, session/always grants, classifier, reviewer,
and a different request's decision cannot authorize it. Unsupported shell
syntax is not newly blocked merely because the recognizer cannot classify it.

The authoritative Auto classifier still reuses the primary conversation model
and includes recent assistant/tool message contents in its context. Do not
treat it as an independent security reviewer. P22.2a adds a separate
non-authoritative shadow for evaluation, and P22.2b can retain bounded local
redacted measurements from that shadow. Neither changes permission outcomes.
The target design and remaining rollout gates are documented in
[`P22 Auto Permission Review`](../migration/plans/p22-auto-permission-review.md).

## Choose the Guest sandbox independently

Sandbox selection and permission mode answer different questions: permission
decides whether Bash may start; the Guest binding limits what the started Bash
process and its descendants can affect. Changing to Auto, bypass, or `--yolo`
does not disable or broaden the Guest binding.

On Darwin amd64/arm64, model-issued Bash defaults to the real Seatbelt
`workspace-write` profile. On Linux amd64/arm64 it uses the fixed
`/usr/bin/bwrap` adapter when the binary and its real capability probe are
available. Both profiles expose only declared filesystem roots, deny network
and unapproved Unix sockets, bind the canonical workspace identity, and keep
shell hooks plus configured stdio MCP ambient and separately reported.

The Linux profile protects only control-plane paths that already exist when
the immutable policy is built. A workspace-local denied path that crosses a
symlink fails closed, but an absent protected path is not reserved against
later creation. Therefore Linux Guest Bash remains prompt-approved only: its
proof does not activate Default/Auto's automatic Bash admission. Darwin is the
only platform currently admitted by that shortcut.

Select the profile explicitly for one invocation:

```bash
yhc --sandbox workspace-write
yhc --sandbox danger-full-access
```

Or set user-owned configuration:

```json
{
  "sandbox": {
    "guest_profile": "workspace-write",
    "extra_read_roots": ["/absolute/existing/non-symlink/toolchain"]
  }
}
```

The CLI overrides user configuration. Project and project-local `sandbox`
fields are discarded with a bounded diagnostic, so checked-in content cannot
select ambient execution or add roots. Extra roots must be absolute, existing,
canonical directories and cannot be a broad system or home ancestor.

`danger-full-access` is an explicit ambient rollback and prints a warning. On
an unsupported platform, missing fixed platform executable
(`/usr/bin/sandbox-exec` or `/usr/bin/bwrap`), failed real probe, or changed
workspace identity, the default Guest binding is unavailable: the app
continues running, but Bash fails before spawn and never retries ambient.

The contained Guest still inherits `os.Environ()` byte-for-byte and has no hard
memory, file-descriptor, or process-count limit. Its aggregate state is
therefore `degraded`, not fully contained. P51.2 checks the exact granular
proof instead of treating this aggregate label as authority.

Runtime bypass has a separate confirmation boundary:

```text
/permissions bypass confirm
```

`/mode`, top-level `/bypass`, and `/yolo` are not runtime aliases. ACP protocol
mode selection cannot request bypass because it carries no equivalent explicit
confirmation. The TUI confirmation dialog and the exact slash command use the
same engine-owned transition.

In the TUI, Shift+Tab cycles Default → Plan → bypass confirmation → Bypass →
Default. Canceling the confirmation leaves Plan active. A running turn or a
pending Plan approval blocks the transition even after confirmation.

## Prefer structured rules

Rules from local, project, and user settings are aggregated. The loader reads
local rules first, and local settings are the default destination for
`/permissions add`, but a local allow does not override a matching deny from
another file because action priority is `deny` > `ask` > `allow`. In Auto,
every source may deny or ask, but only an exact non-wildcard local/user allow
can supply unattended authority; a checked-in project allow or a broad rule
still falls through to the remaining policy.

```json
{
  "permissions": {
    "allow": [
      "Read",
      "Grep",
      "Glob",
      "Bash(go test ./...)"
    ],
    "ask": [
      "Edit"
    ],
    "deny": [
      "Bash(rm -rf *)",
      "Read(/etc/*)"
    ]
  }
}
```

Rule values use `Tool` or `Tool(pattern)`. `*` is a wildcard. For Bash the
pattern matches `command`; for file tools it matches the path. A rule such as
`mcp__github` covers every dynamically registered `mcp__github__TOOL` tool.
Across matching rules, priority is `deny` > `ask` > `allow`.

The legacy `permission_rules` array with `Tool(pattern):action` strings is also
accepted, but structured rules are easier to audit.

Manage rules without hand-editing JSON:

```text
/permissions
/permissions rules list
/permissions rules add allow "Bash(go test ./...)" --local
/permissions rules add deny "Bash(rm -rf *)" --project
/permissions rules remove allow "Bash(go test ./...)" --local
```

## Understand automatic allow paths

Before prompting, the engine can allow:

- explicit allow rules outside Auto, or an exact local/user allow for the
  canonical current action in Auto;
- ordinary canonical built-in Bash in Default or Auto when its exact Darwin
  Guest proof is complete;
- `TodoWrite`, which changes only the current process-local Session/Agent task
  list and does not require ordinary interactive approval;
- an exact session-scoped command/path/input approval, or a contained
  Read/Grep/Glob root approval;
- `Read`, `Grep`, and `Glob` inside the current and added working directories;
- approved persistent-memory paths when memory is enabled;
- the bounded `acceptEdits` and plan-file cases described above.

Explicit `deny` and `ask` rules are evaluated before these path-based defaults.
They also override TodoWrite's default allow behavior.

Proof-bound ordinary Bash is a deterministic Default/Auto path, not an
`acceptEdits` path or a command-name allowlist. Explicit deny rules remain
useful defense in depth, but they are not a shell parser or an external
execution sandbox. Agent/child, network, MCP/app/dynamic, and
direct-interaction capabilities follow the same human-required rule in Auto
unless the user supplied exact authority for that action. Default never invokes
the Auto classifier.

## Interactive approval scope

The TUI offers one-call and session-scoped approval in its normal dialog. Plain
mode prompts with `once`, `session`, `always`, and `deny`; `always` writes an
allow rule to `.claude/settings.local.json`. Session approvals are scoped to the
current root session and are not a substitute for a reviewed durable rule.
Both session and always decisions are derived from the final post-rewrite
action. A critical Bash request is different: every Core adapter exposes only
`AllowOnce` and denial, and the engine rejects a forged persistent response.
An ordinary request rewritten by an adapter into a critical command is denied
without execution or grant persistence; submit the changed command again to
receive its own live `AllowOnce` request.
Always persistence encodes an exact command, exact resolved path, or
canonical JSON input with rule metacharacters escaped; if that identity cannot
round-trip, persistence fails instead of widening to a wildcard. Concurrent
always decisions in one running process serialize the settings read-merge-write
cycle, so distinct exact rules are retained. Separate processes still require
operator-level coordination when editing the same settings file.

The engine can load `<project>/.eino-agent/approvals.json` for compatibility,
but the normal interactive “always” path persists a settings rule, not that
file.

## Headless automation

Headless installs no interactive prompt callback. If a tool reaches that
callback boundary it is denied. Deterministic paths still run, including
workspace reads and explicit allow rules.

For a narrow unattended job, prefer an allowlist:

```bash
go run ./cmd/yhc --tools Read,Grep,Glob,Bash -p "run go test ./... and summarize failures"
```

In Default or Auto, an ordinary Bash command can run unattended from a complete
Darwin Guest proof. Auto can also use an exact local/user
`Bash(go test ./...)` rule; the rule is explicit user authority, not proof, and
never authorizes a critical request. A checked-in project allow cannot widen
unattended Auto authority. Use `-y` only when broad tool access is intentional;
critical Bash still asks for `AllowOnce` and explicit deny rules remain useful
defense in depth.

## Evaluate the separate reviewer shadow

The P22.2a reviewer is off by default. To observe a separately routed model
without changing permission outcomes:

```bash
go run ./cmd/yhc \
  --permission-mode auto \
  --permission-review-shadow \
  --permission-review-provider openai \
  --permission-review-model gpt-4o-mini \
  --permission-review-timeout 8s \
  --permission-review-audit
```

Supply the selected provider's normal provider-specific credential environment
or pass `--permission-review-api-key`; add
`--permission-review-base-url` only for an intentional compatible endpoint.
Provider and model are mandatory when the shadow is enabled. Generic actor
`PROV_*` values do not select or credential the reviewer route.

Startup prints one safe diagnostic naming the provider, model, data-boundary
version, and timeout. When audit is enabled it prints a second diagnostic that
does not reveal the storage path. TUI, plain, headless, and ACP then expose
bounded `checking`, `completed`, or `unavailable` status for eligible actions.

This is measurement scaffolding, not approval:

- deterministic permission policy, the legacy actor-model classifier, prompt
  fallback, and dispatch remain authoritative;
- the reviewer cannot approve, deny, suppress a prompt, create a grant/rule, or
  change denial accounting;
- only eligible complete main-agent built-ins are observed; Bash, Agent/child,
  network, MCP/app/dynamic, ProjectGraph-probe, and standalone MCP paths are
  excluded; and
- reviewer requests, nonces, action digests, raw input, absolute paths,
  rationale, credentials, transcript/session/Agent identity, and policy bytes
  are never written to the measurement journal.

### Inspect or delete retained measurements

Audit collection requires both `--permission-review-shadow` and
`--permission-review-audit`; setting only
`--permission-review-audit-dir DIR` is rejected. The default directory is
`~/.eino-agent/permission-review-audit/v1`. Override it for runtime collection
with `--permission-review-audit-dir DIR` or
`YHC_PERMISSION_REVIEW_AUDIT_DIR`.

The directory is mode `0700`; owned JSONL segments and lock coordination files
are mode `0600`. Each operation pins the validated directory and regular-file
handles, so a concurrent path or symlink replacement cannot redirect journal
I/O outside that directory. An OS file lock serializes the bounded O_EXCL
sentinel and its 30-second stale recovery. The store retains one active 1 MiB
segment plus at most seven rotated segments. This is a size window, not an
age-retention promise. A partial active tail is truncated before the next
append and represented by a typed recovery record. Malformed complete records,
including any `null`, are skipped and counted.

Generate a deterministic local aggregate without starting a provider:

```bash
go run ./cmd/yhc permission-review-audit report
go run ./cmd/yhc permission-review-audit report --output-format json
```

Use `--dir DIR` on the administration command to inspect an explicit store.
Reports never print that path. They distinguish `no_data`, `retained_window`,
and `partial`; an absent human or corpus denominator is `unavailable`, not a
zero-disagreement claim. Metrics include eligible actions, reviewer attempts,
completed/unavailable/escalated outcomes, nearest-rank p50/p95 latency, and
separate legacy-classifier, direct-human, and versioned-corpus denominators.
Every retained false allow lists only its fresh opaque event ID, comparison
source, canonical tool, and action class.

The human denominator accepts only a direct structured adapter decision for
the exact unchanged settled action. Coalesced grants, context timeout/cancel,
invalid adapter output, fail-closed rewrites, and changed input are excluded.
Production runtime does not invent corpus truth; corpus status remains
`unavailable` unless an explicit typed `versioned_corpus` record is supplied
by an offline fixture or evaluator.

Deletion is explicit and removes only the eight owned segment names. Unknown
neighboring files and the directory remain:

```bash
go run ./cmd/yhc permission-review-audit delete --confirm
```

Journal write errors and panics are contained and never alter classifier,
prompt, grant, denial-accounting, or dispatch outcomes. The retained window is
evaluation evidence only; it does not enable reviewer enforcement.

## Standalone MCP is a separate policy boundary

`serve mcp` defaults to `MCP_PERMISSION_MODE=open`. The exact lowercase value
`strict` blocks non-read-only tools; any other non-empty value rejects server
startup. Set it explicitly for read-only exposure:

```bash
MCP_PERMISSION_MODE=strict go run ./cmd/yhc serve mcp
```

Standalone MCP exposes only TaskCreate/Get/List/Update/Stop/Output, the
combined Task adapter, and TodoWrite. Each `Serve` owns fresh ephemeral
Task/Todo state; it has no Session, durable WorkBoard, Agent, Goal, Team, or
plan authority. TodoWrite and mutating Task actions remain non-read-only, so
strict mode blocks them even though QueryEngine does not prompt for TodoWrite
by default.

## Maintainer reference

| Concern | Source |
|---|---|
| Modes and rule matching | [`mode.go`](../../engine/permission/mode.go), [`rules.go`](../../engine/permission/rules.go) |
| Rule loading/persistence | [`loader.go`](../../engine/permission/loader.go), [`persist.go`](../../engine/permission/persist.go) |
| Runtime ordering and exact action binding | [`engine.go`](../../engine/engine.go), [`permission_action.go`](../../engine/permission_action.go), [`permission_scope.go`](../../engine/permission_scope.go) |
| Guest sandbox selection and process bindings | [`sandbox.go`](../../engine/config/sandbox.go), [`execution_policy.go`](../../engine/execution_policy.go), [`binding.go`](../../engine/containment/binding.go) |
| Darwin Seatbelt adapter | [`seatbelt.go`](../../engine/containment/seatbelt.go), [`bash_shell.go`](../../tools/bash_shell.go) |
| Permission reviewer shadow and correlation | [`permission_review.go`](../../engine/permission_review.go), [`permission_interaction.go`](../../engine/permission_interaction.go), [`reviewer.go`](../../engine/permission/reviewer.go), [`provider/reviewer.go`](../../engine/provider/reviewer.go) |
| Redacted journal and aggregate report | [`review_audit.go`](../../engine/permission/review_audit.go), [`review_audit_report.go`](../../engine/permission/review_audit_report.go), [`permission_review_audit.go`](../../cmd/yhc/cmd/permission_review_audit.go) |
| Registry capabilities and dispatch lease | [`registry.go`](../../tools/registry.go) |
| Runtime mode controls | [`execution_controls.go`](../../engine/execution_controls.go), [`cmd_permissions.go`](../../engine/commands/cmd_permissions.go) |
| Headless/MCP policies | [`headless.go`](../../cmd/yhc/cmd/headless.go), [`server.go`](../../server/mcp/server.go) |
| Architecture | [Capabilities](../architecture/capabilities/README.md) |
