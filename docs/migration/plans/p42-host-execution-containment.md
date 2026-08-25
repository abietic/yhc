# P42 Host Execution Containment

**Status:** historical
**Completed slices:** P42.0, P51.1, P51.2 Core, P51.3
**Adoption:** `project-native`
**Gap:** G28

> **Ownership:** accepted execution contract for host-process containment.
> Current evidence and reference comparison are in
> [`host-execution-containment-audit.md`](../reference/runtime/host-execution-containment-audit.md).
> Root [`PLAN.md`](../PLAN.md) alone owns execution order.

P42.0 completion evidence is
[`p42-0-execution-policy-snapshot.md`](../history/runtime/p42-0-execution-policy-snapshot.md).
The 2026-08-08 intake accepted the P42.1 axis-level proof contract, and P51.1
implements its first Darwin Guest adapter slice. P51.2 Core subsequently bound
ordinary Auto Bash admission to that exact Guest proof and added a fresh
`AllowOnce` constraint for the narrow critical path. G28 remains open for the
deliberately ambient credential, hook/MCP, and hard-resource axes. AppServer,
Desktop, and Web UI projection remains deferred without an executable queue
row.

P51.3 completed the prompt-approved Linux Guest subset through fixed
bubblewrap, a real filesystem/network/descendant probe, immutable root
identity, existing control-plane overlays, socket/io_uring seccomp, and a
required Ubuntu integration job. It intentionally did not widen P51.2's
Darwin-only automatic Bash admission, so G28 remains open without an accepted
successor.

## P51.3 accepted Linux Guest bubblewrap subset

P51.3 adapts the current upstream Codex/bubblewrap primitive family to the
project-owned binding and ShellManager lifecycle. On Linux amd64/arm64 only,
the fixed `/usr/bin/bwrap` may supply a Guest `workspace-write` binding after a
real probe. The process view starts from an empty root, mounts only declared
read roots, binds the canonical workspace and approved temporary roots for
write, reapplies every existing denied control-plane root read-only, mounts a
fresh `/dev` and `/proc`, and enters new user, PID, IPC, and network namespaces.
All capabilities are dropped, a classic seccomp filter rejects socket creation
and io_uring setup, and `--new-session` plus `--die-with-parent` remain defense
in depth around the existing process-group owner.

The probe must execute the real fixed helper and prove allowed workspace
read/write, outside-root read/write denial, network denial, canonical root
identity, and nested descendant confinement. Missing or mutable helper
identity, unsupported architecture, namespace/mount/seccomp failure, changed
root identity, or any failed behavior produces an unavailable Guest binding.
An attempted Guest launch fails before the requested Bash starts and never
retries ambient.

P51.3 deliberately omits absent-path creation fencing for control-plane names.
Only existing denied roots enter the Linux policy and are proven read-only;
unmounted host paths remain unreadable. Because that authority is not
equivalent to the Darwin proof consumed by P51.2, Default and Auto must not use
the Linux binding for automatic Bash admission. Explicit permission outcomes
may start the contained Linux Guest; permission cannot select or relax the
adapter. Shell hooks, configured stdio MCP, environment credentials, hard
memory/file-descriptor/process-count limits, Windows, application helpers, and
HTTP hooks remain outside this slice, so G28 stays open.

## Problem

An allowed Bash process inherits the agent's ambient host authority. CWD,
timeout, pipes, permission prompts, and process-group cleanup do not prevent
the process or a descendant from reading credentials, writing outside the
workspace, reaching the network, exhausting resources, or invoking unrestricted
syscalls.

The safety boundary must therefore answer a different question from
permission: permission decides whether an action may start; an execution
envelope decides what the started process can affect.

## Accepted owner and compatibility

One composition-root resolver owns immutable `ExecutionPolicySnapshot` values
and the process-class binding matrix. Every guest/configured-extension process
launch receives its named binding before spawn. QueryEngine remains the
permission and dispatch owner; no permission mode, classifier result, allow
rule, exact grant, or bypass can construct or elevate a snapshot or binding.

P42 preserves current commands, tool names, permission outcomes, timeouts, and
process-tree cleanup. Platform mechanisms are project-owned because no single
reference matches the supported Go targets, entrypoints, child lifecycle, and
permission invariants. The compatibility cost is intentional: a required
process binding rejects spawn when any axis named by its accepted slice is
unavailable instead of silently retaining ambient authority.

## Scope and process classes

The program covers arbitrary guest commands, shell hooks, configured stdio MCP
servers, and child Agents reachable from TUI, Plain, ordinary headless,
headless Goal, or ACP. Async work pins the binding captured when it was
dispatched.

Fixed-shape application helpers such as Git, ripgrep, PDF converters,
notifications, clipboard commands, and external editors are inventoried but
are not called sandboxed by P42.0. HTTP hooks are not processes and require a
separate egress policy. The currently unreached `engine/tasks.ShellTask` and
`engine/services.LSPServiceManager` are not entrypoint evidence; production
wiring would first have to bind the applicable policy. Standalone MCP exposes
only task-record lifecycle and Todo tools, so P42.0 guards that no-host-process
surface instead of inventing a composition dependency.

## Profiles and enforcement state

| Profile | Filesystem | Network | Enforcement contract |
|---|---|---|---|
| `read-only` | Declared readable roots; no writes | Denied unless independently granted | The active slice must name and prove every required resource and descendant axis |
| `workspace-write` | Writes only in canonical workspace and approved temporary roots, except immutable host control-plane denied roots | Denied unless independently granted | The active slice must name and prove every required resource and descendant axis |
| `danger-full-access` | Ambient host filesystem | Ambient unless separately restricted | Existing timeout and cleanup remain; never described as sandboxed |

State is independent from profile:

- `enforced` means every axis required by the complete requested profile is
  active for the exact process;
- `degraded` reports partial primitives and proves no axis by itself. It may
  execute only when an accepted slice names an exact required subset and an
  immutable execution proof contains every member of that subset;
- `unavailable` means the requested profile cannot be established before
  spawn; and
- `disabled` is an explicit operator-selected compatibility mode. It is never
  described as sandboxed.

`workspace-write` is the semantic target for normal coding work. Missing a
required named axis or an `unavailable` binding fails before spawn. Aggregate
`degraded` state must never be converted into a boolean safety claim. Selecting
`danger-full-access` requires explicit user-owned, non-permission configuration
and a visible warning in the slice that exposes selection.

## P42.1 accepted granular-proof divergence

P42.1 is the current contract delivered by P51.1 and the prerequisite for a
possible P51.2 intake. It replaces the ambiguous assumption that one aggregate
state describes all process authority with three immutable process-class
bindings:

| Process class | Current P51.1 binding |
|---|---|
| Guest model Bash, background work, descendants, and child-Agent Bash | Darwin `workspace-write`, `degraded`, `darwin-seatbelt` |
| Shell hooks | `danger-full-access`, `disabled`, `ambient-host` |
| Configured stdio MCP | `danger-full-access`, `disabled`, `ambient-host` |

Each binding contains one policy, one pre-spawn adapter, an adapter proof, an
availability state, and one bounded reason code. A successful adapter proof is
bound to the policy digest and adapter capability generation. An unavailable
binding is a valid immutable value with no proof; it keeps the engine usable
while forcing Guest Bash to fail before spawn. Model output, tool input,
project configuration, hooks, permission state, ACP fields, and entrypoint
adapters cannot create availability or add axes.

New snapshots use policy version `p42.1`. A `p42.0` snapshot remains valid only
as the legacy `danger-full-access` plus `disabled/ambient-host` compatibility
identity and can carry no adapter or execution proof. It can never satisfy
P51.1 or the later P51.2 admission gate.

The Darwin Guest binding requires these proven axes for P51.1 execution:

- filesystem read and workspace-scoped write;
- network denial;
- canonical root identity revalidation;
- descendant confinement inheritance;
- descendant cleanup;
- bounded wall time; and
- bounded retained output.

It deliberately does not prove hard memory, file-descriptor, or process-count
limits. It also passes the parent `os.Environ()` byte-for-byte, records
`ambient-environment` as the credential mode, and therefore does not prove
environment credential isolation. The aggregate state remains `degraded`, and
G28 remains open. A later consumer such as P51.2 must compare the exact proof
axes, policy and binding digests, capability generation, process class, network
mode, and accepted credential mode; it cannot admit execution merely because
the state is `degraded`.

Only user-owned configuration or the explicit CLI sandbox option may select
`danger-full-access`. Project and project-local configuration, permission
modes, Auto, `--yolo`, prompts, hooks, tool input, and ACP clients cannot
broaden a binding. On a missing executable, failed probe, invalid profile, or
changed root identity, the application remains usable but Guest Bash fails
before spawn and never retries ambient.

P51.1 changes containment only. Default, Plan, AcceptEdits, Auto, bypass, and
other permission outcomes remain unchanged until the independently queued
P51.2 slice. Hooks and configured stdio MCP intentionally remain ambient and
cannot supply Guest or Auto containment evidence.

### Binding construction and proof ownership

The construction order is normative and contains no digest/proof cycle:

1. Resolve canonical policy values and root identity, then construct the
   candidate `degraded` snapshot. Its policy digest covers policy values plus
   canonical workspace path and Darwin device/inode, never any proof.
2. Probe the fixed adapter against that candidate. Success returns only an
   `AdapterProof`. Failure constructs a separate `unavailable` snapshot from
   the same canonical values and an explicit unavailable binding with a stable
   reason code; the failed candidate is never exposed or used for spawn.
3. Construct the immutable binding. Its separate binding digest covers process
   class, policy digest, adapter family/generation, adapter axes, availability,
   and reason code.
4. Atomically pin the binding in `ShellManager`. The manager validates the
   configured root recheck, process-group, timeout, and bounded-output owners,
   then constructs an `ExecutionProof` bound to the exact binding digest.
5. Adapter prepare returns a launch specification carrying that binding digest,
   not a second proof. ShellManager compares it to its pinned digest immediately
   before start; mismatch yields zero `exec.Cmd.Start` attempts.
6. Construct QueryEngine only after the ShellManager proof and binding identity
   agree. A host-unavailable binding still constructs the engine, but every
   Guest prepare/start path rejects before `exec.Cmd.Start`.

Proof ownership is intentionally split. The Darwin adapter may attest only
filesystem read/write, network denial, pre-spawn root revalidation, and
Seatbelt inheritance by descendants. ShellManager may attest only per-command
root revalidation, descendant cleanup, wall time, and bounded retained output.
The final execution proof records both source masks and derives its combined
mask using fixed validation rules; neither source can claim memory,
file-descriptor, process-count, or environment-credential isolation. Root
identity is combined only when both the adapter pre-spawn and ShellManager
per-command checks are present.

`AxisDescendantConfinement` and `AxisDescendantCleanup` are distinct: Seatbelt
inheritance prevents a child from escaping the profile, while the existing
process-group owner retires descendants on timeout, cancellation, invalidation,
and close. P51.1 and P51.2 require both. A successful probe alone cannot claim
ShellManager-owned axes, and a configured ShellManager alone cannot claim
Seatbelt axes.

## Immutable identity and propagation

The snapshot has a versioned canonical digest over copied, validated values:

- requested profile, state, selection source, adapter family, platform/arch,
  and capability generation;
- canonical CWD, Darwin device/inode root identity, and typed
  read/write/temp/denied roots;
- network mode and opaque network-projection identity;
- environment-name policy, credential mode, and opaque credential/socket
  projection identity;
- resource and descendant-cleanup limits; and
- entrypoint plus root/child lineage.

Proofs are deliberately excluded from the snapshot digest. The adapter proof
references that digest; the binding digest then covers the adapter proof and
availability; the ShellManager execution proof references the binding digest
and adds only its owned runtime axes. This one-way identity chain is stable and
constructible.

It contains no secret value. Diagnostics expose only bounded reason codes and
redacted identities. Configuration reload, permission settlement, prompt/tool
content, restore, and worktree changes cannot mutate a snapshot already bound
to a process. A child may inherit exactly or narrow capabilities; it cannot
broaden any axis.

## Frozen invariants

- Permission may prevent spawn; it never constructs or mutates the envelope.
- A required binding executes only when every axis named by its accepted
  contract is proven. Missing axes or unavailable enforcement fails before
  spawn. `degraded` may execute only under the explicit axis-level rule above;
  it is never called fully contained.
- One composition-root binding matrix reaches every active process class. A
  child Guest binding is equal or narrower, and async work retains its dispatch
  binding. Ambient hook or MCP authority cannot become Guest authority.
- Existing process-group and Job Object cleanup remains necessary under every
  profile; containment never replaces cancellation ownership.
- Snapshot, binding, and execution-proof identities form the one-way chain
  `policy -> adapter proof -> binding -> runtime proof`; no downstream proof is
  an input to an upstream digest.
- A launch specification carries the pinned binding digest and no independent
  proof. ShellManager rejects any mismatch before constructing or starting the
  process.
- Guest environment inheritance is byte-for-byte in P51.1. Values never enter
  snapshots, diagnostics, permission descriptors, transcripts, or audit.
- The Guest denied-write roots reserve `<canonical-workspace>/.eino-agent` for
  host-owned transcript and WorkBoard state. This adds no Guest read-denial
  claim and does not constrain the independent ambient hook or stdio MCP
  bindings.
- Diagnostics and identity exclude secret values, command text, raw hook
  payloads, and unredacted host paths.

## Entrypoint contract

| Surface | Required binding |
|---|---|
| TUI/Plain | One binding matrix is resolved before engine construction; Guest, hooks, and stdio MCP consume only their named binding |
| Headless/headless Goal | Command root resolves once; Auto, `--yolo`, missing callbacks, and Goal state cannot change any binding |
| ACP | Server policy binds create/load/resume/import/fork staging; client fields cannot enlarge any binding |
| Child Agent | Parent derives an equal-or-narrower Guest binding before child QueryEngine construction; ambient hook/MCP bindings do not become Guest authority |
| Standalone MCP | Current allowlist remains host-process-free; adding a process or child-Agent tool later requires policy resolution before registration |
| Shell hooks | Hook executor receives the explicit ambient ShellHooks binding independently of Guest Bash; async hooks retain their dispatch binding |
| stdio MCP | Each configured external server process receives the explicit ambient StdioMCP binding before spawn |
| Unreached ShellTask/LSP | Stay outside product closure; any production constructor must first bind a policy |
| HTTP hooks | Independent egress decision; process-envelope success grants no HTTP authority |

A child policy has a new digest and root identity, so it cannot copy its
parent's proof. Derivation records the parent binding digest, validates an
equal-or-narrower root set, runs the same adapter generation's real probe for
the child policy, and lets the child ShellManager add runtime axes. A
non-subset, changed root identity, or missing/mismatched parent prevents child
QueryEngine construction. Adapter/platform/executable/probe unavailability
instead creates a child unavailable Guest binding, permits child engine
construction, and rejects only Guest Bash before spawn.

## P42.0 completed slice: immutable disabled-adapter seam

P42.0 is one behavior-preserving implementation PR. It must:

1. Add the typed immutable snapshot, canonical digest, copied-value
   construction, profile/state validation, redacted diagnostic projection, and
   monotonic child-derivation check.
2. Add one compatibility adapter representing the exact current
   `danger-full-access` plus non-enforcing behavior. It must identify itself as
   `disabled`, never `enforced`; P42.0 exposes no public selection flag and
   emits no new startup claim.
3. Resolve one compatibility snapshot at every listed composition root and
   carry it through QueryEngine, persistent Bash/background dispatch, hook
   executor including async work, configured stdio MCP launch, and child
   construction. Keep a source/test guard proving standalone MCP does not
   expose a host-process or child-Agent launcher.
4. Keep current commands, environment inheritance, permission outcomes,
   process startup, timeout, output, cancellation, and cleanup byte-for-byte or
   observably equivalent. P42.0 blocks no action and adds no sandbox.
5. Reject nil replacement, late mutation, invalid profile/state pairs, broader
   child derivation, and permission-derived policy construction in focused
   tests.

P42.0 does not add Linux, Darwin, or Windows enforcement; configuration/UI;
network proxying; credential brokering; resource limits; durable schema; HTTP
hook policy; or an application-helper sandbox. Those require later accepted
slices after the identity seam is proven.

## Required deterministic evidence

- canonical identity is stable across copied maps/slices and excludes secret
  values;
- caller mutation after construction cannot change the snapshot or digest;
- every permission mode, exact grant, and bypass fixture receives the same
  digest, while deny prevents spawn without changing policy;
- TUI, Plain, headless, Goal, ACP create/load/resume/import/fork, child Agent,
  hook/async-hook, and stdio MCP construction bind exactly one root or
  equal/narrower child snapshot; standalone MCP proves the no-host-process
  allowlist;
- child attempts to add roots, network, credentials, or resources fail closed;
- the disabled adapter preserves existing Bash, hook, and MCP lifecycle
  behavior and never reports `enforced`;
- race tests cover async hook capture, child construction, and concurrent
  process launch without snapshot replacement; and
- `make fmt`, `make lint`, `make lint-new`, `make test`, and `make build`, docs
  checks, migration manifest/ledger checks, and `git diff --check` pass.

No simulated test may claim an OS platform is enforced. Actual escape,
filesystem, network, credential, syscall, and resource tests belong to the
later adapter slice on real claimed platforms.

## Documentation ownership

Root `PLAN.md` owns future promotion, this file owns the accepted contract, the
comparative audit owns reference evidence, and `REMAINING.md` keeps G28 open.
P42.0 changes no current authority, so `STATUS.md` and current architecture
must not claim an execution sandbox. The completion record and current owners
describe only the identity seam actually implemented.

## Rollback

P42.0 writes no durable state and changes no external configuration. Rollback
removes the identity seam and returns to the current ambient process launchers.
It must not remove the existing process-group/Job Object cleanup or rewrite
permission prompts as containment.

A later enforced adapter may roll back only to an explicitly reported
`danger-full-access`/`disabled` selection with a compatibility warning. It may
not silently reinterpret a required profile.

## Completed containment slices

The P51.1 promotion gate was satisfied before implementation because:

1. P42.0 has terminal cross-entrypoint identity and orthogonality evidence;
2. P50.1-P50.3 are complete, so the accepted permission-runtime prerequisites
   no longer compete for the one Ready slot;
3. the accepted Darwin design names one platform/profile pair, exact fixed
   executable, proof axes, configuration authority, unsupported behavior,
   real escape tests, and rollback; and
4. a 2026-08-08 Darwin arm64 intake probe executed the fixed
   `/usr/bin/sandbox-exec`, accepted a valid profile, and rejected a real
   file-write operation without creating the target file.

That intake probe proved only that the implementation path was viable. P51.1
subsequently supplied the real subprocess matrix, cross-entrypoint binding,
race/lifecycle evidence, repository gates, and truthful closeout recorded in
[`p51-1-darwin-guest-seatbelt.md`](../verification/p51-1-darwin-guest-seatbelt.md).
P51.2 Core later passed an independent public intake and completed on
2026-08-19. Its historical contract is
[`p51-2-auto-containment-admission.md`](p51-2-auto-containment-admission.md),
and its reproducible delivery evidence is
[`p51-2-auto-containment-admission.md`](../verification/p51-2-auto-containment-admission.md).
The root queue is empty; G28 remains open without an admitted successor.
