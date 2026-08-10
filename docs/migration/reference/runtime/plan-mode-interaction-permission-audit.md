# Plan Mode Interaction And Permission Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-24

> **Ownership:** source-backed comparison of Plan Mode permission precedence,
> approval interaction, further-planning feedback, external-editor handoff,
> focus, and scroll restoration. Accepted execution belongs in
> [`migration/PLAN.md`](../../PLAN.md).

## Observable Question

What project-owned contract should make Plan Mode safe and usable across these
observable boundaries?

1. entering and leaving Plan Mode without changing permissions implicitly;
2. allowing the model to write only its exact Plan file without an ordinary
   permission prompt;
3. reviewing a long Plan and selecting an implementation permission mode;
4. requesting further planning through a complete editor rather than a
   one-line ad hoc buffer; and
5. editing the Plan in an external terminal editor and returning with keyboard,
   mouse, focus, viewport, and approval identity intact.

Plan Mode and worktree lifecycle remain independent. This audit does not create
a shared state, approval, persistence record, or transition between them.

## Snapshots

| Repository | Revision | Working-tree evidence |
|---|---|---|
| Eino-Agent | `b2a8322eba36` | Current working tree, including the unmerged P19 candidate, was reviewed as the user-visible baseline. |
| Claude Code Ripe | `4b9d30f79532` | Reference source verified; only an unrelated `.DS_Store` is untracked. |
| Codex | `66bd101fff6f` | Clean reference snapshot. |
| Crush | `2af939d8e900` | Clean reference snapshot. |
| Grok Build | `a5727c596045` | Clean reference snapshot. |
| OpenCode | `411eff73f026` | Clean reference snapshot. |
| Pi | `c55ae2faa5d8` | Clean reference snapshot. |

These are local source snapshots, not claims about current upstream heads.

### 2026-07-25 Current-Source Revalidation

Eino-Agent master `be1eef648426` was re-read after P19 closeout. P20-F1 is
unchanged: `wrapCanUseTool` evaluates an explicit ordinary `ask` before its
later Plan Write/Edit auto-allow. Exact path and symlink containment,
model-visible filtering, pre-tool hard denial, input-rewrite revalidation, and
typed `ExitPlanMode` approval remain intact. Claude Code Ripe
`4b9d30f79532` and Grok Build `a5727c596045` still support the accepted
deny → internal capability → ordinary permission order. This revalidation
promoted only P20.H0; it does not make later interaction slices executable.

### 2026-07-25 Post-P20.0 Presentation Revalidation

Eino-Agent master `72ad8266e335` was re-read after P20.0 closeout. P20-F1 is
closed by P20.H0. P20-F2's permission semantics are closed by P20.0: every
known non-Plan `ReturnMode` now survives approval and idle abandon. The
remaining presentation still builds static AcceptEdits and Bypass choices, so
either can duplicate the first previous-mode action.

P20.1 remains independently reproduced:

- `PlanDialog` owns `feedbackMode`, `selectedIdx`, and `scrollOff` without an
  explicit Review/Actions focus owner;
- Up/Down always changes the action, Page keys change a raw offset, and Home/
  End do not control the Plan review;
- every wheel event routed to the modal changes the Plan offset even when its
  coordinates are over actions or the footer;
- the render path slices Plan lines but publishes no review/action hitboxes;
  and
- a constant footer estimate can hide sticky regions at compact heights.

Claude Code Ripe `4b9d30f79532` still separates scrollable Plan content from a
sticky action footer. Grok Build `a5727c596045` still owns explicit Preview,
Prompt, and Commenting focus. The promoted P20.1 decision remains `combine`:
adapt those ownership mechanisms, retain Bubble Tea and the P20.0 typed
approval result, and implement project-native coordinate geometry,
de-duplicated actions, and compact layout. Eino/Eino-ext has no relevant
presentation primitive and is not changed.

### 2026-07-25 Post-P20.1 Feedback-Editor Revalidation

Eino-Agent master `f29dba77e5f5` was re-read after P20.1 closeout. P20-F2's
remaining duplicate-action presentation was closed by P20.1, while P20-F3
remained: Feedback still used a widget-local string/rune cursor with no
multiline textarea, paste/undo ownership, effective keymap hints, or semantic
input surface.

The existing project composer already provided the relevant Go/Bubble Tea
baseline. Grok Build `a5727c596045` continued to support reuse of a full prompt
widget under explicit Commenting focus, while Codex `66bd101fff6f` continued
to support separating “stay in Plan” from implementation authorization. The
promoted P20.2 decision therefore remained `combine`: share project textarea,
snapshot, undo, and keybinding mechanisms; retain an independent Plan draft;
and add project-native semantic input styles. Eino/Eino-ext still had no
presentation primitive that would replace this TUI-owned boundary.

P20.2 closed P20-F3 with that contract and added a regression proving typed
Revise bypasses generic denial accounting. At that checkpoint P20-F4-P20-F7
remained reproduced and were owned by P20.3.

### 2026-07-25 Post-P20.3 External-Editor Revalidation

Eino-Agent master `1ef0a9f2bbf7` and the locked Bubble Tea v1.3.10 and
`x/editor` v0.2.0 sources were re-read before implementation. Bubble Tea
`ExecProcess` still restored alternate screen, paste, renderer focus, and
resize, but not mouse. `x/editor` still parsed `EDITOR` arguments and
editor-specific positions but did not read `VISUAL`. The project therefore
kept `x/editor` options, added a shared project-owned `VISUAL` over `EDITOR`
resolver, and used one ordered App terminal-reacquisition helper for Plan,
composer, and suspend/resume.

P20.3 closed P20-F4-P20-F7. Plan callbacks now carry
thread/request/revision/path/generation identity and a complete presentation
snapshot; stale results are ignored before disk reload, errors keep approval
open, and successful reloads preserve the viewport and feedback state. A real
two-round-trip Unix PTY fake-Vim test proves resize, alternate-screen repaint,
arrow selection, PageUp/PageDown, mouse scrolling, and repeated mouse/focus
restoration. Eino/Eino-ext still exposes no replacement for this terminal/TUI
adapter boundary and was not changed.

### 2026-07-25 Post-P20.4 Completion Correction

Eino-Agent master `cb36859` was re-read after the reported P20 closeout. The
engine still rejects an unconfirmed bypass target and preserves exact
request/revision/path/digest settlement, but supported adapters construct the
confirmation flag without the required second user interaction:

- TUI `buildPlanOptions` pre-sets every target confirmed, including bypass and
  a previous ReturnMode that is already bypass; Enter immediately closes the
  dialog;
- plain `readPlainPlanApproval` maps one `b` input directly to confirmed
  bypass;
- ACP exposes one `plan_bypass` option and maps its first selection directly
  to confirmed bypass; and
- current TUI and ACP tests assert that single-action behavior instead of
  proving an intermediate confirmation state.

Two additional current-source gaps remain. TUI
`permissionInteractionResult` infers Revise from nonempty retained feedback,
so a later Esc or force-close can turn explicit Cancel into Revise.
`SemanticPlanFeedbackCursor` supplies foreground/background colors that
Bubbles reverses again; at an empty/end-of-line cell the final background
matches the input surface, while no-color output removes the style entirely.
Existing editor tests assert geometry and semantic token propagation rather
than the final caret cell.

These findings reopen G10 as P20-F8-P20-F10 under the existing `combine`
decision. Claude Code Ripe's explicit risk confirmation, Grok's typed
decision/focus separation, and Codex's separation of stay-Plan from
implementation remain relevant mechanisms from the frozen snapshots. The
accepted repair keeps QueryEngine lifecycle and settlement ownership,
implements two-step confirmation in each supported adapter, makes the TUI
terminal outcome explicit, and adds final-cell cursor acceptance.

A configured `light-daltonized` value silently falling back to Polar Night is
a separate startup-theme compatibility gap, not P20. Unix nano remains the
intentional `x/editor` fallback when neither `VISUAL` nor `EDITOR` is set.

## Audit-Snapshot Eino-Agent Findings

The table below preserves the failures reproduced when this audit was opened.
The dated revalidation sections above are authoritative: original P20-F1-F7
closed, while the post-P20.4 correction reopened G10 as P20-F8-P20-F10.

### Reproduced And Source-Proven Failures

| ID | Observable failure | Current source cause | Consequence |
|---|---|---|---|
| P20-F1 | Writing the exact Plan file can still open an ordinary permission prompt. | `wrapCanUseTool` validates Plan containment, then evaluates ordinary permission rules. An `ask` match calls `promptForTool` before the later Plan Write/Edit auto-allow branch. | The internal Plan capability is presented as an arbitrary filesystem mutation and planning can block on a redundant prompt. |
| P20-F2 | Exit choices do not faithfully preserve the permission mode that existed before Plan Mode. | `PlanApprovalRequest.ReturnMode` is carried by the engine, but `NewPlanDialog` builds a static Default/AcceptEdits/Bypass list and never uses that value. | Entering Plan Mode can silently turn a previous mode into a different implementation mode, making permission behavior hard to predict. |
| P20-F3 | “No, keep planning” has incomplete editing and weak visual state. | `PlanDialog` owns a string, rune cursor, and boolean `feedbackMode`; it implements only basic character movement/deletion. The P19 candidate replaces literal colors but still renders the field with one `EditorPrompt` foreground and no semantic input surface, focus border, placeholder, multiline behavior, paste ownership, or undo. | The fourth option behaves unlike the main composer and has ambiguous focus and poor theme contrast. |
| P20-F4 | Returning from Vim loses terminal interaction state. | `PlanDialog` uses `tea.ExecProcess`. Bubble Tea v1.3.10 `ReleaseTerminal` disables mouse tracking, while `RestoreTerminal` restores alternate screen, bracketed paste, and focus reporting but not mouse tracking. The completion branch returns no `tea.EnableMouseCellMotion`. | Wheel input can fall back to native terminal scrolling or escape-key behavior instead of producing `tea.MouseMsg` for the Plan viewport. |
| P20-F5 | External-editor return discards review state. | `planEditorFinishedMsg` carries only `err`; `ReloadPlan` resets Markdown and `scrollOff` to zero. There is no request/revision, focus, selected action, feedback draft, or viewport snapshot. | A successful edit does not return to the same review position, and a stale editor result cannot be rejected deterministically. |
| P20-F6 | Plan and composer external editors use different command-resolution paths. | The Plan dialog calls `exec.Command(os.Getenv("EDITOR"), path)`, so it ignores `VISUAL` and treats an editor value containing arguments as one executable. The composer already uses `github.com/charmbracelet/x/editor`. | Common values such as `code --wait` and `vim -f` fail for Plan editing even when composer editing works. |
| P20-F7 | The reported Vim round trip is not covered by a terminal test. | Existing Plan tests cover policy/state, dialog Unicode, and generic terminal resume. No PTY scenario performs Plan approval → external editor → return → arrow selection → mouse/page scroll. | Generic green tests cannot prevent this interaction regression. |

The exact wheel takeover follows directly from Bubble Tea's terminal lifecycle.
The complete post-Vim arrow symptom is user-reproduced and consistent with the
missing terminal/focus restoration, but still needs the P20 PTY test before it
is treated as a closed deterministic reproduction.

### Current Strengths To Preserve

- `QueryEngine` owns `Inactive`, `Active`, and `AwaitingApproval`; TUI and ACP
  are adapters, not lifecycle owners.
- model-visible projection and runtime execution both use
  `evaluatePlanToolPolicy`;
- Write/Edit must prove an absolute, clean, exact session Plan path with
  symlink-component rejection;
- `ExitPlanMode` always requires one typed approval for the request ID and Plan
  state revision; generic allow rules, hooks, persisted grants, and bypass mode
  cannot approve it; and
- cold recovery does not revive a historical callback as live authority.

P20 must repair precedence and presentation without replacing those P17
boundaries.

## Cross-Reference Matrix

| Reference | Plan mutation authorization | Exit / further-planning interaction | External-editor and restoration evidence | Decision |
|---|---|---|---|---|
| Claude Code Ripe | Explicit deny is checked first; a current-session Plan path is then an internal editable path and returns `allow` before ordinary ask behavior. Its prefix match is broader than Eino-Agent's exact identity. | A sticky `Select` keeps actions visible while the Plan scrolls. “No, keep planning” is an input option backed by the shared selection/editor behavior. | `VISUAL` precedes `EDITOR`; terminal and GUI editors are distinguished. Terminal return explicitly re-enters alternate screen, restores mouse/focus/extended keys, resumes stdin, resets frames, and repaints. | `adapt` precedence, sticky actions, and restoration; `reject` prefix-wide Plan paths and product-specific options. |
| Codex | Plan is a collaboration mode with strict non-mutation instructions. `apply_patch`/execution tools remain registered, so instruction text is not a sufficient Eino-Agent safety boundary. | A completed `<proposed_plan>` opens a three-choice popup: implement, clear context and implement, or stay in Plan. Staying returns to the normal composer rather than a custom feedback field. Mode switching during a running turn is rejected. | No Plan-specific external-editor round trip. | `adapt` canonical mode ownership, explicit mode-switch message, normal-composer reuse, and running-turn rejection; `reject` instruction-only mutation safety. |
| Crush | No Plan Mode was found. Generic permissions support once/session decisions and first-wins settlement, but have no Plan phase or exact Plan capability. | No Plan approval/further-planning state. | Uses `tea.ExecProcess` for a generic editor and relies on Bubble Tea restoration; no explicit mouse/focus/viewport restoration. | `adapt` only generic scoped-decision concurrency; `reject` treating a normal permission dialog as Plan approval. |
| Grok Build | Session state owns `Inactive/Pending/Active/ExitPending`. `should_auto_approve_edit` is the same predicate used by the edit gate and permission bypass, so exact Plan writes do not prompt. A compatibility carve-out for arbitrary Markdown is broader than the target contract. | Typed `Approve`, `Reject { feedback }`, and `Defer`. The approval UI has explicit `Preview`, `Prompt`, and `Commenting` focus and reuses the full prompt widget. | No equivalent external Plan editor was found, but focus and prompt state are explicit and stashed/restored. | `adapt` one predicate, typed decisions, focus state, and shared prompt editing; `reject` arbitrary-Markdown and yolo leakage during Plan. |
| OpenCode | Plan is a distinct agent permission ruleset: edit is denied globally and allowed for Plan patterns; question and `plan_exit` are allowed. User rules merge later and can override the built-in patterns. | `plan_exit` asks Yes/No and switches to Build only after approval. “No” remains with the Plan agent; no inline feedback editor. | No Plan-specific external-editor lifecycle. | `adapt` permission-backed mode policy and explicit agent transition; retain Eino-Agent's stricter exact capability and engine-owned state. |
| Pi | Core has no Plan Mode implementation; current source only notes that extensions may register a `--plan` flag. | No core Plan approval state. | Its generic editor stops the TUI, launches the editor, and unconditionally restarts plus requests a full render; terminal restart also refreshes dimensions. It does not preserve a Plan viewport. | `adapt` unconditional restart/repaint mechanics only; `reject` inferring a core Plan contract. |

## Important Source Evidence

### Claude Code Ripe

- `src/utils/permissions/filesystem.ts:1220-1249` applies explicit edit deny
  before the internal editable-path result.
- `src/utils/permissions/filesystem.ts:1479-1496` grants current-session Plan
  files without prompting.
- `src/components/permissions/ExitPlanModePermissionRequest/ExitPlanModePermissionRequest.tsx:512-539`
  keeps the decision control in a sticky footer.
- The same component at `:737-744` defines “No, keep planning” as an input
  option rather than a hand-written string buffer.
- `src/ink/ink.tsx:357-418` explicitly restores alternate screen, mouse,
  stdin, focus reporting, extended keys, and a full repaint after a terminal
  editor.

### Codex

- `codex-rs/collaboration-mode-templates/templates/plan.md:5-39` defines the
  mode and its non-mutation contract.
- The same template at `:92-128` separates a complete proposed Plan from user
  choice to remain planning.
- `codex-rs/tui/src/chatwidget/plan_implementation.rs:9-113` owns the
  implement/current-context, implement/fresh-context, and stay-Plan actions.
- `codex-rs/tui/src/chatwidget/input_flow.rs:199-222` rejects collaboration
  mode changes while a turn is running.
- `codex-rs/core/src/tools/spec_plan.rs:745-749` still registers
  `apply_patch` without a Plan-mode filter; Eino-Agent therefore must not rely
  on instructions alone.

### Grok Build

- `xai-grok-shell/src/session/plan_mode.rs:20-51` defines the session-owned
  phase machine.
- The same file at `:179-184` names the exact Plan edit predicate as the
  permission-prompt bypass.
- `xai-grok-shell/src/session/acp_session_impl/tool_calls.rs:145-180` reuses
  that predicate for the edit gate; `:990-1008` bypasses ordinary permission
  handling only for the same admitted edit.
- `xai-grok-workspace-types/src/types/plan_mode.rs:45-73` defines typed
  approve/reject/defer outcomes.
- `xai-grok-pager/src/views/plan_approval_view.rs:37-108` owns explicit review,
  prompt, and commenting focus; `app/agent_view/plan.rs:299-394` routes Tab,
  Esc, newline, submit, and prompt editing through that state.

### OpenCode

- `packages/opencode/src/agent/agent.ts:156-178` builds a Plan-specific
  permission ruleset with a broad edit deny and Plan-pattern allows.
- `packages/opencode/src/tool/plan.ts:15-76` asks the transition question and
  creates the synthetic Build-agent message only after approval.
- `packages/opencode/src/session/prompt/plan-mode.txt:1-67` separately states
  the model workflow; permission rules, not this text alone, enforce edits.

### Pi And Crush

- Pi `packages/coding-agent/src/modes/interactive/components/extension-editor.ts:116-130`
  stops the TUI and unconditionally restarts plus fully renders in `finally`.
- Pi `packages/tui/src/terminal.ts:134-167,426-451` restores raw mode and
  refreshes dimensions after restart.
- Crush `internal/permission/permission.go:114-278` supplies scoped,
  first-wins generic permission settlement, but not Plan semantics.
- Crush `internal/ui/model/ui.go:3499-3538` uses `tea.ExecProcess` for a
  generic editor without a Plan viewport/focus restoration contract.

## Recommended Project Contract

### Adoption

`combine`:

- `preserve` the P17 QueryEngine phase owner, exact path and symlink
  containment, model-visible filtering, runtime revalidation, structured Exit
  approval, and cold-resume normalization;
- `adapt` Claude's deny → internal Plan capability → ordinary ask precedence;
- `adapt` Grok's single gate/bypass predicate, typed decision vocabulary,
  explicit focus states, and shared prompt editor;
- `adapt` Codex's canonical mode indicator, running-turn switch rejection, and
  separation between Plan finalization and implementation submission;
- `adapt` Claude/Pi terminal restart, full repaint, and input-mode restoration;
  and
- implement semantic styling, request identity, viewport restoration, and
  cross-entrypoint behavior as `project-native`.

### Permission Precedence

| Order | Decision owner | Exact Plan Write/Edit result |
|---:|---|---|
| 1 | Registry/`--tools`, canonical name, active Plan phase, exact path, owner, and symlink containment | Fail closed if any identity/admission check fails. |
| 2 | Pre-tool hard deny and explicit permission `deny` | Deny; no prompt and no capability override. |
| 3 | Engine-issued exact Plan-file capability | Allow without generic prompt, session grant, persisted rule, classifier, or denial tracking. |
| 4 | Ordinary permission `allow`/`ask`, mode defaults, approvals, classifier | Reached only for non-capability operations. |
| 5 | Tool runtime revalidation | Recheck the exact capability after any hook/input rewrite before executing. |

An `ask` rule must not intercept the exact Plan capability. An `allow` rule,
hook allow, bypass mode, or persisted grant must not make another mutation
Plan-safe. A hook or explicit rule deny remains authoritative.

### Interaction Ownership

- Engine state remains `Inactive`, `Active`, or `AwaitingApproval`.
- TUI-only presentation state becomes explicit:
  `Review`, `Actions`, `Feedback`, `BypassConfirmation`, and
  `ExternalEditor`.
- Up/Down changes the selected action only in `Actions`; it scrolls the Plan
  only in `Review`; it edits text only in `Feedback`.
- Page keys and a wheel event over the Plan always target its bounded
  viewport. Pointer input never leaks to chat below the modal.
- The further-planning field reuses the established textarea/composer editing
  contract and theme tokens. It is not another editor implementation.
- External editor return validates the request owner, restores terminal
  capabilities, reloads the exact file, preserves/clamps the viewport, and
  returns to the previous focus and draft.

## Explicit Rejections And Deferrals

- `reject` a directory prefix, arbitrary Markdown, or wildcard as the Plan
  capability;
- `reject` instruction-only mutation control;
- `reject` using generic permission allow/deny as the Plan transition result;
- `reject` making the TUI the Plan lifecycle owner;
- `reject` coupling Plan approval to worktree creation or cleanup;
- `defer` line-specific Plan comments until freeform feedback and the external
  editor round trip are reliable; and
- `defer` a new-thread/fresh-context implementation handoff until a separate
  session-history user outcome is accepted.

## Adoption Decision

`combine`, with the detailed implementation contract in
[`migration/plans/p20-plan-mode-interaction.md`](../../plans/p20-plan-mode-interaction.md).
