package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func TestAppUsesEnginePluginCommandRegistry(t *testing.T) {
	pluginRoot := t.TempDir()
	pluginDir := filepath.Join(pluginRoot, "tui-plugin")
	commandDir := filepath.Join(pluginDir, "commands")
	mustWriteTUIPluginFile(t, filepath.Join(commandDir, "show.md"), "startup TUI command")
	mustWriteTUIPluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name": "tui-plugin",
		"version": "1.0.0",
		"commands": [
			{"name": "show", "filePath": "commands/show.md"}
		]
	}`)

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		PluginDirs:        []string{pluginRoot},
		CommandEntrypoint: commands.EntrypointTUI,
	})
	t.Cleanup(eng.Close)

	app := New(Config{Engine: eng})
	if app.commandRegistry != eng.GetCommandRegistry() {
		t.Fatal("New did not adopt the supplied engine command registry")
	}
	app.SetEngine(eng)
	registry := eng.GetCommandRegistry()
	if app.commandRegistry != registry {
		t.Fatal("SetEngine did not reuse the engine-owned command registry pointer")
	}
	startup, err := app.commandRegistry.Dispatch(
		context.Background(),
		commands.EntrypointTUI,
		&commands.CommandContext{Engine: eng},
		"/tui-plugin:show",
	)
	if err != nil {
		t.Fatalf("dispatch startup plugin command through TUI registry: %v", err)
	}
	if startup.Action != commands.ActionPrompt || startup.Output != "startup TUI command" {
		t.Fatalf("unexpected startup command result: %#v", startup)
	}

	mustWriteTUIPluginFile(t, filepath.Join(commandDir, "show.md"), "reloaded TUI command")
	mustWriteTUIPluginFile(t, filepath.Join(commandDir, "new.md"), "new TUI command")
	mustWriteTUIPluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name": "tui-plugin",
		"version": "1.0.0",
		"commands": [
			{"name": "new", "filePath": "commands/new.md"},
			{"name": "show", "filePath": "commands/show.md"}
		]
	}`)

	if _, err := eng.ReloadPluginCommands(); err != nil {
		t.Fatalf("reload plugin commands through engine: %v", err)
	}
	if app.commandRegistry != registry || eng.GetCommandRegistry() != registry {
		t.Fatal("explicit reload changed the shared command registry pointer")
	}
	reloaded, err := app.commandRegistry.Dispatch(
		context.Background(),
		commands.EntrypointTUI,
		&commands.CommandContext{Engine: eng},
		"/tui-plugin:show",
	)
	if err != nil {
		t.Fatalf("dispatch reloaded command through TUI registry: %v", err)
	}
	if reloaded.Output != "reloaded TUI command" {
		t.Fatalf("reloaded command output = %q", reloaded.Output)
	}
	added, err := app.commandRegistry.Dispatch(
		context.Background(),
		commands.EntrypointTUI,
		&commands.CommandContext{Engine: eng},
		"/tui-plugin:new",
	)
	if err != nil {
		t.Fatalf("dispatch newly visible command through TUI registry: %v", err)
	}
	if added.Output != "new TUI command" {
		t.Fatalf("new command output = %q", added.Output)
	}
}

func mustWriteTUIPluginFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create plugin fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plugin fixture %s: %v", path, err)
	}
}
