package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestResumeSessionInfoUsesSelectedStoreInSameCWD(t *testing.T) {
	cwd := t.TempDir()
	currentDir := filepath.Join(cwd, "current")
	selectedDir := filepath.Join(cwd, "selected")
	recorder := writeEngineSelectedSession(t, selectedDir, "selected", "selected prompt")
	engine := NewQueryEngine(QueryEngineConfig{SessionID: "current", CWD: cwd, TranscriptDir: currentDir})
	t.Cleanup(engine.Close)

	resumed, err := engine.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "selected",
		CWD:            cwd,
		TranscriptDir:  selectedDir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "selected" || engine.SessionID() != "selected" || engine.GetTranscriptDir() != selectedDir {
		t.Fatalf("resume result=%#v engine=%q dir=%q", resumed, engine.SessionID(), engine.GetTranscriptDir())
	}
	if len(engine.GetMessages()) != 2 || engine.GetMessages()[0].Content != "selected prompt" {
		t.Fatalf("messages = %#v", engine.GetMessages())
	}
}

func TestResumeSessionInfoRestoresCrossCWDExecutionContext(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	additional := t.TempDir()
	dir := filepath.Join(other, "transcripts")
	recorder := writeEngineSelectedSession(t, dir, "other", "other prompt")
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "other", ThreadID: "other-thread", Model: "gpt-5",
		PermissionMode: "plan", CWD: other, AdditionalDirs: []string{additional},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{SessionID: "current", CWD: cwd, TranscriptDir: filepath.Join(cwd, "transcripts")})
	t.Cleanup(engine.Close)

	resumed, err := engine.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "other", CWD: other, TranscriptDir: dir, TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "other" || engine.SessionID() != "other" || engine.ThreadID() != "other-thread" {
		t.Fatalf("restored identity: resumed=%#v session=%q thread=%q", resumed, engine.SessionID(), engine.ThreadID())
	}
	if engine.GetCWD() != other || engine.GetModelName() != "gpt-5" || engine.PermissionMode() != "plan" {
		t.Fatalf("execution context cwd=%q model=%q mode=%q", engine.GetCWD(), engine.GetModelName(), engine.PermissionMode())
	}
	if got := engine.GetWorkingDirectories(); len(got) != 2 || got[1] != additional {
		t.Fatalf("working directories = %#v", got)
	}
}

func TestResumeSessionInfoFallsBackWhenPersistedWorktreeIsMissing(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	recorder := writeEngineSelectedSession(t, dir, "selected", "prompt")
	missing := filepath.Join(cwd, "missing-worktree")
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", CWD: cwd, WorktreePath: missing,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	engine := NewQueryEngine(QueryEngineConfig{SessionID: "current", CWD: cwd, TranscriptDir: dir})
	t.Cleanup(engine.Close)

	resumed, err := engine.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.GetCWD() != cwd || !containsSessionWarning(resumed.Warnings, "worktree") {
		t.Fatalf("fallback cwd=%q warnings=%#v", engine.GetCWD(), resumed.Warnings)
	}
}

func TestForkSessionInfoCopiesSelectedTranscriptAndResumesChild(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	recorder := writeEngineSelectedSession(t, dir, "parent", "parent prompt")
	fixed := time.Date(2026, 7, 11, 12, 34, 56, 0, time.UTC)
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir, Clock: func() time.Time { return fixed },
	})
	t.Cleanup(engine.Close)

	resumed, branch, err := engine.ForkSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "parent", CWD: cwd, TranscriptDir: dir, TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch == nil || branch.ParentSessionID != "parent" || resumed.SessionID != branch.NewSessionID || engine.SessionID() != branch.NewSessionID {
		t.Fatalf("fork result resumed=%#v branch=%#v", resumed, branch)
	}
	parent, err := transcript.NewRecorder("parent", dir).LoadFull()
	if err != nil || len(parent.Messages) != 2 {
		t.Fatalf("parent changed: messages=%#v err=%v", parent.Messages, err)
	}
	child, err := transcript.NewRecorder(branch.NewSessionID, dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Messages) != 2 {
		t.Fatalf("child messages = %#v", child.Messages)
	}
	metadata := map[string]string{}
	for _, entry := range child.Metadata {
		metadata[entry.Key] = entry.Value
	}
	if metadata["parent_session_id"] != "parent" || metadata["forked"] != "true" || metadata["branch_name"] != "fork-20260711-123456" {
		t.Fatalf("child metadata = %#v", metadata)
	}
}

func writeEngineSelectedSession(t *testing.T, dir, id, prompt string) *transcript.Recorder {
	t.Helper()
	recorder := transcript.NewRecorder(id, dir)
	if err := recorder.Replace([]*schema.Message{
		{Role: schema.User, Content: prompt},
		{Role: schema.Assistant, Content: "answer"},
	}); err != nil {
		t.Fatal(err)
	}
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: id,
		ThreadID:  id,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recorder.Path()); err != nil {
		t.Fatal(err)
	}
	return recorder
}

func writeProjectGraphRootTestMetadata(
	t *testing.T,
	recorder *transcript.Recorder,
	metadata *session.SessionMetadataFull,
) {
	t.Helper()
	if metadata == nil {
		metadata = &session.SessionMetadataFull{}
	}
	metadata.QueryKernelVersion = queryKernelVersionProjectGraph
	metadata.QueryKernelStage = string(queryKernelStageFull)
	if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
		t.Fatal(err)
	}
}

func containsSessionWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}
