package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigToolCreation(t *testing.T) {
	tool := ConfigTool()
	if tool.Info.Name != "Config" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestConfigValidateInput(t *testing.T) {
	tool := ConfigTool()

	if err := tool.ValidateInput(map[string]any{"setting": "model"}); err != nil {
		t.Errorf("valid setting should pass: %v", err)
	}
	if err := tool.ValidateInput(map[string]any{"setting": ""}); err == nil {
		t.Error("empty setting should fail")
	}
	if err := tool.ValidateInput(map[string]any{}); err == nil {
		t.Error("missing setting should fail")
	}
}

func TestConfigGetSet(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".yhc")
	_ = os.MkdirAll(configDir, 0o700)
	t.Chdir(dir)
	useDefaultConfigEnv(t)

	tool := ConfigTool()

	// Set a value
	input := `{"setting":"model","value":"gpt-4o"}`
	result, err := tool.Execute(input)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "gpt-4o") {
		t.Errorf("set result should contain value, got %q", result)
	}

	// Get the value back
	input = `{"setting":"model"}`
	result, err = tool.Execute(input)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "gpt-4o") {
		t.Errorf("get result should contain value, got %q", result)
	}
}

func TestConfigNestedKey(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".yhc")
	_ = os.MkdirAll(configDir, 0o700)
	t.Chdir(dir)
	useDefaultConfigEnv(t)

	tool := ConfigTool()

	input := `{"setting":"permissions.defaultMode","value":"acceptEdits"}`
	_, err := tool.Execute(input)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Verify nested structure
	data, _ := os.ReadFile(filepath.Join(configDir, "settings.json"))
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)

	perms, ok := raw["permissions"].(map[string]any)
	if !ok {
		t.Fatal("permissions key should be a map")
	}
	if perms["defaultMode"] != "acceptEdits" {
		t.Errorf("defaultMode = %v", perms["defaultMode"])
	}
}

func TestConfigValidationOptions(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".yhc")
	_ = os.MkdirAll(configDir, 0o700)
	t.Chdir(dir)
	useDefaultConfigEnv(t)

	tool := ConfigTool()

	// Invalid theme option
	input := `{"setting":"theme","value":"neon"}`
	_, err := tool.Execute(input)
	if err == nil {
		t.Error("invalid theme should fail")
	}
	if !strings.Contains(err.Error(), "invalid value") {
		t.Errorf("error should mention invalid value: %v", err)
	}

	// Valid theme option
	input = `{"setting":"theme","value":"dark"}`
	_, err = tool.Execute(input)
	if err != nil {
		t.Errorf("valid theme should succeed: %v", err)
	}
}

func TestConfigBooleanValidation(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".yhc")
	_ = os.MkdirAll(configDir, 0o700)
	t.Chdir(dir)
	useDefaultConfigEnv(t)

	tool := ConfigTool()

	// Invalid boolean
	input := `{"setting":"memory.enabled","value":"yes"}`
	_, err := tool.Execute(input)
	if err == nil {
		t.Error("invalid boolean should fail")
	}

	// Valid boolean
	input = `{"setting":"memory.enabled","value":"true"}`
	_, err = tool.Execute(input)
	if err != nil {
		t.Errorf("valid boolean should succeed: %v", err)
	}
}

func TestConfigList(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".yhc")
	_ = os.MkdirAll(configDir, 0o700)
	_ = os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(`{"model":"test"}`), 0o600)
	t.Chdir(dir)
	useDefaultConfigEnv(t)

	tool := ConfigTool()

	// Legacy list action
	input := `{"action":"list","setting":"_"}`
	result, err := tool.Execute(input)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "model") {
		t.Errorf("list should show settings, got %q", result)
	}
}

func TestConfigWritesUserCanonicalWhenProjectStateIsAbsent(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Chdir(project)
	t.Setenv("HOME", home)
	useDefaultConfigEnv(t)

	tool := ConfigTool()
	if _, err := tool.Execute(`{"setting":"model","value":"gpt-4o"}`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".yhc", "settings.json")
	if data, err := os.ReadFile(target); err != nil || !strings.Contains(string(data), "gpt-4o") {
		t.Fatalf("canonical user settings = %q, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".eino-agent")); !os.IsNotExist(err) {
		t.Fatalf("legacy user state was created: %v", err)
	}
	assertPrivateMode(t, filepath.Dir(target), 0o700)
	assertPrivateMode(t, target, 0o600)
}

func TestConfigExplicitOverrideRemainsExact(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	canonicalOverride := filepath.Join(t.TempDir(), "canonical-config")
	legacyOverride := filepath.Join(t.TempDir(), "legacy-config")
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("YHC_CONFIG_DIR", canonicalOverride)
	t.Setenv("EINO_AGENT_CONFIG_DIR", legacyOverride)

	tool := ConfigTool()
	if _, err := tool.Execute(`{"setting":"theme","value":"dark"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(canonicalOverride, "settings.json")); err != nil {
		t.Fatalf("canonical override was not used: %v", err)
	}
	for _, path := range []string{
		filepath.Join(legacyOverride, "settings.json"),
		filepath.Join(project, ".yhc", "settings.json"),
		filepath.Join(home, ".yhc", "settings.json"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected settings path %s: %v", path, err)
		}
	}
}

func TestConfigPinnedStoreRejectsRootAndTargetReplacement(t *testing.T) {
	t.Run("root replacement", func(t *testing.T) {
		parent := t.TempDir()
		configDir := filepath.Join(parent, ".yhc")
		if err := os.Mkdir(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		store, exists, err := openConfigStore(
			configDirResolution{path: configDir, canonicalDefault: true},
			false,
		)
		if err != nil || !exists {
			t.Fatalf("openConfigStore() exists=%t err=%v", exists, err)
		}
		defer store.Close() //nolint:errcheck
		detached := configDir + "-detached"
		if err := os.Rename(configDir, detached); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := setConfigValueInStore(store, "model", "private-model"); err == nil {
			t.Fatal("root replacement was accepted")
		}
		for _, path := range []string{
			filepath.Join(configDir, "settings.json"),
			filepath.Join(detached, "settings.json"),
		} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("root replacement wrote %s: %v", path, err)
			}
		}
	})

	t.Run("target symlink", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), ".yhc")
		if err := os.Mkdir(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		store, exists, err := openConfigStore(
			configDirResolution{path: configDir, canonicalDefault: true},
			false,
		)
		if err != nil || !exists {
			t.Fatalf("openConfigStore() exists=%t err=%v", exists, err)
		}
		defer store.Close() //nolint:errcheck
		outside := filepath.Join(t.TempDir(), "outside.json")
		sentinel := []byte("outside-unchanged")
		if err := os.WriteFile(outside, sentinel, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(configDir, "settings.json")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := setConfigValueInStore(store, "model", "private-model"); err == nil {
			t.Fatal("target symlink was accepted")
		}
		got, err := os.ReadFile(outside)
		if err != nil || !bytes.Equal(got, sentinel) {
			t.Fatalf("outside target = %q, %v", got, err)
		}
	})
}

func useDefaultConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("YHC_CONFIG_DIR", "")
	t.Setenv("EINO_AGENT_CONFIG_DIR", "")
}
