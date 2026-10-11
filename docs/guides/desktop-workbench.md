# Desktop Workbench

**Status:** current
**Last verified:** 2026-10-11

> **Ownership:** how an operator builds, starts, uses, and recovers the local
> YHC Electron workbench. Runtime and protocol detail belong to the
> [Desktop workbench architecture](../architecture/desktop-workbench.md).

## Outcome

The Desktop workbench puts session navigation beside the conversation, with
Activity and Changes available on demand. It starts a loopback YHC app-server, keeps the renderer out
of direct filesystem and process access, and sends normal coding turns through
the existing `QueryEngine` only after the user submits a prompt. While one Turn
is active, the same composer can durably queue later text prompts without
interrupting it.

## Prerequisites

- Go and Node/npm compatible with this repository's lockfile.
- A provider configuration that can run YHC tool calls. Environment-derived
  configuration may be used; the Desktop provider dialog can store a replacement
  profile only when operating-system secure storage is available.
- A local working directory to select as the workspace.

The provider dialog intentionally refuses to change a profile while live
Desktop sessions exist. Close them first so one process cannot silently mix
provider credentials or routing inside an active conversation.

Wait for workspace selection/creation to finish before opening provider setup;
new creation is disabled during a provider save. If failed workspace creation
is followed by a successful credential save, the restarted backend opens the
native picker again. Choose the workspace anew; cancelling creates no session.
A failed credential save keeps its error and does not reopen the picker. A
successful creation clears only its earlier creation-error banner, preserving
new unrelated errors and notices in existing conversations.

On macOS, starting with no saved provider profile does not probe Keychain or
request secure-storage approval. Saving the first profile or opening an
existing encrypted profile may still require normal Keychain interaction and
fails closed when encryption or decryption is unavailable. An unsigned QA build
is not suitable evidence for that prompt or identity boundary.

## Build and start

For development, build and stage the host backend and launch Electron:

```bash
make desktop-dev
```

For the local packaged-app smoke path:

```bash
make desktop-package
```

The package command creates a local artifact under `desktop/dist/`.
`app.asar` contains only the seven declared Desktop host files, while the WebUI
tree and platform backend under `resources/` must match the staged source bytes
exactly. Unix backends must remain executable. `resources/licenses/` retains
the YHC and Marked material plus the license and Chromium third-party
collection from the exact pinned Electron runtime. Packaging fails when any of
these contracts is incomplete, unsafe, or divergent. CI repeats the unpacked
assembly for Linux x64 without uploading it as a release artifact. The output
is still ignored QA evidence, not proof of code signing, notarization, or
release readiness.

CI also assembles, verifies, and launches native unpacked outputs on macOS
Intel, macOS Apple Silicon, and Windows x64 runners. All three rows verify
bounded bootstrap, preload, bundled-backend identity, and graceful shutdown;
both macOS rows additionally verify last-window restoration. None of these rows
creates, signs, uploads, or validates final installer media.

`desktop/package.json` is the Desktop version source. Its canonical version has
no `v` prefix. The supported Desktop Make targets depend on that manifest and
inject its version into the staged Go backend; on startup, Electron rejects a
backend that reports a different or malformed identity. Once connected, the
sidebar footer shows the accepted version, short commit, and `dirty` marker
when applicable. This is an accidental-mismatch guard, not proof that a binary
is authentic or signed.

On a Linux x64 host with `xvfb-run`, the normal-shutdown unpacked lifecycle gate
is:

```bash
make desktop-unpacked-lifecycle-smoke-linux-amd64
```

It uses a fresh HOME/XDG profile with no inherited provider credentials,
launches the packaged renderer, crosses preload IPC to identify the bundled
backend, closes the window, and requires both the app and that backend process
to exit. To exercise the packaged Linux unexpected-exit path separately, run:

```bash
make desktop-unpacked-crash-containment-smoke-linux-amd64
```

The crash target verifies the backend executable and Linux process start time,
immediately rechecks that identity, and only then sends `SIGKILL` to that PID.
This is a best-effort pre-signal guard, not an atomic pidfd guarantee. It
requires Electron and the renderer to remain alive, the fixed restart guidance
and checked entry controls to stay disabled, and `app:get-info` to return no
accepted bootstrap throughout an 11-second observation window. The app must
then close normally without accepting a replacement bootstrap during that
window. All local Linux lifecycle targets keep Chromium's sandbox enabled. The
hosted Linux CI job uses their dedicated `-ci` variants, which opt into
`--no-sandbox`; those runs therefore do not validate the production OS
sandbox or physical UI behavior.

To exercise the same unexpected exit while a real attached turn has emitted
unfinished assistant text, run:

```bash
make desktop-unpacked-active-turn-crash-smoke-linux-amd64
```

This gate starts a smoke-owned loopback Responses provider, uses the packaged
backend to create one canonical completed session inside an isolated HOME and
workspace, then launches Desktop with only that fixture provider environment.
Desktop discovers the saved session through its normal catalog, and the first
new prompt uses the production attach path. The gate waits until the provider
has opened the second stream and the public app snapshot reports a non-empty
AppServer active turn plus exactly one sentinel assistant message with its own
non-empty runtime turn ID and `completed: false`. It then applies the same
identity-checked backend kill and requires the partial assistant text,
session labels, and submitted prompt to remain visible while interactions and
backend-dependent controls stay unavailable. No `terminal` or `turn.finished`
event may appear, the loopback provider must receive exactly one seed and one
active request, and the 11-second no-restart observation still applies. The
fixture does not read host provider credentials or claim compatibility with an
arbitrary OpenAI-compatible endpoint.

On an Intel macOS host with an unlocked GUI session, run:

```bash
make desktop-unpacked-lifecycle-smoke-darwin-amd64
```

On an Apple Silicon macOS host, the corresponding unsigned unpacked-app gate
is:

```bash
make desktop-unpacked-lifecycle-smoke-darwin-arm64
```

It starts the exact `YHC.app/Contents/MacOS/YHC` executable with a fresh
`--user-data-dir`, verifies the packaged renderer and frozen preload bridge,
and observes the bundled backend in the app's isolated process group. It then
uses the browser-level graceful-close command and requires both the app and the
original backend process to disappear normally. Darwin process identity is a
best-effort `ps` observation of PID, process group, start time, state, and
resolved command path; it is not an atomic kernel ownership guarantee. A locked
or unavailable GUI session cannot satisfy this gate.

The stronger macOS window-restoration gates used by both native CI rows are:

```bash
make desktop-unpacked-window-reopen-smoke-darwin-amd64
make desktop-unpacked-window-reopen-smoke-darwin-arm64
```

It first closes only the last renderer window and confirms that renderer target
is gone while the original app and backend stay alive. It then launches the
same app again, requires the short-lived second process to exit normally, and
requires the primary process to expose one newly bootstrapped renderer with a
different target identity. A final browser-level close must still end the
original app and backend normally.

Both macOS rows remain automation evidence: they do not exercise Finder/Dock
launch, foreground focus, native picker interaction, secure-storage prompts,
signing, notarization, or a physical display acceptance scenario.

On a Windows x64 host, run both native unpacked lifecycle gates with:

```powershell
make desktop-unpacked-native-smokes-windows-amd64
```

It starts the exact `desktop/dist/win-unpacked/YHC.exe` with an isolated user
profile, verifies the packaged renderer, frozen preload bridge, and bundled
`resources\bin\yhc.exe` process identity, then uses the browser-level graceful
close and requires both original process identities to retire. A second launch
rechecks that backend's PID, UTC creation time, and normalized executable before
terminating only the backend. It requires one backend-exit notification, fixed
Offline guidance, disabled controls, no replacement backend for 11 seconds,
and a normal Electron close. Failure cleanup
queries fresh `Win32_Process` PID, UTC creation time, executable path, and the
visible parent-child tree before invoking `taskkill /PID ... /T /F`. Missing or
changed identity, an unknown descendant executable, or an unowned backend fails
closed. The final identity checks and `taskkill` operations remain separate, so
these are best-effort PID-reuse guards rather than an atomic Windows
process-handle or Job Object ownership guarantee.

The Windows row is native automation evidence, not proof of Start-menu launch,
foreground focus, file-picker interaction, installer behavior, Authenticode,
SmartScreen reputation, or physical UI acceptance. It does not exercise macOS
window restoration.

Run `make desktop-check` for deterministic Node/unit and syntax checks. The
browser-only transport can also be started explicitly with:

```bash
yhc serve app --web
```

## Edit shared defaults

Open **Settings** in the sidebar or press Cmd/Ctrl+comma. Desktop and the Web
companion use the same Go settings service as TUI `/settings` and CLI
`yhc config settings`. Credentials remain separate; the editor neither returns
keys nor tests a provider connection.

1. Choose **User defaults**, or **Workspace overrides** when a live session is
   open. Project scope is resolved from that session, not a page-supplied path.
2. The editor starts with model settings. Filter by category or search across
   categories; changing filters preserves field nodes and unsaved edits.
3. Inspect the stored value, effective value and source before editing. A model
   profile is selected from the host's catalog, never typed into an empty list.
   Switching between a profile and explicit model overrides requires a
   confirmation before one revision-bound save removes conflicting overrides.
   A non-editable blocking setting must be changed outside this editor.
4. **Reset override** / **Use user default** stages a deletion, including for
   stored false or zero values. **Undo reset** restores the prior draft. No
   file changes until **Save changes**.
5. A concurrent-write conflict preserves the draft and requires an explicit
   **Reload**; YHC does not silently overwrite another writer. Leaving a dirty
   editor also requires confirmation.

Model, provider, turn limit and permission defaults apply to new sessions;
the model/effort/access controls beside the composer still change only the
current session. Theme and reduced-motion preferences preview their effective
values in the current interface. Saving `prompt_suggestions` does not itself
generate or send a prompt.

On normal-height windows, only the settings body scrolls. Short windows fall
back to scrolling the whole dialog so an invalid field remains reachable.
Credential guidance is a native expandable section near model settings;
Desktop can open its existing secure-profile dialog after confirming any
discarded draft, while Web only shows host-side guidance.

The Desktop vault is a fallback, not a second defaults store: shared routing
and existing host credentials take precedence, and a saved key is reused only
for the same provider and endpoint. Opening a workspace lets the runtime
resolve that configuration; missing vault metadata alone does not block it.

### Edit permission rules deliberately

Use the permission-rules section only with the scope shown by Settings. A
workspace scope requires the selected live session; no path can be entered or
chosen here. Build an add/remove batch with the typed controls, then select
**Save** to submit its revision-bound changes.

**Cancel** and **Discard** do not submit a write; a revision or validation
rejection leaves the batch unapplied. A timeout or network failure is
indeterminate: Desktop and Web abort the request after 15 seconds, including
a stalled response body. The editor keeps the exact draft but disables editing
and saving until an explicit **Reload** reads a fresh revision; cancelling its
discard confirmation preserves the draft, and a late response cannot clear it. A
`runtime_reload_failed` diagnostic means the rule file may already be committed
but the current runtime did not reload it; it is not evidence that the save was
rolled back. YHC will not use this editor to answer a pending permission or
Plan-approval interaction.

See [configuration](configuration-and-providers.md#edit-persistent-defaults)
for the nine-field whitelist and [experience boundaries](../architecture/workbench-experience.md)
for ownership and verification limits.

## Use a session

1. Select **New session** and choose a workspace in the native picker.
2. The host exchanges the selected path for an opaque workspace handle; the
   page never receives the path. Choose model, reasoning, and permission mode
   before the first turn if desired.
3. Type a request and send it. The center panel renders assistant Markdown,
   code, lists, tables, and safe links; untrusted HTML and unsafe URL schemes
   are not injected into the page.
4. While that Turn is running, waiting, or stopping, the button reads
   **Queue**. Submit later text prompts there. Accepted items appear below the
   composer in execution order. **Remove** cancels an item only while it is
   still pending; once processing starts, use the normal active-Turn **Cancel**
   control instead. A failed or uncertain queue request keeps the draft so the
   same admission can be retried safely, including after the active Turn has
   already ended: the durable admission receipt acknowledges the original
   request without running it twice. Queue updates carry a server revision, so
   a late response cannot replace a newer list or clear text typed for a later
   attempt.
5. Use the right Activity panel to follow meaningful lifecycle changes such as
   a turn, tool, task, Agent, or waiting interaction. It is deliberately not a
   raw streaming-event log.
6. When a decision is needed, use the card in the conversation: permissions,
   questions, Plan approval, and repeated-tool calls each have their own typed
   controls. Do not answer one kind of card by pasting JSON into chat.

### Inspect tasks without controlling them

Open **Tasks** only while the selected session is live. Refresh reads that
session's work items and Agent executions; it does not attach saved history or
control an Agent. Open an execution only when its full execution session and
thread identity are available. Its terminal-output preview is limited to 64 KiB.
That is the only explicitly requested full-output payload; shown metadata text
is independently bounded.

**Open conversation** reads the selected child transcript in pages of at most
32 messages, within a total 256-item display window. It does not switch the
parent session or alter its draft, cursor selection, or Undo history. If the
execution is replay-only, pre-dispatch, incomplete, or has changed since the
read began, return to Tasks and refresh instead of assuming a different Agent's
data is equivalent.

### View current MCP availability

The MCP panel reports the selected live session's current servers, exposed
tools, diagnostics, and revision. It is read-only: use the normal shared
configuration and session lifecycle to change MCP setup. This screen neither
authorizes a connection nor toggles, reconnects, refreshes, or restarts a
server at runtime.

An ordinary Send awaiting its admission reply does not prevent further typing.
The reply clears only its untouched original draft. Later edits remain in their
own session, even when edited back to the same text. Failure keeps the current
draft; this does not introduce ordinary-Send retry idempotency. Closing a durable
session similarly retains the latest draft when its reply arrives, without
restoring already-cleared text or letting an old Send reply clear saved work.

### Insert a command without running it

In an idle live session, type `/` to browse commands admitted by the app-server.
Up/Down previews a candidate without changing the draft. Tab inserts the
selected or first candidate; Enter inserts only after selecting a row. Clicking
a row also inserts it. Escape dismisses the list and preserves the draft.
Cmd/Ctrl+Enter retains Send; ordinary Enter remains a newline when no row is
selected. Input-method composition suspends the menu.

For example, completing `/mod` inserts `/model `, then shows model candidates.
Arguments and quoting share TUI's command registry. In Chromium, each accepted
candidate is one native edit: Cmd/Ctrl+Z restores the prior draft and the
platform Redo shortcut reapplies it. Neither sends a turn. The compatibility
fallback still inserts text but does not guarantee undo history.

Completion is unavailable in detached history or while a turn or decision is
pending. It does not scan paths, enumerate session IDs or call a model. Only a
later explicit Send executes the draft.

### Suggest a next step explicitly

For an empty draft in a live idle session, **Suggest next step** requests one
ephemeral candidate through the current provider/model. This can incur model
charges; there is no automatic Desktop/Web request. **Use** inserts one
undoable draft edit, never a turn. Review it and send separately, or dismiss it.
Typing and session/runtime changes invalidate old results rather than
overwriting your draft.

The shared `prompt_suggestions` preference disables these requests. Re-enabling
a session that started disabled requires creating a new session. The request
is unavailable during pending decisions, Plan mode or unfinished Goal accounting.

When you quit while turns are active, choose **Quit and stop turns** only when
you are ready to interrupt them. Normal shutdown is usually immediate, but YHC
allows a running local backend 17 seconds to finish bounded cleanup and, if
necessary, another three seconds after forced termination. Electron exits only
after it observes that backend exit. If that cannot be confirmed, the app stays
open and asks you to try quitting again. This protects the latest durable write;
it does not turn an interrupted model response into a completed turn.

## Recover from an unexpected backend stop

If the local backend stops unexpectedly, Desktop remains open and shows
**Backend stopped unexpectedly. Restart YHC to reconnect.** Existing messages,
Activity history, and drafts remain visible, but every backend-dependent action
is disabled. A pending permission, question, Plan approval, repeated-tool
decision, or Cancel control cannot be reused against a replacement process.

Close YHC and open it again. Do not use Provider setup, Open Web, or a page
reload as a backend restart mechanism; this release intentionally exposes no
in-app restart control. On the next launch, the app validates a fresh backend
and rediscovers durable sessions. Select the saved session to inspect its
transcript, then submit a new prompt only when you want to attach it again.

The old active turn is not resumed automatically. Text that was visible only as
an unfinished stream may differ from the last durable transcript checkpoint,
and Desktop does not relabel that partial projection as a completed, cancelled,
or failed turn.

## Close and restore the macOS window

On macOS, closing the last YHC window does not quit the app or stop its local
backend. Activate YHC from the Dock or launch the same app again to restore one
window. An existing minimized window is restored and focused; when the old
window has closed, the primary app creates and loads a new one against the same
accepted backend.

Use **Quit YHC** when you intend to stop the backend. Window restoration is not
backend recovery: if the backend has already stopped unexpectedly, relaunch the
complete app as described above rather than expecting a new window to replace
the failed process.

## Reopen saved work safely

Selecting a saved session first loads its durable transcript and renders chat
history. It does not resume the model runtime or contact a provider merely
because the row was opened.

For a canonical saved session, the first new prompt attaches the exact durable
session and starts the turn. For a legacy-only row, **Send** remains disabled:
choose **Import and continue** only after every older agent process that could
write the session has stopped. Import leaves the legacy bytes
unchanged and does not send the draft. Desktop first refreshes the catalog and
requires the row to pass canonical admission; only a later explicit **Send**
attaches it.

This avoids accidental continuation while browsing old conversations, but an
old session can still fail at first send if its saved model/provider
configuration is no longer valid.

## Troubleshooting

| Symptom | Action |
|---|---|
| Provider setup appears before a first session | Supply a supported provider profile, then retry the deferred workspace selection. |
| An unsigned macOS build requests repeated Keychain approval | Use a consistently signed build for provider-profile acceptance. The unpacked lifecycle smoke intentionally uses an empty isolated profile and does not validate Keychain prompts. |
| A saved session fails when first sent | Read the safe turn error, then choose a compatible provider/model or create a new session. The app cannot infer that one provider's credential or HTTP dialect is safe for another. |
| Legacy history cannot continue | Stop every older producer, then use **Import and continue**. A failed import keeps history and the draft available for retry and does not modify the legacy bytes. |
| A decision card has no actionable control | Reload session state. The server fails closed when it cannot project a safe typed interaction. |
| A queued prompt can no longer be removed | It has already started or is no longer pending. Use **Cancel** only if it is the active Turn; otherwise refresh the Session snapshot. |
| Desktop says queued work needs attention | Automatic idle draining stopped after a durable claim failed. Remove the exact still-pending row if it is no longer wanted, or correct an execution setting and retry that update; either successful mutation explicitly re-enables draining. Reloading alone does not claim the item. |
| Desktop reports that the backend stopped unexpectedly | Close and reopen the complete YHC app. Existing projected history and drafts remain read-only until a fresh backend passes bootstrap; the interrupted turn and its old decisions are not resumed. |
| Desktop app will not start | Run `make desktop-check`, then rebuild and stage the paired backend with `make desktop-dev`. A `YHC_BIN` override or stale staged binary whose version does not match the Electron shell is intentionally rejected. |

The app-server uses loopback authentication and does not make a remote provider
compatible by itself. Endpoint-specific tool schemas, provider credentials,
network policy, signing, notarization, and remote CI are separate boundaries.

## Maintainer references

- [`serve_app.go`](../../cmd/yhc/cmd/serve_app.go) composes `yhc serve app`.
- [`server.go`](../../server/appserver/server.go) owns loopback protocol;
  durable-session, activation, and interaction helpers live beside it.
- [`main.cjs`](../../desktop/main.cjs) owns the Electron host and IPC boundary.
- [`app.mjs`](../../internal/webui/assets/app.mjs) owns renderer state; its
  adjacent assets own safe Markdown projection, Activity presentation, and
  typed cards.
