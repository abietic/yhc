# P19.3.1 Revontuli Tool Category Badges

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, and rollback for
> the completed P19.3.1 tool-category badge slice

## Outcome

Known rich tool-history labels now render through one semantic badge boundary
instead of constructing independent dark-hue backgrounds:

| Category | Semantic foreground | Surface |
|---|---|---|
| Bash, shell aliases, MCP | brand teal (`AssistantPrefix`) | `Element` |
| Read, Write, Edit, To-Do | success green (`ToolSuccess`) | `Element` |
| Grep, Glob, LS, Explore, Task, Web | sky (`AuroraSky`) | `Element` |
| Agent, Plan | permission violet (`DialogTitle`) | `Element` |

Unknown dynamic tools retain the plain `ToolName` style. The adoption decision
was `adapt`: keep useful category distinction while replacing the borrowed
per-category background treatment with the project-owned Revontuli palette.

## Preserved Boundary

- Tool names, status icons, arguments, bodies, truncation, display width, and
  specialized renderer dispatch are unchanged.
- No interaction, permission, runtime event, transcript, replay, persistence,
  or cache owner changed.
- Task and To-Do retain their established sky and green category aliases.
- The App remains the theme owner; the badge renderer receives immutable
  `Styles` and creates no package-global theme state.
- P19.3.2 error/mode colors, P19.3.3 composer border, P19.3.4 user panel,
  P19.3.5 welcome wordmark, P20 Plan interaction, and G9 display-cell work
  remain separate rollback units.

## Evidence

[`tool_badge_theme_test.go`](../../../../internal/tui/tool_badge_theme_test.go)
drives the production tool-history renderer for Bash, file, search, Agent,
Plan, MCP, and Web representatives. It also verifies every retained alias, the
unknown-tool fallback, exact display width, semantic foreground plus `Element`
background, and dark-ANSI escape containment.
[`tool_badges.golden`](../../../../internal/tui/testdata/tool_badges.golden)
pins actual Polar Night, Daybreak, and dark-ANSI SGR state for every accepted
category.

Closeout used:

```text
go test ./internal/tui -run ^TestToolBadge -count=1
go test -race ./internal/tui -run ^TestToolBadge -count=1
go test ./internal/tui -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

The manifest classification did not change: the relevant reference tool UI
files were already classified as adapted and mapped to the shared TUI tool
history owner.

## Rollback

Reverting this slice restores the literal dark-hue badge-background map.
P19.0.0-P19.3.0 remain the safe owners for live theme propagation, palette
values, mascot, glyph, spinner, and Markdown identity.

Current rendering ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#rendering-and-layout).
