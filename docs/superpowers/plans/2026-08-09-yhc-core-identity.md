# YHC Core Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `$iteration-workflow` to execute and close this plan task-by-task. Preserve
> the frozen identity and compatibility contract. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make YHC the only public product, Go-module, command, build-artifact,
and current-copy identity while preserving deterministic aliases for supported
`EINO_AGENT_*` runtime variables and truthful historical/source-mapping text.

**Architecture:** A small `internal/identity` package owns immutable product,
command, directory, and environment-name pairs. Module/import/command movement
is a mechanical review commit. Public strings and current documentation consume
the new identity. Runtime environment consumers use one canonical-first
resolver; persistence owners and ACP namespace behavior remain in their
separate plans.

**Tech Stack:** Go 1.26.5, Cobra, Bubble Tea, Makefile cross-builds, Git path
moves, repository publication scanner, and focused CLI/build/TUI tests.

**Status:** active-plan
**Created:** 2026-08-09
**Plan state:** Identity implementation complete; downstream publication clearance pending

> **Ownership:** public identity and environment compatibility from the
> [YHC public-release design](../specs/2026-08-09-yhc-public-release-design.md).
> State import and ACP/MCP protocol behavior are explicitly out of scope.

## Global Constraints

- Canonical product is `YHC — Yet Hooked on Coding`; canonical module and
  repository are `github.com/abietic/yhc`; canonical command and artifact are
  `yhc`.
- Move `cmd/eino-agent` to `cmd/yhc`. Do not ship an `eino-agent` binary,
  command alias, module `replace`, vanity path, or compatibility package.
- Do not change default state roots mechanically in the module/command commit.
  `internal/identity` exposes `.yhc` and `.eino-agent` constants; the state
  plans change each persistence owner with migration tests.
- All 14 production runtime variables receive matching `YHC_*` names. A
  present canonical variable wins even when its value is empty; that empty
  value then follows the existing parser/default behavior and must not fall
  through to the legacy value.
- Never log environment values. Diagnostics may report only canonical,
  legacy, default, invalid, or disabled source classifications.
- Test-helper variables that are not supported runtime contracts are renamed
  mechanically and receive no alias.
- Current public copy uses YHC. Historical migration records, old source
  snapshots, source mappings, and compatibility tests retain old identity when
  changing it would falsify evidence.
- This plan does not change state bytes, ACP Goal methods, MCP transport/config,
  tool ordering, permissions, cancellation, recovery, providers, or supported
  entrypoints.
- Run the publication identity scanner after every task. An old-name hit is
  allowed only by a path-and-purpose rule in `quality/publication.yaml`.

---

## Locked Interfaces

```go
package identity

const (
	ProductName       = "YHC"
	ProductLongName   = "YHC — Yet Hooked on Coding"
	CommandName       = "yhc"
	ModulePath        = "github.com/abietic/yhc"
	ProjectDirName    = ".yhc"
	LegacyDirName     = ".eino-agent"
	LegacyCommandName = "eino-agent"
)

type EnvSource uint8

const (
	EnvUnset EnvSource = iota
	EnvCanonical
	EnvLegacy
)

type EnvPair struct {
	Canonical string
	Legacy    string
}

func LookupEnv(pair EnvPair) (value string, source EnvSource, present bool)
func EnvTruthy(pair EnvPair) bool
```

The 14 production pairs are:

| Canonical | Legacy |
|---|---|
| `YHC_ACCESSIBILITY` | `EINO_AGENT_ACCESSIBILITY` |
| `YHC_CONFIG_DIR` | `EINO_AGENT_CONFIG_DIR` |
| `YHC_DISABLE_ACP_ASSISTANT_MESSAGE_IDS` | `EINO_AGENT_DISABLE_ACP_ASSISTANT_MESSAGE_IDS` |
| `YHC_DISABLE_ACP_COMMAND_UPDATES` | `EINO_AGENT_DISABLE_ACP_COMMAND_UPDATES` |
| `YHC_DISABLE_AUTO_MEMORY` | `EINO_AGENT_DISABLE_AUTO_MEMORY` |
| `YHC_DISABLE_MOUSE` | `EINO_AGENT_DISABLE_MOUSE` |
| `YHC_MEMORY_PATH_OVERRIDE` | `EINO_AGENT_MEMORY_PATH_OVERRIDE` |
| `YHC_PERMISSION_REVIEW_AUDIT_DIR` | `EINO_AGENT_PERMISSION_REVIEW_AUDIT_DIR` |
| `YHC_PROVIDER_PREFLIGHT` | `EINO_AGENT_PROVIDER_PREFLIGHT` |
| `YHC_REDUCED_MOTION` | `EINO_AGENT_REDUCED_MOTION` |
| `YHC_REMOTE_MEMORY_DIR` | `EINO_AGENT_REMOTE_MEMORY_DIR` |
| `YHC_SESSION_CATALOG` | `EINO_AGENT_SESSION_CATALOG` |
| `YHC_SIMPLE` | `EINO_AGENT_SIMPLE` |
| `YHC_TEAM_MEMORY_DIR` | `EINO_AGENT_TEAM_MEMORY_DIR` |

## Task 1: Establish Identity And Environment Resolution

**Files:**

- Create: `internal/identity/identity.go`
- Create: `internal/identity/env.go`
- Create: `internal/identity/identity_test.go`
- Create: `internal/identity/env_test.go`

- [x] **Step 1: Add identity and precedence tests**

Create:

- `TestCanonicalIdentityConstants`;
- `TestLookupEnvCanonicalWinsLegacyWithoutValueExposure`;
- `TestLookupEnvFallsBackToLegacy`;
- `TestLookupEnvCanonicalPresentEmptyStillWins`;
- `TestLookupEnvUnset`; and
- `TestEnvTruthyPreservesExistingParsing`.

Use `t.Setenv` and never include the sentinel values in an error string.

- [x] **Step 2: Run red**

```bash
go test ./internal/identity -count=1
```

Expected: FAIL because the package does not exist.

- [x] **Step 3: Implement the minimal owner**

`LookupEnv` uses `os.LookupEnv` in canonical, legacy order. `EnvTruthy` calls
`LookupEnv` and preserves the current trim/case interpretation of `1`,
`true`, `yes`, and `on` without introducing a second product setting layer.

- [x] **Step 4: Run green and commit**

```bash
go test ./internal/identity -count=1
git add internal/identity
git commit -m "feat(identity): define YHC canonical identity"
```

## Task 2: Move The Module, Command, And Build Artifacts

**Files:**

- Modify: `go.mod`
- Modify: every tracked Go source returned by
  `git grep -l 'github.com/yuhaichuan/eino-agent' -- '*.go'`
- Move: `cmd/eino-agent/` to `cmd/yhc/`
- Modify: `Makefile`
- Modify: `quality/iteration.yaml`
- Modify: `.github/workflows/ci.yml`
- Modify: `scripts/e2e/harness_test.go`
- Modify: `scripts/build_dependencies_test.go`
- Modify: `.gitignore`

**Interfaces:**

- Go module and first-party imports become `github.com/abietic/yhc`.
- All build, run, debug, evaluation, E2E, release, Windows, and CI artifacts are
  named `yhc` or `yhc.exe`.
- `quality/iteration.yaml` uses `github.com/abietic/yhc` and
  `cmd/yhc/**`.

- [x] **Step 1: Add mechanical contract tests before the move**

Extend `scripts/build_dependencies_test.go` and
`scripts/e2e/harness_test.go` with `TestYHCModuleCommandAndArtifactIdentity`.
Assert module path, sole main package `./cmd/yhc`, build target basenames, and
the absence of `./cmd/eino-agent` and public `eino-agent` artifacts.

- [x] **Step 2: Run red**

```bash
go test ./scripts/... -run '^TestYHCModuleCommandAndArtifactIdentity$' -count=1
```

Expected: FAIL on the old module, command path, and artifact names.

- [x] **Step 3: Perform the bounded mechanical move**

```bash
git mv cmd/eino-agent cmd/yhc
```

Change `go.mod` and all tracked first-party Go imports in one mechanical pass.
Update Makefile and workflow command paths, linker symbols, output names, E2E
temporary names, export names, and policy package paths. Keep both
`**/.yhc` and `**/.eino-agent` ignored; the latter remains legacy/private
state.

- [x] **Step 4: Prove no old module or command surface remains**

```bash
go test ./scripts/... -run '^TestYHCModuleCommandAndArtifactIdentity$' -count=1
go list ./...
git grep -n 'github.com/yuhaichuan/eino-agent' -- '*.go' go.mod Makefile quality/iteration.yaml .github/workflows/ci.yml
```

Expected: tests and `go list` pass; the final grep returns no match.

- [x] **Step 5: Run build matrix and commit**

```bash
make fmt
make test
make build
git add go.mod cmd Makefile quality/iteration.yaml .github/workflows/ci.yml scripts .gitignore
git add --pathspec-from-file=build/publication/module-import-paths.txt --pathspec-file-nul
git commit -m "chore(identity): move module and command to YHC"
```

`module-import-paths.txt` is generated from the frozen tracked-path inventory
and contains only Go files whose old first-party import changed.

## Task 3: Rename Public Product Projections

**Files:**

- Modify: `internal/identity/env.go`
- Modify: `internal/identity/env_test.go`
- Modify: `cmd/yhc/cmd/root.go`
- Modify: `cmd/yhc/cmd/serve_acp.go`
- Modify: `cmd/yhc/cmd/serve_mcp.go`
- Modify: `cmd/yhc/cmd/cli_contract_test.go`
- Modify: `internal/buildinfo/buildinfo.go`
- Modify: `internal/buildinfo/buildinfo_test.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/external_editor.go`
- Modify: `internal/tui/external_editor_test.go`
- Modify: `internal/tui/attachments/attachments.go`
- Modify: `internal/tui/composer_editor.go`
- Modify: `engine/commands/registry.go`
- Modify: `engine/commands/commands_test.go`
- Modify: `engine/onboarding/onboarding.go`
- Modify: `engine/session_service.go`
- Create: `scripts/publication/identity_test.go`

**Interfaces:**

- Cobra root `Use` is `yhc [prompt]`.
- Build info begins `yhc v...` and detailed identity is YHC.
- Window title, header, resume hints, editor command, temporary-file patterns,
  session export names, diagnostics, and current package comments use YHC.
- ACP/MCP advertised names and Goal methods remain unchanged until the protocol
  plan; state paths remain unchanged until the state plans.

- [x] **Step 1: Add red projection tests**

Add `TestRootCommandPublishesYHCIdentityAndNoLegacyCommandAlias`,
`TestBuildInfoTextProjectionsShareYHCIdentity`,
`TestYHCPublicProductProjections`, and
`TestCurrentIdentityAllowsLegacyOnlyForHistoryMappingAndCompatibility`.

The publication identity test scans current-copy paths and fails on an
unclassified old product/command hit. It accepts exact legacy constants,
environment aliases, state/protocol compatibility, migration history, and
source mappings by policy ID.

- [x] **Step 2: Run red**

```bash
go test ./cmd/yhc/cmd ./internal/buildinfo ./internal/tui ./scripts/publication -run 'Test(RootCommandPublishesYHC|BuildInfoTextProjectionsShareYHC|YHCPublicProduct|CurrentIdentity)' -count=1
```

- [x] **Step 3: Update public projections**

Consume `internal/identity` constants where doing so prevents divergent product
copy. Do not introduce a dependency from engine runtime into CLI or TUI.
Rename temporary prefixes only; preserve attachment/editor lifetime and file
permissions.

- [x] **Step 4: Run green and commit**

```bash
go test ./cmd/yhc/cmd ./internal/buildinfo ./internal/tui ./scripts/publication -run 'Test(RootCommandPublishesYHC|BuildInfoTextProjectionsShareYHC|YHCPublicProduct|CurrentIdentity|ExternalEditor)' -count=1
git add cmd/yhc internal/buildinfo internal/tui engine/commands engine/onboarding engine/session_service.go scripts/publication/identity_test.go
git commit -m "feat(identity): publish YHC product projections"
```

## Task 4: Adopt Canonical-First Runtime Environment Aliases

**Files:**

- Modify: `cmd/yhc/cmd/root.go`
- Modify: `cmd/yhc/cmd/root_test.go`
- Modify: `cmd/yhc/cmd/serve_acp.go`
- Create: `cmd/yhc/cmd/serve_acp_test.go`
- Modify: `engine/memdir/paths.go`
- Modify: `engine/memdir/team.go`
- Modify: `engine/memdir/memdir_test.go`
- Modify: `engine/memdir/agent_snapshot_test.go`
- Modify: `engine/session/catalog.go`
- Modify: `engine/session/query_test.go`
- Modify: `engine/permission/review_audit.go`
- Modify: `engine/permission/review_audit_test.go`
- Modify: `internal/tui/terminalcap/capabilities.go`
- Modify: `internal/tui/terminalcap/capabilities_test.go`
- Modify: test-helper files returned by the publication identity inventory

- [x] **Step 1: Add the complete precedence matrix**

Add `TestRuntimeEnvironmentCanonicalWinsLegacy`,
`TestYHCEnvironmentPrecedenceForMemoryPaths`,
`TestDefaultCatalogPathPrefersYHCSessionCatalog`, and
`TestDefaultReviewAuditDirPrefersYHCOverride`, plus
`TestProbeMouseEnvironmentCanonicalWinsLegacy`. For every pair test canonical
only, legacy only, both, canonical-present-empty plus legacy non-empty, invalid
canonical plus valid legacy, and neither.

The selected value still uses each owner's existing validation. An invalid
canonical value does not fall through to legacy.

- [x] **Step 2: Run red**

```bash
go test ./cmd/yhc/cmd ./engine/memdir ./engine/session ./engine/permission ./internal/tui/terminalcap -run 'Test(RuntimeEnvironmentCanonical|YHCEnvironment|DefaultCatalogPathPrefersYHC|DefaultReviewAuditDirPrefersYHC|ProbeMouseEnvironment)' -count=1
```

- [x] **Step 3: Replace direct production reads**

Adopt `identity.LookupEnv` or `identity.EnvTruthy` for all 14 environment pairs
across the 21 production reads. Include injected environment maps such as
`terminalcap.Probe`; rename test-only helper variables mechanically only where
the publication inventory proves they are not external contracts.

- [x] **Step 4: Prove the runtime family is complete**

```bash
git grep -n 'os.Getenv("EINO_AGENT_\|os.LookupEnv("EINO_AGENT_' -- '*.go' ':!**/*_test.go'
rg -n --glob '*.go' --glob '!*_test.go' 'EINO_AGENT_' cmd/yhc engine/memdir engine/session/catalog.go engine/permission/review_audit.go internal/identity internal/tui/terminalcap
go test ./cmd/yhc/cmd ./engine/memdir ./engine/session ./engine/permission ./internal/tui/terminalcap -run 'Test(RuntimeEnvironmentCanonical|YHCEnvironment|DefaultCatalogPathPrefersYHC|DefaultReviewAuditDirPrefersYHC|ProbeMouseEnvironment)' -count=1
```

Expected: the direct-read grep is empty; the broader inventory contains only
the exact root-help alias and centralized identity prefix; focused tests pass.

- [x] **Step 5: Commit**

```bash
git add internal/identity cmd/yhc engine/memdir engine/session/catalog.go engine/session/query_test.go engine/permission/review_audit.go engine/permission/review_audit_test.go internal/tui/terminalcap docs/superpowers/plans/2026-08-09-yhc-core-identity.md
git add --pathspec-from-file=build/publication/test-env-helper-paths.txt --pathspec-file-nul
git commit -m "feat(identity): preserve legacy runtime environment aliases"
```

## Task 5: Update Current Documentation And Skills Without Falsifying History

**Files:**

- Modify: `README.md`
- Modify: `PROJECT_DIRECTION.md`
- Modify: `AGENTS.md`
- Modify current owner docs under `docs/architecture/**`,
  `docs/guides/**`, and `docs/contributing/**` selected by the identity
  inventory
- Modify current project skills under `.agents/skills/**` and agents under
  `.codex/agents/**` selected by the identity inventory
- Modify: `docs/superpowers/plans/README.md` only for canonical current links
- Create: `scripts/publication/identity.go`
- Modify: `scripts/publication/identity_test.go`
- Modify: `scripts/publication/inventory.go`
- Modify: `scripts/publication/inventory_test.go`
- Modify: `scripts/publication/main.go`
- Modify: `scripts/publication/main_test.go`

- [x] **Step 1: Generate the current-copy review set**

```bash
go run ./scripts/publication inventory --config quality/publication.yaml --output build/publication/inventory.json
```

The inventory command scans the indexed candidate across the explicit current
documentation, skill, and agent roots. It separates `current-copy` from
`history`, `source-mapping`, and compatibility policy IDs. The generated
`build/publication/current-identity-paths.txt` is NUL-delimited and contains
only files with current-copy findings. The identity gate scans the working tree
across the same roots, so unstaged rewrites cannot escape review.

- [x] **Step 2: Rewrite current copy**

Use the YHC name, command, module, public links, and canonical environment
names. Preserve old names where the text describes the private archive, a
historical commit, an old client, a legacy state/environment/protocol contract,
or a source mapping. Keep current `.eino-agent` state-owner facts until the
state-foundation and state-continuity plans change their production defaults;
those plans own the later `.yhc` examples.

- [x] **Step 3: Run identity and docs gates**

```bash
go test ./scripts/publication -run '^TestCurrentIdentityAllowsLegacyOnlyForHistoryMappingAndCompatibility$' -count=1
make docs-check
git diff --check
```

- [x] **Step 4: Commit**

```bash
git add scripts/publication/identity.go \
  scripts/publication/identity_test.go \
  scripts/publication/inventory.go \
  scripts/publication/inventory_test.go \
  scripts/publication/main.go \
  scripts/publication/main_test.go \
  docs/superpowers/plans/2026-08-09-yhc-core-identity.md
git add --pathspec-from-file=build/publication/current-identity-paths.txt --pathspec-file-nul
git diff --cached --check
git commit -m "docs: adopt YHC current identity"
```

## Task 6: Close Core Identity

- [x] Run the Core Identity local gates:

```bash
make fmt
make lint
make test
make build
make docs-check
git diff --check
```

- [x] Verify the remaining old module/command/product matches in the Core
  Identity scope belong to explicit `history`, `source-mapping`,
  `legacy-state`, `legacy-environment`, or `legacy-protocol` rules.
- [ ] After Publication Readiness Task 3 resolves every path decision, run
  `make publication-check-policy` and record the intentional module hard break
  plus the absence of a command shim in
  `docs/publication/root-clearance.md`.

The last item is a downstream publication-readiness obligation: Task 3 creates
`root-clearance.md` and clears `quality/publication.yaml`. It does not block the
state and protocol leaves from consuming the completed identity implementation.

State-root and protocol checks may still be red at this point only where the
program graph marks their leaf plans pending. No unrelated runtime test may be
accepted as pending.
