# Interaction Modes and Commands

**Status:** current
**Last verified:** 2026-10-25

> **Ownership:** supported entrypoint selection and user-visible command projection differences

## Pick an entrypoint

The examples use `go run`; replace it with your platform binary after building.

| Mode | Command | Use it for | Current limitation |
|---|---|---|---|
| TUI | `go run ./cmd/yhc` | Full interactive terminal experience | Requires interactive stdin/stdout |
| Plain REPL | `go run ./cmd/yhc --plain` | Line-oriented terminals and logs | TUI panels/composer actions degrade to text or are unavailable |
| Headless | `go run ./cmd/yhc exec "prompt"` | One prompt in scripts | No interactive permission prompt; use `-y` only when bypass is intended |
| Bounded Goal run | `go run ./cmd/yhc goal run --resume SESSION_ID --max-continuations 8` | Continue an existing saved Goal in automation | Goal must already exist and be enabled; no prompt/stdin objective or interactive answer |
| Resume in TUI | `go run ./cmd/yhc resume SESSION_ID` | Continue a saved session | Resolves catalogued roots for the current project; duplicate IDs fail |
| Resume in plain | `go run ./cmd/yhc --plain --resume SESSION_ID` | Text-only continuation | Same current-project constraint |
| Session administration | `go run ./cmd/yhc sessions list` | List/resume/rename/export/fork durable sessions without a TUI or provider | Current-project scope; archive/delete are absent |
| Diagnostics administration | `go run ./cmd/yhc config show` / `doctor` | Inspect redacted effective configuration and stable checks without a model runtime | Connectivity is deliberately not probed |
| Extension administration | `go run ./cmd/yhc mcp list` / `plugins validate` | Inspect configured MCP and plugin generations without entering a conversation | MCP health is unprobed; plugin reload is inspection-process local |
| ACP server | `go run ./cmd/yhc serve acp` | IDE clients over stdio | Client owns the interactive surface |
| MCP server | `go run ./cmd/yhc serve mcp` | Expose built-in tools over stdio | Tools only; bypasses `QueryEngine` |

Pipe a generated prompt to headless mode:

```bash
printf '%s\n' 'inspect the failing tests' | go run ./cmd/yhc exec -
```

`exec` reads stdin when no prompt is supplied or when the prompt is `-`. If a
positional prompt and piped stdin are both present, stdin is appended as an
explicit `<stdin>` block. Root `-p`/`--print` remains compatible with the same
runtime, but a root positional prompt without `-p` is now a usage error instead
of being ignored.

Use `--output-format text` for human output, `--output-format json` for one
schema-versioned result object, or `--output-format jsonl` for ordered
lifecycle events followed by one final result. Stdout has one renderer owner;
progress and redacted diagnostics stay on stderr. Exit codes are `0` complete,
`1` runtime failure, `2` usage/validation, and `130` cancelled.

JSONL event records expose the bounded Session/thread/turn identity and
sequence plus committed assistant deltas, redacted canonical tool lifecycle,
command output, compaction, max-turn, or interruption facts. They do not expose
reasoning, provider responses, raw noncanonical tool events, or an interactive
approval channel. Consumers should branch on `schema_version`, `type`, and
`event.kind`, ignore unknown additive fields, and treat the single `result`
record as process closure.

### Attach selected file lines in the TUI

Type a file mention with an inclusive line range, for example
`Explain @engine/prompt_suggestion.go#1-20`, then press Tab to attach the
selected candidate. `#20` selects one line; `@file:path#20-80` also works.
The source must be a regular UTF-8 text file no larger than 5 MiB. Invalid or
out-of-bounds ranges report an error and never attach the whole file instead.

Keep the cursor on or immediately after the reference to see its selected
lines, byte size and a short preview. A clipped preview still submits the
complete selected range. In the standard editor, Backspace at the reference
end removes the attachment; Ctrl+Z restores it. The attachment retains the
content captured when it loaded, even if the file later changes. Remove and
reattach to capture a newer version. History recall restores only the visible
reference. See the [composer contract](../architecture/tui/contracts/composer.md#file-line-ranges).

### Attach selected terminal output in the TUI

With an ordinary prompt draft, drag over the relevant output in the chat and
press **Alt+A** to
attach that selection to the draft. Mouse tracking is enabled by default;
Shift+drag uses the terminal's own selection and does not create a YHC
selection. For longer output, press **Ctrl+O**, select text in the current
expanded transcript and press Alt+A to return with the snippet attached.
Help and `/keybindings` show the configured `selection:attachContext` binding.

The `[Output #N]` reference retains the selected text when the source later
changes. Move the cursor onto or immediately after it to preview the captured
bytes. Preview clipping affects display only. Backspace at the reference end
removes the attachment; Ctrl+Z restores it in the standard editor. Press Enter
separately when ready to send the draft.

A selection can contain up to 5 MiB; oversized or changed selections give a
reason and preserve the draft. Select again after resizing or changing the
expanded projection. History recall restores a visible reference and marks
its output body as not restored. See the
[composer contract](../architecture/tui/contracts/composer.md#selected-terminal-output).

### Manage attached context in the TUI

Press **Alt+C** in an ordinary prompt draft to view all its attached context.
The list includes file ranges, whole files, terminal-output snippets, Skills,
MCP resources, large pasted text and images. Up/Down selects an item and shows
its captured preview and size. Long previews are clipped while the full
captured content remains attached; images show metadata. Loading items update
when their existing read finishes.

Press **/** to search names, paths and sources (case-insensitive). **Enter** or
**Esc** leaves the search field and keeps the query. **Tab** cycles attachment
types; **Shift+Tab** cycles backwards. Search and type filters combine, and
search does not inspect attachment bodies.

Outside the search field, press **s** to cycle draft order, highest token use
first, and lowest token use first. Ties retain draft order; loading or
unavailable estimates stay at the end. Sorting preserves the highlighted
attachment and marks, and changes only the list. Reopening resets to draft
order.

Press **Space** to mark individual attachments or **Ctrl+A** to toggle all
current matches, including offscreen rows. Changing the search or type clears
marks. **Delete** or **Backspace** removes the marked set; with no marks, it
removes the highlighted item. If any target is still loading, the whole batch
waits. Outside the search field, **Ctrl+Z** restores the entire batch with its
captured content in one step.

**Enter** outside search closes the list and positions the cursor at the
highlighted reference; **Esc** closes the list, and **Ctrl+C** closes even while
searching. Closing does not submit the draft. Search, type and marks reset on
reopening. Send the draft separately when ready. The entry and undo bindings
are configurable; Help and `/keybindings` show `chat:manageContext`.

Each row shows an approximate token count (`~123t`); selecting a text attachment
shows its share of the known model context window. The summary counts **all** attached
content, including items hidden by filters. Loading items show `pending`, and
the total is marked partial until their content is available. Removal and undo
refresh the estimate.

When attachment estimates reach **80%** of the known model window, the panel
shows a warning; at **100%** it shows an alert. Partial totals are identified,
and hidden attachments still count. These thresholds use unrounded estimates.
Remove large items to reduce the total; undo restores their estimate and any
warning. Compact views prioritize the warning and list controls.

Counts use the engine's rough text heuristic over complete captured content,
including file/output context metadata. Images carry a generic estimate marked
`*`; their actual provider cost may differ substantially. `Window` is model
capacity, and `Last input` is the latest provider-reported input use. These are
separate values, not remaining space or a forecast of the next request. Shared
message overhead, ordinary draft text, history, instructions and tools are not
in the attachment total. A warning concerns attachment estimates alone, and
does not block sending or trigger compaction. No warning does not guarantee
that the full request fits. Unknown capacity/usage shows unavailable; child
views do not inherit the leader's numbers. Compact terminal views may omit summary
rows. Inspecting the panel performs no new file reads or provider calls.

The list manages the current draft. Switching Agent threads closes it before
showing the next draft, and recalled text references have no attachment body
to list. See the
[composer contract](../architecture/tui/contracts/composer.md#attached-context-manager).

### Request a next-step suggestion

In the TUI, leave the leader input empty and press `Ctrl+Space`. YHC shows
progress, then a suggested next prompt or a short explanation such as an early
conversation, disabled suggestions, or a timed-out request. Suggestions need
at least two assistant messages. `Tab` or Right Arrow copies a candidate into
the draft; press Enter separately to send it. Enter with an unaccepted ghost
only dismisses it. Typing, Escape, or Ctrl+C cancels the pending request or
dismisses its feedback.

The same shortcut still requests argument completion inside a supported slash
command. It follows the configurable `chat:argumentSuggest` binding shown in
Help and `/keybindings`. Desktop/Web uses the existing **Suggest next step**
button with the same result categories. These helper requests may incur
provider usage; they use the current model and do not run tools. See the
[composer contract](../architecture/tui/contracts/composer.md#next-prompt-ghost)
and [runtime service boundary](../architecture/platform/runtime-services.md#interactive-prompt-suggestions).

### Completion and evaluation

`exec` uses the same solver traversal as the TUI and ACP. The solver can inspect,
edit, and test through its normal tools; completion does not start an automatic
second checker or change reasoning effort. Benchmark reward comes from the
external task verifier.

The former `--verification-*` and `--resume-verification` flags and Harbor
`verification_*` options are removed and rejected. Update old evaluation
commands before running them. Existing Session files remain available; use
`--resume <session-id>` with an explicit request to continue the original task.
Optional budgets and ordinary progress-preserving resume are unchanged. See
the [completion boundary](../architecture/runtime/query-engine.md#completion-and-evaluation-boundary).

### Invocation usage and optional limits

After engine execution, ordinary `exec` reports current-invocation usage: JSON and the final
JSONL `result` contain `usage`; text mode prints a numeric summary on stderr.
Resuming a session starts a fresh counter. TUI, plain mode, ACP, and `goal run`
do not automatically enable this invocation collector.

Both budget controls are **off by default** (`0`). Enable either or both:

```bash
yhc exec "inspect the failing tests" --output-format json \
  --max-provider-calls 30 --max-total-tokens 200000
```

`--max-provider-calls` limits admitted YHC provider calls across the main loop,
children, compaction, retries/fallbacks, WebFetch AI and engine-owned auxiliary
calls. Reservations proven not dispatched are refunded. This counts YHC client
entries, not HTTP attempts hidden inside a third-party SDK, external hook/MCP
traffic, or a currency amount.

`--max-total-tokens` checks accumulated provider-reported tokens before admitting
the next call. The last response and concurrent in-flight calls can exceed the
threshold; it is not a strict token reservation or spending cap. Missing,
invalid, or ambiguous usage prevents further calls only when this token control
is enabled. With both controls off, usage collection preserves existing retry
and fallback behavior. Opaque custom permission reviewers are skipped while
limits apply; statistics-only runs mark their coverage incomplete.

Main/child model requests include explicitly enabled invocation limits in a
fixed policy block before conversation history. It describes total limits,
including usage already consumed by children and auxiliary calls; it does not
claim they are remaining allowances. Live counters stay in usage reporting so
they cannot rewrite the cached request prefix each round. The policy is not
saved as conversation history. Admission checks remain authoritative.

`yhc exec --timeout 29m` optionally cancels the query after that duration,
including its active child/tool/provider contexts. The timer starts after provider configuration,
before engine initialization, resume and query submission; zero (the default)
disables it.
An earlier caller deadline still wins. The explicit invocation deadline survives
child foreground/background detachment and child continuation with fresh
contexts; ordinary parent cancellation leases
retain their existing behavior. With an invocation collector, enforced
context deadlines are included as an absolute UTC cutoff in the model's policy,
instead of a changing countdown. The policy asks
for targeted verification and honest reporting of unfinished work; it cannot
guarantee the model will finish early. Cancellation uses the existing exit
`130` path and settles available usage; shutdown/logging can take additional
time, so leave a margin before an external container kill deadline.

For native currency estimates, pass `--price-card public-prices.json`.
Add `--max-estimated-cost 1.50` to stop new calls once the reported estimate
reaches that amount in the card's currency; this option defaults to disabled.
In-flight responses can exceed this stopping threshold. The public price-card
schema, exact route-selector matching, incomplete-cost behavior and explicit
Harbor continuation grants are owned by
[budgets and limits](../architecture/runtime/budgets-and-limits.md#native-currency-estimates-and-stopping-threshold).
A price card records an estimate; it does not read the provider's account bill.

For a conservative pre-dispatch ceiling on that estimate, add
`--reserve-cost-before-dispatch --max-output-tokens-per-call 16384` alongside a
positive `--max-estimated-cost`. This optional mode is off by default. The card
must declare `window: "upper_bound"` and an explicit `api_model` for every route.
Currently only the built-in local DeepSeek adapter supports this mode. Each
request reserves the full supported context capacity at the higher input rate
plus the output bound before dispatch; known usage refunds the difference.
Consequently a call can be denied while some budget remains. Unknown response
cost retains the reservation and stops retries. This controls the declared
estimate, not the provider's invoice or external tool charges.

A rejected admission returns exit `1` with `error.code=run_budget_exceeded`.
`usage.budget_stop_cause` distinguishes `provider_calls`, `total_tokens`,
`estimated_cost_threshold`, and `estimated_cost_reservation`. A reservation
denial can occur while money remains because the next full bound cannot fit.
Incomplete usage under a token or monetary threshold, or missing required pricing,
returns `run_usage_unknown`. Cancellation
retains exit `130`. Reaching a threshold in a successful final answer does not
itself turn success into failure; the next admission is the enforcement point.

`usage` contains input/output/total tokens, cached input, reasoning output,
known/unknown/in-flight calls, denied calls, and breakdowns by model, source,
role, and effort. `total_tokens` is at least input plus output. Cached input and
reasoning output are subsets, never added again. Missing usage is **unknown**,
not zero: totals are partial when `complete=false`. Cache and reasoning details
are those supplied by the adapter; an omitted detail cannot prove that no
caching or thinking occurred. `provider_duration_ms` sums call lifetimes and
may exceed invocation `elapsed_ms` when calls overlap. No price is inferred.
A forced process kill may prevent the final summary from being written.
Opt-in [invocation usage checkpoints](invocation-usage-checkpoints.md) retain
admission and settlement records for inspection from a new process; they do
not grant automatic continuation or reuse the previous budget.

`usage.call_ledger` adds up to 1,024 individual admission records per invocation:
model/provider labels, source/role/effort, attempt/retry indices, timing,
settlement state, and known cached/uncached input plus output/thinking tokens.
`dropped_records` reports omitted details; aggregate totals still include those
calls. Unsettled/unknown/released records have null tokens. `resolved_model` is
observed from supported typed response metadata and otherwise says `unknown`.
`cache_write_prompt_tokens` is a subset of input, paired with
`cache_write_reported_calls` in invocation and route totals. A per-call write
counter is omitted when unreported and can explicitly be zero. `cache_ttl` is
included only when supported typed response metadata actually reports it;
request settings do not supply it. This adds no separate write-token price.
The [invocation usage contract](../architecture/runtime/budgets-and-limits.md#invocation-usage-and-per-call-history)
explains identity, privacy, truncation, and supported entrypoint boundaries.
Harbor continuation metadata retains one ledger per incremental invocation in
`usage.call_ledgers`; `call_ledger_complete` describes history coverage separately
from `usage.complete`. Older logs remain valid without synthesized history.


The [Harbor adapter](../../scripts/terminal_bench/yhc_agent.py) accepts optional
agent arguments `--ak max_provider_calls=30 --ak max_total_tokens=200000`.
An optional `--ak execution_timeout_sec=1740` passes `--timeout 1740s` to YHC;
choose a duration below Harbor's outer timeout to leave shutdown time. All three
controls default to disabled. Complete usage fills Harbor input,
output and cache counters; partial usage stays in metadata so it cannot be
mistaken for a complete total. Cost remains unset.
On Harbor timeout/cancellation, the adapter checks the invocation PID and Linux
executable identity, sends an interrupt, and allows a bounded shutdown before
Harbor collects logs. The original cancellation still propagates. Settled usage
can survive in the final cancelled result; an interrupted call without usage
remains unknown. Hard kills or failed shutdown can still leave an incomplete
stream, so this is not a durable crash-recovery ledger.

For local Docker running an amd64 task through Rosetta on an arm64 host,
`/proc/PID/exe` can identify the emulator rather than the uploaded YHC binary.
The identity guard then refuses to signal that PID. Keep this guard and set an
explicit internal deadline at least 60 seconds before the outer deadline; for
an outer 1200-second limit, use `--ak execution_timeout_sec=1140`. An offline
streaming check confirms that the internal deadline preserves completed usage
and marks the unfinished response unknown. It does not establish an actual
bill or make incomplete usage safe for automatic budget continuation.

Budget continuation is opt-in. Supply a JSON list of new finite allowances to
authorize attempts after a budget stop, for example:

```bash
--ak max_provider_calls=30 \
--ak 'continuation_budgets=[{"max_provider_calls":10,"max_total_tokens":200000}]'
```

The initial allowance and every additional allowance apply to one invocation;
the list authorizes additional spending, rather than resetting an overall
financial cap. At least one limit in each grant must be positive. No grants are
configured by default. The adapter only continues after an exit-1
`run_budget_exceeded` result with complete usage and a saved session ID.

For the
pinned Harbor 0.22 Docker backend it also compares every declared Compose
service's container ID, image, start time, PID and restart count before the
first invocation and before each continuation. Missing, stopped, paused,
unhealthy, replaced or restarted services block the next invocation. An
unsupported backend or an unavailable inventory also blocks continuation;
ordinary independent runs do not require this inventory. No configured Docker
healthcheck is reported as `not_configured`, without claiming application
health was verified.

It runs
`exec --resume` in the same environment with existing files and conversation
history. Unknown usage, cancellation, other errors and exhausted grants stop
the run. The overall configured execution timeout and Harbor timeout do not
restart. Session recovery preserves completed tool history; the model can still
choose to repeat an action, so continuation is an opportunity to finish, not a
guarantee of zero repeated work or eventual reward.

Prior streams and stderr are archived as `yhc.segment-0000.*`, etc.;
`yhc.jsonl` is the latest segment. Harbor metadata contains per-segment usage
and the aggregate across segments. Missing segment usage makes the total
unknown; it never becomes zero. The continuation list must be supplied before
the trial starts. Once Harbor has deleted the environment, this adapter cannot
restore the entire workspace from a session ID alone. A resumed trial also has
different allowances and must be reported separately from a fresh baseline.

Harbor metadata under `yhc.recovery` separates a live continuation check from
cold recovery after teardown. The check is a point-in-time observation of the
declared containers, not a memory snapshot or a future authorization. It cannot
detect an application restarting inside an unchanged container, discover
external state owners, or freeze time while the agent is stopped. An unknown
usage receipt still blocks spending even if the entire environment remains
alive. No diagnostic or saved file authorizes another request.

| State | What currently survives | Cold recovery boundary |
|---|---|---|
| YHC conversation and tool history | Native session files; `exec --resume` loads them in the existing workspace | An archived file or session ID alone does not prove a fresh environment can load it |
| Workspace and file-backed data | Captured files can be retained; an explicit copy can restore known files | There is no automatic complete-environment restore entrypoint |
| Sidecar process memory and background processes | Continue only while those processes remain alive | Container stop/restart loses RAM; retained container files do not restore it |
| Logical clocks and wall time | Live services keep advancing their own clocks | No generic clock checkpoint, pause or rebase is implemented |
| External side effects | Require their own durable state and reconciliation | Conversation replay cannot roll back or deduplicate arbitrary external actions |

[`recovery`](../../scripts/terminal_bench/recovery.py) can assess an existing
capture on a POSIX host without starting it or modifying its files. Supply a new host-owned
output path outside the task's writable mounts:

```bash
mkdir -m 700 /path/to/private-recovery-reports
python -m scripts.terminal_bench.recovery /path/to/checkpoint-manifest.json \
  --inspect-docker --output /path/to/private-recovery-reports/assessment.json
```

`--inspect-docker` only inspects the manifest's owned container handles; it
does not establish complete service discovery. Omit it for a file-only check.
The output parent must be private, owned by the caller, and use a real path
without symbolic-link traversal. The caller must still keep it outside task
mounts; file permissions alone do not protect a file inside a writable mount.
The report verifies the archive digest when available, distinguishes retained
files from verified restoration, and reports `can_resume: false`. A legacy
`capture: complete` or `retained_environment_required: false` never overrides
this boundary.

The [cooperative service cold recovery](service-cold-recovery.md) operator
entrypoint supports explicit service exports, independent probes and logical
clock restoration. It does not upgrade these legacy captures or authorize an
agent continuation. The original file-only two-service fixture in
[`test_recovery_docker`](../../scripts/terminal_bench/test_recovery_docker.py)
actually reconstructs a file-backed SQLite service and probes its data, RAM
and clock generation separately. Its successful file copy does not establish
restoration support for arbitrary benchmark services.

For a new DeepSeek stream failure, inspect the admitted call's `diagnostics`
in the final native JSONL result or Harbor usage metadata. The fields separate
HTTP status, request/response ID hashes, transport phase, validated event
sequence, body bytes, terminal presence and authoritative usage. Cancellation,
deadline, transport error and clean EOF without a terminal event have distinct
categories. They describe the configured HTTP hop and do not identify the
physical cause of a proxy or upstream reset. An absent diagnostic means the
adapter did not supply it; it never means a successful or zero-cost request.

For offline cost reports, use
[`report_costs`](../../scripts/terminal_bench/report_costs.py) with existing
statistics and explicit dated rate cards. It groups successful trials
separately, counts each unique trial once, preserves unknown/lower-bound usage,
and keeps bills distinct from price scenarios:

```bash
python -m scripts.terminal_bench.report_costs \
  --statistics build/experiment/statistics.json \
  --rates build/experiment/rates.json --output build/experiment/costs.json
```

A rate card has `name`, `currency`, `as_of`, `source`, and `models`, keyed by
the experiment's explicitly recorded `manifest.model_request`. Each model has
`cached_input_per_million`, `uncached_input_per_million`, and
`output_per_million`. Statistics are trial rows with unique `trial_path`,
`manifest`, `reward` and `usage`, as emitted by the experiment summarizer.
Historical recovered subsets use `partial_usage_lower_bound` with
`complete=false` and `totals`. When continuation records are missing, `totals`
retains validated usage and native cost receipts from the available segments.
Their known native estimates remain separate by currency and explicitly
incomplete; a missing segment is not a zero-cost segment. Older token-only
subsets cannot supply a native estimate. Cache and thinking subsets are not
charged twice.
No rate is guessed for an unlisted model, and `actual_billed_cost` stays null.
Choose the rate card in the account's settlement currency: providers can publish
independent CNY and USD prices, so their ratio is not a foreign-exchange rate.
The optional `--settlement-currency CNY` records the explicitly checked account
currency and identifies matching cards. This offline command does not access
the account or verify that declaration. There is no default USD conversion.

For a separate display view, add `--display-currency USD --fx fx.json`. An FX
snapshot has `base`, `quote`, `rate`, `as_of` (ISO date/time), and `source`;
`rate` means one unit of `base` buys that many units of `quote`. For example,
`{"base":"CNY","quote":"USD","rate":"0.14","as_of":"2026-10-03","source":"example fixture"}`
illustrates the schema, not a current exchange rate. The report preserves the
original price scenarios and adds `converted_estimates`, including the exact
snapshot used. It permits inversion of an explicitly supplied pair, rejects
duplicate or conflicting pairs, and never chains rates or infers missing ones.
Missing FX leaves the converted amount unknown. Each report freezes its supplied
rate; a later revaluation requires a new output file, so currency fluctuations
cannot silently rewrite historical estimates. Unknown and lower-bound usage
remain unknown and lower-bound after conversion. These views do not implement
a currency spending cap or reconstruct provider invoices.

The CLI checks each trial's Harbor `result.json` for continuation history, so
an older summarizer's latest-segment-only usage cannot undercount the run.
Continuation preserves every segment's model routes. Route `model` values are
configured profile names, not resolved vendor model IDs. Mixed or unknown
profiles, or missing routes in a partly described continuation, remain unpriced.
Older usage without any route fields
retains the explicit experiment-model assumption; a single-model estimate assumes the
experiment's recorded model applies to all counted calls and does not prove
the provider's resolved model identity.
This is a supplied-cohort estimate; it does not read the account's bill,
automatically infer historical prices/time bands, or enforce a currency limit.
The current runtime limits remain calls and reported tokens, with possible
in-flight overshoot.

The collector is invocation-local and distinct from durable Goal accounting.
Embedded consumers can explicitly share `QueryEngineConfig.RunUsage`;
existing Goal accounting remains authoritative and fails closed independently.
See [`RunUsage`](../../engine/execution/run_usage.go),
[provider admission](../../engine/execution/provider_usage.go), and
[headless composition](../../cmd/yhc/cmd/headless.go).

Runtime flags are command-local. Put them after the selected subcommand, for
example `yhc exec --provider openai "prompt"` or
`yhc goal run --resume SESSION_ID --max-continuations 8 --provider openai`
or `yhc serve acp --provider openai`. `serve mcp`, `version`, and
`completion` reject model/runtime flags because those entrypoints do not use
them.

Use the dedicated process only for an existing saved Goal:

```bash
yhc goal run \
  --resume SESSION_ID \
  --max-continuations 8 \
  --output-format json
```

It consumes no prompt or stdin. Exit `0` means the durable Goal is
`complete`; paused, blocked, budget/usage limited, waiting, not-runnable, or a
continuation limit return `1`; usage errors return `2`; cancellation after
durable stop handling returns `130`. JSON stdout is one versioned `goal_run`
object. Ordinary `exec` and `-p` still execute exactly one prompt.

The `sessions` tree has its own `--output-format text|json` flag and stable
`0`/`1`/`2`/`130` exits. It builds a provider-free administration host solely
to reuse the engine-owned session service, so list/rename/export do not start a
model runtime. `sessions resume` restores and reports then exits; use
`exec --resume` to continue with a prompt.

`config`, `doctor`, `mcp`, and `plugins` likewise own scoped
`--output-format text|json` flags and the same stable exit taxonomy. They do
not accept provider/model/runtime flags. `config show` and `doctor` construct
no provider runtime; `mcp list|get` never connect or launch configured servers;
`plugins validate` does not replace the live candidate, and `plugins reload`
replaces only the generation in that short-lived CLI process.

Disable mouse tracking with `--mouse=false` or
`YHC_DISABLE_MOUSE=1`. The TUI enables an alternate screen; use
`--plain` when terminal ownership is undesirable.

## Slash commands

Use `/help` for the registry's visible list and `/help NAME` for syntax. The
list is filtered by the active entrypoint and typed availability. Prefer these
verified outcomes:

| Task | Commands |
|---|---|
| Conversation | TUI/plain/headless: `/new`, `/clear`, `/compact`, `/sessions`, `/resume SESSION_ID`, `/fork`; old `/history`, `/rename`, and `/export` shortcuts are removed; use `yhc sessions ...` for provider-free administration or root `resume SESSION_ID` for the TUI |
| Runtime | `/model`, `/plan`, `/effort`; `/status`, `/context`, `/usage`, `/config`, and `/doctor` share one source-derived diagnostic snapshot |
| Interactive Goal | In a saved-root TUI or Plain Session unless `goal.enabled: false`, `/goal`, `/goal OBJECTIVE`, `/goal edit OBJECTIVE`, `/goal pause`, `/goal resume`, `/goal clear`, and `/goal budget POSITIVE_TOKENS`; `/goal OBJECTIVE` starts immediately even with no budget, and no shipped numeric budget is implied |
| Workspace | `/diff [stat|full|staged]`, `/files`, `/add-dir PATH`; `/init [--force]`; `/memory status` plus explicit `edit project`, `delete project`, or `migrate project` |
| Bundled workflows | TUI/plain: `/commit`, `/review`; `/help NAME` shows bundled source and trust. Removed `/summary`, `/onboarding`, `/pr-comments`, `/issue`, and `/commit-push-pr` (`/cpr`) return migration guidance rather than a prompt |
| Safety and diagnostics | `/permissions mode ...`, `/permissions bypass confirm`, `/permissions rules ...`; TUI-only `/terminal`; diagnostic fields always show state, source, and observation time and never expose credential values |
| Agents and tasks | `/tasks` labels its durable WorkBoard source and read-only command control; `/agents` is execution inspection; `/agent`, `/team`, `/queue`, and `/search` are TUI-owned |
| Extensions | `/mcp` for inspection, `/skills`, `/reload-plugins`; `/mcp add` does not yet synchronize model-visible tools |

The stable aliases are `/reset` for `/clear`, `/exit` for `/quit`, `/ctx` for
`/context`, and `/teams` for `/team`; they do not emit deprecation warnings.
Expired `/stats`, `/cost`, `/settings`, `/allowed-tools`, and `/bashes` names
return replacement guidance without executing `/usage`, `/config`,
`/permissions`, or `/tasks`. Removed `/login`, `/mode`, top-level `/bypass`,
and `/yolo` are non-discoverable tombstones; configure provider credentials
through their owning environment/configuration and use the typed
`/permissions` forms. `/new` is a canonical command: it commits a new durable
empty session and preserves the source transcript.

## Projection differences matter

- The TUI handles panels, selectors, theme/vim changes, queue UI, fork
  selection, and permission dialogs. Session export and fork persistence remain
  engine-service owned.
- Ctrl+T, Ctrl+B, `/team`, activity, sidebar, thread detail, and queued-input
  cancellation all use one bounded `TaskExplorerSnapshot` and exact
  engine-declared actions. Plain/headless and ACP add no explorer mutation
  surface; standalone MCP has only per-server ephemeral Task/Todo state.
- Goal is a saved-root TUI/Plain workflow enabled by default in supported
  production composition roots, with a separate explicit bounded automation
  consumer. Its command mutations,
  provider usage, budget, completion/blocker guards, and continuation
  admission remain engine-owned. The TUI status line projects reducer state;
  Plain labels automatic continuation and lifecycle progress in text while
  one stdin broker gives completed user input and permission/Plan interaction
  precedence. EOF or cancellation durably pauses active automatic work.
  `/goal` commands remain absent from both headless entrypoints. The dedicated
  `goal run` process can see `get_goal`/`update_goal` only during an exact
  admitted Goal turn and can only consume the dedicated cursor. Ordinary
  headless, unnegotiated ACP, child/review, ephemeral, disabled, and standalone
  MCP contexts retain no Goal authority. A negotiated ACP client still uses
  private capability negotiation and explicit continue rather than automatic
  dispatch.
- Plain mode and TUI project the same typed engine-owned command result for
  supported runtime mutations. TUI-only panels and selectors remain explicitly
  scoped.
- TUI, plain, headless, and ACP project the same five diagnostic command
  results. Unknown token, cost, credential, or connectivity facts stay
  unavailable; no entrypoint substitutes a local estimate.
- TUI, plain, headless, and ACP also share engine-owned `/diff`, `/files`, and
  explicit `/add-dir`. `/diff` reports non-Git, unavailable, failed, timed-out,
  and cancelled runner outcomes rather than returning an empty success.
  `/files` omits transcript-observed paths outside active workspace roots;
  `/add-dir` stores the canonical real directory for the current durable
  session and reports nested duplicates without broadening scope.
- `/copy [N]`, `/keybindings`, `/terminal`, and `/search` are TUI-only.
  `/copy` is hidden when the TUI is not interactive and selects only completed
  assistant responses; it does not create a backup file. `/terminal-setup` has
  been removed because it never performed setup.
- `/init` and project-scoped `/memory edit|delete|migrate` return follow-up
  prompts. Git and filesystem work then uses the ordinary Agent tools and the
  active permission flow; the slash handlers do not write configuration,
  launch an editor, or delete files directly. Plain mode can run these prompt
  workflows; headless and ACP do not discover them.
- `/commit` and `/review` are versioned bundled prompt content, not privileged
  Go handlers. They enter the same atomic generation as configured plugin
  prompt commands, remain subject to ordinary tool permissions, and are absent
  when the bundled pack is disabled. A malformed bundled or configured reload
  retains the previous whole generation instead of partially replacing help
  or dispatch. Removed defaults return typed guidance, while qualified
  configured-plugin workflows remain independent.
- `/help` and the TUI help overlay group all currently reachable primary and
  secondary commands by category. The empty command palette shows at most
  three process-local Recent commands followed by the twelve ordered Suggested
  commands; typing searches the complete reachable set. Recent entries are
  recorded only after fresh admission and are never persisted.
- While a TUI request is running, discovery exposes only `/agent`, `/team`,
  `/keybindings`, and `/queue`. Direct dispatch rechecks the live phase, so a
  command selected from an idle snapshot cannot execute after the turn starts.
- Headless `exec` and compatibility `-p` pass through the same `QueryEngine`,
  registry, and typed result projection, but discovery intentionally exposes
  only its small supported command set. `exec` is prompt execution, not a
  second administration runtime.
- ACP protocol resume/load/fork remains supported. ACP slash `/new`, `/resume`,
  and `/fork` fail before mutation because the protocol host owns the external
  session-handle map; `/clear` and `/compact` remain engine-owned. Protocol
  fork registers a separate child handle only after durable restore. ACP
  advertises model/effort configuration and permission modes from the engine
  capability result, and projects successful changes through protocol updates.
- ACP uses `QueryEngine` per ACP session. Standalone MCP directly dispatches
  registry tools and has no conversation, compaction, transcript, or slash
  command runtime.

## Removed commands in the current build

P21.0 and P21.2 moved retired names out of the active command catalog.
The separate catalog contains 27 canonical tombstones and reserves 33
canonical-or-alias keys. Dispatching any reserved key returns typed removal
and replacement guidance with `ActionNone`; it never invokes the replacement
or mutates runtime state. In particular, `/logout` deletes no credentials, and
`/plugin`, `/color`, `/fast`, `/tag`, `/share`, `/release-notes`, `/rewind`,
`/undo`, `/branch`, and `/rewrite` are not supported workflows. Use `/fork` or
version control instead of treating them as recovery primitives.

The current command matrix is owned by
[`commands.md`](../architecture/capabilities/commands.md). The source-backed
P16 comparison is retained in
[`command-surface-audit.md`](../migration/reference/runtime/command-surface-audit.md);
the closed simplification program is recorded in
[`P21 history`](../migration/history/runtime/p21-command-surface-simplification.md).
The default
bundled pack contains only `/commit` and `/review`; the five removed canonical
workflows plus `/cpr` return P21.2 migration guidance.

## Maintainer reference

| Concern | Source |
|---|---|
| Cobra dispatch | [`root.go`](../../cmd/yhc/cmd/root.go), [`headless.go`](../../cmd/yhc/cmd/headless.go), [`headless_goal.go`](../../cmd/yhc/cmd/headless_goal.go), [`diagnostics_extensions.go`](../../cmd/yhc/cmd/diagnostics_extensions.go) |
| Command registry | [`registry.go`](../../engine/commands/registry.go) |
| Workspace command service | [`workspace_commands.go`](../../engine/workspace_commands.go), [`execution_controls.go`](../../engine/execution_controls.go) |
| Entrypoint projections | [`app.go`](../../internal/tui/app.go), [`root.go`](../../cmd/yhc/cmd/root.go), [`agent.go`](../../server/acp/agent.go) |
| Architecture | [Runtime](../architecture/runtime/README.md) |
