package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

type p311aCompatibilityRun struct {
	results []string
	tasks   []*tools.TaskRecord
	runtime RuntimeSnapshot
	record  workboard.Record
}

func runP311aCompatibilityScenario(
	t *testing.T,
	enabled bool,
) p311aCompatibilityRun {
	t.Helper()
	dir := p311aPrivateTempDir(t)
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	runtimeState := NewRuntimeStateStore()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:       "p31-shadow",
		ThreadID:        "p31-shadow",
		CWD:             t.TempDir(),
		TranscriptDir:   dir,
		ToolRegistry:    registry,
		TaskManager:     tools.NewTaskManager(),
		RuntimeState:    runtimeState,
		WorkBoardShadow: enabled,
	})
	t.Cleanup(engine.Close)

	shadowPath, err := workboard.SidecarPath(dir, engine.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.toolExecutor(
		context.Background(),
		"TaskCreate",
		`{"description":"missing subject"}`,
	); err == nil {
		t.Fatal("invalid TaskCreate unexpectedly succeeded")
	}
	if _, err := os.Lstat(shadowPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid mutation created shadow: %v", err)
	}

	steps := []struct {
		tool  string
		input string
	}{
		{
			tool:  "TaskCreate",
			input: `{"subject":"Alpha","description":"First","metadata":{"k":"v"}}`,
		},
		{
			tool:  "TaskCreate",
			input: `{"subject":"Beta","description":"Second"}`,
		},
		{
			tool:  "TaskUpdate",
			input: `{"task_id":"1","status":"running","add_blocks":["2"],"output":"done"}`,
		},
		{
			tool: "TodoWrite",
			input: `{"todos":[` +
				`{"content":"One","status":"pending","activeForm":"Doing one"},` +
				`{"content":"Two","status":"in_progress","activeForm":"Doing two"}` +
				`]}`,
		},
		{
			tool:  "TaskStop",
			input: `{"task_id":"1"}`,
		},
	}
	results := make([]string, 0, len(steps))
	for _, step := range steps {
		result, err := engine.toolExecutor(
			context.Background(),
			step.tool,
			step.input,
		)
		if err != nil {
			t.Fatalf("%s: %v", step.tool, err)
		}
		results = append(results, result)
	}

	run := p311aCompatibilityRun{
		results: results,
		tasks:   engine.taskManager.List(),
		runtime: runtimeState.Snapshot(engine.ThreadID()),
	}
	data, err := os.ReadFile(shadowPath)
	if enabled {
		if err != nil {
			t.Fatalf("read enabled shadow: %v", err)
		}
		run.record, err = workboard.Decode(data, engine.SessionID())
		if err != nil {
			t.Fatalf("decode enabled shadow: %v", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled shadow path error: %v", err)
	}
	return run
}

func TestP311aShadowOnOffPreservesLegacyAndRuntimeBehavior(t *testing.T) {
	disabled := runP311aCompatibilityScenario(t, false)
	enabled := runP311aCompatibilityScenario(t, true)

	if !reflect.DeepEqual(disabled.results, enabled.results) {
		t.Fatalf(
			"tool results differ with shadow enabled:\ndisabled=%#v\nenabled=%#v",
			disabled.results,
			enabled.results,
		)
	}
	if len(disabled.tasks) != len(enabled.tasks) {
		t.Fatalf(
			"task counts differ: disabled=%d enabled=%d",
			len(disabled.tasks),
			len(enabled.tasks),
		)
	}
	for index := range disabled.tasks {
		left := disabled.tasks[index]
		right := enabled.tasks[index]
		left.CreatedAt, left.UpdatedAt = right.CreatedAt, right.UpdatedAt
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("task %d differs:\ndisabled=%#v\nenabled=%#v", index, left, right)
		}
	}
	if !reflect.DeepEqual(disabled.runtime, enabled.runtime) {
		t.Fatalf(
			"runtime projection differs:\ndisabled=%#v\nenabled=%#v",
			disabled.runtime,
			enabled.runtime,
		)
	}
	if enabled.record.Version != workboard.CurrentVersion ||
		enabled.record.SessionID != "p31-shadow" ||
		enabled.record.Candidate == nil {
		t.Fatalf("enabled shadow record = %#v", enabled.record)
	}
}

func TestP311aShadowOwnerIsRootScopedAndSharedWithChildren(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	root := NewQueryEngine(QueryEngineConfig{
		SessionID:       "p31-root",
		ThreadID:        "p31-root",
		CWD:             t.TempDir(),
		TranscriptDir:   t.TempDir(),
		ToolRegistry:    registry,
		ChatModel:       &taskLifecycleModel{},
		WorkBoardShadow: true,
	})
	t.Cleanup(root.Close)

	if root.workBoardShadow == nil {
		t.Fatal("root shadow was not constructed")
	}
	if root.subagentExecutor == nil {
		t.Fatal("root sub-agent executor was not constructed")
	}
	if root.subagentExecutor.workBoardShadow != root.workBoardShadow {
		t.Fatal("sub-agent executor did not borrow the root shadow")
	}

	child := newForegroundChildQueryEngine(QueryEngineConfig{
		SessionID:       "p31-child",
		ThreadID:        "p31-child",
		RootSessionID:   root.SessionID(),
		AgentID:         "child",
		CWD:             t.TempDir(),
		TranscriptDir:   t.TempDir(),
		ToolRegistry:    registry,
		WorkBoardShadow: true,
		workBoardShadow: root.workBoardShadow,
	})
	t.Cleanup(child.Close)
	if child.workBoardShadow != root.workBoardShadow {
		t.Fatal("child engine did not share the root shadow")
	}

	independent := NewQueryEngine(QueryEngineConfig{
		SessionID:       "p31-independent",
		ThreadID:        "p31-independent",
		CWD:             t.TempDir(),
		TranscriptDir:   t.TempDir(),
		ToolRegistry:    registry,
		WorkBoardShadow: true,
	})
	t.Cleanup(independent.Close)
	if independent.workBoardShadow == nil ||
		independent.workBoardShadow == root.workBoardShadow {
		t.Fatal("independent root reused another lineage shadow")
	}
}

func TestP311aAdministrationAndRestoreStagingNeverCreateShadow(t *testing.T) {
	t.Run("administration", func(t *testing.T) {
		engine := newQueryEngineWithOptions(QueryEngineConfig{
			SessionID:       "p31-administration",
			ThreadID:        "p31-administration",
			CWD:             t.TempDir(),
			TranscriptDir:   t.TempDir(),
			WorkBoardShadow: true,
		}, queryEngineConstructionOptions{administration: true})
		t.Cleanup(engine.Close)
		if engine.workBoardShadow != nil {
			t.Fatal("administration engine created WorkBoard shadow")
		}
	})

	t.Run("restore staging", func(t *testing.T) {
		engine := NewRestoreStagingQueryEngine(QueryEngineConfig{
			SessionID:       "p31-restore-staging",
			ThreadID:        "p31-restore-staging",
			CWD:             t.TempDir(),
			TranscriptDir:   t.TempDir(),
			WorkBoardShadow: true,
		})
		t.Cleanup(engine.Close)
		if engine.workBoardShadow != nil {
			t.Fatal("restore staging engine created WorkBoard shadow")
		}
	})
}

func TestP311aPreShadowSessionResumeIgnoresRemovableSidecar(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	recorder := writeEngineSelectedSession(t, dir, "selected", "selected prompt")
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shadow := workboard.NewShadow(workboard.Config{
		SessionID: "selected",
		Dir:       dir,
		BoardID:   "sidecar-only",
	})
	manager := tools.NewTaskManager()
	manager.Create("Observed", "shadow only", "", nil)
	shadow.ObserveTasks(manager.List())
	shadowPath, err := workboard.SidecarPath(dir, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shadowPath); err != nil {
		t.Fatal(err)
	}

	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		CWD:           cwd,
		TranscriptDir: filepath.Join(cwd, "current"),
	})
	t.Cleanup(engine.Close)
	resumed, err := engine.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "selected",
		CWD:            cwd,
		TranscriptDir:  dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "selected" ||
		engine.SessionID() != "selected" ||
		engine.workBoardShadow != nil {
		t.Fatalf(
			"baseline resume result=%#v session=%q shadow=%p",
			resumed,
			engine.SessionID(),
			engine.workBoardShadow,
		)
	}
	messages := engine.GetMessages()
	if len(messages) != 2 || messages[0].Content != "selected prompt" {
		t.Fatalf("baseline resumed messages = %#v", messages)
	}
	if _, err := os.Stat(shadowPath); err != nil {
		t.Fatalf("baseline resume changed removable sidecar: %v", err)
	}
}

func TestP311aSessionActivationDisablesOldLineageShadow(t *testing.T) {
	t.Run("resume", func(t *testing.T) {
		assertP311aSessionActivationDisablesShadow(
			t,
			func(
				t *testing.T,
				engine *QueryEngine,
				cwd string,
				selectedDir string,
				recorderPath string,
			) (string, string) {
				t.Helper()
				resumed, err := engine.ResumeSessionInfo(
					t.Context(),
					session.SessionInfo{
						SessionID:      "selected",
						CWD:            cwd,
						TranscriptDir:  selectedDir,
						TranscriptPath: recorderPath,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				return resumed.SessionID, selectedDir
			},
		)
	})

	t.Run("fork", func(t *testing.T) {
		assertP311aSessionActivationDisablesShadow(
			t,
			func(
				t *testing.T,
				engine *QueryEngine,
				cwd string,
				selectedDir string,
				recorderPath string,
			) (string, string) {
				t.Helper()
				resumed, branch, err := engine.ForkSessionInfo(
					t.Context(),
					session.SessionInfo{
						SessionID:      "selected",
						CWD:            cwd,
						TranscriptDir:  selectedDir,
						TranscriptPath: recorderPath,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				if branch == nil || resumed.SessionID != branch.NewSessionID {
					t.Fatalf("fork activation = resumed %#v branch %#v", resumed, branch)
				}
				return resumed.SessionID, selectedDir
			},
		)
	})
}

func assertP311aSessionActivationDisablesShadow(
	t *testing.T,
	activate func(
		*testing.T,
		*QueryEngine,
		string,
		string,
		string,
	) (string, string),
) {
	t.Helper()
	cwd := t.TempDir()
	currentDir := p311aPrivateTempDir(t)
	selectedDir := filepath.Join(cwd, "selected")
	selected := writeEngineSelectedSession(
		t,
		selectedDir,
		"selected",
		"selected prompt",
	)
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:       "current",
		CWD:             cwd,
		TranscriptDir:   currentDir,
		ToolRegistry:    registry,
		WorkBoardShadow: true,
	})
	t.Cleanup(engine.Close)
	if _, err := engine.toolExecutor(
		context.Background(),
		"TaskCreate",
		`{"subject":"Before","description":"current session"}`,
	); err != nil {
		t.Fatal(err)
	}
	oldPath, err := workboard.SidecarPath(currentDir, "current")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}

	targetSessionID, targetDir := activate(
		t,
		engine,
		cwd,
		selectedDir,
		selected.Path(),
	)
	if engine.workBoardShadow != nil || engine.config.WorkBoardShadow {
		t.Fatalf(
			"activated session retained shadow=%p flag=%v",
			engine.workBoardShadow,
			engine.config.WorkBoardShadow,
		)
	}
	if engine.subagentExecutor != nil &&
		engine.subagentExecutor.workBoardShadow != nil {
		t.Fatal("activated session retained child shadow binding")
	}
	if _, err := engine.toolExecutor(
		context.Background(),
		"TaskCreate",
		`{"subject":"After","description":"activated session"}`,
	); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("activated session wrote to the old Session sidecar")
	}
	targetPath, err := workboard.SidecarPath(targetDir, targetSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session activation created a restored shadow: %v", err)
	}
}

func p311aPrivateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
