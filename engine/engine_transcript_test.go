package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type transcriptModel struct{}

type originTranscriptModel struct {
	origin    providerorigin.Origin
	streamErr error
}

func (m *originTranscriptModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("generate not used")
}

func (m *originTranscriptModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	providerorigin.PublishDispatch(ctx, m.origin)
	chunk := &schema.Message{
		Role:             schema.Assistant,
		Content:          "done",
		ReasoningContent: "private reasoning",
	}
	if m.streamErr == nil {
		return schema.StreamReaderFromArray([]*schema.Message{chunk}), nil
	}
	reader, writer := schema.Pipe[*schema.Message](2)
	go func() {
		defer writer.Close()
		writer.Send(chunk, nil)
		writer.Send(nil, m.streamErr)
	}()
	return reader, nil
}

func engineTranscriptOrigin() providerorigin.Origin {
	return providerorigin.Origin{
		Version:             providerorigin.OriginVersion,
		Provider:            "agenticopenai",
		AccountID:           "work-openai",
		APIFamily:           providerorigin.OpenAIResponsesV1,
		APIModel:            "gpt-5.4",
		RouteIdentityDigest: strings.Repeat("b", 64),
		CredentialOriginID:  "local-record/r4",
		RoutePublication:    12,
	}
}

func (m *transcriptModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *transcriptModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

type repairingTranscriptModel struct {
	transcriptModel
	once sync.Once
	dir  string
	err  error
}

func (m *repairingTranscriptModel) repair() error {
	m.once.Do(func() {
		if err := os.Remove(m.dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.err = err
			return
		}
		m.err = os.MkdirAll(m.dir, 0o755)
	})
	return m.err
}

func (m *repairingTranscriptModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if err := m.repair(); err != nil {
		return nil, err
	}
	return m.transcriptModel.Generate(ctx, input, opts...)
}

func (m *repairingTranscriptModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.repair(); err != nil {
		return nil, err
	}
	return m.transcriptModel.Stream(ctx, input, opts...)
}

type failingTranscriptModel struct{}

func (m *failingTranscriptModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("model failed")
}

func (m *failingTranscriptModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("model failed")
}

func TestQueryEnginePersistsAndReloadsTranscript(t *testing.T) {
	dir := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-a",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           3,
		ChatModel:          &transcriptModel{},
		Clock: func() time.Time {
			return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
		},
	})

	events, _ := eng.SubmitMessage(context.Background(), "hello")
	for range events {
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-a",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(msgs))
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "hello" {
		t.Fatalf("unexpected first message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || msgs[1].Content != "done" {
		t.Fatalf("unexpected second message: %#v", msgs[1])
	}
}

func TestQueryEnginePersistsOriginOnlyAfterCompleteStream(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "origin-complete",
		TranscriptDir: transcriptDir,
		CWD:           dir,
		ChatModel: &originTranscriptModel{
			origin: engineTranscriptOrigin(),
		},
	})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(t.Context(), "hello")
	for range events {
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("messages = %#v", loaded.Messages)
	}
	resolution := eng.GetTranscript().AssistantOriginResolver().ResolveAssistantOrigin(
		loaded.Messages[1],
	)
	if resolution.State != providerorigin.BindingVerified ||
		resolution.Origin.CredentialOriginID != "local-record/r4" {
		t.Fatalf("persisted origin = %#v", resolution)
	}

	reloaded := transcript.NewRecorder("origin-complete", transcriptDir)
	reloadedResult, err := reloaded.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	resolution = reloaded.AssistantOriginResolver().ResolveAssistantOrigin(
		reloadedResult.Messages[1],
	)
	if resolution.State != providerorigin.BindingVerified {
		t.Fatalf("reloaded origin = %#v", resolution)
	}
}

func TestQueryEngineDoesNotPersistOriginForFailedStream(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "origin-failed",
		TranscriptDir: filepath.Join(dir, "transcripts"),
		CWD:           dir,
		ChatModel: &originTranscriptModel{
			origin:    engineTranscriptOrigin(),
			streamErr: errors.New("stream interrupted"),
		},
	})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(t.Context(), "hello")
	for range events {
	}
	raw, err := os.ReadFile(eng.GetTranscript().Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "assistant_origins") ||
		strings.Contains(string(raw), "local-record/r4") {
		t.Fatalf("failed stream minted origin: %s", raw)
	}
}

func TestQueryEngineNormalCheckpointsAppendMessagesWithoutFullStateDuplication(t *testing.T) {
	dir := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session-linear-audit",
		TranscriptDir: filepath.Join(dir, "transcripts"),
		CWD:           dir,
		ChatModel:     &transcriptModel{},
	})
	t.Cleanup(eng.Close)

	for _, prompt := range []string{"first", "second"} {
		events, _ := eng.SubmitMessage(context.Background(), prompt)
		for range events {
		}
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 4 {
		t.Fatalf("active messages = %#v", loaded.Messages)
	}
	for _, boundary := range loaded.LifecycleBoundaries {
		if boundary.Kind == transcript.LifecycleCheckpoint {
			t.Fatalf(
				"normal append-only turn emitted a full state checkpoint: %#v",
				loaded.LifecycleBoundaries,
			)
		}
	}
}

func TestQueryEngineRepairsTransientInitialTranscriptFailureWithFullCheckpoint(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	if err := os.WriteFile(transcriptDir, []byte("temporary blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskManager, logicalWorkAdapter := p311bBoundLogicalWorkFixture(
		t,
		"transient-transcript-failure",
	)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "transient-transcript-failure",
		TranscriptDir:      transcriptDir,
		CWD:                root,
		TaskManager:        taskManager,
		logicalWorkAdapter: logicalWorkAdapter,
		ChatModel: &repairingTranscriptModel{
			dir: transcriptDir,
		},
	})
	t.Cleanup(eng.Close)

	var terminal *Terminal
	events, _ := eng.SubmitMessage(t.Context(), "must remain paired")
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil || terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 ||
		loaded.Messages[0].Content != "must remain paired" ||
		loaded.Messages[1].Content != "done" {
		t.Fatalf("repaired active transcript = %#v", loaded.Messages)
	}
	if len(loaded.LifecycleBoundaries) != 1 ||
		loaded.LifecycleBoundaries[0].Kind != transcript.LifecycleCheckpoint {
		t.Fatalf("repair boundaries = %#v", loaded.LifecycleBoundaries)
	}
}

func TestQueryEngineFlushesUserMessageBeforeModelErrorTerminal(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "durable-user-before-error",
		TranscriptDir: transcriptDir,
		CWD:           root,
		ChatModel:     &failingTranscriptModel{},
	})
	t.Cleanup(eng.Close)

	var terminal *Terminal
	events, _ := eng.SubmitMessage(t.Context(), "persist before model failure")
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil || terminal.Reason != TerminalModelError {
		t.Fatalf("terminal = %#v", terminal)
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) == 0 ||
		loaded.Messages[0].Role != schema.User ||
		loaded.Messages[0].Content != "persist before model failure" {
		t.Fatalf("durable user transcript = %#v", loaded.Messages)
	}
}

func TestQueryEngineSurfacesUnrepairedFinalTranscriptFailure(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	if err := os.WriteFile(transcriptDir, []byte("persistent blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskManager, logicalWorkAdapter := p311bBoundLogicalWorkFixture(
		t,
		"persistent-transcript-failure",
	)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "persistent-transcript-failure",
		TranscriptDir:      transcriptDir,
		CWD:                root,
		ChatModel:          &transcriptModel{},
		TaskManager:        taskManager,
		logicalWorkAdapter: logicalWorkAdapter,
	})
	t.Cleanup(eng.Close)

	var terminal *Terminal
	events, _ := eng.SubmitMessage(t.Context(), "cannot persist")
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil ||
		terminal.Reason != TerminalPersistenceError ||
		terminal.Err == nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	if !eng.transcriptCheckpointRequired {
		t.Fatal("failed final persistence did not require a repair checkpoint")
	}
}

func TestCompactBoundaryUncertainCommitFallsBackToCheckpointRepair(t *testing.T) {
	uncertain := &transcript.DurabilityUncertainError{
		Operation: "sync transcript file",
		Err:       errors.New("injected sync failure"),
	}
	if shouldRetryCompactBoundary(uncertain) {
		t.Fatal("uncertain compact commit would append a duplicate boundary")
	}
	if !shouldRetryCompactBoundary(errors.New("known pre-write failure")) {
		t.Fatal("known pre-write compact failure should remain retryable")
	}
}

func TestQueryEngineReloadsCompactedTranscriptShape(t *testing.T) {
	dir := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-compact",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          &captureInputModel{},
		Clock: func() time.Time {
			return time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
		},
	})

	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")
	events, _ := eng.SubmitMessage(context.Background(), strings.Repeat("hello world ", 260))
	for range events {
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-compact",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) < 3 {
		t.Fatalf("expected compacted transcript shape, got %#v", msgs)
	}
	if msgs[0].Extra == nil || msgs[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected compact boundary first after reload, got %#v", msgs[0])
		return
	}
	if msgs[1].Extra == nil || msgs[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected compact summary second after reload, got %#v", msgs[1])
		return
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	var compactBoundaries int
	for _, boundary := range loaded.LifecycleBoundaries {
		if boundary.Kind == transcript.LifecycleCompact {
			compactBoundaries++
		}
	}
	if compactBoundaries != 1 {
		t.Fatalf("auto compact boundaries = %#v", loaded.LifecycleBoundaries)
	}
	raw, err := os.ReadFile(eng.GetTranscript().Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hello world") {
		t.Fatal("auto compact removed the pre-compact transcript audit")
	}
}
