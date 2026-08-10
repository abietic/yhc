package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/compact"
)

func TestSessionMemoryServiceThresholdsAndPersistence(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	service := NewSessionMemoryService(func(ctx context.Context, systemPrompt string, messages []string) (string, error) {
		call := calls.Add(1)
		if call == 1 {
			if !strings.Contains(systemPrompt, "Extract key information") {
				t.Fatalf("unexpected initial prompt: %s", systemPrompt)
			}
			return "initial notes", nil
		}
		if !strings.Contains(systemPrompt, "Previous notes:\ninitial notes") {
			t.Fatalf("update prompt missing previous notes: %s", systemPrompt)
		}
		return "updated notes", nil
	}, dir, "session-1")

	for i := 0; i < 4; i++ {
		service.RecordToolCall(context.Background(), []string{"msg"})
	}
	waitForCondition(t, 200*time.Millisecond, func() bool { return calls.Load() == 0 })

	service.RecordToolCall(context.Background(), []string{"msg"})
	waitForCondition(t, time.Second, func() bool { return service.GetContent() == "initial notes" })

	for i := 0; i < 10; i++ {
		service.RecordToolCall(context.Background(), []string{"msg"})
	}
	memoryPath := filepath.Join(dir, "session-memory-session-1.md")
	waitForCondition(t, time.Second, func() bool {
		data, err := os.ReadFile(memoryPath)
		return err == nil && string(data) == "updated notes"
	})
}

func TestExtractMemoriesServiceWritesDailyAutoMemory(t *testing.T) {
	dir := t.TempDir()
	service := NewExtractMemoriesService(func(ctx context.Context, prompt string, messages []string) (string, error) {
		if !strings.Contains(prompt, "Extract any durable learnings") {
			t.Fatalf("unexpected prompt: %s", prompt)
		}
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(messages))
		}
		return "project pattern: use gomock", nil
	}, dir)

	if err := service.Extract(context.Background(), []string{"one", "two"}); err != nil {
		t.Fatalf("short extract should not error: %v", err)
		return
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("short extract should not write files: %#v", entries)
	}

	if err := service.Extract(context.Background(), []string{"one", "two", "three"}); err != nil {
		t.Fatalf("extract failed: %v", err)
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "auto-"+time.Now().Format("2006-01-02")+".md"))
	if err != nil {
		t.Fatalf("expected auto memory file: %v", err)
		return
	}
	if !strings.Contains(string(data), "project pattern: use gomock") {
		t.Fatalf("unexpected auto memory content: %q", string(data))
	}
}

func TestAutoDreamServiceGatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	service := NewAutoDreamService(func(ctx context.Context, systemPrompt string, messages []string) (string, error) {
		calls.Add(1)
		if !strings.Contains(systemPrompt, "Consolidate memories") {
			t.Fatalf("unexpected dream prompt: %s", systemPrompt)
		}
		return "consolidated memory", nil
	}, dir)

	for i := 0; i < 4; i++ {
		service.RecordSession()
	}
	if service.ShouldDream() {
		t.Fatal("should not dream before minimum sessions")
	}
	if err := service.RunDream(context.Background(), []string{"too early"}); err != nil {
		t.Fatalf("too-early dream should not error: %v", err)
		return
	}
	if calls.Load() != 0 {
		t.Fatalf("model called before gates met")
	}

	service.RecordSession()
	if !service.ShouldDream() {
		t.Fatal("should dream after five sessions")
	}
	if err := service.RunDream(context.Background(), []string{"s1", "s2"}); err != nil {
		t.Fatalf("dream failed: %v", err)
		return
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one dream model call, got %d", calls.Load())
	}
	if service.ShouldDream() {
		t.Fatal("should not dream immediately after successful dream")
	}
	data, err := os.ReadFile(filepath.Join(dir, "consolidated-"+time.Now().Format("2006-01-02")+".md"))
	if err != nil {
		t.Fatalf("expected consolidated memory file: %v", err)
		return
	}
	if string(data) != "consolidated memory" {
		t.Fatalf("unexpected consolidated memory: %q", string(data))
	}
}

func TestAutoDreamUsesPersistentTranscriptEvidenceAcrossRestart(t *testing.T) {
	transcriptDir := t.TempDir()
	memoryDir := filepath.Join(transcriptDir, "memory")
	store := compact.NewMemoryStore(memoryDir, 20)
	for _, sessionID := range []string{"past-a", "past-b"} {
		if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte("transcript"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(transcriptDir, sessionID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session-memory-"+sessionID+".md"), []byte("summary "+sessionID), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var calls atomic.Int32
	modelFn := func(ctx context.Context, systemPrompt string, messages []string) (string, error) {
		calls.Add(1)
		if len(messages) != 2 {
			t.Fatalf("dream summaries = %#v, want two persisted sessions", messages)
		}
		return "persistent consolidation", nil
	}
	service := newAutoDreamService(modelFn, transcriptDir, memoryDir, "current", store)
	service.minHoursSince = 24
	service.minSessions = 2
	service.scanInterval = 0
	if err := service.RunIfDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("dream model calls = %d, want 1", calls.Load())
	}

	restarted := newAutoDreamService(modelFn, transcriptDir, memoryDir, "next", store)
	restarted.minHoursSince = 24
	restarted.minSessions = 2
	if restarted.ShouldDream() {
		t.Fatal("persisted last-consolidated time should suppress restart bias")
	}
	if got := store.GetRelevant("persistent consolidation", 1); len(got) != 1 || got[0].Category != "dream_consolidation" {
		t.Fatalf("dream result was not stored for later relevance: %#v", got)
	}
}

func TestAutoDreamExclusiveLockPreventsDuplicateModelCall(t *testing.T) {
	transcriptDir := t.TempDir()
	for _, sessionID := range []string{"past-a", "past-b"} {
		if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte("transcript"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, ".auto-dream.lock"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	service := newAutoDreamService(func(context.Context, string, []string) (string, error) {
		calls.Add(1)
		return "unexpected", nil
	}, transcriptDir, filepath.Join(transcriptDir, "memory"), "current", nil)
	service.minSessions = 2
	service.scanInterval = 0
	if err := service.RunIfDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("exclusive lock allowed %d duplicate dream calls", calls.Load())
	}
}

func TestBackgroundServicesShutdownCancelsAndJoinsMemoryUpdate(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	service := NewBackgroundServices(BackgroundServicesConfig{
		ModelFn: func(ctx context.Context, _ string, _ []string) (string, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return "", ctx.Err()
		},
		MemoryDir: t.TempDir(), SessionID: "session",
	})
	service.Start()
	for range 5 {
		service.RecordToolCall([]string{"message"})
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("session-memory update did not start at threshold")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown did not join owned work: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned before model worker observed cancellation")
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %v", timeout)
	}
}
