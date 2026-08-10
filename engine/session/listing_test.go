package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionAPIDefaultsUseYHCProjectTranscriptRoot(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_TRANSCRIPT_DIR", "")
	t.Chdir(project)

	want := filepath.Join(project, ".yhc", "transcripts")
	if got := GetSessionDir(project); got != want {
		t.Fatalf("project session directory = %q, want %q", got, want)
	}
	if got := GetSessionDir(""); got != want {
		t.Fatalf("current-project session directory = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects")); !os.IsNotExist(err) {
		t.Fatalf("implicit Claude session root was touched: %v", err)
	}

	explicit := filepath.Join(t.TempDir(), "explicit-transcripts")
	t.Setenv("CLAUDE_TRANSCRIPT_DIR", explicit)
	if got := GetSessionDir(""); got != explicit {
		t.Fatalf("explicit compatibility directory = %q, want %q", got, explicit)
	}
	if got := GetSessionDir(project); got != want {
		t.Fatalf("project-scoped directory used compatibility override: %q", got)
	}
}

func TestListSessionsIncludesRuntimeGeneratedSessionIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-12345.jsonl")
	line := `{"timestamp":"2026-06-09T00:00:00Z","kind":"user","message":{"role":"user","content":"debug the TUI"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	sessions, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) != 1 || sessions[0].SessionID != "session-12345" {
		t.Fatalf("runtime session ID was not listed: %#v", sessions)
	}
	if sessions[0].Summary != "debug the TUI" {
		t.Fatalf("summary = %q", sessions[0].Summary)
	}
}
