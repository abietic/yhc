package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/tools"
)

func TestInspectionAdministrationEngineLoadsOnlyInspectionOwners(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	pluginRoot := filepath.Join(root, "plugins")
	pluginDir := filepath.Join(pluginRoot, "inspect")
	mustWriteRuntimePluginFile(t, filepath.Join(pluginDir, "commands", "show.md"), "inspect")
	mustWriteRuntimePluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name":"inspect",
		"version":"1.0.0",
		"commands":[{"name":"show","filePath":"commands/show.md"}]
	}`)
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	mcpManager := tools.NewMCPInspectionManager(&mcp.MCPConfig{
		Servers: map[string]*mcp.MCPServerConfig{
			"docs": {Name: "docs", Enabled: true, URL: "https://secret@example.test"},
		},
	})

	if runtime.GOOS != "windows" {
		marker := filepath.Join(root, "git-invoked")
		binDir := filepath.Join(root, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(binDir, "git"),
			[]byte("#!/bin/sh\nprintf invoked > \"$INSPECTION_GIT_MARKER\"\n"),
			0o700,
		); err != nil {
			t.Fatal(err)
		}
		t.Setenv("INSPECTION_GIT_MARKER", marker)
		t.Setenv("PATH", binDir)
		defer func() {
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("inspection invoked git: %v", err)
			}
		}()
	}

	eng := NewInspectionAdministrationEngine(InspectionAdministrationConfig{
		CWD:           root,
		TranscriptDir: transcriptDir,
		ToolRegistry:  registry,
		MCPManager:    mcpManager,
		PluginDirs:    []string{pluginRoot},
	})
	syntheticPath := filepath.Join(transcriptDir, eng.SessionID()+".jsonl")
	if eng.config.ChatModel != nil || eng.config.CommandEntrypoint != commands.EntrypointAdministration {
		t.Fatalf("inspection engine config = %#v", eng.config)
	}
	t.Cleanup(eng.Close)
	if eng.transcript != nil || eng.resultStorage != nil || eng.memoryStore != nil ||
		eng.runtimeState != nil || eng.inputCoordinator != nil ||
		eng.projectGraphCheckpoint != nil || eng.agentRunner != nil ||
		eng.taskManager != nil || eng.shellManager != nil || eng.sessionService != nil ||
		eng.worktreeLifecycle != nil || eng.permissionRegistry != nil ||
		eng.permissionCoordinator != nil || eng.config.HookExecutor != nil ||
		eng.settingsWatcher != nil || eng.skillRegistry != nil ||
		eng.backgroundServices != nil || eng.subagentExecutor != nil ||
		eng.queryKernelSelection.kernel != nil {
		t.Fatal("inspection engine initialized conversation services")
	}
	promptGeneration := eng.GetCommandRegistry().PromptCommandGeneration()
	mcpSnapshot := eng.MCPInventorySnapshot()
	if promptGeneration.Revision != 1 || promptGeneration.Commands != 3 ||
		len(mcpSnapshot.Servers) != 1 || mcpSnapshot.Servers[0].Health != "unprobed" {
		t.Fatalf("prompt generation = %#v; MCP snapshot = %#v", promptGeneration, mcpSnapshot)
	}

	before := eng.GetCommandRegistry().PromptCommandGeneration()
	validated, err := eng.ValidatePromptCommands()
	if err != nil {
		t.Fatalf("validate prompt commands: %v", err)
	}
	if validated.LiveGeneration.Revision != before.Revision || validated.Digest != before.Digest ||
		validated.Commands != before.Commands {
		t.Fatalf("validation result = %#v, live=%#v", validated, before)
	}
	if after := eng.GetCommandRegistry().PromptCommandGeneration(); after.Revision != before.Revision ||
		after.Digest != before.Digest {
		t.Fatalf("validation changed live generation: %#v", after)
	}

	mustWriteRuntimePluginFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name":"inspect",
		"commands":[{"name":"show","filePath":"commands/missing.md"}]
	}`)
	rejected, err := eng.ValidatePromptCommands()
	if err == nil || len(rejected.Diagnostics) == 0 ||
		rejected.LiveGeneration.Revision != before.Revision {
		t.Fatalf("rejected validation = %#v err=%v", rejected, err)
	}
	if after := eng.GetCommandRegistry().PromptCommandGeneration(); after.Revision != before.Revision ||
		after.Digest != before.Digest || eng.GetCommandRegistry().Get("inspect:show") == nil {
		t.Fatalf("rejected validation changed live generation: %#v", after)
	}

	eng.Close()
	if _, err := os.Stat(syntheticPath); !os.IsNotExist(err) {
		t.Fatalf("inspection created a synthetic transcript: %v", err)
	}
}
