# P51.1 Darwin Guest Seatbelt Verification

**Status:** verification
**Last verified:** 2026-08-09

> **Ownership:** reproducible evidence for the P51.1 Darwin Guest execution
> envelope; permission behavior and remaining host authority have separate
> owners

## Contract

TUI, Plain, headless, headless Goal, and ACP resolve one immutable matrix before
ordinary execution. On Darwin amd64/arm64 its Guest binding is
`workspace-write`/`degraded` through the fixed `/usr/bin/sandbox-exec`; shell
hooks and configured stdio MCP retain independent ambient bindings. Child and
restored Guest roots are equal or narrower, recaptured, and re-probed before
any Guest process can start.

An unavailable platform, executable, profile, probe, or root produces an
immutable unavailable Guest binding. QueryEngine remains usable, but Guest Bash
fails before `exec.Cmd.Start` and never retries ambient. Only user configuration
or the explicit CLI flag can select `danger-full-access`. Permission mode,
project configuration, hooks, tool input, and ACP clients cannot broaden the
matrix. P51.1 changes no Default, Plan, AcceptEdits, Auto, bypass, or DontAsk
permission result.

## Deterministic Evidence

Focused fixtures prove:

- policy, adapter proof, binding, and runtime proof form a one-way immutable
  identity chain with fixed adapter/runtime axis ownership;
- project and project-local sandbox fields are discarded before typed decode,
  user extra roots are canonical and bounded, and explicit CLI intent wins;
- every process owner accepts only its named Guest, ShellHooks, or StdioMCP
  binding, including async hook and ACP stdio setup paths;
- ShellManager preserves the exact `os.Environ()` slice, persistent cwd,
  background behavior, merged stderr, wall-time/process-group cleanup, and a
  bounded retained-output collector;
- unavailable, changed-root, wrong-class, generation, or launch-digest
  mismatches make zero process-start attempts;
- restore finalizes the metadata/worktree root before activation, rejects an
  external manager pinned to another root, and re-probes even when the
  canonical restored path is unchanged;
- child Guest derivation can only retain or narrow the parent Guest roots and
  cannot acquire ambient hook/MCP authority; and
- startup diagnostics contain only bounded profile/axis/reason facts, including
  explicit ambient rollback and unavailable-without-fallback states.

## Real Darwin Evidence

The adapter capability probe and integration fixture execute the real fixed
binary. They require workspace read/write success and outside read/write, live
TCP, UDP, unapproved Unix socket, symlink, `..`, command substitution, nested
child, and background-descendant escape failure. The probe earns adapter axes
only after the same real behavioral checks pass.

The QueryEngine product fixture builds a disposable Git/Go repository through
the production binding matrix. It runs `go test ./...`, `make test`, and
`git --no-optional-locks status --short`; verifies an exact inherited
environment value plus background create/rename; and proves writes to project
settings, `.git/config`, Git hooks, and an existing
`.eino-agent/transcripts/*.workboard.json` sentinel fail without changing
their bytes. The `.eino-agent` rule is write-only and does not claim Guest
read isolation.

## Commands

```bash
go test ./engine/containment/ -run 'TestSeatbeltProbe|TestSeatbeltDarwinIntegration' -count=1
go test ./engine/ -run 'TestP511RestoreReprobesSameSeatbeltRoot|TestP511RestoreRejectsSamePathRootReplacement|TestP511DarwinGuestRunsGoAndProtectsControlPlaneWrites' -count=1
go test ./engine/config/ ./cmd/eino-agent/cmd/ ./server/acp/ ./engine/hooks/ ./engine/mcp/ ./tools/ -run 'P511|Sandbox|Binding|AsyncRewake' -count=1
go test -race ./engine/containment/ ./engine/ ./engine/hooks/ ./engine/mcp/ ./tools/ ./server/acp/ -run 'P511|Seatbelt|Binding|AsyncRewake|ProjectGraphColdRestart|ResumeSessionInfo|RestoreStagingDefers' -count=3
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All listed local commands pass on the closeout tree. The required full Makefile
gates remain the repository-level acceptance; real Darwin tests are not
substituted by mocks or cross-compilation. Remote CI remains a separate merge
gate.

## Evidence Limits

The Guest environment remains byte-for-byte ambient. Hard memory,
file-descriptor, and process-count limits are absent. Shell hooks and configured
stdio MCP remain ambient; HTTP hooks and standalone MCP are outside this
process envelope. Linux and Windows do not gain a simulated sandbox. The
aggregate Darwin Guest state remains `degraded`, G28 remains open, and this
P51.1 evidence by itself proves no Auto Permission prompt reduction. P51.2 Core
later added that separate permission matrix; its evidence is recorded in
[`p51-2-auto-containment-admission.md`](p51-2-auto-containment-admission.md).
