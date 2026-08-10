package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestCopyIsTUIOnlyCapabilityGatedIntent(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	ctx := &CommandContext{Messages: []*schema.Message{{Role: schema.Assistant, Content: "committed result"}}}

	unavailable, err := registry.Dispatch(context.Background(), EntrypointTUI, ctx, "/copy")
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.Availability != AvailabilityUnavailable || unavailable.Action != ActionNone {
		t.Fatalf("copy without terminal capability = %#v", unavailable)
	}
	ctx.Extra = map[string]any{"terminal_clipboard": true}
	result, err := registry.Dispatch(context.Background(), EntrypointTUI, ctx, "/copy")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ActionCopy || result.Data["text"] != "committed result" || result.Output != "" {
		t.Fatalf("copy intent = %#v", result)
	}
	for _, entrypoint := range []Entrypoint{EntrypointPlain, EntrypointHeadless, EntrypointACP} {
		if registry.GetFor(entrypoint, "copy") != nil {
			t.Fatalf("copy leaked into %s discovery", entrypoint)
		}
	}
}

func TestFilesCanonicalizesWithinActiveRootsAndOmitsOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	insidePath := filepath.Join(root, "pkg", "file.go")
	outsidePath := filepath.Join(outside, "secret.txt")
	ctx := &CommandContext{
		CWD:                root,
		WorkingDirectories: []string{root},
		Messages: []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"pkg/file.go"}`}},
			{Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"` + outsidePath + `"}`}},
		}}},
	}
	result, err := executeFiles(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, insidePath) || strings.Contains(result.Output, outsidePath) ||
		!strings.Contains(result.Output, "outside the active workspace roots") {
		t.Fatalf("canonical file projection = %q", result.Output)
	}
}

func TestMemoryStatusAndScopedMutationHaveNoHandlerSideEffects(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, "AGENTS.md")
	content := []byte("# Project rules\n")
	if err := os.WriteFile(agentsPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := executeMemory(&CommandContext{CWD: root}, "status")
	if err != nil {
		t.Fatal(err)
	}
	if status.Action != ActionNone || !strings.Contains(status.Output, "AGENTS.md") {
		t.Fatalf("memory status = %#v", status)
	}

	edit, err := executeMemory(&CommandContext{CWD: root}, "edit project")
	if err != nil {
		t.Fatal(err)
	}
	if edit.Action != ActionPrompt || !strings.Contains(edit.Output, "ordinary Read") {
		t.Fatalf("memory edit workflow = %#v", edit)
	}
	got, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("memory handler mutated AGENTS.md: %q", got)
	}

	unscoped, err := executeMemory(&CommandContext{CWD: root}, "delete ../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if unscoped.Action != ActionNone || !strings.Contains(unscoped.Output, "explicit project scope") {
		t.Fatalf("unscoped memory mutation = %#v", unscoped)
	}
}

func TestTerminalProjectionNamesAreAbsentOutsideTUI(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	for _, name := range []string{"keybindings", "terminal", "search", "copy"} {
		for _, entrypoint := range []Entrypoint{EntrypointPlain, EntrypointHeadless, EntrypointACP} {
			if registry.GetFor(entrypoint, name) != nil {
				t.Fatalf("/%s leaked into %s", name, entrypoint)
			}
		}
	}
	if registry.Get("terminal-setup") != nil {
		t.Fatal("removed /terminal-setup remains registered")
	}
}
