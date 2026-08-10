package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConfigReloader_Start_LoadsInitialConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a valid config file.
	configDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	cfg := &Config{
		Model:    "gpt-4o",
		MaxTurns: 30,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")

	reloader := NewConfigReloader(tmpDir, 100*time.Millisecond)
	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}
	defer reloader.Stop()

	settings := reloader.CurrentSettings()
	if settings == nil {
		t.Fatal("expected non-nil settings after Start()")
		return
	}
	if settings.Model != "gpt-4o" {
		t.Errorf("expected model=gpt-4o, got %q", settings.Model)
	}
	if settings.MaxTurns != 30 {
		t.Errorf("expected max_turns=30, got %d", settings.MaxTurns)
	}
}

func TestConfigReloader_DetectsFileChange(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	configPath := filepath.Join(configDir, "settings.json")

	// Write initial config.
	cfg1 := &Config{Model: "gpt-4o", MaxTurns: 30}
	data1, _ := json.Marshal(cfg1)
	if err := os.WriteFile(configPath, data1, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")

	var mu sync.Mutex
	var events []ConfigChangeEvent

	reloader := NewConfigReloader(tmpDir, 50*time.Millisecond)
	reloader.OnChange(func(event ConfigChangeEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}
	defer reloader.Stop()

	// Wait a moment, then change the config.
	time.Sleep(100 * time.Millisecond)

	cfg2 := &Config{Model: "gpt-4o", MaxTurns: 100}
	data2, _ := json.Marshal(cfg2)
	if err := os.WriteFile(configPath, data2, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Wait for the change to be detected.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	eventCount := len(events)
	mu.Unlock()

	if eventCount == 0 {
		t.Error("expected at least one change event")
		return
	}

	mu.Lock()
	lastEvent := events[eventCount-1]
	mu.Unlock()

	if lastEvent.Source != "file_change" {
		t.Errorf("expected source=file_change, got %q", lastEvent.Source)
	}
	if lastEvent.NewSettings.MaxTurns != 100 {
		t.Errorf("expected new max_turns=100, got %d", lastEvent.NewSettings.MaxTurns)
	}

	// Verify current settings were updated.
	current := reloader.CurrentSettings()
	if current.MaxTurns != 100 {
		t.Errorf("expected current max_turns=100, got %d", current.MaxTurns)
	}
}

func TestConfigReloader_InvalidConfigKeepsPrevious(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	configPath := filepath.Join(configDir, "settings.json")

	// Write valid config.
	cfg := &Config{Model: "gpt-4o", MaxTurns: 30}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")

	reloader := NewConfigReloader(tmpDir, 50*time.Millisecond)
	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}
	defer reloader.Stop()

	// Verify initial settings.
	initial := reloader.CurrentSettings()
	if initial.MaxTurns != 30 {
		t.Fatalf("expected initial max_turns=30, got %d", initial.MaxTurns)
	}

	// Write invalid config (negative max_turns triggers validation error).
	time.Sleep(100 * time.Millisecond)
	invalidCfg := &Config{Model: "gpt-4o", MaxTurns: -1}
	invalidData, _ := json.Marshal(invalidCfg)
	if err := os.WriteFile(configPath, invalidData, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Wait for detection.
	time.Sleep(200 * time.Millisecond)

	// Current settings should still be the valid ones (max_turns=30).
	current := reloader.CurrentSettings()
	if current.MaxTurns != 30 {
		t.Errorf("expected current max_turns=30 (kept previous), got %d", current.MaxTurns)
	}
}

func TestConfigReloader_ExplicitReload(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	configPath := filepath.Join(configDir, "settings.json")
	cfg := &Config{Model: "gpt-4o", MaxTurns: 50}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")

	reloader := NewConfigReloader(tmpDir, 1*time.Hour) // Long interval — won't auto-detect.
	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}
	defer reloader.Stop()

	// Change config.
	cfg.MaxTurns = 75
	data, _ = json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Explicitly reload.
	newSettings, vr, err := reloader.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error: %v", err)
		return
	}
	if vr.HasErrors() {
		t.Fatalf("unexpected validation errors: %s", vr.String())
	}
	if newSettings.MaxTurns != 75 {
		t.Errorf("expected max_turns=75 after reload, got %d", newSettings.MaxTurns)
	}

	// Verify current settings updated.
	if reloader.CurrentSettings().MaxTurns != 75 {
		t.Errorf("expected current max_turns=75, got %d", reloader.CurrentSettings().MaxTurns)
	}
}

func TestConfigReloader_ExplicitReload_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	configPath := filepath.Join(configDir, "settings.json")
	cfg := &Config{Model: "gpt-4o", MaxTurns: 50}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")

	reloader := NewConfigReloader(tmpDir, 1*time.Hour)
	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}
	defer reloader.Stop()

	// Write invalid config.
	invalidCfg := &Config{Model: "gpt-4o", MaxTurns: -5}
	invalidData, _ := json.Marshal(invalidCfg)
	if err := os.WriteFile(configPath, invalidData, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Explicit reload should return previous settings when new config has errors.
	settings, vr, err := reloader.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error: %v", err)
		return
	}
	if !vr.HasErrors() {
		t.Error("expected validation errors for invalid config")
	}
	// Should return previous valid settings.
	if settings.MaxTurns != 50 {
		t.Errorf("expected previous max_turns=50, got %d", settings.MaxTurns)
	}
}

func TestConfigReloader_MultipleListeners(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	configPath := filepath.Join(configDir, "settings.json")
	cfg := &Config{Model: "gpt-4o", MaxTurns: 50}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")

	var mu sync.Mutex
	var called1, called2 bool

	reloader := NewConfigReloader(tmpDir, 1*time.Hour)
	reloader.OnChange(func(event ConfigChangeEvent) {
		mu.Lock()
		called1 = true
		mu.Unlock()
	})
	reloader.OnChange(func(event ConfigChangeEvent) {
		mu.Lock()
		called2 = true
		mu.Unlock()
	})

	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}
	defer reloader.Stop()

	// Trigger explicit reload.
	_, _, _ = reloader.ReloadConfig()

	mu.Lock()
	if !called1 {
		t.Error("listener 1 was not called")
	}
	if !called2 {
		t.Error("listener 2 was not called")
	}
	mu.Unlock()
}

func TestConfigReloader_StopIsIdempotent(t *testing.T) {
	reloader := NewConfigReloader("", 1*time.Hour)
	if err := reloader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
		return
	}

	// Stop multiple times should not panic.
	reloader.Stop()
	reloader.Stop()
}
