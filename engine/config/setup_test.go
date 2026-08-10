package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/model"
)

func TestGenerateDefaultConfig_ValidJSON(t *testing.T) {
	output := GenerateDefaultConfig()
	if output == "" {
		t.Fatal("GenerateDefaultConfig() returned empty string")
	}

	// Verify it's valid JSON.
	var cfg Config
	if err := json.Unmarshal([]byte(output), &cfg); err != nil {
		t.Fatalf("GenerateDefaultConfig() produced invalid JSON: %v", err)
		return
	}

	// Verify expected defaults.
	if cfg.Model == "" {
		t.Error("expected non-empty model in default config")
	}
	if cfg.MaxTurns != 0 {
		t.Errorf("expected unlimited max_turns (0), got %d", cfg.MaxTurns)
	}
	if cfg.PermissionMode == "" {
		t.Error("expected non-empty permission_mode")
	}
}

func TestGenerateDefaultConfigWithComments(t *testing.T) {
	output := GenerateDefaultConfigWithComments()
	if output == "" {
		t.Fatal("GenerateDefaultConfigWithComments() returned empty string")
	}

	// Should contain explanatory comments.
	if !containsStr(output, "//") {
		t.Error("expected comments in output")
	}
	// Should contain key fields.
	if !containsStr(output, "model") {
		t.Error("expected 'model' field in output")
	}
	if !containsStr(output, "max_turns") {
		t.Error("expected 'max_turns' field in output")
	}
	if !containsStr(output, "permission_mode") {
		t.Error("expected 'permission_mode' field in output")
	}
}

func TestDetectExistingProviders_WithEnvVars(t *testing.T) {
	// Clear all provider env vars first.
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	// Set specific providers.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")

	detected := DetectExistingProviders()
	if len(detected) < 2 {
		t.Fatalf("expected at least 2 detected providers, got %d", len(detected))
	}

	// Anthropic should be first (priority sort).
	if detected[0].Provider != model.ProviderAnthropic {
		t.Errorf("expected Anthropic first, got %q", detected[0].Provider)
	}

	// Verify properties.
	for _, d := range detected {
		if !d.HasKey {
			t.Errorf("provider %q: expected HasKey=true", d.Provider)
		}
		if d.EnvVar == "" {
			t.Errorf("provider %q: expected non-empty EnvVar", d.Provider)
		}
		if d.DefaultModel == "" {
			t.Errorf("provider %q: expected non-empty DefaultModel", d.Provider)
		}
	}
}

func TestDetectExistingProviders_NoneConfigured(t *testing.T) {
	// Clear all env vars.
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	detected := DetectExistingProviders()
	if len(detected) != 0 {
		t.Errorf("expected 0 detected providers, got %d", len(detected))
	}
}

func TestSuggestProvider_AnthropicPreferred(t *testing.T) {
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENAI_API_KEY", "sk-oai-test")

	provider, modelName := SuggestProvider()
	if provider != model.ProviderAnthropic {
		t.Errorf("expected Anthropic suggestion, got %q", provider)
	}
	if modelName == "" {
		t.Error("expected non-empty model suggestion")
	}
}

func TestSuggestProvider_FallsBackToOpenAI(t *testing.T) {
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	t.Setenv("OPENAI_API_KEY", "sk-oai-test")

	provider, modelName := SuggestProvider()
	if provider != model.ProviderOpenAI {
		t.Errorf("expected OpenAI suggestion, got %q", provider)
	}
	if modelName == "" {
		t.Error("expected non-empty model suggestion")
	}
}

func TestSuggestProvider_NoneAvailable(t *testing.T) {
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	provider, modelName := SuggestProvider()
	if provider != model.ProviderUnknown {
		t.Errorf("expected ProviderUnknown, got %q", provider)
	}
	if modelName != "" {
		t.Errorf("expected empty model, got %q", modelName)
	}
}

func TestWriteDefaultConfig_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".claude", "settings.json")

	if err := WriteDefaultConfig(configPath); err != nil {
		t.Fatalf("WriteDefaultConfig() error: %v", err)
		return
	}

	// Verify file exists and is valid JSON.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
		return
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config file is not valid JSON: %v", err)
		return
	}

	if cfg.Model == "" {
		t.Error("expected non-empty model in written config")
	}
}

func TestWriteDefaultConfig_DoesNotOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "settings.json")

	// Create existing file.
	if err := os.WriteFile(configPath, []byte(`{"model":"existing"}`), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	err := WriteDefaultConfig(configPath)
	if err == nil {
		t.Error("expected error when file already exists")
	}

	// Verify original content unchanged.
	data, _ := os.ReadFile(configPath)
	if !containsStr(string(data), "existing") {
		t.Error("original file was overwritten")
	}
}

func TestProviderSetupSummary_WithProviders(t *testing.T) {
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	summary := ProviderSetupSummary()
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !containsStr(summary, "anthropic") {
		t.Error("expected summary to mention anthropic")
	}
	if !containsStr(summary, "Recommended") {
		t.Error("expected summary to include recommendation")
	}
}

func TestProviderSetupSummary_NoProviders(t *testing.T) {
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(v, "")
	}

	summary := ProviderSetupSummary()
	if !containsStr(summary, "No providers detected") {
		t.Errorf("expected 'No providers detected' in summary, got: %s", summary)
	}
}
