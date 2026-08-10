package config

import (
	"os"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/model"
)

func TestValidateConfig_ValidSettings(t *testing.T) {
	// Set a valid API key for the default model (anthropic)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key-12345")

	s := DefaultSettings()
	vr := ValidateConfig(s)
	if vr.HasErrors() {
		t.Errorf("expected no errors for valid settings, got: %s", vr.String())
	}
}

func TestValidateConfig_EmptyModel(t *testing.T) {
	s := DefaultSettings()
	s.Model = ""
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for empty model")
	}
	errors := vr.Errors()
	found := false
	for _, e := range errors {
		if e.Field == "model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected model field error")
	}
}

func TestValidateConfig_UnknownModel(t *testing.T) {
	t.Setenv("PROV_API_KEY", "sk-test")

	s := DefaultSettings()
	s.Model = "totally-unknown-model-xyz"
	vr := ValidateConfig(s)
	// Should have a warning about unknown model
	if !vr.HasWarnings() {
		t.Error("expected warning for unknown model")
	}
	warnings := vr.Warnings()
	found := false
	for _, w := range warnings {
		if w.Field == "model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected model field warning")
	}
}

func TestValidateConfig_MissingAPIKey(t *testing.T) {
	// Clear all relevant env vars
	for _, envVar := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(envVar, "")
	}

	s := DefaultSettings()
	s.Model = "claude-sonnet-4-6"
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for missing API key")
	}
	errors := vr.Errors()
	found := false
	for _, e := range errors {
		if e.Field == "api_key" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected api_key field error")
	}
}

func TestValidateConfig_ZeroMaxTurnsIsUnlimited(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.MaxTurns = 0
	vr := ValidateConfig(s)
	for _, result := range vr.Errors() {
		if result.Field == "max_turns" {
			t.Fatalf("zero max_turns should be unlimited, got error: %s", result.Message)
		}
	}
}

func TestValidateConfig_NegativeMaxTurns(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.MaxTurns = -1
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for negative max_turns")
	}
}

func TestValidateConfig_HighMaxTurns(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.MaxTurns = 1000
	vr := ValidateConfig(s)
	if !vr.HasWarnings() {
		t.Error("expected warning for very high max_turns")
	}
}

func TestValidateConfig_InvalidMaxTokens(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.MaxTokens = -1
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for negative max_tokens")
	}
}

func TestValidateConfig_InvalidTemperature(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.Temperature = 3.0
	vr := ValidateConfig(s)
	if !vr.HasWarnings() {
		t.Error("expected warning for temperature out of range")
	}
}

func TestValidateConfig_InvalidPermissionMode(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.PermissionMode = "invalid_mode"
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for invalid permission mode")
	}
}

func TestValidateConfig_NegativeTimeout(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	s := DefaultSettings()
	s.Timeout = -1 * time.Second
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for negative timeout")
	}
}

func TestValidateConfig_NoToolSupport(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")

	s := DefaultSettings()
	s.Model = "deepseek-r1"
	vr := ValidateConfig(s)
	// Should warn about no tool support
	warnings := vr.Warnings()
	found := false
	for _, w := range warnings {
		if w.Field == "model" && w.Message != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected warning about no tool support for deepseek-r1")
	}
}

func TestValidationResults_Methods(t *testing.T) {
	vr := &ValidationResults{
		Results: []ValidationResult{
			{Field: "f1", Message: "error msg", Severity: SeverityError},
			{Field: "f2", Message: "warn msg", Severity: SeverityWarning},
			{Field: "f3", Message: "info msg", Severity: SeverityInfo},
		},
	}

	if !vr.HasErrors() {
		t.Error("HasErrors should be true")
	}
	if !vr.HasWarnings() {
		t.Error("HasWarnings should be true")
	}
	if vr.IsValid() {
		t.Error("IsValid should be false when there are errors")
	}
	if len(vr.Errors()) != 1 {
		t.Errorf("expected 1 error, got %d", len(vr.Errors()))
	}
	if len(vr.Warnings()) != 1 {
		t.Errorf("expected 1 warning, got %d", len(vr.Warnings()))
	}

	str := vr.String()
	if str == "" {
		t.Error("String() should not be empty")
	}
}

func TestValidationResults_Empty(t *testing.T) {
	vr := &ValidationResults{}
	if vr.HasErrors() {
		t.Error("empty results should not have errors")
	}
	if vr.HasWarnings() {
		t.Error("empty results should not have warnings")
	}
	if !vr.IsValid() {
		t.Error("empty results should be valid")
	}
	if vr.String() != "configuration is valid" {
		t.Errorf("unexpected string: %q", vr.String())
	}
}

func TestValidateConfig_CustomBaseURL(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_BASE_URL", "https://custom.example.com")

	s := DefaultSettings()
	vr := ValidateConfig(s)
	// Should have info about custom base URL
	found := false
	for _, r := range vr.Results {
		if r.Field == "base_url" && r.Severity == SeverityInfo {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected info about custom base URL")
	}
}

func TestValidateConfig_AllIssuesReported(t *testing.T) {
	// Clear all env vars
	for _, envVar := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY",
		"QWEN_API_KEY", "ARK_API_KEY", "PROV_API_KEY",
	} {
		t.Setenv(envVar, "")
	}

	s := &Settings{
		Model:          "claude-sonnet-4-6",
		MaxTurns:       0,
		MaxTokens:      -1,
		Temperature:    5.0,
		PermissionMode: "invalid",
		Timeout:        -1 * time.Second,
	}

	vr := ValidateConfig(s)
	// Should report ALL issues, not just the first
	if len(vr.Results) < 4 {
		t.Errorf("expected at least 4 validation results, got %d: %s", len(vr.Results), vr.String())
	}
}

// --- Provider Config Tests ---

func TestResolveProviderConfig_FromEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai-test-123")

	s := DefaultSettings()
	s.Model = "gpt-4o"
	pc := ResolveProviderConfig(s)

	if pc.Provider != model.ProviderOpenAI {
		t.Errorf("expected OpenAI provider, got %q", pc.Provider)
	}
	if pc.APIKey != "sk-openai-test-123" {
		t.Errorf("expected API key from env, got %q", pc.APIKey)
	}
	if pc.BaseURL == "" {
		t.Error("expected non-empty base URL")
	}
	if pc.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %q", pc.Model)
	}
}

func TestResolveProviderConfig_FallbackEnvVar(t *testing.T) {
	// ANTHROPIC_API_KEY not set, but PROV_API_KEY is
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("PROV_API_KEY", "sk-generic-key")

	s := DefaultSettings()
	s.Model = "claude-sonnet-4-6"
	pc := ResolveProviderConfig(s)

	if pc.Provider != model.ProviderAnthropic {
		t.Errorf("expected Anthropic provider, got %q", pc.Provider)
	}
	if pc.APIKey != "sk-generic-key" {
		t.Errorf("expected fallback API key, got %q", pc.APIKey)
	}
}

func TestResolveProviderConfig_BaseURLOverride(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_BASE_URL", "https://custom-openai.example.com/v1")

	s := DefaultSettings()
	s.Model = "gpt-4o"
	pc := ResolveProviderConfig(s)

	if pc.BaseURL != "https://custom-openai.example.com/v1" {
		t.Errorf("expected custom base URL from env, got %q", pc.BaseURL)
	}
}

func TestResolveProviderConfig_SettingsBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	// Don't set OPENAI_BASE_URL env var
	t.Setenv("OPENAI_BASE_URL", "")

	s := DefaultSettings()
	s.Model = "gpt-4o"
	s.APIBaseURL = "https://from-settings.example.com/v1"
	pc := ResolveProviderConfig(s)

	if pc.BaseURL != "https://from-settings.example.com/v1" {
		t.Errorf("expected base URL from settings, got %q", pc.BaseURL)
	}
}

func TestResolveProviderConfig_UnknownProvider(t *testing.T) {
	t.Setenv("PROV_API_KEY", "sk-generic")

	s := DefaultSettings()
	s.Model = "some-custom-model"
	pc := ResolveProviderConfig(s)

	if pc.Provider != model.ProviderUnknown {
		t.Errorf("expected unknown provider, got %q", pc.Provider)
	}
	if pc.APIKey != "sk-generic" {
		t.Errorf("expected PROV_API_KEY fallback, got %q", pc.APIKey)
	}
}

func TestResolveProviderConfig_NilSettings(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	pc := ResolveProviderConfig(nil)
	if pc == nil {
		t.Fatal("expected non-nil provider config")
		return
	}
	// Should use defaults
	if pc.Provider == "" {
		t.Error("expected a detected provider")
	}
}

func TestProviderConfig_IsConfigured(t *testing.T) {
	pc := &ProviderConfig{Provider: model.ProviderOpenAI, APIKey: "sk-test"}
	if !pc.IsConfigured() {
		t.Error("expected IsConfigured=true")
	}

	pc = &ProviderConfig{Provider: model.ProviderUnknown, APIKey: "sk-test"}
	if pc.IsConfigured() {
		t.Error("expected IsConfigured=false for unknown provider")
	}

	pc = &ProviderConfig{Provider: model.ProviderOpenAI, APIKey: ""}
	if pc.IsConfigured() {
		t.Error("expected IsConfigured=false for empty API key")
	}
}

func TestProviderConfig_ModelAliases(t *testing.T) {
	pc := &ProviderConfig{
		Model: "fast",
		ModelAliases: map[string]string{
			"fast":  "gpt-4o-mini",
			"smart": "claude-opus-4-6",
		},
	}

	if got := pc.ResolveModelAlias("fast"); got != "gpt-4o-mini" {
		t.Errorf("ResolveModelAlias(fast) = %q, want gpt-4o-mini", got)
	}
	if got := pc.ResolveModelAlias("smart"); got != "claude-opus-4-6" {
		t.Errorf("ResolveModelAlias(smart) = %q, want claude-opus-4-6", got)
	}
	if got := pc.ResolveModelAlias("unknown-alias"); got != "unknown-alias" {
		t.Errorf("ResolveModelAlias(unknown) = %q, want unknown-alias", got)
	}
	if got := pc.EffectiveModel(); got != "gpt-4o-mini" {
		t.Errorf("EffectiveModel() = %q, want gpt-4o-mini", got)
	}
}

func TestProviderConfig_SanitizedString(t *testing.T) {
	opaqueFixture := "sk-very-" + "secret-key-12345"
	pc := &ProviderConfig{
		Provider: model.ProviderOpenAI,
		Model:    "gpt-4o",
		APIKey:   opaqueFixture,
		BaseURL:  "https://api.openai.com/v1",
	}

	s := pc.SanitizedString()
	if s == "" {
		t.Error("SanitizedString should not be empty")
	}
	// Must NOT contain the full API key
	if contains(s, opaqueFixture) {
		t.Error("SanitizedString should not contain the full API key")
	}
	// Should contain masked version
	if !contains(s, "2345") {
		t.Error("SanitizedString should show last 4 chars of key")
	}
}

func TestProviderConfig_SanitizedString_NoKey(t *testing.T) {
	pc := &ProviderConfig{
		Provider: model.ProviderOpenAI,
		Model:    "gpt-4o",
		APIKey:   "",
		BaseURL:  "https://api.openai.com/v1",
	}

	s := pc.SanitizedString()
	if !contains(s, "(not set)") {
		t.Errorf("expected '(not set)' for empty key, got: %s", s)
	}
}

func TestProviderConfig_GoogleEnvVarFallback(t *testing.T) {
	// Test that Google provider checks multiple env vars
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-key-test")
	t.Setenv("PROV_API_KEY", "")

	s := DefaultSettings()
	s.Model = "gemini-2.5-pro"
	pc := ResolveProviderConfig(s)

	if pc.APIKey != "gemini-key-test" {
		t.Errorf("expected GEMINI_API_KEY fallback, got %q", pc.APIKey)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Ensure env vars are properly cleaned up (using t.Setenv handles this automatically).
func TestResolveProviderConfig_EnvVarPriority(t *testing.T) {
	// Both specific and generic env vars set — specific should win
	t.Setenv("OPENAI_API_KEY", "specific-key")
	t.Setenv("PROV_API_KEY", "generic-key")

	s := DefaultSettings()
	s.Model = "gpt-4o"
	pc := ResolveProviderConfig(s)

	if pc.APIKey != "specific-key" {
		t.Errorf("expected specific env var to win, got %q", pc.APIKey)
	}
}

func TestValidateConfig_OpenAIModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-openai")
	// Clear anthropic key to avoid confusing the validator
	for _, v := range []string{"ANTHROPIC_API_KEY", "PROV_API_KEY"} {
		t.Setenv(v, "")
	}

	s := DefaultSettings()
	s.Model = "gpt-4o"
	vr := ValidateConfig(s)
	if vr.HasErrors() {
		t.Errorf("expected no errors for valid OpenAI config, got: %s", vr.String())
	}
}

func TestValidateConfig_OpenAI_MissingKey(t *testing.T) {
	// Clear all potential API key sources
	for _, envVar := range []string{
		"OPENAI_API_KEY", "PROV_API_KEY",
	} {
		_ = os.Setenv(envVar, "")
		defer os.Unsetenv(envVar) //nolint:errcheck
	}

	s := DefaultSettings()
	s.Model = "gpt-4o"
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Error("expected error for missing OpenAI API key")
	}
}
