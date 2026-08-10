# YHC Clean Root And Remote Cutover Implementation Plan

> **Historical execution note:** This plan records the completed clean-root
> cutover and its approval boundaries. Future changes use the normal protected
> workflow. Checkboxes remain as closeout evidence.

**Goal:** Convert the fully cleared YHC candidate into one signed fresh root,
retain the old repository as a private archive, bootstrap and govern a separate
private `<public-repository>`, then make only YHC public and accept it after
the exact public root passes required CI.

**Architecture:** The publication tool materializes an allowlisted tree into a
directory that has never contained the private `.git` store. A signed root
commit is created there and pushed before branch rules are enabled. The old
remote rename, new private bootstrap, remote re-clone verification, public
visibility promotion, and post-public acceptance are separate phases with
readbacks. The existing multi-worktree clone stays on the archive; a new clone
owns public development.

**Tech Stack:** Git, GitHub CLI and REST API, signed commits, repository
rulesets, Actions and security settings, unauthenticated GitHub verification,
repository publication tooling, Gitleaks, `govulncheck`, CycloneDX, Makefile
gates, and a separate local clone.

**Status:** historical
**Created:** 2026-08-09
**Completed:** 2026-08-11
**Plan state:** Completed; clean root, private archive, public promotion, and canonical clone verified

> **Ownership:** clean-root assembly, remote bootstrap, visibility promotion,
> and post-public acceptance from the
> [YHC public-release design](../specs/2026-08-09-yhc-public-release-design.md).

## Global Constraints

- Do not start until every other YHC leaf plan is complete, the private
  candidate commit has `evidence_ready`, and publication checks are green.
- The public root contains file content only. Never copy `.git`, objects, refs,
  tags, branches, stashes, reflogs, alternates, hooks, PR/issue/review exports,
  Actions logs/artifacts/caches, or author history. The new repository creates
  its own object database. Cleared identical file bytes may naturally hash to
  the same blob ID in both repositories; acceptance forbids shared commit/tag/
  ref/parent history, not content-address equality.
- The existing private repository is renamed to `<private-archive>` and remains
  private. It is not a fork or mirror of YHC. If a remote step fails, keep both
  repositories private.
- The old multi-worktree clone changes only its `origin` URL to the archive.
  Because remotes are shared by worktrees, never point that clone at YHC.
- The candidate root author identity and signing capability require explicit
  release approval. If signature verification fails, stop before any push.
- The initial `master` root push is the only direct-master exception. Configure
  rules immediately afterward and do not reuse the exception.
- Visibility promotion is an irreversible exposure event. Refresh every remote,
  content, security, and tree-hash fact immediately before it and require the
  explicit publication readback approval.
- Do not delete a staging repository, temporary clone, failed candidate, or
  archive during failure handling. Retain it for diagnosis unless the user
  separately authorizes deletion.
- Raw remote inventories and scanner findings remain under ignored
  `build/publication` in the private staging checkout. They never enter the
  public root.
- After public exposure, a secret/private-content finding is an incident, not a
  normal rollback. Revoke credentials and limit access; making the repo private
  cannot retract clones.
- Public runtime/CI failure without a content leak leaves YHC public but
  non-canonical. Repair through a normal short-lived branch and PR.

---

## Phase And Stop-Point Matrix

| Phase | Remote state | May continue without a new readback? | Failure boundary |
|---|---|---|---|
| Local root assembly | old repo private; no new repo | Yes | Keep local candidate; no remote change |
| Pre-remote approval | old repo private | No | Stop |
| Archive rename and private bootstrap | archive private; YHC private | Yes within approved phase | Keep both private; diagnose |
| Re-clone verification | archive private; YHC private | Yes | Do not change visibility |
| Publication approval | archive private; YHC private | No | Stop |
| Visibility promotion | archive private; YHC public | Only acceptance checks | Incident or non-canonical repair |
| Canonical closeout | archive private; YHC public and green | Yes | Normal PR workflow |

## Task 1: Materialize And Sign The Fresh Root

**Files:**

- Source policy: `quality/publication.yaml`
- Candidate-generated: `PUBLICATION_MANIFEST.json`
- Candidate-generated: `sbom.cdx.json`
- Private evidence: `build/publication/root-assembly.json`

- [x] **Step 1: Re-run the exact private candidate gates**

```bash
git status --short
env GOCACHE="<task-cache>" make change-evidence
make verify-publication
make fmt-check
make lint
make test
make build
make docs-check
git diff --check
```

Expected: status is empty, `change-evidence` is `evidence_ready`, and every
gate exits 0.

- [x] **Step 2: Materialize into a new empty directory**

```bash
public_parent=$(mktemp -d "<temporary-directory>/public-root.XXXXXX")
public_root="$public_parent/repository"
test ! -e "$public_root"
source_commit=$(git rev-parse HEAD)
go run ./scripts/publication materialize --config quality/publication.yaml --source-commit "$source_commit" --output "$public_root"
test ! -e "$public_root/.git"
go run ./scripts/publication check-tree --config quality/publication.yaml --root "$public_root"
```

Expected: the output is outside the private checkout, contains no `.git`, and
passes the candidate tree policy.

- [x] **Step 3: Generate the redacted manifest and verify payload digest**

```bash
go run ./scripts/publication manifest --config quality/publication.yaml --root "$public_root" --output "$public_root/PUBLICATION_MANIFEST.json"
go run ./scripts/publication check-tree --config quality/publication.yaml --root "$public_root"
make secret-check PUBLICATION_ROOT="$public_root"
```

The manifest's payload digest excludes the manifest itself to avoid a
self-reference. The private evidence file records `source_commit`; the public
manifest records only public payload digests and check statuses.

- [x] **Step 4: Resolve and approve public commit identity**

Use the separately approved public author identity and signing capability. Read
back the approved public identity without displaying private author data. If
the identity or signature verification is not approved, stop.

- [x] **Step 5: Create one signed root**

```bash
git -C "$public_root" init -b master
git -C "$public_root" config user.name "<approved-public-author>"
git -C "$public_root" config user.email "<approved-public-email>"
git -C "$public_root" add --all
git -C "$public_root" diff --cached --check
git -C "$public_root" commit -S -m "chore: bootstrap YHC public project"
git -C "$public_root" verify-commit HEAD
test "$(git -C "$public_root" rev-list --count HEAD)" = 1
test -z "$(git -C "$public_root" tag --list)"
```

Expected: one verified root commit with no parent and no tag.

## Task 2: Capture Pre-Remote Facts And Stop For Approval

**Files:**

- Private evidence: `build/publication/remote-before.json`
- Tracked desired state: `.github/repository-settings.json`
- Tracked ruleset: `.github/rulesets/master.json`

- [x] **Step 1: Verify current remote facts**

```bash
gh repo view "<private-source-repository>" --json nameWithOwner,visibility,defaultBranchRef,url,description,mergeCommitAllowed,rebaseMergeAllowed,squashMergeAllowed,deleteBranchOnMerge
gh api "repos/<private-source-repository>/actions/permissions"
gh repo view "<public-repository>"
gh repo view "<private-archive>"
```

Expected before rename: `<private-source-repository>` exists and is private;
`<public-repository>` and `<private-archive>` do not exist. An existing
target name is a hard stop, never an overwrite/delete instruction.

- [x] **Step 2: Export audit-only inventories**

Use authenticated read-only GitHub API calls to record repository settings,
rulesets/protection visibility, tags/releases, open/closed PR and issue counts,
Actions workflow/run/artifact counts, and security-feature status under
`build/publication`. Do not export bodies, comments, logs, artifacts, or
credentials.

- [x] **Step 3: Present the pre-remote readback**

Read back:

- private source commit and `evidence_ready` digest;
- signed public root SHA and payload/tree digest;
- exact archive and public repository names;
- approved public author identity;
- zero unresolved provenance, vulnerability, secret, privacy, or license
  blocker; and
- the fact that rename changes existing clone URLs but exposes nothing.

- [x] **Step 4: Stop until the user approves archive rename and private
bootstrap**

Do not issue any repository PATCH/CREATE/PUSH before this approval.

## Task 3: Rename The Archive And Bootstrap Private YHC

- [x] **Step 1: Rename the old repository and verify privacy**

```bash
gh api --method PATCH "repos/<private-source-repository>" -f "name=<private-archive-name>"
gh repo view "<private-archive>" --json nameWithOwner,visibility,defaultBranchRef,url
```

Expected: exact owner/name, `PRIVATE`, default `master`.

- [x] **Step 2: Repoint only the old clone to the archive**

Run from `<local-checkout>`:

```bash
git remote set-url origin "<private-archive-remote>"
git remote -v
git fetch origin --prune
```

Expected: every existing worktree still shares the archive remote. Do not
switch, reset, merge, or delete active private branches.

- [x] **Step 3: Create the new private repository**

```bash
gh repo create "<public-repository>" --private --description "YHC — Yet Hooked on Coding" --disable-wiki
gh repo view "<public-repository>" --json nameWithOwner,visibility,defaultBranchRef,url
```

Expected: `<public-repository>` exists and is `PRIVATE`. If creation fails after the
archive rename, leave the archive name in place and diagnose; do not
automatically rename it back.

- [x] **Step 4: Push the sole bootstrap root**

```bash
git -C "$public_root" remote add origin "<public-repository-remote>"
git -C "$public_root" push --set-upstream origin master
remote_root=$(gh api "repos/<public-repository>/commits/master" --jq .sha)
test "$remote_root" = "$(git -C "$public_root" rev-parse HEAD)"
```

This direct push occurs before the ruleset and is the one bootstrap exception.

## Task 4: Configure Private Staging Governance

- [x] **Step 1: Apply repository metadata and merge policy**

```bash
gh api --method PATCH "repos/<public-repository>" --input "$public_root/.github/repository-settings.json"
gh api --method PUT "repos/<public-repository>/actions/permissions" -F enabled=true -f allowed_actions=selected -F github_owned_allowed=true -F verified_allowed=false -F sha_pinning_required=true
gh api --method PUT "repos/<public-repository>/actions/permissions/workflow" -f default_workflow_permissions=read -F can_approve_pull_request_reviews=false
```

- [x] **Step 2: Enable dependency and private-reporting features**

```bash
gh api --method PUT "repos/<public-repository>/vulnerability-alerts"
gh api --method PUT "repos/<public-repository>/automated-security-fixes"
gh api --method PUT "repos/<public-repository>/private-vulnerability-reporting"
```

If a feature is unavailable while private, record `not_available_private` and
make it a required immediate post-public action; do not report it as enabled.

- [x] **Step 3: Create and verify the master ruleset**

```bash
gh api --method POST "repos/<public-repository>/rulesets" --input "$public_root/.github/rulesets/master.json"
gh api "repos/<public-repository>/rulesets"
```

The active ruleset targets `master`, blocks force push and deletion, requires
PRs, conversation resolution, and `Required gates`, with zero human
approvals until a second maintainer. It has no reusable direct-push bypass.

- [x] **Step 4: Read back desired state**

Compare live repository, Actions, workflow-permission, security, and ruleset
JSON with the tracked desired-state files. Any mismatch blocks re-clone
acceptance.

## Task 5: Re-Clone And Verify What GitHub Stores

- [x] **Step 1: Clone the private remote into a second new directory**

```bash
remote_clone=$(mktemp -d "<temporary-directory>/remote-verification.XXXXXX")
git clone "<public-repository-remote>" "$remote_clone"
test "$(git -C "$remote_clone" rev-list --all --count)" = 1
test -z "$(git -C "$remote_clone" tag --list)"
test -z "$(git -C "$remote_clone" ls-remote --tags origin)"
git -C "$remote_clone" fsck --full --strict

archive_compare_parent=$(mktemp -d "<temporary-directory>/archive-compare.XXXXXX")
archive_mirror="$archive_compare_parent/archive.git"
archive_commits="$archive_compare_parent/archive-commits.txt"
public_commits="$archive_compare_parent/public-commits.txt"
archive_ref_targets="$archive_compare_parent/archive-ref-targets.txt"
public_ref_targets="$archive_compare_parent/public-ref-targets.txt"

git clone --mirror "<private-archive-remote>" "$archive_mirror"
git -C "$archive_mirror" rev-list --all | sort -u >"$archive_commits"
git -C "$remote_clone" rev-list --all | sort -u >"$public_commits"
git -C "$archive_mirror" for-each-ref --format='%(objectname)' | sort -u >"$archive_ref_targets"
git -C "$remote_clone" for-each-ref --format='%(objectname)' | sort -u >"$public_ref_targets"

test -z "$(comm -12 "$archive_commits" "$public_commits")"
test -z "$(comm -12 "$archive_ref_targets" "$public_ref_targets")"
test "$(git -C "$remote_clone" rev-list --parents -n 1 HEAD | awk '{print NF}')" = 1
test -z "$(git -C "$remote_clone" for-each-ref --format='%(refname)' refs/replace)"
test ! -s "$remote_clone/.git/objects/info/alternates"
test ! -f "$remote_clone/.gitmodules"
test "$(git -C "$remote_clone" for-each-ref --format='%(refname)' refs/remotes/origin | sed '/\/HEAD$/d')" = "refs/remotes/origin/master"
```

Also require exactly one remote branch (`master`), no submodule, no alternates,
and no replace refs. The public root may reuse object IDs for identical file
blobs, but it must share no reachable commit, tag, ref-target, or parent history
with the private archive.

- [x] **Step 2: Compare payload and root identity**

```bash
test "$(git -C "$remote_clone" rev-parse HEAD)" = "$remote_root"
make -C "$remote_clone" publication-check-tree PUBLICATION_ROOT="$remote_clone"
```

Compare the repository-owned payload digest and `PUBLICATION_MANIFEST.json`
against the signed local root. Keep the Git commit SHA separate from the
content-tree digest. The Step 1 commit/ref-target intersections, empty public
tag set, and parentless public root are the executable proof; do not require
zero blob-ID intersection. When `publication-check-tree` runs at the root of
the current clean source checkout, it excludes only that root's `.git`
metadata from the payload digest and accepts ordinary clone directory modes
that grant the owner `rwx` while granting no group/other write bit. Detached
publication trees retain the stricter `0700` directory contract, and
`verify-publication-tree` still requires `.git` to be absent.

- [x] **Step 3: Run all gates from the remote clone**

```bash
make -C "$remote_clone" fmt-check
make -C "$remote_clone" lint
make -C "$remote_clone" test
make -C "$remote_clone" build
make -C "$remote_clone" docs-check
make -C "$remote_clone" test-race
make -C "$remote_clone" test-contract
make -C "$remote_clone" test-e2e
make -C "$remote_clone" verify-publication
```

Run from `remote_clone` with task-specific Go/build caches. Run Gitleaks on both
the filesystem and the fresh one-commit Git database. Expected: all pass.

## Task 6: Refresh Facts And Stop For Publication Approval

- [x] Re-query both repository visibilities, remote root SHA, default branch,
  merge settings, Actions permissions, security settings, and master ruleset.
- [x] Re-run secret/privacy/license/vulnerability/provenance checks and compare
  the remote payload digest.
- [x] Present the publication readback:

  - `<private-archive>` is private;
  - `<public-repository>` is private;
  - YHC has one signed root and no copied private history;
  - all local and remote-clone gates are green;
  - rules/security desired state matches or names only features that become
    available immediately after public;
  - the exact root SHA to expose; and
  - publication cannot be retracted after visibility changes.

- [x] Stop until the user explicitly approves changing only
  `<public-repository>` to public.

## Task 7: Promote Visibility And Accept Public CI

- [x] **Step 1: Change only YHC visibility**

```bash
gh api --method PATCH "repos/<public-repository>" -f visibility=public
gh repo view "<public-repository>" --json nameWithOwner,visibility,defaultBranchRef,url
gh repo view "<private-archive>" --json nameWithOwner,visibility,defaultBranchRef,url
```

Expected: YHC `PUBLIC`, archive `PRIVATE`.

- [x] **Step 2: Verify logged-out visibility**

Use an unauthenticated GitHub API request and logged-out browser view. YHC must
return public repository metadata; the archive must not expose repository
metadata. Do not infer logged-out behavior from an authenticated `gh` response.

- [x] **Step 3: Finish public-only security enablement**

Retry any security feature recorded `not_available_private`, verify secret
scanning/Dependabot alerts/private reporting/CodeQL availability, and read back
the exact status.

- [x] **Step 4: Dispatch and watch CI on the exact root**

```bash
gh workflow run CI --repo "<public-repository>" --ref master
public_run=$(gh run list --repo "<public-repository>" --workflow CI --branch master --limit 5 --json databaseId,headSha --jq '.[] | select(.headSha == "'$remote_root'") | .databaseId' | head -n 1)
test -n "$public_run"
```

The selected run has head SHA `remote_root`. Then:

```bash
gh run watch "$public_run" --repo "<public-repository>" --exit-status
gh run view "$public_run" --repo "<public-repository>"
```

Expected: `Required gates` succeeds on `remote_root`. Inspect CodeQL
separately and do not call it required-gate success.

- [x] **Step 5: Handle failure by class**

If a secret/private-content finding appears, begin incident handling and stop.
If only CI/runtime acceptance fails, keep YHC non-canonical and repair via a
short-lived public PR; do not use another direct-master push.

## Task 8: Establish The Separate Canonical Clone And Close Out

- [x] **Step 1: Clone YHC separately**

```bash
git clone "<public-repository-remote>" "<publication-tree>"
git -C "<publication-tree>" remote -v
git -C "<local-checkout>" remote -v
```

Expected: the new clone points to public YHC; the old multi-worktree clone
points only to the private archive.

- [x] **Step 2: Verify normal post-bootstrap workflow**

Create a short-lived documentation branch in the new clone, update plan
closeout state, open a PR, require `Required gates`, squash merge, and
delete the branch. This proves the configured rules rather than only reading
their JSON.

- [x] **Step 3: Record terminal evidence**

Record public root SHA, public workflow run ID, CodeQL state, archive/public
visibility readbacks, repository-rules readback, and separate-clone remotes in
the public closeout document without private paths beyond the public clone,
private author data, or scanner findings.

YHC becomes canonical only after Task 8 completes. The archive and all active
private worktrees remain available for their existing iterations.
