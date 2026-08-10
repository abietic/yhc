package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPluginConfigStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	cfg := &PluginConfig{
		PluginName: "test-plugin",
		Values:     map[string]any{"apiKey": "sk-123", "debug": true},
	}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("test-plugin")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.PluginName != "test-plugin" {
		t.Errorf("plugin name: got %q", loaded.PluginName)
	}
	if loaded.Values["apiKey"] != "sk-123" {
		t.Errorf("apiKey: got %v", loaded.Values["apiKey"])
	}
	if loaded.Values["debug"] != true {
		t.Errorf("debug: got %v", loaded.Values["debug"])
	}
}

func TestPluginConfigStore_LoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	cfg, err := store.Load("nonexistent")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Values) != 0 {
		t.Errorf("expected empty values, got %v", cfg.Values)
	}
}

func TestPluginConfigStore_SetOption(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	if err := store.SetOption("my-plugin", "key", "value"); err != nil {
		t.Fatalf("SetOption: %v", err)
	}

	cfg, _ := store.Load("my-plugin")
	if cfg.Values["key"] != "value" {
		t.Errorf("key: got %v", cfg.Values["key"])
	}
}

func TestPluginConfigStore_GetOption_WithDefault(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	schema := []PluginOption{
		{Name: "port", Type: "number", Default: float64(8080)},
		{Name: "host", Type: "string", Default: "localhost"},
	}

	got := store.GetOption("my-plugin", "port", schema)
	if got != float64(8080) {
		t.Errorf("port default: got %v", got)
	}

	store.SetOption("my-plugin", "port", float64(9090))
	got = store.GetOption("my-plugin", "port", schema)
	if got != float64(9090) {
		t.Errorf("port after set: got %v", got)
	}
}

func TestPluginConfigStore_ResolveAll(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	schema := []PluginOption{
		{Name: "a", Type: "string", Default: "default-a"},
		{Name: "b", Type: "string", Default: "default-b"},
		{Name: "c", Type: "number", Default: float64(42)},
	}

	store.SetOption("p", "b", "custom-b")

	resolved := store.ResolveAll("p", schema)
	if resolved["a"] != "default-a" {
		t.Errorf("a: got %v", resolved["a"])
	}
	if resolved["b"] != "custom-b" {
		t.Errorf("b: got %v", resolved["b"])
	}
	if resolved["c"] != float64(42) {
		t.Errorf("c: got %v", resolved["c"])
	}
}

func TestPluginConfigStore_Delete(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	store.SetOption("del-me", "key", "val")
	if err := store.Delete("del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	cfg, _ := store.Load("del-me")
	if len(cfg.Values) != 0 {
		t.Errorf("expected empty after delete, got %v", cfg.Values)
	}
}

func TestValidateOption(t *testing.T) {
	tests := []struct {
		name    string
		opt     PluginOption
		value   any
		wantErr bool
	}{
		{"string valid", PluginOption{Name: "s", Type: "string"}, "hello", false},
		{"string invalid", PluginOption{Name: "s", Type: "string"}, 123, true},
		{"boolean valid", PluginOption{Name: "b", Type: "boolean"}, true, false},
		{"boolean invalid", PluginOption{Name: "b", Type: "boolean"}, "yes", true},
		{"number valid", PluginOption{Name: "n", Type: "number"}, float64(1.5), false},
		{"number invalid", PluginOption{Name: "n", Type: "number"}, "1.5", true},
		{"select valid", PluginOption{Name: "s", Type: "select", Choices: []string{"a", "b"}}, "a", false},
		{"select invalid", PluginOption{Name: "s", Type: "select", Choices: []string{"a", "b"}}, "c", true},
		{"required nil", PluginOption{Name: "r", Type: "string", Required: true}, nil, true},
		{"optional nil", PluginOption{Name: "o", Type: "string"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOption(tt.opt, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateOption(%v, %v) error = %v, wantErr %v", tt.opt, tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestSubstituteVars(t *testing.T) {
	resolved := map[string]any{
		"api_key": "sk-secret",
		"port":    8080,
		"debug":   true,
	}

	input := "curl -H 'Authorization: ${PLUGIN_CONFIG:api_key}' http://localhost:${PLUGIN_CONFIG:port}"
	got := SubstituteVars(input, resolved)
	want := "curl -H 'Authorization: sk-secret' http://localhost:8080"
	if got != want {
		t.Errorf("SubstituteVars:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestPluginConfigStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewPluginConfigStore(dir)

	store.SetOption("perms-test", "key", "val")

	path := filepath.Join(dir, "perms-test.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("file permissions: got %o want %o", perm, 0o600)
	}
}
