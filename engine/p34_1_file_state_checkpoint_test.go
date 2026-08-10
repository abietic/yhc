package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type p341ToolModel struct {
	calls        atomic.Int32
	toolCalls    []schema.ToolCall
	afterToolErr error
}

func (m *p341ToolModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *p341ToolModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if m.calls.Add(1) == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:      schema.Assistant,
			ToolCalls: append([]schema.ToolCall(nil), m.toolCalls...),
		}}), nil
	}
	if m.afterToolErr != nil {
		return nil, m.afterToolErr
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func p341ToolCall(t *testing.T, id, name, path string) schema.ToolCall {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{"file_path": path})
	if err != nil {
		t.Fatal(err)
	}
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: string(arguments),
		},
	}
}

func p341ToolRegistry(
	names []string,
	concurrencySafe bool,
	execute func(context.Context, string) (string, error),
) *tools.Registry {
	registry := tools.NewRegistry()
	for _, name := range names {
		toolName := name
		registry.Register(tools.ToolImpl{
			Info: &schema.ToolInfo{Name: toolName},
			ExecuteCtx: func(ctx context.Context, input string) (string, error) {
				return execute(ctx, input)
			},
			IsConcurrencySafe: func(map[string]any) bool {
				return concurrencySafe
			},
			IsReadOnly: toolName == "Read",
		})
	}
	return registry
}

func p341NewEngine(
	t *testing.T,
	root string,
	model model.BaseChatModel,
	registry *tools.Registry,
	maxTurns int,
	hookExecutor *hooks.Executor,
) *QueryEngine {
	t.Helper()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p34-1-file-state",
		TranscriptDir: filepath.Join(root, "transcripts"),
		CWD:           root,
		MaxTurns:      maxTurns,
		ChatModel:     model,
		ToolRegistry:  registry,
		Tools:         registry.List(),
		Model:         "test-model",
		HookExecutor:  hookExecutor,
	})
	t.Cleanup(engine.Close)
	return engine
}

func p341RunTurn(
	t *testing.T,
	engine *QueryEngine,
) (*Terminal, []*schema.Message) {
	t.Helper()
	events, admission := engine.SubmitMessage(
		t.Context(),
		"exercise file state",
	)
	if admission.Reason != "" &&
		admission.Reason != TerminalCompleted {
		t.Fatalf("submit admission = %#v", admission)
	}
	var terminal *Terminal
	var toolResults []*schema.Message
	for event := range events {
		switch event.Type {
		case EventToolResult:
			if event.ToolResultMessage != nil {
				toolResults = append(toolResults, event.ToolResultMessage)
			}
		case EventTerminal:
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil {
		t.Fatal("turn emitted no terminal event")
	}
	return terminal, toolResults
}

func p341CheckpointCount(loaded *transcript.LoadResult) int {
	count := 0
	if loaded == nil {
		return count
	}
	for _, boundary := range loaded.LifecycleBoundaries {
		if boundary.Kind == transcript.LifecycleCheckpoint {
			count++
		}
	}
	return count
}

func p341FileState(
	cache *FileStateCache,
	path string,
) (read, edit, write bool) {
	if cache == nil {
		return false, false, false
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.ReadFiles[path], cache.EditFiles[path], cache.WriteFiles[path]
}

func TestP341SuccessfulFileSnapshotsKeepAppendOnlyCheckpointing(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		wantRead  bool
		wantEdit  bool
		wantWrite bool
	}{
		{name: "read", tool: "Read", wantRead: true},
		{name: "edit", tool: "Edit", wantRead: true, wantEdit: true},
		{name: "write", tool: "Write", wantRead: true, wantWrite: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, test.name+".go")
			var executions atomic.Int32
			registry := p341ToolRegistry(
				[]string{test.tool},
				false,
				func(context.Context, string) (string, error) {
					executions.Add(1)
					return "stable-tool-result", nil
				},
			)
			engine := p341NewEngine(
				t,
				root,
				&p341ToolModel{toolCalls: []schema.ToolCall{
					p341ToolCall(t, "call-"+test.name, test.tool, path),
				}},
				registry,
				4,
				nil,
			)

			terminal, toolResults := p341RunTurn(t, engine)
			if terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal = %#v", terminal)
			}
			if executions.Load() != 1 ||
				len(toolResults) != 1 ||
				toolResults[0].Content != "stable-tool-result" ||
				toolResults[0].Extra != nil {
				t.Fatalf(
					"tool executions=%d results=%#v",
					executions.Load(),
					toolResults,
				)
			}
			loaded, err := engine.GetTranscript().LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			if checkpoints := p341CheckpointCount(loaded); checkpoints != 0 {
				t.Fatalf("full checkpoints = %d, want 0", checkpoints)
			}

			restarted := NewQueryEngine(QueryEngineConfig{
				SessionID:     engine.SessionID(),
				TranscriptDir: engine.GetTranscriptDir(),
				CWD:           root,
			})
			t.Cleanup(restarted.Close)
			read, edit, write := p341FileState(restarted.fileStateCache, path)
			if read != test.wantRead ||
				edit != test.wantEdit ||
				write != test.wantWrite {
				t.Fatalf(
					"restored state = (read=%t edit=%t write=%t)",
					read,
					edit,
					write,
				)
			}
		})
	}
}

func TestP341SnapshotFailureForcesOneFullCheckpointWithoutRepeatingTool(
	t *testing.T,
) {
	root := t.TempDir()
	path := filepath.Join(root, "repair.go")
	var executions atomic.Int32
	registry := p341ToolRegistry(
		[]string{"Read"},
		false,
		func(context.Context, string) (string, error) {
			executions.Add(1)
			return "stable-tool-result", nil
		},
	)
	model := &p341ToolModel{toolCalls: []schema.ToolCall{
		p341ToolCall(t, "call-repair", "Read", path),
	}}
	engine := p341NewEngine(t, root, model, registry, 4, nil)
	var snapshotAttempts atomic.Int32
	engine.fileStateSnapshotWriter = func(
		map[string]transcript.FileState,
	) error {
		snapshotAttempts.Add(1)
		return &transcript.DurabilityUncertainError{
			Operation: "encode file-history snapshot",
			Err:       errors.New("injected incremental snapshot failure"),
		}
	}

	terminal, toolResults := p341RunTurn(t, engine)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if executions.Load() != 1 ||
		snapshotAttempts.Load() != 1 ||
		model.calls.Load() != 2 {
		t.Fatalf(
			"executions=%d snapshots=%d model_calls=%d",
			executions.Load(),
			snapshotAttempts.Load(),
			model.calls.Load(),
		)
	}
	if len(toolResults) != 1 ||
		toolResults[0].Content != "stable-tool-result" ||
		toolResults[0].Extra != nil {
		t.Fatalf("tool result changed after snapshot failure: %#v", toolResults)
	}
	loaded, err := engine.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoints := p341CheckpointCount(loaded); checkpoints != 1 {
		t.Fatalf(
			"full checkpoints = %d, want exactly 1: %#v",
			checkpoints,
			loaded.LifecycleBoundaries,
		)
	}
	if engine.transcriptCheckpointRequired {
		t.Fatal("successful full repair retained checkpoint requirement")
	}

	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID:     engine.SessionID(),
		TranscriptDir: engine.GetTranscriptDir(),
		CWD:           root,
	})
	t.Cleanup(restarted.Close)
	read, edit, write := p341FileState(restarted.fileStateCache, path)
	if !read || edit || write {
		t.Fatalf(
			"restored state = (read=%t edit=%t write=%t)",
			read,
			edit,
			write,
		)
	}

	host := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p34-1-resume-host",
		TranscriptDir: engine.GetTranscriptDir(),
		CWD:           root,
	})
	t.Cleanup(host.Close)
	if _, err := host.ResumeSession(t.Context(), engine.SessionID()); err != nil {
		t.Fatal(err)
	}
	read, edit, write = p341FileState(host.fileStateCache, path)
	if !read || edit || write {
		t.Fatalf(
			"explicit resume state = (read=%t edit=%t write=%t)",
			read,
			edit,
			write,
		)
	}
}

func TestP341ConcurrentSnapshotOutcomesCoalesceIntoOneRepair(t *testing.T) {
	tests := []struct {
		name            string
		failingAttempts int32
	}{
		{name: "both snapshots fail", failingAttempts: 2},
		{name: "first snapshot fails and second succeeds", failingAttempts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			firstPath := filepath.Join(root, "first.go")
			secondPath := filepath.Join(root, "second.go")
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			var executions atomic.Int32
			registry := p341ToolRegistry(
				[]string{"Read"},
				true,
				func(ctx context.Context, _ string) (string, error) {
					executions.Add(1)
					started <- struct{}{}
					select {
					case <-release:
						return "stable-tool-result", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				},
			)
			engine := p341NewEngine(
				t,
				root,
				&p341ToolModel{toolCalls: []schema.ToolCall{
					p341ToolCall(t, "call-first", "Read", firstPath),
					p341ToolCall(t, "call-second", "Read", secondPath),
				}},
				registry,
				4,
				nil,
			)
			var snapshotAttempts atomic.Int32
			engine.fileStateSnapshotWriter = func(
				files map[string]transcript.FileState,
			) error {
				attempt := snapshotAttempts.Add(1)
				if attempt <= test.failingAttempts {
					return errors.New(
						"injected concurrent snapshot failure",
					)
				}
				return engine.GetTranscript().
					RecordFileHistorySnapshot(files)
			}

			events, admission := engine.SubmitMessage(
				t.Context(),
				"run concurrent reads",
			)
			if admission.Reason != "" &&
				admission.Reason != TerminalCompleted {
				t.Fatalf("submit admission = %#v", admission)
			}
			for range 2 {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					close(release)
					t.Fatal(
						"file tools did not enter the concurrency-safe batch",
					)
				}
			}
			close(release)
			var terminal *Terminal
			var toolResults int
			for event := range events {
				if event.Type == EventToolResult &&
					event.ToolResultMessage != nil {
					toolResults++
				}
				if event.Type == EventTerminal {
					terminal = event.TerminalInfo
				}
			}
			if terminal == nil || terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal = %#v", terminal)
			}
			if executions.Load() != 2 ||
				snapshotAttempts.Load() != 2 ||
				toolResults != 2 {
				t.Fatalf(
					"executions=%d snapshots=%d results=%d",
					executions.Load(),
					snapshotAttempts.Load(),
					toolResults,
				)
			}
			loaded, err := engine.GetTranscript().LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			if checkpoints := p341CheckpointCount(loaded); checkpoints != 1 {
				t.Fatalf(
					"full checkpoints = %d, want exactly 1",
					checkpoints,
				)
			}
			for _, path := range []string{firstPath, secondPath} {
				read, edit, write := p341FileState(
					engine.fileStateCache,
					path,
				)
				if !read || edit || write {
					t.Fatalf(
						"live state for %q = (read=%t edit=%t write=%t)",
						path,
						read,
						edit,
						write,
					)
				}
				state := loaded.FileSnapshots[len(loaded.FileSnapshots)-1][path]
				if !state.WasRead || state.WasEdit || state.WasWrite {
					t.Fatalf("durable state for %q = %#v", path, state)
				}
			}
		})
	}
}

func TestP341FailedFullRepairSurfacesPersistenceErrorAndRetainsRequirement(
	t *testing.T,
) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	path := filepath.Join(root, "unrepaired.go")
	var executions atomic.Int32
	registry := p341ToolRegistry(
		[]string{"Read"},
		false,
		func(context.Context, string) (string, error) {
			executions.Add(1)
			return "stable-tool-result", nil
		},
	)
	engine := p341NewEngine(
		t,
		root,
		&p341ToolModel{toolCalls: []schema.ToolCall{
			p341ToolCall(t, "call-unrepaired", "Read", path),
		}},
		registry,
		4,
		nil,
	)
	engine.fileStateSnapshotWriter = func(
		map[string]transcript.FileState,
	) error {
		if err := engine.GetTranscript().Close(); err != nil {
			return err
		}
		if err := os.RemoveAll(transcriptDir); err != nil {
			return err
		}
		if err := os.WriteFile(
			transcriptDir,
			[]byte("persistent blocker"),
			0o600,
		); err != nil {
			return err
		}
		return errors.New("injected incremental snapshot failure")
	}

	terminal, toolResults := p341RunTurn(t, engine)
	if terminal.Reason != TerminalPersistenceError || terminal.Err == nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	if executions.Load() != 1 ||
		len(toolResults) != 1 ||
		toolResults[0].Content != "stable-tool-result" ||
		toolResults[0].Extra != nil {
		t.Fatalf(
			"executions=%d results=%#v",
			executions.Load(),
			toolResults,
		)
	}
	if !engine.transcriptCheckpointRequired {
		t.Fatal("failed full repair did not retain checkpoint requirement")
	}
}

func TestP341SnapshotRepairRunsBeforeTerminalPaths(t *testing.T) {
	tests := []struct {
		name            string
		maxTurns        int
		afterToolErr    error
		configure       func(*hooks.Executor, **QueryEngine)
		wantReason      TerminalReason
		wantCheckpoints int
	}{
		{
			name:            "post tool hook stop",
			maxTurns:        4,
			wantReason:      TerminalHookStopped,
			wantCheckpoints: 1,
			configure: func(executor *hooks.Executor, _ **QueryEngine) {
				executor.RegisterPostTool(func(
					context.Context,
					string,
					string,
					map[string]any,
					string,
				) *hooks.PostToolHookResult {
					return &hooks.PostToolHookResult{
						PreventContinuation: true,
						StopReason:          "stop after successful tool",
					}
				})
			},
		},
		{
			name:            "interrupt",
			maxTurns:        4,
			wantReason:      TerminalAbortedTools,
			wantCheckpoints: 1,
			configure: func(executor *hooks.Executor, engine **QueryEngine) {
				executor.RegisterPostTool(func(
					context.Context,
					string,
					string,
					map[string]any,
					string,
				) *hooks.PostToolHookResult {
					(*engine).Interrupt()
					return nil
				})
			},
		},
		{
			name:            "max turns",
			maxTurns:        1,
			wantReason:      TerminalMaxTurns,
			wantCheckpoints: 2,
		},
		{
			name:            "model error",
			maxTurns:        4,
			afterToolErr:    errors.New("injected model failure"),
			wantReason:      TerminalModelError,
			wantCheckpoints: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "terminal.go")
			var executions atomic.Int32
			registry := p341ToolRegistry(
				[]string{"Read"},
				false,
				func(context.Context, string) (string, error) {
					executions.Add(1)
					return "stable-tool-result", nil
				},
			)
			hookExecutor := hooks.NewExecutor()
			var engine *QueryEngine
			if test.configure != nil {
				test.configure(hookExecutor, &engine)
			}
			engine = p341NewEngine(
				t,
				root,
				&p341ToolModel{
					toolCalls: []schema.ToolCall{
						p341ToolCall(
							t,
							"call-terminal",
							"Read",
							path,
						),
					},
					afterToolErr: test.afterToolErr,
				},
				registry,
				test.maxTurns,
				hookExecutor,
			)
			engine.fileStateSnapshotWriter = func(
				map[string]transcript.FileState,
			) error {
				return errors.New("injected incremental snapshot failure")
			}

			terminal, toolResults := p341RunTurn(t, engine)
			if terminal.Reason != test.wantReason {
				t.Fatalf("terminal = %#v, want %q", terminal, test.wantReason)
			}
			if executions.Load() != 1 ||
				len(toolResults) != 1 ||
				toolResults[0].Content != "stable-tool-result" {
				t.Fatalf(
					"executions=%d results=%#v",
					executions.Load(),
					toolResults,
				)
			}
			loaded, err := engine.GetTranscript().LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			if checkpoints := p341CheckpointCount(loaded); checkpoints != test.wantCheckpoints {
				t.Fatalf(
					"full checkpoints = %d, want %d",
					checkpoints,
					test.wantCheckpoints,
				)
			}
			if engine.transcriptCheckpointRequired {
				t.Fatal("terminal path retained a repaired checkpoint requirement")
			}
		})
	}
}
