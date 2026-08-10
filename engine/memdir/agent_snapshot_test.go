package memdir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgentSnapshot(t *testing.T, projectRoot, agentType, timestamp string, files map[string]string) {
	t.Helper()
	dir := GetAgentSnapshotDir(agentType, projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(agentSnapshotMetaFile{UpdatedAt: timestamp})
	if err := os.WriteFile(filepath.Join(dir, agentSnapshotMeta), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentMemorySnapshotInitializeAndTimestampDiscovery(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	agentType := "reviewer"
	firstTimestamp := "2026-07-12T01:02:03Z"
	writeAgentSnapshot(t, projectRoot, agentType, firstTimestamp, map[string]string{
		"MEMORY.md": "seed index",
		"notes.txt": "seed note",
	})

	status := CheckAgentMemorySnapshot(agentType, ScopeUser, projectRoot)
	if status.Action != AgentSnapshotInitialize || status.SnapshotTimestamp != firstTimestamp {
		t.Fatalf("initial status = %#v", status)
	}
	if err := InitializeAgentMemoryFromSnapshot(agentType, ScopeUser, projectRoot, firstTimestamp); err != nil {
		t.Fatal(err)
	}
	localDir := GetAgentMemoryDirForProject(agentType, ScopeUser, projectRoot)
	for name, want := range map[string]string{"MEMORY.md": "seed index", "notes.txt": "seed note"} {
		data, err := os.ReadFile(filepath.Join(localDir, name))
		if err != nil || string(data) != want {
			t.Fatalf("local %s = %q err=%v", name, data, err)
		}
	}
	if status := CheckAgentMemorySnapshot(agentType, ScopeUser, projectRoot); status.Action != AgentSnapshotNone {
		t.Fatalf("synced status = %#v", status)
	}

	newTimestamp := "2026-07-13T01:02:03Z"
	writeAgentSnapshot(t, projectRoot, agentType, newTimestamp, map[string]string{"MEMORY.md": "new seed"})
	status = CheckAgentMemorySnapshot(agentType, ScopeUser, projectRoot)
	if status.Action != AgentSnapshotPromptUpdate || status.SnapshotTimestamp != newTimestamp {
		t.Fatalf("newer status = %#v", status)
	}
	if err := MarkAgentMemorySnapshotSynced(agentType, ScopeUser, projectRoot, newTimestamp); err != nil {
		t.Fatal(err)
	}
	if status := CheckAgentMemorySnapshot(agentType, ScopeUser, projectRoot); status.Action != AgentSnapshotNone {
		t.Fatalf("kept status = %#v", status)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "MEMORY.md"))
	if err != nil || string(data) != "seed index" {
		t.Fatalf("keep changed memory: %q err=%v", data, err)
	}
}

func TestAgentMemorySnapshotTimestampMatrix(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	agentType := "timestamp-reviewer"
	snapshotTimestamp := "2026-07-12T12:00:00Z"
	writeAgentSnapshot(t, projectRoot, agentType, snapshotTimestamp, map[string]string{"MEMORY.md": "seed"})

	localDir := GetAgentMemoryDirForProject(agentType, ScopeUser, projectRoot)
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "MEMORY.md"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(localDir, agentSnapshotSynced)

	tests := []struct {
		name       string
		marker     string
		wantAction AgentSnapshotAction
	}{
		{name: "missing", wantAction: AgentSnapshotPromptUpdate},
		{name: "malformed", marker: `{"syncedFrom":"not-a-time"}`, wantAction: AgentSnapshotPromptUpdate},
		{name: "older", marker: `{"syncedFrom":"2026-07-12T11:59:59Z"}`, wantAction: AgentSnapshotPromptUpdate},
		{name: "equal", marker: `{"syncedFrom":"2026-07-12T12:00:00Z"}`, wantAction: AgentSnapshotNone},
		{name: "newer", marker: `{"syncedFrom":"2026-07-12T12:00:01Z"}`, wantAction: AgentSnapshotNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tt.marker != "" {
				if err := os.WriteFile(markerPath, []byte(tt.marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			status := CheckAgentMemorySnapshot(agentType, ScopeUser, projectRoot)
			if status.Action != tt.wantAction {
				t.Fatalf("status = %#v, want action %q", status, tt.wantAction)
			}
		})
	}

	if err := os.WriteFile(filepath.Join(GetAgentSnapshotDir(agentType, projectRoot), agentSnapshotMeta), []byte(`{"updatedAt":"not-a-time"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if status := CheckAgentMemorySnapshot(agentType, ScopeUser, projectRoot); status.Action != AgentSnapshotNone {
		t.Fatalf("malformed snapshot metadata status = %#v", status)
	}
}

func TestAgentMemorySnapshotReplacePreservesNonMarkdownAndNestedFiles(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	agentType := "writer"
	timestamp := "2026-07-12T04:05:06Z"
	writeAgentSnapshot(t, projectRoot, agentType, timestamp, map[string]string{
		"MEMORY.md":         "replacement",
		"config.txt":        "new config",
		"nested/ignored.md": "not copied",
	})
	localDir := GetAgentMemoryDirForProject(agentType, ScopeUser, projectRoot)
	for name, content := range map[string]string{
		"old.md":          "old",
		"keep.bin":        "keep",
		"config.txt":      "old config",
		"nested/local.md": "nested keep",
	} {
		path := filepath.Join(localDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReplaceAgentMemoryFromSnapshot(agentType, ScopeUser, projectRoot, timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "old.md")); !os.IsNotExist(err) {
		t.Fatalf("old Markdown was not removed: %v", err)
	}
	for name, want := range map[string]string{
		"MEMORY.md":       "replacement",
		"config.txt":      "new config",
		"keep.bin":        "keep",
		"nested/local.md": "nested keep",
	} {
		data, err := os.ReadFile(filepath.Join(localDir, name))
		if err != nil || string(data) != want {
			t.Fatalf("local %s = %q err=%v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(localDir, "nested", "ignored.md")); !os.IsNotExist(err) {
		t.Fatalf("nested snapshot file should not be copied: %v", err)
	}
}

func TestAgentMemorySnapshotFailureDoesNotAdvanceMarkerOrDeleteMemory(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	agentType := "blocked"
	timestamp := "2026-07-12T07:08:09Z"
	writeAgentSnapshot(t, projectRoot, agentType, timestamp, map[string]string{
		"MEMORY.md": "replacement",
		"blocked":   "cannot replace a directory",
	})
	localDir := GetAgentMemoryDirForProject(agentType, ScopeUser, projectRoot)
	if err := os.MkdirAll(filepath.Join(localDir, "blocked"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(localDir, "old.md")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceAgentMemoryFromSnapshot(agentType, ScopeUser, projectRoot, timestamp); err == nil {
		t.Fatal("replace should fail on non-regular destination")
	}
	data, err := os.ReadFile(oldPath)
	if err != nil || string(data) != "old" {
		t.Fatalf("failed replace changed old memory: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(localDir, agentSnapshotSynced)); !os.IsNotExist(err) {
		t.Fatalf("failed replace advanced marker: %v", err)
	}
}

func TestAgentMemorySnapshotMarkerFailurePreservesExistingTarget(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	localDir := GetAgentMemoryDirForProject("marker-blocked", ScopeUser, projectRoot)
	markerPath := filepath.Join(localDir, agentSnapshotSynced)
	if err := os.MkdirAll(markerPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err := MarkAgentMemorySnapshotSynced("marker-blocked", ScopeUser, projectRoot, "2026-07-12T07:08:09Z")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular marker error, got %v", err)
	}
	info, statErr := os.Stat(markerPath)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("failed marker write changed existing target: info=%v err=%v", info, statErr)
	}
}

func TestAgentMemorySnapshotRejectsInvalidMetadataAndSnapshotSymlinkEscape(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	unsafeName := "../../outside:agent"
	dir := GetAgentSnapshotDir(unsafeName, projectRoot)
	if rel, err := filepath.Rel(resolveProjectRoot(projectRoot), dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("sanitized snapshot dir escaped project: %q rel=%q err=%v", dir, rel, err)
	}
	writeAgentSnapshot(t, projectRoot, unsafeName, "not-a-time", map[string]string{"MEMORY.md": "seed"})
	if status := CheckAgentMemorySnapshot(unsafeName, ScopeUser, projectRoot); status.Action != AgentSnapshotNone {
		t.Fatalf("invalid metadata status = %#v", status)
	}

	escapedProject := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(escapedProject, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(escapedProject, ".claude", agentSnapshotBaseDir)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeAgentSnapshot(t, escapedProject, "reviewer", "2026-07-12T01:02:03Z", map[string]string{"MEMORY.md": "outside"})
	if status := CheckAgentMemorySnapshot("reviewer", ScopeUser, escapedProject); status.Action != AgentSnapshotNone {
		t.Fatalf("symlink escape status = %#v", status)
	}
	if err := InitializeAgentMemoryFromSnapshot("reviewer", ScopeUser, escapedProject, "2026-07-12T01:02:03Z"); err == nil {
		t.Fatal("symlink escape initialization should fail")
	}
}
