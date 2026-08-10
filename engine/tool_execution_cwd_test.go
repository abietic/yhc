package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestToolExecutorInjectsEngineExecutionCWD(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "CaptureExecutionCWD"},
		ExecuteCtx: func(ctx context.Context, _ string) (string, error) {
			return tools.ExecutionCWDFromCtx(ctx), nil
		},
	})
	engine := &QueryEngine{
		config:       QueryEngineConfig{CWD: "/engine-owned-cwd"},
		toolRegistry: registry,
	}

	result, err := engine.toolExecutor(context.Background(), "CaptureExecutionCWD", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if result != "/engine-owned-cwd" {
		t.Fatalf("execution cwd = %q, want engine config cwd", result)
	}
}

func TestQueryEngineOwnsAndClosesScopedShells(t *testing.T) {
	cwd := t.TempDir()
	registry := tools.NewRegistry()
	registry.Register(tools.BashTool())
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "shell-owner",
		CWD:           cwd,
		TranscriptDir: t.TempDir(),
		ToolRegistry:  registry,
	})

	result, err := engine.toolExecutor(context.Background(), "Bash", `{"command":"pwd"}`)
	if err != nil {
		engine.Close()
		t.Fatal(err)
	}
	if !strings.Contains(result, cwd) {
		engine.Close()
		t.Fatalf("bash result %q does not contain engine cwd %q", result, cwd)
	}
	if engine.shellManager == nil || len(engine.shellManager.ListShells()) != 1 {
		engine.Close()
		t.Fatalf("engine shell manager = %#v", engine.shellManager)
	}

	engine.Close()
	if len(engine.shellManager.ListShells()) != 0 {
		t.Fatalf("engine close retained shells: %#v", engine.shellManager.ListShells())
	}
}
