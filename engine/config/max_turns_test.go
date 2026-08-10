package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitZeroMaxTurnsOverridesFiniteUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_MAX_TURNS", "")
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"max_turns": 17}`), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(`{"max_turns": 0}`), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	settings, err := LoadSettings(project)
	if err != nil {
		t.Fatal(err)
		return
	}
	if settings.MaxTurns != 0 {
		t.Fatalf("MaxTurns = %d, want unlimited (0)", settings.MaxTurns)
	}
}

func TestMaxTurnsEnvironmentSupportsZeroAndRejectsNegative(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_MAX_TURNS", "0")
	settings, err := LoadSettings("")
	if err != nil {
		t.Fatal(err)
		return
	}
	if settings.MaxTurns != 0 {
		t.Fatalf("MaxTurns = %d, want unlimited (0)", settings.MaxTurns)
	}

	t.Setenv("CLAUDE_MAX_TURNS", "-1")
	if _, err = LoadSettings(""); err == nil {
		t.Fatal("negative environment max turns was accepted")
		return
	}
}
