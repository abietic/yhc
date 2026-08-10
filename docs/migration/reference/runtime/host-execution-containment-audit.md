# Host Execution Containment Audit

**Status:** reference-snapshot
**Snapshot:** 2026-08-02; Eino-Agent
`967dfdc87dbb87c018eb7431b948ee9b26687d92`, Codex
`66bd101fff6f`, Claude Code Ripe `4b9d30f79532`, OpenCode
`411eff73f026`, and Crush `2af939d8e900`

> **Ownership:** source-backed evidence for the P42 execution-envelope
> boundary. The accepted implementation contract and queue state belong in
> [`p42-host-execution-containment.md`](../../plans/p42-host-execution-containment.md)
> and [`PLAN.md`](../../PLAN.md). This audit does not claim that Eino-Agent
> currently has an OS sandbox.

## Decision

Use `project-native`. Preserve the project's existing process-tree cancellation
and entrypoint ownership, but introduce one immutable containment-policy
identity before adopting platform-specific mechanisms. Permission remains an
admission decision only: it may prevent a process from starting, but it cannot
select, relax, or elevate the process's filesystem, network, environment, or
resource authority.

No inspected reference can be copied as the complete contract. Codex provides
the strongest platform-specific enforcement evidence, but some of its approval
paths can change whether a sandbox attempt is made. Claude Code Ripe delegates
to an external sandbox runtime and exposes useful availability diagnostics.
Crush and OpenCode provide descendant-lifecycle evidence, not host-authority
containment.

## Reproduced current behavior

`ShellManager.startShell` launches host Bash with only a CWD and process-tree
owner. The child inherits the agent process environment because `cmd.Env` is
unset. `ExecuteShellHook` launches `sh -c`, explicitly starts from
`os.Environ()`, and overlays hook variables. Both paths bound timeout and
cleanup, but neither constrains filesystem roots, network, credentials,
syscalls, memory, descriptors, or process count.

Configured stdio MCP servers are stricter about lifecycle: their executable
must be absolute, Unix uses a process group, and Windows starts suspended before
assigning the process to a kill-on-close Job Object. They still inherit an
environment plus configured overlay and receive no filesystem, network, or
credential boundary.

The standalone MCP server does not directly register Bash. Its allowlist is
limited to task-record lifecycle and Todo tools; those tools do not launch an
Agent or host process. `LSPServiceManager` and `engine/tasks.ShellTask` contain
process launchers, but current production source has no constructor for either
outside its defining package. Definitions and package tests are not production
reachability evidence.

## Host-process inventory and trust boundary

P42 must classify every process launch, but it must not pretend that every
application helper has the same authority contract.

| Class | Current examples | P42 treatment |
|---|---|---|
| Guest command | Persistent Bash and commands/background jobs executed through it | Must receive the selected execution envelope before spawn; descendants retain the same identity |
| Configured extension | Shell hooks and stdio MCP servers | Must receive an explicit envelope or an explicitly separate policy; configuration trust is not containment |
| Indirect child Agent | TUI, Plain, headless Goal, and ACP paths that create a child QueryEngine | Must inherit the parent snapshot and may only narrow it |
| Application helper | Fixed-shape Git, ripgrep, PDF converter, notification, clipboard, and external-editor launches | Inventory is required, but P42.0 makes no sandbox claim for them; later work must either bind or explicitly exclude each helper class |
| Outside product closure | `engine/tasks.ShellTask` and `engine/services.LSPServiceManager` | No production constructor was found outside either defining package; activation must first bind the applicable policy |
| Standalone MCP | Task-record lifecycle and Todo tools only | Current allowlist has no host-process or child-Agent capability; keep a guard so later registration cannot create an unbound launcher |
| Non-process egress | HTTP hooks | Outside the process sandbox; needs an independent network-egress policy |

This classification prevents two false conclusions: process-group ownership is
not containment, and a trusted configuration file does not make a spawned
program unable to escape the workspace.

## Comparative evidence

| Source | Verified behavior | Adoption consequence |
|---|---|---|
| Eino-Agent | Bash and shell hooks own timeout/cancellation; stdio MCP owns complete process trees on Darwin, Linux, and Windows; permission architecture explicitly says Auto is not an OS sandbox | Preserve lifecycle owners and permission separation; add one policy identity before enforcement |
| Codex | `SandboxType` selects macOS Seatbelt, Linux sandbox helpers, Windows restricted-token execution, or none; execution requests carry filesystem and network policy; tests cover protected paths, network proxy sockets, and platform unavailability | Reuse primitive families and conformance questions, not approval-driven sandbox bypass semantics |
| Claude Code Ripe | The sandbox adapter checks dependencies, converts filesystem/network settings, reports missing dependencies, and can fail when unavailable | Preserve truthful probe and fail-if-unavailable behavior; do not import mutable UI/config ownership |
| Crush | Unix shell isolation creates a separate session/process group and tests descendant cleanup | Reuse cancellation evidence only; it proves no filesystem or network boundary |
| OpenCode | Shell execution selects a platform shell and kills the Unix process group or Windows process tree | Reuse lifecycle evidence only; no inspected execution path establishes OS containment |

## Platform primitive matrix

The matrix names candidate primitive families; it does not mark any platform as
currently supported. A requested profile is `enforced` only when every required
axis is active for that exact process. Partial enforcement is `degraded` and
cannot silently run a required profile.

| Target | Filesystem/syscall candidate | Network candidate | Resource/descendant candidate | Unavailable condition |
|---|---|---|---|---|
| Linux amd64/arm64 | User/mount namespace with a verified helper, or Landlock where its access model is sufficient; seccomp for disallowed syscall classes | Network namespace or mandatory managed proxy | cgroup v2 where delegated, `setrlimit`, process group/pid owner | Required kernel feature, helper, namespace, policy compilation, or resource owner cannot be established before spawn |
| Darwin amd64/arm64 | `/usr/bin/sandbox-exec` Seatbelt profile with canonical read/write rules | Seatbelt socket rules and, when allowed, a managed proxy/socket projection | `setrlimit` plus process-group ownership | Seatbelt binary/profile application, required socket rule, or resource owner is unavailable |
| Windows amd64 | Restricted token or AppContainer-equivalent identity with explicit filesystem grants | WFP/firewall policy or mandatory managed proxy | Job Object kill-on-close and resource limits | Token/SID, ACL, Job Object, or required network policy cannot be installed before resume |
| Other GOOS/GOARCH | None accepted | None accepted | Existing direct-child cleanup is not containment | Always `unavailable` for a required contained profile |

The implementation may choose a smaller primitive set after platform tests, but
it cannot rename partial coverage to `enforced`. Linux Landlock without the
required network and resource axes, for example, is a useful probe result but
not a complete `workspace-write` envelope.

## Capability and failure states

Profiles describe requested authority; state describes whether that authority
is actually enforced.

| Profile/state | Start decision |
|---|---|
| `read-only` + `enforced` | Start with declared readable roots, no writes, network denied unless separately granted, and bounded resources |
| `workspace-write` + `enforced` | Start with writes limited to canonical workspace/temp roots and the same independent network/resource policy |
| Required profile + `degraded` or `unavailable` | Fail before spawn with a stable reason code |
| `danger-full-access` + explicit `disabled` | Preserve ambient host authority only after an operator-visible, non-permission selection and warning |

An interactive permission prompt cannot double as selection of
`danger-full-access`. Non-interactive and autonomous entrypoints fail closed
when a required profile is unavailable. Diagnostics expose profile, state,
adapter family, and reason code; they do not expose command text, secrets, raw
roots, proxy endpoints, hook payloads, or inherited environment values.

## Immutable policy identity

One composition-root resolver produces an immutable snapshot before an engine,
hook executor, extension manager, or child runner can launch a process. The
identity contains:

- schema revision and canonical digest;
- requested profile, enforcement state, selection source, adapter family, and
  platform capability generation;
- canonical CWD plus typed readable, writable, temporary, and denied-root
  identities;
- network mode and opaque projection identity;
- environment-name policy and opaque credential/socket projection identity;
- time, memory, descriptor, process-count, output, and descendant-cleanup
  policy;
- entrypoint, root/child lineage, and monotonic-narrowing relation.

Secret values and raw diagnostic paths are not identity fields. Construction
copies caller-owned maps and slices, validates canonical roots, and hashes one
canonical encoding. A process and every descendant keep the exact snapshot
used at spawn even if configuration, CWD, permission mode, or session state
changes later.

## Entrypoint wiring matrix

| Surface | Frozen requirement |
|---|---|
| TUI and Plain | Resolve once before `NewQueryEngine`; both consumers use the same engine snapshot |
| Ordinary headless and headless Goal | Resolve at the command composition root; `--yolo`, Auto, missing callbacks, and Goal transitions cannot change it |
| ACP | Resolve one server policy, bind it to create/load/resume/import/fork staging, and reject client attempts to enlarge it |
| Child Agent | Derive by monotonic narrowing from the parent; prompt/tool output, worktree selection, or inherited permission grants cannot broaden it |
| Standalone MCP | Prove the current allowlist has no host-process or child-Agent capability; future expansion must resolve a policy before registering such a tool |
| Shell hooks | Bind separately from Bash dispatch; asynchronous work pins the snapshot captured at dispatch |
| stdio MCP | Bind each configured server launch explicitly; process-tree ownership remains necessary but insufficient |
| Unreached ShellTask/LSP | Keep outside current coverage; production activation first requires a bound policy |
| HTTP hooks | Evaluate an independent egress policy; process-envelope success grants no HTTP authority |

## Deterministic escape and conformance tests

1. The same action under every permission mode, exact grant, and future bypass
   sees the same policy digest. Deny prevents spawn; allow does not mutate the
   digest.
2. Tool input, hook output, model output, MCP metadata, ACP client fields, child
   results, environment reload, and session restore cannot broaden any axis.
3. Required-but-unavailable fails before spawn on every target. Explicit
   disabled mode emits one redacted diagnostic and never calls itself sandboxed.
4. `..`, symlink, renamed/deleted roots, mount/reparse boundaries, shell
   substitution, background descendants, cancellation, and timeout keep the
   original identity and descendant owner.
5. Credential sentinels, SSH agents, cloud metadata access, and credential
   sockets are absent unless explicitly projected.
6. Network denial and opt-in network capability are tested independently from
   filesystem writes; HTTP hooks cannot inherit process authorization.
7. TUI, Plain, headless, Goal, ACP create/load/resume/import/fork, child Agent,
   async hook, and stdio MCP fixtures prove one root snapshot and no late
   replacement. Standalone MCP proves its no-host-process allowlist.
8. Resource exhaustion covers memory, descriptors, process count, output, and
   wall time. Cancellation proves child and grandchild cleanup.
9. Platform enforcement tests run only on a real claimed adapter. Unsupported
   targets assert `unavailable` or explicit `disabled`, never simulated success.

## Compatibility and exclusions

P42.0 is a behavior-preserving identity seam. It adds no sandbox adapter,
network proxy, credential broker, public profile flag, configuration migration,
or supported-platform claim. Current host execution remains ambient until a
later accepted enforcement slice is delivered. Application helpers and HTTP
hooks remain explicitly uncontained rather than being hidden inside a broad
claim.

## Source anchors

| Boundary | Evidence |
|---|---|
| Persistent Bash lifecycle | [`ShellManager.startShell`](../../../../tools/bash_shell.go), [`BashTool.Execute`](../../../../tools/bash.go) |
| Shell hooks and async ownership | [`ExecuteShellHook`](../../../../engine/hooks/shell.go), [`Executor`](../../../../engine/hooks/hooks.go) |
| stdio MCP process tree | [`newStdioProcessTransport`](../../../../engine/mcp/stdio_transport.go), [`stdio_transport_unix.go`](../../../../engine/mcp/stdio_transport_unix.go), [`stdio_transport_windows.go`](../../../../engine/mcp/stdio_transport_windows.go) |
| Unreached LSP process owner | [`LSPServiceManager.Start`](../../../../engine/services/lsp_manager.go) |
| Child Agent construction | [`SubAgentExecutor.Execute`](../../../../engine/subagent.go) |
| Standalone MCP allowlist | [`standaloneMCPToolAllowlist`](../../../../server/mcp/server.go) |
| Permission is not a sandbox | [`permissions.md`](../../../architecture/capabilities/permissions.md) |
| Codex platform transform | `.reference/codex/codex-rs/sandboxing/src/manager.rs` |
| Codex macOS policy | `.reference/codex/codex-rs/sandboxing/src/seatbelt.rs` |
| Codex Linux tests | `.reference/codex/codex-rs/linux-sandbox/tests/suite/landlock.rs` |
| Codex Windows implementation | `.reference/codex/codex-rs/windows-sandbox-rs/src` |
| Claude availability adapter | `.reference/claude-code-ripe/src/utils/sandbox/sandbox-adapter.ts` |
