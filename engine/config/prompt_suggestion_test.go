package config

import "testing"

func TestPromptSuggestionsDefaultEnabledAndExplicitDisableMerges(t *testing.T) {
	defaults := DefaultConfig()
	if defaults.PromptSuggestions == nil || !*defaults.PromptSuggestions {
		t.Fatalf("default prompt_suggestions = %#v", defaults.PromptSuggestions)
	}

	disabled := false
	merged := MergeConfigs(&Config{PromptSuggestions: &disabled}, nil)
	if merged.PromptSuggestions == nil || *merged.PromptSuggestions {
		t.Fatalf("explicitly disabled prompt_suggestions = %#v", merged.PromptSuggestions)
	}
	disabled = true
	if *merged.PromptSuggestions {
		t.Fatal("merged prompt_suggestions retained the caller-owned pointer")
	}

	disabled = false
	enabled := true
	merged = MergeConfigs(
		&Config{PromptSuggestions: &disabled},
		&Config{PromptSuggestions: &enabled},
	)
	if merged.PromptSuggestions == nil || !*merged.PromptSuggestions {
		t.Fatalf("project prompt_suggestions override = %#v", merged.PromptSuggestions)
	}
}
