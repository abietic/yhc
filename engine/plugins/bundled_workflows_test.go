package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/commands"
)

type bundledWorkflowGolden struct {
	Input  string `json:"input"`
	CWD    string `json:"cwd"`
	Output string `json:"output"`
}

func TestBundledWorkflowPackPreservesGoldenPrompts(t *testing.T) {
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	loader := NewLoader()
	candidate, err := loader.BuildCommandGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Sources) != 1 ||
		candidate.Sources[0].Kind != commands.CommandSourceBundled ||
		candidate.Sources[0].Trust != commands.CommandTrustBundled ||
		candidate.Sources[0].Version != "2.0.0" ||
		len(candidate.Commands) != 2 {
		t.Fatalf("bundled candidate = %#v", candidate)
	}
	if _, err := registry.ReplacePromptCommandGeneration(candidate); err != nil {
		t.Fatal(err)
	}
	for _, command := range candidate.Commands {
		if command.Kind != commands.CommandKindPromptWorkflow ||
			command.SideEffect != commands.SideEffectNone ||
			command.ExecutionOwner != commands.ExecutionOwnerEntrypoint {
			t.Fatalf("workflow gained privileged execution metadata: %#v", command)
		}
	}

	data, err := os.ReadFile(filepath.Join("testdata", "bundled_workflow_prompts.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(defaultBundledWorkflowData), "/onboarding") ||
		strings.Contains(string(defaultBundledWorkflowData), ".claude") ||
		strings.Contains(string(data), "/onboarding") ||
		strings.Contains(string(data), ".claude") {
		t.Fatal("bundled workflow fixtures retained foreign onboarding guidance")
	}
	var golden []bundledWorkflowGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range golden {
		t.Run(fixture.Input, func(t *testing.T) {
			result, err := registry.Dispatch(
				context.Background(),
				commands.EntrypointTUI,
				&commands.CommandContext{CWD: fixture.CWD},
				fixture.Input,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Action != commands.ActionPrompt || result.Output != fixture.Output {
				t.Fatalf("result = %#v, want output %q", result, fixture.Output)
			}
		})
	}

	review := registry.Get("review")
	if review == nil || review.Source != "yhc-workflows" ||
		review.SourceVersion != "2.0.0" ||
		review.Trust != commands.CommandTrustBundled {
		t.Fatalf("review command metadata = %#v", review)
	}
	help := review.FormatHelpFor(commands.EntrypointTUI)
	if !strings.Contains(help, "Source: yhc-workflows@2.0.0") ||
		!strings.Contains(help, "Trust: bundled") {
		t.Fatalf("workflow help = %q", help)
	}
	if registry.GetFor(commands.EntrypointPlain, "review") == nil ||
		registry.GetFor(commands.EntrypointHeadless, "review") != nil ||
		registry.GetFor(commands.EntrypointACP, "review") != nil {
		t.Fatal("bundled workflow entrypoint scope drifted")
	}
}

func TestMalformedBundledWorkflowPackRetainsLiveGeneration(t *testing.T) {
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	if err := NewLoader().RegisterCommands(registry); err != nil {
		t.Fatal(err)
	}
	before := registry.PromptCommandGeneration()
	stable, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{},
		"/review",
	)
	if err != nil {
		t.Fatal(err)
	}

	invalid := NewLoaderWithOptions(LoaderOptions{
		BundledWorkflowData: []byte(`{"schemaVersion":1,"id":"broken"}`),
	})
	if err := invalid.RegisterCommands(registry); err == nil {
		t.Fatal("malformed bundled pack replaced the live generation")
	}
	after := registry.PromptCommandGeneration()
	if after.Revision != before.Revision || after.Digest != before.Digest {
		t.Fatalf("failed reload changed generation: before=%#v after=%#v", before, after)
	}
	retained, err := registry.Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{},
		"/review",
	)
	if err != nil || retained.Output != stable.Output {
		t.Fatalf("retained workflow = %#v, %v", retained, err)
	}
}

func TestDisablingBundledWorkflowPackKeepsCoreCommands(t *testing.T) {
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	loader := NewLoaderWithOptions(LoaderOptions{DisableBundledWorkflows: true})
	if err := loader.RegisterCommands(registry); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"commit", "review"} {
		if registry.Get(name) != nil {
			t.Fatalf("disabled bundled workflow %q remained registered", name)
		}
	}
	for _, name := range []string{"pr-comments", "summary", "issue", "onboarding", "commit-push-pr", "cpr"} {
		if registry.GetRemoved(name) == nil {
			t.Fatalf("removed bundled workflow %q lost its tombstone", name)
		}
	}
	core := registry.Get("help")
	if core == nil || core.Source != string(commands.CommandSourceCore) ||
		core.Trust != commands.CommandTrustCore {
		t.Fatalf("core command changed after disabling pack: %#v", core)
	}
	generation := registry.PromptCommandGeneration()
	if generation.Commands != 0 || len(generation.Sources) != 0 {
		t.Fatalf("disabled generation = %#v", generation)
	}
}
