package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMarketplace(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}
}

func TestLoadMarketplaceFileParsesReferenceShapeAndSortsPlugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marketplace.json")
	writeMarketplace(t, path, `{
		"name": "local-market",
		"owner": {"name": "Team", "email": "team@example.com"},
		"metadata": {"version": "2026.1", "description": "Local marketplace", "pluginRoot": "plugins"},
		"plugins": [
			{"name":"zeta","description":"Z plugin","source":{"source":"github","repo":"org/zeta","ref":"main"},"category":"dev","tags":["go"],"strict":false},
			{"name":"alpha","version":"1.0.0","source":"./plugins/alpha"}
		]
	}`)

	marketplace, err := LoadMarketplaceFile(path)
	if err != nil {
		t.Fatalf("LoadMarketplaceFile failed: %v", err)
		return
	}
	if marketplace.Name != "local-market" || marketplace.Owner.Name != "Team" || marketplace.Metadata.PluginRoot != "plugins" || marketplace.SourcePath != path {
		t.Fatalf("unexpected marketplace metadata: %#v", marketplace)
	}
	if len(marketplace.Plugins) != 2 {
		t.Fatalf("expected two plugins, got %#v", marketplace.Plugins)
	}
	if marketplace.Plugins[0].Name != "alpha" || marketplace.Plugins[0].Source.Kind != "path" || marketplace.Plugins[0].Source.Path != "./plugins/alpha" || !marketplace.Plugins[0].Strict {
		t.Fatalf("unexpected first plugin: %#v", marketplace.Plugins[0])
	}
	if marketplace.Plugins[1].Name != "zeta" || marketplace.Plugins[1].Source.Kind != "github" || marketplace.Plugins[1].Source.Repo != "org/zeta" || marketplace.Plugins[1].Strict {
		t.Fatalf("unexpected second plugin: %#v", marketplace.Plugins[1])
	}
}

func TestLoadMarketplaceFileValidatesRequiredFieldsAndSources(t *testing.T) {
	dir := t.TempDir()
	tests := map[string]string{
		"missing-name":           `{"owner":{"name":"Team"},"plugins":[]}`,
		"missing-owner":          `{"name":"m","plugins":[]}`,
		"missing-plugins":        `{"name":"m","owner":{"name":"Team"}}`,
		"plugin-name-with-space": `{"name":"m","owner":{"name":"Team"},"plugins":[{"name":"bad name","source":"./p"}]}`,
		"missing-source":         `{"name":"m","owner":{"name":"Team"},"plugins":[{"name":"p"}]}`,
		"bad-github":             `{"name":"m","owner":{"name":"Team"},"plugins":[{"name":"p","source":{"source":"github"}}]}`,
		"unsupported":            `{"name":"m","owner":{"name":"Team"},"plugins":[{"name":"p","source":{"source":"file","path":"p"}}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			writeMarketplace(t, path, body)
			if _, err := LoadMarketplaceFile(path); err == nil {
				t.Fatal("expected validation error")
				return
			}
		})
	}
}

func TestDiscoverMarketplacesSupportsFilesAndCachedDirectories(t *testing.T) {
	root := t.TempDir()
	fileSource := filepath.Join(root, "one.json")
	writeMarketplace(t, fileSource, `{"name":"z-market","owner":{"name":"Zed"},"plugins":[]}`)
	directDir := filepath.Join(root, "direct")
	writeMarketplace(t, filepath.Join(directDir, "marketplace.json"), `{"name":"a-market","owner":{"name":"Ann"},"plugins":[]}`)
	nestedDir := filepath.Join(root, "nested")
	writeMarketplace(t, filepath.Join(nestedDir, ".claude-plugin", "marketplace.json"), `{"name":"m-market","owner":{"name":"Moe"},"plugins":[]}`)

	marketplaces, err := DiscoverMarketplaces(fileSource, directDir, nestedDir)
	if err != nil {
		t.Fatalf("DiscoverMarketplaces failed: %v", err)
		return
	}
	if len(marketplaces) != 3 {
		t.Fatalf("expected three marketplaces, got %#v", marketplaces)
	}
	got := []string{marketplaces[0].Name, marketplaces[1].Name, marketplaces[2].Name}
	want := []string{"a-market", "m-market", "z-market"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marketplaces sorted by name = %#v want %#v", got, want)
		}
	}
}

func TestDiscoverMarketplacesReturnsPartialResultsWithErrors(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good.json")
	writeMarketplace(t, good, `{"name":"good","owner":{"name":"Team"},"plugins":[]}`)
	bad := filepath.Join(root, "bad.json")
	writeMarketplace(t, bad, `{not-json`)

	marketplaces, err := DiscoverMarketplaces(good, bad, filepath.Join(root, "missing"))
	if err == nil || !strings.Contains(err.Error(), "marketplace discovery errors") {
		t.Fatalf("expected discovery errors, got %v", err)
		return
	}
	if len(marketplaces) != 1 || marketplaces[0].Name != "good" {
		t.Fatalf("expected partial good marketplace result, got %#v", marketplaces)
	}
}
