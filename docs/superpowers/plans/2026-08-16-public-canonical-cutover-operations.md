# Public Canonical Cutover Operations Implementation Plan

> **For agentic workers:** REQUIRED PROJECT SKILL: use
> `$iteration-workflow` for repository changes. Treat GitHub, Codex project
> routing, live recovery capture, and archive movement as separately read-back
> operations; never infer success from a local plan checkbox.

**Goal:** Route all new YHC work through the public checkout, preserve the
existing Desktop and Auto Permission work, and promote the private checkout to
a recovery archive only after the machine-readable preflight passes.

**Architecture:** Repository documentation and recovery tooling land through
ordinary public topic PRs. Auto Permission remains a stacked Draft based on the
unmerged Desktop topic. The public folder is added as a Codex local project
before the old project is retired; live recovery evidence is captured outside
both repositories, and the final filesystem rename is a reversible manual
operation guarded by exact process, path, Git, and manifest readbacks.

**Tech Stack:** Git, GitHub pull requests and checks, Codex desktop local
projects, the `scripts/cutover_recovery` command, `lsof`, repository publication
gates.

**Status:** active-plan
**Created:** 2026-08-16
**Plan state:** Ready; archive promotion waits for a fresh public-rooted task

> **Ownership:** public PR routing, Codex local-project cutover, private
> recovery capture, reversible archive promotion, and final acceptance from the
> [public canonical cutover design](../specs/2026-08-15-public-canonical-cutover-design.md)

## Global Constraints

- Public `origin/master` is the sole base for new independent iterations.
- Desktop PR #14 remains Draft and must not be merged, closed, rebased,
  retargeted, or deleted by this plan.
- Auto Permission may be opened only as a Draft whose base is the Desktop topic
  branch; it remains unmergeable while Desktop is unmerged.
- Never add the private repository as a public remote, repoint the old clone to
  the public repository, or replay a private SHA.
- Never publish `.yhc/`, transcripts, credentials, local artifacts, stashes,
  ignored reference snapshots, or private operational evidence.
- Existing tasks keep their current working directory until idle or explicitly
  handed off. New YHC tasks use the public local project.
- No branch, worktree, stash, task, saved project, checkout, or archive is
  deleted by this plan.
- An archive rename is allowed only after a fresh `pre-move` verification exits
  zero and the destination remains absent immediately before rename.
- An absent prunable worktree registration is retained as metadata without a
  move mapping. This plan never runs `git worktree prune`.
- Reverse movement is allowed only after `pre-rollback` proves archive-side
  zero occupancy and original-path collision freedom.

## Operation Map

| Surface | Owner and output |
|---|---|
| Public docs PR | Approved cutover contract on protected public `master` |
| Auto Permission PR | Stacked Draft metadata and Auto-only remote review surface |
| Codex local projects | Public root for new tasks; retained private entry for history |
| Private cutover evidence | Mode-`0600` input and sealed recovery manifest outside both repositories |
| Filesystem promotion | Exact reversible mappings only after a fresh zero-occupancy preflight |
| Closeout | Separate public Git, CI, publication, routing, recovery, and deferred-Desktop claims |

---

### Task 1: Land the approved cutover contract through a public PR

**Files:**

- Modify: `docs/superpowers/specs/2026-08-15-public-canonical-cutover-design.md`
- Modify: `docs/superpowers/specs/README.md`
- Create: `docs/superpowers/plans/2026-08-16-public-canonical-cutover-recovery.md`
- Create: `docs/superpowers/plans/2026-08-16-public-canonical-cutover-operations.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- Consumes: the user-approved design direction and Desktop deferral.
- Produces: an approved, indexed public contract and two executable plans.

- [ ] **Step 1: Mark written review approved and add both plan routes**

Set the design metadata to `Written review: approved 2026-08-16`, update the
specification index state to implementation planning active, and add both plan
rows to the plan index.

- [ ] **Step 2: Run documentation and diff-bound focused gates**

```bash
make change-plan
make verify-focused
git diff --check
```

Expected: documentation ownership is selected, all available public-tree docs
checks pass, and no whitespace error exists. A private reference checkout that
is intentionally absent from an isolated public worktree is reported as
not-applicable rather than fabricated as a pass.

- [ ] **Step 3: Commit and verify the committed tree**

```bash
git add docs/superpowers/specs/2026-08-15-public-canonical-cutover-design.md docs/superpowers/specs/README.md docs/superpowers/plans/2026-08-16-public-canonical-cutover-recovery.md docs/superpowers/plans/2026-08-16-public-canonical-cutover-operations.md docs/superpowers/plans/README.md
git commit -m "docs: approve public canonical cutover"
make verify-merge
make change-evidence-ready
```

Expected: committed-tree evidence is ready before push.

- [ ] **Step 4: Push, open, review, and squash the documentation PR**

Push only `codex/docs/public-canonical-cutover`. The PR title is
`docs: define public canonical cutover`; its body states the private-history
exclusions, Desktop deferral, Auto stacked-Draft rule, and archive stop gates.
Wait for required checks, review the exact PR diff, squash-merge it, and read
back the new public `master` SHA. Do not mutate PR #14.

### Task 2: Publish the Auto Permission review surface without merging Desktop

**Files:** None; this task changes public PR metadata only.

**Interfaces:**

- Consumes: public branches `codex/feat/desktop-workbench` and
  `codex/feat/proof-bound-auto-bash` plus their remote check results.
- Produces: one Draft Auto Permission PR with Desktop as its base and an
  Auto-only diff surface.

- [ ] **Step 1: Refresh and prove the stacked graph**

```bash
git fetch --prune origin
git merge-base --is-ancestor origin/codex/feat/desktop-workbench origin/codex/feat/proof-bound-auto-bash
git rev-list --count origin/codex/feat/desktop-workbench..origin/codex/feat/proof-bound-auto-bash
git diff --check origin/codex/feat/desktop-workbench...origin/codex/feat/proof-bound-auto-bash
```

Expected: ancestor check exits zero, the count is exactly the intended 14 Auto
Permission commits, and the Auto-only diff passes whitespace validation. If the
count or graph changed, stop and re-audit rather than rewriting either branch.

- [ ] **Step 2: Read back Desktop state before creating the Draft**

Verify PR #14 is still Draft, open, unmerged, and based on `master`, with head
`codex/feat/desktop-workbench`. Record current required-check conclusions
separately from local evidence.

- [ ] **Step 3: Create the stacked Draft PR**

Create a Draft PR with base `codex/feat/desktop-workbench`, head
`codex/feat/proof-bound-auto-bash`, and title
`feat: add proof-bound automatic bash permission`. Its first paragraph says
`Stacked on Desktop PR #14; do not merge until the base is integrated and this
branch is rebuilt or retargeted from the resulting public master.` Link the
accepted Auto Permission design and list only Desktop-to-Auto verification.

- [ ] **Step 4: Verify the remote review surface**

Read back `isDraft=true`, the exact base/head refs, merge state, commit count,
changed-file count, and every check result. A missing authenticated GitHub
session blocks PR creation but does not justify changing branches, remotes, or
Desktop state.

### Task 3: Add and verify the public Codex local project

**Files:** None; this task changes local Codex application state only.

**Interfaces:**

- Consumes: the clean public checkout and the official Codex local-project
  workflow documented in [Projects and chats](https://learn.chatgpt.com/docs/projects).
- Produces: a discoverable public YHC project entry while retaining the private
  project entry for historical tasks.

- [ ] **Step 1: Verify the public folder before opening it**

Resolve the selected folder with `git rev-parse --show-toplevel`, require its
`origin` to be the public YHC repository, require its Git common directory not
to equal the private checkout's common directory, and record its current branch
and status. The existing ignored reference link is not copied into a new
worktree and is not publication evidence.

- [ ] **Step 2: Open the public folder as a Codex local project**

Use the desktop application's Open Folder action and select the verified public
checkout. Do not remove, rename, or edit the private project entry during this
step.

- [ ] **Step 3: Read back project routing**

Confirm the project list contains distinct public and private absolute roots.
Open a new chat only when a real new YHC outcome is requested; its recorded
working directory must resolve beneath the public checkout. Historical chats
remain attached to their existing transcript and working directory until
explicit handoff.

- [ ] **Step 4: Freeze the old entry as recovery-only**

Name or annotate the old entry as private history only if the current Codex UI
offers a verified non-destructive operation. If it does not, retain the entry
unchanged and use the public entry for all newly created work; do not edit app
state files directly.

### Task 4: Capture and approve the live private recovery inventory

**Files:** Private external artifacts only; no repository file is modified.

**Interfaces:**

- Consumes: a mode-`0600` cutover input outside both repositories and
  the committed `scripts/cutover_recovery` command.
- Produces: a sealed private manifest and a successful structural capture
  verification. `pre-move` may remain blocked while tasks or file descriptors
  occupy a move root.

- [ ] **Step 1: Bind explicit paths without storing them in Git**

```bash
: "${YHC_PUBLIC_ROOT:?set the absolute public checkout}"
: "${YHC_PRIVATE_ROOT:?set the absolute private-history checkout}"
: "${YHC_ARCHIVE_ROOT:?set the absent archive destination}"
: "${YHC_CUTOVER_EVIDENCE:?set a private evidence directory outside both repositories}"
install -d -m 0700 "$YHC_CUTOVER_EVIDENCE"
```

Expected: all paths are absolute, public/private Git common directories differ,
and the archive destination does not exist.

- [ ] **Step 2: Write and validate exact cutover rules**

Set `expected_public_repository` to `abietic/yhc` and
`expected_private_repository` to `abietic/yhc-private-history`. Supply one
default for each of `ref`, `worktree`, `dirty_path`, and `stash`, then add exact
overrides for `already_forward_ported`, `candidate_public_delta`,
`never_public`, or intentionally `unresolved` records. Default conservatively
to `private_recovery`, `preserve`, and `omit_sensitive`; authorize `sha256` only
for reviewed non-secret regular files. Give every worktree one explicit
collision-free mapping of kind `main_checkout` or `linked_worktree` for every
present, non-prunable worktree. Record and classify an absent prunable
registration without a mapping; never invent a directory for it or prune it.
The input contains metadata only and has mode `0600`; process occupants are
captured as blockers and need no classification.

- [ ] **Step 3: Capture the sealed manifest**

```bash
go run ./scripts/cutover_recovery capture \
  --private-root "$YHC_PRIVATE_ROOT" \
  --public-root "$YHC_PUBLIC_ROOT" \
  --archive-root "$YHC_ARCHIVE_ROOT" \
  --input "$YHC_CUTOVER_EVIDENCE/cutover-input.json" \
  --output "$YHC_CUTOVER_EVIDENCE/recovery-manifest.json"
```

Expected: capture exits zero, the output mode is `0600`, and printed output
contains only status and aggregate counts.

- [ ] **Step 4: Review unresolved and occupied results**

Read only classifications, counts, refs, mappings, and omission reasons. Do
not display path bodies, secret material, or stash contents. Any unresolved
item receives an owner and remains preserved. An active process blocks only the
archive move; it does not block public PR or project-routing work.

### Task 5: Promote the old checkout to a private archive when unoccupied

**Files:** Filesystem paths recorded in the private manifest; no public source
file is modified.

**Interfaces:**

- Consumes: a fresh sealed manifest whose `pre-move` verification exits zero.
- Produces: an exact path rename, repaired linked-worktree registrations, and a
  successful `post-move` verification; otherwise the original mapping is
  restored and verified.

This task begins only from a fresh Codex task rooted in the public project. The
current migration task is itself evidence that the old root is still occupied;
it must finish before the final pre-move check can succeed.

- [ ] **Step 1: Confirm task and saved-project state**

Require the migration task and every retained private task to be idle or
explicitly handed off. Require the public Codex project to remain discoverable.
Retain the old saved entry until the filesystem move and post-move verification
succeed.

- [ ] **Step 2: Run the immediate pre-move gate**

```bash
go run ./scripts/cutover_recovery verify \
  --manifest "$YHC_CUTOVER_EVIDENCE/recovery-manifest.json" \
  --phase pre-move
```

Expected: zero process occupants, zero unresolved classifications, exact
remotes, complete record-ID sets, and absent destinations. Any failure stops
before a rename. Do not pause for additional work between this gate and Step 3.

- [ ] **Step 3: Apply the frozen path mapping exactly**

Rename only the source/destination pairs present in the sealed manifest. Move
linked worktree roots first and the main checkout last so the common directory
remains available until every link path is in place. Execute from the public
project:

```zsh
setopt ERR_EXIT NO_UNSET PIPE_FAIL
manifest="$YHC_CUTOVER_EVIDENCE/recovery-manifest.json"
go run ./scripts/cutover_recovery verify --manifest "$manifest" --phase pre-move
mapping_fields=()
while IFS= read -r -d '' field; do
  mapping_fields+=("$field")
done < <(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); order={"linked_worktree":0,"main_checkout":1}; items=sorted(d["archive_mapping"], key=lambda x:(order[x["kind"]],x["record_id"])); sys.stdout.buffer.write(b"".join(x[k].encode()+b"\0" for x in items for k in ("source","destination")))' "$manifest")
(( ${#mapping_fields} % 2 == 0 ))
for ((i=1; i<=${#mapping_fields}; i+=2)); do
  source_path="${mapping_fields[i]}"
  destination_path="${mapping_fields[i+1]}"
  [[ -e "$source_path" ]]
  [[ ! -e "$destination_path" ]]
  install -d -m 0700 "${destination_path:h}"
  /bin/mv "$source_path" "$destination_path"
done
```

Do not use a glob, recursive delete, copy, Git remote mutation, or stash
operation. A partial loop immediately enters the rollback path; it does not
continue with repair or saved-project retirement.

- [ ] **Step 4: Repair retained linked worktrees**

From the moved main checkout, load the stable destination list without shell
evaluation, then repair it:

```zsh
manifest="$YHC_CUTOVER_EVIDENCE/recovery-manifest.json"
moved_main="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(next(x["destination"] for x in d["archive_mapping"] if x["kind"]=="main_checkout"))' "$manifest")"
repair_paths=()
while IFS= read -r -d '' path; do
  repair_paths+=("$path")
done < <(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); items=sorted(d["archive_mapping"], key=lambda x:x["record_id"]); sys.stdout.buffer.write(b"".join(x["destination"].encode()+b"\0" for x in items if x["kind"]=="linked_worktree"))' "$manifest")
git -C "$moved_main" worktree repair "${repair_paths[@]}"
```

The arguments are the manifest-recorded current locations in stable record-ID
order. Do not run `git worktree prune`.

- [ ] **Step 5: Verify post-move or execute exact rollback**

```bash
go run ./scripts/cutover_recovery verify \
  --manifest "$YHC_CUTOVER_EVIDENCE/recovery-manifest.json" \
  --phase post-move
```

If verification fails, first require archive-side zero occupancy and a safe
reverse mapping:

```bash
go run ./scripts/cutover_recovery verify \
  --manifest "$YHC_CUTOVER_EVIDENCE/recovery-manifest.json" \
  --phase pre-rollback
```

If `pre-rollback` fails, stop and preserve the current paths for diagnosis. If
it passes, reverse only pairs whose source is absent and destination exists, in
reverse move order, then repair original linked paths:

```zsh
setopt ERR_EXIT NO_UNSET PIPE_FAIL
manifest="$YHC_CUTOVER_EVIDENCE/recovery-manifest.json"
mapping_fields=()
while IFS= read -r -d '' field; do
  mapping_fields+=("$field")
done < <(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); order={"linked_worktree":0,"main_checkout":1}; items=sorted(d["archive_mapping"], key=lambda x:(order[x["kind"]],x["record_id"])); sys.stdout.buffer.write(b"".join(x[k].encode()+b"\0" for x in items for k in ("source","destination")))' "$manifest")
for ((i=${#mapping_fields}-1; i>=1; i-=2)); do
  source_path="${mapping_fields[i]}"
  destination_path="${mapping_fields[i+1]}"
  if [[ ! -e "$source_path" && -e "$destination_path" ]]; then
    install -d -m 0700 "${source_path:h}"
    /bin/mv "$destination_path" "$source_path"
  fi
done
restored_main="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(next(x["source"] for x in d["archive_mapping"] if x["kind"]=="main_checkout"))' "$manifest")"
original_worktrees=()
while IFS= read -r -d '' path; do
  original_worktrees+=("$path")
done < <(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); items=sorted(d["archive_mapping"], key=lambda x:x["record_id"]); sys.stdout.buffer.write(b"".join(x["source"].encode()+b"\0" for x in items if x["kind"]=="linked_worktree"))' "$manifest")
git -C "$restored_main" worktree repair "${original_worktrees[@]}"
```

Then require:

```bash
go run ./scripts/cutover_recovery verify \
  --manifest "$YHC_CUTOVER_EVIDENCE/recovery-manifest.json" \
  --phase rollback
```

No failed post-move result is reported as a completed cutover.

### Task 6: Prove the final public and deferred boundaries

**Files:** Private external evidence only; no new source change is implied.

**Interfaces:**

- Consumes: final public `master`, anonymous clone, publication tooling, Codex
  project readback, private archive readback, and the remote PR graph.
- Produces: a closeout that separates local gates, remote CI, public-history
  proof, task routing, archive recovery, and deferred Desktop state.

- [ ] **Step 1: Run clean committed public gates**

```bash
make change-evidence-ready
make verify-publication
```

Materialize the exact committed tree into a new no-`.git` directory and run
`make verify-publication-tree` there. Expected: all public-tree policy,
expression, secret, license, and vulnerability gates pass.

- [ ] **Step 2: Verify an anonymous public clone**

Require only the intended public refs and graph, no replace refs, alternates,
submodules, private remote, or reachable private commits/tags/ref targets.
Compare the repository-owned publication digest with public `master`.

- [ ] **Step 3: Read back routing and recovery**

Confirm new YHC work starts under the public project, the private archive remote
still names only the private-history repository, and the sealed manifest still
passes `post-move`. Retain all archives, stashes, worktrees, and task history.

- [ ] **Step 4: Confirm the deferred PR boundary**

Read back Desktop PR #14 as Draft and unmerged. If the Auto Permission Draft
exists, confirm it is still based on the Desktop branch and unmerged. Report
Desktop physical acceptance and signed/notarized distribution as separate,
still-unclaimed evidence.
