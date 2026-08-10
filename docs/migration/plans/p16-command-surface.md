# P16 Command Surface Productization Plan

**Status:** historical
**Completed:** 2026-07-22
**Last verified:** 2026-07-23

> **Ownership:** completed P16 contracts for making every visible CLI or slash
> command truthful, safe, useful, entrypoint-aware, and single-owner

Root [`migration/PLAN.md`](../PLAN.md) owns global execution order and slice
state. Current behavior belongs in
[`architecture/capabilities/commands.md`](../../architecture/capabilities/commands.md).
The complete inventory and reference decision are in
[`migration/reference/runtime/command-surface-audit.md`](../reference/runtime/command-surface-audit.md).

This is a frozen historical contract. Present-tense checklist language below
records the acceptance boundary used during delivery; it is not ready work.

## Decision

P16 is a **`combine`** decision:

- preserve Eino-Agent's useful session, context, safety, Agent, task, and
  extension outcomes;
- adapt Codex and Pi's explicit command type, scope, availability, lifecycle,
  compaction, and lineage semantics without adopting Pi's tree-storage
  mechanism;
- combine Crush and OpenCode's dynamic prompt-command approach with the
  existing atomic plugin snapshot;
- keep runtime mutation and durable session state project-native;
- reject unsafe, stale, placeholder, vendor-hosted, or implementation-free
  command names.

The success metric is not fewer or more commands. P16 closes when every visible
command has a real user outcome, a declared entrypoint set, one side-effect
owner, truthful help, and deterministic tests.

The research narrows the build target further than the pre-P16 66-command
registry:

- daily core: session lifecycle, model/plan/permission control, truthful
  diagnostics, and high-value workspace inspection;
- capability-gated core: Agent/task/skill/MCP/hook inspection and atomic plugin
  reload;
- TUI-only actions: terminal presentation, search and pickers;
- bundled workflows: prompt recipes that continue through ordinary tool
  permissions;
- dependency-gated recovery: undo/redo/rewrite/branch/rewind, hidden until
  durable replay and rollback exist.

## User Problem

The current registry exposes commands based on registration rather than usable
capability. Some work only in the TUI, some silently fail in plain mode, some
execute but their output disappears in headless/ACP, and some advertise effects
that do not exist. Before P16.H0, `/logout` also deleted credential files
outside the project's owned state; that safety gap is now contained.

This creates three costs:

1. users cannot predict whether a visible command will work in their current
   mode;
2. maintainers can fix one entrypoint while leaving another action switch
   inconsistent;
3. adding reference commands increases surface area without increasing product
   value.

## Scope And Non-Goals

### In scope

- Cobra administration commands and interactive slash commands;
- built-in, bundled workflow, plugin, skill, and MCP prompt-command discovery;
- TUI, plain, headless, and ACP command availability and projection;
- command parsing, validation, action ownership, safety classification, help,
  aliases, and deprecation;
- durable session mutations invoked by commands.

### Not in scope

- making standalone MCP a conversation/slash entrypoint;
- copying all Claude, Codex, Grok, OpenCode, Crush, or Pi command names;
- implementing hosted sharing, update distribution, marketplace trust, voice,
  media generation, cloud dashboards, billing, or employee-only workflows;
- changing provider, permission, transcript, plugin-trust, or file-history
  semantics beyond the named command contract;
- using P16 to bypass P13 kernel gates or create a second runtime loop.

## Frozen Program Invariants

1. A visible command is executable on the current entrypoint or is excluded
   from discovery with a machine-readable reason.
2. Runtime or durable state mutates exactly once under an engine/service owner;
   renderers never repeat the mutation.
3. Parsing is quote-aware, validation runs before execution, and malformed
   safety rules fail closed.
4. Command execution accepts `context.Context`; cancellation cannot be lost at
   the dispatch boundary.
5. Help, completion, palette, and dispatch read the same command metadata.
6. A destructive command names the owned store and rollback boundary. No
   command deletes another product's state.
7. Plain, headless, and ACP never silently discard the result of a command they
   advertise. TUI-only projection remains TUI-only.
8. Session history mutations update live memory, transcript/catalog state, and
   replay behavior atomically or fail without partial success.
9. Plugin/bundled prompt-command replacement remains collision-safe and atomic;
   failed reload leaves the previous snapshot live.
10. Compatibility aliases are hidden and time-bounded; an alias cannot preserve
    semantically wrong behavior.

## Frozen Target Product Surface

| Layer | Canonical outcomes | Compatibility/consolidation |
|---|---|---|
| Session core | `/help`, `/new`, `/clear`, `/compact`, `/sessions`, `/resume`, `/fork`; provider-free `sessions list/resume/rename/export/fork` | Slash `/resume` remains the canonical direct shortcut; `/sessions` owns interactive list/search plus rename/export. Cobra uses `sessions resume`; `/history`, `/rename`, and `/export` were removed at P16.7b. |
| Execution and safety | `/model`, `/effort`, `/plan`, `/permissions` | `/permissions` owns typed modes including bypass. Generic `/mode`, top-level `/bypass`, and `/yolo` are not canonical. |
| Diagnostics | `/status`, `/context`, `/usage`, `/config`; shared doctor/version services | `/stats` and `/cost` converge on `/usage`; `/settings` converges on `/config`; estimates and hard-coded provider/release facts are removed. |
| Workspace | `/diff`, `/files`, `/copy`, `/add-dir`, `/init`, `/memory` | Terminal/path/workspace availability is explicit. `/init` is project-native. |
| Extensibility | `/tasks`, `/agents`, `/skills`, `/mcp`, `/hooks`, `/reload-plugins` | Inspection is accepted first. MCP/plugin mutation remains dependency-gated. `/team` converges on `/agents` or a TUI picker. |
| TUI-only | `/theme`, `/vim`, `/keybindings`, `/terminal`, `/search`, `/suspend`, `/quit`; Agent/team/queue pickers | Hidden from plain/headless/ACP discovery; palette-only actions need not have universal slash names. |
| Bundled workflows | `/review`, `/commit`, `/summary`, `/issue`, `/pr-comments`, `/onboarding`, `/commit-push-pr` | Loaded as versioned prompt data; source/trust shown; no privileged side-effect path. |
| Deferred recovery | `/undo`, future `/redo`, `/rewrite`, `/branch`, `/rewind` | Hidden until append-only reversible events, restart replay and rollback are proved; rewind additionally needs workspace snapshots. |

This table is the selection boundary. A later slice may choose exact Go type or
subcommand names, but it cannot reintroduce a rejected/deferred surface merely
to retain command-count parity.

## Target Command Contract

The exact Go names remain slice-local, but P16.2 must express these fields in
one canonical registry record:

| Field | Required meaning |
|---|---|
| identity | canonical name, aliases, category, description and usage |
| kind | query, runtime mutation, durable session mutation, UI action, prompt workflow, or administration |
| entrypoints | TUI, plain, headless, ACP, and CLI administration as explicit capabilities |
| availability | supported, hidden, disabled, or unavailable with a reason and optional dependency gate |
| arguments | quote-aware typed schema and validation result |
| side effects | none, process-local, workspace, durable session, credential/auth, or external service |
| execution | cancellation-aware handler returning one typed result |
| compatibility | deprecated aliases and removal boundary |

`/help` and completion filter this record for the active entrypoint. A direct
call to an unavailable command returns the same explicit reason instead of
falling through to a partial action switch.

## Ordered Slices

| ID | Atomic outcome | Depends on | State |
|---|---|---|---|
| P16.H0 | Contain unowned credential deletion | None | **Completed 2026-07-18** |
| P16.0 | Repair reproduced plain/parser correctness failures and fail closed on incomplete history mutation | P16.H0 | **Completed 2026-07-18** |
| P16.1 | Stop advertising false or semantically wrong commands | P16.0 | **Completed 2026-07-18** |
| P16.2 | Install one typed, quote-aware, availability-aware command contract | P16.1 and P13.0 baseline | **Completed 2026-07-18** |
| P16.3 | Make one engine/service boundary apply each command action once | P16.2 | **Completed 2026-07-20** |
| P16.4a1 | Make new/clear/compact/direct-resume lifecycle durable | P16.3 | **Completed 2026-07-20** |
| P16.4a2 | Consolidate sessions discovery/actions and the resume picker | P16.4a1 | **Completed 2026-07-20** |
| P16.4b | Make fork creation, lineage and switch atomic | P16.4a2 | **Completed 2026-07-20** |
| P16.5a | Converge model/effort/plan/permissions execution and safety | P16.3 | **Completed 2026-07-22** |
| P16.5b | Build source-derived status/context/usage/config/doctor diagnostics | P16.3 | **Completed 2026-07-22** |
| P16.5c | Converge workspace and terminal-capability commands | P16.3 | **Completed 2026-07-22** |
| P16.5d | Converge Agent/task/skill/MCP/hook/plugin inspection | P16.2 | **Completed 2026-07-20** |
| P16.6 | Move prompt macros into an atomic bundled workflow pack | P16.2 | **Completed 2026-07-22** |
| P16.7a | Add explicit exec/serve/version/completion CLI foundations | P16.2 | Completed 2026-07-22 |
| P16.7b | Add sessions inspection and lifecycle CLI projections | P16.7a and P16.4b | **Completed 2026-07-22** |
| P16.7c | Add config/doctor/MCP/plugin inspection CLI projections | P16.7a, P16.5b and P16.5d | **Completed 2026-07-22** |

## P16.H0 Unowned Credential Deletion Containment

**State:** completed 2026-07-18.

### User outcome

Invoking `/logout` cannot delete project or user `.claude` credential files.
The command explains that environment/config credentials must be removed from
their actual owner.

### Allowed change

- remove `clearCachedCredentials` and `clearProviderCredentials` execution from
  the slash path;
- return a non-mutating unsupported/configuration result during the short
  compatibility window;
- add filesystem fixtures proving project and user credential files remain
  byte-identical for empty, known-provider, unknown-provider, and cancelled
  invocations.

### Excluded change

- creating a new token store;
- editing shell profiles or environment variables;
- invoking vendor logout APIs;
- deleting any compatibility credential file.

### Promotion gate and rollback

Tests use isolated temporary directories and prove zero deletion. P16.H0
removes destructive code rather than hiding it behind a runtime flag. Rollback
may restore the informational command, never the unowned deletion behavior.

## P16.0 Plain And Parser Correctness

**State:** completed 2026-07-18.

### User outcome

Supported plain commands do not report success without applying the requested
operation, do not apply it twice, and do not corrupt quoted permission rules.

### Required fixes

- `/clear` either clears through the current engine owner once or returns an
  explicit unsupported result; no `SetResumedMessages(nil)` success path;
- `/compact` is applied through the current engine action path or rejected
  explicitly;
- `/fork` reads the canonical `new_session_id` result and switches exactly once;
- `/undo` cannot truncate twice: until a durable reversible-history owner is
  accepted, it returns explicit unavailable before any mutation;
- the production dispatch path preserves quoted arguments and invokes command
  validation, especially for permission rules;
- focused tests cover TUI/plain/headless/ACP visibility for these commands even
  before the full P16.2 metadata refactor.

### Atomicity and rollback

This slice fixes reproduced behavior without redesigning the registry or adding
commands. If a cross-entrypoint operation cannot be made safe within the
current owner, it becomes explicitly unavailable until P16.3 rather than
keeping a false success path.

## P16.1 Truthful Visibility And Alias Cleanup

**State:** completed 2026-07-18.

### User outcome

Default help no longer advertises commands whose current effect is absent,
stale, unsafe, or dependency-free.

### Required changes

- remove `/new` as a `/clear` alias; retain `/reset` temporarily;
- remove `/logout` from visible commands after P16.H0's compatibility message;
- default-hide `/plugin`, `/bug`, `/undo`, `/rewrite`, `/branch`, `/rewind`,
  `/color`, `/fast`, `/tag`, `/share`, `/release-notes`, and
  `/terminal-setup`;
- default-hide generic `/mode`, top-level `/bypass`, and `/yolo` until typed
  `/permissions` owns the equivalent safety transitions;
- converge `/team` and `/queue` on `/agents`, `/tasks`, or explicit TUI palette
  actions instead of advertising them as universal runtime controls;
- keep hidden `/env`, `/output-style`, and legacy `/session` hidden pending
  removal/consolidation;
- add direct regression tests for visible names and aliases;
- record a compatibility note for each hidden/removed name. No silent fallback
  may perform its old side effect.

P16.1 does not implement replacements. It makes the current product truthful
before the structural refactor.

### Delivered boundary

- default discovery is frozen by test at 46 visible and 20 hidden built-ins;
- `/new` is no longer registered, `/reset` remains the only `/clear` alias,
  and the removed name cannot enter the engine clear path;
- every hidden unsupported canonical name and alias returns an explicit
  compatibility reason with `ActionNone`;
- `/mode` and `/rewrite` no longer bypass the registry in the TUI, while
  `/team` and `/queue` remain explicit TUI-local interactions; and
- no Eino dependency, Graph topology, kernel selector, action schema, or
  durable state changed. Typed availability remains P16.2 scope.

## P16.2 Canonical Command Contract

**State:** completed 2026-07-18.

### User outcome

Help, completion, dispatch, and error messages agree about which commands exist
and where they work.

### Required changes

- converge `Registry.Dispatch` and `CommandDispatcher` into one production
  parser/validator/executor; delete the unused alternative after migration;
- pass `context.Context` through every handler;
- add kind, entrypoint, availability, dependency, side-effect, and typed-result
  metadata;
- reject built-in, alias, bundled, and plugin collisions before installing a
  candidate snapshot;
- register existing TUI-local `/search` and other local actions with explicit
  TUI-only availability rather than a hidden vocabulary;
- generate `/help`, detailed help, completion, and palette entries from the
  same filtered snapshot;
- add a table-driven capability matrix test for every canonical command and
  alias on TUI, plain, headless, ACP, and CLI-administration scopes.

### Non-goals

No command gains a new runtime effect in this slice. Metadata cannot claim
support merely to preserve current help output.

### Rollback

Keep no live dual registries. Rollback restores the previous registry as one
code change; plugin snapshots and transcripts require no migration.

### Delivered boundary

- after entrypoint ownership classification, `Registry.Dispatch` is the only
  strict parse, validation, availability, cancellation, and execution
  boundary; the test-only `CommandDispatcher` was deleted;
- one canonical record declares kind, entrypoints, availability and reason,
  dependency, side-effect, result kind, compatibility, arguments, and a
  context-aware handler;
- registration normalizes canonical names and aliases, rejects built-in,
  alias, case-only, and plugin collisions before installation, and returns
  cloned immutable snapshots;
- TUI help, completion, palette, and dispatch use the same TUI-filtered
  snapshot; `/search`, `/team`, and `/queue` are explicit TUI-only records;
- the all-command and alias matrix proves exact behavior for TUI, plain,
  headless, ACP, and CLI-administration entrypoints, while parser edge/fuzz,
  cancellation, collision, immutable-snapshot, and plugin atomicity tests
  cover negative paths; and
- no command gained a new runtime effect. Legacy `Action`/`Data` application
  and headless/ACP command-result projection remain P16.3 scope.

## P16.3 Single Action Owner And Typed Projection

**State:** completed 2026-07-20.

### User outcome

A command mutates state once and every supported renderer displays the same
typed success, failure, or unsupported outcome.

### Required changes

- introduce one engine/service command executor for runtime and session actions;
- replace untyped `Data` key conventions for migrated commands with typed
  result payloads or validated accessors;
- emit one command-result event that headless and ACP can project;
- reduce TUI/plain action switches to presentation-only UI actions or calls to
  the canonical executor;
- preserve TUI selectors/panels as projections without making them state owners;
- test no duplicate clear, compact, model/permission, fork, rename, or add-dir
  mutation across entrypoints, and prove deferred history commands fail before
  mutation;
- keep standalone MCP excluded.

P16.3 must not move generic Agent execution or live queue ownership ahead of
P13. It changes command action ownership only.

### Delivered boundary

- the canonical command record declares `engine` or `entrypoint` execution
  ownership, and engine submission rejects entrypoint-owned handlers before
  dispatch;
- engine-owned handlers return read-only results or validated mutation
  intents; one `QueryEngine` executor applies clear, compact, resume, model,
  Plan, effort, add-dir, fork, rename, permission-rule, and plugin-reload
  effects;
- `EventCommandResult` carries command, action, succeeded/failed/unsupported
  status, output, error, and an optional interactive follow-up before one
  terminal event;
- command and model turns share one admission boundary; a rejected concurrent
  command cannot mutate state or consume runtime sequence, while Plan-mode
  commands publish transition/result/terminal through one replayable stream;
- the emitter freezes submit-time identity, so resume/fork complete the source
  command turn even when the next turn uses a new active session/thread;
- runtime replay retains bounded typed
  command/action/status/error/follow-up fields,
  while TUI, plain, headless, and ACP only project the result or explicitly
  unsupported outcome;
- TUI resume/model selectors, help/MCP panels, prompt workflows, terminal
  lifecycle, theme, and other UI actions remain entrypoint projections;
  standalone MCP, generic Agent execution, live queue ownership, Eino
  dependencies, Graph topology, and durable schemas are unchanged; and
- P16.4b subsequently closed atomic fork create/metadata/switch compensation;
  P16.3 remains the proof of one live action owner and source-bound typed
  projection.

## P16.4a Core Session Lifecycle

**State:** completed 2026-07-20; P16.4b also completed.

### Selected surface

- add a true `/new` that creates a new session identity without erasing the old
  transcript;
- preserve `/clear` as appending a reset boundary in the current session:
  subsequent model context excludes prior turns, while the prior transcript
  remains auditable; it is not a transcript-delete command;
- preserve `/compact` with one compact event and durable boundary;
- combine `/history` and hidden `/session` discovery under `/sessions`;
- keep slash `/resume [session]` as the canonical high-frequency direct
  shortcut backed by the same service and picker as `/sessions`;
- move rename and export under `/sessions`, keeping semantically equivalent
  compatibility shortcuts only until the P16.7b CLI projection replaces them;
- keep `resume` as both an explicit CLI operation and a slash outcome.

### P16.4a1 delivered boundary

- normal production checkpoints append only new role-derived message records;
  they no longer rewrite the whole JSONL or repeat complete state at every safe
  point;
- additive `session-start`, `reset-boundary`, `compact-boundary`, and reserved
  `state-checkpoint` records let new readers select active messages,
  replacements, and file state while retaining pre-boundary audit lines;
- `/new` persists full metadata and a fsynced empty-session marker before
  activation, then uses the same engine restore service as direct `/resume`;
- `/clear` and `/compact` use the lifecycle record as their commit point and
  change live state only after persistence succeeds;
- ordinary-message writes advance their durable cursor only after sync;
  transient failures repair through one full checkpoint, unrepaired final
  failures surface `persistence_error`, and post-write sync failures are
  explicitly indeterminate;
- resumed identity resets session-local usage, activity, denial, result/cache,
  replacement, and file-state owners before reconstructing durable state;
- new sessions pin the production `project_graph/v1/full` kernel; resumed
  sessions keep and validate their persisted ProjectGraph kernel metadata;
- TUI/plain/headless expose `/new`; ACP slash `/new` and `/resume` are excluded
  before handler execution because the ACP session map has no atomic identity
  remap. ACP protocol load/resume remains supported; and
- focused restart, source-preservation, persistence-failure, auto/manual
  compact, active/audit replay, entrypoint-scope, and TUI-rebind tests close the
  durable core.

This sub-slice is `combine`: it preserves project-owned `QueryEngine`, command,
session, kernel-pin, permission, runtime-event, and TUI contracts while adapting
append-only lifecycle markers. It does not change Eino, Compose Graph topology,
fork lineage, archive/delete, or CLI administration.

### P16.4a2 delivered boundary

- one engine-owned `SessionService` now owns bounded catalog query, exact-source
  resume, rename, and persisted Markdown export; explicit IDs resolve the
  transcript root they were listed from and duplicate IDs fail before mutation;
- canonical `/sessions list [limit]`, `/sessions search <query> [limit]`,
  `/sessions resume <id>`, `/sessions rename <id|current> <name>`, and
  `/sessions export <id|current> [filename]` share typed engine intents;
- no-argument TUI `/resume`, direct `/resume [id]`, startup resume, and
  `/sessions resume` use that service. The TUI picker remains a projection and
  carries its selected source locator into the same restore owner;
- `/history`, `/rename`, and `/export` were delivered as hidden, directly
  executable exact compatibility shortcuts; P16.7b later removed them after
  promoting the same service to the provider-free CLI;
- export reads the durable transcript rather than a TUI-local live renderer,
  flushes the active recorder, and commits a same-directory temp file through
  atomic replace. Existing directory and symlink targets fail closed; Windows
  uses replace-existing plus write-through semantics;
- ACP slash `/sessions`, `/new`, and `/resume` remain unavailable before
  mutation, while explicit ACP session protocol adapters remain supported; and
- the P16.4a1 append-only schema, Eino dependencies, Compose Graph topology,
  query-kernel selection, and P16.4b fork behavior are unchanged.

### Promotion gate

Fresh, resumed, compacted, and cleared sessions replay deterministically after
process restart. TUI/plain/headless/ACP either produce the same lifecycle state
or declare the operation unavailable before mutation.

## P16.4b Durable Fork Lifecycle

**State:** completed 2026-07-20.
**Decision:** `combine`

### Selected surface

Preserve and adapt `/fork` as the single accepted near-term lineage operation.
It creates a child session from a chosen current-session boundary and switches
only after the child transcript/catalog record is durable.

### Promotion gate

- fork identity and lineage survive restart;
- fork switches only after the new session is durable;
- a failed persistence step leaves the original live and durable state active;
- repeated invocation cannot double-apply a prior typed result;
- TUI selectors call the same service used by direct plain/ACP requests.

### Delivered boundary

- `SessionService.CreateFork` is the single durable create owner for the active
  engine, a TUI-selected source, and ACP protocol fork;
- one operation ID deterministically selects one child. A retry loads the
  matching committed child instead of creating another, while an existing
  child owned by another operation is never replaced;
- `BranchSession` adapts source execution metadata into a complete child record:
  model/provider, permission mode, query-kernel pin, Plan state, CWD and
  additional directories survive; the Plan capability is rebound to the
  child-owned file identity and an in-flight approval normalizes to Active
  without callback authority; child identity/timestamps/lineage are new;
  pending request, Agent runtime, revision, and worktree-ownership fields are
  cleared;
- `Recorder.BranchWithState` writes copied active messages, replacements, file
  snapshots, lineage, branch name, operation marker, and full child metadata
  into one unique same-directory temp file, syncs it, installs the child
  without replacement, and syncs the parent directory;
- persistence failure before install exposes no child. A post-install
  durability failure is explicitly indeterminate and retry inspects the
  operation marker rather than reapplying the fork;
- TUI/plain/headless activate only after commit with cancellation removed from
  the activation phase. A restore failure removes only the child bearing the
  matching operation identity and keeps the source active;
- TUI picker fork mode calls the same service and retains the source/picker on
  failure. ACP slash `/fork` is unavailable before handler execution, while
  ACP protocol Fork leaves the source handle unchanged and registers a child
  handle only after durable restore; and
- Eino dependencies, Compose Graph topology, query-kernel selection rules,
  lifecycle record kinds, and public Eino schemas are unchanged.

### Promotion evidence

- transcript tests cover complete child composition, source byte preservation,
  pre-commit failure, post-commit uncertainty, and no-clobber behavior;
- session/service tests cover restart metadata, copied auxiliary state,
  deterministic retry, conflicting-operation rejection, stale active-boundary
  rejection, activation compensation, and post-commit cancellation;
- direct engine, TUI picker, and ACP protocol tests cover supported/rejected
  entrypoints, source identity, cold restore, and explicit handle ordering; and
- focused race/platform compilation plus repository Makefile, docs, manifest,
  and diff gates close the slice.

`/branch`, `/undo`, `/redo`, `/rewrite`, and `/rewind` are not part of this
slice. They require the separate dependency gates below; fork cannot smuggle in
their restore or reversible-edit semantics. Fork also does not copy the source
session's external Plan Markdown file as a second persistence artifact. It
preserves the Plan phase and return context in child metadata, binds the child
to its own exact Plan path, and requires the child to write its own Plan content
when needed. A cross-file atomic artifact bundle is outside this slice.

## P16.5a Execution And Safety Controls

**State:** completed 2026-07-22.
**Decision:** `combine`

### Selected surface

- `/model` validates the resolved provider/model inventory before mutation;
- `/effort` is visible only when the selected provider/model exposes a
  compatible reasoning capability;
- `/plan` has one durable runtime owner and returns the effective state;
- `/permissions` owns a typed mode and rule schema, including an explicitly
  confirmed bypass transition.

Generic `/mode`, top-level `/bypass`, and `/yolo` are removed as canonical
names. A temporary alias may call the exact `/permissions` transition only when
the confirmation and entrypoint contract are identical.

### Promotion gate

Help, completion and direct dispatch use the same capability result. Unsupported
models/effort levels fail before mutation; permission changes apply once; TUI,
plain, headless and ACP never disagree about the effective mode.

### Delivered contract

- one contextual registry capability result drives discovery, help,
  completion, palette, and dispatch;
- provider-runtime resolution validates `/model` before mutation, and
  provider/model capability gates the real request-level `/effort` value;
- one serialized engine boundary owns plan, confirmed permission mode, and
  typed permission-rule mutation across entrypoints;
- TUI direct controls and ACP configuration/mode protocol projections reuse
  those engine owners and reject active-turn races without mutation; and
- unsupported generic mode/bypass aliases remain hidden. Reasoning effort is
  explicitly process-local and is omitted after an incompatible fallback.

Completion evidence is retained in
[`post-parity.md`](../history/runtime/post-parity.md#p165a-unified-execution-and-safety-controls).

## P16.5b Truthful Diagnostics

### Selected outcomes

| Target | Inputs consolidated | Contract |
|---|---|---|
| `/status` | runtime, session and provider summary | Compact source-derived snapshot with freshness and links to detail commands. |
| `/context` | context-window and compaction detail | Actual message/tool/system contributors and known/unknown token fields. |
| `/usage` | `/stats`, `/cost`, provider/session usage | Aggregate actual persisted usage; omit money without authoritative provider/model cost. |
| `/config` | `/settings`, auth/provider hints, effective config | Redacted values, source and precedence; never print secrets or pretend auth exists. |
| `doctor` | executable environment/provider/runtime checks | Shared diagnostic service with stable check IDs and terminal states. |

Every field is `known`, `unavailable`, `stale`, or `refreshing`; zero values
cannot impersonate facts. `/login` is visible only if an owned provider auth
flow exists. Hard-coded release text, generic pricing, and four-chars-per-token
accounting are removed.

### Completion evidence

- one renderer-neutral `QueryEngine.DiagnosticsSnapshot` supplies status,
  context, usage, config, and stable-ID doctor results to TUI, plain, headless,
  and ACP slash-command entrypoints;
- cumulative provider-reported usage survives clear, compact, checkpoint,
  session start, child admission, and restart. Missing response metadata,
  including empty assistant responses, legacy boundaries, and transcript
  corruption retain explicit coverage rather than becoming zero. Compaction
  request usage remains cumulative but cannot masquerade as post-compact
  active-context usage;
- provider resolution exposes only field source, credential presence, and a
  scheme/host endpoint. Secret values, suffixes, URL userinfo/path/query/
  fragment, hard-coded billing, and connectivity guesses are excluded;
- `/usage` replaces canonical `/stats` and `/cost`, `/config` replaces
  `/settings`, and the old names remain deprecated compatibility aliases for
  one documented window. `/login` is hidden because no auth flow is owned; and
- the TUI status/spinner use provider-reported usage plus a known context
  window and no longer render a generic price, price-derived warning, or
  inferred auto-compaction token savings.

## P16.5c Workspace And Terminal Capabilities

**State:** completed 2026-07-22.
**Decision:** `combine`

### Selected surface

- `/diff` and `/files` are read-only workspace outcomes with explicit non-Git
  and command-runner terminal states;
- `/copy` is TUI/terminal-capability gated and reads a committed assistant
  result rather than partial streaming output;
- `/add-dir` canonicalizes and validates a path against workspace/sandbox
  policy before atomically updating active roots;
- `/init` creates project-native AGENTS/config material and sends any follow-up
  prompt through ordinary tools and permissions;
- `/memory` defaults to read-only browse/status; edit/delete/migrate require
  explicit scoped subcommands;
- `/keybindings`, `/terminal`, and `/search` remain TUI-only projections, with
  `/terminal-setup` removed as a false setup action.

### Promotion gate

Path and terminal capability failures occur before side effects; no workflow
handler bypasses Bash/Git/file permission; TUI-only names are absent from
plain/headless/ACP discovery.

### Delivered boundary

- `/diff` and `/files` are engine-owned across TUI, plain, headless, and ACP.
  One injected read-only Git runner supplies explicit non-Git, unavailable,
  failed, timed-out, cancelled, and initial-repository outcomes; observed file
  paths are canonicalized against detached active roots;
- `/add-dir` returns a raw intent and the engine action boundary expands home,
  resolves symlinks, rejects unsafe/non-directory paths, deduplicates coverage,
  and commits one canonical additional root under the active turn owner. The
  normal final checkpoint persists the root;
- `/copy` is a contextual TUI-only action. It selects a committed assistant
  message and performs no handler-side clipboard or temp-file I/O;
- `/init`, project-scoped `/memory` mutations, and `/commit` perform no direct
  Git, editor, or file mutation. Their prompts require ordinary Agent tools and
  permissions; memory status remains read-only; and
- `/keybindings`, `/terminal`, `/search`, and `/copy` are absent from
  plain/headless/ACP. `/terminal-setup` is no longer registered.

### Promotion evidence

- focused command/engine tests cover every runner terminal state, repositories
  before an initial commit, cancellation before process start, Git redirect
  isolation, external-diff/textconv suppression, and end-to-end headless
  projection;
- canonical-path tests cover symlink aliases, existing-root coverage,
  invalid-file rejection before mutation, detached file projection, outside-root
  omission, and checkpointed additional roots;
- registry and TUI-context tests cover clipboard capability admission,
  committed-only copy intent, no temp-file handler, TUI-only discovery, removed
  terminal setup, and permission-mediated init/memory workflows; and
- repository Makefile, documentation, manifest, diff, focused race, and
  independent-review gates close the slice.

## P16.5d Extensibility And Orchestration Inspection

**State:** completed 2026-07-20.
**Decision:** `combine`

### Selected surface

- `/tasks` and `/agents` use engine-owned runtime snapshots; team/thread pickers
  remain scoped TUI actions;
- `/skills`, `/mcp`, and `/hooks` expose source, availability and health without
  claiming unsupported mutations;
- `/reload-plugins` validates a whole candidate generation, reports aggregated
  per-source diagnostics, rejects canonical/alias collisions, and atomically
  swaps help/palette/dispatch together;
- the current fake `/plugin` administration surface remains hidden.

### Promotion gate

Built-in safety controls cannot be shadowed. A malformed dynamic source leaves
the prior generation live. MCP inspection agrees with manager inventory, while
MCP/plugin mutation remains unavailable until persistence, reload,
model-visible generation synchronization and rollback are one transaction.

### Delivered boundary

- `QueryEngine.RuntimeInspectionSnapshot` is the sole command-facing read
  model for `/tasks`, `/agents`, `/skills`, `/mcp`, and `/hooks`. Command
  handlers no longer fall back to package-global task, Agent-definition, or
  configuration loaders;
- every top-level QueryEngine owns or receives one `tools.TaskManager`, injects
  it into the canonical Task tool family, shares it only with child engines in
  that root lineage, and supplies it to TUI task controls and inspection;
- task and MCP mutation-shaped arguments return an explicit read-only or
  unavailable result before manager/tool side effects. `/agent` is scoped to
  the TUI picker instead of advertising a universal runtime action;
- skill loading retains source and malformed-file diagnostics; hook inspection
  clones the exact executor configuration; MCP inspection reads one
  manager-revision inventory including failed-server health categories;
- plugin reload discovers and validates the complete configured source set,
  aggregates all source/command diagnostics, qualifies names and aliases,
  calculates a functional digest, and rejects any invalid candidate;
- the registry performs final built-in/name/alias collision checks and swaps
  command map, ordering, revision, digest, sources, and diagnostics under one
  lock. Help, completion, palette, and dispatch therefore observe either the
  complete old generation or the complete new generation; and
- a rejected candidate reports the retained live generation. Plugin
  installation/trust, symlink-resolved containment, non-command contributions,
  and MCP model-visible registry synchronization remain outside this slice.

### Promotion evidence

- command tests prove all five inspection handlers use the engine snapshot,
  `/tasks kill`, MCP add/remove/restart, and non-TUI `/agent` cause zero
  mutation;
- two-engine and focused race tests prove Task tools, inspection, lifecycle
  draining, and TUI controls use the injected lineage manager without
  cross-engine leakage; a real child TaskCreate round proves its single drain
  reaches the lineage-shared runtime store and root projection exactly once;
- plugin tests prove aggregated invalid-source diagnostics, source precedence,
  qualified aliases, immutable generation metadata, built-in/alias collision
  rejection, and prior-generation retention;
- skill, hook, and MCP tests prove source/health projection, malformed-source
  diagnostics, deep-cloned snapshots, deterministic ordering, and manager
  revision behavior; and
- focused race validation plus repository Makefile, documentation, manifest,
  and diff gates close the slice.

## P16.6 Bundled Workflow Commands

**State:** completed 2026-07-22.

### Selected move

Move `/commit`, `/review`, `/pr-comments`, `/summary`, `/issue`, `/onboarding`,
and `/commit-push-pr` from compiled Go registration into a versioned bundled
workflow command pack loaded through the same atomic prompt-command snapshot as
project/user/plugin contributions.

### Frozen rules

- precedence and collision policy are explicit and deterministic;
- a malformed bundled or plugin command cannot evict the live snapshot;
- help identifies source and trust class;
- external-write workflows remain prompts subject to normal tool permission,
  not privileged command-side effects;
- disabling the bundled pack leaves core runtime/session/safety commands intact.

### Promotion gate

Golden prompt outcomes remain compatible, reload is atomic, source attribution
is visible, and no compiled runtime switch knows individual workflow names.

### Delivered boundary

- a versioned embedded JSON pack now owns `/commit`, `/review`, `/pr-comments`,
  `/summary`, `/issue`, `/onboarding`, and `/commit-push-pr`; compiled Go core
  registration no longer names or dispatches those workflows;
- the loader validates the bundled pack before configured plugin sources and
  builds one functional digest and candidate. Compiled core commands have
  precedence, bundled names are unqualified, and configured plugin names remain
  qualified;
- `Registry.ReplacePromptCommandGeneration` commits command records, order,
  revision, digest, source snapshots, and diagnostics under one lock. A malformed
  bundle, plugin, or collision leaves the previous complete generation live;
- command metadata and detailed help expose `core`, `bundled`, or `configured`
  source/trust. The pack can be disabled without removing runtime, session, or
  safety commands; and
- every bundled command returns only `ActionPrompt`. Git, filesystem, and
  external-service effects continue through normal Agent tools and permissions.

### Promotion evidence

- golden fixtures preserve every no-argument and argument-dependent prompt;
- focused loader/registry tests cover source precedence, attribution,
  collision rejection, malformed bundled reload retention, disabled-pack core
  survival, immutable generation metadata, and shared TUI registry adoption;
- focused race tests cover generation replacement plus engine/TUI startup and
  reload projection; and
- repository Makefile, documentation, manifest, diff, and independent-review
  gates pass at closeout.

### Adoption decision and Eino boundary

This slice is `combine`: it reuses the project-owned atomic registry generation
from P16.5d and a data-driven embedded pack while preserving configured plugin
qualification. It changes no Eino or Eino-ext dependency, Compose Graph
topology, query-kernel selection, provider request, or durable schema because
prompt-command discovery and attribution are product runtime ownership, not an
execution-Graph capability.

## P16.7a CLI Foundations

**Completed:** 2026-07-22
**Decision:** `combine`

### Selected surface

- `eino-agent exec [prompt]` for explicit non-interactive execution; retain
  `-p` compatibility and stop silently ignoring a positional prompt;
- protocol-specific `serve mcp` and `serve acp` flags rather than inheriting
  unrelated root runtime flags;
- `version` and `completion` with stable text/machine-readable output.

### Resolution

- constructs a fresh Cobra tree for every execution and gives root interactive
  mode, `exec`, `resume`, and `serve acp` only the runtime flags they consume;
- makes `exec` the canonical headless command while retaining root `-p`,
  reading no-prompt or `-` input from stdin, and explicitly appending piped
  stdin when a prompt is also present;
- gives headless stdout one text or schema-versioned JSON renderer and maps
  completion, runtime failure, usage, and cancellation to stable process exits;
- shares one build identity across CLI version, slash `/version`, MCP metadata,
  and release ldflags; completion remains runtime-independent; and
- removes argument/result/raw-error payloads from the default MCP tool log.

The existing `QueryEngine` remains the only headless conversation owner. No
Eino/Eino-ext dependency, Compose Graph topology, provider request, or durable
schema changed.

### Promotion gate

The CLI tree, exit-code taxonomy, output envelope, cancellation, and redaction
contract are stable without introducing a second runtime/service owner.

Closeout evidence covers command-tree and flag-scope negatives, prompt/stdin
resolution, JSON/text ownership, exit `0/1/2/130`, cancellation propagation,
version/completion without model initialization, headless and MCP redaction,
focused race tests, all repository/documentation/manifest gates, and an
independent review.

## P16.7b Sessions CLI Projection

**State:** completed 2026-07-22.

**Decision:** `combine`. Reuse the project-owned `SessionService` and durable
lifecycle contracts while adding a project-native provider-free Cobra host and
administration renderer. Do not modify Eino, Eino-ext, Compose Graph topology,
the query kernel, provider requests, or the transcript schema.

### Selected surface

`eino-agent sessions {list,resume,rename,export,fork}` calls the same services
accepted in P16.4a-b. Slash `/resume` remains canonical for interactive direct
use; Cobra's canonical form is `sessions resume`.

Archive/delete are excluded from this slice. They require an owned retention
store, confirmation semantics, atomic failure behavior, recovery/rollback, and
machine-readable exit contracts before acceptance.

### Resolution

- `NewSessionAdministrationEngine` constructs only the lightweight engine
  ownership needed by `SessionService`; it does not initialize a provider,
  connect MCP, load plugins, install shell hooks, start the settings watcher,
  or compile ProjectGraph merely to list sessions;
- resume and fork activation validate the selected durable kernel and preserve
  the canonical target-session restore checkpoint, but skip MCP, skills,
  hooks, watchers, worktree recovery, Agent replay, and long-lived services.
  Administration close appends neither a second target checkpoint nor a
  synthetic source transcript;
- `sessions list` supports bounded current-workspace query with search, cursor,
  and limit controls; resume, rename, export, and fork resolve the exact durable
  session source and reject ambiguous IDs before mutation;
- CLI fork uses the same commit-then-activate lifecycle and compensates only
  the operation-owned child if activation fails;
- text and JSON share one administration result model. JSON is schema-versioned
  and every command maps complete/runtime/usage/cancelled to `0/1/2/130`;
- root `resume SESSION_ID` remains the interactive TUI path. Provider-free
  inspection uses `sessions resume SESSION_ID`, while `exec --resume` remains
  the path for continuing a model turn; and
- hidden `/history`, `/rename`, and `/export` compatibility shortcuts were
  removed at their planned P16.7b boundary. Canonical `/sessions` remains the
  interactive surface, and archive/delete remain absent.

### Promotion gate

CLI and slash projections produce the same session identity, lineage,
persistence, failure, and output semantics without requiring a TUI. Promotion
evidence covers provider-invalid list success, no synthetic transcript,
exact-source resume/rename/export/fork, compensation ownership, stable JSON and
exit codes including usage failures, cancellation, duplicate-ID rejection,
shortcut removal, archive/delete absence, focused race tests,
repository/documentation gates, and independent review.

## P16.7c Diagnostics And Extension CLI Projection

**State:** completed 2026-07-22.

### Selected surface

- `eino-agent config show` and `eino-agent doctor` use P16.5b's redacted,
  source-derived diagnostic service;
- `eino-agent mcp {list,get}` and
  `eino-agent plugins {list,validate,reload}` use P16.5d's inventory,
  validation, and atomic generation service;
- add/remove/install/uninstall/enable/disable/marketplace remain blocked by
  their persistence, synchronization, containment, and trust gates.

### Promotion gate

CLI and slash inspection reuse the same typed evidence and renderer owners;
snapshot source distinguishes configured/unprobed CLI MCP state from live
runtime-manager health. Plugin validation returns the retained generation
without mutation, and every command has stable exits plus optional
machine-readable output. No unsupported mutation is reported successful
through a read-only projection.

### Resolution

- config and doctor reuse the P16.5b snapshot and text renderers through a
  provider-free inspection host; provider route resolution remains data-only,
  connectivity stays skipped, invalid config fails `config show`, and doctor
  reports invalid files through stable check IDs while the absent active
  Session transcript is explicitly skipped;
- MCP list/get load only configured names and enabled state into the existing
  inventory type. Snapshot source is `configuration`, health is `unprobed`,
  and no command, transport, URL, arguments, environment, headers, tools, or
  runtime connection is created;
- plugin list/validate build the complete bundled/configured candidate and
  apply the same registry collision checks. Validation returns the retained
  live generation under the same read lock and never replaces it;
- plugin reload uses the existing atomic replacement owner only inside the
  short-lived `inspection-host`; structured output makes that process scope
  explicit rather than implying another running agent was reloaded; and
- one versioned administration envelope preserves text/JSON output and
  `0`/`1`/`2`/`130` exits. MCP mutation and plugin install/uninstall/
  enable/disable/marketplace commands remain absent.

Promotion evidence covers provider-invalid and malformed-settings diagnostics,
secret/URL/MCP connection-material redaction, no provider runtime, no MCP
launch/connect, no Graph/transcript/long-service construction, stable text and
JSON source/health, candidate-only validation, atomic process-local reload,
unsupported mutation rejection, focused race tests, repository/documentation
gates, and independent review.

## Dependency-Gated Restoration

The candidates below are **not accepted executable P16 slices**. Each requires
a new atomic plan item after its entry gate closes and its user value is
reconfirmed.

No command becomes visible merely because a reference supports its name.

| Candidate | Required entry gate |
|---|---|
| `/undo` and `/redo` | Append-only reversible session marker, live/durable atomicity, process-restart replay, idempotent repeated invocation and source-session preservation. |
| `/rewrite` | The undo/redo gate closes first; replacement prompt state and subsequent branch semantics are versioned and replayable. |
| `/branch` | A distinct user outcome from `/fork`, named checkpoint identity and switch/no-switch semantics are accepted and durable. |
| `/rewind` | G1 snapshot persistence and G2 production `FileTracker` construction close; restore is restart-safe. |
| session archive/delete | Owned retention store, explicit confirmation, atomic failure behavior, recovery/rollback and stable CLI exit contract. |
| MCP add/remove | G5 model-visible registry synchronization is atomic with manager inventory. |
| plugin install/uninstall/marketplace | G4 resolved-path containment plus accepted trust/signature/source policy. |
| `/share` | Owned backend, authentication, confidentiality model, revocation, expiry, and verified creation. |
| `/tag` | Durable indexed metadata and real session search/filter consumer. |
| `/fast` | Provider/model capability, measurable routing outcome, billing disclosure, and resolved-config projection. |
| `/release-notes` or `update` | Build/release metadata and an owned distribution/update channel. |

Failure to meet a gate means `defer` or `reject`, not a placeholder command.

## Verification Matrix

Every production slice runs focused command tests plus all repository gates for
code changes: `make fmt`, `make lint`, `make test`, and `make build`. Each slice
also runs `make docs-check`, manifest validation, and `git diff --check`.

Required focused evidence grows by slice:

| Slice | Minimum focused evidence |
|---|---|
| P16.H0 | isolated credential trees remain unchanged; no delete call reachable |
| P16.0 | plain clear/compact/fork/undo and quoted permission regression tests |
| P16.1 | exact visible/hidden/alias snapshot and compatibility errors |
| P16.2 | all-command entrypoint/availability matrix, parser fuzz/edge cases, collision tests |
| P16.3 | one mutation and one typed result per supported entrypoint; headless/ACP projection |
| P16.4a-b | process-restart replay, compact boundary, atomic persistence failure, idempotence and fork lineage |
| P16.5a | provider/model capability and one permission/plan mutation across entrypoints |
| P16.5b | source/freshness fixtures, usage aggregation and secret-redaction tests |
| P16.5c | canonical path, workspace/sandbox, terminal capability and TUI-only discovery tests |
| P16.5d | runtime snapshot, source diagnostics, collision and atomic generation tests |
| P16.6 | precedence, attribution, collision, malformed reload and golden prompt tests |
| P16.7a | CLI tree, exit-code, JSON/text output, cancellation, redaction and flag-scope tests |
| P16.7b | session service parity, restart lineage, no-TUI and archive/delete absence tests |
| P16.7c | diagnostic/extension generation parity and read-only mutation rejection tests |

## Rollback Strategy

- P16.H0 never rolls back to deletion.
- P16.0-P16.3 are code-only command-path changes with no durable schema; rollback
  restores the last single owner, not a live dual path.
- P16.4 durable changes use versioned records and retain readers for the prior
  transcript format until process-restart compatibility is proved.
- P16.5a-d aliases remain hidden and time-bounded for one documented
  compatibility window; rollback re-enables an alias only when semantics are
  equivalent.
- Deferred recovery commands stay unavailable during rollback; no rollback may
  restore the current double-truncate or unwired file-restore behavior.
- P16.6 keeps the previous atomic prompt-command snapshot live on load failure.
- P16.7a-c add no separate data store; CLI rollback leaves session/config state
  readable by the existing services.
