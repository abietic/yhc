// Owned offline fixture. No provider request is made.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func main() {
	if err := seed(); err != nil {
		fmt.Fprintln(os.Stderr, "native fixture seed failed")
		os.Exit(1)
	}
}

func seed() error {
	root, err := os.Getwd()
	if err != nil || (len(os.Args) != 2 && (len(os.Args) != 3 || (os.Args[2] != "--unbound-model" && os.Args[2] != "--tool-ready" && os.Args[2] != "--history-journal" && os.Args[2] != "--restored-history" && os.Args[2] != "--post-import-restore"))) {
		return fmt.Errorf("fixture arguments")
	}
	if len(os.Args) == 3 && (os.Args[2] == "--history-journal" || os.Args[2] == "--restored-history" || os.Args[2] == "--post-import-restore") {
		id := os.Args[1]
		journal, err := filehistory.OpenRevisionJournal(root, filepath.Join(root, ".yhc", "transcripts", id+".workspace-history"), id)
		if err != nil {
			return err
		}
		defer journal.Close()
		restore := os.Args[2] != "--history-journal"
		path, turnID := "side.txt", "fixture-history-turn"
		if os.Args[2] == "--post-import-restore" { path, turnID = "continued.txt", "fixture-post-import-turn" }
		if restore {
			if err := os.WriteFile(filepath.Join(root, path), []byte("operator original"), 0o600); err != nil {
				return err
			}
		}
		if err := journal.RecordTurnStartWithRestoreBoundary(turnID, id, time.Unix(1, 0).UTC(), true, ""); err != nil {
			return err
		}
		if restore {
			revision, err := journal.Begin(turnID, turnID+"-write", "Write", path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, path), []byte("operator changed"), 0o600); err != nil {
				return err
			}
			if err := journal.Finish(revision, "succeeded"); err != nil {
				return err
			}
		}
		turns := journal.Snapshot().Turns
		turn := turns[len(turns)-1]
		turn.FinishedAt, turn.TerminalReason, turn.ToolCountsBounded = time.Unix(2, 0).UTC(), "completed", true
		if restore {
			turn.KnownToolRecords, turn.KnownToolsSucceeded = 1, 1
		}
		if err := journal.RecordTurnTerminal(turn); err != nil || !restore {
			return err
		}
		op, err := journal.PrepareRestoreOperation(turnID, "")
		if err != nil {
			return err
		}
		// This owned operator fixture exercises the real journal transaction;
		// it does not claim interactive permission or TUI acceptance evidence.
		op.ConfirmationID, op.PermissionRevision = "owned-fixture-confirmation", "owned-fixture-policy"
		_, err = journal.ApplyRestoreOperation(context.Background(), op)
		return err
	}
	write := func(name, text string, mode os.FileMode) error {
		return os.WriteFile(filepath.Join(root, name), []byte(text), mode)
	}
	git := func(args ...string) error {
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		return command.Run()
	}
	for _, item := range []struct{ name, content string }{
		{"tracked.txt", "base\n"}, {"deleted.txt", "remove after commit\n"}, {".gitignore", ".yhc/\n"},
	} {
		if err := write(item.name, item.content, 0o644); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"}, {"config", "user.name", "fixture"}, {"config", "user.email", "fixture.invalid"},
		{"add", "."}, {"commit", "--quiet", "-m", "owned recovery fixture"},
	} {
		if err := git(args...); err != nil {
			return err
		}
	}
	if err := write("tracked.txt", "staged change\n", 0o644); err != nil {
		return err
	}
	if err := git("add", "tracked.txt"); err != nil {
		return err
	}
	for _, item := range []struct{ name, content string; mode os.FileMode }{
		{"tracked.txt", "unstaged change\n", 0o644}, {"new.bin", "\x00binary\xffdata", 0o640},
		{"run.sh", "#!/bin/sh\nexit 0\n", 0o751}, {"effects.count", "1\n", 0o644},
	} {
		if err := write(item.name, item.content, item.mode); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(root, "empty-directory"), 0o755); err != nil {
		return err
	}
	dir := filepath.Join(root, ".yhc", "transcripts")
	id := os.Args[1]
	generated := id == "--generated-session-id"
	if generated {
		// Exercise the production default ID generator without a model or turn.
		// Staging construction and Close do not persist a second Session.
		staging := engine.NewRestoreStagingQueryEngine(engine.QueryEngineConfig{CWD: root, TranscriptDir: dir})
		id = staging.SessionID()
		staging.Close()
	}
	recorder := transcript.NewRecorder(id, dir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "owned checkpoint fixture prompt"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "fixture-write", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{"file_path":"effects.count","content":"1\n"}`}}}, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5}}},
		{Role: schema.Tool, ToolCallID: "fixture-write", Content: "committed once"},
		{Role: schema.Assistant, Content: "checkpoint complete", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}}},
	}
	if err := recorder.RecordMessages(messages); err != nil {
		return err
	}
	modelName, providerName := "deepseek-v4-flash", "agenticdeepseek"
	permissionMode := ""
	if len(os.Args) == 3 && os.Args[2] == "--unbound-model" {
		modelName, providerName = "", ""
	}
	if len(os.Args) == 3 && os.Args[2] == "--tool-ready" {
		permissionMode = "acceptEdits"
	}
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: id, ThreadID: id, CWD: root, Status: "idle", QueryKernelVersion: "project_graph/v1",
		QueryKernelStage: "full", CreatedAt: time.Now().UTC(), Model: modelName, Provider: providerName,
		PermissionMode: permissionMode,
	}); err != nil {
		return err
	}
	auxiliary := transcript.UsageSummary{}
	auxiliary.ObserveAuxiliaryMessage(&schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}})
	if err := recorder.RecordAuxiliaryUsage(auxiliary); err != nil {
		return err
	}
	if err := recorder.Close(); err != nil {
		return err
	}
	if generated {
		fmt.Println(id)
	}
	return nil
}
