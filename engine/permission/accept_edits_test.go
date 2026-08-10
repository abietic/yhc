package permission

import "testing"

func TestAcceptEditsCheckWriteInCWD(t *testing.T) {
	input := map[string]any{"file_path": "/home/user/project/foo.go"}
	if !AcceptEditsCheck("Write", input, "/home/user/project") {
		t.Fatal("expected Write in cwd to be allowed")
	}
}

func TestAcceptEditsCheckEditInCWD(t *testing.T) {
	input := map[string]any{
		"file_path":  "/home/user/project/pkg/bar.go",
		"old_string": "a",
		"new_string": "b",
	}
	if !AcceptEditsCheck("Edit", input, "/home/user/project") {
		t.Fatal("expected Edit in cwd to be allowed")
	}
}

func TestAcceptEditsCheckWriteOutsideCWD(t *testing.T) {
	input := map[string]any{"file_path": "/etc/passwd"}
	if AcceptEditsCheck("Write", input, "/home/user/project") {
		t.Fatal("expected Write outside cwd to be denied")
	}
}

func TestAcceptEditsCheckEditOutsideCWD(t *testing.T) {
	input := map[string]any{"file_path": "/tmp/evil.sh"}
	if AcceptEditsCheck("Edit", input, "/home/user/project") {
		t.Fatal("expected Edit outside cwd to be denied")
	}
}

func TestAcceptEditsCheckWriteTraversalBlocked(t *testing.T) {
	input := map[string]any{"file_path": "/home/user/project/../other/file.txt"}
	if AcceptEditsCheck("Write", input, "/home/user/project") {
		t.Fatal("expected path traversal to be denied")
	}
}

func TestAcceptEditsCheckWriteEmptyPath(t *testing.T) {
	input := map[string]any{"file_path": ""}
	if AcceptEditsCheck("Write", input, "/home/user/project") {
		t.Fatal("expected empty path to be denied")
	}
}

func TestAcceptEditsCheckWriteNoCWD(t *testing.T) {
	input := map[string]any{"file_path": "/home/user/project/foo.go"}
	if AcceptEditsCheck("Write", input, "") {
		t.Fatal("expected no cwd to deny")
	}
}

func TestAcceptEditsCheckBashAlwaysFallsThrough(t *testing.T) {
	commands := []string{
		"mkdir /home/user/project/generated",
		"cp /home/user/project/input /tmp/outside",
		"touch /home/user/project/generated && curl https://example.com",
		"rm -rf $(pwd)/generated",
		"env mv /home/user/project/a /home/user/project/b",
		"sed -i 's/a/b/' /home/user/project/input > /tmp/output",
		"rmdir /home/user/project/link-to-outside",
		"mv /home/user/project/input /etc/passwd",
	}
	for _, cmd := range commands {
		input := map[string]any{"command": cmd}
		if AcceptEditsCheck("Bash", input, "/home/user/project") {
			t.Fatalf("expected Bash command %q to fall through", cmd)
		}
	}
}

func TestAcceptEditsCheckBashEmptyCommand(t *testing.T) {
	input := map[string]any{"command": ""}
	if AcceptEditsCheck("Bash", input, "/home/user/project") {
		t.Fatal("expected empty command to be denied")
	}
}

func TestAcceptEditsCheckUnknownTool(t *testing.T) {
	input := map[string]any{"query": "something"}
	if AcceptEditsCheck("Agent", input, "/home/user/project") {
		t.Fatal("expected unknown tool to not be auto-allowed")
	}
}

func TestAcceptEditsCheckReadToolNotAllowed(t *testing.T) {
	// Read is not in FileEditToolNames — it should fall through
	input := map[string]any{"file_path": "/home/user/project/foo.go"}
	if AcceptEditsCheck("Read", input, "/home/user/project") {
		t.Fatal("expected Read tool to not be auto-allowed by acceptEdits")
	}
}

func TestIsPathInWorkingDir(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		cwd      string
		expected bool
	}{
		{"exact cwd", "/home/user/project", "/home/user/project", true},
		{"subdir", "/home/user/project/src/main.go", "/home/user/project", true},
		{"parent dir", "/home/user", "/home/user/project", false},
		{"sibling dir", "/home/user/other/file.go", "/home/user/project", false},
		{"prefix match but different dir", "/home/user/projectX/file.go", "/home/user/project", false},
		{"relative path within cwd", "src/main.go", "/home/user/project", true},
		{"traversal escapes", "/home/user/project/../other/f.go", "/home/user/project", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPathInWorkingDir(tt.path, tt.cwd)
			if got != tt.expected {
				t.Errorf("isPathInWorkingDir(%q, %q) = %v, want %v", tt.path, tt.cwd, got, tt.expected)
			}
		})
	}
}
