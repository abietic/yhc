// Owned offline fixture. No provider is constructed or called.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/schema"

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
	if err != nil || (len(os.Args) != 2 && (len(os.Args) != 3 || os.Args[2] != "--unbound-model")) {
		return fmt.Errorf("fixture arguments")
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
	if len(os.Args) == 3 {
		modelName, providerName = "", ""
	}
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: id, ThreadID: id, CWD: root, Status: "idle", QueryKernelVersion: "project_graph/v1",
		QueryKernelStage: "full", CreatedAt: time.Now().UTC(), Model: modelName, Provider: providerName,
	}); err != nil {
		return err
	}
	auxiliary := transcript.UsageSummary{}
	auxiliary.ObserveAuxiliaryMessage(&schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}})
	if err := recorder.RecordAuxiliaryUsage(auxiliary); err != nil {
		return err
	}
	return recorder.Close()
}
