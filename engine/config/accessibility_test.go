package config

import (
	"encoding/json"
	"testing"
)

func TestReducedMotionConfigLoadsAndMerges(t *testing.T) {
	var user Config
	if err := json.Unmarshal([]byte(`{"reduced_motion":true}`), &user); err != nil {
		t.Fatal(err)
	}
	got := MergeConfigs(&user, nil)
	if !got.ReducedMotion {
		t.Fatal("reduced_motion was not preserved by effective config merge")
	}
}

func TestProviderConfigFieldsMerge(t *testing.T) {
	user := &Config{
		Provider:      "anthropic",
		Model:         "claude-sonnet-4-6",
		APIBaseURL:    "https://user.example",
		FallbackModel: "claude-haiku-4-5-20251001",
		ModelAliases:  map[string]string{"fast": "claude-haiku-4-5-20251001", "smart": "claude-opus-4-6"},
	}
	project := &Config{
		Provider:      "openai",
		Model:         "gpt-4o",
		APIBaseURL:    "https://project.example/v1",
		FallbackModel: "google:gemini-2.5-flash",
		ModelAliases:  map[string]string{"fast": "openai:gpt-4o-mini"},
	}

	got := MergeConfigs(user, project)
	if got.Provider != "openai" || got.Model != "gpt-4o" || got.APIBaseURL != "https://project.example/v1" {
		t.Fatalf("provider config was not overridden: %#v", got)
	}
	if got.FallbackModel != "google:gemini-2.5-flash" {
		t.Fatalf("fallback model = %q", got.FallbackModel)
	}
	if got.ModelAliases["fast"] != "openai:gpt-4o-mini" || got.ModelAliases["smart"] != "claude-opus-4-6" {
		t.Fatalf("model aliases = %#v", got.ModelAliases)
	}
	if DefaultConfig().Model != "default" {
		t.Fatalf("default model = %q", DefaultConfig().Model)
	}
}
