package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/memdir"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type memoryPromptCaptureModel struct {
	mu       sync.Mutex
	messages []*schema.Message
}

func (m *memoryPromptCaptureModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *memoryPromptCaptureModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.messages = append([]*schema.Message(nil), messages...)
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryEngineInjectsPrivateAndTeamIndexesIntoModelInput(t *testing.T) {
	projectRoot := t.TempDir()
	privateDir := filepath.Join(t.TempDir(), "private")
	teamDir := filepath.Join(t.TempDir(), "team")
	t.Setenv("YHC_MEMORY_PATH_OVERRIDE", privateDir)
	t.Setenv("YHC_TEAM_MEMORY_DIR", teamDir)
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	for path, content := range map[string]string{
		filepath.Join(privateDir, "MEMORY.md"): "private-model-index",
		filepath.Join(teamDir, "MEMORY.md"):    "team-model-index",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	chatModel := &memoryPromptCaptureModel{}
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                    projectRoot,
		MemoryProjectRoot:      projectRoot,
		EnablePersistentMemory: true,
		TranscriptDir:          t.TempDir(),
		CustomSystemPrompt:     "base prompt",
		ChatModel:              chatModel,
	})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(context.Background(), "hello")
	for range events {
	}

	chatModel.mu.Lock()
	messages := append([]*schema.Message(nil), chatModel.messages...)
	chatModel.mu.Unlock()
	if len(messages) == 0 || messages[0] == nil || messages[0].Role != schema.System {
		t.Fatalf("model input missing system prompt: %#v", messages)
	}
	content := messages[0].Content
	privateAt := strings.Index(content, "private-model-index")
	teamAt := strings.Index(content, "team-model-index")
	if privateAt < 0 || teamAt < 0 || privateAt >= teamAt {
		t.Fatalf("model prompt did not preserve index order: %q", content)
	}
	if !strings.Contains(content, `<team-memory-content source="shared">`) {
		t.Fatalf("model prompt missing shared marker: %q", content)
	}
}

func TestSubagentExecutorInheritsPersistentMemoryRoot(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                    t.TempDir(),
		MemoryProjectRoot:      root,
		EnablePersistentMemory: true,
		ChatModel:              &memoryPromptCaptureModel{},
		ToolRegistry:           registry,
	})
	t.Cleanup(eng.Close)
	if eng.subagentExecutor == nil {
		t.Fatal("subagent executor not configured")
	}
	if eng.subagentExecutor.MemoryProjectRoot != root || !eng.subagentExecutor.EnablePersistentMemory {
		t.Fatalf("subagent memory scope = root %q enabled %v", eng.subagentExecutor.MemoryProjectRoot, eng.subagentExecutor.EnablePersistentMemory)
	}
}

func TestPersistentMemoryPathsUseEngineOwnedPermissionFastPath(t *testing.T) {
	root := t.TempDir()
	configDir := t.TempDir()
	teamDir := filepath.Join(t.TempDir(), "team")
	t.Setenv("YHC_CONFIG_DIR", configDir)
	t.Setenv("YHC_MEMORY_PATH_OVERRIDE", "")
	t.Setenv("YHC_TEAM_MEMORY_DIR", teamDir)
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	prompts := 0
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                    root,
		MemoryProjectRoot:      root,
		EnablePersistentMemory: true,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompts++
			return false, "prompted"
		},
	})
	t.Cleanup(eng.Close)
	privatePath := memdir.GetAutoMemPathForProject(root)
	teamPath := filepath.Join(teamDir, "topic.md")
	if !memdir.IsSafeTeamMemPath(teamPath) {
		t.Fatal("configured team path should be filesystem-safe")
	}
	_, scopedTeamPath, _ := permissionInvocation(root, "Write", map[string]any{"file_path": teamPath})
	if !eng.memoryPathAllowed(scopedTeamPath) {
		t.Fatalf("engine rejected resolved team path %q", scopedTeamPath)
	}
	for _, call := range []struct {
		tool  string
		input map[string]any
	}{
		{tool: "Read", input: map[string]any{"file_path": filepath.Join(privatePath, "anything.md")}},
		{tool: "Write", input: map[string]any{"file_path": teamPath, "content": "team memory"}},
	} {
		allowed, reason := eng.wrappedCanUseTool(context.Background(), call.tool, call.input, &ToolUseContext{})
		if !allowed || reason != "" {
			t.Fatalf("%s memory path allowed=%v reason=%q", call.tool, allowed, reason)
		}
	}
	if prompts != 0 {
		t.Fatalf("memory fast paths prompted %d times", prompts)
	}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Write", map[string]any{
		"file_path": teamPath,
		"content":   "team memory",
	}, &ToolUseContext{
		Options: &ToolUseOptions{PermissionMode: permission.ModePlan},
	})
	if allowed ||
		!strings.Contains(reason, "exact session plan file") ||
		prompts != 0 {
		t.Fatalf("plan-mode memory write allowed=%v reason=%q prompts=%d", allowed, reason, prompts)
	}
}
