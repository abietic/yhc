package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

func TestP311bQueryEngineBindsSingleAuthorityBeforeToolMutation(t *testing.T) {
	dir := p311bPrivateDir(t)
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session",
		ThreadID:      "session",
		TranscriptDir: dir,
		CWD:           dir,
		ToolRegistry:  registry,
	})
	defer engine.Close()
	for _, suffix := range p311bArtifactSuffixes() {
		if _, err := os.Lstat(
			filepath.Join(dir, "session"+suffix),
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact %s exists before mutation: %v", suffix, err)
		}
	}

	result, err := engine.toolExecutor(
		context.Background(),
		"TaskCreate",
		`{"subject":"task","description":"description","activeForm":"doing task"}`,
	)
	if err != nil {
		t.Fatalf("TaskCreate through QueryEngine: %v", err)
	}
	if result != "Task #1 created successfully: task" {
		t.Fatalf("TaskCreate result = %q", result)
	}
	for _, suffix := range p311bArtifactSuffixes() {
		if _, err := os.Lstat(
			filepath.Join(dir, "session"+suffix),
		); err != nil {
			t.Fatalf("artifact %s missing after mutation: %v", suffix, err)
		}
	}
	if _, err := engine.toolExecutor(
		context.Background(),
		"TodoWrite",
		`{"todos":[{"content":"todo","status":"pending","activeForm":"doing todo"}]}`,
	); err != nil {
		t.Fatalf("TodoWrite through QueryEngine: %v", err)
	}
	state := p311bInspect(t, dir, "session")
	foundTodo := false
	for _, item := range state.Record.Board.Items {
		if item.Source.Kind == "todo" &&
			item.Source.SessionID == "session" &&
			item.Title == "todo" {
			foundTodo = true
		}
	}
	if !foundTodo {
		t.Fatalf("authoritative Todo missing: %+v", state.Record.Board.Items)
	}
}

func TestP311bQueryEngineRepairsLegacyTranscriptDirectory(t *testing.T) {
	tests := []struct {
		name      string
		mode      permission.Mode
		sandboxed bool
	}{
		{name: "default ambient", mode: permission.ModeDefault},
		{name: "auto ambient", mode: permission.ModeAuto},
		{name: "default workspace write", mode: permission.ModeDefault, sandboxed: true},
		{name: "auto workspace write", mode: permission.ModeAuto, sandboxed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "transcripts")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			config := QueryEngineConfig{
				SessionID:      "legacy-session",
				ThreadID:       "legacy-session",
				CWD:            root,
				TranscriptDir:  dir,
				ToolRegistry:   registry,
				PermissionMode: test.mode,
				CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
					return false, "inner permission callback must be bypassed"
				},
				CommandEntrypoint: commands.EntrypointHeadless,
			}
			if test.sandboxed {
				selection, err := NewSandboxSelection(
					containment.ProfileWorkspaceWrite,
					containment.SelectionDefault,
					nil,
				)
				if err != nil {
					t.Fatal(err)
				}
				config.SandboxSelection = selection
			}
			eng := NewQueryEngine(config)
			input := map[string]any{"todos": []any{
				map[string]any{
					"content":    "persisted todo",
					"status":     "pending",
					"activeForm": "persisting todo",
				},
			}}
			allowed, reason := eng.wrappedCanUseTool(
				context.Background(),
				"TodoWrite",
				input,
				&ToolUseContext{Options: &ToolUseOptions{PermissionMode: test.mode}},
			)
			if !allowed {
				eng.Close()
				t.Fatalf("TodoWrite permission denied: %s", reason)
			}
			if _, err := eng.toolExecutor(
				context.Background(),
				"TodoWrite",
				`{"todos":[{"content":"persisted todo","status":"pending","activeForm":"persisting todo"}]}`,
			); err != nil {
				eng.Close()
				t.Fatalf("TodoWrite after legacy repair: %v", err)
			}
			eng.Close()
			info, err := os.Lstat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("repaired transcript directory = %#v, %v", info, err)
			}

			reloaded := NewQueryEngine(config)
			defer reloaded.Close()
			todos, err := reloaded.logicalWorkAdapter.Todos(tools.TodoScope{
				SessionID: "legacy-session",
			})
			if err != nil || len(todos) != 1 || todos[0].Content != "persisted todo" {
				t.Fatalf("reloaded Todos = %+v, %v", todos, err)
			}
		})
	}
}

func TestP311bExplicitChildTodoMutationOwnsFirstCutover(t *testing.T) {
	dir := p311bPrivateDir(t)
	childDir := p311bPrivateDir(t)
	childTodos := []tools.TodoItem{{
		Content:    "child legacy todo",
		Status:     "in_progress",
		ActiveForm: "working child legacy todo",
	}}
	root := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session",
		ThreadID:      "root-thread",
		TranscriptDir: dir,
		CWD:           dir,
	})
	t.Cleanup(root.Close)
	if root.logicalWorkAdapter.Mode() != workboard.AuthorityModeLegacy {
		t.Fatalf("initial authority mode = %q", root.logicalWorkAdapter.Mode())
	}

	child := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session",
		ThreadID:           "child-thread",
		RootSessionID:      "session",
		AgentID:            "child",
		TranscriptDir:      childDir,
		CWD:                childDir,
		ToolRegistry:       p311bDefaultToolRegistry(),
		TaskManager:        root.GetTaskManager(),
		logicalWorkAdapter: root.logicalWorkAdapter,
	})
	t.Cleanup(child.Close)
	if child.logicalWorkErr != nil {
		t.Fatalf("register child legacy Todo scope: %v", child.logicalWorkErr)
	}
	if _, err := child.toolExecutor(
		context.Background(),
		"TodoWrite",
		`{"todos":[{"content":"child legacy todo","status":"in_progress","activeForm":"working child legacy todo"}]}`,
	); err != nil {
		t.Fatalf("write child Todo through explicit owner: %v", err)
	}

	if _, err := root.GetTaskManager().CreateWithError(
		"first root task",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatalf("trigger first root cutover: %v", err)
	}
	state := p311bInspect(t, dir, "session")
	scope := tools.TodoScope{SessionID: "session", AgentID: "child"}
	assertScope := func(
		label string,
		scopes []workboard.TodoScopeCompatibility,
	) {
		t.Helper()
		for _, candidate := range scopes {
			if candidate.SessionID == scope.SessionID &&
				candidate.AgentID == scope.AgentID {
				if len(candidate.CurrentItemIDs) != 1 {
					t.Fatalf("%s child Todo scope = %+v", label, candidate)
				}
				return
			}
		}
		t.Fatalf("%s missing child Todo scope: %+v", label, scopes)
	}
	assertScope("authority", state.Record.Compatibility.TodoScopes)
	for _, candidate := range state.Backup.Compatibility.TodoScopes {
		if candidate.SessionID == scope.SessionID &&
			candidate.AgentID == scope.AgentID &&
			len(candidate.CurrentItemIDs) != 0 {
			t.Fatalf(
				"backup child Todo scope includes post-cutover mutation: %+v",
				candidate,
			)
		}
	}
	projected, err := root.logicalWorkAdapter.Todos(scope)
	if err != nil {
		t.Fatalf("project child Todo scope: %v", err)
	}
	if !reflect.DeepEqual(projected, childTodos) {
		t.Fatalf("projected child Todos = %+v", projected)
	}
}

func p311bDefaultToolRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	return registry
}

func TestP311bPreparedQuarantineRejectsBeforeModelDispatch(t *testing.T) {
	dir := p311bPrivateDir(t)
	artifacts, err := workboard.NewArtifactStore(
		dir,
		"session",
		func(
			kind workboard.ArtifactKind,
			stage workboard.FailureStage,
		) error {
			if kind == workboard.ArtifactAuthority &&
				(stage == workboard.FailureDirSync ||
					stage == workboard.FailureRollback) {
				return errors.New("stop")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Write(
		workboard.ArtifactAuthority,
		[]byte("{}\n"),
	); err == nil || !workboard.IsDurabilityUncertain(err) {
		t.Fatalf("prepare quarantine error = %v", err)
	}

	model := &p172NoDispatchModel{}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session",
		ThreadID:      "session",
		TranscriptDir: dir,
		CWD:           dir,
		ChatModel:     model,
	})
	t.Cleanup(engine.Close)
	events, terminal := engine.SubmitMessage(
		context.Background(),
		"must not reach the model",
	)
	for range events {
	}
	if terminal.Reason != TerminalModelError ||
		terminal.Err == nil ||
		!strings.Contains(terminal.Err.Error(), "unsafe prepared authority") {
		t.Fatalf("terminal = %#v", terminal)
	}
	if model.CallCount() != 0 {
		t.Fatalf("model calls = %d, want zero", model.CallCount())
	}
}

func TestP311bResumePreflightsAndAdoptsMarkedAuthority(t *testing.T) {
	dir := p311bPrivateDir(t)
	source := NewQueryEngine(QueryEngineConfig{
		SessionID:     "source",
		ThreadID:      "source",
		TranscriptDir: dir,
		CWD:           dir,
	})
	if _, err := source.GetTaskManager().CreateWithError(
		"persisted",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatalf("create authoritative Task: %v", err)
	}
	p311bWriteTranscript(t, source.GetTranscript(), "source")
	source.Close()

	current := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		ThreadID:      "current",
		TranscriptDir: dir,
		CWD:           dir,
	})
	defer current.Close()
	manager := current.GetTaskManager()
	resumed, err := current.ResumeSession(context.Background(), "source")
	if err != nil {
		t.Fatalf("resume marked Session: %v", err)
	}
	if resumed.SessionID != "source" || current.SessionID() != "source" {
		t.Fatalf(
			"resumed identity = %+v engine=%q",
			resumed,
			current.SessionID(),
		)
	}
	if current.GetTaskManager() != manager {
		t.Fatal("resume replaced the TaskManager compatibility facade pointer")
	}
	task, found := manager.Get("1")
	if !found || task.Subject != "persisted" {
		t.Fatalf("resumed authoritative Task = %+v found=%v", task, found)
	}
}

func TestP311bResumeRejectsCorruptMarkerBeforeActivation(t *testing.T) {
	dir := p311bPrivateDir(t)
	source := NewQueryEngine(QueryEngineConfig{
		SessionID:     "source",
		ThreadID:      "source",
		TranscriptDir: dir,
		CWD:           dir,
	})
	if _, err := source.GetTaskManager().CreateWithError(
		"persisted",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	p311bWriteTranscript(t, source.GetTranscript(), "source")
	source.Close()
	if err := os.WriteFile(
		filepath.Join(dir, "source"+workboard.AuthorityMarkerSuffix),
		[]byte(`{"version":99}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	current := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		ThreadID:      "current",
		TranscriptDir: dir,
		CWD:           dir,
	})
	defer current.Close()
	manager := current.GetTaskManager()
	if _, err := current.ResumeSession(
		context.Background(),
		"source",
	); err == nil {
		t.Fatal("expected corrupt marker resume rejection")
	}
	if current.SessionID() != "current" ||
		current.GetTaskManager() != manager {
		t.Fatalf(
			"failed resume activated target: session=%q manager_changed=%v",
			current.SessionID(),
			current.GetTaskManager() != manager,
		)
	}
}

func TestP311bAuthoritativeForkCommitsChildBoardBeforeTranscript(t *testing.T) {
	dir := p311bPrivateDir(t)
	source := NewQueryEngine(QueryEngineConfig{
		SessionID:     "source",
		ThreadID:      "source",
		TranscriptDir: dir,
		CWD:           dir,
	})
	defer source.Close()
	if _, err := source.GetTaskManager().CreateWithError(
		"cloned",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	p311bWriteTranscript(t, source.GetTranscript(), "source")
	sourceState := p311bInspect(t, dir, "source")
	created, err := source.SessionService().CreateFork(
		context.Background(),
		SessionForkRequest{
			Source: &session.SessionInfo{
				SessionID:      "source",
				ThreadID:       "source",
				CWD:            dir,
				TranscriptDir:  dir,
				TranscriptPath: source.GetTranscript().Path(),
			},
			OperationID: "authoritative-fork",
		},
	)
	if err != nil {
		t.Fatalf("create authoritative fork: %v", err)
	}
	defer func() {
		if err := source.SessionService().DiscardFork(created); err != nil {
			t.Fatalf("discard fork: %v", err)
		}
	}()
	childState := p311bInspect(t, dir, created.Info.SessionID)
	if childState.Record.BoardID == sourceState.Record.BoardID {
		t.Fatal("authoritative fork reused source BoardID")
	}
	if len(childState.Record.Compatibility.Tasks) != 1 ||
		childState.Record.Compatibility.Tasks[0].Subject != "cloned" {
		t.Fatalf(
			"authoritative fork Task projection = %+v",
			childState.Record.Compatibility.Tasks,
		)
	}
	childScope, exists := p311bTodoScope(
		childState.Record.Compatibility.TodoScopes,
		created.Info.SessionID,
		"",
	)
	if !exists || len(childScope.CurrentItemIDs) != 0 {
		t.Fatalf("child root Todo scope = %+v exists=%v", childScope, exists)
	}
	if _, err := os.Lstat(created.Branch.TranscriptPath); err != nil {
		t.Fatalf("child transcript not published: %v", err)
	}
	childAdapter, err := workboard.NewLogicalWorkAdapter(
		workboard.AdapterConfig{
			SessionID:   created.Info.SessionID,
			Dir:         dir,
			LeaderScope: tools.TodoScope{SessionID: created.Info.SessionID},
			NewBoardID:  func() string { return "recovered-child-board" },
		},
		tools.TaskManagerSnapshot{NextID: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := childAdapter.Create(
		"child-only",
		"discard on recovery",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	beforeRecovery := childAdapter.Snapshot()
	if _, err := childAdapter.Recover(workboard.RecoveryRequest{
		SessionID:           created.Info.SessionID,
		BoardID:             beforeRecovery.BoardID,
		Revision:            beforeRecovery.Revision,
		AcknowledgeDataLoss: true,
	}); err != nil {
		t.Fatal(err)
	}
	recoveredTasks := childAdapter.LegacyTaskSnapshot().Tasks
	if len(recoveredTasks) != 1 || recoveredTasks[0].Subject != "cloned" {
		t.Fatalf("child fork recovery baseline = %+v", recoveredTasks)
	}
}

func TestP311bLegacyForkCarriesTaskSnapshotWithoutArtifacts(t *testing.T) {
	dir := p311bPrivateDir(t)
	manager := tools.NewTaskManager()
	manager.Create("legacy", "description", "", nil)
	source := NewQueryEngine(QueryEngineConfig{
		SessionID:     "source",
		ThreadID:      "source",
		TranscriptDir: dir,
		CWD:           dir,
		TaskManager:   manager,
	})
	defer source.Close()
	p311bWriteTranscript(t, source.GetTranscript(), "source")
	resumed, created, err := source.SessionService().Fork(
		context.Background(),
		SessionForkRequest{
			Source: &session.SessionInfo{
				SessionID:      "source",
				ThreadID:       "source",
				CWD:            dir,
				TranscriptDir:  dir,
				TranscriptPath: source.GetTranscript().Path(),
			},
			OperationID: "legacy-fork",
		},
	)
	if err != nil {
		t.Fatalf("fork legacy Session: %v", err)
	}
	if resumed.SessionID != created.Info.SessionID {
		t.Fatalf("legacy fork activation = %+v created=%+v", resumed, created)
	}
	if source.GetTaskManager() != manager {
		t.Fatal("legacy fork replaced TaskManager facade")
	}
	task, found := manager.Get("1")
	if !found || task.Subject != "legacy" {
		t.Fatalf("legacy fork Task snapshot = %+v found=%v", task, found)
	}
	for _, suffix := range p311bArtifactSuffixes() {
		if _, statErr := os.Lstat(
			filepath.Join(dir, created.Info.SessionID+suffix),
		); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf(
				"legacy fork created WorkBoard artifact %s: %v",
				suffix,
				statErr,
			)
		}
	}
}

func TestP311bCompactionDoesNotRewriteAuthority(t *testing.T) {
	dir := p311bPrivateDir(t)
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session",
		ThreadID:      "session",
		TranscriptDir: dir,
		CWD:           dir,
	})
	defer engine.Close()
	if _, err := engine.GetTaskManager().CreateWithError(
		"stable",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	authorityPath := filepath.Join(
		dir,
		"session"+workboard.AuthorityRecordSuffix,
	)
	before, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	messages := []*schema.Message{
		{Role: schema.User, Content: "before compact"},
		{Role: schema.Assistant, Content: "summary"},
	}
	if err := engine.recordTranscriptBoundary(
		transcript.LifecycleCompact,
		messages,
	); err != nil {
		t.Fatalf("record compact boundary: %v", err)
	}
	after, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("compaction rewrote WorkBoard authority")
	}
}

func TestP311bActiveDeleteCannotRecreateTranscriptOnClose(t *testing.T) {
	dir := p311bPrivateDir(t)
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session",
		ThreadID:      "session",
		TranscriptDir: dir,
		CWD:           dir,
	})
	if _, err := engine.GetTaskManager().CreateWithError(
		"persisted",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	p311bWriteTranscript(t, engine.GetTranscript(), "session")
	result, err := engine.SessionService().Delete(
		context.Background(),
		"session",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TranscriptRemoved ||
		!result.WorkBoardAuthorityRemoved ||
		!result.CleanupCompleted {
		t.Fatalf("active delete result = %+v", result)
	}
	engine.Close()
	if _, err := os.Lstat(filepath.Join(dir, "session.jsonl")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("Close recreated deleted transcript: %v", err)
	}
	for _, suffix := range p311bArtifactSuffixes() {
		if _, err := os.Lstat(
			filepath.Join(dir, "session"+suffix),
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("active delete left artifact %s: %v", suffix, err)
		}
	}
}

func TestP311bRestoreStagingDoesNotAdvertiseAuthorityBeforeCommit(t *testing.T) {
	dir := p311bPrivateDir(t)
	source := NewQueryEngine(QueryEngineConfig{
		SessionID:     "source",
		ThreadID:      "source",
		TranscriptDir: dir,
		CWD:           dir,
	})
	if _, err := source.GetTaskManager().CreateWithError(
		"restored",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	p311bWriteTranscript(t, source.GetTranscript(), "source")
	source.Close()
	before, err := os.ReadFile(filepath.Join(
		dir,
		"source"+workboard.AuthorityRecordSuffix,
	))
	if err != nil {
		t.Fatal(err)
	}

	staging := NewRestoreStagingQueryEngine(QueryEngineConfig{
		SessionID:     "host",
		ThreadID:      "host",
		TranscriptDir: dir,
		CWD:           dir,
	})
	if _, err := staging.ResumeSession(context.Background(), "source"); err != nil {
		t.Fatal(err)
	}
	if staging.GetTaskManager().AuthorityBound() ||
		staging.logicalWorkAdapter != nil {
		t.Fatal("staged restore advertised a live WorkBoard before commit")
	}
	if err := staging.CommitRestoreStaging(); err != nil {
		t.Fatal(err)
	}
	if !staging.GetTaskManager().AuthorityBound() ||
		staging.logicalWorkAdapter == nil {
		t.Fatal("committed restore did not install WorkBoard authority")
	}
	task, found := staging.GetTaskManager().Get("1")
	if !found || task.Subject != "restored" {
		t.Fatalf("committed restored Task = %+v found=%v", task, found)
	}
	after, err := os.ReadFile(filepath.Join(
		dir,
		"source"+workboard.AuthorityRecordSuffix,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restore staging rewrote WorkBoard during activation")
	}
	staging.Close()
}

func TestP311bExportExcludesAuthorityAndRecoveryData(t *testing.T) {
	dir := p311bPrivateDir(t)
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session",
		ThreadID:      "session",
		TranscriptDir: dir,
		CWD:           dir,
	})
	defer engine.Close()
	if _, err := engine.GetTaskManager().CreateWithError(
		"private board item",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	p311bWriteTranscript(t, engine.GetTranscript(), "session")
	state := p311bInspect(t, dir, "session")
	output := filepath.Join(dir, "export.md")
	if _, err := engine.SessionService().Export(
		context.Background(),
		"session",
		output,
	); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		state.Record.BoardID,
		workboard.AuthorityRecordSuffix,
		workboard.AuthorityMarkerSuffix,
		workboard.LegacyBackupSuffix,
		"private board item",
	} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("export leaked WorkBoard data %q: %s", secret, data)
		}
	}
}

func p311bPrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func p311bArtifactSuffixes() []string {
	return []string{
		workboard.AuthorityRecordSuffix,
		workboard.AuthorityMarkerSuffix,
		workboard.LegacyBackupSuffix,
	}
}

func p311bInspect(
	t *testing.T,
	dir string,
	sessionID string,
) workboard.AuthorityState {
	t.Helper()
	store, err := workboard.NewStore(workboard.StoreConfig{
		Dir:       dir,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != workboard.AuthorityModeWorkBoard {
		t.Fatalf("authority mode = %q", state.Mode)
	}
	return state
}

func p311bWriteTranscript(
	t *testing.T,
	recorder *transcript.Recorder,
	content string,
) {
	t.Helper()
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: content,
		ThreadID:  content,
	})
	if err := recorder.Record(
		[]*schema.Message{{Role: schema.User, Content: content}},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
}

func p311bBoundLogicalWorkFixture(
	t *testing.T,
	sessionID string,
) (*tools.TaskManager, *workboard.LogicalWorkAdapter) {
	t.Helper()
	manager := tools.NewTaskManager()
	adapter, err := workboard.BindLogicalWorkAdapter(
		workboard.AdapterConfig{
			SessionID: sessionID,
			Dir:       p311bPrivateDir(t),
			LeaderScope: tools.TodoScope{
				SessionID: sessionID,
			},
		},
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}
	return manager, adapter
}

func p311bTodoScope(
	scopes []workboard.TodoScopeCompatibility,
	sessionID string,
	agentID string,
) (workboard.TodoScopeCompatibility, bool) {
	for _, scope := range scopes {
		if scope.SessionID == sessionID && scope.AgentID == agentID {
			return scope, true
		}
	}
	return workboard.TodoScopeCompatibility{}, false
}
