package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/engine/skills"
)

func writePluginManifest(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
		return
	}
}

func TestLoaderLoadParsesManifestCapabilitiesAndLookup(t *testing.T) {
	root := t.TempDir()
	writePluginManifest(t, filepath.Join(root, "plugin-dir"), `{
		"name": "ExamplePlugin",
		"version": "1.2.3",
		"description": "example plugin",
		"skills": [{"name": "review", "description": "Review code", "filePath": "skills/review.md"}],
		"commands": [{"name": "hello", "description": "Say hello"}],
		"hooks": [{"event": "PreToolUse", "type": "command"}],
		"mcpServers": [{"name": "docs", "command": "docs-mcp"}]
	}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}

	plugin, ok := loader.Get("exampleplugin")
	if !ok {
		t.Fatal("expected case-insensitive plugin lookup by manifest name")
	}
	if plugin.Name != "ExamplePlugin" || plugin.Version != "1.2.3" || plugin.Description != "example plugin" {
		t.Fatalf("unexpected plugin metadata: %#v", plugin)
	}
	if plugin.Directory != filepath.Join(root, "plugin-dir") || !plugin.Enabled {
		t.Fatalf("unexpected plugin directory/enabled state: %#v", plugin)
	}
	if len(plugin.Skills) != 1 || plugin.Skills[0].Name != "review" || plugin.Skills[0].FilePath != "skills/review.md" {
		t.Fatalf("unexpected skills: %#v", plugin.Skills)
	}
	if len(plugin.Commands) != 1 || plugin.Commands[0].Name != "hello" {
		t.Fatalf("unexpected commands: %#v", plugin.Commands)
	}
	if len(plugin.Hooks) != 1 || plugin.Hooks[0].Event != "PreToolUse" || plugin.Hooks[0].Type != "command" {
		t.Fatalf("unexpected hooks: %#v", plugin.Hooks)
	}
	if len(plugin.MCPServers) != 1 || plugin.MCPServers[0].Name != "docs" || plugin.MCPServers[0].Command != "docs-mcp" {
		t.Fatalf("unexpected MCP servers: %#v", plugin.MCPServers)
	}

	if !loader.Disable("EXAMPLEPLUGIN") {
		t.Fatal("expected case-insensitive disable")
	}
	if plugin.Enabled {
		t.Fatal("expected plugin disabled")
	}
	if !loader.Enable("exampleplugin") {
		t.Fatal("expected case-insensitive enable")
	}
	if !plugin.Enabled {
		t.Fatal("expected plugin enabled")
	}
}

func TestLoaderPrecedenceUsesLaterDirectories(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writePluginManifest(t, filepath.Join(first, "shared"), `{
		"name": "shared",
		"version": "1.0.0",
		"description": "from first"
	}`)
	writePluginManifest(t, filepath.Join(second, "shared"), `{
		"name": "shared",
		"version": "2.0.0",
		"description": "from second"
	}`)

	loader := NewLoader(first, second)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}

	plugin, ok := loader.Get("shared")
	if !ok {
		t.Fatal("expected shared plugin")
	}
	if plugin.Version != "2.0.0" || plugin.Directory != filepath.Join(second, "shared") {
		t.Fatalf("expected later directory to override earlier one, got %#v", plugin)
	}
}

func TestLoaderRejectsInvalidPluginAndPreservesPreviousSnapshot(t *testing.T) {
	root := t.TempDir()
	writePluginManifest(t, filepath.Join(root, "valid"), `{"name":"valid","version":"1.0.0"}`)
	if err := os.WriteFile(filepath.Join(root, "not-a-dir"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write file entry: %v", err)
		return
	}

	loader := NewLoader(root, filepath.Join(root, "missing"))
	if err := loader.Load(); err != nil {
		t.Fatalf("initial Load failed: %v", err)
		return
	}
	plugins := loader.List()
	if len(plugins) != 1 || plugins[0].Name != "valid" {
		t.Fatalf("expected only valid plugin to load, got %#v", plugins)
	}

	writePluginManifest(t, filepath.Join(root, "invalid-json"), `{not-json`)
	if err := loader.Load(); err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("Load should reject an invalid discovered plugin, got: %v", err)
	}
	plugins = loader.List()
	if len(plugins) != 1 || plugins[0].Name != "valid" {
		t.Fatalf("failed reload changed the previous snapshot: %#v", plugins)
	}
}

func TestBuildCommandGenerationAggregatesDiagnosticsAndPreservesLiveLoader(t *testing.T) {
	root := t.TempDir()
	writePluginManifest(t, filepath.Join(root, "stable"), `{
		"name": "stable",
		"version": "1.0.0",
		"commands": [{"name": "inspect", "description": "stable"}]
	}`)
	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatal(err)
	}
	before := registry.PromptCommandGeneration()
	if registry.Get("stable:inspect") == nil {
		t.Fatal("initial configured command was not installed")
	}

	writePluginManifest(t, filepath.Join(root, "bad-manifest"), `{not-json`)
	writePluginManifest(t, filepath.Join(root, "bad-command"), `{
		"name": "bad-command",
		"commands": [{"name": "missing", "filePath": "missing.md"}]
	}`)
	candidate, err := loader.BuildCommandGeneration()
	if err == nil {
		t.Fatal("invalid candidate generation succeeded")
	}
	var generationErr *GenerationError
	if !errors.As(err, &generationErr) {
		t.Fatalf("generation error type = %T: %v", err, err)
	}
	errorCount := 0
	codes := make(map[string]bool)
	for _, diagnostic := range candidate.Diagnostics {
		if diagnostic.Severity == "error" {
			errorCount++
			codes[diagnostic.Code] = true
		}
	}
	if errorCount != 2 ||
		!codes["load_manifest"] ||
		!codes["invalid_command"] {
		t.Fatalf("candidate diagnostics = %#v", candidate.Diagnostics)
	}
	live := loader.List()
	if len(live) != 1 || live[0].Name != "stable" {
		t.Fatalf("rejected candidate changed live loader: %#v", live)
	}
	if err := loader.RegisterCommands(registry); err == nil {
		t.Fatal("invalid configured candidate replaced the live registry generation")
	}
	after := registry.PromptCommandGeneration()
	if after.Revision != before.Revision ||
		after.Digest != before.Digest ||
		after.Commands != before.Commands ||
		registry.Get("stable:inspect") == nil {
		t.Fatalf("rejected candidate changed live registry generation: before=%#v after=%#v", before, after)
	}
}

func TestBuildCommandGenerationQualifiesAliases(t *testing.T) {
	root := t.TempDir()
	writePluginManifest(t, filepath.Join(root, "local-workflows"), `{
		"name": "local-workflows",
		"version": "1.0.0",
		"commands": [{
			"name": "summary",
			"aliases": ["recap"],
			"description": "Summarize locally"
		}]
	}`)
	loader := NewLoader(root)
	candidate, err := loader.BuildCommandGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Commands) != 3 {
		t.Fatalf("commands = %#v", candidate.Commands)
	}
	command := candidate.Commands[2]
	if command.Name != "local-workflows:summary" ||
		len(command.Aliases) != 1 ||
		command.Aliases[0] != "local-workflows:recap" {
		t.Fatalf("qualified command = %#v", command)
	}
	if candidate.Digest == "" || len(candidate.Sources) != 2 ||
		candidate.Sources[0].Kind != commands.CommandSourceBundled ||
		candidate.Sources[1].Kind != commands.CommandSourcePlugin {
		t.Fatalf("candidate metadata = %#v", candidate)
	}
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	if _, err := registry.ReplacePromptCommandGeneration(candidate); err != nil {
		t.Fatalf("qualified replacement collided with tombstone: %v", err)
	}
	if registry.Get("local-workflows:summary") == nil ||
		registry.Get("summary") != nil ||
		registry.GetRemoved("summary") == nil {
		t.Fatal("configured workflow did not remain qualified beside the unqualified tombstone")
	}
}

func TestLoadPluginDefaultsNameToDirectory(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "DirectoryName")
	writePluginManifest(t, pluginDir, `{"version":"0.1.0"}`)

	loader := NewLoader(root)
	plugin, err := loader.loadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("loadPlugin failed: %v", err)
		return
	}
	if plugin.Name != "DirectoryName" || !plugin.Enabled {
		t.Fatalf("expected directory name fallback and enabled plugin, got %#v", plugin)
	}
}

func TestDefaultPluginDirsOnlyIncludesExistingProjectPlugins(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	cwd := filepath.Join(root, "project")

	dirs := DefaultPluginDirs(configDir, cwd)
	if len(dirs) != 1 || dirs[0] != filepath.Join(configDir, "plugins") {
		t.Fatalf("expected only config plugins dir before project dir exists, got %#v", dirs)
	}

	projectPlugins := filepath.Join(cwd, ".claude", "plugins")
	if err := os.MkdirAll(projectPlugins, 0o755); err != nil {
		t.Fatalf("create project plugins dir: %v", err)
		return
	}
	dirs = DefaultPluginDirs(configDir, cwd)
	if len(dirs) != 2 || dirs[1] != projectPlugins {
		t.Fatalf("expected project plugins dir after it exists, got %#v", dirs)
	}
}

func TestValidateManifestFailuresAndSuccess(t *testing.T) {
	root := t.TempDir()
	if err := Validate(root); err == nil || err.Error() != "missing plugin.json" {
		t.Fatalf("expected missing plugin.json error, got %v", err)
		return
	}

	invalidJSON := filepath.Join(root, "invalid")
	writePluginManifest(t, invalidJSON, `{not-json`)
	if err := Validate(invalidJSON); err == nil || !strings.Contains(err.Error(), "invalid plugin.json") {
		t.Fatalf("expected invalid plugin.json error, got %v", err)
		return
	}

	missingName := filepath.Join(root, "missing-name")
	writePluginManifest(t, missingName, `{"version":"1.0.0"}`)
	if err := Validate(missingName); err == nil || err.Error() != "plugin name is required" {
		t.Fatalf("expected required-name error, got %v", err)
		return
	}

	valid := filepath.Join(root, "valid")
	writePluginManifest(t, valid, `{"name":"valid","version":"1.0.0"}`)
	if err := Validate(valid); err != nil {
		t.Fatalf("Validate valid manifest failed: %v", err)
		return
	}
}

func TestLoaderRegisterSkillsFromManifestAndDefaultDirectory(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "skill-plugin")
	writePluginManifest(t, pluginDir, `{
		"name": "skill-plugin",
		"version": "1.0.0",
		"skills": [{"name": "declared", "filePath": "custom/declared.md"}]
	}`)
	if err := os.MkdirAll(filepath.Join(pluginDir, "custom"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "custom", "declared.md"), []byte("---\nname: declared\ndescription: Declared skill\n---\nDeclared {{thing}}."), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "skills", "default.md"), []byte("---\nname: default-skill\ndescription: Default skill\n---\nDefault body."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	registry := skills.NewSkillRegistry()
	if err := loader.RegisterSkills(registry); err != nil {
		t.Fatalf("RegisterSkills failed: %v", err)
		return
	}

	declared, ok := registry.Get("declared")
	if !ok || declared.Description != "Declared skill" {
		t.Fatalf("expected declared plugin skill, got %#v ok=%v", declared, ok)
	}
	expanded, err := registry.Invoke("declared", map[string]string{"thing": "value"})
	if err != nil {
		t.Fatalf("invoke declared skill: %v", err)
		return
	}
	if !strings.Contains(expanded, "Declared value.") {
		t.Fatalf("unexpected declared skill body: %q", expanded)
	}
	if _, ok := registry.Get("default-skill"); !ok {
		t.Fatal("expected conventional skills/ directory skill to be registered")
	}
}

func TestLoaderRegisterSkillsSkipsDisabledPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "disabled-plugin")
	writePluginManifest(t, pluginDir, `{"name":"disabled-plugin","version":"1.0.0","skills":[{"name":"disabled","filePath":"skill.md"}]}`)
	if err := os.WriteFile(filepath.Join(pluginDir, "skill.md"), []byte("---\nname: disabled\n---\nNope"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	if !loader.Disable("disabled-plugin") {
		t.Fatal("expected disable to succeed")
	}
	registry := skills.NewSkillRegistry()
	if err := loader.RegisterSkills(registry); err != nil {
		t.Fatalf("RegisterSkills failed: %v", err)
		return
	}
	if _, ok := registry.Get("disabled"); ok {
		t.Fatal("disabled plugin skill should not be registered")
	}
}

func TestLoaderRegisterSkillsRejectsEscapingSkillPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.md")
	if err := os.WriteFile(outside, []byte("---\nname: outside\n---\nOutside"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	pluginDir := filepath.Join(root, "bad-plugin")
	writePluginManifest(t, pluginDir, `{"name":"bad-plugin","version":"1.0.0","skills":[{"name":"escape","filePath":"../outside.md"}]}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	err := loader.RegisterSkills(skills.NewSkillRegistry())
	if err == nil || !strings.Contains(err.Error(), "escapes plugin directory") {
		t.Fatalf("expected escaping skill path error, got %v", err)
		return
	}
}

func TestLoaderRegisterShellHooksFromEnabledPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "hook-plugin")
	writePluginManifest(t, pluginDir, `{
		"name": "hook-plugin",
		"version": "1.0.0",
		"hooks": [
			{"event":"PreToolUse","type":"command","command":"echo pre","matcher":"Bash","timeout":3,"async":true,"statusMessage":"running pre"},
			{"event":"PostToolUse","type":"command","command":"echo post","matcher":"Read"},
			{"event":"UserPromptSubmit","type":"command","command":"echo prompt"},
			{"event":"Stop","type":"command","command":"echo unsupported"},
			{"event":"PreToolUse","type":"http","command":"ignored"}
		]
	}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	cfg := &hooks.ShellHookConfig{}
	if err := loader.RegisterShellHooks(cfg); err != nil {
		t.Fatalf("RegisterShellHooks failed: %v", err)
		return
	}

	if len(cfg.PreToolHooks) != 1 || cfg.PreToolHooks[0].Command != "echo pre" {
		t.Fatalf("unexpected pre hooks: %#v", cfg.PreToolHooks)
	}
	if cfg.PreToolHooks[0].ToolPattern != "Bash" || !cfg.PreToolHooks[0].Async || cfg.PreToolHooks[0].StatusMessage != "running pre" {
		t.Fatalf("unexpected pre hook metadata: %#v", cfg.PreToolHooks[0])
	}
	if cfg.PreToolHooks[0].Timeout != 3*time.Second {
		t.Fatalf("unexpected pre hook timeout: %v", cfg.PreToolHooks[0].Timeout)
	}
	if len(cfg.PostToolHooks) != 1 || cfg.PostToolHooks[0].Command != "echo post" || cfg.PostToolHooks[0].Phase != "post" {
		t.Fatalf("unexpected post hooks: %#v", cfg.PostToolHooks)
	}
	if len(cfg.UserPromptHooks) != 1 || cfg.UserPromptHooks[0].Command != "echo prompt" || cfg.UserPromptHooks[0].Phase != "user_prompt" {
		t.Fatalf("unexpected user prompt hooks: %#v", cfg.UserPromptHooks)
	}
}

func TestLoaderRegisterShellHooksSkipsDisabledPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "disabled-hook-plugin")
	writePluginManifest(t, pluginDir, `{"name":"disabled-hook-plugin","version":"1.0.0","hooks":[{"event":"PreToolUse","type":"command","command":"echo nope"}]}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	loader.Disable("disabled-hook-plugin")
	cfg := &hooks.ShellHookConfig{}
	if err := loader.RegisterShellHooks(cfg); err != nil {
		t.Fatalf("RegisterShellHooks failed: %v", err)
		return
	}
	if len(cfg.PreToolHooks) != 0 {
		t.Fatalf("disabled plugin hook should not be registered: %#v", cfg.PreToolHooks)
	}
}

func TestLoaderRegisterShellHooksRequiresCommandForSupportedEvents(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "bad-hook-plugin")
	writePluginManifest(t, pluginDir, `{"name":"bad-hook-plugin","version":"1.0.0","hooks":[{"event":"PreToolUse","type":"command"}]}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	err := loader.RegisterShellHooks(&hooks.ShellHookConfig{})
	if err == nil || !strings.Contains(err.Error(), "missing command") {
		t.Fatalf("expected missing command error, got %v", err)
		return
	}
}

func TestPluginHookToShellHookAsyncRewakeImpliesAsync(t *testing.T) {
	hook, err := pluginHookToShellHook(&Plugin{Name: "rewake-plugin"}, PluginHook{
		Event: string(hooks.HookEventPostToolUse), Type: string(hooks.HookCommandTypeCommand),
		Command: "check-policy", AsyncRewake: true,
	})
	if err != nil {
		t.Fatalf("pluginHookToShellHook: %v", err)
	}
	if hook == nil || !hook.Async || !hook.AsyncRewake {
		t.Fatalf("async rewake mapping = %#v", hook)
	}
}

func TestLoaderRegisterMCPServersFromEnabledPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "mcp-plugin")
	writePluginManifest(t, pluginDir, `{
		"name": "mcp-plugin",
		"version": "1.0.0",
		"mcpServers": [
			{"name":"docs","command":"docs-mcp","args":["--stdio"],"env":{"TOKEN":"abc"},"cwd":"server","type":"stdio"},
			{"name":"remote","type":"http","url":"https://example.com/mcp","headers":{"X-Test":"yes"},"enabled":false}
		]
	}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	cfg := &mcp.MCPConfig{Servers: map[string]*mcp.MCPServerConfig{
		"docs": {Name: "docs", Command: "old-command", Enabled: true},
	}}
	if err := loader.RegisterMCPServers(cfg); err != nil {
		t.Fatalf("RegisterMCPServers failed: %v", err)
		return
	}

	docs := cfg.Servers["docs"]
	if docs == nil || docs.Command != "docs-mcp" || docs.Args[0] != "--stdio" || docs.Env["TOKEN"] != "abc" {
		t.Fatalf("unexpected docs MCP config: %#v", docs)
		return
	}
	if docs.CWD != filepath.Join(pluginDir, "server") || !docs.Enabled || docs.Type != "stdio" {
		t.Fatalf("unexpected docs CWD/enabled/type: %#v", docs)
	}
	remote := cfg.Servers["remote"]
	if remote == nil || remote.Type != "http" || remote.URL != "https://example.com/mcp" || remote.Headers["X-Test"] != "yes" || remote.Enabled {
		t.Fatalf("unexpected remote MCP config: %#v", remote)
		return
	}

	// Ensure env map was cloned.
	docs.Env["TOKEN"] = "mutated"
	if plugin, _ := loader.Get("mcp-plugin"); plugin.MCPServers[0].Env["TOKEN"] != "abc" {
		t.Fatal("plugin env should not be mutated through MCP config")
	}
}

func TestLoaderRegisterMCPServersSkipsDisabledPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "disabled-mcp-plugin")
	writePluginManifest(t, pluginDir, `{"name":"disabled-mcp-plugin","version":"1.0.0","mcpServers":[{"name":"disabled","command":"nope"}]}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	loader.Disable("disabled-mcp-plugin")
	cfg := &mcp.MCPConfig{}
	if err := loader.RegisterMCPServers(cfg); err != nil {
		t.Fatalf("RegisterMCPServers failed: %v", err)
		return
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("disabled plugin MCP server should not be registered: %#v", cfg.Servers)
	}
}

func TestLoaderRegisterMCPServersRequiresName(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "bad-mcp-plugin")
	writePluginManifest(t, pluginDir, `{"name":"bad-mcp-plugin","version":"1.0.0","mcpServers":[{"command":"missing-name"}]}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	err := loader.RegisterMCPServers(&mcp.MCPConfig{})
	if err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Fatalf("expected missing MCP server name error, got %v", err)
		return
	}
}

func TestLoaderRegisterCommandsDispatchesPromptAction(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "command-plugin")
	writePluginManifest(t, pluginDir, `{
		"name": "command-plugin",
		"version": "1.0.0",
		"commands": [
			{"name":"review","description":"Review code","filePath":"commands/review.md"},
			{"name":"describe","description":"Describe current task"}
		]
	}`)
	if err := os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "commands", "review.md"), []byte("Review these files carefully."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	registry := commands.NewRegistry()
	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatalf("RegisterCommands failed: %v", err)
		return
	}

	result, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointTUI,
		&commands.CommandContext{},
		"/command-plugin:review src/main.go",
	)
	if err != nil {
		t.Fatalf("dispatch plugin command failed: %v", err)
		return
	}
	if result.Action != commands.ActionPrompt || !strings.Contains(result.Output, "Review these files carefully.") || !strings.Contains(result.Output, "Arguments: src/main.go") {
		t.Fatalf("unexpected plugin command result: %#v", result)
	}
	if result.Data["plugin"] != "command-plugin" || result.Data["command"] != "review" {
		t.Fatalf("unexpected plugin command metadata: %#v", result.Data)
	}

	fallback, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointTUI,
		&commands.CommandContext{},
		"/command-plugin:describe",
	)
	if err != nil {
		t.Fatalf("dispatch description command failed: %v", err)
		return
	}
	if fallback.Action != commands.ActionPrompt || fallback.Output != "Describe current task" {
		t.Fatalf("unexpected description command result: %#v", fallback)
	}
}

func TestLoaderRegisterCommandsSkipsDisabledPlugins(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "disabled-command-plugin")
	writePluginManifest(t, pluginDir, `{"name":"disabled-command-plugin","version":"1.0.0","commands":[{"name":"skip","description":"Skip"}]}`)

	loader := NewLoader(root)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	loader.Disable("disabled-command-plugin")
	registry := commands.NewRegistry()
	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatalf("RegisterCommands failed: %v", err)
		return
	}
	if got := registry.Get("disabled-command-plugin:skip"); got != nil {
		t.Fatalf("disabled plugin command should not be registered: %#v", got)
		return
	}
}

func TestLoaderRegisterCommandsRejectsEscapingFilePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	pluginDir := filepath.Join(root, "bad-command-plugin")
	writePluginManifest(t, pluginDir, `{"name":"bad-command-plugin","version":"1.0.0","commands":[{"name":"escape","filePath":"../outside.md"}]}`)

	loader := NewLoader(root)
	err := loader.Load()
	if err == nil ||
		!strings.Contains(err.Error(), "escapes plugin directory") {
		t.Fatalf("expected escaping command path error, got %v", err)
		return
	}
	if len(loader.List()) != 0 {
		t.Fatalf("escaping command path changed live loader: %#v", loader.List())
	}
}
