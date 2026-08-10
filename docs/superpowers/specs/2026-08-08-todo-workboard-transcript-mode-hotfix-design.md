# Todo And WorkBoard Transcript Mode Hotfix Design

**Status:** historical
**Review:** approved 2026-08-09
**Accepted direction:** 2026-08-08
**Completed:** 2026-08-09
**Source snapshot:** `origin/master` at
`1bff5539d733ac1370f75e1f99cd14e96fc0a291`
**Adoption:** `project-native`

> **Ownership:** reviewed hotfix design for private transcript-directory
> compatibility, truthful Todo permission metadata, and Guest protection of
> engine-owned `.eino-agent` state; current behavior remains owned by
> [`tasks-and-agents.md`](../../architecture/runtime/tasks-and-agents.md),
> [`transcripts.md`](../../architecture/state/transcripts.md), and
> [`p42-host-execution-containment.md`](../../migration/plans/p42-host-execution-containment.md)

## User Problem

`TodoWrite` is admitted without an ordinary permission prompt, but its first
durable WorkBoard mutation fails when an existing transcript directory has
mode `0755` instead of `0700`. The failure is reproducible in Default and Auto
permission modes, with either ambient or Darwin `workspace-write` Guest
execution. It is a transcript-store compatibility failure, not a Seatbelt
denial.

The same area has two related contract drifts:

- `TodoWrite` is described as process-local even though QueryEngine routes it
  to the durable WorkBoard authority; and
- Guest Bash may write `.eino-agent`, including transcript and WorkBoard
  control-plane state, because the workspace write root currently contains no
  matching denial.

## Selected Approach

Use one narrow compatibility repair at the mutation-capable WorkBoard adapter
boundary, correct the permission capability vocabulary without changing the
default permission outcome, and reserve `.eino-agent` writes for the host.

Two alternatives are rejected:

- Error-message-only remediation still makes users run `chmod` manually and
  leaves an otherwise safe legacy directory unusable.
- Moving WorkBoard artifacts into a new private subdirectory adds migration,
  fork, resume, delete, and recovery risk while transcript files still depend
  on a private transcript root.

## Observable Contract

1. A missing transcript directory is created with mode `0700` before a
   mutation-capable `LogicalWorkAdapter` becomes usable.
2. An existing real directory with a different permission mode is safely
   tightened to `0700`; the first Todo or Task mutation then succeeds.
3. A symlink, non-directory, directory replaced during preparation, or
   directory that cannot be secured fails before model or tool execution and
   before any WorkBoard artifact is written.
4. Low-level read-only Store inspection remains fail-closed and performs no
   permission repair. Session administration therefore does not gain an
   implicit filesystem mutation.
5. `TodoWrite` remains default-allowed in Default, DontAsk, Plan, and Auto
   modes. Explicit `ask` and `deny` rules remain authoritative.
6. Permission mode still cannot select or broaden a sandbox binding.
7. Darwin `workspace-write` Guest Bash cannot write `.eino-agent`; host-owned
   transcript and WorkBoard operations remain unaffected.

All configured transcript roots are private runtime stores. Selecting a custom
transcript root does not opt it out of the `0700` invariant.

## Components And Data Flow

### Private transcript directory preparation

`engine/internal/workboard` owns a small helper used by
`NewLogicalWorkAdapter` after read-only Store inspection successfully reports
legacy authority:

1. `Lstat` the final path.
2. Create it with `0700` when missing.
3. Reject a symlink or non-directory.
4. Open and pin the directory with `os.OpenRoot`.
5. Revalidate that the path and opened handle identify the same object.
6. Apply `Chmod(".", 0700)` through the pinned handle.
7. Revalidate path identity and exact mode before returning.
8. Bind the secured identity to the adapter Store and revalidate it before
   first cutover and artifact root-open.

The existing `ArtifactStore.validateDirectory` check remains in place as
defense in depth before every artifact operation. The helper does not repair
artifact file modes, replace corrupt objects, or follow a final-path symlink.

`NewLogicalWorkAdapter` is the repair boundary because it creates a
mutation-capable durable owner. It inspects first, so invalid committed or
prepared authority fails before any mode repair; only a successful legacy
result reaches the helper. `Store.Inspect` stays read-only so list, inspection,
and administration flows do not silently change user state. The identity
observation is captured before inspection, so replacement before preparation
also fails; the bound secured identity closes the later gap before first
mutation.

### Permission capability correction

`TodoWrite` changes from `ToolActionProcessLocal` to
`ToolActionRuntimeState`. The permission descriptor replaces the narrow
`ProcessLocalDefaultSafe` fact with a trusted built-in internal-state fact.
That fact requires all of:

- the tool is built in and its capability metadata is declared;
- the registry explicitly marks it `DefaultPermissionAllowed`; and
- its action kind is `process_local` or `runtime_state`.

This preserves the existing explicit-rule ordering and default allow result
without falsely claiming that the operation has no durable side effect.

### Guest control-plane protection

The Darwin Guest policy adds `<canonical-workspace>/.eino-agent` to its denied
write roots. The rule is immutable policy input and applies to persistent Bash,
background commands, descendants, and child-Agent Bash. It does not affect
host execution of `TodoWrite`, transcript persistence, or Session services.

This hotfix adds write protection only. Expanding the containment policy with
a distinct host-private no-read root is a separate security design and is not
claimed here.

## Error Handling

- Directory preparation wraps errors with the WorkBoard authority boundary and
  stage: inspect, create, open, identity revalidation, chmod, or final mode.
- Failure writes no authority, marker, or backup artifact.
- Unsafe legacy directories are never accepted merely to improve usability.
- A sandbox denial remains an ordinary contained Bash failure and never causes
  ambient retry or permission-driven escalation.

## Verification

Regression tests must cover:

- missing, existing `0700`, and legacy `0755` transcript directories;
- first `TodoWrite` and Task mutation after legacy-directory repair;
- symlink and non-directory rejection with zero WorkBoard artifacts;
- deterministic same-path replacement both after inspection and before first
  cutover, with zero artifacts in the original or replacement directory;
- permission matrix preservation for Default, DontAsk, Plan, Auto, explicit
  ask, and explicit deny;
- `TodoWrite` capability classification as trusted runtime state;
- Auto plus `workspace-write` executing `TodoWrite` without a prompt or Guest
  process;
- immutable sandbox denied roots containing `.eino-agent`;
- real Darwin Guest failure to modify an existing transcript/WorkBoard
  sentinel while ordinary workspace writes still succeed; and
- restart/reload of the successfully promoted WorkBoard.

Final gates are:

```bash
make fmt
make lint
make test
make build
go run ./scripts/migration_manifest.go check
git diff --check
```

Darwin enforcement tests, cross-platform build evidence, remote CI, and real
product acceptance remain separately reported evidence.

## Rollback

Revert the directory preparation, capability correction, and `.eino-agent`
deny as one hotfix. Existing `0700` directories and WorkBoard artifacts remain
compatible; rollback does not require a data migration. A directory already
tightened from `0755` to `0700` stays private after rollback.

## Non-Goals

- P51.2 Auto prompt reduction.
- Reviewer enforcement or classifier changes.
- Moving WorkBoard artifacts or changing their schemas.
- Repairing corrupt, symlinked, or wrongly typed artifacts.
- Sandbox enforcement for hooks, stdio MCP, or non-Darwin platforms.
- Guest environment sanitization or a host-private no-read policy.
