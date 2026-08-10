package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerInstallLocalLoadsCapabilitiesAndUninstall(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source plugin")
	writePluginManifest(t, source, `{
		"name": "Local Plugin",
		"version": "1.0.0",
		"description": "local install",
		"skills": [{"name": "local-skill", "description": "Skill", "filePath": "skills/local.md"}],
		"commands": [{"name": "local-command", "description": "Command"}],
		"hooks": [{"event": "PostToolUse", "type": "command"}],
		"mcpServers": [{"name": "local-mcp", "command": "local-mcp-server"}]
	}`)
	if err := os.MkdirAll(filepath.Join(source, "skills"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "local.md"), []byte("# Local skill"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	installDir := t.TempDir()
	manager := NewManager(installDir)
	result, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatalf("InstallLocal failed: %v", err)
		return
	}
	if result.Name != "Local Plugin" || result.Version != "1.0.0" || result.Replaced {
		t.Fatalf("unexpected install result: %#v", result)
	}
	if !isPathWithin(result.TargetDir, installDir) {
		t.Fatalf("target %q should be under install dir %q", result.TargetDir, installDir)
	}
	if _, err := os.Stat(filepath.Join(result.TargetDir, "skills", "local.md")); err != nil {
		t.Fatalf("expected nested skill file copied: %v", err)
		return
	}

	plugin, ok := manager.Loader().Get("local plugin")
	if !ok {
		t.Fatal("expected installed plugin to be loadable")
	}
	if len(plugin.Skills) != 1 || plugin.Skills[0].Name != "local-skill" {
		t.Fatalf("unexpected plugin skills: %#v", plugin.Skills)
	}
	if len(plugin.Commands) != 1 || plugin.Commands[0].Name != "local-command" {
		t.Fatalf("unexpected plugin commands: %#v", plugin.Commands)
	}
	if len(plugin.Hooks) != 1 || plugin.Hooks[0].Event != "PostToolUse" {
		t.Fatalf("unexpected plugin hooks: %#v", plugin.Hooks)
	}
	if len(plugin.MCPServers) != 1 || plugin.MCPServers[0].Command != "local-mcp-server" {
		t.Fatalf("unexpected plugin MCP servers: %#v", plugin.MCPServers)
	}

	uninstalled, err := manager.Uninstall("LOCAL PLUGIN")
	if err != nil {
		t.Fatalf("Uninstall failed: %v", err)
		return
	}
	if uninstalled.Name != "Local Plugin" || !uninstalled.Removed {
		t.Fatalf("unexpected uninstall result: %#v", uninstalled)
	}
	if _, err := os.Stat(result.TargetDir); !os.IsNotExist(err) {
		t.Fatalf("expected target dir removed, stat err=%v", err)
	}
	if _, ok := manager.Loader().Get("local plugin"); ok {
		t.Fatal("plugin should not remain loaded after uninstall")
	}
}

func TestManagerInstallLocalReplacesExistingManagedPlugin(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	writePluginManifest(t, source, `{"name":"ReplaceMe","version":"1.0.0"}`)

	manager := NewManager(t.TempDir())
	first, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatalf("first install failed: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(first.TargetDir, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	writePluginManifest(t, source, `{"name":"ReplaceMe","version":"2.0.0"}`)
	second, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatalf("second install failed: %v", err)
		return
	}
	if !second.Replaced {
		t.Fatalf("expected replacement result, got %#v", second)
	}
	plugin, _ := manager.Loader().Get("replaceme")
	if plugin.Version != "2.0.0" {
		t.Fatalf("expected replacement version loaded, got %#v", plugin)
	}
	if _, err := os.Stat(filepath.Join(second.TargetDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected stale file removed during replacement, err=%v", err)
	}
}

func TestManagerReconcileLocalRefreshesFromRecordedSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	writePluginManifest(t, source, `{"name":"RefreshMe","version":"1.0.0"}`)
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	manager := NewManager(t.TempDir())
	installed, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatalf("install failed: %v", err)
		return
	}
	sourceData, err := os.ReadFile(filepath.Join(installed.TargetDir, managedSourceFileName))
	if err != nil {
		t.Fatalf("expected managed source metadata: %v", err)
		return
	}
	if strings.TrimSpace(string(sourceData)) != source {
		t.Fatalf("unexpected managed source metadata: %q", string(sourceData))
	}

	writePluginManifest(t, source, `{"name":"RefreshMe","version":"2.0.0"}`)
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	reconciled, err := manager.ReconcileLocal("refreshme")
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
		return
	}
	if !reconciled.Updated || reconciled.Name != "RefreshMe" || reconciled.SourceDir != source {
		t.Fatalf("unexpected reconcile result: %#v", reconciled)
	}
	plugin, ok := manager.Loader().Get("refreshme")
	if !ok || plugin.Version != "2.0.0" {
		t.Fatalf("expected refreshed plugin version, got %#v ok=%v", plugin, ok)
	}
	payload, err := os.ReadFile(filepath.Join(reconciled.TargetDir, "payload.txt"))
	if err != nil {
		t.Fatalf("read payload: %v", err)
		return
	}
	if string(payload) != "v2" {
		t.Fatalf("expected refreshed payload, got %q", string(payload))
	}
}

func TestManagerReconcileLocalRequiresManagedSourceMetadata(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	writePluginManifest(t, source, `{"name":"NoSource","version":"1.0.0"}`)

	manager := NewManager(t.TempDir())
	installed, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatalf("install failed: %v", err)
		return
	}
	if err := os.Remove(filepath.Join(installed.TargetDir, managedSourceFileName)); err != nil {
		t.Fatal(err)
		return
	}
	if _, err := manager.ReconcileLocal("nosource"); err == nil || !strings.Contains(err.Error(), "managed source metadata missing") {
		t.Fatalf("expected missing source metadata error, got %v", err)
		return
	}
}

func TestManagerInstallLocalValidationAndConfigurationErrors(t *testing.T) {
	source := t.TempDir()
	manager := NewManager(t.TempDir())

	if _, err := manager.InstallLocal(source); err == nil || !strings.Contains(err.Error(), "missing plugin.json") {
		t.Fatalf("expected missing manifest validation error, got %v", err)
		return
	}

	if _, err := (*Manager)(nil).InstallLocal(source); err == nil || !strings.Contains(err.Error(), "install directory not configured") {
		t.Fatalf("expected nil manager configuration error, got %v", err)
		return
	}

	noInstallDir := NewManager("")
	if _, err := noInstallDir.Uninstall("anything"); err == nil || !strings.Contains(err.Error(), "install directory not configured") {
		t.Fatalf("expected uninstall configuration error, got %v", err)
		return
	}
}

func TestManagerUninstallRejectsUnmanagedPlugin(t *testing.T) {
	managed := t.TempDir()
	external := t.TempDir()
	writePluginManifest(t, filepath.Join(external, "external"), `{"name":"external","version":"1.0.0"}`)

	manager := NewManager(managed, external)
	if err := manager.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	if _, err := manager.Uninstall("external"); err == nil || !strings.Contains(err.Error(), "not managed by install directory") {
		t.Fatalf("expected unmanaged uninstall rejection, got %v", err)
		return
	}
}

func TestSanitizePluginDirName(t *testing.T) {
	tests := map[string]string{
		"Normal_Name-1.0":    "Normal_Name-1.0",
		"plugin with spaces": "plugin-with-spaces",
		"../":                "plugin",
		"插件":                 "plugin",
	}
	for input, want := range tests {
		if got := sanitizePluginDirName(input); got != want {
			t.Fatalf("sanitizePluginDirName(%q) = %q, want %q", input, got, want)
		}
	}
}
