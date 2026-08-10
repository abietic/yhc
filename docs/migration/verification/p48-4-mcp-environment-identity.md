# P48.4 MCP Environment Identity Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that ACP admission, setup fingerprints,
> and stdio process overlay share one OS-aware environment-key identity

## Contract

`engine/mcp.CanonicalEnvironmentKey` maps Windows names to uppercase and keeps
non-Windows names exact. ACP uses that identity to reject semantic duplicates
before construction and to encode stable `(canonical name, exact value)`
fingerprint pairs. It continues to retain each admitted original key spelling
and value for process construction. The stdio launcher uses the same identity
without changing inherited parsing or deterministic overlay order.

The repair does not change server identity, command/argument validation,
descriptor bounds, environment values, Unix case sensitivity, persistence,
or manager and process lifecycle.

## Deterministic Evidence

`TestCanonicalEnvironmentKeyForOS` executes both Windows and Unix identity
branches without depending on the host OS. `TestInheritedEnvironmentWithOverlay`
proves exact non-Windows overlay output, one semantic replacement, and no input
slice or map mutation.

On the native non-Windows host, `TestP235ACPStdioUnixEnvironmentIdentity`
admits `Path` and `PATH` as distinct original entries and proves that changing
their value mapping changes the fingerprint. The Windows-tagged ACP tests
encode the inverse contract: `Path` plus `PATH` is rejected, equivalent
single-spelling descriptors fingerprint equally, and original admitted
spelling is retained. Those tests cross-compile into a Windows binary locally;
they were not executed on a Windows kernel.

Focused race, complete MCP/ACP package tests, the official TypeScript ACP SDK
v1.3.0 subprocess harness, contract tests, and repository gates cover the
surrounding ownership and wire boundaries.

## Commands

```bash
go test ./engine/mcp/ -run '^(TestCanonicalEnvironmentKeyForOS|TestInheritedEnvironmentWithOverlay)$' -count=1
go test ./server/acp/ -run '^TestP235ACPStdio' -count=1
go test -race ./engine/mcp/ ./server/acp/ -run '^(TestCanonicalEnvironmentKeyForOS|TestInheritedEnvironmentWithOverlay|TestP235ACPStdio)' -count=1
go test ./engine/mcp/ -count=1
go test ./server/acp/ -count=1
windows_test_dir=$(mktemp -d /tmp/eino-p48-4-windows.XXXXXX)
GOOS=windows GOARCH=amd64 go test -c ./server/acp -o "$windows_test_dir/acp.test.exe"
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make test-race
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands pass on the closeout tree. The Windows `go test -c` command
produces a compile artifact only.

## Evidence Limits

Native local tests prove non-Windows behavior and execute the pure Windows
canonicalization branch. The Windows-tagged ACP tests and cross-platform build
prove compilation, not a real Windows environment block, `exec.Cmd`, process,
or Job Object run. No real Windows host was available locally. Remote CI,
live-provider behavior, and third-party MCP server compatibility remain
separate evidence classes.
