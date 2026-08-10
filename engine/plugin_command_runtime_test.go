package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/commands"
)

func TestQueryEnginePluginCommands(t *testing.T) {
	pluginRoot := t.TempDir()
	pluginDir := filepath.Join(pluginRoot, "runtime-plugin")
	commandDir := filepath.Join(pluginDir, "commands")
	mustWriteRuntimePluginFile(t, filepath.Join(commandDir, "greet.md"), "startup greeting")
	mustWriteRuntimePluginFile(t, filepath.Join(commandDir, "obsolete.md"), "obsolete command")
	mustWriteRuntimePluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name": "runtime-plugin",
		"version": "1.0.0",
		"commands": [
			{"name": "greet", "filePath": "commands/greet.md"},
			{"name": "obsolete", "filePath": "commands/obsolete.md"}
		]
	}`)

	eng := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		PluginDirs:        []string{pluginRoot},
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)

	registry := eng.GetCommandRegistry()
	startupGeneration := registry.PromptCommandGeneration()
	if startupGeneration.Revision != 1 ||
		startupGeneration.Commands != 4 ||
		len(startupGeneration.Sources) != 2 {
		t.Fatalf("startup generation = %#v", startupGeneration)
	}
	result, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{CWD: eng.config.CWD, Engine: eng},
		"/runtime-plugin:greet target.go",
	)
	if err != nil {
		t.Fatalf("dispatch startup plugin command: %v", err)
	}
	if result.Action != commands.ActionPrompt {
		t.Fatalf("startup command action = %q, want %q", result.Action, commands.ActionPrompt)
	}
	if result.Output != "startup greeting\n\nArguments: target.go" {
		t.Fatalf("startup command output = %q", result.Output)
	}
	if result.Data["plugin"] != "runtime-plugin" || result.Data["command"] != "greet" {
		t.Fatalf("startup command metadata = %#v", result.Data)
	}
	pluginCommand := registry.Get("runtime-plugin:greet")
	if pluginCommand == nil || pluginCommand.Source != "plugin:runtime-plugin" ||
		pluginCommand.Trust != commands.CommandTrustConfigured {
		t.Fatalf("plugin command source metadata = %#v", pluginCommand)
	}
	pluginHelp := pluginCommand.FormatHelpFor(commands.EntrypointPlain)
	if !strings.Contains(pluginHelp, "Source: plugin:runtime-plugin@1.0.0") ||
		!strings.Contains(pluginHelp, "Trust: configured") {
		t.Fatalf("plugin command help = %q", pluginHelp)
	}
	if registry.Get("runtime-plugin:obsolete") == nil {
		t.Fatal("startup did not load the obsolete command")
	}

	mustWriteRuntimePluginFile(t, filepath.Join(commandDir, "greet.md"), "updated greeting")
	mustWriteRuntimePluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name": "runtime-plugin",
		"version": "1.0.0",
		"commands": [
			{"name": "greet", "filePath": "commands/greet.md"}
		]
	}`)

	events, _ := eng.SubmitMessage(context.Background(), "/reload-plugins")
	var reloadOutput string
	for evt := range events {
		if evt.Type == EventCommandResult && evt.CommandResult != nil {
			reloadOutput = evt.CommandResult.Output
		}
	}
	if !strings.Contains(
		reloadOutput,
		"Reloaded prompt-command generation 2: 1 bundled packs, 1 plugins, 3 commands",
	) || !strings.Contains(
		reloadOutput,
		"runtime-plugin@1.0.0 [healthy; kind=configured-plugin; trust=configured]",
	) {
		t.Fatalf("reload command output = %q", reloadOutput)
	}
	if eng.GetCommandRegistry() != registry {
		t.Fatal("reload replaced the engine-owned registry pointer")
	}
	updated, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{Engine: eng},
		"/runtime-plugin:greet",
	)
	if err != nil {
		t.Fatalf("dispatch updated plugin command: %v", err)
	}
	if updated.Output != "updated greeting" {
		t.Fatalf("updated command output = %q", updated.Output)
	}
	if registry.Get("runtime-plugin:obsolete") != nil {
		t.Fatal("successful reload retained a removed plugin command")
	}
	if _, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{Engine: eng},
		"/runtime-plugin:obsolete",
	); err == nil {
		t.Fatal("removed plugin command still dispatches")
	}

	mustWriteRuntimePluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name": "runtime-plugin",
		"version": "1.0.0",
		"commands": [
			{"name": "greet", "filePath": "commands/missing.md"}
		]
	}`)
	rejected, err := eng.ReloadPluginCommands()
	if err == nil {
		t.Fatal("reload with a missing command markdown file succeeded")
	}
	if rejected.Generation.Revision != 2 ||
		len(rejected.Diagnostics) != 1 ||
		rejected.Diagnostics[0].Code != "invalid_command" {
		t.Fatalf("rejected reload result = %#v", rejected)
	}
	if eng.GetCommandRegistry() != registry {
		t.Fatal("failed reload replaced the engine-owned registry pointer")
	}
	rolledBack, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{Engine: eng},
		"/runtime-plugin:greet",
	)
	if err != nil {
		t.Fatalf("dispatch command after failed reload: %v", err)
	}
	if rolledBack.Output != "updated greeting" {
		t.Fatalf("failed reload changed the live snapshot, output = %q", rolledBack.Output)
	}
	if snapshot := eng.RuntimeInspectionSnapshot(); snapshot.PromptCommands.Revision != 2 {
		t.Fatalf("runtime inspection generation = %#v", snapshot.PromptCommands)
	}
}

func TestQueryEngineCanDisableBundledWorkflowsWithoutLosingCore(t *testing.T) {
	emptyPluginRoot := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                     t.TempDir(),
		TranscriptDir:           t.TempDir(),
		CommandEntrypoint:       commands.EntrypointPlain,
		PluginDirs:              []string{emptyPluginRoot},
		DisableBundledWorkflows: true,
	})
	t.Cleanup(eng.Close)
	registry := eng.GetCommandRegistry()
	if registry.Get("help") == nil || registry.Get("review") != nil ||
		registry.Get("commit") != nil {
		t.Fatalf("disabled bundled registry has unexpected surface: %#v", registry.List())
	}
	generation := registry.PromptCommandGeneration()
	if generation.Revision != 1 || generation.Commands != 0 ||
		len(generation.Sources) != 0 {
		t.Fatalf("disabled bundled generation = %#v", generation)
	}
}

func TestQueryEngineLoadsBundledWorkflowsIntoProductionSurface(t *testing.T) {
	emptyPluginRoot := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		PluginDirs:        []string{emptyPluginRoot},
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	registry := eng.GetCommandRegistry()
	wantCounts := map[commands.Entrypoint]int{
		commands.EntrypointTUI:            42,
		commands.EntrypointPlain:          32,
		commands.EntrypointHeadless:       18,
		commands.EntrypointACP:            14,
		commands.EntrypointAdministration: 0,
	}
	for entrypoint, want := range wantCounts {
		if got := len(registry.ListFor(entrypoint)); got != want {
			t.Fatalf("%s command count = %d, want %d", entrypoint, got, want)
		}
	}
	generation := registry.PromptCommandGeneration()
	if generation.Revision != 1 || generation.Commands != 2 ||
		len(generation.Sources) != 1 ||
		generation.Sources[0].Kind != commands.CommandSourceBundled {
		t.Fatalf("bundled production generation = %#v", generation)
	}
	if alias := registry.GetRemoved("cpr"); alias == nil || alias.Name != "commit-push-pr" {
		t.Fatalf("removed bundled alias = %#v", alias)
	}
}

func mustWriteRuntimePluginFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create plugin fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plugin fixture %s: %v", path, err)
	}
}
