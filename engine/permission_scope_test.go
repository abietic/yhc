package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/permission"
)

func TestDefaultModeAllowsWorkingDirectoryReadsAndSearches(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "main.go")
	if err := os.WriteFile(file, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
		return
	}

	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return false, "prompted"
		},
	})
	ctx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}

	tests := []struct {
		tool  string
		input map[string]any
	}{
		{tool: "Read", input: map[string]any{"file_path": file}},
		{tool: "Grep", input: map[string]any{"pattern": "main", "path": cwd}},
		{tool: "Glob", input: map[string]any{"pattern": "*.go"}},
	}
	for _, tt := range tests {
		allowed, reason := eng.wrappedCanUseTool(context.Background(), tt.tool, tt.input, ctx)
		if !allowed || reason != "" {
			t.Fatalf("%s should be allowed in cwd, allowed=%v reason=%q", tt.tool, allowed, reason)
		}
	}
	if prompted {
		t.Fatal("working-directory read/search should not prompt")
	}
}

func TestDefaultModePromptsForReadOutsideWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
		return
	}

	prompts := 0
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompts++
			return false, "user denied"
		},
	})
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Read", map[string]any{"file_path": outside}, nil)
	if allowed || prompts != 1 {
		t.Fatalf("outside read should prompt once, allowed=%v prompts=%d", allowed, prompts)
	}
}

func TestDefaultModeRejectsSymlinkEscapeFromWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	link := filepath.Join(cwd, "secret.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
		return
	}

	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return false, "user denied"
		},
	})
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Read", map[string]any{"file_path": link}, nil)
	if allowed || !prompted {
		t.Fatal("symlink escaping cwd must require permission")
	}
}

func TestResolvedDenyRuleBlocksSymlinkTarget(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), `secret.txt`)
	if err := os.WriteFile(outside, []byte(`secret`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, `secret.txt`)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(cwd, `.claude`)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := "{\"permissions\":{\"deny\":[\"Read(" + outside + ")\"]}}"
	if err := os.WriteFile(filepath.Join(settingsDir, `settings.json`), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return true, ``
		},
	})
	allowed, _ := eng.wrappedCanUseTool(context.Background(), `Read`, map[string]any{`file_path`: link}, nil)
	if allowed || prompted {
		t.Fatal(`deny rule on a resolved target must block without prompting`)
	}
}

func TestResolvedAskRulePromptsForSymlinkTarget(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "secret.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := "{\"permissions\":{\"ask\":[\"Read(" + outside + ")\"]}}"
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	prompts := 0
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompts++
			return false, "user denied"
		},
	})
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Read", map[string]any{"file_path": link}, nil)
	if allowed || prompts != 1 {
		t.Fatalf("resolved ask rule allowed=%v prompts=%d", allowed, prompts)
	}
}

func TestAdditionalWorkingDirectoryAllowsRead(t *testing.T) {
	cwd := t.TempDir()
	additional := t.TempDir()
	file := filepath.Join(additional, `main.go`)
	if err := os.WriteFile(file, []byte(`package main`), 0o600); err != nil {
		t.Fatal(err)
	}
	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:            cwd,
		AdditionalDirs: []string{additional},
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return false, `prompted`
		},
	})
	allowed, reason := eng.wrappedCanUseTool(context.Background(), `Read`, map[string]any{`file_path`: file}, nil)
	if !allowed || reason != `` || prompted {
		t.Fatalf(`additional-directory read allowed=%v reason=%q prompted=%v`, allowed, reason, prompted)
	}
}

func TestAcceptEditsPromptsForCreateThroughSymlinkParent(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(cwd, `generated`)
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatal(err)
	}
	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return false, `user denied`
		},
	})
	toolCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, _ := eng.wrappedCanUseTool(context.Background(), `Write`, map[string]any{
		`file_path`: filepath.Join(linkDir, `new.txt`),
		`content`:   `unsafe`,
	}, toolCtx)
	if allowed || !prompted {
		t.Fatal(`acceptEdits must prompt for a create through an escaping symlink parent`)
	}
}

func TestAcceptEditsPromptsForEveryBashInvocation(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(cwd, `link-to-outside`)
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatal(err)
	}

	prompts := 0
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompts++
			return false, `user denied`
		},
	})
	toolCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	containedEdits := []struct {
		tool  string
		input map[string]any
	}{
		{tool: `Write`, input: map[string]any{`file_path`: filepath.Join(cwd, `generated`, `new.txt`), `content`: `safe`}},
		{tool: `Edit`, input: map[string]any{`file_path`: filepath.Join(cwd, `generated`, `existing.txt`), `old_string`: `old`, `new_string`: `new`}},
	}
	for _, edit := range containedEdits {
		allowed, _ := eng.wrappedCanUseTool(context.Background(), edit.tool, edit.input, toolCtx)
		if !allowed {
			t.Fatalf(`contained %s must auto-allow`, edit.tool)
		}
	}
	if prompts != 0 {
		t.Fatalf(`contained Write/Edit prompted %d times`, prompts)
	}

	commands := []string{
		`mkdir ` + filepath.Join(cwd, `generated`),
		`cp ` + filepath.Join(cwd, `input`) + ` ` + filepath.Join(outside, `copy`),
		`touch ` + filepath.Join(cwd, `generated`) + ` && curl https://example.com`,
		`rm -rf $(pwd)/generated`,
		`env mv ` + filepath.Join(cwd, `a`) + ` ` + filepath.Join(cwd, `b`),
		`sed -i 's/a/b/' ` + filepath.Join(cwd, `input`) + ` > ` + filepath.Join(outside, `output`),
		`rmdir ` + linkDir,
		`mv ` + filepath.Join(cwd, `input`) + ` /etc/passwd`,
	}
	for _, command := range commands {
		allowed, _ := eng.wrappedCanUseTool(context.Background(), `Bash`, map[string]any{`command`: command}, toolCtx)
		if allowed {
			t.Fatalf(`Bash command %q must prompt rather than auto-allow`, command)
		}
	}
	if prompts != len(commands) {
		t.Fatalf(`prompts=%d, want %d`, prompts, len(commands))
	}
}

func TestExplicitAskRuleOverridesWorkingDirectoryRead(t *testing.T) {
	cwd := t.TempDir()
	settingsDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"permissions":{"ask":["Read"]}}`), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	file := filepath.Join(cwd, "main.go")
	if err := os.WriteFile(file, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
		return
	}

	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return true, ""
		},
	})
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Read", map[string]any{"file_path": file}, nil)
	if !allowed || !prompted {
		t.Fatal("explicit ask rule must prompt before cwd default")
	}
}

func TestExplicitDenyRuleBlocksWorkingDirectoryRead(t *testing.T) {
	cwd := t.TempDir()
	settingsDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"permissions":{"deny":["Read"]}}`), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	file := filepath.Join(cwd, "main.go")
	if err := os.WriteFile(file, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
		return
	}

	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompted = true
			return true, ""
		},
	})
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Read", map[string]any{"file_path": file}, nil)
	if allowed || prompted {
		t.Fatal("explicit deny rule must block without prompting")
	}
}

func TestSessionApprovalIsParameterScoped(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(),
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			return false, "prompted"
		},
	})
	approved := map[string]any{"command": "go test ./..."}
	if err := eng.ApproveForSession("Bash", approved); err != nil {
		t.Fatal(err)
		return
	}

	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Bash", approved, nil)
	if !allowed {
		t.Fatal("approved exact command should be allowed")
	}
	allowed, _ = eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "go test ./... -count=1"}, nil)
	if allowed {
		t.Fatal("different command parameters must not inherit session approval")
	}
}

func TestSessionFileApprovalDoesNotApproveOtherFiles(t *testing.T) {
	cwd := t.TempDir()
	first := filepath.Join(cwd, "first.txt")
	second := filepath.Join(cwd, "second.txt")
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			return false, "prompted"
		},
	})
	if err := eng.ApproveForSession("Write", map[string]any{"file_path": first, "content": "one"}); err != nil {
		t.Fatal(err)
		return
	}
	if allowed, _ := eng.wrappedCanUseTool(context.Background(), "Write", map[string]any{"file_path": first, "content": "changed"}, nil); !allowed {
		t.Fatal("same file path should retain session approval")
	}
	if allowed, _ := eng.wrappedCanUseTool(context.Background(), "Write", map[string]any{"file_path": second, "content": "two"}, nil); allowed {
		t.Fatal("different file path must not inherit session approval")
	}
}
