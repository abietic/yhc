# YHC State Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `$iteration-workflow` and `$runtime-depth-change` to execute and close this
> state-sensitive plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `<project>/.yhc` and `~/.yhc` the canonical default write roots,
provide a root-pinned exact-artifact importer, and migrate only validated
non-secret settings, keybindings, memory, approvals, project history, and
permission-review audit while leaving every legacy byte intact.

**Architecture:** `internal/statepath` resolves canonical/legacy defaults and
explicit overrides using `internal/identity`. `internal/statemigration` owns
root pinning, per-owner locks, collision refusal, staging, fsync, promotion,
restart cleanup, and value-free results. Each persistence owner supplies its
exact artifact/subtree schema and transformation; no global copier understands
owner data. A `yhc migrate-state` command exposes inspect/apply without
enumerating credentials.

**Tech Stack:** Go 1.26.5, `os.OpenRoot`, `io/fs`, JSON/JSONL/Markdown owner
schemas, Cobra, secure file modes, atomic rename/fsync, race and failpoint tests,
and existing permission/memory/TUI oracles.

**Status:** active-plan
**Created:** 2026-08-09
**Plan state:** Tasks 1-5 complete; Task 6 verification in progress

> **Ownership:** canonical roots, generic import safety, and plain state owners
> from the [YHC public-release design](../specs/2026-08-09-yhc-public-release-design.md).
> Session bundles, cron, and worktrees are owned by the state-continuity plan.

## Global Constraints

- Canonical defaults write only under `.yhc` or `~/.yhc`. Legacy defaults are
  discoverable only through owner-declared read/import paths and are never
  deleted, renamed, chmodded, truncated, rewritten, or merged.
- Explicit paths selected by canonical or legacy `*_CONFIG_DIR`, catalog,
  memory, audit, or comparable overrides remain exact and are marked
  `Migratable=false`. An empty canonical variable blocks its legacy alias and
  selects the canonical default.
- No API accepts “copy this root.” An artifact names exact relative files or one
  exact owner subtree and validates every traversed entry. Symlinks, special
  files, hard-link ambiguity, non-owner-writable modes, unsupported versions,
  and path replacement fail closed. An owner may explicitly accept historical
  group/other read or directory-execute bits only when its real legacy writer
  produced them; canonical staged output remains `0700`/`0600`.
- Import is per owner. A destination artifact collision returns
  `destination_exists` and performs no merge even if legacy appears newer.
  Canonical state wins.
- Staging is a sibling inside the pinned canonical owner root. Success fsyncs
  content and parents before atomic promotion. Failure leaves canonical absent
  or previously valid and legacy byte-identical; restart can safely inspect and
  retry.
- Locks serialize YHC importers but do not prove a legacy producer is stopped.
  An owner with a legacy writer must supply a compatible quiescence check or
  remain read-only.
- Never copy `provider.apiKey`, OAuth tokens, credentials, credential-like
  settings, `.mcp.json`, or `.claude` data into YHC state. Preserve existing
  `.claude` read/write semantics and credential store paths in place.
- Diagnostics contain owner, scope, status, schema version, and relative
  artifact ID only. They never contain config values, prompts, commands, paths
  outside the selected root, credential names, or file bodies.
- This plan must not change session resume, WorkBoard, scheduler, worktree,
  provider, tool, permission-decision, cancellation, or protocol semantics.

---

## Locked Interfaces

```go
package statepath

type Source uint8

const (
	SourceDefault Source = iota
	SourceCanonicalEnv
	SourceLegacyEnv
)

type Roots struct {
	Canonical string
	Legacy    string
}

type Selection struct {
	Effective  string
	Roots      Roots
	Source     Source
	Migratable bool
}

func ProjectRoots(projectRoot string) (Roots, error)
func UserRoots(userHome string) (Roots, error)
func ResolveOverride(pair identity.EnvPair, defaults Roots) (Selection, error)
```

```go
package statemigration

type Kind uint8

const (
	RegularFile Kind = iota
	DirectoryTree
)

type LegacyMode uint8

const (
	LegacyPrivate LegacyMode = iota
	LegacyOwnerControlled
)

type Snapshot interface {
	Open(relative string) (io.ReadCloser, fs.FileInfo, error)
	Walk(func(relative string, entry fs.DirEntry) error) error
	Digest() string
}

type ArtifactSpec struct {
	Owner       string
	Scope       string
	SourceRel   string
	TargetRel   string
	Kind        Kind
	LegacyMode  LegacyMode
	MaxFiles    int
	MaxBytes    int64
	Validate    func(context.Context, Snapshot) error
	Stage       func(context.Context, Snapshot, *os.Root) error
	Quiescent   func(context.Context, Snapshot) (bool, error)
	AcquireSourceLease func(context.Context, string) (func(), bool, error)
}

type Status string

const (
	StatusAbsent            Status = "absent"
	StatusReady             Status = "ready"
	StatusImported          Status = "imported"
	StatusDestinationExists Status = "destination_exists"
	StatusLegacyBusy        Status = "legacy_busy"
	StatusUnsafe            Status = "unsafe"
)

type Result struct {
	Owner  string
	Scope  string
	Status Status
}

type Importer struct{}

func (Importer) Inspect(context.Context, statepath.Roots, ArtifactSpec) (Result, error)
func (Importer) Import(context.Context, statepath.Roots, ArtifactSpec) (Result, error)

type CanonicalStore struct { /* pinned private root and exact directory */ }

func OpenCanonicalStore(root, relative string, create bool) (*CanonicalStore, bool, error)
func (*CanonicalStore) Root() *os.Root
func (*CanonicalStore) Revalidate() error
func (*CanonicalStore) Sync() error
func (*CanonicalStore) OpenRegular(name string, flag int, create bool) (*os.File, os.FileInfo, bool, error)
func (*CanonicalStore) PromoteRegular(from, to string, expected os.FileInfo) error
func (*CanonicalStore) ValidateRegular(name string, expected os.FileInfo) error
func (*CanonicalStore) RemoveRegularIfSame(name string, expected os.FileInfo)
func (*CanonicalStore) Close() error
```

CLI:

```text
yhc migrate-state inspect [--scope project|user] [--owner NAME]
yhc migrate-state apply --scope project|user --owner NAME
```

There is no all-roots recursive mode. `apply` requires one owner per invocation
so status and rollback boundaries remain explicit.

## Task 1: Resolve Canonical Roots And Exact Overrides

**Files:**

- Create: `internal/statepath/paths.go`
- Create: `internal/statepath/paths_test.go`
- Create: `cmd/yhc/cmd/migrate_state.go`
- Create: `cmd/yhc/cmd/migrate_state_test.go`
- Modify: `cmd/yhc/cmd/root.go`

- [x] **Step 1: Add root and CLI red tests**

Create `TestProjectAndUserRootsUseYHCAndPreserveLegacy`,
`TestCanonicalAndLegacyOverridesPreserveExactPath`,
`TestEmptyCanonicalOverrideBlocksLegacyAndUsesDefault`,
`TestInvalidCanonicalOverrideDoesNotFallThrough`,
`TestMigrateStateRequiresOneKnownOwnerForApply`, and
`TestMigrateStateDiagnosticsContainNoValues`.

- [x] **Step 2: Run red**

```bash
go test ./internal/statepath ./cmd/yhc/cmd -run 'Test(ProjectAndUserRoots|CanonicalAndLegacyOverrides|EmptyCanonicalOverride|InvalidCanonicalOverride|MigrateState)' -count=1
```

- [x] **Step 3: Implement path-only resolution and command shell**

Canonical and legacy roots use `identity.ProjectDirName` and
`identity.LegacyDirName`. Canonicalize caller-provided project/home inputs
without following a missing final component. The initial command lists only
registered owner names and invokes injected inspect/apply functions; it does not
walk files or initialize the model runtime.

- [ ] **Step 4: Run green and commit**

```bash
go test ./internal/statepath ./cmd/yhc/cmd -run 'Test(ProjectAndUserRoots|CanonicalAndLegacyOverrides|EmptyCanonicalOverride|InvalidCanonicalOverride|MigrateState)' -count=1
git add internal/statepath cmd/yhc/cmd
git commit -m "feat(state): resolve YHC canonical and legacy roots"
```

## Task 2: Implement The Exact-Artifact Importer

**Files:**

- Create: `internal/statemigration/importer.go`
- Create: `internal/statemigration/roots.go`
- Create: `internal/statemigration/snapshot.go`
- Create: `internal/statemigration/staging.go`
- Create: `internal/statemigration/lock.go`
- Create: `internal/statemigration/platform_{unix,windows}.go`
- Create: `internal/statemigration/rename_{darwin,linux}.go`
- Create: `internal/statemigration/importer_test.go`
- Create: `internal/statemigration/failpoint_test.go`
- Create: `internal/statemigration/special_{unix,windows}_test.go`
- Modify: `quality/iteration.yaml`

- [x] **Step 1: Add red safety tests**

Create:

- `TestImporterRejectsSymlinkHardlinkAndRootReplacement`;
- `TestImporterRejectsDestinationCollisionWithoutMerge`;
- `TestImporterConcurrentSinglePromotion`;
- `TestImporterFailureIsAtomicAndRestartSafe`;
- `TestImporterLeavesLegacyBytesModeAndMtimeUnchanged`;
- `TestImporterRefusesUnknownEntryUnsupportedSchemaAndUnsafeMode`;
- `TestImporterEnforcesFileAndByteBudgets`; and
- `TestImporterDiagnosticsAreValueFree`.

Inject failpoints after source snapshot, each staged write, staged fsync, target
parent fsync, and rename. For each failpoint assert canonical absent/previous
valid and legacy digest/mode/mtime unchanged.

- [x] **Step 2: Run red**

```bash
go test ./internal/statemigration -count=1
```

- [x] **Step 3: Implement root pinning and promotion**

Open source and destination parents with `os.OpenRoot`. Lstat every component,
reject symlinks/special files, require expected ownership-safe modes, open then
re-stat with `os.SameFile`, and store inode/size/mtime snapshots. Acquire one
`.migration/v1/<owner>-<scope>.lock` with bounded stale-lock recovery.

The lock file is a persistent, identity-checked advisory guard: process exit
releases the kernel lock, so a stale on-disk file is reusable without unsafe
unlink races. Promotion uses OS-specific no-replace primitives and fsyncs the
destination parent before the source parent. Here, failure leaving canonical
state "absent" means the declared `TargetRel` remains absent; private
`.yhc/.migration/v1` administration directories and the persistent lock may
remain for a safe retry.

For `DirectoryTree`, traversal begins only at `SourceRel` and every entry is
passed to the owner validator. The importer never exposes a generic legacy-root
walk.

- [x] **Step 4: Run green and race**

```bash
go test ./internal/statemigration -count=1
go test -race ./internal/statemigration -run 'TestImporterConcurrentSinglePromotion' -count=1
```

- [x] **Step 5: Commit**

```bash
git add internal/statemigration
git commit -m "feat(state): add fail-closed artifact migration"
```

## Task 3: Migrate Non-Secret Settings And Keybindings

**Files:**

- Modify: `tools/config.go`
- Modify: `tools/config_test.go`
- Create: `tools/config_store.go`
- Create: `tools/config_store_{unix,windows,other}.go`
- Create: `tools/config_migration.go`
- Create: `tools/config_migration_test.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/keybindings/resolver.go`
- Modify: `internal/tui/keybindings/resolver_test.go`
- Create: `internal/tui/keybindings/migration.go`
- Create: `internal/tui/keybindings/migration_test.go`
- Modify: `cmd/yhc/cmd/migrate_state.go`

**Interfaces:**

- Canonical settings live at project `.yhc/settings.json` when project-scoped
  canonical state exists; otherwise `~/.yhc/settings.json`.
- Automatic settings import allowlist is `model`, `theme`,
  `permissions.defaultMode`, `permissions.allow`, `permissions.deny`,
  `compact.threshold`, `compact.strategy`, `memory.enabled`, and `provider`.
- `provider.apiKey`, `provider.baseURL`, `hooks.*`, unknown keys, and values
  matching credential patterns are never copied. They may be read from the
  exact legacy settings file through a value-redacted compatibility lookup
  only where current behavior already supports them.
- Keybindings schema owner remains
  `internal/tui/keybindings.Resolver.LoadUserBindings`. Canonical file is
  `~/.yhc/keybindings.json`; valid legacy
  `~/.eino-agent/keybindings.json` may be imported as one file.

- [x] **Step 1: Add red owner tests**

Create `TestSettingsMigrationCopiesOnlyNonSecretAllowlist`,
`TestSettingsMigrationRejectsUnknownAndCredentialLikeValues`,
`TestSettingsCanonicalCollisionWinsWithoutMerge`,
`TestKeybindingsMigrationUsesResolverValidation`, and
`TestTUILoadsCanonicalKeybindingsAfterImport`.

- [x] **Step 2: Run red**

```bash
go test ./tools ./internal/tui/keybindings ./internal/tui -run 'Test(SettingsMigration|SettingsCanonical|KeybindingsMigration|TUILoadsCanonical)' -count=1
```

- [x] **Step 3: Implement owner adapters**

Parse settings into a fresh allowlisted object; never copy the source file
wholesale. Reuse the keybinding parser/validator before staging. Register
`settings` and `keybindings` with the migration CLI. All new Config writes go
to canonical state; a destination file prevents auto-merge.

Keybindings staging rebuilds only the resolver-owned `bindings` envelope, so
parser-ignored fields cannot cross into canonical state. Config reads and
writes pin the selected root and settings file handles, reject link ambiguity,
and revalidate identity around mutation.

- [x] **Step 4: Run green and commit**

```bash
go test ./tools ./internal/tui/keybindings ./internal/tui -run 'Test(SettingsMigration|SettingsCanonical|KeybindingsMigration|TUILoadsCanonical)' -count=1
git add tools internal/tui cmd/yhc/cmd/migrate_state.go
git commit -m "feat(state): migrate non-secret settings and keybindings"
```

## Task 4: Migrate User And Project Memory

**Files:**

- Modify: `engine/memdir/paths.go`
- Modify: `engine/memdir/team.go`
- Modify: `engine/memdir/memdir.go`
- Modify: `engine/memdir/prompt.go`
- Modify: `engine/memdir/agent_snapshot.go`
- Modify: `engine/memdir/memdir_test.go`
- Modify: `engine/memdir/agent_snapshot_test.go`
- Create: `engine/memdir/migration.go`
- Create: `engine/memdir/migration_test.go`
- Modify: `internal/statemigration/importer.go`
- Modify: `internal/statemigration/importer_test.go`
- Modify: `internal/statemigration/roots.go`
- Modify: `internal/statemigration/snapshot.go`
- Modify: `cmd/yhc/cmd/migrate_state.go`

**Interfaces:**

- Default user memory root becomes `~/.yhc` and project/local agent memory
  becomes `<project>/.yhc/{agent-memory,agent-memory-local}`.
- User auto-memory project encoding is recalculated from the canonical project
  root; copied files never retain an old absolute base path.
- `YHC_REMOTE_MEMORY_DIR`, `YHC_CONFIG_DIR`,
  `YHC_MEMORY_PATH_OVERRIDE` and their aliases select exact non-migratable
  paths.
- Memory owners alone accept the real legacy writer's owner-controlled
  `0755`/`0644` inputs and normalize staged output to `0700`/`0600`; other
  owners retain the private legacy-mode policy.

- [x] **Step 1: Add red tests**

Create `TestMemoryDefaultsWriteOnlyYHCRoots`,
`TestMemoryMigrationRebasesProjectEncoding`,
`TestMemoryMigrationRejectsSymlinkUnknownEntryAndCollision`,
`TestMemoryExplicitOverridesAreNeverMigrated`, and
`TestMemoryMigrationLeavesLegacyTreeUnchanged`.

- [x] **Step 2: Run red**

```bash
go test ./engine/memdir -run 'Test(MemoryDefaults|MemoryMigration|MemoryExplicit)' -count=1
```

- [x] **Step 3: Implement exact memory-subtree adapters**

Name only `projects/<encoded>/memory`, `agent-memory`, and
`agent-memory-local` artifacts. Validate Markdown/log/metadata files and
budgets, reject symlinks/special files, and recalculate owner-defined internal
project paths in staging. Do not traverse the rest of either root.

- [x] **Step 4: Run green and commit**

```bash
go test ./engine/memdir -run 'Test(MemoryDefaults|MemoryMigration|MemoryExplicit|AutoMemory|AgentMemory)' -count=1
git add engine/memdir internal/statemigration cmd/yhc/cmd/migrate_state.go docs/superpowers/plans/2026-08-09-yhc-state-foundation.md
git commit -m "feat(state): migrate YHC memory owners"
```

## Task 5: Migrate Approvals, Project History, And Review Audit

**Files:**

- Modify: `engine/permission/setup.go`
- Modify: `engine/engine.go`
- Modify: `engine/permission/approvals.go`
- Modify: `engine/permission/approvals_test.go`
- Create: `engine/permission/approvals_migration.go`
- Create: `engine/permission/approvals_migration_test.go`
- Create: `engine/permission/approvals_path.go`
- Modify: `engine/permission/review_audit.go`
- Modify: `engine/permission/review_audit_test.go`
- Create: `engine/permission/review_audit_migration.go`
- Create: `engine/permission/review_audit_migration_test.go`
- Modify: `engine/session_restore.go`
- Modify: `internal/tui/history.go`
- Create: `internal/tui/history_test.go`
- Modify: `internal/statemigration/importer.go`
- Modify: `internal/statemigration/importer_test.go`
- Create: `internal/statemigration/runtime_store.go`
- Create: `internal/statemigration/runtime_store_test.go`
- Modify: `cmd/yhc/cmd/migrate_state.go`
- Modify: `cmd/yhc/cmd/migrate_state_test.go`

**Interfaces:**

- Canonical approvals are `<project>/.yhc/approvals.json`. Import only valid
  persistent, parameter-scoped entries; reject credential-shaped commands,
  unsafe/out-of-project paths, malformed timestamps, and session-scoped rows.
- Canonical plain TUI history is `<project>/.yhc/history`. Continue current
  `~/.claude` JSONL history compatibility in place, but stop writing new plain
  entries to `.eino-agent/history`.
- Canonical review audit is
  `~/.yhc/permission-review-audit/v1` unless an exact override is selected.
  Import requires the audit owner's cross-process quiescence/lock protocol and
  preserves rotation/tail-recovery semantics.
- Audit uses the optional non-mutating `AcquireSourceLease` seam. Import
  acquires the existing audit coordination file, re-captures the source, and
  holds the lease through staging and promotion; inspect remains non-mutating
  and only reports the snapshot-visible quiescence state.
- Default runtime writers reuse the importer root-pinning boundary through a
  canonical store handle. Pre-existing or concurrently replaced `.yhc`
  roots/intermediate directories fail closed; rooted writes cannot be
  redirected outside the originally pinned canonical state tree.

- [x] **Step 1: Add red tests**

Create `TestApprovalMigrationPreservesScopeWithoutCredentialCopy`,
`TestApprovalMigrationRejectsUnsafePathAndDestinationCollision`,
`TestHistoryWritesCanonicalAndLeavesClaudeCompatibilityInPlace`,
`TestHistoryMigrationPreservesOrderAndBounds`,
`TestReviewAuditMigrationRequiresQuiescence`,
`TestReviewAuditMigrationPreservesRedactionRotationAndRecovery`, and
`TestCredentialStoresAreNeverEnumeratedOrCopied`.

- [x] **Step 2: Run red**

```bash
go test ./engine/permission ./internal/tui ./engine/mcp ./engine/auth -run 'Test(ApprovalMigration|HistoryWritesCanonical|HistoryMigration|ReviewAuditMigration|CredentialStores)' -count=1
```

- [x] **Step 3: Implement adapters and canonical wiring**

Use the existing approval schema validator and audit secure-store patterns.
Treat any load/validation failure as a refused import instead of the current
silent approval-load behavior. Keep `~/.claude/mcp_oauth_tokens.json`,
`~/.claude/credentials.json`, Claude settings/hooks/commands/agents/skills, and
`.mcp.json` out of discovery and diagnostics.

- [x] **Step 4: Run green and commit**

```bash
go test ./engine/permission ./internal/tui ./engine/mcp ./engine/auth -run 'Test(ApprovalMigration|HistoryWritesCanonical|HistoryMigration|ReviewAuditMigration|CredentialStores|ReviewAuditStore)' -count=1
git add engine/permission engine/session_restore.go internal/tui/history.go internal/tui/history_test.go cmd/yhc/cmd/migrate_state.go
git commit -m "feat(state): migrate approvals history and review audit"
```

## Task 6: Verify The Plain-State Safety Envelope

- [x] Run focused and race suites:

```bash
go test ./internal/statepath ./internal/statemigration ./tools ./engine/memdir ./engine/permission ./internal/tui -count=1
go test -race ./internal/statemigration ./engine/permission -run 'Test(ImporterConcurrent|ReviewAuditMigration)' -count=1
```

- [ ] Run repository gates:

```bash
make fmt
make lint
make test
make build
make docs-check
make publication-check-policy
git diff --check
```

- [x] Run a real-binary temporary-home fixture that contains eligible legacy
  settings/keybindings/memory/approvals/history/audit plus credentials and
  `.claude` data. Inspect, apply one owner at a time, restart YHC, and assert:
  canonical reads/writes succeed, collisions refuse, credential stores remain
  exact, and a recursive digest of all legacy artifacts is unchanged.

  The durable subprocess fixture lives in
  `cmd/yhc/cmd/migrate_state_binary_e2e_test.go`; owner runtime tests remain the
  schema/read-write oracle while the restarted binary proves root resolution,
  one-owner admission, destination refusal, and legacy byte/mode/mtime
  immutability.

> **Publication dependency:** the ordinary repository gates above are green,
> but `make publication-check-policy` intentionally remains fail-closed while
> Publication Readiness Task 3 still has unresolved per-path provenance
> decisions. Do not close the repository-gates checkbox or weaken the policy;
> rerun it after that task resolves every path.

This plan is not complete if the session picker can write a legacy transcript,
if cron was imported, or if a worktree moved; those are state-continuity
boundaries.
