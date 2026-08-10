# G11.D1 Markdown Profile Projection

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.D1 delivery result, App/Markdown render-environment
> projection, cache identity, compatibility consequences, verification, and
> rollback

## Outcome

The accepted `combine` slice adds one immutable
[`RenderEnvironment`](../../../../internal/tui/render_environment.go#L5) owned
by `App`. It contains the current `Styles`, a monotonic theme generation, the
session-selected immutable `DisplayCellProfile`, and a separate monotonic
geometry generation. Theme application replaces the value and advances only
theme identity. A real terminal-size change advances only geometry identity;
an identical size is inert. The selected profile remains immutable for the
App lifetime.

The same environment is projected into active, inactive, restored, future,
and durable-reset thread views, `HistoryRenderContext`, finalized assistant
messages, the compatibility streaming message, and the Plan dialog. This is a
presentation-only value: no engine event, transcript entry, session schema,
permission, replay, or canonical Markdown owner changed.

## Markdown And Cache Boundary

Production `StreamingMarkdown` and semantic-table rendering receive the
App-selected profile. Glamour renderer keys and stable-prefix/full-output
identities bind exact width, semantic theme/color profile, theme generation,
geometry generation, profile identity, canonical source, and completeness
where relevant. An environment mismatch invalidates rendered fragments before
the exact-cache fast path.

`ChatView` frozen-item caches now require exact width and the complete render-
environment identity; the former `±2` width tolerance is removed. The
viewport cache explicitly binds the same environment. Restored existing views
are reprojected before use, and a durable session reset constructs both the
thread store and active chat from the App-owned value.

Profile-only/default constructors remain compatibility and test seams. They
do not select production Markdown geometry. Non-Markdown chat clipping,
sticky output, final main/sidebar composition, status width, pill geometry,
dialogs, and pickers remain unchanged for G11.D2-G11.E4.

## Verification

Focused lifecycle and compatibility evidence covers independent generation
changes, renderer/stable/full cache identities, active/inactive/restored/future
thread projection, durable reset, frozen/viewport cache reuse, and mixed
bold/code semantic tables under a non-default profile:

```text
go test ./internal/tui -run '^TestG11D1' -count=1
go test -race ./internal/tui -run '^TestG11D1' -count=1
go test ./internal/tui -run '^(TestG9|TestG11A|TestG11C|TestStreamingMarkdown|TestApplyThemePropagates|TestChatSetStyles|TestTerminalCommandReportsEffectiveCapabilities)' -count=1
```

Repository closeout uses:

```text
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Compatibility And Rollback

Canonical Markdown/table source, visible default-profile output, non-Markdown
final composition, runtime state, persisted data, and supported entrypoints
remain compatible. A custom constructor-injected profile now affects
production Markdown/table geometry as intended.

Rollback removes the App render environment, projection methods, Markdown
environment identities, exact ChatView cache identity, and focused tests
together. A partial rollback that leaves mixed theme/profile/geometry
generations or restores tolerant frozen-width reuse is forbidden. Current
behavior belongs in [`architecture/tui/README.md`](../../../architecture/tui/README.md);
remaining final-frame migration belongs in
[`migration/PLAN.md`](../../PLAN.md).
