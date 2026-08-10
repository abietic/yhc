package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/worktree"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type p181WorktreeModel struct {
	calls atomic.Int32
}

func (m *p181WorktreeModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	m.calls.Add(1)
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "child complete",
	}, nil
}

func (m *p181WorktreeModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.calls.Add(1)
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{
			Role:    schema.Assistant,
			Content: "child complete",
		}, nil)
	}()
	return reader, nil
}

func TestP181AgentUsesEngineWorktreeServiceAndParentCWD(t *testing.T) {
	root := t.TempDir()
	runEngineWorktreeGit(t, root, "init", "-b", "master")
	runEngineWorktreeGit(t, root, "config", "user.email", "test@example.com")
	runEngineWorktreeGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(
		filepath.Join(root, "tracked.txt"),
		[]byte("base\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runEngineWorktreeGit(t, root, "add", "tracked.txt")
	runEngineWorktreeGit(t, root, "commit", "-m", "base")
	if err := os.WriteFile(
		filepath.Join(root, "local-only.txt"),
		[]byte("omitted\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	runner := tools.NewAgentRunner(2)
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	runner.SetOutputDir(outputDir)
	chatModel := &p181WorktreeModel{}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:             "p181-parent",
		ThreadID:              "p181-parent-thread",
		CWD:                   root,
		MemoryProjectRoot:     root,
		PermissionProjectRoot: root,
		TranscriptDir:         filepath.Join(t.TempDir(), "parent-transcripts"),
		ChatModel:             chatModel,
		ToolRegistry:          registry,
		AgentRunner:           runner,
	})
	defer engine.Close()
	if engine.subagentExecutor == nil ||
		engine.subagentExecutor.WorktreeService != engine.worktreeLifecycle {
		t.Fatal("sub-agent executor is not bound to the engine worktree service")
	}
	ctx := tools.WithAgentRunner(context.Background(), runner)
	ctx = tools.WithAgentExecutor(ctx, engine.subagentExecutor)
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	_, err = tools.RunAgent(ctx, runner, tools.AgentExecOptions{
		AgentID:         "p181-dirty-default",
		ParentSessionID: "p181-parent",
		ParentThreadID:  "p181-parent-thread",
		Task:            "must fail before model",
		Description:     "dirty default",
		Isolation:       "worktree",
	})
	if err == nil || !strings.Contains(err.Error(), "ignore_dirty") {
		t.Fatalf("dirty default error = %v", err)
	}
	if chatModel.calls.Load() != 0 {
		t.Fatalf("dirty default reached model %d times", chatModel.calls.Load())
	}

	result, err := tools.RunAgent(ctx, runner, tools.AgentExecOptions{
		AgentID:            "p181-ignore-dirty",
		ParentSessionID:    "p181-parent",
		ParentThreadID:     "p181-parent-thread",
		Task:               "run from committed HEAD",
		Description:        "ignore dirty",
		Isolation:          "worktree",
		WorktreeSourceMode: worktree.SourceIgnoreDirty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Worktree == nil ||
		result.Worktree.State != worktree.StateRemoved ||
		result.Worktree.SourceDirtyReport == nil ||
		!result.Worktree.SourceDirtyReport.Dirty {
		t.Fatalf("worktree result = %#v", result.Worktree)
	}
	if len(result.Worktree.SourceDirtyReport.ChangedFiles) != 1 ||
		result.Worktree.SourceDirtyReport.ChangedFiles[0] != "local-only.txt" {
		t.Fatalf(
			"source dirty report = %#v",
			result.Worktree.SourceDirtyReport,
		)
	}
	record, found, err := engine.worktreeLifecycle.Get(
		t.Context(),
		result.Worktree.RecordID,
	)
	if err != nil || !found || record.State != worktree.StateRemoved {
		t.Fatalf("durable record = %#v, found=%v, err=%v", record, found, err)
	}
	if _, err := os.Stat(record.Path); !os.IsNotExist(err) {
		t.Fatalf("clean worktree path still exists: %v", err)
	}
	afterCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if afterCWD != processCWD || engine.GetCWD() != root {
		t.Fatalf(
			"cwd drift: process %q -> %q, engine=%q",
			processCWD,
			afterCWD,
			engine.GetCWD(),
		)
	}
	snapshot, ok := runner.GetAgentSnapshot("p181-ignore-dirty")
	if !ok ||
		snapshot.Options.CWD != record.Path ||
		snapshot.WorktreePath != "" ||
		snapshot.Worktree == nil ||
		snapshot.Worktree.RecordID != record.ID {
		t.Fatalf("Agent metadata = %#v, found=%v", snapshot, ok)
	}
	loaded := loadProjectGraphSession(
		t,
		filepath.Join(outputDir, "transcripts"),
		"p181-ignore-dirty",
	)
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
		metadata.QueryKernelStage !=
			string(queryKernelStageForegroundChild) {
		t.Fatalf("worktree child kernel metadata = %#v", metadata)
	}
}
