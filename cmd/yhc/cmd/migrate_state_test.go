package cmd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	enginecron "github.com/abietic/yhc/engine/cron"
	"github.com/abietic/yhc/engine/memdir"
	"github.com/abietic/yhc/engine/permission"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statepath"
)

func TestMigrateStateProductionRegistersPlainOwners(t *testing.T) {
	command := newMigrateStateCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"inspect"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := "Registered state migration owners:\n- agent-memory\n- agent-memory-local\n- approvals\n- cron\n- history\n- keybindings\n- memory\n- permission-review-audit\n- session\n- settings\n- worktree\n"
	if output.String() != want {
		t.Fatalf("owner listing = %q, want %q", output.String(), want)
	}
}

func TestMigrateStateHasNoWorktreeApplyOperation(t *testing.T) {
	project := t.TempDir()
	inspectCalls := 0
	dependencies := migrateStateDependencies{
		owners: map[string]migrateStateOwner{
			"worktree": {
				projectContext: true,
				inspect: func(_ context.Context, request migrateStateRequest) (migrateStateResult, error) {
					inspectCalls++
					if request.Scope != "project" {
						return migrateStateResult{}, errMigrateStateScopeUnavailable
					}
					return migrateStateResult{
						Status:   "ready",
						Count:    2,
						HasCount: true,
						Records: []migrateStateRecord{
							{RecordID: "active-one", Status: "active"},
							{RecordID: "unknown-two", Status: "unavailable"},
						},
					}, nil
				},
			},
		},
		projectRoot: func() (string, error) { return project, nil },
		userHome:    func() (string, error) { return t.TempDir(), nil },
	}

	output, err := executeMigrateStateCommand(
		t, dependencies, "inspect", "--owner", "worktree",
	)
	if err != nil || output != "owner=worktree scope=project status=ready count=2\nrecord=active-one status=active\nrecord=unknown-two status=unavailable\n" {
		t.Fatalf("inspect output=%q err=%v", output, err)
	}
	if inspectCalls != 1 {
		t.Fatalf("inspect calls = %d", inspectCalls)
	}

	_, err = executeMigrateStateCommand(
		t, dependencies, "apply", "--scope", "project", "--owner", "worktree",
	)
	if ExitCode(err) != ExitUsage || inspectCalls != 1 {
		t.Fatalf("worktree apply err=%v inspectCalls=%d", err, inspectCalls)
	}
	_, err = executeMigrateStateCommand(
		t, dependencies, "inspect", "--scope", "user", "--owner", "worktree",
	)
	if ExitCode(err) != ExitUsage || inspectCalls != 1 {
		t.Fatalf("worktree user inspect err=%v inspectCalls=%d", err, inspectCalls)
	}
}

func TestCronMigrationCommandDoesNotInitializeRuntime(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("PROV", "unsupported-provider-that-must-not-be-read")
	legacyRoot := filepath.Join(project, identity.LegacyDirName)
	if err := os.Mkdir(legacyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(legacyRoot, "scheduled_tasks.json")
	legacyData := []byte(`{"tasks":[{"id":"task-1","cron":"* * * * *","prompt":"private","createdAt":1}]}`)
	if err := os.WriteFile(legacy, legacyData, 0o644); err != nil {
		t.Fatal(err)
	}
	dependencies := migrateStateDependencies{
		owners:      productionMigrateStateOwners(),
		projectRoot: func() (string, error) { return project, nil },
		userHome:    func() (string, error) { return home, nil },
	}

	output, err := executeMigrateStateCommand(
		t, dependencies, "inspect", "--owner", "cron",
	)
	if err != nil || output != "owner=cron scope=project status=ready count=1\n" {
		t.Fatalf("cron inspect output=%q err=%v", output, err)
	}
	if _, err := executeMigrateStateCommand(
		t, dependencies, "apply", "--owner", "cron",
	); ExitCode(err) != ExitUsage {
		t.Fatalf("unconfirmed cron apply error=%v", err)
	}
	output, err = executeMigrateStateCommand(
		t, dependencies, "apply", "--owner", "cron", "--confirm-legacy-stopped",
	)
	if err != nil || output != "owner=cron scope=project status=imported count=1\n" {
		t.Fatalf("cron apply output=%q err=%v", output, err)
	}
	canonical, err := os.ReadFile(enginecron.GetCronFilePath(project))
	if err != nil || !bytes.Equal(canonical, legacyData) {
		t.Fatalf("canonical cron mismatch: %v", err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(after, legacyData) {
		t.Fatalf("legacy cron changed: %v", err)
	}
}

func TestMigrateStateProductionMemoryAdapter(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	for _, name := range []string{
		"YHC_CONFIG_DIR", "EINO_AGENT_CONFIG_DIR",
		"YHC_REMOTE_MEMORY_DIR", "EINO_AGENT_REMOTE_MEMORY_DIR",
		"YHC_MEMORY_PATH_OVERRIDE", "EINO_AGENT_MEMORY_PATH_OVERRIDE",
	} {
		t.Setenv(name, "")
	}
	spec, err := memdir.MemoryMigrationSpec("memory", "user", project)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, identity.LegacyDirName, filepath.FromSlash(spec.SourceRel))
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, identity.LegacyDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "MEMORY.md"), []byte("index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies := migrateStateDependencies{
		owners:      productionMigrateStateOwners(),
		projectRoot: func() (string, error) { return project, nil },
		userHome:    func() (string, error) { return home, nil },
	}
	output, err := executeMigrateStateCommand(
		t,
		dependencies,
		"apply", "--scope", "user", "--owner", "memory",
	)
	if err != nil || output != "owner=memory scope=user status=imported\n" {
		t.Fatalf("apply output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(home, identity.ProjectDirName, filepath.FromSlash(spec.TargetRel), "MEMORY.md")); err != nil {
		t.Fatalf("canonical memory missing: %v", err)
	}
}

func TestMigrateStateProductionSettingsAdapter(t *testing.T) {
	project := t.TempDir()
	legacy := filepath.Join(project, identity.LegacyDirName)
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(legacy, "settings.json"),
		[]byte(`{"model":"gpt-4o"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	dependencies := migrateStateDependencies{
		owners:      productionMigrateStateOwners(),
		projectRoot: func() (string, error) { return project, nil },
		userHome:    func() (string, error) { return t.TempDir(), nil },
	}
	output, err := executeMigrateStateCommand(
		t,
		dependencies,
		"apply", "--scope", "project", "--owner", "settings",
	)
	if err != nil || output != "owner=settings scope=project status=imported\n" {
		t.Fatalf("apply output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(project, identity.ProjectDirName, "settings.json")); err != nil {
		t.Fatalf("canonical settings missing: %v", err)
	}

	_, err = executeMigrateStateCommand(
		t,
		dependencies,
		"inspect", "--scope", "project", "--owner", "keybindings",
	)
	if err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("project keybindings error = %v, want usage", err)
	}
}

func TestSessionMigrationCommandDoesNotInitializeRuntime(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROV", "unsupported-provider-that-must-not-be-read")
	t.Setenv("YHC_SESSION_CATALOG", "")
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(project, identity.LegacyDirName, "transcripts")
	writeSessionCLITranscript(t, legacyDir, "migrate-session", "migrate without runtime")
	legacyCatalog := filepath.Join(home, identity.LegacyDirName, "session-roots.json")
	if err := enginesession.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "migrate-session.jsonl")
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := migrateStateDependencies{
		owners:      productionMigrateStateOwners(),
		projectRoot: func() (string, error) { return project, nil },
		userHome:    func() (string, error) { return home, nil },
	}

	if _, err := executeMigrateStateCommand(
		t,
		dependencies,
		"apply", "--owner", "session", "--session", "migrate-session",
	); ExitCode(err) != ExitUsage {
		t.Fatalf("unconfirmed session migration error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(project, identity.ProjectDirName, "transcripts", "migrate-session.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfirmed migration wrote canonical transcript: %v", err)
	}

	output, err := executeMigrateStateCommand(
		t,
		dependencies,
		"apply", "--owner", "session", "--session", "migrate-session",
		"--confirm-legacy-stopped",
	)
	if err != nil || output != "owner=session scope=project status=imported\n" {
		t.Fatalf("session apply output=%q err=%v", output, err)
	}
	canonical, err := os.ReadFile(filepath.Join(
		project, identity.ProjectDirName, "transcripts", "migrate-session.jsonl",
	))
	if err != nil || string(canonical) != string(legacyBefore) {
		t.Fatalf("canonical session mismatch: %v", err)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("legacy session changed: %v", err)
	}
}

func TestSessionMigrationRefusesExplicitCatalogOverrideWithoutWrites(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("YHC_SESSION_CATALOG", filepath.Join(t.TempDir(), "catalog.json"))
	legacyDir := filepath.Join(project, identity.LegacyDirName, "transcripts")
	writeSessionCLITranscript(t, legacyDir, "override-session", "do not import")
	legacyPath := filepath.Join(legacyDir, "override-session.jsonl")
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyCatalog := filepath.Join(home, identity.LegacyDirName, "session-roots.json")
	if err := enginesession.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}

	_, err = executeMigrateStateCommand(
		t,
		migrateStateDependencies{
			owners:      productionMigrateStateOwners(),
			projectRoot: func() (string, error) { return project, nil },
			userHome:    func() (string, error) { return home, nil },
		},
		"apply", "--owner", "session", "--session", "override-session",
		"--confirm-legacy-stopped",
	)
	if ExitCode(err) != ExitUsage {
		t.Fatalf("explicit catalog migration error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(project, identity.ProjectDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("explicit catalog migration created canonical state: %v", statErr)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("explicit catalog migration changed legacy transcript: %v", err)
	}
}

func TestCredentialStoresAreNeverEnumeratedOrCopied(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	pair := identity.RuntimeEnvPermissionReviewAuditDir.Pair()
	t.Setenv(pair.Canonical, "")
	t.Setenv(pair.Legacy, "")
	credentialStores := map[string][]byte{
		filepath.Join(home, ".claude", "credentials.json"):      []byte(`{"credential":"credential-file-sentinel"}`),
		filepath.Join(home, ".claude", "mcp_oauth_tokens.json"): []byte(`{"token":"oauth-file-sentinel"}`),
		filepath.Join(project, ".mcp.json"):                     []byte(`{"command":"mcp-config-sentinel"}`),
	}
	for path, data := range credentialStores {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	projectRoots, err := statepath.ProjectRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	tracker := permission.NewApprovalTracker()
	tracker.Approve(permission.ApprovalKey{
		ToolName:       "Bash",
		CommandPattern: "go test ./...",
		ExactCommand:   true,
	}, "user", false)
	if err := tracker.SaveTo(filepath.Join(projectRoots.Legacy, "approvals.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoots.Legacy, "history"), []byte("go test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	userRoots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	auditDir := filepath.Join(userRoots.Legacy, "permission-review-audit", "v1")
	audit, err := permission.NewReviewAuditStore(permission.ReviewAuditStoreOptions{
		Dir:            auditDir,
		Now:            func() time.Time { return time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC) },
		LockTimeout:    time.Second,
		StaleLockAfter: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(t.Context(), permission.ReviewAuditRecord{
		SchemaVersion:      permission.ReviewAuditSchemaVersion,
		EventID:            "0123456789abcdef0123456789abcdef",
		OccurredAt:         time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC),
		Kind:               permission.ReviewAuditKindEligible,
		CanonicalTool:      "Bash",
		ActionKind:         "filesystem_read",
		DeterministicClass: "review",
	}); err != nil {
		t.Fatal(err)
	}

	dependencies := migrateStateDependencies{
		owners:      productionMigrateStateOwners(),
		projectRoot: func() (string, error) { return project, nil },
		userHome:    func() (string, error) { return home, nil },
	}
	for _, operation := range []struct {
		scope string
		owner string
	}{
		{scope: "project", owner: "approvals"},
		{scope: "project", owner: "history"},
		{scope: "user", owner: "permission-review-audit"},
	} {
		output, err := executeMigrateStateCommand(
			t,
			dependencies,
			"apply", "--scope", operation.scope, "--owner", operation.owner,
		)
		if err != nil || !strings.Contains(output, "status=imported") {
			t.Fatalf("apply %s output=%q err=%v", operation.owner, output, err)
		}
		for _, marker := range []string{"credential-file-sentinel", "oauth-file-sentinel", "mcp-config-sentinel"} {
			if strings.Contains(output, marker) {
				t.Fatalf("apply %s leaked credential marker", operation.owner)
			}
		}
	}

	for path, before := range credentialStores {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("credential store changed: %v", err)
		}
	}
	for _, root := range []string{projectRoots.Canonical, userRoots.Canonical} {
		assertStateTreeExcludesCredentialStores(t, root)
	}
}

func assertStateTreeExcludesCredentialStores(t *testing.T, root string) {
	t.Helper()
	forbiddenNames := map[string]bool{
		"credentials.json":      true,
		"mcp_oauth_tokens.json": true,
		".mcp.json":             true,
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if forbiddenNames[entry.Name()] {
			t.Fatalf("credential store copied to canonical state")
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		if strings.Contains(text, "credential-file-sentinel") ||
			strings.Contains(text, "oauth-file-sentinel") ||
			strings.Contains(text, "mcp-config-sentinel") {
			t.Fatalf("credential value copied to canonical state")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrateStateRequiresOneKnownOwnerForApply(t *testing.T) {
	projectRoot := t.TempDir()
	canonicalProjectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	applyCalls := 0
	dependencies := migrateStateDependencies{
		owners: map[string]migrateStateOwner{
			"settings": {
				apply: func(_ context.Context, request migrateStateRequest) (migrateStateResult, error) {
					applyCalls++
					if request.Owner != "settings" || request.Scope != "project" {
						t.Fatalf("apply request = %#v", request)
					}
					if request.Roots.Canonical != filepath.Join(canonicalProjectRoot, identity.ProjectDirName) ||
						request.Roots.Legacy != filepath.Join(canonicalProjectRoot, identity.LegacyDirName) {
						t.Fatalf("apply roots = %#v", request.Roots)
					}
					return migrateStateResult{Status: "imported"}, nil
				},
			},
		},
		projectRoot: func() (string, error) { return projectRoot, nil },
		userHome:    func() (string, error) { return userHome, nil },
	}

	for _, args := range [][]string{
		{"apply", "--scope", "project"},
		{"apply", "--scope", "project", "--owner", "unknown-private-sentinel"},
		{"apply", "--owner", "settings"},
	} {
		_, err := executeMigrateStateCommand(t, dependencies, args...)
		if err == nil || ExitCode(err) != ExitUsage {
			t.Fatalf("args %v error = %v, want usage error", args, err)
		}
	}
	if applyCalls != 0 {
		t.Fatalf("invalid apply calls = %d, want zero", applyCalls)
	}

	output, err := executeMigrateStateCommand(
		t,
		dependencies,
		"apply", "--scope", "project", "--owner", "settings",
	)
	if err != nil {
		t.Fatal(err)
	}
	if applyCalls != 1 || output != "owner=settings scope=project status=imported\n" {
		t.Fatalf("apply calls=%d output=%q", applyCalls, output)
	}
}

func TestMigrateStateInspectListsOnlyRegisteredOwners(t *testing.T) {
	called := false
	dependencies := migrateStateDependencies{
		owners: map[string]migrateStateOwner{
			"settings": {inspect: func(context.Context, migrateStateRequest) (migrateStateResult, error) {
				called = true
				return migrateStateResult{Status: "ready"}, nil
			}},
			"memory": {inspect: func(context.Context, migrateStateRequest) (migrateStateResult, error) {
				called = true
				return migrateStateResult{Status: "ready"}, nil
			}},
		},
		projectRoot: func() (string, error) {
			t.Fatal("owner listing resolved project state")
			return "", nil
		},
		userHome: func() (string, error) {
			t.Fatal("owner listing resolved user state")
			return "", nil
		},
	}

	output, err := executeMigrateStateCommand(t, dependencies, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if called || output != "Registered state migration owners:\n- memory\n- settings\n" {
		t.Fatalf("called=%t output=%q", called, output)
	}
}

func TestMigrateStateDiagnosticsContainNoValues(t *testing.T) {
	privateSentinel := "state-migration-private-sentinel"
	root := filepath.Join(t.TempDir(), privateSentinel)
	dependencies := migrateStateDependencies{
		owners: map[string]migrateStateOwner{
			"settings": {
				inspect: func(context.Context, migrateStateRequest) (migrateStateResult, error) {
					return migrateStateResult{}, errors.New(privateSentinel)
				},
			},
		},
		projectRoot: func() (string, error) { return root, nil },
		userHome:    func() (string, error) { return root, nil },
	}

	output, err := executeMigrateStateCommand(
		t,
		dependencies,
		"inspect", "--scope", "project", "--owner", "settings",
	)
	if err == nil {
		t.Fatal("injected inspect failure was accepted")
	}
	if strings.Contains(output, privateSentinel) || strings.Contains(err.Error(), privateSentinel) {
		t.Fatalf("migration diagnostic leaked a value: output=%q error=%q", output, err)
	}

	output, err = executeMigrateStateCommand(
		t,
		dependencies,
		"inspect", "--scope", "private-scope-sentinel",
	)
	if err == nil || strings.Contains(output+err.Error(), "private-scope-sentinel") {
		t.Fatalf("owner-list scope diagnostic leaked input: output=%q error=%v", output, err)
	}

	output, err = executeMigrateStateCommand(
		t,
		dependencies,
		"inspect", "--scope", "private-scope-sentinel", "--owner", "settings",
	)
	if err == nil || strings.Contains(output+err.Error(), "private-scope-sentinel") {
		t.Fatalf("invalid scope diagnostic leaked input: output=%q error=%v", output, err)
	}

	output, err = executeMigrateStateCommand(
		t,
		dependencies,
		"inspect", "--scope", "project", "--owner", "unknown-private-sentinel",
	)
	if err == nil || strings.Contains(output+err.Error(), "unknown-private-sentinel") {
		t.Fatalf("unknown owner diagnostic leaked input: output=%q error=%v", output, err)
	}
}

func executeMigrateStateCommand(
	t *testing.T,
	dependencies migrateStateDependencies,
	args ...string,
) (string, error) {
	t.Helper()
	command := newMigrateStateCommandWithDependencies(dependencies)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return output.String(), err
}
