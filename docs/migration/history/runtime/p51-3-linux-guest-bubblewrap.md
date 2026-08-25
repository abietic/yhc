# P51.3 Linux Guest Bubblewrap

**Status:** historical
**Completed:** 2026-08-26
**Adoption:** `adapt`

> **Ownership:** completion record for prompt-approved Linux Guest containment;
> current behavior belongs in architecture and operator documentation

## Outcome

Linux amd64/arm64 `workspace-write` Guest Bash now uses the fixed
`/usr/bin/bwrap` executable when its immutable policy and real capability probe
succeed. The adapter constructs an empty root, projects declared runtime roots
read-only, binds workspace/temp roots writable, overlays existing protected
paths read-only, enters user/PID/IPC/network namespaces, drops capabilities,
and denies socket operations plus `io_uring_setup` through seccomp.

The probe proves allowed reads/writes, outside-root denial, TCP/UDP denial, and
descendant confinement before a binding becomes available. ShellManager pins
the exact binding and root, revalidates before start and command submission,
passes the seccomp descriptor explicitly, and retains wall-time, output, and
process-group cleanup owners. Child and restored Guests derive the same adapter
family and re-probe an equal-or-narrower root. Any missing helper, incompatible
host namespace policy, failed probe, or root drift rejects Bash before spawn;
there is no ambient retry.

## Retained Boundary

Only protected paths that exist while the Linux policy is built can be
overlaid read-only. Workspace-local denied paths crossing symlinks fail closed,
but absent names are not reserved against later creation. Linux proof therefore
does not satisfy Default/Auto automatic Bash admission; another permission path
must approve the invocation first.

Environment credentials, hard memory/file-descriptor/process limits, shell
hooks, stdio MCP, Windows, and application helpers remain outside this subset.
G28 remains open without an accepted successor.

## Evidence

Platform-neutral unit, engine, ACP, tool, race, contract, documentation,
publication, and committed-tree gates passed. Linux amd64/arm64 test binaries
cross-compiled, and the required GitHub-hosted Ubuntu job executed the real
bubblewrap integration without a skip. The reproducible matrix and exact job
identity are in the
[`P51.3 verification record`](../../verification/p51-3-linux-guest-bubblewrap.md).
