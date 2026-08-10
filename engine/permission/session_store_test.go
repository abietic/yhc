package permission

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreAddAndLookup(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{
		ToolName:  "Bash",
		Action:    ActionAllow,
		Scope:     ScopeProject,
		CreatedAt: time.Now(),
	})

	d, found := store.Lookup("Bash", map[string]any{"command": "ls"})
	if !found {
		t.Fatal("expected to find decision for Bash")
	}
	if d.Action != ActionAllow {
		t.Fatalf("expected ActionAllow, got %q", d.Action)
	}
}

func TestSessionStoreLookupWithInputPattern(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{
		ToolName:     "Bash",
		InputPattern: "git*",
		Action:       ActionAllow,
		Scope:        ScopeProject,
		CreatedAt:    time.Now(),
	})

	// "git push" should match "git*"
	_, found := store.Lookup("Bash", map[string]any{"command": "git push"})
	if !found {
		t.Fatal("expected to find decision for git push")
	}

	// "rm -rf" should not match "git*"
	_, found = store.Lookup("Bash", map[string]any{"command": "rm -rf /"})
	if found {
		t.Fatal("expected NOT to find decision for rm")
	}
}

func TestSessionStoreProjectScopeBeatsGlobal(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{
		ToolName:  "Bash",
		Action:    ActionDeny,
		Scope:     ScopeGlobal,
		CreatedAt: time.Now(),
	})
	store.Add(SessionDecision{
		ToolName:  "Bash",
		Action:    ActionAllow,
		Scope:     ScopeProject,
		CreatedAt: time.Now(),
	})

	d, found := store.Lookup("Bash", map[string]any{"command": "ls"})
	if !found {
		t.Fatal("expected to find decision")
	}
	// Project scope should win over global.
	if d.Action != ActionAllow {
		t.Fatalf("expected project-scope allow to win, got %q", d.Action)
	}
}

func TestSessionStoreRemove(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{
		ToolName:  "Read",
		Action:    ActionAllow,
		Scope:     ScopeProject,
		CreatedAt: time.Now(),
	})

	removed := store.Remove("Read", "", ScopeProject)
	if !removed {
		t.Fatal("expected Remove to return true")
	}

	_, found := store.Lookup("Read", map[string]any{"file_path": "/tmp/foo"})
	if found {
		t.Fatal("expected decision to be removed")
	}
}

func TestSessionStoreClearAll(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{ToolName: "Read", Action: ActionAllow, Scope: ScopeProject, CreatedAt: time.Now()})
	store.Add(SessionDecision{ToolName: "Write", Action: ActionDeny, Scope: ScopeGlobal, CreatedAt: time.Now()})

	store.Clear("")
	list := store.List()
	if len(list) != 0 {
		t.Fatalf("expected empty after Clear(\"\"), got %d decisions", len(list))
	}
}

func TestSessionStoreClearByScope(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{ToolName: "Read", Action: ActionAllow, Scope: ScopeProject, CreatedAt: time.Now()})
	store.Add(SessionDecision{ToolName: "Write", Action: ActionDeny, Scope: ScopeGlobal, CreatedAt: time.Now()})

	store.Clear(ScopeProject)
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 decision after clearing project scope, got %d", len(list))
	}
	if list[0].ToolName != "Write" {
		t.Fatalf("expected Write to survive, got %q", list[0].ToolName)
	}
}

func TestSessionStoreUpdateExisting(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{ToolName: "Bash", Action: ActionDeny, Scope: ScopeProject, CreatedAt: time.Now()})
	// Add same tool+scope again with different action — should update.
	store.Add(SessionDecision{ToolName: "Bash", Action: ActionAllow, Scope: ScopeProject, CreatedAt: time.Now()})

	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 decision after update, got %d", len(list))
	}
	if list[0].Action != ActionAllow {
		t.Fatalf("expected updated action to be allow, got %q", list[0].Action)
	}
}

func TestSessionStoreToRules(t *testing.T) {
	store := NewSessionStore()

	store.Add(SessionDecision{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Scope: ScopeProject, CreatedAt: time.Now()})
	store.Add(SessionDecision{ToolName: "Read", Action: ActionAllow, Scope: ScopeGlobal, CreatedAt: time.Now()})

	rules := store.ToRules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	// Check sources map correctly.
	for _, r := range rules {
		switch r.ToolName {
		case "Bash":
			if r.Source != SourceLocal {
				t.Fatalf("expected project decision → SourceLocal, got %q", r.Source)
			}
			if r.InputPattern != "git*" {
				t.Fatalf("expected inputPattern=git*, got %q", r.InputPattern)
			}
		case "Read":
			if r.Source != SourceUser {
				t.Fatalf("expected global decision → SourceUser, got %q", r.Source)
			}
		}
	}
}

func TestSessionStorePersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.json")

	// Create and save.
	store := NewSessionStore()
	store.Add(SessionDecision{
		ToolName:     "Bash",
		InputPattern: "npm*",
		Action:       ActionAllow,
		Scope:        ScopeProject,
		CreatedAt:    time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Reason:       "user approved",
	})
	store.Add(SessionDecision{
		ToolName:  "Read",
		Action:    ActionAllow,
		Scope:     ScopeGlobal,
		CreatedAt: time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
	})

	if err := store.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
		return
	}

	// Load into fresh store.
	store2 := NewSessionStore()
	if err := store2.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom: %v", err)
		return
	}

	list := store2.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 decisions after load, got %d", len(list))
	}

	// Verify lookup works.
	d, found := store2.Lookup("Bash", map[string]any{"command": "npm install"})
	if !found {
		t.Fatal("expected to find Bash npm decision after load")
	}
	if d.Action != ActionAllow {
		t.Fatalf("expected allow, got %q", d.Action)
	}
	if d.Reason != "user approved" {
		t.Fatalf("expected reason preserved, got %q", d.Reason)
	}
}

func TestSessionStoreLoadFromMissingFile(t *testing.T) {
	store := NewSessionStore()
	err := store.LoadFrom("/nonexistent/path/decisions.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
		return
	}
	if len(store.List()) != 0 {
		t.Fatal("expected empty store for missing file")
	}
}

func TestSessionStoreLoadFromCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.json")

	// Write corrupt JSON.
	_ = os.WriteFile(path, []byte("{invalid json"), 0o644)

	store := NewSessionStore()
	err := store.LoadFrom(path)
	if err != nil {
		t.Fatalf("expected nil error for corrupt file (lenient), got: %v", err)
		return
	}
	if len(store.List()) != 0 {
		t.Fatal("expected empty store for corrupt file")
	}
}

func TestSessionStoreLoadFromEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.json")

	_ = os.WriteFile(path, []byte(""), 0o644)

	store := NewSessionStore()
	err := store.LoadFrom(path)
	if err != nil {
		t.Fatalf("expected nil error for empty file, got: %v", err)
		return
	}
}

func TestSessionStoreLoadSkipsInvalidDecisions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.json")

	// Write valid JSON with some invalid decisions.
	content := `{
  "version": 1,
  "decisions": [
    {"tool_name": "", "action": "allow", "scope": "project", "created_at": "2024-01-01T00:00:00Z"},
    {"tool_name": "Read", "action": "invalid", "scope": "project", "created_at": "2024-01-01T00:00:00Z"},
    {"tool_name": "Write", "action": "deny", "scope": "global", "created_at": "2024-01-01T00:00:00Z"}
  ]
}`
	_ = os.WriteFile(path, []byte(content), 0o644)

	store := NewSessionStore()
	if err := store.LoadFrom(path); err != nil {
		t.Fatalf("LoadFrom: %v", err)
		return
	}

	// Only the valid Write decision should survive.
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 valid decision, got %d", len(list))
	}
	if list[0].ToolName != "Write" {
		t.Fatalf("expected Write, got %q", list[0].ToolName)
	}
}

func TestSessionStoreNoMatchReturnsNotFound(t *testing.T) {
	store := NewSessionStore()
	store.Add(SessionDecision{ToolName: "Write", Action: ActionDeny, Scope: ScopeProject, CreatedAt: time.Now()})

	_, found := store.Lookup("Read", map[string]any{"file_path": "/tmp/foo"})
	if found {
		t.Fatal("expected no match for Read against Write decision")
	}
}
