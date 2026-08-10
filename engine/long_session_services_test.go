package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type longSessionServicesModel struct {
	streamCalls   atomic.Int32
	generateCalls atomic.Int32
}

func (m *longSessionServicesModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.generateCalls.Add(1)
	prompt := ""
	if len(input) > 0 {
		prompt = input[0].Content
	}
	if strings.Contains(prompt, "Extract key information") {
		return &schema.Message{Role: schema.Assistant, Content: "session notes after five tools"}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: "- durable long-session memory"}, nil
}

func (m *longSessionServicesModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.streamCalls.Add(1) == 1 {
		calls := make([]schema.ToolCall, 0, 5)
		for index := 0; index < 5; index++ {
			calls = append(calls, schema.ToolCall{
				ID: "call_" + string(rune('a'+index)), Type: "function",
				Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"printf ok"}`},
			})
		}
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, ToolCalls: calls}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryEngineRecordsProductionToolResultsForSessionMemory(t *testing.T) {
	dir := t.TempDir()
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	chatModel := &longSessionServicesModel{}
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "long-session", CWD: dir, TranscriptDir: filepath.Join(dir, "transcripts"),
		CustomSystemPrompt: "test", MaxTurns: 3, ChatModel: chatModel,
		ToolRegistry: registry, Tools: registry.List(), Model: "test-model",
		CanUseTool:                func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) { return true, "" },
		EnableLongSessionServices: true,
	})

	events, _ := query.SubmitMessage(context.Background(), "run five tools")
	for range events {
	}
	query.Close()

	path := filepath.Join(dir, "transcripts", "long-session", "session-memory-long-session.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("production tool results did not trigger session memory: %v", err)
	}
	if string(data) != "session notes after five tools" {
		t.Fatalf("session memory = %q", string(data))
	}
	if chatModel.generateCalls.Load() == 0 {
		t.Fatal("background services never called the model")
	}
}

func TestResumeRebindsLongSessionServicesToSelectedStorage(t *testing.T) {
	cwd := t.TempDir()
	initialDir := filepath.Join(t.TempDir(), "initial")
	selectedDir := filepath.Join(t.TempDir(), "selected")
	recorder := transcript.NewRecorder("selected-session", selectedDir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: "resume me"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          "selected-session",
			ThreadID:           "selected-session",
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	_ = recorder.Close()

	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	chatModel := &longSessionServicesModel{}
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "initial-session", CWD: cwd, TranscriptDir: initialDir,
		CustomSystemPrompt: "test", MaxTurns: 3, ChatModel: chatModel,
		ToolRegistry: registry, Tools: registry.List(), Model: "test-model",
		CanUseTool:                func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) { return true, "" },
		EnableLongSessionServices: true,
	})
	if _, err := query.ResumeSessionInfo(context.Background(), session.SessionInfo{
		SessionID: "selected-session", CWD: cwd, TranscriptDir: selectedDir,
		TranscriptPath: recorder.Path(),
	}); err != nil {
		t.Fatal(err)
	}
	events, _ := query.SubmitMessage(context.Background(), "run five tools")
	for range events {
	}
	query.Close()

	selectedMemory := filepath.Join(selectedDir, "selected-session", "session-memory-selected-session.md")
	if _, err := os.Stat(selectedMemory); err != nil {
		t.Fatalf("resumed services did not write selected session storage: %v", err)
	}
	initialMemory := filepath.Join(initialDir, "initial-session", "session-memory-initial-session.md")
	if _, err := os.Stat(initialMemory); !os.IsNotExist(err) {
		t.Fatalf("resumed services wrote stale initial storage: %v", err)
	}
}

func TestP242bQueuedLongSessionCallRechecksProviderBoundaryAtDispatch(
	t *testing.T,
) {
	root := t.TempDir()
	chatModel := &longSessionServicesModel{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var goalActive atomic.Bool
	background := newEngineBackgroundServices(
		chatModel,
		root,
		"queued-before-goal",
		compact.NewMemoryStore(filepath.Join(root, "memory"), 10),
		func(
			ctx context.Context,
			chatModel model.BaseChatModel,
			messages []*schema.Message,
			opts ...model.Option,
		) (*schema.Message, error) {
			enteredOnce.Do(func() { close(entered) })
			<-release
			if goalActive.Load() {
				return nil, errGoalUsageCapabilityUnavailable
			}
			return chatModel.Generate(ctx, messages, opts...)
		},
	)
	background.Start()
	for range 5 {
		background.RecordToolCall([]string{"queued before Goal activation"})
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("queued long-session call did not reach provider boundary")
	}
	goalActive.Store(true)
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := background.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := chatModel.generateCalls.Load(); got != 0 {
		t.Fatalf("queued long-session provider calls = %d, want zero", got)
	}
}

func TestP242bLongSessionProviderEntryRejectsUnfinishedGoal(t *testing.T) {
	root := t.TempDir()
	chatModel := &longSessionServicesModel{}
	query := NewQueryEngine(QueryEngineConfig{
		SessionID:     "long-session-goal-boundary",
		ThreadID:      "long-session-goal-boundary",
		CWD:           root,
		TranscriptDir: filepath.Join(root, "transcripts"),
		ChatModel:     chatModel,
	})
	t.Cleanup(query.Close)
	budget := uint64(100)
	if _, err := query.goalService.create(goalCreateRequest{
		Objective:   "block unrelated background provider calls",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := query.callBackgroundProvider(
		context.Background(),
		chatModel,
		[]*schema.Message{{Role: schema.User, Content: "must not dispatch"}},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "unfinished Goal") ||
		chatModel.generateCalls.Load() != 0 {
		t.Fatalf(
			"Goal background provider error=%v calls=%d",
			err,
			chatModel.generateCalls.Load(),
		)
	}
}
