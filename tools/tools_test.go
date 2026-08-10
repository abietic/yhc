package tools

import (
	"context"
	"os"
	"testing"
)

func TestReadTool(t *testing.T) {
	f, err := os.CreateTemp("", "eino-agent-test-read-*.txt")
	if err != nil {
		t.Fatal(err)
		return
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, _ = f.WriteString("line 1\nline 2\nline 3\n")
	_ = f.Close()

	read := ReadTool()
	if read.Execute == nil {
		t.Fatal("ReadTool has no Execute function")
		return
	}

	result, err := read.Execute(`{"file_path": "` + f.Name() + `"}`)
	if err != nil {
		t.Fatalf("ReadTool failed: %v", err)
		return
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestWriteTool(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.txt"

	write := WriteTool()
	if write.Execute == nil {
		t.Fatal("WriteTool has no Execute function")
		return
	}

	result, err := write.Execute(`{"file_path": "` + path + `", "content": "hello world"}`)
	if err != nil {
		t.Fatalf("WriteTool failed: %v", err)
		return
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
}

func TestEditTool(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.txt"
	_ = os.WriteFile(path, []byte("hello world"), 0o644)

	// Register the file as read (required by file-read guard).
	RecordFileRead(path, false)

	edit := EditTool()
	if edit.Execute == nil {
		t.Fatal("EditTool has no Execute function")
		return
	}

	result, err := edit.Execute(`{"file_path": "` + path + `", "old_string": "world", "new_string": "golang"}`)
	if err != nil {
		t.Fatalf("EditTool failed: %v", err)
		return
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello golang" {
		t.Errorf("expected 'hello golang', got %q", string(data))
	}
}

func TestEditToolDuplicateFails(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.txt"
	_ = os.WriteFile(path, []byte("hello world world"), 0o644)

	// Register the file as read (required by file-read guard).
	RecordFileRead(path, false)

	edit := EditTool()
	_, err := edit.Execute(`{"file_path": "` + path + `", "old_string": "world", "new_string": "x"}`)
	if err == nil {
		t.Error("expected error for duplicate old_string")
	}
}

func TestBashTool(t *testing.T) {
	bash := BashTool()
	if bash.Execute == nil && bash.ExecuteCtx == nil {
		t.Fatal("BashTool has no Execute function")
		return
	}

	var result string
	var err error
	if bash.ExecuteCtx != nil {
		result, err = bash.ExecuteCtx(context.Background(), `{"command": "echo hello"}`)
	} else {
		result, err = bash.Execute(`{"command": "echo hello"}`)
	}
	if err != nil {
		t.Fatalf("BashTool failed: %v", err)
		return
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestRegisterDefaults(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	names := []string{"Read", "Write", "Bash", "Edit", "Agent", "Task"}
	noExec := map[string]bool{"Agent": true, "Task": true}
	for _, name := range names {
		impl, ok := reg.Get(name)
		if !ok {
			t.Errorf("tool %q not found in registry", name)
			continue
		}
		if impl.Execute == nil && impl.ExecuteCtx == nil && !noExec[name] {
			t.Errorf("tool %q has no Execute function", name)
		}
	}
}
