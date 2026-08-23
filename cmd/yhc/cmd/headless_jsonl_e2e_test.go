package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	enginetransport "github.com/abietic/yhc/engine/transport"
)

type failingHeadlessWriter struct {
	err error
}

func (writer failingHeadlessWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func TestHeadlessJSONLPublicExecStreamsCommittedLifecycle(t *testing.T) {
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	p430CopyFixture(t, repo)
	outside := filepath.Join(root, "outside-sentinel")
	if err := os.WriteFile(outside, []byte(p430OutsideBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	script := p430Provider(t, outside, filepath.Join(repo, "greet", "decorate.go"))
	defer script.server.Close()

	home := filepath.Join(root, "home")
	for _, pair := range [][2]string{
		{"HOME", home},
		{"XDG_CONFIG_HOME", filepath.Join(home, "config")},
		{"XDG_DATA_HOME", filepath.Join(home, "data")},
		{"XDG_CACHE_HOME", filepath.Join(home, "cache")},
	} {
		t.Setenv(pair[0], pair[1])
	}
	for _, name := range []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "PROV",
		"PROV_API_KEY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"SSH_AUTH_SOCK",
	} {
		t.Setenv(name, "")
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var stdout, stderr bytes.Buffer
	rootCmd := newRootCommand()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"exec", p430Prompt,
		"--output-format", "jsonl", "--provider", "openai", "--model", "gpt-4o", "--base-url", script.server.URL,
		"--api-key", p430FakeKey, "--max-turns", "3", "--permission-mode", "acceptEdits", "--tools", "Write",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("public headless JSONL exec: %v; stderr=%s", err, stderr.String())
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var records []enginetransport.LifecycleRecord
	for {
		var record enginetransport.LifecycleRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode lifecycle record: %v; output=%s", err, stdout.String())
		}
		records = append(records, record)
	}
	if len(records) < 2 {
		t.Fatalf("lifecycle records = %#v", records)
	}

	wantKinds := map[string]bool{
		"assistant_delta": false,
		"tool_start":      false,
		"tool_input":      false,
		"tool_terminal":   false,
	}
	var previousSequence uint64
	for index, record := range records[:len(records)-1] {
		if record.SchemaVersion != enginetransport.LifecycleSchemaVersion ||
			record.Type != enginetransport.LifecycleRecordEvent ||
			record.Event == nil ||
			record.Result != nil {
			t.Fatalf("event record %d = %#v", index, record)
		}
		if record.Event.Sequence == 0 || record.Event.Sequence <= previousSequence {
			t.Fatalf("event sequence at %d = %d after %d", index, record.Event.Sequence, previousSequence)
		}
		previousSequence = record.Event.Sequence
		if _, ok := wantKinds[record.Event.Kind]; ok {
			wantKinds[record.Event.Kind] = true
		}
	}
	for kind, seen := range wantKinds {
		if !seen {
			t.Fatalf("missing committed %s event: %#v", kind, records)
		}
	}

	final := records[len(records)-1]
	if final.SchemaVersion != enginetransport.LifecycleSchemaVersion ||
		final.Type != enginetransport.LifecycleRecordResult ||
		final.Event != nil ||
		final.Result == nil ||
		final.Result.Status != "completed" ||
		final.Result.Output != "fixed" ||
		final.Result.ExitCode != ExitSuccess ||
		final.Result.SessionID == "" ||
		final.Result.Sequence <= previousSequence {
		t.Fatalf("final lifecycle record = %#v", final)
	}
	if calls := script.calls.Load(); calls != 3 {
		t.Fatalf("provider calls = %d, want 3", calls)
	}
}

func TestHeadlessJSONLPreTurnFailureClosesWithSessionIdentity(t *testing.T) {
	root := t.TempDir()
	blockedTranscriptDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedTranscriptDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "session-pre-turn",
		ThreadID:      "thread-not-entered",
		TranscriptDir: blockedTranscriptDir,
		CWD:           root,
	})
	defer eng.Close()

	var stdout, stderr bytes.Buffer
	writer := enginetransport.NewLifecycleWriter(&stdout)
	events, _ := eng.SubmitMessage(context.Background(), "must fail before turn admission")
	result, streamErr := collectHeadlessEventsWithObserver(
		context.Background(),
		&stderr,
		events,
		func(event engine.QueryEvent) error {
			_, err := writer.WriteEvent(event)
			return err
		},
	)
	if streamErr != nil {
		t.Fatalf("stream pre-turn failure: %v", streamErr)
	}
	if result.Err == nil || result.TerminalReason != string(engine.TerminalModelError) {
		t.Fatalf("pre-turn result = %#v", result)
	}
	result.SessionID = eng.SessionID()
	result.Err = sanitizeHeadlessError(result.Err)
	if err := renderHeadlessResult(outputFormatJSONL, &stdout, &stderr, result); err != nil {
		t.Fatalf("render pre-turn result: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var record enginetransport.LifecycleRecord
	if err := decoder.Decode(&record); err != nil {
		t.Fatalf("decode pre-turn result: %v; output=%s", err, stdout.String())
	}
	if record.Type != enginetransport.LifecycleRecordResult ||
		record.Result == nil ||
		record.Result.Status != "failed" ||
		record.Result.ExitCode != ExitFailure ||
		record.Result.SessionID != "session-pre-turn" ||
		record.Result.ThreadID != "" ||
		record.Result.TurnID != "" ||
		record.Result.Sequence != 0 ||
		record.Result.Timestamp != "" {
		t.Fatalf("pre-turn lifecycle record = %#v", record)
	}
	var extra enginetransport.LifecycleRecord
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected pre-turn record %#v, err=%v", extra, err)
	}
}

func TestHeadlessJSONLUsageFailureHasNoRuntimeIdentity(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runHeadless(context.Background(), "", headlessOptions{
		OutputFormat: string(outputFormatJSONL),
		Stdin:        bytes.NewReader(nil),
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if ExitCode(err) != ExitUsage {
		t.Fatalf("usage failure = %v, exit=%d", err, ExitCode(err))
	}

	var record enginetransport.LifecycleRecord
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &record); err != nil {
		t.Fatalf("decode usage result: %v; output=%s", err, stdout.String())
	}
	if record.Type != enginetransport.LifecycleRecordResult ||
		record.Result == nil ||
		record.Result.Status != "failed" ||
		record.Result.ExitCode != ExitUsage ||
		record.Result.Error == nil ||
		record.Result.Error.Code != "usage_error" ||
		record.Result.SessionID != "" ||
		record.Result.ThreadID != "" ||
		record.Result.TurnID != "" ||
		record.Result.Sequence != 0 {
		t.Fatalf("usage lifecycle record = %#v", record)
	}
}

func TestHeadlessJSONLCancellationClosesAfterLastCommittedEvent(t *testing.T) {
	timestamp := time.Date(2026, time.August, 24, 3, 4, 5, 6, time.UTC)
	events := make(chan engine.QueryEvent, 2)
	events <- engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: "session-cancelled",
			ThreadID:  "thread-cancelled",
			TurnID:    "turn-cancelled",
			Sequence:  1,
			Timestamp: timestamp,
		},
		Type: engine.EventCanonicalProjection,
		CanonicalProjection: &engine.CanonicalProjectionEvent{
			Version: engine.CanonicalProjectionVersion,
			Kind:    engine.CanonicalProjectionAssistantDelta,
			Assistant: &engine.CanonicalAssistantPayload{
				MessageID: "assistant-cancelled",
				Delta:     []byte("partial"),
			},
		},
	}
	events <- engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: "session-cancelled",
			ThreadID:  "thread-cancelled",
			TurnID:    "turn-cancelled",
			Sequence:  2,
			Timestamp: timestamp.Add(time.Second),
		},
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalAbortedStreaming,
		},
	}
	close(events)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	writer := enginetransport.NewLifecycleWriter(&stdout)
	result, streamErr := collectHeadlessEventsWithObserver(
		ctx,
		&stderr,
		events,
		func(event engine.QueryEvent) error {
			_, err := writer.WriteEvent(event)
			return err
		},
	)
	if streamErr != nil {
		t.Fatalf("stream cancellation: %v", streamErr)
	}
	result.SessionID = "session-cancelled"
	result.Err = sanitizeHeadlessError(result.Err)
	if err := renderHeadlessResult(outputFormatJSONL, &stdout, &stderr, result); err != nil {
		t.Fatalf("render cancellation result: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var eventRecord, resultRecord enginetransport.LifecycleRecord
	if err := decoder.Decode(&eventRecord); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&resultRecord); err != nil {
		t.Fatal(err)
	}
	if eventRecord.Type != enginetransport.LifecycleRecordEvent ||
		eventRecord.Event == nil ||
		eventRecord.Event.Sequence != 1 ||
		resultRecord.Type != enginetransport.LifecycleRecordResult ||
		resultRecord.Result == nil ||
		resultRecord.Result.Status != "cancelled" ||
		resultRecord.Result.ExitCode != ExitCancelled ||
		resultRecord.Result.Sequence != 2 {
		t.Fatalf("cancellation records = %#v, %#v", eventRecord, resultRecord)
	}
	var extra enginetransport.LifecycleRecord
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected cancellation record %#v, err=%v", extra, err)
	}
}

func TestHeadlessJSONLWriteFailureCancelsAndDrainsTerminal(t *testing.T) {
	writeErr := errors.New("fixture lifecycle write failure")
	events := make(chan engine.QueryEvent, 2)
	events <- engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: "session-write-failure",
			ThreadID:  "thread-write-failure",
			TurnID:    "turn-write-failure",
			Sequence:  1,
		},
		Type: engine.EventCanonicalProjection,
		CanonicalProjection: &engine.CanonicalProjectionEvent{
			Version: engine.CanonicalProjectionVersion,
			Kind:    engine.CanonicalProjectionAssistantDelta,
			Assistant: &engine.CanonicalAssistantPayload{
				MessageID: "assistant-write-failure",
				Delta:     []byte("partial"),
			},
		},
	}
	events <- engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: "session-write-failure",
			ThreadID:  "thread-write-failure",
			TurnID:    "turn-write-failure",
			Sequence:  2,
		},
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	}
	close(events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := enginetransport.NewLifecycleWriter(failingHeadlessWriter{err: writeErr})
	result, streamErr := collectHeadlessEventsWithObserver(
		ctx,
		io.Discard,
		events,
		func(event engine.QueryEvent) error {
			_, err := writer.WriteEvent(event)
			if err != nil {
				cancel()
			}
			return err
		},
	)
	if !errors.Is(streamErr, writeErr) {
		t.Fatalf("stream error = %v, want %v", streamErr, writeErr)
	}
	if result.TerminalEvent.Sequence != 2 ||
		result.TerminalReason != string(engine.TerminalCompleted) ||
		result.Status != "cancelled" ||
		result.ExitCode != ExitCancelled {
		t.Fatalf("drained result = %#v", result)
	}
}
