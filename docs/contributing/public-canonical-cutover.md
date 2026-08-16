# Public Canonical Cutover

**Status:** current
**Last verified:** 2026-08-16

> **Ownership:** preserve the private-history checkout as a recovery archive
> while making the public checkout the only future development root

> **Delivery boundary:** the tooling is implemented on a topic branch. It is
> not a public-master capability until its reviewed PR is merged, and the live
> checkout move has not been executed.

The recovery command is deliberately read-only with respect to Git and project
trees. It captures a sealed inventory and verifies one cutover phase. It never
renames a checkout, repairs a worktree, prunes a registration, changes a remote,
or deletes an archive.

Desktop PR #14 is outside this workflow. Leave it open and unmerged until its
own review resumes.

## Stop conditions

Stop instead of moving anything when capture or verification reports any of
these conditions:

- a process has a current working directory or open file beneath a move root;
- a source or destination exists at the wrong phase, resolves through a changed
  symlink parent, or overlaps another mapping;
- public or private origin identity, HEAD, branch, refs, stash, dirty status,
  worktree porcelain, or Git common-directory ownership differs;
- a classification is unresolved, missing, duplicated, or has an invalid
  restore disposition;
- an allowlisted command, `lsof`, checksum, strict JSON decode, or manifest
  checksum fails.

Do not translate a stop into cleanup. Preserve the current trees and diagnose
the mismatch first.

## 1. Choose paths outside the repositories

Use canonical absolute paths. The manifest must be outside the public checkout,
every private move root, and every archive destination.

```bash
export CUTOVER_PRIVATE_ROOT="/absolute/path/to/private-history"
export CUTOVER_PUBLIC_ROOT="/absolute/path/to/public-yhc"
export CUTOVER_ARCHIVE_ROOT="/absolute/path/to/private-history-archive"
export CUTOVER_INPUT="/absolute/path/to/private-cutover-input.json"
export CUTOVER_MANIFEST="/absolute/path/to/private-cutover-manifest.json"
```

Run live capture and preflight on Darwin. Other platforms compile the command
but intentionally fail closed because recursive process occupancy cannot be
proved there.

## 2. Create the private input

The input is strict JSON: unknown fields and trailing values are rejected. It
must contain exactly one mapping for every present worktree, no mapping for an
absent prunable registration, and exactly one default for each retained record
kind.

This example uses one linked worktree. Add one `linked_worktree` mapping for
each additional present registration reported by `git worktree list
--porcelain`.

```bash
export CUTOVER_LINKED_ROOT="/absolute/path/to/linked-worktree"
export CUTOVER_LINKED_ARCHIVE="/absolute/path/to/linked-worktree-archive"

jq -n \
  --arg private "$CUTOVER_PRIVATE_ROOT" \
  --arg archive "$CUTOVER_ARCHIVE_ROOT" \
  --arg linked "$CUTOVER_LINKED_ROOT" \
  --arg linked_archive "$CUTOVER_LINKED_ARCHIVE" \
  '{
    schema_version: 1,
    expected_public_repository: "abietic/yhc",
    expected_private_repository: "abietic/yhc-private-history",
    mappings: [
      {kind: "main_checkout", source: $private, destination: $archive},
      {kind: "linked_worktree", source: $linked, destination: $linked_archive}
    ],
    defaults: [
      {kind: "ref", classification: "private_recovery", owner: "operator", restore_disposition: "preserve", checksum_policy: "omit_sensitive"},
      {kind: "worktree", classification: "private_recovery", owner: "operator", restore_disposition: "preserve", checksum_policy: "omit_sensitive"},
      {kind: "dirty_path", classification: "private_recovery", owner: "operator", restore_disposition: "preserve", checksum_policy: "omit_sensitive"},
      {kind: "stash", classification: "private_recovery", owner: "operator", restore_disposition: "preserve", checksum_policy: "omit_sensitive"}
    ],
    rules: []
  }' >"$CUTOVER_INPUT"
chmod 600 "$CUTOVER_INPUT"
```

Defaults are conservative. An exact `rules` entry may replace one default when
the operator has already classified that record. Allowed pairs are:

| Classification | Restore disposition | Meaning |
|---|---|---|
| `already_forward_ported` | `retain_archive` | public equivalent exists; retain private evidence |
| `candidate_public_delta` | `reexpress_public` | reimplement through a reviewed public change |
| `private_recovery` | `preserve` | preserve only in the private archive |
| `never_public` | `exclude_public` | never publish this record |
| `unresolved` | `block` | pre-move verification must stop |

Every rule has exactly these fields: `kind`, `source`, `identity`,
`classification`, `owner`, `restore_disposition`, and `checksum_policy`.
For refs and stashes, `source` is the private root and `identity` is the ref
name. For worktrees, `source` is the private root and `identity` is the
worktree's canonical path. For a dirty path, `source` is its worktree root and
`identity` is the exact status code, base64 relative path, and base64 original
path joined by the unit-separator byte. An unmatched or multiply matched rule
is an error; do not guess a dirty-path identity by eye.

Use `sha256` only for an explicitly allowlisted regular dirty file. Keep
`omit_sensitive` for all other records. The manifest stores metadata and
digests, never file bodies, prompts, transcripts, credentials, command lines,
environments, stash contents, or Git object bodies.

## 3. Capture and verify before moving

Do not run the live cutover from an unmerged topic branch. After the recovery
tooling PR is reviewed, merged, and present in the public checkout, run the
targets from that public repository root:

```bash
cd "$CUTOVER_PUBLIC_ROOT"
make cutover-recovery-capture
CUTOVER_PHASE=pre-move make cutover-recovery-verify
```

Success prints only `status=ok` and record counts. The manifest is written with
mode `0600`, atomically replaced, and strictly re-read before capture succeeds.

`pre-move` is point-in-time evidence, not a filesystem or process lock. Close
every task, terminal, editor, indexer, and background process that touches a
move root, then run `pre-move` again immediately before the authorized rename.
If anything can have changed after that result, stop and re-run it.

## 4. Perform the external move and repair

Only the operator performs this step. Move each present linked worktree first
and the main checkout last, using the exact frozen source/destination mapping.
Do not move an absent prunable registration.

After all renames, repair registrations from the archived main checkout:

```bash
git -C "$CUTOVER_ARCHIVE_ROOT" worktree repair "$CUTOVER_LINKED_ARCHIVE"
CUTOVER_PHASE=post-move make cutover-recovery-verify
```

For multiple linked worktrees, pass every moved linked destination to
`git worktree repair`. A successful `post-move` proves the frozen stable sets,
repository identities, dirty metadata or allowlisted hashes, stash identities,
porcelain bytes, and common-directory ownership. It does not authorize pruning
or deleting the archive.

Start every subsequent development task from the public checkout. Keep the
archived private-history origin unchanged as the recovery identity.

## Rollback

Rollback is also operator-owned and uses the same point-in-time rule:

```bash
CUTOVER_PHASE=pre-rollback make cutover-recovery-verify
```

Only after it succeeds, reverse the frozen mappings: move linked worktrees back
first, move the main checkout back last, repair the original linked paths, and
then verify:

```bash
git -C "$CUTOVER_PRIVATE_ROOT" worktree repair "$CUTOVER_LINKED_ROOT"
CUTOVER_PHASE=rollback make cutover-recovery-verify
```

Keep the sealed manifest and the private archive until a separate, explicit
retention decision. Never use this workflow to prune worktrees, drop stashes,
delete dirty files, or publish private-only material.
