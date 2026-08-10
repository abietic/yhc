# 2026-07-25 Dirty-Worktree Recovery

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption decision, compatibility effect,
> rollback, and verification evidence for recovering useful behavior from the
> pre-reset P19/TUI workspace

## User Problem

Resetting the integration checkout to `origin/master` removed useful
uncommitted behavior that had not yet entered a reviewable PR. The visible
regressions included clicking the new-message pill no longer jumping to the
bottom and Shift+Tab no longer completing the explicit yolo-mode cycle. A
broader 83/86-file workspace also needed an auditable disposition instead of
another all-or-nothing checkout.

## Decision

This recovery is `project-native` for current runtime ownership and `combine`
for the retained G9 design:

- treat the two Git snapshots as evidence, not authoritative replacement
  trees;
- port only missing observable behavior onto current P19/P20 APIs;
- keep already-landed and evolved files on current `master`;
- reject stale tests, duplicate documentation owners, and mixed-width
  Markdown-table shortcuts; and
- retain the G9 single-parser/single-`WidthProfile` design as a future
  accepted-slice candidate without changing production table rendering.

The exhaustive snapshot and path evidence is
[`migration/verification/p19-dirty-worktree-recovery.md`](../../verification/p19-dirty-worktree-recovery.md).

## Delivered Behavior

- Primary clicks inside the rendered new-message pill reset chat follow state;
  modal/sidebar/expand ownership still has precedence.
- Untouched recalled history suppresses autocomplete until the first edit, so
  Up/Down remains history traversal. Compact layouts reserve bounded hint rows
  and render active candidates before queued previews.
- `RuntimeStateStore` owns cumulative active time. Human waits, pauses, and
  terminal states freeze the clock; status and spinner share its projection.
- Shift+Tab follows Default → Plan → confirmation → Bypass → Default. Cancel
  preserves Plan. The engine accepts the confirmed Plan → Bypass transition
  only at an idle boundary; active turns and approvals reject it.
- Generic idle Plan abandon still restores the saved `ReturnMode`, and
  model-issued `ExitPlanMode` still requires typed reviewed-byte approval.
- Question-dialog backspace removes a complete UTF-8 rune, while zero and
  narrow dimensions avoid invalid repeat counts or slice bounds.
- The detailed G9 display-cell audit is restored and revalidated against
  current source ownership.

## Compatibility And Safety

The confirmed Shift+Tab path is not a new model authority. A dedicated TUI
risk dialog supplies a `user_confirmed` transition source, which can target
only bypass and remains subject to idle/approval checks. ACP and generic mode
controls do not gain this source.

Runtime timing fields are in-memory projections, not durable checkpoint or
wire-schema changes. On replay, no disconnected wall-clock interval is
charged; a new running event starts a fresh active segment.

The recovery changes no Eino/Eino-ext dependency, Graph topology, provider
route, transcript format, or permission grant scope.

## Verification

Focused tests cover:

- click hitbox geometry and full mouse dispatch;
- history traversal, edit re-enablement, and compact candidate priority;
- active-time wait/pause/terminal/cross-turn/restore/eviction semantics;
- status and spinner sharing the engine clock;
- confirmed, cancelled, active-turn, and approval-time bypass transitions;
  and
- Unicode backspace plus zero/narrow dialog geometry.

Repository formatting, lint, test, build, new-lint, documentation, manifest,
and diff gates passed before merge.

## Rollback

Revert the recovery as one unit. Do not restore the raw snapshot tree over
current `master`: that would also roll back later P19/P20 behavior. If only
the Shift+Tab contract is rejected, revert the `user_confirmed` engine source,
TUI confirmation path, and its tests together so Plan never silently becomes
bypass.
