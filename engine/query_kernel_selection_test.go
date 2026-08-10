package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

type p1310FailingProjectGraphKernel struct {
	calls *atomic.Int32
	err   error
}

func (p1310FailingProjectGraphKernel) kind() queryKernelKind {
	return queryKernelProjectGraph
}

func (kernel p1310FailingProjectGraphKernel) run(
	context.Context,
	queryKernelRequest,
) Terminal {
	kernel.calls.Add(1)
	return Terminal{Reason: TerminalModelError, Err: kernel.err}
}

func TestP1310NewSessionSelectsFullProjectGraphForEveryToolSurface(
	t *testing.T,
) {
	t.Parallel()

	registry := tools.NewRegistry()
	for _, implementation := range []tools.ToolImpl{
		{
			Info:       &schema.ToolInfo{Name: "ReadOnly"},
			IsReadOnly: true,
		},
		{Info: &schema.ToolInfo{Name: "LocalWrite"}},
		{Info: &schema.ToolInfo{Name: "mcp__server__remote"}},
		{Info: &schema.ToolInfo{Name: "mcp_tool"}},
	} {
		registry.Register(implementation)
	}
	visible := []*schema.ToolInfo{
		{Name: "ReadOnly"},
		{Name: "LocalWrite"},
		{Name: "mcp__server__remote"},
		{Name: "mcp_tool"},
	}

	selection := initialSessionQueryKernelSelection(nil)
	if selection.err != nil {
		t.Fatalf("selection error: %v", selection.err)
	}
	if selection.kernel == nil ||
		selection.kernel.kind() != queryKernelProjectGraph {
		t.Fatalf("kernel selection = %#v", selection)
	}
	if selection.version != queryKernelVersionProjectGraph ||
		selection.stage != queryKernelStageFull ||
		selection.incompatibility != "" {
		t.Fatalf("selection metadata = %#v", selection)
	}
	if incompatibility := projectGraphStageIncompatibility(
		selection.stage,
		visible,
		registry,
	); incompatibility != "" {
		t.Fatalf(
			"full production stage rejected complete tool surface: %s",
			incompatibility,
		)
	}
}

func TestP1310RetainedCompatibilityStagesEnforcePinnedSurfaceWithoutFallback(
	t *testing.T,
) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:       &schema.ToolInfo{Name: "ReadOnly"},
		IsReadOnly: true,
	})
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "LocalWrite"},
	})
	tests := []struct {
		name               string
		stage              string
		visible            []*schema.ToolInfo
		wantReason         string
		wantSelectionError bool
	}{
		{
			name:               "off",
			stage:              "off",
			wantReason:         "has no stage",
			wantSelectionError: true,
		},
		{
			name:               "invalid",
			stage:              "everything",
			wantReason:         "has invalid stage",
			wantSelectionError: true,
		},
		{
			name:       "no_tools_rejects_visible",
			stage:      "no_tools",
			visible:    []*schema.ToolInfo{{Name: "ReadOnly"}},
			wantReason: "model_visible_tools_present:ReadOnly",
		},
		{
			name:       "read_only_rejects_write",
			stage:      "read_only",
			visible:    []*schema.ToolInfo{{Name: "LocalWrite"}},
			wantReason: "tool_not_read_only:LocalWrite",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			selection := persistedSessionQueryKernelSelection(
				queryKernelVersionProjectGraph,
				test.stage,
				"",
			)
			if test.wantSelectionError {
				if selection.kernel != nil ||
					selection.err == nil ||
					!strings.Contains(
						selection.err.Error(),
						test.wantReason,
					) {
					t.Fatalf(
						"selection = %#v, want error containing %q",
						selection,
						test.wantReason,
					)
				}
				return
			}
			if selection.kernel == nil || selection.err != nil {
				t.Fatalf(
					"supported persisted stage was not restored: %#v",
					selection,
				)
			}
			incompatibility := projectGraphStageIncompatibility(
				selection.stage,
				test.visible,
				registry,
			)
			if !strings.Contains(incompatibility, test.wantReason) {
				t.Fatalf(
					"stage incompatibility = %q, want %q",
					incompatibility,
					test.wantReason,
				)
			}
		})
	}
}

func TestP1310InternalChildStagesRemainDurableOnly(t *testing.T) {
	t.Parallel()

	for _, want := range []queryKernelStage{
		queryKernelStageForegroundChild,
		queryKernelStageBackgroundChild,
	} {
		t.Run(string(want), func(t *testing.T) {
			t.Parallel()
			if _, err := parseQueryKernelStage(string(want)); err == nil {
				t.Fatalf("%s stage was admitted as a root-session stage", want)
			}
			stage, err := parsePersistedQueryKernelStage(string(want))
			if err != nil || stage != want {
				t.Fatalf(
					"persisted %s stage = %q, err=%v",
					want,
					stage,
					err,
				)
			}
		})
	}
}

func TestP1310NewSessionPinsAndPersistsFullProjectGraph(t *testing.T) {
	transcriptDir := t.TempDir()
	model := &canonicalScriptModel{}
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			transcriptDir,
			"new-graph",
			model,
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	if eng.queryKernelSelection.kernel == nil ||
		eng.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		eng.queryKernelSelection.stage != queryKernelStageFull {
		t.Fatalf("new-session selection = %#v", eng.queryKernelSelection)
	}
	for _, prompt := range []string{"first", "second"} {
		events, terminal := eng.SubmitMessage(context.Background(), prompt)
		if terminal.Err != nil {
			t.Fatalf("submit %q: %v", prompt, terminal.Err)
		}
		for range events {
		}
	}
	eng.Close()

	loaded := loadProjectGraphSession(t, transcriptDir, "new-graph")
	meta := session.ReadSessionMetadataFull(loaded)
	if meta == nil ||
		meta.QueryKernelVersion != queryKernelVersionProjectGraph ||
		meta.QueryKernelStage != string(queryKernelStageFull) ||
		meta.QueryKernelIncompatibility != "" {
		t.Fatalf("persisted kernel metadata = %#v", meta)
	}

	resumed := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			transcriptDir,
			"new-graph",
			&canonicalScriptModel{},
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	defer resumed.Close()
	if resumed.queryKernelSelection.kernel == nil ||
		resumed.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		resumed.queryKernelSelection.stage != queryKernelStageFull {
		t.Fatalf("resumed selection = %#v", resumed.queryKernelSelection)
	}
}

func TestP1310ExistingGraphSessionKeepsItsDurableStage(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	writeProjectGraphSession(
		t,
		transcriptDir,
		"stored-graph",
		&session.SessionMetadataFull{
			SessionID:          "stored-graph",
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageNoTools),
			CreatedAt:          time.Now().UTC(),
		},
	)
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			transcriptDir,
			"stored-graph",
			&canonicalScriptModel{},
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	defer eng.Close()
	if eng.queryKernelSelection.kernel == nil ||
		eng.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		eng.queryKernelSelection.stage != queryKernelStageNoTools {
		t.Fatalf("stored selection = %#v", eng.queryKernelSelection)
	}
}

func TestP1310HistoricalStageJSONResumesAndCheckpointsWithoutKeyMigration(
	t *testing.T,
) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "historical-stage-json"
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	if err := recorder.Record([]*schema.Message{
		{Role: schema.User, Content: "existing user"},
		{Role: schema.Assistant, Content: "existing assistant"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMetadata(
		"session_metadata_full",
		`{"session_id":"historical-stage-json","query_kernel_version":"project_graph/v1","query_kernel_canary_stage":"read_only"}`,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	newEngine := func() *QueryEngine {
		return NewQueryEngine(
			projectGraphEngineConfig(
				t,
				transcriptDir,
				sessionID,
				&canonicalScriptModel{},
				tools.NewRegistry(),
				&tools.ToolSelection{},
			),
		)
	}
	eng := newEngine()
	if eng.queryKernelSelection.err != nil ||
		eng.queryKernelSelection.kernel == nil ||
		eng.queryKernelSelection.stage != queryKernelStageReadOnly {
		t.Fatalf("historical selection = %#v", eng.queryKernelSelection)
	}
	if err := eng.persistSessionCheckpoint(""); err != nil {
		t.Fatalf("persist checkpoint: %v", err)
	}
	eng.Close()

	raw, err := os.ReadFile(filepath.Join(transcriptDir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `query_kernel_canary_stage`) ||
		strings.Contains(string(raw), `query_kernel_stage`) {
		t.Fatalf("durable stage key migrated: %s", raw)
	}
	reloaded := newEngine()
	defer reloaded.Close()
	if reloaded.queryKernelSelection.err != nil ||
		reloaded.queryKernelSelection.kernel == nil ||
		reloaded.queryKernelSelection.stage != queryKernelStageReadOnly {
		t.Fatalf("reloaded selection = %#v", reloaded.queryKernelSelection)
	}
}

func TestP1310RetiredSessionsFailWithoutExecutionOrTranscriptRewrite(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name string
		meta *session.SessionMetadataFull
	}{
		{
			name: "legacy",
			meta: &session.SessionMetadataFull{
				SessionID:                  "legacy",
				QueryKernelVersion:         queryKernelVersionLegacy,
				QueryKernelStage:           string(queryKernelStageReadOnly),
				QueryKernelIncompatibility: "tool_not_read_only:Write",
				CreatedAt:                  time.Now().UTC(),
			},
		},
		{name: "pre_graph"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transcriptDir := t.TempDir()
			writeProjectGraphSession(t, transcriptDir, test.name, test.meta)
			path := filepath.Join(transcriptDir, test.name+".jsonl")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			model := &canonicalScriptModel{}
			eng := NewQueryEngine(
				projectGraphEngineConfig(
					t,
					transcriptDir,
					test.name,
					model,
					tools.NewRegistry(),
					&tools.ToolSelection{},
				),
			)
			if eng.queryKernelSelection.kernel != nil ||
				eng.queryKernelSelection.err == nil ||
				!strings.Contains(
					eng.queryKernelSelection.err.Error(),
					"is retired; start a new ProjectGraph session",
				) {
				t.Fatalf("retired selection = %#v", eng.queryKernelSelection)
			}
			events, terminal := eng.SubmitMessage(
				context.Background(),
				"must not execute",
			)
			if terminal.Reason != TerminalModelError ||
				terminal.Err == nil ||
				!strings.Contains(terminal.Err.Error(), "is retired") {
				t.Fatalf("terminal = %#v", terminal)
			}
			for range events {
			}
			if model.callCount != 0 {
				t.Fatalf("model calls = %d, want zero", model.callCount)
			}
			eng.Close()
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("retired-session transcript was rewritten")
			}
		})
	}
}

func TestP1310InvalidKernelAdmissionPrecedesCommandsAndInputOverrides(
	t *testing.T,
) {
	t.Parallel()

	selections := []struct {
		name string
		meta *session.SessionMetadataFull
	}{
		{
			name: "legacy",
			meta: &session.SessionMetadataFull{
				QueryKernelVersion: queryKernelVersionLegacy,
				QueryKernelStage:   string(queryKernelStageReadOnly),
			},
		},
		{name: "unpinned"},
		{
			name: "off",
			meta: &session.SessionMetadataFull{
				QueryKernelVersion: queryKernelVersionProjectGraph,
				QueryKernelStage:   string(queryKernelStageUnset),
			},
		},
		{
			name: "invalid",
			meta: &session.SessionMetadataFull{
				QueryKernelVersion: queryKernelVersionProjectGraph,
				QueryKernelStage:   "invalid-stage",
			},
		},
		{
			name: "unknown",
			meta: &session.SessionMetadataFull{
				QueryKernelVersion: "project_graph/v999",
				QueryKernelStage:   string(queryKernelStageFull),
			},
		},
	}
	inputs := []struct {
		name    string
		prompt  string
		command bool
	}{
		{name: "clear", prompt: "/clear", command: true},
		{name: "model_override", prompt: "@model:override-model do not run"},
	}
	for _, selection := range selections {
		for _, input := range inputs {
			t.Run(selection.name+"/"+input.name, func(t *testing.T) {
				t.Parallel()

				transcriptDir := t.TempDir()
				sessionID := selection.name + "-" + input.name
				meta := selection.meta
				if meta != nil {
					copy := *meta
					copy.SessionID = sessionID
					copy.CreatedAt = time.Now().UTC()
					meta = &copy
				}
				writeProjectGraphSession(t, transcriptDir, sessionID, meta)
				path := filepath.Join(transcriptDir, sessionID+".jsonl")
				beforeTranscript, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				model := &canonicalScriptModel{}
				eng := NewQueryEngine(
					projectGraphEngineConfig(
						t,
						transcriptDir,
						sessionID,
						model,
						tools.NewRegistry(),
						&tools.ToolSelection{},
					),
				)
				if eng.queryKernelSelection.err == nil {
					t.Fatalf(
						"invalid selection was admitted: %#v",
						eng.queryKernelSelection,
					)
				}
				beforeMessages := eng.GetMessages()
				beforeModel := eng.GetModelName()

				events, terminal := eng.SubmitMessage(
					context.Background(),
					input.prompt,
				)
				if terminal.Reason != TerminalModelError ||
					terminal.Err == nil {
					t.Fatalf("terminal = %#v", terminal)
				}
				var commandResult *CommandResultEvent
				for event := range events {
					if event.Type == EventCommandResult {
						commandResult = event.CommandResult
					}
				}
				if input.command &&
					(commandResult == nil ||
						commandResult.Status != CommandResultFailed) {
					t.Fatalf("command result = %#v", commandResult)
				}
				if !input.command && commandResult != nil {
					t.Fatalf("unexpected command result = %#v", commandResult)
				}
				if model.callCount != 0 {
					t.Fatalf("model calls = %d, want zero", model.callCount)
				}
				afterMessages := eng.GetMessages()
				if len(afterMessages) != len(beforeMessages) {
					t.Fatalf(
						"messages changed: before=%d after=%d",
						len(beforeMessages),
						len(afterMessages),
					)
				}
				for index := range beforeMessages {
					if beforeMessages[index].Role != afterMessages[index].Role ||
						beforeMessages[index].Content != afterMessages[index].Content {
						t.Fatalf(
							"message %d changed: before=%#v after=%#v",
							index,
							beforeMessages[index],
							afterMessages[index],
						)
					}
				}
				if got := eng.GetModelName(); got != beforeModel {
					t.Fatalf(
						"model changed: before=%q after=%q",
						beforeModel,
						got,
					)
				}
				eng.Close()
				afterTranscript, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(beforeTranscript, afterTranscript) {
					t.Fatal("rejected session transcript was rewritten")
				}
			})
		}
	}
}

func TestP1310NewCommandEscapesRetiredSessionWithoutSourceRewrite(
	t *testing.T,
) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sourceSessionID = "retired-new-escape"
	writeProjectGraphSession(
		t,
		transcriptDir,
		sourceSessionID,
		&session.SessionMetadataFull{
			SessionID:          sourceSessionID,
			QueryKernelVersion: queryKernelVersionLegacy,
			QueryKernelStage:   string(queryKernelStageReadOnly),
			CreatedAt:          time.Now().UTC(),
		},
	)
	sourcePath := filepath.Join(transcriptDir, sourceSessionID+".jsonl")
	before, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			transcriptDir,
			sourceSessionID,
			&canonicalScriptModel{},
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	defer eng.Close()
	if eng.queryKernelSelection.err == nil {
		t.Fatalf("retired selection = %#v", eng.queryKernelSelection)
	}

	events, terminal := eng.SubmitMessage(context.Background(), "/new")
	if terminal.Reason != TerminalCompleted || terminal.Err != nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	var result *CommandResultEvent
	for event := range events {
		if event.Type == EventCommandResult {
			result = event.CommandResult
		}
	}
	if result == nil || result.Status != CommandResultSucceeded {
		t.Fatalf("command result = %#v", result)
	}
	if eng.SessionID() == sourceSessionID ||
		eng.queryKernelSelection.kernel == nil ||
		eng.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		eng.queryKernelSelection.stage != queryKernelStageFull {
		t.Fatalf(
			"new session=%q selection=%#v",
			eng.SessionID(),
			eng.queryKernelSelection,
		)
	}
	after, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source transcript was rewritten by /new escape")
	}
}

func TestP1310ResumeRestoresProjectGraphAtSessionBoundary(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	writeProjectGraphSession(
		t,
		transcriptDir,
		"resumed-graph",
		&session.SessionMetadataFull{
			SessionID:          "resumed-graph",
			ThreadID:           "resumed-graph",
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageNoTools),
			CreatedAt:          time.Now().UTC(),
			CWD:                t.TempDir(),
		},
	)
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			transcriptDir,
			"current-graph",
			&canonicalScriptModel{},
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	defer eng.Close()

	resumed, err := eng.ResumeSession(context.Background(), "resumed-graph")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.SessionID != "resumed-graph" ||
		eng.SessionID() != "resumed-graph" ||
		eng.queryKernelSelection.kernel == nil ||
		eng.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		eng.queryKernelSelection.stage != queryKernelStageNoTools {
		t.Fatalf(
			"resumed metadata=%#v selection=%#v",
			resumed,
			eng.queryKernelSelection,
		)
	}
}

func TestP1310UnsupportedPersistedKernelFailsWithoutTranscriptRewrite(
	t *testing.T,
) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "future-kernel"
	writeProjectGraphSession(
		t,
		transcriptDir,
		sessionID,
		&session.SessionMetadataFull{
			SessionID:          sessionID,
			QueryKernelVersion: "project_graph/v999",
			CreatedAt:          time.Now().UTC(),
		},
	)
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	model := &canonicalScriptModel{}
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			transcriptDir,
			sessionID,
			model,
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	if eng.queryKernelSelection.err == nil {
		t.Fatal("unsupported persisted kernel was accepted")
	}
	events, terminal := eng.SubmitMessage(
		context.Background(),
		"must not reach the model",
	)
	if terminal.Reason != TerminalModelError ||
		terminal.Err == nil ||
		!strings.Contains(
			terminal.Err.Error(),
			"unsupported persisted query kernel version",
		) {
		t.Fatalf("terminal = %#v", terminal)
	}
	for range events {
	}
	if model.callCount != 0 {
		t.Fatalf("model calls = %d, want zero", model.callCount)
	}
	eng.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("unsupported-kernel transcript was rewritten")
	}
}

func TestP1310ResumeRejectsUnsupportedAndRetiredKernelsBeforeMutation(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{
			name:    "unsupported",
			version: "project_graph/v999",
			wantErr: "unsupported persisted query kernel version",
		},
		{
			name:    "retired",
			version: queryKernelVersionLegacy,
			wantErr: "is retired",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transcriptDir := t.TempDir()
			sourceID := test.name + "-resume-kernel"
			writeProjectGraphSession(
				t,
				transcriptDir,
				sourceID,
				&session.SessionMetadataFull{
					SessionID:          sourceID,
					QueryKernelVersion: test.version,
					CreatedAt:          time.Now().UTC(),
				},
			)
			path := filepath.Join(transcriptDir, sourceID+".jsonl")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			eng := NewQueryEngine(
				projectGraphEngineConfig(
					t,
					transcriptDir,
					"still-current",
					&canonicalScriptModel{},
					tools.NewRegistry(),
					&tools.ToolSelection{},
				),
			)
			if _, err := eng.ResumeSession(
				context.Background(),
				sourceID,
			); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("resume error = %v", err)
			}
			if eng.SessionID() != "still-current" ||
				eng.queryKernelSelection.kernel == nil ||
				eng.queryKernelSelection.kernel.kind() !=
					queryKernelProjectGraph ||
				eng.queryKernelSelection.stage !=
					queryKernelStageFull {
				t.Fatalf(
					"current session mutated: id=%q selection=%#v",
					eng.SessionID(),
					eng.queryKernelSelection,
				)
			}
			eng.Close()
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected resume rewrote the source transcript")
			}
		})
	}
}

func TestP1310FullProjectGraphExecutesExternalToolExactlyOnce(
	t *testing.T,
) {
	t.Parallel()

	registry := tools.NewRegistry()
	var toolCalls atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "mcp__server__remote"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			toolCalls.Add(1)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "mcp-call",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "mcp__server__remote",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			t.TempDir(),
			"full-external-tool",
			model,
			registry,
			&tools.ToolSelection{
				Names: []string{"mcp__server__remote"},
			},
		),
	)
	defer eng.Close()
	events, terminal := eng.SubmitMessage(
		context.Background(),
		"run external tool",
	)
	if terminal.Err != nil {
		t.Fatalf("submit: %v", terminal.Err)
	}
	var final *Terminal
	for event := range events {
		if event.Type == EventTerminal {
			final = event.TerminalInfo
		}
	}
	if final == nil || final.Reason != TerminalCompleted || final.Err != nil {
		t.Fatalf("final terminal = %#v", final)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want 2", model.callCount)
	}
}

func TestP1310ProjectGraphFailureNeverReplays(t *testing.T) {
	t.Parallel()

	model := &canonicalScriptModel{}
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			t.TempDir(),
			"no-fallback",
			model,
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	defer eng.Close()
	var graphCalls atomic.Int32
	sentinel := errors.New("project Graph failed")
	eng.mu.Lock()
	eng.queryKernelSelection = sessionQueryKernelSelection{
		kernel: p1310FailingProjectGraphKernel{
			calls: &graphCalls,
			err:   sentinel,
		},
		version: queryKernelVersionProjectGraph,
		stage:   queryKernelStageFull,
	}
	eng.mu.Unlock()

	events, terminal := eng.SubmitMessage(
		context.Background(),
		"do not replay",
	)
	if terminal.Err != nil {
		t.Fatalf("synchronous submit error = %v", terminal.Err)
	}
	var final *Terminal
	for event := range events {
		if event.Type == EventTerminal {
			final = event.TerminalInfo
		}
	}
	if final == nil ||
		final.Reason != TerminalModelError ||
		!errors.Is(final.Err, sentinel) {
		t.Fatalf("final terminal = %#v", final)
	}
	if got := graphCalls.Load(); got != 1 {
		t.Fatalf("Graph calls = %d, want 1", got)
	}
	if model.callCount != 0 {
		t.Fatalf("model calls = %d, want zero", model.callCount)
	}
}

func TestP1310PermissionCapableGraphRequiresDurableCheckpoint(t *testing.T) {
	t.Parallel()

	model := &canonicalScriptModel{}
	eng := NewQueryEngine(
		projectGraphEngineConfig(
			t,
			t.TempDir(),
			"checkpoint-required",
			model,
			tools.NewRegistry(),
			&tools.ToolSelection{},
		),
	)
	defer eng.Close()
	sentinel := errors.New("checkpoint store unavailable")
	eng.mu.Lock()
	eng.projectGraphHITLEnabled = true
	eng.projectGraphCheckpoint = nil
	eng.projectGraphCheckpointErr = sentinel
	eng.mu.Unlock()

	events, terminal := eng.SubmitMessage(
		context.Background(),
		"must fail before model",
	)
	if terminal.Reason != TerminalModelError ||
		terminal.Err == nil ||
		!errors.Is(terminal.Err, sentinel) ||
		!strings.Contains(
			terminal.Err.Error(),
			"session project graph checkpoint is unavailable",
		) {
		t.Fatalf("terminal = %#v", terminal)
	}
	for range events {
	}
	if model.callCount != 0 {
		t.Fatalf("model calls = %d, want zero", model.callCount)
	}
}

func projectGraphEngineConfig(
	t *testing.T,
	transcriptDir string,
	sessionID string,
	model *canonicalScriptModel,
	registry *tools.Registry,
	selection *tools.ToolSelection,
) QueryEngineConfig {
	t.Helper()
	return QueryEngineConfig{
		SessionID:     sessionID,
		TranscriptDir: transcriptDir,
		CWD:           t.TempDir(),
		ChatModel:     model,
		ToolRegistry:  registry,
		ToolSelection: selection,
		MCPManager:    tools.NewMCPToolManager(),
		SkillRegistry: skills.NewSkillRegistry(),
	}
}

func writeProjectGraphSession(
	t *testing.T,
	transcriptDir string,
	sessionID string,
	meta *session.SessionMetadataFull,
) {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	if err := recorder.Record([]*schema.Message{
		{Role: schema.User, Content: "existing user"},
		{Role: schema.Assistant, Content: "existing assistant"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		if err := session.WriteSessionMetadata(recorder, meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
}

func loadProjectGraphSession(
	t *testing.T,
	transcriptDir string,
	sessionID string,
) *transcript.LoadResult {
	t.Helper()
	loaded, err := transcript.NewRecorder(sessionID, transcriptDir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}
