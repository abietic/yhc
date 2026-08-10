package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
)

type fakeInspectionProvider struct {
	snapshot RuntimeInspectionSnapshot
}

func (f fakeInspectionProvider) RuntimeInspectionSnapshot() RuntimeInspectionSnapshot {
	return f.snapshot
}

func TestInspectionCommandsReadEngineOwnedSnapshot(t *testing.T) {
	provider := fakeInspectionProvider{snapshot: RuntimeInspectionSnapshot{
		Tasks: tools.RuntimeTaskSnapshot{
			Agents: []tools.RuntimeAgentSnapshot{{
				ID:          "agent-1",
				Name:        "worker",
				Status:      "running",
				Description: "Inspect command ownership",
			}},
		},
		AgentDefinitions: map[string]AgentInfo{
			"reviewer": {
				Name:      "reviewer",
				WhenToUse: "Review bounded diffs",
				Source:    "project",
			},
		},
		Skills: skills.Snapshot{
			Skills: []*skills.Skill{{
				Name:        "audit",
				Description: "Audit runtime boundaries",
				Content:     "Inspect owners.",
				Source:      "project",
				Health:      "available",
			}},
			Diagnostics: []skills.Diagnostic{{
				Source:   "user",
				FilePath: "/tmp/bad-skill.md",
				Message:  "invalid frontmatter",
			}},
		},
		MCP: tools.MCPInventorySnapshot{
			Revision: 7,
			Servers: []tools.MCPServerSnapshot{{
				Name:   "docs",
				Source: "runtime-manager",
				Status: "connected",
				Health: "healthy",
				Tools: []*tools.MCPToolInfo{{
					ServerName:  "docs",
					ToolName:    "search",
					Description: "Search documentation",
				}},
			}},
		},
		Hooks: &hooks.ShellHookConfig{
			Source: "/project/.claude/hooks.json",
			PreToolHooks: []hooks.ShellHook{{
				Command:     "check-read",
				ToolPattern: "Read",
			}},
		},
	}}
	ctx := &CommandContext{Engine: provider}

	assertInspectionOutputContains(
		t,
		executeAgents,
		ctx,
		"",
		"Runtime agents (1)",
		"agent-1",
		"Available agent types (1)",
		"reviewer",
	)
	assertInspectionOutputContains(
		t,
		executeSkills,
		ctx,
		"",
		"audit",
		"Source: project",
		"Health: available",
		"Registry diagnostics: 1",
	)
	assertInspectionOutputContains(
		t,
		executeMCP,
		ctx,
		"",
		"generation 7",
		"docs [connected; health=healthy; source=runtime-manager]",
		"search",
	)
	assertInspectionOutputContains(
		t,
		executeHooks,
		ctx,
		"",
		"Source: /project/.claude/hooks.json",
		"Health: healthy",
		"check-read",
	)
}

func TestInspectionMutationSubcommandsAreUnavailableAndSideEffectFree(t *testing.T) {
	projectDir := t.TempDir()
	mcpConfig := filepath.Join(projectDir, ".mcp.json")
	original := []byte(`{"mcpServers":{"stable":{"command":"stable"}}}`)
	if err := os.WriteFile(mcpConfig, original, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &CommandContext{
		CWD:    projectDir,
		Engine: fakeInspectionProvider{},
	}

	taskResult, err := executeTasks(ctx, "kill task-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(taskResult.Output, "read-only") {
		t.Fatalf("task mutation result = %q", taskResult.Output)
	}

	for _, args := range []string{
		"add unsafe command",
		"remove stable",
		"restart stable",
	} {
		result, err := executeMCP(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Output, "MCP mutation is unavailable") {
			t.Fatalf("mcp mutation %q result = %q", args, result.Output)
		}
	}
	after, err := os.ReadFile(mcpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("MCP mutation changed config: %q", after)
	}
}

func TestAgentMutationCommandIsTUIScoped(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	if registry.GetFor(EntrypointTUI, "agent") == nil {
		t.Fatal("/agent missing from TUI scope")
	}
	for _, entrypoint := range []Entrypoint{
		EntrypointPlain,
		EntrypointHeadless,
		EntrypointACP,
	} {
		if registry.GetFor(entrypoint, "agent") != nil {
			t.Fatalf("/agent unexpectedly visible on %s", entrypoint)
		}
	}
}

func assertInspectionOutputContains(
	t *testing.T,
	execute legacyCommandExecutor,
	ctx *CommandContext,
	args string,
	want ...string,
) {
	t.Helper()
	result, err := execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("inspection returned no result")
	}
	for _, fragment := range want {
		if !strings.Contains(result.Output, fragment) {
			t.Fatalf("output missing %q:\n%s", fragment, result.Output)
		}
	}
}
