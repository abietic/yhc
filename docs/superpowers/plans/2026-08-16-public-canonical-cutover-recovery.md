# Public Canonical Cutover Recovery Implementation Plan

> **For agentic workers:** REQUIRED PROJECT SKILL: use
> `$iteration-workflow` to implement this plan task by task. Use the checkbox
> steps for tracking, preserve one reviewable behavior change per commit, and
> run committed-tree evidence before push.

**Goal:** Build a fail-closed, read-only capture and verification tool that
proves the retained private-history checkout can be archived without losing a
dirty path, stash, ref, worktree registration, or active process occupant.

**Architecture:** A dedicated `scripts/cutover_recovery` command owns a private
schema-v1 recovery envelope. Capture reads Git and process state, applies an
explicit private cutover input, and atomically writes canonical JSON;
verification re-reads live state for `pre-move`, `post-move`, or `rollback`
without moving, repairing, cleaning, or publishing anything. Existing
`scripts/worktree_audit`, publication tooling, and iteration evidence remain
separate owners.

**Tech Stack:** Go 1.26.5, Git porcelain, `lsof` on Darwin, canonical JSON,
SHA-256, GNU Make, repository iteration evidence.

**Status:** active-plan
**Created:** 2026-08-16
**Plan state:** Ready for implementation from current public `origin/master`

> **Ownership:** implementation steps for the private recovery envelope,
> read-only inventory capture, and archive-phase verification defined by the
> [public canonical cutover design](../specs/2026-08-15-public-canonical-cutover-design.md)

## Global Constraints

- The command is read-only with respect to Git, source paths, worktrees,
  stashes, remotes, processes, and archive paths; it may write only the
  explicitly requested manifest output through an atomic replace.
- The output path must be outside both the public checkout and every private
  move root. A manifest inside either tree is rejected.
- No file body, prompt, transcript, credential value, process command line,
  environment variable, stash content, or Git object body enters the manifest.
- SHA-256 is calculated only for a regular file whose exact classification
  rule says `checksum_policy: sha256`. `omit_sensitive` records no derived
  digest.
- Every captured ref, worktree, dirty path, stash, and process occupant has one
  stable `record_id`; refs, worktrees, dirty paths, and stashes require exactly
  one classification. Duplicate IDs, missing or extra classifications, unknown
  fields, and aggregate mismatches fail closed. Process occupants are immediate
  move blockers rather than retained-item classifications.
- The `refs`, `worktrees`, `dirty_paths`, `stashes`, and `classifications`
  arrays describe only the private-history repository. The public repository
  contributes its root, HEAD, branch, common directory, and normalized origin
  identity, not a second recovery inventory.
- `pre-move` requires zero process occupants, a collision-free archive mapping,
  exact public/private remote identities, and no unresolved classification.
- Source roots and destination roots must each be pairwise non-overlapping;
  neither set may contain an ancestor/descendant pair. The main-checkout
  destination must equal `--archive-root`.
- `post-move` and `rollback` compare live HEAD, branch/detached identity,
  porcelain status, dirty paths, stash identities, and Git common-directory
  ownership with the frozen mapping. They never run `git worktree repair`.
- A registered prunable worktree whose root is absent is recorded and
  classified but has no archive mapping. It remains present in exact Git
  registration comparisons and is never repaired or pruned.
- `pre-rollback` requires zero process occupants beneath archive-side roots and
  validates the reverse mapping and original-path collisions before any reverse
  rename.
- Desktop PR #14 and both public feature branches are outside this tool's
  mutation surface.
- The command compiles on every supported Go target, but live process capture
  is admitted only on Darwin. Other platforms and a missing or unreadable
  `lsof` binary fail closed.

## File Map

| File | Responsibility |
|---|---|
| `scripts/cutover_recovery/model.go` | Schema-v1 types, enums, stable record IDs, and aggregate validation |
| `scripts/cutover_recovery/manifest.go` | Strict JSON decoding, canonical sealing, private atomic output |
| `scripts/cutover_recovery/git.go` | Read-only Git allowlist and ref/worktree/dirty/stash capture |
| `scripts/cutover_recovery/process.go` | Exact-root `lsof` execution and privacy-minimized occupancy parsing |
| `scripts/cutover_recovery/verify.go` | Live `pre-move`, `post-move`, and `rollback` comparison |
| `scripts/cutover_recovery/main.go` | Two-command CLI and redacted result rendering |
| `docs/contributing/public-canonical-cutover.md` | Operator-owned capture, move, repair, rollback, and stop rules |

---

### Task 1: Freeze the strict recovery envelope

**Files:**

- Create: `scripts/cutover_recovery/model.go`
- Create: `scripts/cutover_recovery/manifest.go`
- Create: `scripts/cutover_recovery/manifest_test.go`

**Interfaces:**

- Consumes: RFC 3339 capture time, canonical absolute paths, Git object IDs,
  explicit archive mappings, and classification rules.
- Produces: `manifest`, `cutoverInput`, `validateManifest(manifest,
  validationPhase) error`, `canonicalPayload(manifest) ([]byte, error)`,
  `sealManifest(manifest) (manifest, error)`, `readManifest(string) (manifest,
  error)`, and `writeManifestAtomic(string, manifest) error`.

- [ ] **Step 1: Write strict-decoding and canonical-checksum tests**

Create table tests that require this public shape:

```go
type manifest struct {
	SchemaVersion  int                    `json:"schema_version"`
	CapturedAt     string                 `json:"captured_at"`
	Public         repositoryRecord       `json:"public"`
	Private        repositoryRecord       `json:"private"`
	ArchiveMapping  []archiveMappingRecord  `json:"archive_mapping"`
	Refs            []refRecord             `json:"refs"`
	Worktrees       []worktreeRecord        `json:"worktrees"`
	DirtyPaths      []dirtyPathRecord        `json:"dirty_paths"`
	Stashes         []stashRecord            `json:"stashes"`
	Processes       []processRecord          `json:"processes"`
	Classifications []classificationRecord `json:"classifications"`
	Aggregates     aggregateRecord         `json:"aggregates"`
	Checksum       string                  `json:"checksum"`
}

type repositoryRecord struct {
	Role             string `json:"role"`
	Root             string `json:"root"`
	Head             string `json:"head"`
	Branch           string `json:"branch,omitempty"`
	Detached         bool   `json:"detached"`
	CommonDir        string `json:"common_dir"`
	OriginRepository string `json:"origin_repository"`
}

type archiveMappingRecord struct {
	RecordID        string `json:"record_id"`
	WorktreeRecordID string `json:"worktree_record_id"`
	Kind            string `json:"kind"`
	Source          string `json:"source"`
	Destination     string `json:"destination"`
}

type refRecord struct {
	RecordID      string `json:"record_id"`
	RepositoryRole string `json:"repository_role"`
	RefName       string `json:"ref_name"`
	ObjectID      string `json:"object_id"`
}

type worktreeRecord struct {
	RecordID       string `json:"record_id"`
	Source         string `json:"source"`
	Head           string `json:"head"`
	Branch         string `json:"branch,omitempty"`
	Detached       bool   `json:"detached"`
	Locked         bool   `json:"locked"`
	Prunable       bool   `json:"prunable"`
	Present        bool   `json:"present"`
	CommonDir      string `json:"common_dir"`
	PorcelainBase64 string `json:"porcelain_base64"`
}

type dirtyPathRecord struct {
	RecordID          string `json:"record_id"`
	WorktreeRecordID  string `json:"worktree_record_id"`
	StatusCode        string `json:"status_code"`
	RelativePathBase64 string `json:"relative_path_base64"`
	OriginalPathBase64 string `json:"original_path_base64,omitempty"`
	FileType          string `json:"file_type"`
	Size              int64  `json:"size,omitempty"`
	SHA256            string `json:"sha256,omitempty"`
	OmissionReason    string `json:"omission_reason,omitempty"`
}

type stashRecord struct {
	RecordID    string `json:"record_id"`
	RefName     string `json:"ref_name"`
	ObjectID    string `json:"object_id"`
	CapturedUnix int64 `json:"captured_unix"`
}

type processRecord struct {
	RecordID      string `json:"record_id"`
	RootRecordID  string `json:"root_record_id"`
	PID           int    `json:"pid"`
	OccupancyKind string `json:"occupancy_kind"`
	Path          string `json:"path"`
}

type classificationRecord struct {
	RecordID           string `json:"record_id"`
	TargetRecordID     string `json:"target_record_id"`
	TargetKind         string `json:"target_kind"`
	Classification     string `json:"classification"`
	Owner              string `json:"owner"`
	RestoreDisposition string `json:"restore_disposition"`
	ChecksumPolicy     string `json:"checksum_policy"`
}

type aggregateRecord struct {
	ArchiveMappings int `json:"archive_mappings"`
	Refs            int `json:"refs"`
	Worktrees       int `json:"worktrees"`
	DirtyPaths      int `json:"dirty_paths"`
	Stashes         int `json:"stashes"`
	Processes       int `json:"processes"`
	Classifications int `json:"classifications"`
}

type cutoverInput struct {
	SchemaVersion             int                  `json:"schema_version"`
	ExpectedPublicRepository  string               `json:"expected_public_repository"`
	ExpectedPrivateRepository string               `json:"expected_private_repository"`
	Mappings                  []archiveMappingInput `json:"mappings"`
	Defaults                  []classificationDefault `json:"defaults"`
	Rules                     []classificationRule `json:"rules"`
}

type archiveMappingInput struct {
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type classificationDefault struct {
	Kind               string `json:"kind"`
	Classification     string `json:"classification"`
	Owner              string `json:"owner"`
	RestoreDisposition string `json:"restore_disposition"`
	ChecksumPolicy     string `json:"checksum_policy"`
}

type classificationRule struct {
	Kind               string `json:"kind"`
	Source             string `json:"source"`
	Identity           string `json:"identity"`
	Classification     string `json:"classification"`
	Owner              string `json:"owner"`
	RestoreDisposition string `json:"restore_disposition"`
	ChecksumPolicy     string `json:"checksum_policy"`
}

type validationPhase string

const (
	phaseCapture  validationPhase = "capture"
	phasePreMove validationPhase = "pre-move"
	phasePostMove validationPhase = "post-move"
	phasePreRollback validationPhase = "pre-rollback"
	phaseRollback validationPhase = "rollback"
)
```

Tests must prove that array order does not change the sealed checksum, changing
one retained field does change it, the `checksum` field is excluded from its
own digest, and `readManifest` rejects unknown JSON fields and trailing values.

- [ ] **Step 2: Run the focused test and observe the missing owner**

```bash
go test ./scripts/cutover_recovery -run 'Test(ManifestStrictDecode|CanonicalPayload|SealManifest)' -count=1
```

Expected: FAIL because the package and types do not exist.

- [ ] **Step 3: Implement the envelope and stable record identities**

Use `sha256:<lowercase hex>` for both manifest checksums and record IDs. A
record ID hashes three NUL-separated fields:

```go
func makeRecordID(kind, source, identity string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + source + "\x00" + identity))
	return "sha256:" + hex.EncodeToString(sum[:])
}
```

Before canonical JSON encoding, sort every record array, including archive
mappings, by `record_id`; set `Checksum` to the empty string; and encode with
`json.Marshal`. Reject schema versions other than `1`. Accept only mapping kinds
`main_checkout` and `linked_worktree`; classifications
`already_forward_ported`, `candidate_public_delta`, `private_recovery`,
`never_public`, and `unresolved`; and checksum policies `sha256` and
`omit_sensitive`. Restore dispositions are exactly `retain_archive`,
`reexpress_public`, `preserve`, `exclude_public`, and `block`; validation
requires the classification/disposition pairing implied by those names.

- [ ] **Step 4: Implement private atomic output**

Resolve the output parent and existing repository roots with `EvalSymlinks`;
resolve a prospective missing archive path through its nearest existing parent,
then reject an output beneath any source or destination root. Create a sibling
temporary file with mode
`0600`, write and `fsync` it, re-open and strictly decode it, rename it over the
target, then `fsync` the parent directory. Reject a symlink output or a parent
directory that changes identity during the write.

- [ ] **Step 5: Run the package tests and commit the schema owner**

```bash
go test ./scripts/cutover_recovery -count=1
git add scripts/cutover_recovery/model.go scripts/cutover_recovery/manifest.go scripts/cutover_recovery/manifest_test.go
git commit -m "feat(cutover): define recovery manifest"
```

Expected: PASS and a commit containing only the strict envelope owner.

### Task 2: Capture exact Git inventory without content transfer

**Files:**

- Create: `scripts/cutover_recovery/git.go`
- Create: `scripts/cutover_recovery/git_test.go`
- Modify: `scripts/cutover_recovery/model.go`

**Interfaces:**

- Consumes: `gitReader.Run(ctx context.Context, root string, argv ...string)
  ([]byte, error)` and a strict `cutoverInput` with
  `expected_public_repository`, `expected_private_repository`, and exact
  archive mappings, per-kind classification defaults, and exact override rules.
  Each override has `kind`, `source`, `identity`, `classification`, `owner`,
  `restore_disposition`, and `checksum_policy`.
- Produces: `collectRepositoryRecord(context.Context, gitReader, string,
  string) (repositoryRecord, error)` for either role and
  `collectPrivateInventory(context.Context, gitReader, string, cutoverInput)
  (repositoryInventory, error)` containing private ref, archive-mapping,
  worktree, dirty-path, stash, and classification records.

Use these exact internal seams:

```go
type gitReader interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type repositoryInventory struct {
	Repository     repositoryRecord
	Refs           []refRecord
	ArchiveMapping []archiveMappingRecord
	Worktrees      []worktreeRecord
	DirtyPaths     []dirtyPathRecord
	Stashes        []stashRecord
	Classifications []classificationRecord
}
```

- [ ] **Step 1: Write fake-runner tests for complete Git capture**

Use NUL-delimited fixtures for:

```text
git worktree list --porcelain -z
git -C "$root" remote get-url --all origin
git -C "$root" rev-parse --git-common-dir
git -C "$root" for-each-ref --format=%(refname)%00%(objectname)%00
git -C "$worktree" rev-parse --verify HEAD
git -C "$worktree" symbolic-ref --quiet --short HEAD
git -C "$worktree" status --porcelain=v1 -z --untracked-files=all
git -C "$root" stash list --format=%gd%x00%H%x00%ct%x00
```

Require rename records to preserve both source and destination identities,
paths containing spaces/newlines to remain unambiguous, prunable and locked
worktrees to remain present, detached worktrees to have no branch, local branch
and all refs to retain their object IDs. Every ref/dirty path/stash/worktree
receives exactly one default-or-override classification. Every present,
non-prunable worktree receives one archive mapping; an absent prunable
registration receives none. Mapping kinds are exactly `main_checkout` and
`linked_worktree`.

- [ ] **Step 2: Prove the tests fail before the collector exists**

```bash
go test ./scripts/cutover_recovery -run 'TestCollect(GitInventory|DirtyRename|Stashes|ClassificationCoverage)' -count=1
```

Expected: FAIL with missing collector symbols.

- [ ] **Step 3: Implement an explicit read-only Git allowlist**

Allow only the commands enumerated in Step 1 plus `rev-parse --show-toplevel`.
Validate `-C` roots against the captured worktree set before execution. Never
allow `checkout`, `switch`, `reset`, `clean`, `stash apply`, `worktree repair`,
`remote set-url`, `fetch`, `push`, or arbitrary caller-supplied arguments.

- [ ] **Step 4: Parse and classify every retained Git record**

Canonicalize paths without following a missing archive destination. A
prunable registration with an absent root retains its lexically cleaned
absolute path and porcelain identity, skips live status/common-dir reads, and
must not produce dirty-path or archive-mapping records. Normalize
GitHub SSH and HTTPS remotes to `owner/name` and require them to equal the two
repository identities in `cutoverInput`. Record
porcelain status bytes as a base64 string so path boundaries are preserved
without including file bodies. For an allowlisted regular dirty file, record
size and SHA-256; for directories, symlinks, missing paths, or
`omit_sensitive`, record only type and omission reason. For `sha256`, open the
regular file without following a symlink and compare device, inode, size, and
modification time before and after hashing; a concurrent change fails capture.
Require one default for
each of `ref`, `worktree`, `dirty_path`, and `stash`. An override rule must
match one record exactly; zero or multiple matches is an error, and a matched
override replaces rather than duplicates its kind default.

- [ ] **Step 5: Run focused and full package tests, then commit**

```bash
go test ./scripts/cutover_recovery -run 'TestCollect(GitInventory|DirtyRename|Stashes|ClassificationCoverage)' -count=1
go test ./scripts/cutover_recovery -count=1
git add scripts/cutover_recovery/model.go scripts/cutover_recovery/git.go scripts/cutover_recovery/git_test.go
git commit -m "feat(cutover): capture private git inventory"
```

Expected: PASS; no captured file content appears in test output or fixtures.

### Task 3: Capture process occupancy and verify archive phases

**Files:**

- Create: `scripts/cutover_recovery/process.go`
- Create: `scripts/cutover_recovery/process_test.go`
- Create: `scripts/cutover_recovery/verify.go`
- Create: `scripts/cutover_recovery/verify_test.go`
- Create: `scripts/cutover_recovery/integration_test.go`
- Modify: `scripts/cutover_recovery/model.go`

**Interfaces:**

- Consumes: `processReader.Run(context.Context, ...string) (commandResult,
  error)`, where `commandResult` retains stdout, stderr, and exit code only for
  validation; the manifest move roots; and `validationPhase` values `capture`,
  `pre-move`, `post-move`, `pre-rollback`, or `rollback`.
- Produces: `collectProcessOccupancy(context.Context, processReader, []string)
  ([]processRecord, error)` and `verifyLiveState(context.Context, dependencies,
  manifest, validationPhase) error`.

Use these exact internal seams:

```go
type commandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type processReader interface {
	Run(context.Context, ...string) (commandResult, error)
}

type dependencies struct {
	Git       gitReader
	Processes processReader
	Now       func() time.Time
}
```

- [ ] **Step 1: Write lsof parser and path-boundary tests**

Fixture the machine-readable fields emitted after `absolute_root` is resolved
with `filepath.EvalSymlinks`:

```bash
lsof -nP -Fpfn +D "$absolute_root"
```

Require a `cwd` descriptor to become `occupancy_kind: cwd`, every other file
descriptor beneath the exact root to become `occupancy_kind: open_file`, and
no command text to be retained. Prove that `/repo-old` does not match `/repo`
and a failing/missing `lsof` blocks capture rather than returning an empty set.

- [ ] **Step 2: Write phase-verification failure tests**

Cover duplicate/missing record IDs, aggregate mismatch, changed stash OID,
changed dirty porcelain bytes, changed HEAD/branch/common-dir, an occupied
source root, an existing archive destination, a public remote other than the
declared public repository, a private remote other than the declared private
archive repository, unresolved classification, and post-move mappings that do
not enumerate the original worktree record-ID set. Also reject duplicate,
nested, or cross-overlapping source/destination mappings. A prunable absent
registration must remain in the worktree record-ID set but must have no mapping.

- [ ] **Step 3: Run the phase tests and observe failure**

```bash
go test ./scripts/cutover_recovery -run 'Test(ProcessOccupancy|VerifyPreMove|VerifyPostMove|VerifyPreRollback|VerifyRollback|PrunableRegistration)' -count=1
```

Expected: FAIL because process and phase verification are not implemented.

- [ ] **Step 4: Implement fail-closed occupancy capture**

On Darwin, run one exact recursive `lsof` query per move root with a bounded
30-second context. Parse only PID, file
descriptor, and path; discard command names. Deduplicate by
`occupancy_kind/root/pid/path`, sort by record ID, and treat exit code `1` with
empty output as zero occupants. Any missing binary, malformed output, partial
record, timeout, permission error, or other exit status is an error.

- [ ] **Step 5: Implement phase-specific live comparison**

`pre-move` re-collects at source paths and additionally requires zero process
records and non-existent destinations. `post-move` reads destination mappings.
`pre-rollback` reads archive-side roots, requires zero occupants, and requires
every original source to remain collision-free. `rollback` reads original
source mappings. Post-move and rollback re-collect Git state and compare
stable sets plus HEAD, branch/detached identity, porcelain bytes, stash OIDs,
dirty records, and common-directory ownership. Return a sorted list of mismatch
codes without mutating either location.

- [ ] **Step 6: Exercise real Git move, repair, and rollback in isolation**

In `integration_test.go`, create a temporary main repository, one live linked
worktree, one dirty path, one stash, and one linked worktree whose directory is
removed so Git reports it prunable. Capture the inventory, move the live linked
root then main root with `os.Rename`, run the real `git worktree repair`, and
require `post-move` to preserve common-dir, HEAD, status, stash, and worktree
record IDs. Reverse both renames, repair original paths, and require `rollback`
to pass. The prunable record must survive both phases without a mapping.

```bash
go test ./scripts/cutover_recovery -run '^TestIntegrationMoveRepairRollback$' -count=1
```

Expected: PASS on any CI platform with Git; it touches only `t.TempDir()`.

- [ ] **Step 7: Run tests and commit the verifier**

```bash
go test ./scripts/cutover_recovery -count=1
git add scripts/cutover_recovery/model.go scripts/cutover_recovery/process.go scripts/cutover_recovery/process_test.go scripts/cutover_recovery/verify.go scripts/cutover_recovery/verify_test.go scripts/cutover_recovery/integration_test.go
git commit -m "feat(cutover): verify archive preconditions"
```

Expected: PASS; process occupancy and all mismatches are fail-closed.

### Task 4: Expose the two-command CLI and repository gates

**Files:**

- Create: `scripts/cutover_recovery/main.go`
- Create: `scripts/cutover_recovery/main_test.go`
- Modify: `Makefile`
- Create: `docs/contributing/public-canonical-cutover.md`
- Modify: `docs/contributing/README.md`
- Modify: `docs/superpowers/plans/2026-08-16-public-canonical-cutover-recovery.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- Consumes: `capture --private-root PATH --public-root PATH --archive-root PATH
  --input PATH --output PATH` and `verify --manifest PATH --phase
  pre-move|post-move|pre-rollback|rollback`.
- Produces: Make targets `cutover-recovery-capture` and
  `cutover-recovery-verify`, plus an operator guide that never embeds a personal
  absolute path.

- [ ] **Step 1: Write CLI contract tests**

Require unknown commands, positional arguments, repeated/missing flags,
relative paths, an output beneath either repository, unknown phases, and an
archive root equal to a source root to exit non-zero. Require `capture` to seal
and re-read its output before success and `verify` to print only a status and
record counts, never manifest contents.

- [ ] **Step 2: Run the CLI tests and observe failure**

```bash
go test ./scripts/cutover_recovery -run 'TestCLI' -count=1
```

Expected: FAIL because the entrypoint does not exist.

- [ ] **Step 3: Implement CLI parsing and Make targets**

Add these variables and targets without defaults for private paths:

```make
CUTOVER_PRIVATE_ROOT ?=
CUTOVER_PUBLIC_ROOT ?=
CUTOVER_ARCHIVE_ROOT ?=
CUTOVER_INPUT ?=
CUTOVER_MANIFEST ?=
CUTOVER_PHASE ?= pre-move
```

Add both targets to `.PHONY`. Each Make target must fail with exit code `2`
when a required variable is
empty, then invoke `$(GO) run ./scripts/cutover_recovery` with explicit flags.

- [ ] **Step 4: Document capture, verify, repair, and rollback ownership**

The guide defines the private cutover-input schema, exact environment
variables, capture/verify commands, the external authorized rename step,
`git worktree repair` command, post-move verification, and reverse mapping. It
states that the tool never performs a rename or repair and that an unresolved
or occupied result is a stop, not a cleanup recommendation.

- [ ] **Step 5: Run focused repository gates**

```bash
go test ./scripts/cutover_recovery -count=1
make change-plan
make verify-focused
git diff --check
```

Expected: package tests pass, the iteration planner selects repository tooling
and documentation owners, focused evidence succeeds, and the diff check is
empty.

- [ ] **Step 6: Commit, verify the committed tree, and make evidence ready**

```bash
git add Makefile scripts/cutover_recovery docs/contributing/public-canonical-cutover.md docs/contributing/README.md docs/superpowers/plans/2026-08-16-public-canonical-cutover-recovery.md docs/superpowers/plans/README.md
git commit -m "feat(cutover): add recovery inventory gate"
make verify-merge
make change-evidence
make change-evidence-ready
```

Expected: committed-tree merge verification passes and evidence is
`evidence_ready`. Remote CI and the live private capture remain separate next
steps.
