# MCP Environment-Key Identity Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-07

> **Ownership:** test-first implementation and closeout steps for P48.4/G45;
> root migration queue state remains authoritative.

**Goal:** Give ACP stdio-MCP validation, setup fingerprinting, and child-process
environment overlay one shared OS-aware key identity.

**Architecture:** `engine/mcp` exports the smallest semantic helper needed by
both the launcher and ACP adapter. Windows canonicalizes admitted environment
names to uppercase; Unix-like systems retain exact spelling. ACP preserves the
original key/value map for process construction but uses the canonical key for
duplicate detection, stable fingerprint ordering, and fingerprint encoding.

**Tech Stack:** Go 1.26.5, `engine/mcp` stdio transport, ACP MCP setup,
build-tagged platform tests, Windows cross-compilation, repository build matrix,
migration queue, and Makefile gates.

## Global Constraints

- Execute only when P48.4 is `Ready` and P48.3 has completed.
- Close only G45. Do not change server-name normalization, command/argument
  validation, values, descriptor limits, or Unix case-sensitive behavior.
- One helper owns key identity. ACP tests must not copy the uppercasing
  algorithm into a second production seam.
- Preserve original environment key spelling in `mcp.ServerConfig.Env`; the
  canonical form is semantic identity and fingerprint input, not a request
  rewrite.
- Reject semantic duplicates before engine or process construction.
- Report Windows cross-compilation separately from a real Windows process run.

---

## Task 1: Establish the shared environment identity owner

**Files:**

- Create: `engine/mcp/environment.go`
- Create: `engine/mcp/environment_test.go`
- Modify: `engine/mcp/stdio_transport.go`
- Modify: `engine/mcp/stdio_transport_unix_test.go`

**Interfaces:**

- Adds: `mcp.CanonicalEnvironmentKey(string) string`.
- Keeps an unexported `canonicalEnvironmentKeyForOS(goos, key string)` for
  deterministic current-platform-independent tests.
- Replaces launcher-local `environmentKey` without changing overlay order.

- [x] **Step 1: Add pure Windows and Unix identity tests**

Create `TestCanonicalEnvironmentKeyForOS` with at least:

```go
{goos: "windows", key: "Path", want: "PATH"}
{goos: "windows", key: "PATH", want: "PATH"}
{goos: "linux", key: "Path", want: "Path"}
{goos: "darwin", key: "Path", want: "Path"}
```

Also retain the existing inherited-overlay tests proving lexical output,
single replacement, and no mutation of input slices.

- [x] **Step 2: Run the focused red test**

```bash
go test ./engine/mcp/ -run '^TestCanonicalEnvironmentKeyForOS$' -count=1
```

Expected: FAIL to compile because the shared helper does not exist.

- [x] **Step 3: Implement and adopt the helper**

```go
func CanonicalEnvironmentKey(key string) string {
	return canonicalEnvironmentKeyForOS(runtime.GOOS, key)
}

func canonicalEnvironmentKeyForOS(goos, key string) string {
	if goos == "windows" {
		return strings.ToUpper(key)
	}
	return key
}
```

Use `CanonicalEnvironmentKey` inside `inheritedEnvironmentWithOverlay` and
remove `environmentKey`. Preserve inherited entry parsing and original overlay
key emission.

- [x] **Step 4: Run engine/MCP green and commit**

```bash
go test ./engine/mcp/ -run '^(TestCanonicalEnvironmentKeyForOS|TestInheritedEnvironmentWithOverlay)' -count=1
git add engine/mcp/environment.go engine/mcp/environment_test.go engine/mcp/stdio_transport.go engine/mcp/stdio_transport_unix_test.go
git commit -m "refactor(mcp): share environment key identity"
```

## Task 2: Align ACP duplicate admission and fingerprints

**Files:**

- Modify: `server/acp/mcp_setup.go`
- Modify: `server/acp/mcp_setup_test.go`
- Create: `server/acp/mcp_setup_windows_test.go`
- Create: `server/acp/mcp_setup_unix_test.go`

**Interfaces:**

- Consumes: `mcp.CanonicalEnvironmentKey`.
- Preserves: `tools.SessionMCPServer.Config.Env` original names and values.
- Produces: fingerprint pairs sorted and encoded by canonical key.

- [x] **Step 1: Add platform-specific red contracts**

In `mcp_setup_windows_test.go` with `//go:build windows`, assert:

- one descriptor containing `Path` and `PATH` returns InvalidParams with
  `reason: environment_name_duplicate`; and
- two otherwise identical admitted descriptors using one spelling each have
  equal setup fingerprints when values match.

In `mcp_setup_unix_test.go` with `//go:build !windows`, assert `Path` and `PATH`
are distinct admitted keys and that changing their semantic mapping changes
the fingerprint. Keep exact-duplicate coverage in the common test file.

- [x] **Step 2: Make admission use canonical identity**

Build a per-descriptor `seenEnvironment` set keyed by
`mcp.CanonicalEnvironmentKey(variable.Name)`. Continue writing the original
`variable.Name` and value to the configuration map after the duplicate check.

- [x] **Step 3: Canonicalize fingerprint pairs before sorting**

Construct `(canonicalName, value)` pairs, sort by canonical name, and encode
the canonical name followed by the exact value. Do not index the original map
with a canonical name. Admission already guarantees one pair per semantic key.

- [x] **Step 4: Run native ACP tests and Windows compile proof**

```bash
go test ./server/acp/ -run '^TestP235ACPStdio' -count=1
windows_test_dir=$(mktemp -d /tmp/eino-p48-4-windows.XXXXXX)
GOOS=windows GOARCH=amd64 go test -c ./server/acp -o "$windows_test_dir/acp.test.exe"
```

The compile artifact proves build compatibility only. If no Windows runner is
available, record real `exec.Cmd` behavior as unavailable rather than passed.

- [x] **Step 5: Commit the ACP adoption**

```bash
git add server/acp/mcp_setup.go server/acp/mcp_setup_test.go server/acp/mcp_setup_windows_test.go server/acp/mcp_setup_unix_test.go
git commit -m "fix(acp): align MCP environment identity"
```

## Task 3: Close G45 with platform evidence boundaries

**Files:**

- Modify: `docs/architecture/platform/acp-adapter.md`
- Modify: `docs/architecture/capabilities/mcp.md`
- Create: `docs/migration/verification/p48-4-mcp-environment-identity.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p48-4-mcp-environment-identity.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p48-acp-boundary-remediation.md`

- [x] **Step 1: Document the shared identity and evidence class**

Remove G45 and P48.4, leave P48.5 queued unless root governance promotes it,
and state Windows case folding plus Unix exact identity. Record native tests,
cross-builds, remote CI, and real Windows runtime evidence as separate facts.

- [x] **Step 2: Run platform and repository gates**

```bash
go test ./engine/mcp/ -run '^(TestCanonicalEnvironmentKeyForOS|TestInheritedEnvironmentWithOverlay)' -count=1
go test ./server/acp/ -run '^TestP235ACPStdio' -count=1
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit closeout and open one atomic PR**

```bash
git add docs/architecture docs/migration
git commit -m "docs: close P48.4 MCP environment identity"
```

The PR must state the `adapt` decision, Unix compatibility, Windows evidence
boundary, rollback, local gate results, and remote-CI state.
