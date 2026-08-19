# Desktop Workbench

**Status:** current
**Last verified:** 2026-08-20

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
Apple Silicon, and Windows x64 runners. These jobs run the exact package
verifier but do not launch those apps or create, sign, upload, or validate final
installer media.

`desktop/package.json` is the Desktop version source. Its canonical version has
no `v` prefix. The supported Desktop Make targets depend on that manifest and
inject its version into the staged Go backend; on startup, Electron rejects a
backend that reports a different or malformed identity. Once connected, the
sidebar footer shows the accepted version, short commit, and `dirty` marker
when applicable. This is an accidental-mismatch guard, not proof that a binary
is authentic or signed.

On a Linux x64 host with `xvfb-run`, the same full unpacked lifecycle gate is:

```bash
make desktop-unpacked-lifecycle-smoke-linux-amd64
```

It uses a fresh HOME/XDG profile with no inherited provider credentials,
launches the packaged renderer, crosses preload IPC to identify the bundled
backend, closes the window, and requires both the app and that backend process
to exit. This local target keeps Chromium's sandbox enabled. The hosted CI job
uses the dedicated `desktop-unpacked-lifecycle-smoke-linux-amd64-ci` target,
which opts into `--no-sandbox`; that CI run therefore does not validate the
production OS sandbox or replace physical UI acceptance on Linux, macOS, or
Windows.

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
| A saved session fails when first sent | Read the safe turn error, then choose a compatible provider/model or create a new session. The app cannot infer that one provider's credential or HTTP dialect is safe for another. |
| Legacy history cannot continue | Stop every older producer, then use **Import and continue**. A failed import keeps history and the draft available for retry and does not modify the legacy bytes. |
| A decision card has no actionable control | Reload session state. The server fails closed when it cannot project a safe typed interaction. |
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
