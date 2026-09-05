package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/skills"
)

func TestSkillCommandsProjectionAndDispatch(t *testing.T) {
	source := skills.NewSkillRegistry()
	source.Register(&skills.Skill{Name: "check", Description: "Check project", Content: "Check {{target}}", Args: []skills.SkillArg{{Name: "target", Required: true}}})
	registry := NewRegistry()
	RegisterDefaults(registry)
	registry.SetSkillRegistry(source)
	cmd := registry.GetForContext(context.Background(), EntrypointTUI, nil, "check")
	if cmd == nil || cmd.Name != "skill:check" || cmd.ArgumentHint() != "<target>" {
		t.Fatalf("skill command = %#v", cmd)
	}
	result, err := registry.Dispatch(context.Background(), EntrypointTUI, nil, `/check "文件 with spaces"`)
	if err != nil || result == nil || result.Action != ActionPrompt || !strings.Contains(result.Output, "Check 文件 with spaces") {
		t.Fatalf("dispatch = %#v, %v", result, err)
	}
	if _, err := registry.Dispatch(context.Background(), EntrypointTUI, nil, "/skill:check"); err == nil {
		t.Fatal("required argument admitted")
	}
	source.Register(&skills.Skill{Name: "check", Content: "updated"})
	result, err = registry.Dispatch(context.Background(), EntrypointTUI, nil, "/skill:check")
	if err != nil || !strings.Contains(result.Output, "updated") {
		t.Fatalf("stale skill: %#v %v", result, err)
	}
	for _, list := range [][]*Command{registry.List(), registry.ListFor(EntrypointTUI), registry.ListForContext(context.Background(), EntrypointTUI, nil)} {
		found := false
		for _, cmd := range list {
			if cmd.Name == "skill:check" {
				found = true
			}
		}
		if !found {
			t.Fatal("skill missing from discovery")
		}
	}
}

func TestSkillCommandsPreserveReservedNamesAndSnapshot(t *testing.T) {
	source := skills.NewSkillRegistry()
	for _, name := range []string{"help", "exit", "issue", "check"} {
		source.Register(&skills.Skill{Name: name, Content: "original"})
	}
	registry := NewRegistry()
	RegisterDefaults(registry)
	registry.SetSkillRegistry(source)
	if registry.Get("help").Name != "help" || registry.Get("exit").Name != "quit" || registry.Get("issue") != nil {
		t.Fatal("skill shadowed reserved command")
	}
	for _, name := range []string{"help", "exit", "issue"} {
		if registry.Get("skill:"+name) == nil {
			t.Fatalf("qualified skill %s unavailable", name)
		}
	}
	cmd := registry.Get("check")
	source.Register(&skills.Skill{Name: "check", Content: "replacement"})
	result, err := cmd.Execute(context.Background(), &CommandContext{})
	if err != nil || !strings.Contains(result.Output, "original") || strings.Contains(result.Output, "replacement") {
		t.Fatalf("snapshot changed: %#v %v", result, err)
	}
	if registry.GetFor(EntrypointACP, "check") != nil {
		t.Fatal("unsupported ACP skill surfaced")
	}
	active := &CommandContext{Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn}}
	if registry.GetForContext(context.Background(), EntrypointTUI, active, "check") != nil {
		t.Fatal("skill surfaced during active turn")
	}
	registry.SetSkillRegistry(nil)
	if registry.Get("check") != nil {
		t.Fatal("old engine skills survived rebind")
	}
}

func TestSkillCommandsFlagsArgumentsAndCollisions(t *testing.T) {
	hidden := false
	source := skills.NewSkillRegistry()
	source.Register(&skills.Skill{Name: "hidden", Content: "secret", UserInvocable: &hidden})
	source.Register(&skills.Skill{Name: "manual", Content: "{{mode}}: $ARGUMENTS", ArgumentHint: "[mode]", DisableModelInvocation: true, Args: []skills.SkillArg{{Name: "mode", Default: "safe"}}})
	source.Register(&skills.Skill{Name: "skill:manual", Content: "nested"})
	source.Register(&skills.Skill{Name: "中文", Content: "unicode skill"})
	registry := NewRegistry()
	registry.SetSkillRegistry(source)
	if registry.Get("中文") != nil || registry.Get("skill:中文") == nil {
		t.Fatal("non-ASCII skill must use its parseable qualified name")
	}
	if registry.Get("hidden") != nil || registry.Get("skill:hidden") != nil {
		t.Fatal("user-invocable false was exposed")
	}
	if cmd := registry.Get("manual"); cmd == nil || cmd.ArgumentHint() != "[mode]" {
		t.Fatalf("manual hint = %#v", cmd)
	}
	for _, tc := range []struct{ input, want string }{{"/manual", "Skill: manual\n\nsafe: "}, {`/manual "$ARGUMENTS" extra`, "Skill: manual\n\n$ARGUMENTS: $ARGUMENTS extra"}, {"/skill:skill:manual", "Skill: skill:manual\n\nnested"}} {
		result, err := registry.Dispatch(context.Background(), EntrypointPlain, nil, tc.input)
		if err != nil || result.Output != tc.want {
			t.Fatalf("%s: %#v %v; want %q", tc.input, result, err, tc.want)
		}
	}
	if registry.Get("skill:manual").Name != "skill:manual" {
		t.Fatal("bare alias stole qualified name")
	}
}

func TestSkillCommandsConcurrentDiscoveryAndRebind(t *testing.T) {
	source := skills.NewSkillRegistry()
	source.Register(&skills.Skill{Name: "check", Content: "version"})
	registry := NewRegistry()
	registry.SetSkillRegistry(source)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			registry.SetSkillRegistry(source)
			source.Register(&skills.Skill{Name: "check", Content: "version"})
		}
	}()
	for range 100 {
		snapshot := registry.DiscoverySnapshotForContext(context.Background(), EntrypointTUI, nil)
		if len(snapshot.Rows) != 1 || snapshot.Rows[0].Name != "skill:check" {
			t.Errorf("snapshot = %#v", snapshot)
		}
		result, err := registry.Dispatch(context.Background(), EntrypointTUI, nil, "/check")
		if err != nil || result.Output != "Skill: check\n\nversion" {
			t.Errorf("dispatch %#v %v", result, err)
		}
	}
	<-done
}

func TestSkillCommandsReserveQualifiedNamespaceAcrossPluginReload(t *testing.T) {
	registry := NewRegistry()
	source := skills.NewSkillRegistry()
	source.Register(&skills.Skill{Name: "check", Content: "skill content"})
	registry.SetSkillRegistry(source)
	for _, tc := range []struct {
		name    string
		aliases []string
	}{{"skill:check", nil}, {"plugin:check", []string{"skill:check"}}} {
		command := &Command{Name: tc.name, Aliases: tc.aliases, Source: "plugin:skill", Trust: CommandTrustConfigured, Execute: p233TestHandler}
		if err := registry.Register(command); err == nil {
			t.Fatal("static command stole skill namespace")
		}
		candidate := PromptCommandGenerationCandidate{Digest: "conflicting", Commands: []*Command{command}}
		if _, err := registry.ValidatePromptCommandGeneration(candidate); err == nil {
			t.Fatal("validation accepted skill namespace")
		}
		if _, err := registry.ReplacePromptCommandGeneration(candidate); err == nil {
			t.Fatal("reload accepted skill namespace")
		}
		if registry.Get("check") == nil || registry.Get("skill:check").Source != "skill:runtime" {
			t.Fatal("failed reload lost skill")
		}
	}
	if err := registry.RegisterRemoved(&RemovedCommand{Name: "skill:check", Reason: "fixture", RemovedIn: "fixture"}); err == nil {
		t.Fatal("tombstone reserved skill name")
	}
}

func TestSkillCommandsExpansionCannotBecomeAnotherCommand(t *testing.T) {
	source := skills.NewSkillRegistry()
	source.Register(&skills.Skill{Name: "inspect", Content: "/new"})
	registry := NewRegistry()
	RegisterDefaults(registry)
	registry.SetSkillRegistry(source)
	result, err := registry.Dispatch(context.Background(), EntrypointTUI, nil, "/inspect")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ActionPrompt || IsCommand(result.Output) || result.Output != "Skill: inspect\n\n/new" {
		t.Fatalf("skill body was reclassified: %#v", result)
	}
}
