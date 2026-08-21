# Desktop Workbench

**Status:** current
**Last verified:** 2026-08-21

> **Ownership:** how an operator builds, starts, uses, and recovers the local
> YHC Electron workbench. Runtime and protocol detail belong to the
> [Desktop workbench architecture](../architecture/desktop-workbench.md).

## Outcome

The Desktop workbench gives one local three-panel view of sessions, chat, and
semantic activity. It starts a loopback YHC app-server, keeps the renderer out
of direct filesystem and process access, and sends normal coding turns through
the existing `QueryEngine` only after the user submits a prompt.

## Prerequisites

- Go and Node/npm compatible with this repository's lockfile.
- A provider configuration that can run YHC tool calls. Environment-derived
  configuration may be used; the Desktop provider dialog can store a replacement
  profile only when operating-system secure storage is available.
- A local working directory to select as the workspace.

The provider dialog intentionally refuses to change a profile while live
Desktop sessions exist. Close them first so one process cannot silently mix
provider credentials or routing inside an active conversation.

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

CI also assembles and verifies native unpacked outputs on macOS Intel, macOS
Apple Silicon, and Windows x64 runners. The Apple Silicon row additionally
launches its unsigned unpacked app and verifies the bounded bootstrap, preload,
backend, and graceful-shutdown lifecycle described below. Intel and Windows
remain package-verifier coverage only. None of these rows creates, signs,
uploads, or validates final installer media.

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
window. Both local Linux targets keep Chromium's sandbox enabled. The hosted
Linux CI job uses their dedicated `-ci` variants, which opt into
`--no-sandbox`; those runs therefore do not validate the production OS
sandbox, an active provider turn, or physical UI behavior.

On an Apple Silicon macOS host with an unlocked GUI session, the corresponding
unsigned unpacked-app gate is:

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

The macOS lifecycle row also remains automation evidence: it does not exercise
Finder launch, foreground focus, native picker interaction, secure-storage
prompts, or a physical display acceptance scenario.

Run `make desktop-check` for deterministic Node/unit and syntax checks. The
browser-only transport can also be started explicitly with:

```bash
yhc serve app --web
```

## Use a session

1. Select **New session** and choose a workspace in the native picker.
2. The host exchanges the selected path for an opaque workspace handle; the
   page never receives the path. Choose model, reasoning, and permission mode
   before the first turn if desired.
3. Type a request and send it. The center panel renders assistant Markdown,
   code, lists, tables, and safe links; untrusted HTML and unsafe URL schemes
   are not injected into the page.
4. Use the right Activity panel to follow meaningful lifecycle changes such as
   a turn, tool, task, Agent, or waiting interaction. It is deliberately not a
   raw streaming-event log.
5. When a decision is needed, use the card in the conversation: permissions,
   questions, Plan approval, and repeated-tool calls each have their own typed
   controls. Do not answer one kind of card by pasting JSON into chat.

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
