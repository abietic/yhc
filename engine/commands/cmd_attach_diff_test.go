package commands

import (
	"context"
	"strings"
	"testing"
)

func TestAttachDiffTUIIntentAndGrammar(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	for _, source := range []string{"", "all", "staged", "unstaged"} {
		result, err := registry.Dispatch(context.Background(), EntrypointTUI, &CommandContext{}, "/attach-diff "+source)
		if source == "" {
			source = "all"
		}
		if err != nil || result == nil || result.Action != ActionAttachDiff || result.Data["source"] != DiffContextSource(source) || result.Output != "" {
			t.Fatalf("TUI diff intent = %#v, %v", result, err)
		}
	}
	for _, source := range []string{"bad", "all extra", "staged file.txt"} {
		result, err := registry.Dispatch(context.Background(), EntrypointTUI, &CommandContext{}, "/attach-diff "+source)
		if err != nil || result.Action != ActionNone || !strings.Contains(result.Output, "Usage:") {
			t.Fatalf("invalid source = %#v, %v", result, err)
		}
	}
	for _, entrypoint := range []Entrypoint{EntrypointPlain, EntrypointHeadless, EntrypointACP, EntrypointAppServer} {
		if registry.GetFor(entrypoint, "attach-diff") != nil {
			t.Fatalf("TUI dialog command exposed in %s", entrypoint)
		}
	}
}
