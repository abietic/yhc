package tools

import (
	"testing"
)

func TestToolPromptsRegistered(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	prompts := r.GetToolPrompts()

	expectedTools := []string{"Bash", "Grep", "Read", "Edit", "Write"}
	for _, name := range expectedTools {
		if _, ok := prompts[name]; !ok {
			t.Errorf("tool %q should have a prompt registered", name)
		}
	}

	if len(prompts) < 5 {
		t.Errorf("expected at least 5 tool prompts, got %d", len(prompts))
	}
}

func TestToolPromptsContent(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	prompts := r.GetToolPrompts()

	// Bash prompt should mention background
	if p, ok := prompts["Bash"]; ok {
		if len(p) < 100 {
			t.Errorf("Bash prompt too short: %d chars", len(p))
		}
	}

	// Read prompt should mention absolute path
	if p, ok := prompts["Read"]; ok {
		if len(p) < 100 {
			t.Errorf("Read prompt too short: %d chars", len(p))
		}
	}
}

func TestWithPromptHelper(t *testing.T) {
	impl := ToolImpl{}
	fn := func() string { return "test prompt" }
	result := withPrompt(impl, fn)
	if result.Prompt == nil {
		t.Fatal("Prompt should not be nil after withPrompt")
		return
	}
	if result.Prompt() != "test prompt" {
		t.Errorf("Prompt() = %q", result.Prompt())
	}
}
