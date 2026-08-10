package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemToolsUseExecutionCWD(t *testing.T) {
	processCWD, processCWDErr := os.Getwd()
	executionCWD := t.TempDir()
	if err := os.WriteFile(filepath.Join(executionCWD, "marker.txt"), []byte("context-only-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(executionCWD, "edit.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(executionCWD, "symbol.go"), []byte("package sample\n\nfunc ContextOnlySymbol() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(executionCWD, "notebook.ipynb"),
		[]byte(`{"cells":[{"cell_type":"markdown","source":["before"],"metadata":{}}],"metadata":{},"nbformat":4,"nbformat_minor":5}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	ctx := WithExecutionCWD(context.Background(), executionCWD)
	tests := []struct {
		name  string
		tool  ToolImpl
		input string
		want  string
	}{
		{name: "read", tool: ReadTool(), input: `{"file_path":"marker.txt"}`, want: "context-only-value"},
		{name: "glob", tool: GlobTool(), input: `{"pattern":"*.txt"}`, want: "marker.txt"},
		{name: "grep", tool: GrepTool(), input: `{"pattern":"context-only-value"}`, want: "marker.txt"},
		{name: "lsp", tool: LSPTool(), input: `{"action":"symbols","query":"ContextOnlySymbol"}`, want: "ContextOnlySymbol"},
		{name: "brief", tool: BriefTool(), input: `{"content":"attached","attachments":["marker.txt"]}`, want: "context-only-value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.tool.ExecuteCtx == nil {
				t.Fatalf("%s has no context-aware executor", test.name)
			}
			output, err := test.tool.ExecuteCtx(ctx, test.input)
			if err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if !strings.Contains(output, test.want) {
				t.Fatalf("%s output %q does not contain %q", test.name, output, test.want)
			}
		})
	}

	write := WriteTool()
	if _, err := write.ExecuteCtx(ctx, `{"file_path":"created.txt","content":"created in context\n"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(executionCWD, "created.txt")); err != nil || string(data) != "created in context\n" {
		t.Fatalf("write used wrong directory: data=%q err=%v", data, err)
	}

	read := ReadTool()
	if _, err := read.ExecuteCtx(ctx, `{"file_path":"edit.txt"}`); err != nil {
		t.Fatalf("read before edit: %v", err)
	}
	edit := EditTool()
	if _, err := edit.ExecuteCtx(ctx, `{"file_path":"edit.txt","old_string":"before","new_string":"after"}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(executionCWD, "edit.txt")); err != nil || string(data) != "after\n" {
		t.Fatalf("edit used wrong directory: data=%q err=%v", data, err)
	}
	notebook := NotebookEditTool()
	if _, err := notebook.ExecuteCtx(
		ctx,
		`{"notebook_path":"notebook.ipynb","command":"replace","cell_index":0,"cell_type":"markdown","source":"after"}`,
	); err != nil {
		t.Fatalf("notebook edit: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(executionCWD, "notebook.ipynb")); err != nil || !strings.Contains(string(data), "after") {
		t.Fatalf("notebook edit used wrong directory: data=%q err=%v", data, err)
	}

	if processCWDErr == nil {
		if current, err := os.Getwd(); err != nil || current != processCWD {
			t.Fatalf("tools mutated process cwd: got %q want %q err=%v", current, processCWD, err)
		}
	}
}

func TestBashToolScopesPersistentShellToExecutionCWD(t *testing.T) {
	processCWD, processCWDErr := os.Getwd()
	first := t.TempDir()
	second := t.TempDir()
	ctxFirst := WithAgentID(
		WithThreadID(WithSessionID(WithExecutionCWD(context.Background(), first), "session"), "thread"),
		"agent",
	)
	ctxSecond := WithAgentID(
		WithThreadID(WithSessionID(WithExecutionCWD(context.Background(), second), "session"), "thread"),
		"agent",
	)
	t.Cleanup(func() {
		mgr := getDefaultShellManager()
		_ = mgr.Kill(foregroundShellID(ctxFirst, first))
		_ = mgr.Kill(foregroundShellID(ctxSecond, second))
	})

	bash := BashTool()
	for _, test := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "first", ctx: ctxFirst, want: first},
		{name: "second", ctx: ctxSecond, want: second},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := bash.ExecuteCtx(test.ctx, `{"command":"pwd"}`)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, test.want) {
				t.Fatalf("output %q does not contain execution cwd %q", output, test.want)
			}
		})
	}

	if foregroundShellID(ctxFirst, first) == foregroundShellID(ctxSecond, second) {
		t.Fatal("different execution directories shared one persistent shell identity")
	}
	if processCWDErr == nil {
		if current, err := os.Getwd(); err != nil || current != processCWD {
			t.Fatalf("bash mutated process cwd: got %q want %q err=%v", current, processCWD, err)
		}
	}
}

func TestShellManagerExecuteAtStartsInRequestedDirectory(t *testing.T) {
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })
	cwd := t.TempDir()

	result, err := mgr.ExecuteAt(context.Background(), "scoped", cwd, "pwd", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(result.Stdout); got != cwd {
		t.Fatalf("pwd = %q, want %q", got, cwd)
	}
	if result.CWD != cwd {
		t.Fatalf("reported cwd = %q, want %q", result.CWD, cwd)
	}

	result, err = mgr.ExecuteAt(context.Background(), "scoped", fmt.Sprintf("%s-ignored", cwd), "pwd", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(result.Stdout); got != cwd {
		t.Fatalf("existing persistent shell was reset to %q", got)
	}
}
