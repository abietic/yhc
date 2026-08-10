package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallFromMarketplace(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":        "test-remote-plugin",
		"version":     "1.2.0",
		"description": "A test plugin from marketplace",
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plugins/test-remote-plugin" {
			json.NewEncoder(w).Encode(MarketplaceSearchResult{
				Name:    "test-remote-plugin",
				Version: "1.2.0",
				Source:  pluginDir,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewMarketplaceClient(server.URL)
	installDir := t.TempDir()
	mgr := NewManager(installDir)

	result, err := mgr.InstallFromMarketplace(context.Background(), client, "test-remote-plugin")
	if err != nil {
		t.Fatalf("InstallFromMarketplace: %v", err)
	}
	if result.Name != "test-remote-plugin" {
		t.Errorf("name: got %q want %q", result.Name, "test-remote-plugin")
	}
	if result.Version != "1.2.0" {
		t.Errorf("version: got %q want %q", result.Version, "1.2.0")
	}
	if result.Replaced {
		t.Error("expected first install to not be a replacement")
	}

	metaPath := filepath.Join(result.TargetDir, remoteSourceFileName)
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("remote source metadata missing: %v", err)
	}
}

func TestCheckForUpdates(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    "updatable-plugin",
		"version": "1.0.0",
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plugins/updatable-plugin" {
			json.NewEncoder(w).Encode(MarketplaceSearchResult{
				Name:    "updatable-plugin",
				Version: "2.0.0",
				Source:  pluginDir,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewMarketplaceClient(server.URL)
	installDir := t.TempDir()
	mgr := NewManager(installDir)

	_, err := mgr.InstallLocal(pluginDir)
	if err != nil {
		t.Fatalf("InstallLocal: %v", err)
	}

	check, err := mgr.CheckForUpdates(context.Background(), client, "updatable-plugin")
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if !check.UpdateAvailable {
		t.Error("expected update to be available")
	}
	if check.InstalledVersion != "1.0.0" {
		t.Errorf("installed version: got %q want %q", check.InstalledVersion, "1.0.0")
	}
	if check.LatestVersion != "2.0.0" {
		t.Errorf("latest version: got %q want %q", check.LatestVersion, "2.0.0")
	}
}

func TestUpdatePlugin(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    "update-me",
		"version": "1.0.0",
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path == "/plugins/update-me" {
			json.NewEncoder(w).Encode(MarketplaceSearchResult{
				Name:    "update-me",
				Version: "2.0.0",
				Source:  pluginDir,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewMarketplaceClient(server.URL)
	installDir := t.TempDir()
	mgr := NewManager(installDir)

	_, err := mgr.InstallLocal(pluginDir)
	if err != nil {
		t.Fatalf("InstallLocal: %v", err)
	}

	result, err := mgr.UpdatePlugin(context.Background(), client, "update-me")
	if err != nil {
		t.Fatalf("UpdatePlugin: %v", err)
	}
	if result.Version != "2.0.0" {
		t.Errorf("updated version: got %q want %q", result.Version, "2.0.0")
	}
	if !result.Replaced {
		t.Error("expected update to be a replacement")
	}
}

func TestUpdatePlugin_NoUpdateAvailable(t *testing.T) {
	pluginDir := t.TempDir()
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    "current-plugin",
		"version": "1.0.0",
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plugins/current-plugin" {
			json.NewEncoder(w).Encode(MarketplaceSearchResult{
				Name:    "current-plugin",
				Version: "1.0.0",
				Source:  pluginDir,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewMarketplaceClient(server.URL)
	installDir := t.TempDir()
	mgr := NewManager(installDir)

	_, err := mgr.InstallLocal(pluginDir)
	if err != nil {
		t.Fatalf("InstallLocal: %v", err)
	}

	result, err := mgr.UpdatePlugin(context.Background(), client, "current-plugin")
	if err != nil {
		t.Fatalf("UpdatePlugin: %v", err)
	}
	if result.Version != "1.0.0" {
		t.Errorf("version should remain 1.0.0, got %q", result.Version)
	}
}

func TestInstallFromMarketplace_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewMarketplaceClient(server.URL)
	installDir := t.TempDir()
	mgr := NewManager(installDir)

	_, err := mgr.InstallFromMarketplace(context.Background(), client, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent plugin")
	}
}
