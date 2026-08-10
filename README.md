# YHC — Yet Hooked on Coding

A Go-based coding-agent runtime built on the
[Eino](https://github.com/cloudwego/eino) framework. It preserves proven Claude
Code capabilities, combines verified patterns from other mainstream coding
agents, and develops project-owned runtime and workflow contracts.

YHC is an independent product, not a 1:1 port. See
[`PROJECT_DIRECTION.md`](PROJECT_DIRECTION.md) for the product objective,
reference roles, and adoption rules.

## Status

The original Claude Code structural migration, the P1-P12 depth program, and
the TUI M0-M7 modernization program are complete at their recorded verification
snapshots. P13, a staged Eino ADK kernel convergence program, is the accepted
next evolution track; it has not yet changed the production query authority.

Use the documentation owners instead of counts copied into this README:

- [current verified status](docs/migration/STATUS.md);
- [unresolved implementation gaps](docs/migration/REMAINING.md);
- [accepted execution order](docs/migration/PLAN.md).

## What It Provides

- an imperative, streaming query engine with tool projection, stable tool
  scheduling, compaction, recovery, permission, hook, and event boundaries;
- built-in file, shell, search, planning, task, Agent, MCP, LSP, skill, memory,
  and worktree capabilities;
- durable transcripts and sessions plus process-local runtime projections;
- Bubble Tea TUI, plain/headless CLI, ACP server, and standalone MCP server
  entrypoints;
- provider-neutral model resolution through Eino provider adapters.

For package-by-package ownership and production-wiring labels, use the
[production code map](docs/architecture/code-map.md). For end-to-end behavior,
start with the [architecture guide](docs/architecture/README.md).

## Requirements

- Go 1.26.5

## Quick Start

Follow [Getting Started](docs/guides/getting-started.md) for the current build
artifact paths, provider credentials, first interactive run, and non-interactive
modes. Configuration and provider precedence are documented separately in
[Configuration And Providers](docs/guides/configuration-and-providers.md).

## Development

```bash
# Start from the protected trunk
git switch master
git pull --ff-only
git switch -c feat/<topic>

# Once per clone: block accidental direct pushes to master
make setup-git-hooks

# Before pushing code
make fmt
make lint
make test
make build
make docs-check
```

Push the short-lived branch, open a pull request into `master`, and merge only
after the required CI check passes. Use squash merge and delete the branch after
merging. Keep one independently reviewable behavior change per PR; evolution
work tracked in `docs/migration/PLAN.md` should normally correspond to one
accepted slice.

CI runs Makefile formatting, test, cross-platform build, and incremental v2
lint gates; local development retains the full v1 `make lint` baseline. The
versioned pre-push hook is a local guard, not access control: server-side branch
protection requires PRs, the `Required gates` check, linear history, and
resolved conversations.

## Documentation

- [`docs/README.md`](docs/README.md) — role- and task-oriented documentation home
- [`docs/guides/README.md`](docs/guides/README.md) — user workflows and operational examples
- [`docs/architecture/README.md`](docs/architecture/README.md) — current implementation and subsystem map
- [`docs/migration/README.md`](docs/migration/README.md) — status, gaps, accepted plans, evidence, and history
- [`docs/publication/README.md`](docs/publication/README.md) — public-source clearance, provenance, and release boundaries
- [`docs/contributing/README.md`](docs/contributing/README.md) — documentation and verification rules
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — contribution, rights, and provenance requirements
- [`SECURITY.md`](SECURITY.md) — confidential vulnerability reporting
- [`PROJECT_DIRECTION.md`](PROJECT_DIRECTION.md) — product objective and reference-adoption rules
- [`AGENTS.md`](AGENTS.md) — project conventions and decision constraints for coding agents

## License

YHC project-owned code and documentation are licensed under
[Apache-2.0](LICENSE). Bundled third-party material retains its own license and
notice obligations; see [NOTICE](NOTICE).
