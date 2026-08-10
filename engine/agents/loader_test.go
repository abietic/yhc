package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMarkdownAgent(t *testing.T) {
	dir := t.TempDir()
	agentContent := `---
description: An exploration agent
model: claude-sonnet-4-6
permissionMode: plan
maxTurns: 50
tools:
  - Read
  - Glob
  - Grep
---
You are an exploration agent. Search the codebase to answer questions.
`
	_ = os.WriteFile(filepath.Join(dir, "explore.md"), []byte(agentContent), 0o644)

	loader := NewAgentLoader(dir)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
		return
	}

	agent, ok := loader.Get("explore")
	if !ok {
		t.Fatal("agent 'explore' not found")
	}
	if agent.Description != "An exploration agent" {
		t.Errorf("description = %q", agent.Description)
	}
	if agent.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", agent.Model)
	}
	if agent.PermissionMode != "plan" {
		t.Errorf("permissionMode = %q", agent.PermissionMode)
	}
	if agent.MaxTurns != 50 {
		t.Errorf("maxTurns = %d", agent.MaxTurns)
	}
	if len(agent.Tools) != 3 {
		t.Errorf("tools = %v", agent.Tools)
	}
	if agent.Prompt == "" {
		t.Error("prompt should not be empty")
	}
}

func TestLoadJSONAgents(t *testing.T) {
	dir := t.TempDir()
	jsonContent := `{
  "reviewer": {
    "description": "Code review agent",
    "prompt": "You review code for bugs",
    "tools": ["Read", "Grep"],
    "maxTurns": 20
  },
  "planner": {
    "description": "Planning agent",
    "prompt": "You create implementation plans"
  }
}`
	_ = os.WriteFile(filepath.Join(dir, "agents.json"), []byte(jsonContent), 0o644)

	loader := NewAgentLoader(dir)
	_ = loader.Load()

	if _, ok := loader.Get("reviewer"); !ok {
		t.Error("reviewer not found")
	}
	if _, ok := loader.Get("planner"); !ok {
		t.Error("planner not found")
	}

	reviewer, _ := loader.Get("reviewer")
	if reviewer.Description != "Code review agent" {
		t.Errorf("description = %q", reviewer.Description)
	}
	if reviewer.MaxTurns != 20 {
		t.Errorf("maxTurns = %d", reviewer.MaxTurns)
	}
}

func TestAgentLoaderRejectsNegativeMaxTurns(t *testing.T) {
	dir := t.TempDir()
	content := "---\ndescription: invalid\nmaxTurns: -1\n---\nprompt\n"
	if err := os.WriteFile(filepath.Join(dir, "invalid.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	loader := NewAgentLoader(dir)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
		return
	}
	if _, ok := loader.Get("invalid"); ok {
		t.Fatal("agent with negative maxTurns was loaded")
	}
}

func TestLoadMultipleDirectories(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir1, "agent1.md"), []byte("---\ndescription: First\n---\nPrompt 1"), 0o644)
	_ = os.WriteFile(filepath.Join(dir2, "agent2.md"), []byte("---\ndescription: Second\n---\nPrompt 2"), 0o644)

	loader := NewAgentLoader(dir1, dir2)
	_ = loader.Load()

	names := loader.Names()
	if len(names) != 2 {
		t.Errorf("got %d agents, want 2", len(names))
	}
}

func TestCaseInsensitiveLookup(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "MyAgent.md"), []byte("---\ndescription: Test\n---\nPrompt"), 0o644)

	loader := NewAgentLoader(dir)
	_ = loader.Load()

	if _, ok := loader.Get("myagent"); !ok {
		t.Error("case-insensitive lookup failed")
	}
	if _, ok := loader.Get("MYAGENT"); !ok {
		t.Error("uppercase lookup failed")
	}
}

func TestParseFrontmatter(t *testing.T) {
	content := "---\nkey: value\nnum: 42\n---\nBody text here"
	fm, body := parseFrontmatter(content)
	if fm == nil {
		t.Fatal("frontmatter should not be nil")
		return
	}
	if fm["key"] != "value" {
		t.Errorf("key = %v", fm["key"])
	}
	if body == "" {
		t.Error("body should not be empty")
	}
}

func TestParseFrontmatterNone(t *testing.T) {
	content := "Just a regular markdown file"
	fm, body := parseFrontmatter(content)
	if fm != nil {
		t.Error("frontmatter should be nil for non-frontmatter content")
	}
	if body != content {
		t.Error("body should be the full content")
	}
}

func TestEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	loader := NewAgentLoader(dir)
	_ = loader.Load()

	if len(loader.List()) != 0 {
		t.Error("empty dir should produce no agents")
	}
}

func TestDefaultAgentDirs(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".claude", "agents"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, ".agents"), 0o755)

	dirs := DefaultAgentDirs(dir, "")
	if len(dirs) != 2 {
		t.Errorf("got %d dirs, want 2", len(dirs))
	}
}

func TestAgentWithAllFields(t *testing.T) {
	dir := t.TempDir()
	content := `---
description: Full agent
agentType: custom
whenToUse: When you need everything
model: gpt-4
permissionMode: auto
maxTurns: 100
isolation: worktree
background: true
memory: project
initialPrompt: Start by reading README
tools:
  - Read
  - Write
disallowedTools:
  - Bash
skills:
  - review
  - test
---
Full prompt body.
`
	_ = os.WriteFile(filepath.Join(dir, "full.md"), []byte(content), 0o644)

	loader := NewAgentLoader(dir)
	_ = loader.Load()

	a, ok := loader.Get("full")
	if !ok {
		t.Fatal("full agent not found")
	}
	if a.AgentType != "custom" {
		t.Errorf("agentType = %q", a.AgentType)
	}
	if a.Isolation != "worktree" {
		t.Errorf("isolation = %q", a.Isolation)
	}
	if !a.Background {
		t.Error("background should be true")
	}
	if a.Memory != "project" {
		t.Errorf("memory = %q", a.Memory)
	}
	if len(a.DisallowedTools) != 1 || a.DisallowedTools[0] != "Bash" {
		t.Errorf("disallowedTools = %v", a.DisallowedTools)
	}
	if len(a.Skills) != 2 {
		t.Errorf("skills = %v", a.Skills)
	}
}

func TestBuiltInAgents(t *testing.T) {
	agents := BuiltInAgents()
	if len(agents) < 3 {
		t.Fatalf("expected at least 3 built-in agents, got %d", len(agents))
	}

	names := make(map[string]bool)
	for _, a := range agents {
		names[a.Name] = true
		if a.Source != "built-in" {
			t.Errorf("agent %q source = %q, want built-in", a.Name, a.Source)
		}
	}
	if !names["general-purpose"] {
		t.Error("missing general-purpose built-in agent")
	}
	if !names["Explore"] {
		t.Error("missing Explore built-in agent")
	}
	if !names["Plan"] {
		t.Error("missing Plan built-in agent")
	}
}

func TestLoadWithBuiltIns(t *testing.T) {
	dir := t.TempDir()
	// Write a custom agent that overrides Explore
	agentContent := `---
name: Explore
description: Custom explore agent
model: custom-model
---
My custom explore prompt`

	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "explore.md"), []byte(agentContent), 0o644)

	loader := NewAgentLoader(dir)
	if err := loader.LoadWithBuiltIns(); err != nil {
		t.Fatal(err)
		return
	}

	// Custom should override built-in
	agent, ok := loader.Get("explore")
	if !ok {
		t.Fatal("explore agent not found")
	}
	if agent.Model != "custom-model" {
		t.Errorf("model = %q, want custom-model (override)", agent.Model)
	}

	// Built-in general-purpose should still be present
	gp, ok := loader.Get("general-purpose")
	if !ok {
		t.Fatal("general-purpose agent not found after LoadWithBuiltIns")
	}
	if gp.Source != "built-in" {
		t.Errorf("general-purpose source = %q, want built-in", gp.Source)
	}
}

func TestNameFromFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// File named "foo.md" but frontmatter says name: "bar"
	content := `---
name: bar
description: A bar agent
---
Bar prompt`

	_ = os.WriteFile(filepath.Join(dir, "foo.md"), []byte(content), 0o644)

	loader := NewAgentLoader(dir)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
		return
	}

	// Should be findable by frontmatter name
	agent, ok := loader.Get("bar")
	if !ok {
		t.Fatal("agent should be findable by frontmatter name 'bar'")
	}
	if agent.Name != "bar" {
		t.Errorf("name = %q, want bar", agent.Name)
	}
	if agent.Filename != "foo" {
		t.Errorf("filename = %q, want foo", agent.Filename)
	}
}

func TestMcpServersField(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: mcp-agent
description: Agent with MCP
mcpServers:
  - slack
  - github
---
MCP prompt`

	_ = os.WriteFile(filepath.Join(dir, "mcp-agent.md"), []byte(content), 0o644)

	loader := NewAgentLoader(dir)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
		return
	}

	agent, ok := loader.Get("mcp-agent")
	if !ok {
		t.Fatal("mcp-agent not found")
	}
	if len(agent.McpServers) != 2 {
		t.Fatalf("mcpServers = %v, want 2 entries", agent.McpServers)
	}
	if agent.McpServers[0] != "slack" || agent.McpServers[1] != "github" {
		t.Errorf("mcpServers = %v", agent.McpServers)
	}
}
