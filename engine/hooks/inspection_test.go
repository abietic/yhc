package hooks

import "testing"

func TestShellHookSnapshotIsDetachedFromRegisteredGeneration(t *testing.T) {
	executor := NewExecutor()
	config := &ShellHookConfig{
		Source: "/project/.claude/hooks.json",
		PreToolHooks: []ShellHook{{
			Command:     "check",
			ToolPattern: "Read",
			If: &ShellHookCondition{
				ToolName: "Read",
			},
		}},
	}
	executor.RegisterShellHooks(config)
	config.Source = "mutated"
	config.PreToolHooks[0].Command = "mutated"
	config.PreToolHooks[0].If.ToolName = "Write"

	snapshot := executor.ShellHookSnapshot()
	if snapshot.Source != "/project/.claude/hooks.json" ||
		len(snapshot.PreToolHooks) != 1 ||
		snapshot.PreToolHooks[0].Command != "check" ||
		snapshot.PreToolHooks[0].If == nil ||
		snapshot.PreToolHooks[0].If.ToolName != "Read" {
		t.Fatalf("shell hook snapshot = %#v", snapshot)
	}

	snapshot.PreToolHooks[0].Command = "changed"
	again := executor.ShellHookSnapshot()
	if again.PreToolHooks[0].Command != "check" {
		t.Fatalf("snapshot mutated live generation: %#v", again)
	}
}
