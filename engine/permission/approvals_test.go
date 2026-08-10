package permission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/internal/statepath"
)

func TestApprovalTrackerIgnoresUnscopedApproval(t *testing.T) {
	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{ToolName: "Bash"}, "user", true)
	if tracker.Count() != 0 {
		t.Fatal("tool-wide approval must not be recorded")
	}
}

func TestSessionApprovalsAreNotPersisted(t *testing.T) {
	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true}, "user", true)
	path := filepath.Join(t.TempDir(), ".yhc", "approvals.json")
	if err := tracker.SaveTo(path); err != nil {
		t.Fatal(err)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
		return
	}
	if string(data) != "[]" {
		t.Fatalf("session approval leaked to disk: %s", data)
	}
	loaded := NewApprovalTracker()
	if err := loaded.LoadFrom(path); err != nil || loaded.Count() != 0 {
		t.Fatalf("empty approval store did not round trip: count=%d err=%v", loaded.Count(), err)
	}
}

func TestApprovalTrackerLoadFromIsStrictAndAtomic(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".eino-agent", "approvals.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"tool_name":"Bash","command_pattern":"go test ./...","exact_command":true,"approved_at":"2026-08-10T01:02:03Z","reason":"user","session_scoped":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{ToolName: "Bash", CommandPattern: "go vet ./...", ExactCommand: true}, "user", false)
	if err := tracker.LoadFrom(path); err == nil {
		t.Fatal("LoadFrom accepted an unknown session field")
	}
	if tracker.Count() != 1 || !tracker.IsApprovedInvocation("Bash", "go vet ./...", "", "") {
		t.Fatalf("tracker mutated after rejected load: %#v", tracker.List())
	}
}

func TestApprovalTrackerSaveToUsesPrivateModes(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".eino-agent", "approvals.json")
	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true}, "user", false)
	if err := tracker.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(path)
	if err != nil || file.Mode().Perm() != 0o600 {
		t.Fatalf("approval file mode = %v err=%v", infoMode(file), err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("approval dir mode = %v err=%v", infoMode(dir), err)
	}
}

func TestApprovalTrackerSaveToRejectsUnreadablePersistentEntry(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".yhc", "approvals.json")
	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{
		ToolName:      "Read",
		PathPattern:   filepath.Join(t.TempDir(), "outside"),
		RecursivePath: true,
	}, "user", false)
	if err := tracker.SaveTo(path); err == nil {
		t.Fatal("SaveTo persisted an approval that its strict loader would reject")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected approval store exists: %v", err)
	}
}

func TestApprovalStorePathUsesCanonicalProjectRoot(t *testing.T) {
	project := t.TempDir()
	path, err := ApprovalStorePath(project)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := statepath.ProjectRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(roots.Canonical, "approvals.json"); path != want {
		t.Fatalf("ApprovalStorePath() = %q, want %q", path, want)
	}
}

func TestApprovalTrackerSaveToRejectsSymlinkCanonicalRoot(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, ".yhc")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true}, "user", false)
	path, err := ApprovalStorePath(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.SaveTo(path); err == nil {
		t.Fatal("SaveTo accepted a symlink canonical root")
	}
	if _, err := os.Lstat(filepath.Join(outside, "approvals.json")); !os.IsNotExist(err) {
		t.Fatalf("approval escaped canonical root: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(outside, "approvals.json"),
		[]byte(`[{"tool_name":"Bash","command_pattern":"go test ./...","exact_command":true,"approved_at":"2026-08-10T01:02:03Z","reason":"user"}]`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	loaded := NewApprovalTracker()
	if err := loaded.LoadFrom(path); err == nil || loaded.Count() != 0 {
		t.Fatalf("LoadFrom followed a symlink canonical root: count=%d err=%v", loaded.Count(), err)
	}
}

func TestApprovalTrackerApproveAndSaveRollsBackOnFailure(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, ".yhc")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	path, err := ApprovalStorePath(project)
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewApprovalTracker()
	key := ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true}
	if err := tracker.ApproveAndSave(key, "user", path); err == nil {
		t.Fatal("ApproveAndSave accepted an invalid canonical root")
	}
	if tracker.Count() != 0 || tracker.IsApprovedInvocation("Bash", "go test ./...", "", "") {
		t.Fatalf("failed persistence retained authority: %#v", tracker.List())
	}
}

func TestExactAndRecursiveApprovalMatching(t *testing.T) {
	tracker := NewApprovalTracker()
	tracker.Approve(ApprovalKey{ToolName: "Write", PathPattern: "/tmp/a.txt", ExactPath: true}, "user", true)
	tracker.Approve(ApprovalKey{ToolName: "Read", PathPattern: "/tmp/project", RecursivePath: true}, "user", true)

	if !tracker.IsApprovedInvocation("Write", "", "/tmp/a.txt", "") {
		t.Fatal("exact path should match")
	}
	if tracker.IsApprovedInvocation("Write", "", "/tmp/other.txt", "") {
		t.Fatal("different exact path should not match")
	}
	if !tracker.IsApprovedInvocation("Read", "", "/tmp/project/sub/file.go", "") {
		t.Fatal("recursive path should match child")
	}
}

func TestSessionApprovalsRequireMatchingRootSession(t *testing.T) {
	tracker := NewApprovalTracker()
	sessionKey := ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true}
	tracker.ApproveForRootSession(sessionKey, "user", "root-a")

	if !tracker.IsApprovedInvocationForRootSession("Bash", "go test ./...", "", "", "root-a") {
		t.Fatal("matching root session did not see its approval")
	}
	if tracker.IsApprovedInvocationForRootSession("Bash", "go test ./...", "", "", "root-b") {
		t.Fatal("session approval crossed root-session lineage")
	}
	if tracker.IsApprovedInvocation("Bash", "go test ./...", "", "") {
		t.Fatal("lineage-scoped approval leaked through the legacy unscoped lookup")
	}

	persistentKey := ApprovalKey{ToolName: "Bash", CommandPattern: "go vet ./...", ExactCommand: true}
	tracker.Approve(persistentKey, "user", false)
	for _, root := range []string{"root-a", "root-b"} {
		if !tracker.IsApprovedInvocationForRootSession("Bash", "go vet ./...", "", "", root) {
			t.Fatalf("persistent approval was hidden from %s", root)
		}
	}
}

func TestApprovalKeyMatchesInvocation(t *testing.T) {
	tests := []struct {
		name             string
		key              ApprovalKey
		toolName         string
		command          string
		path             string
		inputFingerprint string
		want             bool
	}{
		{
			name:     "exact command positive",
			key:      ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true},
			toolName: "Bash",
			command:  "go test ./...",
			want:     true,
		},
		{
			name:     "exact command near-miss with extra args",
			key:      ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true},
			toolName: "Bash",
			command:  "go test ./... -v",
			want:     false,
		},
		{
			name:     "recursive path positive child file",
			key:      ApprovalKey{ToolName: "Read", PathPattern: "/tmp/project", RecursivePath: true},
			toolName: "Read",
			path:     "/tmp/project/sub/file.go",
			want:     true,
		},
		{
			name:     "recursive path sibling-prefix near-miss",
			key:      ApprovalKey{ToolName: "Read", PathPattern: "/tmp/project", RecursivePath: true},
			toolName: "Read",
			path:     "/tmp/project-sibling/file.go",
			want:     false,
		},
		{
			name:     "exact path positive",
			key:      ApprovalKey{ToolName: "Write", PathPattern: "/tmp/a.txt", ExactPath: true},
			toolName: "Write",
			path:     "/tmp/a.txt",
			want:     true,
		},
		{
			name:     "exact path canonical positive",
			key:      ApprovalKey{ToolName: "Write", PathPattern: "/tmp/a.txt", ExactPath: true},
			toolName: "Write",
			path:     "/tmp//foo/../a.txt",
			want:     true,
		},
		{
			name:     "exact path near-miss",
			key:      ApprovalKey{ToolName: "Write", PathPattern: "/tmp/a.txt", ExactPath: true},
			toolName: "Write",
			path:     "/tmp/b.txt",
			want:     false,
		},
		{
			name:             "input fingerprint positive",
			key:              ApprovalKey{ToolName: "Edit", InputFingerprint: "fp-abc"},
			toolName:         "Edit",
			inputFingerprint: "fp-abc",
			want:             true,
		},
		{
			name:             "input fingerprint near-miss",
			key:              ApprovalKey{ToolName: "Edit", InputFingerprint: "fp-abc"},
			toolName:         "Edit",
			inputFingerprint: "fp-abd",
			want:             false,
		},
		{
			name:     "tool-name mismatch",
			key:      ApprovalKey{ToolName: "Bash", CommandPattern: "go test ./...", ExactCommand: true},
			toolName: "Read",
			command:  "go test ./...",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.key.MatchesInvocation(tt.toolName, tt.command, tt.path, tt.inputFingerprint)
			if got != tt.want {
				t.Fatalf("MatchesInvocation() = %v, want %v", got, tt.want)
			}
		})
	}
}
