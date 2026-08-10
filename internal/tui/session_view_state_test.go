package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestPersistSessionViewStateKeepsOnlySafePresentationFields(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	query := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "session", ThreadID: "leader", CWD: cwd, TranscriptDir: dir})
	t.Cleanup(query.Close)
	app := New(Config{Engine: query})
	app.textarea.SetValue("plain draft")
	app.textarea.CursorEnd()
	app.composerElements = []threadComposerElement{{ID: "image-secret", Kind: "image", Data: "base64-secret"}}
	app.queuedInputPreview = []threadQueuedInput{{
		ID:    "queue-secret",
		Parts: []engine.QueuedPromptPart{{Kind: engine.QueuedPromptPartText, Text: "queued-secret"}},
	}}
	app.composerUndo = []composerUndoEntry{{Text: "undo-secret"}}
	app.chat.restoreAway()
	app.chat.offsetIdx = 3
	app.chat.offsetLine = 2

	if err := app.persistSessionViewState(); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.LoadSessionViewState(dir, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Threads) != 1 || loaded.Threads[0].Draft != "plain draft" || loaded.Threads[0].Follow || loaded.Threads[0].ScrollItem != 3 {
		t.Fatalf("persisted view = %#v", loaded)
	}
	data, err := os.ReadFile(session.SessionViewStatePath(dir, "session"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"image-secret", "base64-secret", "queue-secret", "queued-secret", "undo-secret"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("view sidecar leaked %q: %s", forbidden, data)
		}
	}
}

func TestNewResumedAppRestoresAgentViewAgainstRuntimeCatalog(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	recorder := transcript.NewRecorder("session", dir)
	if err := recorder.Replace([]*schema.Message{
		{Role: schema.User, Content: "leader prompt"},
		{Role: schema.Assistant, Content: "leader answer"},
	}); err != nil {
		t.Fatal(err)
	}
	store := engine.NewRuntimeStateStore()
	if err := store.RestoreAgentSnapshot(engine.RuntimeAgentSnapshot{
		AgentID: "agent-1", SessionID: "agent-session", ThreadID: "agent-thread",
		ParentSessionID: "session", ParentThreadID: "leader", Status: "completed",
		StartedAt: time.Now().Add(-time.Minute), CompletedAt: time.Now(),
	}, []*schema.Message{{Role: schema.Assistant, Content: "agent answer"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveSessionViewState(dir, "session", session.PersistedSessionViewState{
		ActiveThreadID: "agent-thread",
		Threads: []session.PersistedThreadViewState{
			{ThreadID: "leader", Mode: string(engine.ThreadModeLiveAttach), Draft: "leader draft", Follow: true},
			{ThreadID: "agent-thread", Mode: string(engine.ThreadModeLiveAttach), Draft: "agent draft", CursorColumn: 5, Follow: false, DetailTab: int(agentDetailTranscript)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "session", ThreadID: "leader", CWD: cwd, TranscriptDir: dir, RuntimeState: store,
	})
	t.Cleanup(query.Close)
	app := New(Config{Engine: query, Resumed: true})

	if app.activeThreadViewID() != "agent-thread" || app.activeThreadViewMode() != engine.ThreadModeReplayOnly {
		t.Fatalf("active restored view id=%q mode=%q", app.activeThreadViewID(), app.activeThreadViewMode())
	}
	if app.textarea.Value() != "agent draft" || app.threadDetailTab != agentDetailTranscript {
		t.Fatalf("restored draft=%q tab=%d", app.textarea.Value(), app.threadDetailTab)
	}
	if len(app.composerElements) != 0 || len(app.queuedInputPreview) != 0 || len(app.composerUndo) != 0 {
		t.Fatalf("unsafe state restored: elements=%#v queue=%#v undo=%#v", app.composerElements, app.queuedInputPreview, app.composerUndo)
	}
}

func TestScheduleSessionViewSaveFreezesDuringResume(t *testing.T) {
	query := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "session", CWD: t.TempDir(), TranscriptDir: t.TempDir()})
	t.Cleanup(query.Close)
	app := New(Config{Engine: query})
	if command := app.scheduleSessionViewSave(); command == nil {
		t.Fatal("expected debounced save command")
	}
	app.sessionRestorePending = true
	if command := app.scheduleSessionViewSave(); command != nil {
		t.Fatal("resume should freeze sidecar writes")
	}
}
