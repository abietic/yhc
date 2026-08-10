# P19 Dirty-Worktree Recovery Audit

**Status:** verification
**Last verified:** 2026-07-25

> **Ownership:** evidence and disposition for the uncommitted P19/TUI workspace
> that was removed from the `master` checkout before every useful change had
> entered a reviewable PR. Current behavior belongs in architecture documents,
> accepted future work belongs in `PLAN.md`, and completed delivery belongs in
> `history/`.

## Conclusion

The reported “85 files” were not physically destroyed. Two reachable Git
objects retained the work:

| Snapshot | Parent | Time | Files | Meaning |
|---|---|---|---:|---|
| `8ae743604a10f40c2ea58053277f2468b4a20248` | `fed0d6c476cfe9c33ec21b40a3518fff5701a4ff` | 2026-07-25 02:09 +08:00 | 83 | newest coherent pre-P19.2.1 dirty workspace; primary recovery source |
| `c7a6afec7dcd8e73ce05c30ae16b928f3dc174fa` | `28e7bed1218432393c1aa09d61ba2888db2e1daf` | 2026-07-24 16:24 +08:00 | 86 | earlier automatic checkpoint with additional provider/ACP/P19-design work |

The main branch reflog records:

```text
2026-07-25 04:40:40 +0800
4c886bd11600aed78052b7ce0fb8e9fd2e288d9b
reset: moving to origin/master
```

The failure was procedural: the reset happened after some changes had been
split into PRs, but before every useful hunk and document had an explicit
disposition. A checkpoint proved recoverability; it did not prove that the
decomposition was complete.

The primary snapshot is protected by local recovery branch
`codex/wip-pre-p19-2-1-reconcile-20260725`; the earlier checkpoint is protected
by `codex/recovery-p19-wip-20260724`.

## Recovery Method

The recovery does not check out all 83 paths over current `master`. That would
revert later P19/P20 changes in 62 evolved files and reintroduce obsolete
dialog fields, stale line anchors, a duplicate root guide, and an insufficient
Markdown-table width heuristic.

Instead:

1. compare each snapshot blob with its parent and current `origin/master`;
2. classify every primary-snapshot path exactly once;
3. port missing observable behavior onto current owners and current APIs;
4. add independent regression tests before accepting a recovered behavior;
5. preserve the G9 design evidence but defer its old runtime shortcut; and
6. run focused and repository gates before closeout.

Reproduction commands:

```bash
git show --stat 8ae743604a10f40c2ea58053277f2468b4a20248
git diff --name-status \
  fed0d6c476cfe9c33ec21b40a3518fff5701a4ff \
  8ae743604a10f40c2ea58053277f2468b4a20248
git show --stat c7a6afec7dcd8e73ce05c30ae16b928f3dc174fa
git reflog show --date=iso master
```

## Observable Behaviors Recovered

| Behavior | Current owner | Recovery contract |
|---|---|---|
| Click “new messages / Jump to bottom” | `internal/tui.App` and `ChatView` | A primary click inside the rendered pill's exact chat-relative hitbox calls `ResetFollow`; modal/sidebar/expand ownership still wins first. |
| History traversal versus autocomplete | composer state and key resolver | Untouched recalled text suppresses command/file/mention hints so Up/Down continue history traversal; the first edit re-enables hints. Plain recalled text exits command mode. |
| Compact command hints | `calculateLayout` and hint composition | Compact mode reserves at most seven hint rows; active autocomplete precedes queued previews so clipping cannot hide the selected command. |
| Canonical active time | `RuntimeStateStore` | Cumulative thread time advances only while the canonical thread is running, freezes for human wait/pause/terminal states, accumulates across turns, and drives both status and spinner. |
| Shift+Tab yolo cycle | QueryEngine execution controls plus TUI risk dialog | Default → Plan; Plan → risk confirmation while Plan remains active; confirmed user action → Bypass; cancel → Plan; Bypass → Default. Active turn and AwaitingApproval remain fail-closed. |
| Unicode and narrow question dialogs | `QuestionDialog` | Backspace removes one UTF-8 rune, never one byte; zero/very narrow geometry cannot form negative slices or repeat counts. |
| G9 evidence | reference audit | The detailed single-parser/single-`WidthProfile` design is restored, while its old mixed-width inline-table implementation stays deferred. |

The yolo recovery deliberately differs from a model-issued `ExitPlanMode`.
Typed reviewed-byte approval still owns model Plan exit. The new
`user_confirmed` transition source is available only after the adapter supplies
an explicit bypass-risk confirmation and the engine proves the boundary idle.

## Primary 83-Path Disposition

Counts are exhaustive:

| Disposition | Count |
|---|---:|
| recovered or ported onto current owners | 14 |
| restored as design evidence | 1 |
| already landed and subsequently evolved | 58 |
| deliberately superseded or deferred | 10 |
| **total** | **83** |

### Recovered or ported onto current owners — 14

```text
engine/runtime_events.go
engine/runtime_state.go
engine/runtime_state_test.go
internal/tui/app.go
internal/tui/dialog_unicode_test.go
internal/tui/history_hint_suppress_test.go
internal/tui/key_actions.go
internal/tui/layout.go
internal/tui/question_dialog.go
internal/tui/responsive_layout_test.go
internal/tui/status_timing_test.go
internal/tui/sticky_header_test.go
internal/tui/testdata/app_layout.golden
internal/tui/welcome_lifecycle_test.go
```

Some tests moved to current owners rather than reviving an obsolete filename:
runtime timing uses `engine/runtime_timing_test.go`; pill geometry extends
`internal/tui/sticky_header_geometry_test.go`.

### Restored as design evidence — 1

```text
docs/migration/reference/tui/markdown-table-display-cell-audit.md
```

The document is revalidated against current source anchors. Runtime
implementation remains G9 work and is not smuggled into this recovery.

### Already landed and subsequently evolved — 59

```text
docs/architecture/tui/README.md
docs/architecture/tui/contracts/responsive-layout.md
docs/architecture/tui/contracts/runtime-events.md
docs/architecture/tui/contracts/terminal-lifecycle.md
docs/migration/PLAN.md
docs/migration/REMAINING.md
docs/migration/STATUS.md
docs/migration/history/runtime/p17-plan-mode-runtime.md
docs/migration/manifest.yaml
docs/migration/plans/README.md
docs/migration/plans/p20-plan-mode-interaction.md
docs/migration/reference/README.md
docs/migration/reference/runtime/plan-mode-interaction-permission-audit.md
docs/migration/reference/runtime/plan-mode-lifecycle-audit.md
engine/commands/registry.go
internal/tui/agent_thread_picker.go
internal/tui/agent_wizard.go
internal/tui/background_tasks.go
internal/tui/chat.go
internal/tui/chat_integration.go
internal/tui/command_palette.go
internal/tui/composer_border_test.go
internal/tui/composer_mentions.go
internal/tui/dialog.go
internal/tui/error_display.go
internal/tui/expand_search.go
internal/tui/glyph_test.go
internal/tui/help.go
internal/tui/highlight_verify_test.go
internal/tui/markdown.go
internal/tui/markdown_bench_test.go
internal/tui/markdown_theme_test.go
internal/tui/mascot.go
internal/tui/mascot_idle_test.go
internal/tui/mascot_welcome_test.go
internal/tui/mcp_approval.go
internal/tui/mcp_settings.go
internal/tui/message_selector.go
internal/tui/model_picker.go
internal/tui/parity_test.go
internal/tui/permission_prompt.go
internal/tui/plan_dialog.go
internal/tui/resume_dialog.go
internal/tui/search.go
internal/tui/session_view_state.go
internal/tui/spinner.go
internal/tui/spinner_aurora_test.go
internal/tui/streaming.go
internal/tui/styles.go
internal/tui/teams.go
internal/tui/testdata/product_states.golden
internal/tui/theme.go
internal/tui/theme_contrast_test.go
internal/tui/theme_palette_test.go
internal/tui/theme_propagation_test.go
internal/tui/thread_view_state.go
internal/tui/tools.go
internal/tui/welcome.go
```

These paths are not “ignored.” Their current blobs differ because later P19
and P20 PRs retained the intended behavior while changing APIs, styles,
geometry, or tests. Replacing them with snapshot blobs would be a rollback.

### Deliberately superseded or deferred — 10

```text
PROJECT_GUIDE.md
docs/architecture/code-map.md
docs/architecture/platform/onboarding.md
docs/architecture/tui/contracts/sessions.md
internal/tui/app_layout_golden_test.go
internal/tui/markdown_table_inline_test.go
internal/tui/nocolor_gate_test.go
internal/tui/product_states_golden_test.go
internal/tui/pty_workflow_unix_test.go
internal/tui/table_render.go
```

Path-specific reasons:

- `PROJECT_GUIDE.md` duplicates the current `docs/architecture` ownership tree
  and would create a second, rapidly stale architecture owner.
- The three architecture diffs change only old `app.go` line anchors; current
  anchors must be maintained through `docs-check`, not restored from the WIP.
- The three golden/PTY diffs only remove deterministic `sessionStart`
  normalization. The recovery retains the startup timer as a pre-event
  compatibility fallback, so those removals are no longer valid.
- The hardcoded-color source gate already evolved into
  `internal/tui/semantic_color_test.go`.
- The inline-table test and `table_render.go` patch parse raw cell Markdown and
  mix global theme/width owners. They prove styling in isolation but violate
  the restored G9 contract and are deferred to G9.C/G9.D.

## Earlier 86-File Checkpoint

The older checkpoint has 19 paths absent from the primary 83-path snapshot:

| Group | Paths | Disposition |
|---|---|---|
| P19 design/history | `docs/migration/history/tui/README.md`, `docs/migration/history/tui/p19-revontuli-closeout.md`, `docs/migration/plans/p19-tui-revontuli-design/README.md`, six demo assets/pages, `docs/migration/plans/p19-tui-revontuli-identity.md` | Current per-slice P19 history and design files supersede the monolithic closeout. The old closeout claimed completion before the work had actually entered PRs and must not be restored. |
| provider/stream assembly | `engine/execution/stream_processor.go`, `engine/messages/normalize.go`, `engine/messages/sanitize_test.go`, `engine/provider/merge_parts_test.go`, `engine/provider/provider.go`, `engine/provider/stream_sanitizer_test.go` | Structured output-part preservation landed later in `f889b6b`. The old newline/fragment sanitizer is not restored because it can rewrite legitimate paragraph boundaries; current structured replay tests remain authoritative. |
| chat follow offset | `internal/tui/chat_scroll_test.go` | Landed independently in `b48d92a`; current regression test is present. |
| ACP raw input | `server/acp/agent.go`, `server/acp/agent_session_test.go` | Landed independently in `bdf72c0`; current production and protocol tests are present. |

## Prevention Rules

1. Never reset a dirty integration checkout merely because an automatic
   checkpoint exists.
2. Before reset, compare checkpoint parent → checkpoint and checkpoint →
   target; give every path an explicit `landed`, `recover`, `defer`, or
   `reject` disposition.
3. Preserve the raw checkpoint on a named local ref before decomposition.
4. Keep one reviewable behavior per PR, but maintain a recovery ledger until
   every source hunk is accounted for.
5. Treat semantic deletion—such as changing the Shift+Tab mode cycle—as an
   observable product change requiring a replacement UX or explicit adoption
   decision.
6. Run focused behavior tests and the full Makefile gates before calling the
   recovery complete.

## Verification

Focused evidence covers:

- runtime active-time pause/resume/terminal/cross-turn/restore/eviction;
- confirmed and unconfirmed Plan → Bypass transitions plus owned boundaries;
- click hitbox and full mouse dispatch;
- history traversal, edit re-enablement, and command-mode exit;
- compact hint geometry and selected-item visibility;
- status/spinner use of the same engine clock; and
- Unicode backspace and narrow/zero-height question geometry.

Current closeout evidence:

| Gate | Result |
|---|---|
| `go test ./engine ./internal/tui` | pass after accepting the intentional compact-hint golden change |
| targeted engine `go test -race` | pass for active timing and confirmed bypass transitions |
| `make fmt` | pass |
| `make lint` | pass |
| `make test` | pass, 4,733 tests |
| `make build` | pass for Linux amd64, macOS amd64/arm64, and Windows amd64 |
| `make lint-new` | pass, zero new issues |
| `make docs-check` | pass, 132 reachable Markdown files and 1,810 checked local links |
| manifest check | pass, 1,884 reference files classified and 814 mapped |
| `git diff --check` | pass |

The isolated worktree does not contain the ignored `.reference/` checkout.
For the successful manifest-backed docs gate, it temporarily consumed the
existing main-checkout reference snapshot and removed that worktree-only link
after verification. No reference file or manifest entry changed.
