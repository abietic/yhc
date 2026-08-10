# YHC State Continuity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `$iteration-workflow` and `$runtime-depth-change` to execute and close this
> state-sensitive plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve recoverable session, transcript, WorkBoard, scheduler, and
owned-worktree continuity while moving all new default writes to YHC roots and
never mutating legacy state or moving a live checkout.

**Architecture:** Session migration is a recoverable cross-root transaction
owned by `engine/session`: it snapshots one legacy transcript plus its exact
WorkBoard set, stages canonical files, writes a project bundle marker, then
commits the canonical user catalog entry under a durable journal. Resume cannot
write until marker and catalog agree. Cron remains read-only unless the user
explicitly attests the archive producer is stopped and stability checks pass.
Worktrees expose read-only legacy inspection; only newly created worktrees use
YHC roots.

**Tech Stack:** Go 1.26.5, transcript JSONL recovery, WorkBoard secure store,
session catalog locks, `os.OpenRoot`, durable journals, atomic rename/fsync,
Cobra/TUI resume flows, cron parsing, Git worktree identity, race/failpoint/E2E
tests, and Makefile gates.

**Status:** active-plan
**Created:** 2026-08-09
**Plan state:** Tasks 1-6 complete; downstream publication clearance pending

> **Ownership:** session bundle, cron, and worktree rows of the
> [YHC state compatibility matrix](../specs/2026-08-09-yhc-public-release-design.md#state-owners-migrate-their-own-artifacts).
> It consumes the statepath/import foundation and does not redesign runtime
> traversal.

## Global Constraints

- Canonical transcript root is `<project>/.yhc/transcripts` and canonical
  catalog is `~/.yhc/session-roots.json` unless an exact environment override
  is selected. New transcript files are `0600` under `0700` directories.
- The implicit legacy session union is limited to
  `~/.eino-agent/session-roots.json`. `~/.claude/projects` is not a third
  implicit discovery source; an explicit `CLAUDE_TRANSCRIPT_DIR` remains an
  opt-in compatibility path only when no project path is supplied.
- A legacy session is discoverable read-only. Any resume path that can append a
  transcript, mutate WorkBoard, update catalog, or persist recovery state must
  first complete the bundle transaction.
- One session bundle is exactly
  `<session>.jsonl`,
  `<session>.workboard-v2.json`,
  `<session>.workboard-authority-v1.json`, and
  `<session>.workboard-legacy-backup-v1.json` when those WorkBoard files are
  present, plus one canonical catalog entry. Missing optional WorkBoard files
  are interpreted only by the existing WorkBoard authority rules.
- No existing protocol proves a private archived process has stopped appending
  a transcript. Automatic import therefore refuses. Interactive import requires
  explicit `--confirm-legacy-stopped` plus two stable pinned snapshots; this is
  an attested quiescence operation, not an inferred live-process guarantee.
- The complete legacy bundle remains byte/mode/mtime-identical on failure and
  success. If an archived process later writes it contrary to the user's
  attestation, YHC does not merge the divergence into canonical state.
- A project bundle marker is not enough to resume: canonical marker, files,
  hashes, user journal, and catalog entry must agree. Recovery completes or
  refuses; it never opens a partial bundle for writes.
- Existing transcript tail recovery and WorkBoard marker semantics remain
  authoritative. Migration validates through those owners rather than parsing a
  looser duplicate schema.
- Cron automatic import is forbidden. Explicit import requires the user
  attestation, no live legacy scheduler PID, and two stable pinned snapshots.
  Malformed legacy cron data is an error, not the runtime's historical
  “empty-list” fallback.
- New cron writes are atomic `0600` under `.yhc`. This hardens storage without
  changing task parsing, jitter, firing, recurrence, or scheduling order.
- Never move, copy, delete, clean, or reparent a legacy managed worktree.
  Legacy records are inspection-only. `Creating`, `Ready`, `Retained`,
  `Removing`, `CleanupFailed`, `Failed`, dirty, or unverifiable records are
  never considered quiescent. Only a durable `Removed` record is terminal;
  `Failed` can still retain a checkout created before a later validation
  failure.
- Legacy work continues with the archived binary. Explicit legacy worktree
  adoption/retirement is not required for the public root and needs a later
  accepted plan.

---

## Locked Session Transaction

```go
type ImportPhase string

const (
	PhasePrepared        ImportPhase = "prepared"
	PhaseFilesPromoted   ImportPhase = "files_promoted"
	PhaseMarkerCommitted ImportPhase = "marker_committed"
	PhaseCatalogCommitted ImportPhase = "catalog_committed"
)

type BundleFile struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Mode         uint32 `json:"mode"`
}

type ImportJournal struct {
	Version           int         `json:"version"`
	SessionID         string      `json:"session_id"`
	LegacyTranscript  string      `json:"legacy_transcript"`
	CanonicalProject  string      `json:"canonical_project"`
	CanonicalCatalog  string      `json:"canonical_catalog"`
	Files             []BundleFile `json:"files"`
	Phase             ImportPhase `json:"phase"`
}

type LegacySessionTarget struct {
	SessionID     string
	CWD           string
	TranscriptDir string
	ReadOnly      bool
	NeedsImport   bool
}

func InspectLegacySessions(context.Context, string) ([]LegacySessionTarget, error)
func ImportSessionForResume(context.Context, ImportRequest) (SessionRoot, error)
func RecoverSessionImports(context.Context, statepath.Roots) error
```

Journal path:
`~/.yhc/session-imports/v1/<repository-key>/<session-id>.json`.
Project commit marker:
`<project>/.yhc/transcripts/.imports/v1/<session-id>.json`.

Commit sequence:

1. acquire canonical user-journal, catalog, and project-session locks in that
   order;
2. pin and validate two identical legacy snapshots after explicit attestation;
3. stage and validate transcript and WorkBoard artifacts;
4. promote all canonical files and persist `PhaseFilesPromoted`;
5. write/fsync the project marker and persist `PhaseMarkerCommitted`;
6. atomically add the canonical catalog entry and persist
   `PhaseCatalogCommitted`; and
7. expose the canonical target to resume only after a full reread verifies all
   hashes and both commit records.

Recovery uses the journal and hashes. Before the marker it may remove only
YHC-created staged/partial canonical files recorded in the journal. After the
marker it completes the catalog or reports a deterministic collision; it never
changes legacy state.

## Task 1: Characterize Legacy Discovery And Switch New Defaults

**Files:**

- Modify: `engine/engine.go`
- Modify: `engine/inspection_administration.go`
- Modify: `engine/session/catalog.go`
- Modify: `engine/session/listing.go`
- Modify: `engine/session/listing_test.go`
- Modify: `engine/session/query.go`
- Modify: `engine/session/query_test.go`
- Modify: `engine/session_administration.go`
- Modify: `engine/session_service.go`
- Modify: `engine/session_service_test.go`
- Modify: `cmd/yhc/cmd/root.go`
- Modify: `cmd/yhc/cmd/sessions.go`
- Modify: `cmd/yhc/cmd/sessions_test.go`
- Modify: `cmd/yhc/cmd/p43_0_characterization_test.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/command_discovery.go`
- Modify: `server/acp/agent_protocol_test.go`
- Modify: `server/acp/agent_session_test.go`
- Modify: `server/acp/agent_test.go`
- Modify: `server/acp/command_discovery_test.go`
- Modify: `server/acp/mcp_session_lifecycle_unix_test.go`
- Modify: `server/acp/query_kernel_selection_test.go`
- Modify: `server/acp/replay_test.go`
- Modify: `scripts/e2e/harness_test.go`

- [x] **Step 1: Add red default/discovery tests**

Create `TestNewSessionsWriteOnlyYHCTranscriptAndCatalogRoots`,
`TestLegacyCatalogRowsAreDiscoverableReadOnly`,
`TestCanonicalAndLegacyCatalogDeduplicateByRepositoryAndSession`,
`TestLegacySessionMutationRequiresImport`, and
`TestExplicitCatalogOverrideRemainsExactAndNonMigratable`.

Canonical rows win duplicate identity. A legacy-only row exposes session ID,
timestamp, repository identity, and read-only/import-required status without
opening it for mutation.

- [x] **Step 2: Run red**

```bash
go test ./engine/session ./engine ./cmd/yhc/cmd -run 'Test(NewSessionsWriteOnlyYHC|LegacyCatalogRows|CanonicalAndLegacyCatalog|LegacySessionMutation|ExplicitCatalogOverride)' -count=1
```

- [x] **Step 3: Switch defaults and add read-only union**

Use `statepath` roots. Load the canonical catalog normally and the legacy
default catalog through a read-only query input. Do not register, refresh, or
repair legacy catalog entries. Existing explicit catalog paths remain the sole
catalog and receive no default-root import.

- [x] **Step 4: Run green and commit**

```bash
go test ./engine/session ./engine ./cmd/yhc/cmd -run 'Test(NewSessionsWriteOnlyYHC|LegacyCatalogRows|CanonicalAndLegacyCatalog|LegacySessionMutation|ExplicitCatalogOverride|SessionAPIDefaults|SessionCursor|QuerySessions)' -count=1
git add \
  cmd/yhc/cmd/p43_0_characterization_test.go cmd/yhc/cmd/root.go \
  cmd/yhc/cmd/sessions.go cmd/yhc/cmd/sessions_test.go \
  docs/migration/plans/p20-plan-mode-interaction.md \
  docs/superpowers/plans/2026-08-09-yhc-state-continuity.md \
  engine/engine.go engine/inspection_administration.go engine/session \
  engine/session_administration.go engine/session_service.go \
  engine/session_service_test.go scripts/e2e/harness_test.go server/acp
git commit -m "feat(session): discover legacy state without writes"
```

Closeout also passed the focused race checks, ACP and E2E regressions,
`make fmt`, `make lint`, `make test`, `make build`, and `make docs-check`.

## Task 2: Implement The Recoverable Session Bundle

**Files:**

- Create: `engine/session/migration.go`
- Create: `engine/session/migration_journal.go`
- Create: `engine/session/migration_test.go`
- Create: `engine/session/migration_failpoint_test.go`
- Modify: `engine/session/catalog.go`
- Modify: `engine/transcript/persist.go`
- Modify: `engine/transcript/persist_test.go`
- Create: `internal/statemigration/fileset.go`
- Create: `internal/statemigration/fileset_test.go`
- Modify: `internal/statemigration/runtime_store.go`

- [x] **Step 1: Add transaction red tests**

Create:

- `TestImportSessionBundleCommitsTranscriptWorkBoardAndCatalogTogether`;
- `TestResumeLegacyCatalogIsReadOnlyUntilBundlePromotion`;
- `TestSessionImportRequiresExplicitAttestationAndStableSnapshot`;
- `TestSessionImportRejectsSymlinkReplacementCollisionAndUnsafeMode`;
- `TestSessionImportFailureIsRestartSafeAtEveryPhase`;
- `TestSessionImportConcurrentSingleWinner`;
- `TestSessionImportPreservesTruncatedTailRecovery`;
- `TestSessionImportPreservesWorkBoardAuthorityAndRevision`; and
- `TestSessionImportLeavesLegacyBundleUnchanged`.

Inject failure after every journal write, file rename, fsync, marker write, and
catalog replacement. Attempt resume after each failure and assert no writable
engine is returned.

- [x] **Step 2: Run red**

```bash
go test ./engine/session ./engine/transcript ./engine/internal/workboard -run 'Test(SessionImport|ImportSessionBundle|ResumeLegacyCatalog)' -count=1
```

- [x] **Step 3: Implement the journal and owner validation**

Use the state importer for pinning/staging but keep this cross-owner commit
protocol in `engine/session`. Validate transcript by loading recoverable
context, validate WorkBoard through its secure store, require session IDs to
match every artifact, rebase canonical project/transcript paths, and create
canonical transcripts as `0600`.

Catalog registration gains an internal transaction hook that runs while its
existing lock is held; ordinary `RegisterSessionRoot` behavior stays unchanged.

- [x] **Step 4: Run green and race**

```bash
go test ./engine/session ./engine/transcript ./engine/internal/workboard -run 'Test(SessionImport|ImportSessionBundle|ResumeLegacyCatalog|Persist|Artifact)' -count=1
go test -race ./engine/session -run 'TestSessionImportConcurrentSingleWinner' -count=1
```

- [x] **Step 5: Commit**

```bash
git add engine/session engine/transcript/persist.go engine/transcript/persist_test.go engine/internal/workboard/secure_store.go engine/internal/workboard/secure_store_test.go
git commit -m "feat(session): import legacy bundles transactionally"
```

Closeout reused transcript recovery and WorkBoard `Store.Inspect` as the owner
validators, so those WorkBoard files required no production change. The
transaction adds a bounded exact-file-set prepare seam, durable journal/marker
phases, no-replace promotion, catalog serialization, and recovery from the
journal-owned stage/target union without reopening legacy state. Every declared
failure point, the concurrent winner/catalog cases, focused package and race
suites, `make fmt`, `make lint`, `make test`, `make build`, and
`make docs-check` passed. `make lint-new` still reports the ten existing
publication-tool findings owned by the later Publication Readiness plan; no
Task 2 file appears in that finding set.

## Task 3: Gate Every Resume Entrypoint On Bundle Commit

**Files:**

- Create: `engine/session/admission.go`
- Create: `engine/session/admission_test.go`
- Modify: `engine/session/listing.go`
- Modify: `engine/session/query.go`
- Modify: `engine/session_service.go`
- Modify: `engine/session_service_test.go`
- Modify: `engine/restore_staging_test.go`
- Modify: `cmd/yhc/cmd/headless.go`
- Modify: `cmd/yhc/cmd/headless_goal.go`
- Modify: `cmd/yhc/cmd/root.go`
- Modify: `cmd/yhc/cmd/sessions.go`
- Modify: `cmd/yhc/cmd/sessions_test.go`
- Modify: `cmd/yhc/cmd/migrate_state.go`
- Modify: `cmd/yhc/cmd/migrate_state_test.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/resume_dialog.go`
- Modify: `internal/tui/resume_dialog_test.go`
- Modify: `internal/tui/resume_session_view.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/agent_session_test.go`
- Modify: `server/acp/streaming.go`
- Modify: `docs/architecture/state/sessions.md`
- Modify: `docs/architecture/tui/contracts/sessions.md`
- Modify: `docs/guides/sessions-and-transcripts.md`

**Interfaces:**

- Interactive CLI/TUI may ask for the explicit legacy-stopped attestation and
  then call `ImportSessionForResume`.
- Noninteractive CLI and ACP return typed
  `legacy_session_import_required` with session ID only and no automatic write.
- `yhc migrate-state apply --owner session --session ID
  --confirm-legacy-stopped` performs the same operation without model/runtime
  initialization.

- [x] **Step 1: Add entrypoint red tests**

Create `TestInteractiveResumeImportsOnlyAfterConfirmation`,
`TestNonInteractiveResumeReturnsImportRequiredWithoutWrites`,
`TestACPResumeReturnsImportRequiredWithoutPrivateMigration`,
`TestSessionMigrationCommandDoesNotInitializeRuntime`, and
`TestCommittedCanonicalResumePreservesExistingRecoveryOrdering`.

- [x] **Step 2: Run red**

```bash
go test ./engine ./cmd/yhc/cmd ./internal/tui ./server/acp -run 'Test(InteractiveResume|NonInteractiveResume|ACPResume|SessionMigrationCommand|CommittedCanonicalResume)' -count=1
```

- [x] **Step 3: Wire one gate before engine construction**

Resolve canonical/legacy identity before recorder, WorkBoard, approvals, hooks,
or QueryEngine construction. A failed/refused import returns before any
canonical or legacy mutation. After import, rerun the normal canonical restore
path; do not create a second legacy-specific engine path.

- [x] **Step 4: Run green and commit**

```bash
go test ./engine ./cmd/yhc/cmd ./internal/tui ./server/acp -run 'Test(InteractiveResume|NonInteractiveResume|ACPResume|SessionMigrationCommand|CommittedCanonicalResume|Session|Resume|Recovery)' -count=1
git add engine/session_restore.go engine/restore_staging.go engine/session_service.go engine/session_service_test.go cmd/yhc/cmd internal/tui/app.go internal/tui/resume_dialog_test.go server/acp/agent.go server/acp/agent_session_test.go
git commit -m "feat(session): gate resume on canonical bundle commit"
```

Closeout added a read-only, preconstruction admission seam for startup,
headless, sessions CLI, TUI, ACP resume, and ACP load. Interactive TUI and the
explicit session migration command can attest and import, then re-enter the
ordinary canonical path; noninteractive callers receive only the typed session
ID projection. Admission verifies the complete journal, marker, file manifest,
and catalog commit before the default `.yhc` bundle can resume, while preserving
existing explicit non-default transcript stores. Focused and race suites,
`make fmt`, `make lint`, `make test`, `make build`, and `make docs-check` passed,
and an independent review closed all provenance and compatibility findings.
`make lint-new` still reports only the ten existing publication-tool findings;
no Task 3 file appears in that finding set.

## Task 4: Move Cron Defaults With Explicit Legacy Quiescence

**Files:**

- Modify: `engine/cron/cron.go`
- Modify: `engine/cron/lock.go`
- Modify: `engine/cron/cron_test.go`
- Create: `engine/cron/lock_test.go`
- Create: `engine/cron/migration.go`
- Create: `engine/cron/migration_test.go`
- Create: `engine/cron/storage.go`
- Modify: `internal/statemigration/fileset_test.go`
- Modify: `cmd/yhc/cmd/migrate_state.go`
- Modify: `cmd/yhc/cmd/migrate_state_test.go`
- Modify: `docs/architecture/code-map.md`
- Modify: `docs/architecture/platform/runtime-services.md`

- [x] **Step 1: Add cron red tests**

Create `TestCronDefaultsWriteOnlyYHCRoot`,
`TestCronLegacyInspectIsReadOnlyAndValueFree`,
`TestCronAutomaticImportAlwaysRefuses`,
`TestCronExplicitImportRequiresStoppedAttestationDeadPIDAndStableSnapshot`,
`TestCronMigrationRejectsMalformedCollisionAndReplacement`,
`TestCronWritesAreAtomicPrivateAndPreserveSchedulingSemantics`, and
`TestCronMigrationLeavesLegacyBytesUnchanged`.

- [x] **Step 2: Run red**

```bash
go test ./engine/cron ./cmd/yhc/cmd -run 'Test(CronDefaults|CronLegacy|CronAutomatic|CronExplicit|CronMigration|CronWrites)' -count=1
```

- [x] **Step 3: Implement conservative import**

Use canonical `.yhc/scheduled_tasks.json` and
`.yhc/scheduler.lock` for new runtime. Legacy inspect parses strictly but emits
only task count/status. Apply requires `--confirm-legacy-stopped`, rejects a
live legacy PID, then takes two pinned digest/stat snapshots separated by a
bounded stability interval. It never creates/removes the legacy lock.

Write canonical cron JSON through a `0600` temp file, fsync, rename, and parent
fsync. Keep parse, jitter, recurrence, and firing order unchanged.

- [x] **Step 4: Run green and commit**

```bash
go test ./engine/cron ./cmd/yhc/cmd -run 'Test(CronDefaults|CronLegacy|CronAutomatic|CronExplicit|CronMigration|CronWrites|ParseCron|Scheduler)' -count=1
git add engine/cron cmd/yhc/cmd/migrate_state.go cmd/yhc/cmd/migrate_state_test.go
git commit -m "feat(cron): migrate only quiesced legacy schedules"
```

Closeout moved only new cron task and lock writes to private `.yhc` files and
replaced direct writes with pinned, fsynced temporary-file promotion. The
provider-free `cron` migration owner performs strict read-only inspection,
projects only status/count, requires stopped-producer attestation, treats an
unknown PID as live, and holds an exact task/lock snapshot across a bounded
stability interval before the generic no-replace importer may promote the task
file. Legacy bytes, modes, mtimes, and lock ownership remain untouched.
Focused package and race suites, the same-store no-replace regression, all four
Makefile gates, and `make docs-check` passed. Independent review reported no
findings; Windows and Linux builds passed, with native Windows filesystem
execution retained as a CI residual risk. `make lint-new` still reports only
the ten existing publication-tool findings and no Task 4 file.

## Task 5: Preserve Legacy Worktrees As Inspection-Only

**Files:**

- Modify: `engine/worktree/service.go`
- Modify: `engine/worktree/git.go`
- Modify: `engine/worktree/store.go`
- Modify: `engine/worktree/service_test.go`
- Create: `engine/worktree/legacy.go`
- Create: `engine/worktree/legacy_test.go`
- Modify: `engine/worktree_lifecycle_test.go`
- Modify: `tools/agent_runner_test.go`
- Modify: `cmd/yhc/cmd/migrate_state.go`
- Modify: `cmd/yhc/cmd/migrate_state_test.go`

**Interfaces:**

- `NewService` creates records/trees under
  `<project>/.yhc/worktrees/v1` and branch prefix `yhc/worktree/`.
- `InspectLegacy(projectRoot)` reads
  `.eino-agent/worktrees/v1/records` without recovery, cleanup, adoption,
  checkout, branch mutation, or record writes.
- The migration CLI supports `inspect --owner worktree` but has no `apply`
  handler for worktrees in this release.

- [x] **Step 1: Add worktree red tests**

Create `TestNewWorktreesUseYHCRootAndBranchPrefix`,
`TestInspectLegacyWorktreesIsReadOnly`,
`TestWorktreeMigrationRefusesEveryLiveOrUnverifiableRecord`,
`TestLegacyInspectionRejectsSymlinkAndRepositoryIdentityMismatch`, and
`TestMigrateStateHasNoWorktreeApplyOperation`.

- [x] **Step 2: Run red**

```bash
go test ./engine/worktree ./engine ./cmd/yhc/cmd -run 'Test(NewWorktreesUseYHC|InspectLegacyWorktrees|WorktreeMigrationRefuses|LegacyInspection|MigrateStateHasNoWorktree)' -count=1
```

- [x] **Step 3: Implement canonical creation and read-only legacy view**

Reuse `Discover` validation without calling `RecoverForContinuation`. Return
value-free status and record IDs only. Keep old private branches and trees
attached to the archive clone. Do not add an adoption shortcut.

- [x] **Step 4: Run green and commit**

```bash
go test ./engine/worktree ./engine ./cmd/yhc/cmd -run 'Test(NewWorktreesUseYHC|InspectLegacyWorktrees|WorktreeMigrationRefuses|LegacyInspection|MigrateStateHasNoWorktree|Discover|Recover|Store|Service)' -count=1
git add engine/worktree engine/worktree_lifecycle.go cmd/yhc/cmd/migrate_state.go cmd/yhc/cmd/migrate_state_test.go
git commit -m "feat(worktree): keep legacy worktrees inspection-only"
```

Closeout moved only newly created records, trees, and branches to `.yhc` and
`yhc/worktree/`. Legacy discovery now uses pinned, bounded record reads and an
explicit `ReadOnlyGit` boundary; it projects only record IDs plus
`active`/`dirty`/`unavailable`/`terminal`, never calls recovery or cleanup, and
exposes no CLI `apply` handler. Only `Removed` is terminal: independent review
identified that `Failed` may follow a successful `git worktree add`, so both
missing and live failed records fail closed as `unavailable`. Focused and race
suites, all four Makefile gates, and `make docs-check` passed. `make lint-new`
still reports only the ten existing publication-tool findings and no Task 5
file.

## Task 6: Run State Continuity Acceptance

- [x] Run focused/race suites:

```bash
go test ./engine/session ./engine/transcript ./engine/internal/workboard ./engine/cron ./engine/worktree ./engine ./cmd/yhc/cmd ./internal/tui ./server/acp -count=1
go test -race ./engine/session ./engine/internal/workboard ./engine/cron ./engine/worktree -count=1
```

- [x] Run the real-binary E2E fixture with a legacy catalog, recoverable
  truncated transcript, WorkBoard authority set, approvals, schedule, and live
  owned worktree. Prove:

  - legacy rows are discoverable without writes;
  - noninteractive resume refuses before runtime construction;
  - confirmed session import survives every crash phase and resumes once;
  - canonical continuation mutates only `.yhc`;
  - cron auto-import refuses and explicit stopped import preserves tasks;
  - legacy worktree status is visible but no checkout/record/branch moves; and
  - recursive legacy digests/modes/mtimes are identical before and after.

- [x] Run repository/publication gates:

```bash
make fmt
make lint
make test
make build
make docs-check
make test-contract
make test-e2e
make publication-check-policy
git diff --check
```

Do not declare this plan complete from unit tests alone; the session bundle and
legacy-worktree refusal require the real-binary E2E evidence.

Closeout uses two explicit evidence layers. The real `yhc` binary fixture
proves one joint ordinary path across legacy discovery, preconstruction resume
refusal, session/WorkBoard import and canonical continuation, approvals, cron,
and a dirty live worktree while recursively preserving every legacy byte,
mode, mtime, branch, and checkout. The package-level
`TestSessionImportFailureIsRestartSafeAtEveryPhase` matrix separately injects
every internal crash phase; no production fault-injection control was added.

Acceptance exposed and fixed one post-import contract defect: catalog
timestamps and canonical owner files are mutable after commit, so admission
now keeps journal/marker identity immutable while revalidating current
transcript and WorkBoard state through their owners. The canonical catalog is
read twice through its pinned private store, which rejects non-`0600`, linked,
or replaced files. Focused tests, race tests, `make fmt`, `make lint`,
`make test`, `make build`, `make docs-check`, `make test-contract`,
`make test-e2e`, `git diff --check`, and an independent remediation re-review
passed on the final Task 6 diff. `make publication-check-policy` was also run
and remains red only because `.agents/skill-runtime/skill_log.py` retains its
pre-existing `unresolved` publication decision. That classification belongs to
the downstream publication-readiness plan and remains a public-release blocker;
it is not represented as a passing Task 6 gate.
