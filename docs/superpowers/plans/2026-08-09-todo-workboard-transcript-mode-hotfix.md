# Todo And WorkBoard Transcript Mode Hotfix Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Last verified:** 2026-08-09
**Completed:** 2026-08-09

> **Ownership:** executable test-first steps for the approved
> [Todo and WorkBoard transcript mode hotfix design](../specs/2026-08-08-todo-workboard-transcript-mode-hotfix-design.md)

**Goal:** Repair legacy private transcript roots for Todo/Task persistence, describe `TodoWrite` as trusted durable runtime state, and prevent Darwin Guest Bash from modifying `.eino-agent` control-plane files.

**Architecture:** `LogicalWorkAdapter` first performs strict read-only Store inspection, then prepares and identity-checks its private transcript root only after a successful legacy result. Permission metadata gains one truthful internal-state default-safe predicate, while Darwin Guest policy adds a write denial for the host-owned `.eino-agent` subtree.

**Tech Stack:** Go 1.26.5, `os.OpenRoot`, QueryEngine permission descriptors, Darwin Seatbelt, Go white-box tests, Makefile gates.

## Global Constraints

- Preserve explicit permission `deny` and `ask` precedence and all non-default-safe permission ordering.
- Permission modes cannot select, disable, or broaden containment bindings.
- Unsafe storage fails before model/tool execution and before any WorkBoard artifact write.
- Low-level read-only Store inspection must not chmod or create directories.
- `.eino-agent` protection is write-only; do not claim Guest no-read isolation.
- Do not change WorkBoard schemas, artifact names, fork/recovery formats, or environment inheritance.
- Preserve unrelated dirty/untracked files and stage only task-owned files.

---

### Task 1: Prepare legacy transcript roots at the WorkBoard boundary

**Files:**
- Modify: `engine/internal/workboard/secure_store.go`
- Modify: `engine/internal/workboard/adapter.go`
- Modify: `engine/internal/workboard/adapter_test.go`
- Modify: `engine/internal/workboard/secure_store_test.go`
- Modify: `engine/p31_1b_workboard_authority_test.go`
- Modify: `docs/architecture/runtime/tasks-and-agents.md`
- Modify: `docs/architecture/state/transcripts.md`

**Interfaces:**
- Consumes: `AdapterConfig.Dir`, `os.OpenRoot`, `os.SameFile`, and existing `ArtifactStore.validateDirectory`.
- Produces: `preparePrivateTranscriptDirectory(string) error` plus
  deterministic test-hook/observation variants; `NewLogicalWorkAdapter` calls
  preparation only after `Store.Inspect` successfully reports legacy authority,
  then binds the secured directory identity through first cutover.

- [x] **Step 1: Write failing legacy-directory regressions**

Rename the existing bare-legacy failure test to `TestLogicalWorkAdapterRepairsBareLegacyDirectoryBeforeMutation`. Keep a `0755` directory, then require adapter construction to change it to `0700` and the first Task mutation to create authority, marker, and backup artifacts.

Add a missing-directory case:

```go
func TestLogicalWorkAdapterPreparesMissingPrivateDirectory(t *testing.T) {
    dir := filepath.Join(t.TempDir(), "transcripts")
    adapter, err := NewLogicalWorkAdapter(AdapterConfig{
        SessionID: "session", Dir: dir,
    }, tools.TaskManagerSnapshot{NextID: 1})
    if err != nil || adapter == nil {
        t.Fatalf("prepare missing directory: adapter=%v err=%v", adapter, err)
    }
    info, err := os.Lstat(dir)
    if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
        t.Fatalf("prepared directory = %#v, %v", info, err)
    }
}
```

Add regular-file and final-symlink cases. Skip only the symlink case on Windows, verify its real target remains unchanged, and assert no WorkBoard artifacts exist.

- [x] **Step 2: Add a deterministic replacement test**

Call a test-hook variant that runs immediately after `os.OpenRoot`. In the hook rename the opened directory and create a same-path replacement. Require `changed while securing` and zero artifacts:

```go
err := preparePrivateTranscriptDirectoryWithHook(dir, func() {
    if err := os.Rename(dir, moved); err != nil { t.Fatal(err) }
    if err := os.Mkdir(dir, 0o700); err != nil { t.Fatal(err) }
})
if err == nil || !strings.Contains(err.Error(), "changed while securing") {
    t.Fatalf("replacement error = %v", err)
}
```

Also replace the path immediately after Store inspection and again after
successful preparation but before first cutover. Both cases must fail and
leave the original and replacement directories free of WorkBoard artifacts.

- [x] **Step 3: Verify RED**

Run:

```bash
go test ./engine/internal/workboard ./engine -run 'TestLogicalWorkAdapter(RepairsBareLegacyDirectoryBeforeMutation|PreparesMissingPrivateDirectory)|TestPreparePrivateTranscriptDirectory|TestP311bQueryEngineRepairsLegacyTranscriptDirectory' -count=1
```

Expected: FAIL because no preparation helper exists and `0755` still fails.

- [x] **Step 4: Implement pinned directory preparation**

Add this shape to `secure_store.go`, with stage-specific wrapped errors:

```go
const privateTranscriptDirectoryMode os.FileMode = 0o700

func preparePrivateTranscriptDirectory(dir string) error {
    return preparePrivateTranscriptDirectoryWithHook(dir, nil)
}

func preparePrivateTranscriptDirectoryWithHook(dir string, afterOpen func()) error {
    info, err := os.Lstat(dir)
    if errors.Is(err, os.ErrNotExist) {
        if err := os.MkdirAll(dir, privateTranscriptDirectoryMode); err != nil {
            return fmt.Errorf("workboard authority: create transcript directory: %w", err)
        }
        info, err = os.Lstat(dir)
    }
    if err != nil { return fmt.Errorf("workboard authority: inspect transcript directory: %w", err) }
    if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
        return errors.New("workboard authority: transcript directory is not a directory")
    }
    root, err := os.OpenRoot(dir)
    if err != nil { return fmt.Errorf("workboard authority: open transcript directory: %w", err) }
    defer root.Close() //nolint:errcheck
    if afterOpen != nil { afterOpen() }
    opened, openErr := root.Stat(".")
    current, pathErr := os.Lstat(dir)
    if openErr != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(current, opened) {
        return errors.New("workboard authority: transcript directory changed while securing")
    }
    if err := root.Chmod(".", privateTranscriptDirectoryMode); err != nil {
        return fmt.Errorf("workboard authority: secure transcript directory: %w", err)
    }
    secured, secureErr := root.Stat(".")
    current, pathErr = os.Lstat(dir)
    if secureErr != nil || pathErr != nil || !os.SameFile(current, secured) {
        return errors.New("workboard authority: transcript directory changed after securing")
    }
    if secured.Mode().Perm() != privateTranscriptDirectoryMode {
        return errors.New("workboard authority: transcript directory mode is not 0700")
    }
    return nil
}
```

Validate blank input before `Lstat`. Call the production wrapper after the
read-only `Store.Inspect` result is known to be legacy; do not call it from
`Store.Inspect` or `NewArtifactStore`. This preserves fail-closed handling for
invalid committed or prepared authority instead of silently chmodding it.
Bind the secured directory identity to the adapter Store and require both the
first cutover factory and artifact root-open path to match it.

- [x] **Step 5: Add the QueryEngine matrix and verify GREEN**

Add `TestP311bQueryEngineRepairsLegacyTranscriptDirectory` covering Default/Auto and ambient/workspace-write. Each case starts with `0755`, asserts permission admission, executes `TodoWrite`, then checks success and `0700`. Workspace-write must not launch Bash or require Darwin availability.

After the successful mutation, close the first engine, construct a fresh engine
against the same Session and transcript directory, and assert the Todo remains
present through the reloaded logical-work adapter.

Run:

```bash
go test ./engine/internal/workboard ./engine -run 'TestLogicalWorkAdapter|TestPreparePrivateTranscriptDirectory|TestP311bQueryEngine' -count=1
go test -race ./engine/internal/workboard ./engine -run 'TestPreparePrivateTranscriptDirectory|TestP311bQueryEngineRepairsLegacyTranscriptDirectory' -count=3
```

Expected: PASS without sleeps or private-session access.

- [x] **Step 6: Synchronize architecture and commit**

Document mutation-capable root preparation, read-only Store inspection, and durable WorkBoard authority.

```bash
git add engine/internal/workboard/secure_store.go engine/internal/workboard/adapter.go engine/internal/workboard/adapter_test.go engine/internal/workboard/secure_store_test.go engine/p31_1b_workboard_authority_test.go docs/architecture/runtime/tasks-and-agents.md docs/architecture/state/transcripts.md
git commit -m "fix(workboard): repair private transcript directory mode"
```

### Task 2: Make Todo default-safe metadata truthful

**Files:**
- Modify: `tools/registry.go`
- Modify: `tools/tool_contract_test.go`
- Modify: `engine/permission_action.go`
- Modify: `engine/permission_action_test.go`
- Modify: `engine/query_engine_permission_test.go`
- Modify: `engine/golden_test.go`
- Modify: `engine/engine.go`
- Modify: `docs/architecture/capabilities/permissions.md`

**Interfaces:**
- Consumes: `ToolImpl.DefaultPermissionAllowed`, `ToolCapabilities`, explicit rule ordering.
- Produces: `PermissionActionDescriptor.InternalStateDefaultSafe bool` and `defaultSafeInternalState(tools.ToolImpl, tools.ToolCapabilities) bool`.

- [x] **Step 1: Write failing capability and permission tests**

Require Todo's golden action kind to be `ToolActionRuntimeState`, add Auto to `TestTodoWriteDefaultPermissionPreservesExplicitRules`, and add:

```go
action, err := engine.buildPermissionActionDescriptor(
    "TodoWrite", map[string]any{"todos": []any{}},
    &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}},
)
if err != nil { t.Fatal(err) }
if action.ActionKind != tools.ToolActionRuntimeState || !action.InternalStateDefaultSafe {
    t.Fatalf("TodoWrite descriptor = %#v", action)
}
```

Update tool-contract assertions to say host-owned runtime state, not process-local state.

- [x] **Step 2: Verify RED**

```bash
go test ./tools ./engine -run 'TestContractTodoWriteTool|TestGoldenRegistryOwnsEveryBuiltinCapability|TestTodoWriteDefaultPermissionPreservesExplicitRules|TestTodoWriteRuntimeStateDescriptor' -count=1
```

Expected: FAIL on process-local metadata and the missing descriptor field.

- [x] **Step 3: Implement one internal-state predicate**

Rename `ProcessLocalDefaultSafe` to `InternalStateDefaultSafe` in descriptor construction, equality/rebuild checks, and the fast path. Centralize the value:

```go
func defaultSafeInternalState(impl tools.ToolImpl, capabilities tools.ToolCapabilities) bool {
    if !impl.DefaultPermissionAllowed || !capabilities.Declared || capabilities.Origin != tools.ToolOriginBuiltin {
        return false
    }
    return capabilities.ActionKind == tools.ToolActionProcessLocal ||
        capabilities.ActionKind == tools.ToolActionRuntimeState
}
```

Set Todo's capability to built-in `ToolActionRuntimeState`. Keep explicit-rule evaluation and fast-path ordering unchanged.

- [x] **Step 4: Verify GREEN and commit**

```bash
go test ./tools ./engine -run 'TodoWrite|PermissionAction|GoldenRegistryOwnsEveryBuiltinCapability' -count=1
go test -race ./engine -run 'TestTodoWriteDefaultPermissionPreservesExplicitRules|TestTodoWriteRuntimeStateDescriptor' -count=3
git add tools/registry.go tools/tool_contract_test.go engine/permission_action.go engine/permission_action_test.go engine/query_engine_permission_test.go engine/golden_test.go engine/engine.go docs/architecture/capabilities/permissions.md
git commit -m "fix(permission): classify todo as trusted runtime state"
```

### Task 3: Reserve `.eino-agent` writes for the host

**Files:**
- Modify: `engine/execution_policy.go`
- Modify: `engine/execution_policy_test.go`
- Modify: `docs/architecture/platform/runtime-services.md`
- Modify: `docs/migration/plans/p42-host-execution-containment.md`
- Modify: `docs/migration/verification/p51-1-darwin-guest-seatbelt.md`

**Interfaces:**
- Consumes: `workspaceGuestRoots`, `Spec.DeniedRoots`, Seatbelt deny-after-allow rules.
- Produces: denied write root `<canonical CWD>/.eino-agent` for Guest bindings.

- [x] **Step 1: Write failing policy and Darwin tests**

Add `filepath.Join(canonicalRoot, ".eino-agent")` to the expected policy-deny list. Add `.eino-agent/transcripts/foreign-session.workboard.json` as a byte-stable sentinel in the real Darwin control-plane mutation loop while preserving ordinary workspace-write success.

- [x] **Step 2: Verify RED**

```bash
go test ./engine -run 'TestP511WorkspaceGuestIncludesRuntimeRootsAndControlPlaneDenies' -count=1
go test ./engine -run 'TestP511DarwinGuestRunsGoAndProtectsControlPlaneWrites' -count=1
```

Expected: policy test FAILS; real enforcement either FAILS on Darwin or keeps its explicit non-Darwin skip.

- [x] **Step 3: Implement the immutable denied root**

Append before sorting/compaction:

```go
filepath.Join(cwd, ".eino-agent"),
```

Do not add a read denial, permission fact, hook/MCP restriction, or ambient fallback.

- [x] **Step 4: Verify GREEN and commit**

```bash
go test ./engine/containment ./engine ./tools -run 'P511|Seatbelt|Binding' -count=1
go test -race ./engine/containment ./engine ./tools -run 'P511|Seatbelt|Binding' -count=3
git add engine/execution_policy.go engine/execution_policy_test.go docs/architecture/platform/runtime-services.md docs/migration/plans/p42-host-execution-containment.md docs/migration/verification/p51-1-darwin-guest-seatbelt.md
git commit -m "fix(containment): protect eino agent control-plane writes"
```

### Task 4: Cross-boundary verification and closeout

**Files:**
- Modify: `docs/superpowers/plans/README.md`
- Modify: `docs/superpowers/plans/2026-08-09-todo-workboard-transcript-mode-hotfix.md`
- Modify: `docs/superpowers/specs/README.md`
- Modify: `docs/superpowers/specs/2026-08-08-todo-workboard-transcript-mode-hotfix-design.md`

**Interfaces:**
- Consumes: the three completed behavior commits and exact current diff.
- Produces: source-backed closeout state; no migration queue promotion.

- [x] **Step 1: Run combined focused and race verification**

```bash
go test ./engine/internal/workboard ./tools ./engine -run 'TodoWrite|LogicalWorkAdapter|PreparePrivateTranscriptDirectory|P511WorkspaceGuestIncludesRuntimeRootsAndControlPlaneDenies' -count=1
go test -race ./engine/internal/workboard ./engine -run 'TodoWrite|PreparePrivateTranscriptDirectory|P511WorkspaceGuestIncludesRuntimeRootsAndControlPlaneDenies' -count=3
```

- [x] **Step 2: Run every repository gate**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

Expected: all pass. Remote CI and real product acceptance remain separate evidence.

- [x] **Step 3: Perform independent bounded review**

Review `origin/master...HEAD` for unsafe chmod/symlink handling, permission ordering drift, Guest broadening, non-Darwin regressions, missing restart/explicit-rule tests, and overclaimed documentation. Fix confirmed findings and rerun affected tests.

- [x] **Step 4: Mark execution state and commit closeout**

Mark this plan executed and the spec implemented only after local gates pass.

```bash
git add docs/superpowers/plans/README.md docs/superpowers/plans/2026-08-09-todo-workboard-transcript-mode-hotfix.md docs/superpowers/specs/README.md docs/superpowers/specs/2026-08-08-todo-workboard-transcript-mode-hotfix-design.md
git commit -m "docs: close todo workboard transcript mode hotfix"
```

- [x] **Step 5: Prepare PR evidence**

State the reproduced failure, `project-native` adoption, unchanged explicit permission ordering, directory-mode migration and rollback consequence, write-only Guest control-plane denial, exact local gates, remote CI status, and real Darwin/product acceptance status.
