# Desktop Workbench Forward-Port

**Status:** historical
**Completed:** 2026-08-13

> **Ownership:** local delivery record for the Desktop workbench forward-port;
> current behavior is owned by
> [`architecture/desktop-workbench.md`](../../architecture/desktop-workbench.md).

## Outcome

This delivery added the Electron workbench, authenticated loopback app-server,
and embedded Web UI to the public YHC tree. It retained `QueryEngine` as the
only conversation runtime and made durable-history viewing separate from
runtime activation: saved sessions render transcript history first, and first
user send attaches/resumes the selected session.

The renderer now has safe Markdown projection, semantic Activity entries, and
typed controls for permission, question, Plan approval, and repeated-tool
interactions. Workspace selection crosses the host/server boundary through an
opaque handle rather than a renderer-visible local path.

## Evidence recorded with the delivery

- focused Go coverage for app-server session admission, durable transcript
  projection, typed interaction routes, workspace handles, and the exact
  DeepSeek Anthropic-compatible tool dialect;
- Node coverage for renderer state, Markdown safety, Activity, interactions,
  host lifecycle, request routing, provider setup, and layout; and
- local packaged-app QA for startup, session selection, history-first reopen,
  first-send activation, and live-provider response rendering.

## Exclusions at closeout

The local package is an unsigned QA artifact. This record makes no claim about
code signing, notarization, distribution readiness, or compatibility with an
arbitrary third-party provider endpoint and tool schema. Remote CI status is
reported by the pull request rather than this historical source record.

## Current replacement

Use the [Desktop workbench guide](../../guides/desktop-workbench.md) for the
operator workflow and the
[Desktop workbench architecture](../../architecture/desktop-workbench.md) for
current ownership and invariants.
