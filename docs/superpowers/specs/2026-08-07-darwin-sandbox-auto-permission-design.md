# Darwin Sandbox And Auto Permission Design

**Status:** historical
**Delivery:** P51.1 and P51.2 Core complete
**Accepted:** 2026-08-07
**Last verified:** 2026-08-19
**Source snapshot:** `origin/master` at
`de74294b29f40d19bfd0e37f09889bd6f8037d90`
**Adoption:** `project-native`

> **Ownership:** reviewed design for a Darwin Seatbelt guest-process adapter
> and its later Auto Permission integration; the accepted P42 execution
> contract remains
> [`p42-host-execution-containment.md`](../../migration/plans/p42-host-execution-containment.md),
> comparative evidence remains
> [`host-execution-containment-audit.md`](../../migration/reference/runtime/host-execution-containment-audit.md),
> and current permission behavior remains
> [`permissions.md`](../../architecture/capabilities/permissions.md)

## Outcome

P51.1 defaults model-issued Bash on Darwin amd64 and arm64 to a real Seatbelt
`workspace-write` envelope. It deliberately leaves all permission outcomes
unchanged. P51.2 Core now stops ordinary Bash prompts through its automatic
Auto path only when QueryEngine proves that the exact action will use the exact
Guest binding whose required filesystem, network, root, and descendant axes
are revalidated before dispatch. Exact local/user rules remain a separate
explicit user authority path and never receive proof-bound admission.

The first platform slice contains persistent Bash, its background work, child
Agent Bash, and all descendants. Shell hooks and configured stdio MCP servers
remain explicitly `danger-full-access` plus `disabled`; they cannot contribute
sandbox evidence to Auto Permission.

The envelope restricts filesystem reads and writes, denies network access, and
preserves descendant cleanup. It does not add hard memory, file-descriptor, or
process-count limits, so the aggregate snapshot remains truthfully `degraded`
and resource exhaustion remains possible without an Auto prompt. For
compatibility, Guest Bash also inherits the agent's environment unchanged. The
design therefore claims neither complete P42 enforcement nor environment
credential isolation: API keys and tokens already present in the environment
can be read and printed by Bash.

This document preserves the original two-stage design. P51.1 and P51.2 Core
are implemented, while G28 remains open because credentials, hooks/MCP, and hard
resource limits are still ambient. The master-native
[`P51.2 Core contract`](../../migration/plans/p51-2-auto-containment-admission.md)
supersedes this design wherever the critical-command rule or delivered
entrypoint boundary differs. AppServer, Desktop, and Web UI projection is not
part of the Core delivery.

### Accepted P42 divergence

The current P42 contract treats a required `degraded` profile as insufficient
for execution and permission promotion. This design deliberately proposes a
narrower Auto contract: QueryEngine checks a named immutable axis set instead
of treating aggregate state as a boolean safety result. The user accepted that
memory, file-descriptor, and process-count exhaustion can run without an Auto
prompt when deterministic risk detection does not catch it.

The independent intake updated the accepted P42 contract before runtime work.
P51.1 must keep G28 open and the aggregate state `degraded`; it may not
reinterpret this design as complete host containment.

## User-Visible Contract

- Darwin selects Guest `workspace-write` by default.
- The application remains usable when Seatbelt is unavailable, but every Bash
  attempt fails before spawn with a typed sandbox-unavailable result.
- No permission prompt, Auto decision, `--yolo`, model output, hook, project
  configuration, or ACP client field can disable or broaden the Guest envelope.
- The user may explicitly select `danger-full-access` through user-owned
  configuration or CLI. Ambient execution emits a visible warning and is never
  described as sandboxed.
- Auto skips ordinary Bash prompts only when the immutable execution proof
  contains every axis required by the narrower Auto admission contract.
- The narrow literal critical `rm`/`rmdir` subset always requires one fresh
  live `AllowOnce`; exact rules and other persistent authority do not cover it.
- Filesystem or network denial never triggers an ambient retry or one-shot
  sandbox escalation in the first version.

## Architecture

### Process-class bindings

Replace the ambiguous single process policy at composition roots with explicit
bindings:

```go
type Bindings struct {
    guest      *Binding
    shellHooks *Binding
    stdioMCP   *Binding
}

type Binding struct {
    policy       *Snapshot
    adapter      SpawnAdapter
    adapterProof AdapterProof
    availability BindingAvailability
    reasonCode   ReasonCode
    digest       string
}

type BindingAvailability string
type ReasonCode string

const (
    BindingAvailable   BindingAvailability = "available"
    BindingUnavailable BindingAvailability = "unavailable"
)
```

Constructors copy and validate every input; accessors return immutable values
or detached projections. Callers cannot replace a process-class field, adapter,
proof, availability, or reason after construction. An unavailable Guest
binding is valid for engine construction but cannot prepare or start a process.

The Darwin composition root resolves:

| Process class | Profile and state | Adapter |
|---|---|---|
| Model Bash, background work, descendants | `workspace-write`, `degraded`; Seatbelt filesystem/network/confinement plus ShellManager root/cleanup/wall/output axes, hard resource axes ambient | `darwin-seatbelt` |
| Shell hooks | `danger-full-access`, `disabled` | `ambient-host` |
| Configured stdio MCP | `danger-full-access`, `disabled` | `ambient-host` |

TUI, Plain, ordinary headless, headless Goal, and ACP resolve the same Guest
default before QueryEngine construction. A Child Agent derives an equal or
narrower Guest binding from its parent. It cannot inherit ambient hook or MCP
authority as Guest authority.

Child derivation never copies a proof across policy digests. It records the
parent binding digest, proves that root sets are equal or narrower, captures the
child root identity, and runs the selected adapter's real probe against the
child snapshot under the same capability generation. The child ShellManager
then contributes its own runtime axes before child QueryEngine construction.
Non-subset roots, changed identity, or a missing/mismatched parent are
derivation violations and prevent child engine construction. Platform,
fixed-executable, or probe unavailability instead creates a child unavailable
Guest binding: child engine construction succeeds and only its Bash path fails
before spawn.

### Pre-spawn adapter

`engine/containment` owns a small deep interface:

```go
type SpawnAdapter interface {
    Probe(context.Context, *Snapshot) ProbeResult
    Prepare(context.Context, SpawnRequest) (SpawnSpec, error)
}

type ProbeResult struct {
    Proof      AdapterProof
    Diagnostic Diagnostic
}
```

`SpawnSpec` carries the launch transform and exact binding digest, never a
second proof. ShellManager compares that digest with its pinned binding
immediately before `exec.Cmd.Start`; any mismatch rejects with zero start
attempts.

The Darwin implementation:

- trusts only `/usr/bin/sandbox-exec`, never a PATH lookup;
- validates and compiles one canonical Seatbelt profile from the immutable
  snapshot;
- transforms the original persistent-Bash argv without interpolating command
  text into the profile;
- returns a spawn specification only after every required probe passes; and
- returns typed failure before any process exists.

`ShellManager` atomically binds the Guest `Binding`. After validating its own
root-recheck, process-group, timeout, and bounded-output owners, it creates the
runtime half of an immutable execution proof bound to the exact binding digest.
Persistent Bash starts through the adapter once; background commands and
descendants naturally retain that Seatbelt policy. Existing cancellation and
shell-recovery owners remain in place.

Per-command wrapping is rejected because it would break persistent cwd,
environment, background, cancellation, and recovery behavior. A container or
external sandbox daemon is deferred because its dependency, synchronization,
and lifecycle cost is not required for the first Darwin outcome.

## Configuration Authority

The user-owned configuration shape is:

```json
{
  "sandbox": {
    "guest_profile": "workspace-write",
    "extra_read_roots": []
  }
}
```

Rules:

- Darwin uses `workspace-write` when `guest_profile` is absent.
- User configuration or an explicit CLI flag may select
  `danger-full-access`.
- Project and project-local configuration cannot disable the sandbox, add
  readable roots, or broaden another axis. A project may only request a
  supported narrowing in a later design.
- Extra roots are canonicalized user-level inputs. Empty, relative, symlinked,
  missing, filesystem-root, or otherwise overbroad roots fail configuration
  validation.
- Linux and Windows retain truthful `disabled/ambient` behavior. They do not
  claim the Darwin default or simulated enforcement.

Permission mode is not a configuration source for these bindings.

Darwin emits one bounded startup diagnostic that names the enforced axes and
warns that memory, descriptor, and process-count exhaustion remain ambient.
Project configuration cannot suppress that diagnostic.

## Darwin Filesystem And Network Policy

Seatbelt starts from deny and adds only the required rules.

### Readable roots

- canonical workspace;
- existing `/System`, `/usr`, `/bin`, `/sbin`, `/Library`, `/opt/homebrew`, and
  `/usr/local` roots;
- the toolchain root containing the resolved `go` executable;
- `<os.UserHomeDir()>/go/pkg/mod` as the default read-only Go module cache;
- `<os.UserHomeDir()>/Library/Caches/go-build` and canonical `os.TempDir()`;
  and
- user-configured extra read roots.

Custom language caches and toolchains require explicit user configuration.
Failure to read an undeclared dependency path remains a sandboxed command
failure; it does not enlarge the snapshot.

### Writable roots

- canonical workspace;
- `<os.UserHomeDir()>/Library/Caches/go-build`; and
- canonical `os.TempDir()`.

The first slice has no arbitrary extra-write-root setting.

Passing the environment unchanged requires admitting the existing TMPDIR
location rather than rewriting it to an agent-private directory. This permits
Guest Bash to read and write other files already reachable under that temporary
root; diagnostics must report this compatibility boundary without exposing the
path.

### Control-plane write denial

Guest Bash cannot write paths that change future Agent or host authority,
including:

- user and project permission/settings files;
- project skill and Agent-definition directories; and
- Git configuration and hooks that could affect later host-owned commands.

Engine-owned settings, permission, and skill APIs remain separate authority
paths and are not implemented by Guest Bash.

### Credential and environment consequence

Seatbelt denies reads of undeclared credential directories and denies network
and unapproved Unix sockets. It does not sanitize the process environment.
Guest Bash receives the current `os.Environ()` byte-for-byte.

The snapshot records `CredentialMode=ambient-environment` and only a digest of
environment names, never values. The aggregate state remains `StateDegraded`
because environment credentials and hard resource bounds are ambient. Auto
Permission explicitly accepts both compatibility boundaries and relies on
granular proof rather than interpreting `degraded` as fully enforced.

Future credential restriction requires a new capability generation and an
explicit compatibility decision; it cannot silently change this profile.

### Network

Guest Bash receives no AF_INET/AF_INET6 outbound or bind authority and no
unapproved Unix socket authority. A command such as `curl` may start but its
network operation fails inside Seatbelt. HTTP hooks and stdio MCP remain
outside this Guest policy and gain no authority from it.

## Granular Proof, Snapshot, And Root Identity

P42.1 adds immutable adapter and runtime proof components. The policy digest is
constructed first and excludes every proof; the binding digest then covers the
policy digest, process class, adapter generation, adapter axes, and availability;
the final execution proof references that binding digest. This one-way chain
avoids a policy/proof digest cycle:

```go
type EnforcementAxes uint64

const (
    AxisFilesystemRead EnforcementAxes = 1 << iota
    AxisFilesystemWrite
    AxisNetworkDenied
    AxisRootIdentity
    AxisDescendantConfinement
    AxisDescendantCleanup
    AxisWallTime
    AxisOutput
    AxisMemory
    AxisFileDescriptors
    AxisProcessCount
)

type AdapterProof struct {
    PolicyDigest         string
    CapabilityGeneration string
    Enforced             EnforcementAxes
}

type ExecutionProof struct {
    BindingDigest        string
    PolicyDigest         string
    CapabilityGeneration string
    AdapterAxes          EnforcementAxes
    RuntimeAxes          EnforcementAxes
    Enforced             EnforcementAxes
}
```

The selected adapter constructs only `AdapterProof` after real probes. It may
attest filesystem read/write, network denial, pre-spawn root identity, and
descendant Seatbelt inheritance. `ShellManager` contributes only per-command
root identity, descendant cleanup, wall time, and bounded retained output. The
containment finalizer rejects an axis outside either owner's allowlist and sets
combined root identity only when both owners attest their half. Neither may
claim memory, file descriptors, process count, or environment credential
isolation. Tool input, model output, hooks, configuration, permission state,
ACP, and entrypoint adapters cannot create or add axes.

The construction order is:

1. resolve canonical policy values/root identity and construct the candidate
   `degraded` snapshot, including canonical workspace path and Darwin
   device/inode identity;
2. run the Darwin probe against that candidate snapshot;
3. create an available binding from the adapter proof, or construct a separate
   `unavailable` snapshot from the same canonical values and an explicit
   unavailable binding with no proof and one bounded reason code; the failed
   candidate is never exposed or used for spawn;
4. pin the exact binding in ShellManager and finalize its runtime proof; and
5. construct QueryEngine only after the manager and binding identities agree.

An unavailable binding keeps the engine usable but every Guest Bash path fails
before `exec.Cmd.Start`; it never falls back or retries ambient. An overall
`degraded` state proves no axis by itself, so consumers must check the combined
execution proof explicitly.

The real adapter probe proves, on the current host:

- workspace write succeeds;
- outside-root write fails;
- undeclared sensitive read fails;
- network connection fails; and
- a descendant cannot escape the policy.

String generation, mocks, or a successful `sandbox-exec -p` parse do not prove
an axis.

The snapshot captures canonical workspace path and host-local device/inode
identity before the probe and includes them in its policy digest. Adapter
prepare revalidates that exact object before spawn; ShellManager revalidates it
before every submitted command. Only their joint result sets
`AxisRootIdentity`. Removal, replacement, or symlink substitution terminates
the existing shell and returns `sandbox_root_changed`; execution does not
continue against the new object.

`AxisDescendantConfinement` and `AxisDescendantCleanup` are separate. The
adapter proves that descendants inherit Seatbelt; ShellManager proves that the
process group is retired on timeout, cancellation, invalidation, and close.

Configuration reload affects only a newly constructed engine. Shell recovery
uses the original binding and cannot broaden it.

## Auto Permission Admission

### Trusted local facts

The QueryEngine-owned action descriptor gains local fields equivalent to:

```go
ExecutionPolicyDigest string
ExecutionBindingDigest string
ExecutionProfile      containment.Profile
ExecutionState        containment.State
ExecutionAdapter      containment.AdapterFamily
ExecutionNetwork      containment.NetworkMode
CredentialMode        containment.CredentialMode
AdapterAxes           containment.EnforcementAxes
RuntimeAxes           containment.EnforcementAxes
EnforcementAxes       containment.EnforcementAxes
GuestProcess          bool
```

Raw environment values and the generated Seatbelt profile never enter the
descriptor or model reviewer projection.

### Decision order

The delivered ordering preserves tool registration, schema validation, Plan,
and explicit deny first; then it recognizes the narrow critical Bash subset
before explicit ask/allow, exact user authority, grants, Bypass, classifier,
reviewer, or coalescing can authorize execution. An incomplete or unavailable
Guest binding cannot authorize the P51.2 shortcut; the existing Auto fallback
may ask or deny, and any eventual Guest launch remains fail-closed before
spawn.

Only `ModeAuto` plus canonical `Bash` may use the containment shortcut. It
requires all of:

- the action is registered, enabled, selected, schema-valid, and Guest-owned;
- profile is `workspace-write`;
- aggregate state is the expected `degraded` state and is never presented as
  fully enforced;
- adapter is `darwin-seatbelt`;
- network is denied;
- credential mode is the explicitly accepted `ambient-environment`; and
- action, QueryEngine, and ShellManager carry the same policy digest and
  binding digest plus capability generation; and
- adapter and runtime source masks contain only their accepted owned axes; and
- the immutable execution proof contains filesystem read/write, network
  denial, root identity, descendant confinement, descendant cleanup, wall-time,
  and output axes.

Memory, file-descriptor, and process-count axes are deliberately not required.
A fork bomb or resource-exhaustion command can therefore run without an Auto
prompt when the deterministic destructive classifier does not identify it.
This is an accepted first-version risk, not an implementation claim that
Seatbelt contains resource consumption.

Use the delivered narrow literal recognizer as a workspace-protection signal,
not a host security boundary:

- supported critical `rm`/`rmdir` requests require fresh live `AllowOnce`;
- non-critical or unknown syntax may use the proof-bound or existing fallback
  path; and
- exact local/user authority remains independently authoritative only for
  non-critical actions.

Non-Bash tools, BashOutput, KillShell, Agent, MCP, dynamic tools, network tools,
and user-interaction tools retain the current permission flow. Bypass mode may
skip permission but still uses Seatbelt.

### Hook rewrite and final dispatch

PreToolUse hooks remain before the final permission check. Updated input causes
canonical action rebuild, risk reclassification, policy-digest validation, and
complete permission re-evaluation.

The settled action binds canonical tool and input, permission policy snapshot,
Guest execution-policy digest, Guest binding digest, and adapter capability
generation. Immediately before tool execution, dispatch revalidates the same
values against ShellManager. Mismatch returns `sandbox_binding_expired`,
persists no new grant, dispatches nothing, and never retries ambient.

### Reviewer shadow

Reviewer enforcement remains disabled. If the existing shadow is explicitly
enabled, it may asynchronously compare against a `sandbox_containment` legacy
decision only after the independent audit-dispatch repair exists. Reviewer
Allow, Deny, timeout, error, malformed output, or binding drift never changes
the sandbox decision.

## Failures And Diagnostics

Stable reason codes include:

- `sandbox_unsupported_platform`;
- `sandbox_executable_missing`;
- `sandbox_profile_invalid`;
- `sandbox_probe_failed`;
- `sandbox_required_axis_unavailable`;
- `sandbox_root_changed`;
- `sandbox_binding_expired`; and
- `sandbox_operation_denied`.

Diagnostics expose profile, state, adapter, reason code, and opaque root or
policy identity. They do not expose absolute roots, generated profiles,
environment values, command text, hook payloads, credentials, or socket paths.

Reliable EPERM/EACCES evidence may mark a command result as
`sandbox_operation_denied`. When denial cannot be distinguished from an
ordinary command error, retain the command failure and attach the active
sandbox diagnostic; do not fabricate a violation or automatic escalation.

## Verification

### Real Darwin enforcement

Actual Darwin tests prove:

- workspace read, write, create, rename, Go build-cache write, `go test`,
  `make`, and Git read operations succeed;
- environment values are inherited unchanged as the approved compatibility
  behavior;
- outside write, undeclared user and credential read, TCP/UDP, unapproved Unix
  socket, `..`, symlink, command substitution, and background-descendant
  escape fail;
- replaced workspace root invalidates the shell; and
- missing or failed Seatbelt never starts an ambient child.

The same tests assert that memory, descriptor, and process-count axes are
absent and that diagnostics report the partial resource boundary. Tests must
not rename this aggregate state to `enforced`.

Only real Darwin tests may claim Seatbelt enforcement. Pure compiler tests and
cross-builds remain useful but insufficient.

### Permission matrix

- a routine corpus including `go test`, `make test`, `git status`, `git diff`,
  search, formatting, and generated-file writes produces zero Auto permission
  prompts;
- deterministic destructive fixtures including recursive deletion, hard reset,
  forced clean, raw-disk writes, and dangerous permission changes always
  prompt absent exact user authority;
- explicit deny, explicit ask, Plan, hook rewrite, stale binding, and
  unsupported capability never use the shortcut;
- Default, Plan, and AcceptEdits permission outcomes remain unchanged;
- bypass remains sandboxed; and
- TUI, Plain, headless, Goal, ACP, and Child Agent fixtures prove the exact
  binding matrix while hooks and MCP remain ambient/disabled.

### Concurrency and lifecycle

Race and deterministic lifecycle tests cover concurrent shell creation,
binding, command submission, background work, cancellation, shell crash,
recovery, root invalidation, engine shutdown, and child derivation. Timing
sleeps are not concurrency or ownership proof.

## Delivery Decomposition

After the independent permission repairs, implementation planning creates two
containment slices:

| Order | Slice | Deliverable | Rollback boundary |
|---:|---|---|---|
| 1 | P51.1 Darwin Guest adapter `Complete` | Explicit process bindings, user-owned config, real Seatbelt launch and escape tests; no permission behavior change | Restore Guest to truthful ambient/disabled and remove Darwin selection/config as one unit |
| 2 | P51.2 Auto containment admission `Core complete` | Trusted granular proof, narrow critical live AllowOnce, hook rewrite and dispatch revalidation, prompt-reduction fixtures | Remove only the sandbox shortcut while retaining filesystem/network containment |

Each slice starts from then-current `origin/master`, ships through one
short-lived branch and pull request, and updates only current fact owners.
The root migration plan records P51.1 and P51.2 Core complete with no active
queue row. It still owns mutable acceptance plus the one-Ready-slice limit. The
independent public intake, rather than this design, promoted P51.2. AppServer,
Desktop, and Web UI projection remains deferred.

Every code slice closes with:

```bash
make fmt
make lint
make test
make build
go run ./scripts/migration_manifest.go check
git diff --check
```

Darwin escape tests, focused race tests, cross-platform build, remote CI, and
real product acceptance are reported separately.

## Rollback

The user may explicitly select ambient execution only outside permission:

```json
{
  "sandbox": {
    "guest_profile": "danger-full-access"
  }
}
```

Ambient selection emits a visible warning. Project configuration, a permission
prompt, Auto, `--yolo`, tool input, hook output, and ACP fields cannot trigger
this rollback.

## Non-Goals

- Linux or Windows enforcement.
- Sandbox enforcement for shell hooks, stdio MCP, HTTP hooks, LSP, fixed-shape
  application helpers, or standalone MCP.
- One-shot permission-driven sandbox escalation or ambient retry.
- Managed network proxy, network allowlist, or credential sanitization.
- Workspace checkpoint/rollback before every Bash write.
- Hard memory, file-descriptor, or process-count enforcement in the first
  Darwin adapter.
- Reviewer enforcement, a semantic reviewer projection, or P22.3 promotion.
- Treating a shell parser or model classifier as the host containment boundary.
