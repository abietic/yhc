package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/memdir"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestLoadAgentDefinitionsLoadsProjectAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	agentsDir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	content := `---
name: verifier
description: Verify a change without modifying files
tools: Read, Grep, Glob
disallowedTools:
  - Edit
  - Write
permissionMode: plan
maxTurns: 18
---
You are a verification specialist. Report evidence for every conclusion.
`
	path := filepath.Join(agentsDir, "verifier.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	defs, errs := LoadAgentDefinitions(root)
	if len(errs) != 0 {
		t.Fatalf("LoadAgentDefinitions errors: %v", errs)
	}
	def, ok := defs["verifier"]
	if !ok {
		t.Fatal("project agent was not loaded")
	}
	if def.Source != agentSourceProject || def.FilePath != path {
		t.Fatalf("unexpected source: %#v", def)
	}
	if def.MaxTurns != 18 || !def.ReadOnly || def.PermissionMode != "plan" {
		t.Fatalf("frontmatter was not applied: %#v", def)
	}
	if def.SystemPrompt != "You are a verification specialist. Report evidence for every conclusion." {
		t.Fatalf("unexpected prompt: %q", def.SystemPrompt)
	}

	exec := &SubAgentExecutor{AgentDefinitions: defs}
	if got := exec.defaultMaxTurns("VERIFIER"); got != 18 {
		t.Fatalf("defaultMaxTurns = %d, want 18", got)
	}
	if got := exec.agentTypeDescription("verifier"); got != def.SystemPrompt {
		t.Fatalf("agentTypeDescription = %q, want custom prompt", got)
	}
}

func TestCustomAgentMemoryMetadataAddsToolsAndPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := `---
name: remembering-reviewer
description: Review code and retain durable feedback
tools: Read, Grep
memory: project
---
Review the requested change.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	defs, errs := LoadAgentDefinitions(root)
	if len(errs) != 0 {
		t.Fatalf("load errors: %v", errs)
	}
	def := defs["remembering-reviewer"]
	if def.Memory != memdir.ScopeProject || def.ReadOnly {
		t.Fatalf("memory definition = %#v", def)
	}
	for _, tool := range []string{"Read", "Grep", "Edit", "Write"} {
		if !slices.Contains(def.Tools, tool) {
			t.Fatalf("memory definition missing %s: %#v", tool, def.Tools)
		}
	}
	entrypoint := filepath.Join(memdir.GetAgentMemoryDirForProject(def.Name, def.Memory, root), "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("remember this review rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := &SubAgentExecutor{
		CWD:                    root,
		MemoryProjectRoot:      root,
		EnablePersistentMemory: true,
		ToolRegistry:           tools.NewRegistry(),
		AgentDefinitions:       defs,
	}
	prompt := exec.buildSystemPrompt(def.Name, "", root, nil)
	for _, want := range []string{"Review the requested change.", "Persistent Agent Memory", "remember this review rule"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("agent prompt missing %q: %q", want, prompt)
		}
	}
}

func TestCustomAgentRejectsUnknownMemoryScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.md")
	definition := `---
name: invalid-memory
description: Invalid memory scope
memory: organization
---
Do work.
`
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCustomAgentDefinition(path, agentSourceProject); err == nil || !strings.Contains(err.Error(), "unknown memory scope") {
		t.Fatalf("expected unknown memory error, got %v", err)
	}
}

func TestAgentMemorySnapshotInitializationAndExplicitResolution(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YHC_CONFIG_DIR", configDir)
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := `---
name: snapshot-agent
description: Uses seeded user memory
memory: user
---
Use durable memory.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "snapshot.md"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSnapshot := func(timestamp, content string) {
		t.Helper()
		dir := memdir.GetAgentSnapshotDir("snapshot-agent", root)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{"updatedAt":"`+timestamp+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	newExecutor := func() *SubAgentExecutor {
		t.Helper()
		exec := NewSubAgentExecutor(nil, tools.NewRegistry(), root)
		exec.MemoryProjectRoot = root
		exec.EnablePersistentMemory = true
		return exec
	}

	writeSnapshot("2026-07-12T01:00:00Z", "initial seed")
	exec := newExecutor()
	if errs := exec.InitializeAgentMemorySnapshots(); len(errs) != 0 {
		t.Fatalf("initialize errors: %v", errs)
	}
	localPath := filepath.Join(memdir.GetAgentMemoryDirForProject("snapshot-agent", memdir.ScopeUser, root), "MEMORY.md")
	if data, err := os.ReadFile(localPath); err != nil || string(data) != "initial seed" {
		t.Fatalf("initial local memory = %q err=%v", data, err)
	}

	writeSnapshot("2026-07-13T01:00:00Z", "newer seed")
	exec = newExecutor()
	if errs := exec.InitializeAgentMemorySnapshots(); len(errs) != 0 {
		t.Fatalf("pending discovery errors: %v", errs)
	}
	if pending := exec.PendingAgentMemorySnapshots(); pending["snapshot-agent"].SnapshotTimestamp != "2026-07-13T01:00:00Z" {
		t.Fatalf("pending snapshots = %#v", pending)
	}
	if err := exec.ResolveAgentMemorySnapshot("snapshot-agent", AgentSnapshotKeep); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(localPath); err != nil || string(data) != "initial seed" {
		t.Fatalf("keep changed local memory = %q err=%v", data, err)
	}

	writeSnapshot("2026-07-14T01:00:00Z", "replacement seed")
	exec = newExecutor()
	if errs := exec.InitializeAgentMemorySnapshots(); len(errs) != 0 {
		t.Fatalf("replace discovery errors: %v", errs)
	}
	if err := exec.ResolveAgentMemorySnapshot("SNAPSHOT-AGENT", AgentSnapshotReplace); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(localPath); err != nil || string(data) != "replacement seed" {
		t.Fatalf("replace local memory = %q err=%v", data, err)
	}
	if pending := exec.PendingAgentMemorySnapshots(); len(pending) != 0 {
		t.Fatalf("pending snapshots after resolution = %#v", pending)
	}
}

func TestAgentMemorySnapshotAutoInitializationIsUserScopeOnly(t *testing.T) {
	t.Setenv("YHC_CONFIG_DIR", t.TempDir())
	t.Setenv("YHC_REMOTE_MEMORY_DIR", "")
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	root := t.TempDir()
	defs := map[string]BuiltInAgentDef{
		"user-agent": {
			Name:   "user-agent",
			Source: agentSourceProject,
			Memory: memdir.ScopeUser,
		},
		"project-agent": {
			Name:   "project-agent",
			Source: agentSourceProject,
			Memory: memdir.ScopeProject,
		},
		"local-agent": {
			Name:   "local-agent",
			Source: agentSourceProject,
			Memory: memdir.ScopeLocal,
		},
	}
	for name := range defs {
		dir := memdir.GetAgentSnapshotDir(name, root)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(`{"updatedAt":"2026-07-12T01:00:00Z"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if errs := initializeAgentMemorySnapshots(defs, root); len(errs) != 0 {
		t.Fatalf("initialization errors: %v", errs)
	}
	userPath := filepath.Join(memdir.GetAgentMemoryDirForProject("user-agent", memdir.ScopeUser, root), "MEMORY.md")
	if data, err := os.ReadFile(userPath); err != nil || string(data) != "user-agent" {
		t.Fatalf("user memory = %q err=%v", data, err)
	}
	for _, scope := range []memdir.AgentMemoryScope{memdir.ScopeProject, memdir.ScopeLocal} {
		name := string(scope) + "-agent"
		path := filepath.Join(memdir.GetAgentMemoryDirForProject(name, scope, root), "MEMORY.md")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s snapshot initialized unexpectedly: %v", scope, err)
		}
	}
}

func TestBuiltInAgentDefinitionsDefaultUnlimited(t *testing.T) {
	for name, def := range BuiltInAgentDefs {
		if def.MaxTurns != 0 {
			t.Errorf("%s MaxTurns = %d, want unlimited (0)", name, def.MaxTurns)
		}
	}
}

func TestBuiltInReadOnlyAgentDefinitionsMatchRegisteredToolContract(t *testing.T) {
	want := []string{"Read", "Glob", "Grep", "Bash"}
	for _, name := range []string{"Explore", "Plan"} {
		if got := BuiltInAgentDefs[name].Tools; !slices.Equal(got, want) {
			t.Fatalf("%s tools = %q, want %q", name, got, want)
		}
	}
}

func TestSubAgentPermissionModePrecedence(t *testing.T) {
	exec := &SubAgentExecutor{
		PermissionMode: permission.ModeDefault,
		AgentDefinitions: map[string]BuiltInAgentDef{
			"defined": {PermissionMode: "plan"},
		},
	}

	tests := []struct {
		name string
		opts tools.AgentExecOptions
		want permission.Mode
	}{
		{
			name: "runtime parent transition is inherited",
			opts: tools.AgentExecOptions{InheritedPermissionMode: "bypassPermissions"},
			want: permission.ModeBypassPermissions,
		},
		{
			name: "agent definition overrides inherited mode",
			opts: tools.AgentExecOptions{SubagentType: "defined", InheritedPermissionMode: "bypassPermissions"},
			want: permission.ModePlan,
		},
		{
			name: "explicit spawn mode overrides definition and inherited mode",
			opts: tools.AgentExecOptions{SubagentType: "defined", Mode: "auto", InheritedPermissionMode: "bypassPermissions"},
			want: permission.ModeAuto,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exec.resolvePermissionMode(tt.opts); got != tt.want {
				t.Fatalf("resolvePermissionMode() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("startup bypass fallback remains inherited", func(t *testing.T) {
		exec.PermissionMode = permission.ModeBypassPermissions
		if got := exec.resolvePermissionMode(tools.AgentExecOptions{}); got != permission.ModeBypassPermissions {
			t.Fatalf("resolvePermissionMode() = %q, want %q", got, permission.ModeBypassPermissions)
		}
	})
}

func TestLoadAgentDefinitionsProjectOverridesBuiltIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	agentsDir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	content := `---
name: Explore
description: Project-specific exploration agent
maxTurns: 40
---
Use the project's exploration procedure.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "explore.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	defs, errs := LoadAgentDefinitions(root)
	if len(errs) != 0 {
		t.Fatalf("LoadAgentDefinitions errors: %v", errs)
	}
	if defs["Explore"].Source != agentSourceProject || defs["Explore"].MaxTurns != 40 {
		t.Fatalf("project definition did not override built-in: %#v", defs["Explore"])
	}
}

func TestLoadAgentDefinitionsSkipsDocumentationAndReportsMalformedAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	agentsDir := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "README.md"), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	bad := "---\nname: bad\n---\nprompt\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	defs, errs := LoadAgentDefinitions(root)
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want one parse error", errs)
	}
	if _, ok := defs["bad"]; ok {
		t.Fatal("malformed agent should not be active")
	}
}
