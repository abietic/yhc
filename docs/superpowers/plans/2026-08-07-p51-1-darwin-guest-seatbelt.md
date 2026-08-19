# Darwin Guest Seatbelt Adapter Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-08
**Queue state:** completed and removed; P51.2 Core later completed
**Created:** 2026-08-07
**Source snapshot:** `origin/master` at
`6ae2573aa3854a514fe550248b316921fa9a2984`
**Adoption:** `project-native`

> **Ownership:** test-first delivery plan for P51.1, the first containment slice
> accepted by the [Darwin Sandbox And Auto Permission Design](../specs/2026-08-07-darwin-sandbox-auto-permission-design.md)

**Goal:** Default model-issued Bash on Darwin amd64/arm64 to a real Seatbelt
`workspace-write` Guest binding while preserving persistent-shell behavior,
background descendants, cancellation, root identity, bounded wall/output
owners, ambient hooks/MCP, and explicit user-owned ambient rollback.

**Architecture:** Composition roots resolve three immutable process-class
bindings: Guest, ShellHooks, and StdioMCP. Only Guest selects the Darwin
Seatbelt adapter. `ShellManager` atomically pins that binding before process
spawn, launches the persistent shell through `/usr/bin/sandbox-exec`, and
revalidates root identity before every command. The aggregate Guest state stays
`degraded` because environment credentials and hard memory/FD/process limits
remain ambient.

**Tech Stack:** Go 1.26.5, Darwin Seatbelt, build-tagged platform adapters,
process groups, persistent Bash, Cobra/config authority, ACP/child composition,
real Darwin subprocess tests, race detector, and Makefile gates.

## Global Constraints

- Execute only after P50.1-P50.3 are complete and root
  `docs/migration/queue.yaml` admits P51.1 as its sole `Ready` slice.
- Update the accepted P42 contract to P42.1 semantics in a contract commit
  before changing runtime behavior. Keep G28 open and aggregate state
  `degraded`.
- P51.1 changes containment only. It must not reduce Auto Permission prompts;
  that belongs exclusively to P51.2.
- Trust only `/usr/bin/sandbox-exec`; never use PATH lookup, shell interpolation,
  or command text in the generated profile.
- Pass `os.Environ()` to Guest byte-for-byte. Do not filter, sort, deduplicate,
  or redact the environment passed to the child.
- Environment values, command text, generated profiles, and absolute roots must
  never enter diagnostics, permission descriptors, transcripts, or audit.
- Shell hooks and configured stdio MCP remain `danger-full-access` plus
  `disabled/ambient-host`. HTTP hooks and standalone MCP are out of scope.
- An unavailable/failed adapter keeps the application usable but makes every
  Guest Bash spawn fail closed. Never retry ambient.
- Preserve persistent cwd/environment, background execution, timeout,
  process-group cleanup, cancellation, recovery, and child policy lineage.
- Real Darwin tests are required for enforcement claims; cross-builds prove
  compilation only.

---

## Task 1: Admit P42.1 and define explicit process bindings

**Files:**

- Modify: `docs/migration/plans/p42-host-execution-containment.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/README.md`
- Modify: `engine/containment/policy.go`
- Modify: `engine/containment/policy_test.go`
- Create: `engine/containment/binding.go`
- Create: `engine/containment/binding_test.go`

- [x] **Step 1: Record the accepted P42.1 divergence**

Before source changes, add P51.1 as the sole Ready slice only after its
predecessors are terminal. State that `degraded` is not full containment and
that later Auto admission must check named axes. Keep memory, file descriptors,
process count, environment credentials, and G28 open.

The independent 2026-08-08 contract intake completed this step without
changing runtime behavior. The remaining tasks were delivered by P51.1.

- [x] **Step 2: Add immutable binding and adapter contracts**

Implement these conceptual shapes in `engine/containment`; fields remain
unexported and constructors/accessors preserve immutability:

```go
type Bindings struct {
    guest      *Binding
    shellHooks *Binding
    stdioMCP   *Binding
}

type Binding struct {
    policy        *Snapshot
    adapter       SpawnAdapter
    adapterProof  AdapterProof
    availability BindingAvailability
    reasonCode    ReasonCode
    digest        string
}

type BindingAvailability string
type ReasonCode string

const (
    BindingAvailable   BindingAvailability = "available"
    BindingUnavailable BindingAvailability = "unavailable"
)

type SpawnAdapter interface {
    Probe(context.Context, *Snapshot) ProbeResult
    Prepare(context.Context, SpawnRequest) (SpawnSpec, error)
}

type ProbeResult struct {
    Proof      AdapterProof
    Diagnostic Diagnostic
}

type SpawnRequest struct {
    Binding    *Binding
    Executable string
    Args       []string
    Dir        string
    Env        []string
}

type SpawnSpec struct {
    Path          string
    Args          []string
    Dir           string
    Env           []string
    BindingDigest string
}
```

Construct successful bindings through `NewBinding`; validate non-nil
policy/adapter, matching adapter-proof policy digest and capability generation,
adapter-axis ownership, and immutable copies. Construct failed probes only
through `NewUnavailableBinding`, using a separate `StateUnavailable` snapshot
derived from the same canonical values. It retains adapter family and bounded
reason code but has no proof and can never prepare a process; the failed
candidate snapshot is never exposed. The separate binding digest covers process
class, policy digest, adapter generation, proof axes, availability, and reason
code; proof is never an input to the policy digest.
`NewBindings` requires all three process classes and truthful policies, then
returns only accessors or detached diagnostics; profiles need not match.
`SpawnSpec` carries no independently supplied proof. `ShellManager` compares
its binding digest to `SpawnSpec.BindingDigest` immediately before constructing
and starting `exec.Cmd`; mismatch returns `sandbox_binding_expired` with zero
start attempts.

- [x] **Step 3: Add source-owned granular axes and credential mode**

Add the axis constants, including distinct `AxisDescendantConfinement` and
`AxisDescendantCleanup`, plus `AdapterProof` and `ExecutionProof` exactly as
accepted in the design. Adapter axes are limited to filesystem read/write,
network denial, pre-spawn root identity, and descendant confinement. Runtime
axes are limited to per-command root identity, descendant cleanup, wall time,
and bounded retained output. Combined root identity requires both source masks.
Add:

```go
type CredentialMode string

const CredentialAmbientEnvironment CredentialMode = "ambient-environment"
```

Add credential mode and an immutable Darwin root identity (canonical path plus
device/inode) to `CredentialPolicy`/`Spec`, the canonical snapshot digest, and
redacted diagnostics. Diagnostics expose only opaque identity or counts, never
the path. Add `AdapterDarwinSeatbelt`. Set `PolicyVersion` to `p42.1` for newly
constructed snapshots and retain
`LegacyDisabledPolicyVersion = "p42.0"`. Validation may accept `p42.0` only
when the snapshot is `danger-full-access`, `disabled`, `ambient-host`, and has
no adapter proof. `DisabledCompatibilitySnapshot` now creates a truthful p42.1
ambient snapshot; a caller-supplied p42.0 snapshot is accepted only through the
one-release compatibility mapping into three ambient bindings. Neither can
satisfy P51.1 or P51.2 admission.

- [x] **Step 4: Pin immutability, lineage, and degraded truth**

Tests require copied slices, stable digest, no environment values, exact child
derivation, policy-to-binding one-way digest identity, adapter/runtime axis
allowlists, joint root identity, and rejection of any proof that claims
memory/FD/process-count/credential enforcement. Prove caller mutation cannot
replace any process-class binding. `Diagnostic.State` remains `StateDegraded`
for a successful Guest binding. An unavailable Guest binding validates for
engine construction, carries `StateUnavailable`, has no proof, and rejects
prepare before spawn.

- [x] **Step 5: Run focused type tests**

```bash
go test ./engine/containment/ -run '^(TestP511|TestSnapshot|TestBinding)' -count=1
```

## Task 2: Enforce user-only sandbox configuration authority

**Files:**

- Modify: `engine/config/config.go`
- Create: `engine/config/sandbox.go`
- Create: `engine/config/sandbox_test.go`
- Modify: `cmd/eino-agent/cmd/root.go`
- Modify: `cmd/eino-agent/cmd/root_test.go`

- [x] **Step 1: Add the strict user-owned config shape**

Add:

```go
type SandboxConfig struct {
    GuestProfile  string   `json:"guest_profile,omitempty"`
    ExtraReadRoots []string `json:"extra_read_roots,omitempty"`
}
```

Decode `sandbox` strictly in user config. For project and project-local files,
remove the entire raw `sandbox` field before typed decode and emit one bounded
`forbidden_project_sandbox_keys` diagnostic containing key names only. Do not
partially consume an attacker-controlled subset.

- [x] **Step 2: Validate roots without silently resolving authority**

For every extra read root require: non-empty, absolute, existing directory,
`Lstat` not symlink, `EvalSymlinks` equal to the cleaned input, not filesystem
root, and not an ancestor that collapses authority to the user's home or a
system root. Deduplicate by canonical path. Return field/code errors without
echoing the rejected path.

- [x] **Step 3: Add one explicit CLI rollback flag**

Bind:

```text
--sandbox workspace-write|danger-full-access
```

An explicit flag overrides user config. Absence selects `workspace-write`; an
unsupported platform produces an unavailable Guest binding rather than a
silent ambient fallback.
Project config, permission modes, `--yolo`, hooks, ACP requests, and tool input
are never resolver inputs.

- [x] **Step 4: Test precedence and hostile project input**

Cover default, user workspace-write, user danger-full-access, CLI override,
project/local attempted disable, project extra roots, unknown fields, missing
roots, symlink roots, `/`, and no diagnostic value leakage.

- [x] **Step 5: Run focused config tests**

```bash
go test ./engine/config/ ./cmd/eino-agent/cmd/ -run 'Sandbox|P511' -count=1
```

## Task 3: Implement the Darwin Seatbelt adapter and real capability probe

**Files:**

- Create: `engine/containment/seatbelt_darwin.go`
- Create: `engine/containment/seatbelt_other.go`
- Create: `engine/containment/seatbelt_profile.go`
- Create: `engine/containment/seatbelt_profile_test.go`
- Create: `engine/containment/seatbelt_darwin_test.go`

- [x] **Step 1: Build canonical root sets**

Create a resolver that canonicalizes the workspace and includes only existing
system read roots from `/System`, `/usr`, `/bin`, `/sbin`, `/Library`,
`/opt/homebrew`, and `/usr/local`, plus the root containing the resolved `go`
executable,
`$HOME/go/pkg/mod`, `$HOME/Library/Caches/go-build`, `os.TempDir()`, and
validated user extra roots. Writable roots are workspace, Go build cache, and
`os.TempDir()`.

Add explicit deny subpaths for `$HOME/.claude/settings.json`,
`$HOME/.claude/skills`, `$HOME/.claude/agents`, `$HOME/.codex/skills`,
`$HOME/.codex/agents`, workspace `.claude/settings.json`,
`.claude/settings.local.json`, `.claude/skills`, `.claude/agents`,
`.agents/skills`, `.codex/agents`, `.git/config`, and `.git/hooks`. A deny rule
must win over a broader workspace-write allow. Missing paths remain denied by
their canonical lexical location; they are not omitted merely because they do
not exist during profile compilation.

- [x] **Step 2: Generate a deny-first profile safely**

Use one Seatbelt string-literal escaping function for roots. Sort and dedupe
canonical roots before rendering. The profile begins with `(version 1)` and
`(deny default)`, allows only required process/sysctl/Mach/file operations,
applies read/write subpaths, reapplies control-plane denies, and denies network
and unapproved sockets. Never insert argv, command text, environment values, or
uncanonicalized input.

- [x] **Step 3: Prepare only the fixed executable**

On Darwin, `Prepare` verifies `/usr/bin/sandbox-exec` by absolute `Lstat`,
revalidates root identities, compiles the profile, and returns:

```go
SpawnSpec{
    Path: "/usr/bin/sandbox-exec",
    Args: append([]string{"sandbox-exec", "-p", profile, request.Executable}, request.Args...),
    Dir: request.Dir,
    Env: append([]string(nil), request.Env...),
    BindingDigest: request.Binding.Digest(),
}
```

On non-Darwin, return `sandbox_unsupported_platform`. Missing executable,
invalid profile, failed probe, and changed roots use the accepted stable codes.

- [x] **Step 4: Probe real behavior, not profile text**

The Darwin probe creates two private temporary subdirectories and compiles a
probe profile that admits only one. Through the real fixed executable prove
allowed write, denied sibling write, denied sensitive read, denied local TCP
and unapproved Unix-socket connection, and denied descendant escape. It returns
only adapter-owned axes. The probe uses the same compiler and launch transform
as production but never broadens the production snapshot. A parse-only success
earns no axis, and the adapter cannot claim wall time, retained output, or
process-group cleanup.

- [x] **Step 5: Run real Darwin adapter tests**

```bash
go test ./engine/containment/ -run '^(TestP511Darwin|TestSeatbelt)' -count=1
```

Require real subprocess behavior on Darwin; build-tagged non-Darwin tests only
require the stable unsupported result.

## Task 4: Launch and retain the Guest binding in ShellManager

**Files:**

- Modify: `tools/bash_shell.go`
- Modify: `tools/bash_shell_test.go`
- Modify: `tools/bash_unix.go`
- Modify: `tools/bash_windows.go`
- Modify: `tools/bash.go`
- Create: `tools/bash_seatbelt_darwin_test.go`

- [x] **Step 1: Pin a binding atomically before spawn**

Replace the manager's policy field with `*containment.Binding`. Add
`NewShellManagerWithExecutionBinding`, `BindExecutionBinding`, and a redacted
binding diagnostic accessor. Binding validates its adapter proof; ShellManager
then creates the runtime-owned half of `ExecutionProof` and binds it to the
exact binding digest. Keep legacy `BindExecutionPolicy` only as a disabled
ambient compatibility adapter for embedded callers; it must not synthesize a
Darwin enforced binding or runtime proof axes.

- [x] **Step 2: Launch the persistent shell through `Prepare`**

Build `SpawnRequest` with executable `bash`, args
`--noediting --noprofile --norc`, the effective cwd, and an exact copy of
`os.Environ()`. Construct `exec.Cmd` from `SpawnSpec`, then apply the existing
process-group owner before `Start`. Immediately before construction/start,
require the spec binding digest to equal the manager's pinned binding digest.
If prepare fails or any digest/generation/availability identity differs, no
`exec.Cmd.Start` call is allowed. Add a fake adapter returning a mismatched
binding digest and assert zero starts.

- [x] **Step 3: Revalidate roots before every command**

Before writing a sentinel-wrapped command to a persistent shell, revalidate the
binding's captured workspace device/inode identity. On removal, replacement,
or symlink substitution, retire the entire shell process group and return
`sandbox_root_changed`. Adapter prepare performs the matching pre-spawn check;
only both owners together contribute combined `AxisRootIdentity`. Shell
recovery must reuse the original binding.

- [x] **Step 4: Make output and wall-time axes real owners**

Keep the existing context timeout/process-group retirement. Replace the
unbounded `strings.Builder` accumulation with a bounded byte collector derived
from `policy.Spec().Resources.OutputBytes`; continue draining after the cap and
mark truncation once. Add a large-output test that proves bounded retained bytes
and successful shell recovery. ShellManager contributes wall-time and output
axes only after these owners are configured and pinned to the binding digest.

- [x] **Step 5: Prove background and descendant containment**

Use Bash, background Bash, command substitution, and nested child processes to
attempt outside write, network, and unapproved socket access. Require failure
inside the same Seatbelt policy, and require Kill/timeout/engine close to remove
the complete process group. The adapter attests descendant confinement;
ShellManager separately attests descendant cleanup.

- [x] **Step 6: Run focused shell and race tests**

```bash
go test ./tools/ -run '^(TestP511|TestShellManager)' -count=1
go test -race ./tools/ -run '^(TestP511|TestShellManager)' -count=20
```

## Task 5: Resolve the binding matrix at every supported composition root

**Files:**

- Modify: `engine/execution_policy.go`
- Modify: `engine/execution_policy_test.go`
- Modify: `engine/engine.go`
- Modify: `engine/subagent.go`
- Modify: `cmd/eino-agent/cmd/root.go`
- Modify: `cmd/eino-agent/cmd/headless.go`
- Modify: `cmd/eino-agent/cmd/headless_goal.go`
- Modify: `cmd/eino-agent/cmd/serve_acp.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/agent_test.go`

- [x] **Step 1: Resolve Guest, Hook, and MCP independently**

Add `ExecutionBindings *containment.Bindings` to `QueryEngineConfig`. Production
roots first resolve the immutable policy/root identity, then probe the adapter,
construct an available or unavailable Guest binding, and bind ShellManager so
it can finalize the exact runtime proof before QueryEngine construction.
`ExecutionPolicy` remains one-release embedded compatibility input and maps
only to three disabled ambient bindings with no proof axes.
`ExecutionPolicySnapshot()` returns Guest policy for compatibility; add
`ExecutionBindings()` and `GuestExecutionProof()` returning detached
identities.

- [x] **Step 2: Bind owners to the correct process class**

Bind `ShellManager` to Guest, `hooks.Executor` to ShellHooks, and
`MCPToolManager` to StdioMCP. Remove the current behavior that sends the same
Guest snapshot to all three. Existing hook/MCP process launch remains ambient.

- [x] **Step 3: Derive Child Agent Guest only from parent Guest**

Child derivation preserves or narrows roots and records the parent binding
digest plus adapter capability generation. Because the child policy digest and
root identity change, it must not copy the parent's adapter proof. The selected
adapter runs its real probe against the validated child snapshot and issues a
proof bound to the child policy, then the child ShellManager independently adds
its runtime axes. A non-subset, changed root, or missing/mismatched parent is a
derivation safety violation and fails before child engine construction. A
missing fixed executable, unsupported platform, or host probe failure instead
creates a child `StateUnavailable` Guest binding: child engine construction
succeeds, but its Bash path rejects before spawn. Hook/MCP ambient authority
never becomes Guest authority.

- [x] **Step 4: Prove the entrypoint matrix and unavailable behavior**

Cover TUI, Plain, headless, headless Goal, ACP, and Child Agent. On Darwin all
Guest bindings default workspace-write/degraded; hooks/MCP remain
danger-full-access/disabled. Simulate missing/failed Seatbelt and require engine
startup with an explicit unavailable Guest binding plus Bash pre-spawn denial.
Require the same unavailable behavior for Child Agent; separately prove that a
non-subset or mismatched parent prevents child engine construction.
An available binding whose ShellManager cannot establish its owned proof axes
fails engine construction as an internal contract violation; it never becomes
unavailable or ambient implicitly. Explicit danger-full-access emits one bounded
warning and never says sandboxed.

- [x] **Step 5: Run engine, adapter, and cross-platform build checks**

```bash
go test ./engine/ ./cmd/eino-agent/cmd/ ./server/acp/ -run 'P511|ExecutionPolicy' -count=1
go test -race ./engine/ ./tools/ -run 'P511|ExecutionPolicy|ShellManager' -count=10
make build
```

## Task 6: Run real-product containment acceptance and close P51.1

**Files:**

- Modify: `docs/architecture/platform/runtime-services.md`
- Modify: `docs/architecture/capabilities/permissions.md`
- Modify: `docs/architecture/capabilities/mcp.md`
- Modify: `docs/guides/permissions-and-safety.md`
- Create: `docs/migration/verification/p51-1-darwin-guest-seatbelt.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p51-1-darwin-guest-seatbelt.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p42-host-execution-containment.md`

- [x] **Step 1: Exercise a real Darwin workspace**

From a disposable real repository prove workspace read/write/create/rename,
`go test`, `make`, Git read operations, environment inheritance, background
work, timeout, cancellation, and recovery. Prove outside/control-plane write,
undeclared credential read, TCP/UDP, unapproved Unix socket, symlink, `..`,
command substitution, and descendant escape fail. Record physical host and
platform evidence separately from unit tests.

- [x] **Step 2: Synchronize truthful current docs**

Describe Darwin Guest only, explicit ambient rollback, ambient environment,
missing hard resource limits, and ambient hooks/MCP. Keep G28 open and do not
claim Auto prompt reduction before P51.2.

- [x] **Step 3: Run final repository gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 4: Commit and open one atomic containment PR**

Use scoped `git add` over the files above and commit:

```bash
git commit -m "feat(containment): enforce Darwin Guest Seatbelt"
```

The PR must state `project-native`, exact process-class matrix, environment and
resource risks, no ambient retry, rollback, real Darwin evidence, full local
gates, remote CI, and unchanged Auto Permission outcomes.
