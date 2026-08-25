# P51.3 Linux Guest Bubblewrap Verification

**Status:** verification
**Last verified:** 2026-08-26
**Platform:** Darwin arm64 local checks; Linux amd64 GitHub-hosted Ubuntu

> **Ownership:** reproducible evidence for the prompt-approved Linux Guest
> bubblewrap subset and its explicit exclusion from automatic Bash admission

## Accepted result

P51.3 adds a Linux amd64/arm64 `workspace-write` Guest adapter around the fixed
`/usr/bin/bwrap` executable. It builds an empty mount root, projects declared
read roots read-only and workspace/temp roots writable, overlays existing
denied roots read-only, unshares user/PID/IPC/network namespaces, drops all
capabilities, and installs a seccomp filter that denies socket operations and
`io_uring_setup`. A real pre-binding probe exercises allowed access, outside
read/write denial, TCP/UDP denial, and descendant confinement. Missing binary,
root drift, malformed policy, or failed probe produces an unavailable Guest;
there is no ambient fallback.

This slice does not close G28. Linux inherits ambient environment credentials
and lacks hard memory, file-descriptor, and process-count limits. More
importantly, only protected paths that already exist can be overlaid read-only;
an absent control-plane path is not reserved against later creation. Linux
proof therefore remains ineligible for Default/Auto's automatic ordinary Bash
admission and is used only after another permission path approves execution.

## Contract-to-evidence map

| Contract | Production owner | Oracle |
|---|---|---|
| Fixed trusted executable and supported architectures | `verifyBubblewrapExecutable`, `linuxBubblewrapAdapter.supported` | `TestBubblewrapProbeReasonMapping`, required Linux integration job |
| Empty root plus ordered read/write/deny mounts | `renderBubblewrapArgs` | `TestBubblewrapPrepareUsesFixedBoundaryAndSeccompFD`, `TestBubblewrapLinuxIntegration` |
| Network and local Unix-socket denial | network namespace plus `newSocketDenySeccompFile` | real probe TCP/UDP checks and integration Unix-socket helper |
| Root, child, restore, and no-fallback identity | `ResolveExecutionBindings`, binding derivation, `Binding.Prepare` | existing P51 binding and lifecycle suites plus Linux integration |
| Existing-only protected paths and symlink failure | `workspaceGuestRoots`, `pathCrossesSymlink` | `TestP513LinuxWorkspaceGuest*`, `TestP513PathCrossesSymlink` |
| No Linux automatic Bash admission | `completeContainedAutoBashProof` | Linux-adapter mutation in P51.2 permission-action tests |

## Observed checks

The implementation passed the platform-neutral containment,
tools, and engine suites, the focused ACP/engine checks, and Linux amd64/arm64
test-binary cross-compilation. Those checks prove build and deterministic
contract behavior; they do not prove Linux kernel enforcement.

The required GitHub Actions `linux-sandbox` job installs bubblewrap, enables
unprivileged user namespaces on the disposable hosted runner, clears Ubuntu's
AppArmor user-namespace gate when present, and runs:

```bash
YHC_REQUIRE_LINUX_BWRAP=1 go test ./engine/containment -run '^TestBubblewrapLinuxIntegration$' -count=1
```

The required job passed without a skip for commit `3501ae2` in CI run
[`32887604283`](https://github.com/abietic/yhc/actions/runs/32887604283), job
[`97931665052`](https://github.com/abietic/yhc/actions/runs/32887604283/job/97931665052),
in 31 seconds. The committed-tree focused and merge workflows were
`evidence_ready` before push. Final PR-wide CI after closeout documentation
remains a separate merge gate.

## Failure and skip interpretation

- `YHC_REQUIRE_LINUX_BWRAP=1` converts a missing binary or failed real probe
  into a test failure. A local optional skip proves no Linux containment.
- A host that blocks unprivileged user namespaces remains unsupported at
  runtime and produces an unavailable Guest. The CI sysctl setup changes only
  its disposable hosted runner; it is not an application fallback.
- A Linux unavailable Guest rejects Bash before spawn and never retries
  ambient execution.
- A successful Linux binding does not authorize automatic Bash admission; an
  ordinary permission path must still approve the invocation.
- Darwin local tests and cross-compilation cannot replace the real Linux job.

## Reproduction

```bash
make change-plan
make verify-focused
make verify-merge
make change-evidence
make change-evidence-ready
```
