# Desktop Session Creation Activation Design

**Status:** active-plan
**Accepted:** 2026-08-13
**Parent product contract:**
[`2026-08-13-yhc-desktop-workbench-forward-port-design.md`](2026-08-13-yhc-desktop-workbench-forward-port-design.md)

> **Ownership:** renderer-side ordering between native workspace selection,
> live-session creation, active-session selection, and background hydration

## Problem

The native picker and app-server already bind the selected directory to the
correct opaque workspace handle and session summary. The renderer currently
waits for transcript, snapshot, execution-settings, and event-stream hydration
before selecting that new summary as the active session.

While hydration is pending, the window continues to render the previously
active workspace. A slow or stalled request therefore makes a successful
workspace selection look ignored. When hydration eventually finishes, the late
selection can also take focus back from a session the user deliberately chose
in the meantime. A second **New session** attempt during the same single-flight
operation is silently coalesced with the first and does not use the second
workspace selection.

## Decision

Adopt an immediate-activation, background-hydration sequence:

1. After `createSession` returns a valid summary, upsert it and select its ID
   immediately.
2. Mark the new session as restoring while transcript, snapshot, execution
   settings, and the event stream are hydrated for that specific session.
3. Hydration may update only the target session. It must never select a session
   or otherwise reclaim focus when it completes.
4. Disable **New session** from the start of workspace selection through the
   end of creation/hydration. The UI must expose a visible creation state rather
   than accepting a second picker result that cannot be honored.
5. If creation fails before the server returns a summary, keep the previous
   active session and surface the existing provider/setup error path. If
   hydration partially fails after the summary exists, keep the new session
   selected and use the existing bounded incomplete-history notice; do not
   delete or hide a server-created live session.

This is a renderer orchestration repair. It does not change the app-server
protocol, provider behavior, durable-history discovery, first-send activation,
lease ownership, or persisted transcript semantics.

## State and interaction contract

The creation busy flag is renderer-local and is not persisted. It begins before
opening the native picker so repeated button or keyboard activation cannot
create overlapping selections. Cancellation clears the flag without changing
session state. The flag also clears in every error and success path.

The new session becomes active as soon as the server response establishes its
identity and workspace label. The composer may remain unavailable while the
session is restoring; availability continues to derive from the session's
existing execution and hydration state. Session-navigation rows remain usable,
so a user can leave the restoring session. Later hydration completion updates
that row in place and preserves the user's current selection.

Saved-session behavior remains unchanged: selecting a durable row reconstructs
history without creating a runtime, and the first explicit user request owns
attach and provider dispatch.

## Deterministic regression evidence

A test-owned orchestration helper will expose the ordering without depending on
timers, Electron, or a live provider. Its tests use controllable Promise
barriers and an independent state oracle:

- with `old/eino-agent` active, a `new/yhc` creation response must make `new`
  active before the hydration barrier is released;
- if the user selects `old` while `new` is hydrating, releasing the barrier
  must leave `old` active;
- while creation is busy, a second attempt must be rejected before another
  workspace selection is opened, then become available after success,
  cancellation, or failure; and
- a hydration failure after creation must retain the selected server-created
  session and its public workspace label.

Focused verification runs the renderer/desktop Node suite and its syntax and
security checks. Final verification follows the repository's diff-bound
`make verify-focused`, committed-tree `make verify-merge`, package build, and
fresh macOS Computer Use acceptance. The physical scenario must visibly prove
the selected `yhc` label appears, Markdown input/send/rendering works, Activity
and Changes remain semantic, and saved history still attaches only on first
send.

## Rollback and claim boundary

The change is independently reversible by restoring the prior renderer
ordering and removing its focused tests. It introduces no data migration and
does not alter server state. The unsigned local `.app` may be described as
physically accepted after the fresh-package scenario passes; signing,
notarization, and distribution readiness remain out of scope.
