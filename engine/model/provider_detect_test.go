package model

import "testing"

func TestDetectProvider_Prefixes(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  ProviderID
	}{
		// Anthropic/Claude
		{"claude basic", "claude-sonnet-4-6", ProviderAnthropic},
		{"claude opus", "claude-opus-4-20250514", ProviderAnthropic},
		{"claude haiku", "claude-haiku-4-5-20251001", ProviderAnthropic},
		{"claude 3.5", "claude-3-5-sonnet-20241022", ProviderAnthropic},

		// OpenAI
		{"gpt-4o", "gpt-4o", ProviderOpenAI},
		{"gpt-4o-mini", "gpt-4o-mini", ProviderOpenAI},
		{"gpt-4-turbo", "gpt-4-turbo", ProviderOpenAI},
		{"o1", "o1", ProviderOpenAI},
		{"o1-mini", "o1-mini", ProviderOpenAI},
		{"o3", "o3", ProviderOpenAI},
		{"o3-mini", "o3-mini", ProviderOpenAI},
		{"o4-mini", "o4-mini", ProviderOpenAI},

		// Google
		{"gemini pro", "gemini-2.5-pro", ProviderGoogle},
		{"gemini flash", "gemini-2.5-flash", ProviderGoogle},
		{"gemini old", "gemini-1.5-pro", ProviderGoogle},

		// DeepSeek
		{"deepseek chat", "deepseek-chat", ProviderDeepSeek},
		{"deepseek v3", "deepseek-v3", ProviderDeepSeek},
		{"deepseek r1", "deepseek-r1", ProviderDeepSeek},
		{"deepseek v4 pro", "deepseek-v4-pro", ProviderDeepSeek},

		// Qwen
		{"qwen max", "qwen-max", ProviderQwen},
		{"qwen plus", "qwen-plus", ProviderQwen},
		{"qwen coder", "qwen2.5-coder-32b-instruct", ProviderQwen},

		// Ark/ByteDance
		{"doubao", "doubao-1.5-pro-32k", ProviderArk},
		{"ark endpoint", "ep-20240101001234", ProviderArk},

		// Unknown
		{"unknown model", "some-random-model", ProviderUnknown},
		{"empty", "", ProviderUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectProvider(tt.model)
			if got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestDetectProvider_ProviderPrefix(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  ProviderID
	}{
		{"agenticclaude prefix", "agenticclaude:claude-sonnet-4-6", ProviderAnthropic},
		{"agenticopenai prefix", "agenticopenai:gpt-4o", ProviderOpenAI},
		{"agenticgemini prefix", "agenticgemini:gemini-2.5-flash", ProviderGoogle},
		{"agenticdeepseek prefix", "agenticdeepseek:deepseek-v4-pro", ProviderDeepSeek},
		{"agenticqwen prefix", "agenticqwen:qwen-max", ProviderQwen},
		{"agenticark prefix", "agenticark:doubao-1.5-pro-32k", ProviderArk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectProvider(tt.model)
			if got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestDetectProvider_CaseInsensitive(t *testing.T) {
	tests := []struct {
		model string
		want  ProviderID
	}{
		{"Claude-Sonnet-4-6", ProviderAnthropic},
		{"GPT-4o", ProviderOpenAI},
		{"GEMINI-2.5-pro", ProviderGoogle},
		{"DeepSeek-Chat", ProviderDeepSeek},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := DetectProvider(tt.model)
			if got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestDetectProvider_VersionSuffixes(t *testing.T) {
	tests := []struct {
		model string
		want  ProviderID
	}{
		{"claude-opus-4-6[1m]", ProviderAnthropic},
		{"gpt-4o[2m]", ProviderOpenAI},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := DetectProvider(tt.model)
			if got != tt.want {
				t.Errorf("DetectProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestDetectProvider_BedrockARN(t *testing.T) {
	// Bedrock ARN-style model names
	got := DetectProvider("us.anthropic.claude-sonnet-4-20250514-v1:0")
	if got != ProviderAnthropic {
		t.Errorf("DetectProvider(bedrock ARN) = %q, want %q", got, ProviderAnthropic)
	}
}

func TestGetProviderEnvConfig(t *testing.T) {
	tests := []struct {
		provider   ProviderID
		wantNil    bool
		wantEnvVar string // first expected env var
	}{
		{ProviderAnthropic, false, "ANTHROPIC_API_KEY"},
		{ProviderOpenAI, false, "OPENAI_API_KEY"},
		{ProviderGoogle, false, "GOOGLE_API_KEY"},
		{ProviderDeepSeek, false, "DEEPSEEK_API_KEY"},
		{ProviderQwen, false, "DASHSCOPE_API_KEY"},
		{ProviderArk, false, "ARK_API_KEY"},
		{ProviderUnknown, true, ""},
		{"nonexistent", true, ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			cfg := GetProviderEnvConfig(tt.provider)
			if tt.wantNil {
				if cfg != nil {
					t.Errorf("expected nil for provider %q, got %+v", tt.provider, cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatalf("expected non-nil config for provider %q", tt.provider)
				return
			}
			if len(cfg.APIKeyEnvVars) == 0 {
				t.Errorf("provider %q has no API key env vars", tt.provider)
			}
			if cfg.APIKeyEnvVars[0] != tt.wantEnvVar {
				t.Errorf("provider %q first env var = %q, want %q", tt.provider, cfg.APIKeyEnvVars[0], tt.wantEnvVar)
			}
			if cfg.DefaultBaseURL == "" {
				t.Errorf("provider %q has empty default base URL", tt.provider)
			}
			if cfg.DefaultModel == "" {
				t.Errorf("provider %q has empty default model", tt.provider)
			}
		})
	}
}

func TestGetProviderEnvConfig_ReturnsCopy(t *testing.T) {
	cfg1 := GetProviderEnvConfig(ProviderOpenAI)
	cfg2 := GetProviderEnvConfig(ProviderOpenAI)
	if cfg1 == cfg2 {
		t.Error("GetProviderEnvConfig should return a copy, not the same pointer")
	}
	// Mutate cfg1 and verify cfg2 is not affected
	cfg1.APIKeyEnvVars[0] = "MUTATED"
	cfg2Again := GetProviderEnvConfig(ProviderOpenAI)
	if cfg2Again.APIKeyEnvVars[0] == "MUTATED" {
		t.Error("mutation of returned config affected the source")
	}
}

func TestAllProviderEnvConfigs(t *testing.T) {
	all := AllProviderEnvConfigs()
	if len(all) < 5 {
		t.Errorf("expected at least 5 providers, got %d", len(all))
	}
	for id, cfg := range all {
		if cfg.Provider != id {
			t.Errorf("config for %q has Provider=%q", id, cfg.Provider)
		}
	}
}

func TestDetectProviderForConfig(t *testing.T) {
	cfg := DetectProviderForConfig("gpt-4o")
	if cfg == nil {
		t.Fatal("expected non-nil config for gpt-4o")
		return
	}
	if cfg.Provider != ProviderOpenAI {
		t.Errorf("expected OpenAI provider, got %q", cfg.Provider)
	}

	cfg = DetectProviderForConfig("some-unknown-model")
	if cfg != nil {
		t.Errorf("expected nil for unknown model, got %+v", cfg)
	}
}

func TestCapabilities_SupportsTools(t *testing.T) {
	tests := []struct {
		model     string
		wantTools bool
	}{
		{"claude-sonnet-4-6", true},
		{"gpt-4o", true},
		{"deepseek-r1", false},
		{"deepseek-reasoner", false},
		{"gemini-2.5-pro", true},
		{"qwen-max", true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cap := GetCapabilities(tt.model)
			if cap.SupportsTools != tt.wantTools {
				t.Errorf("GetCapabilities(%q).SupportsTools = %v, want %v", tt.model, cap.SupportsTools, tt.wantTools)
			}
		})
	}
}

func TestCapabilities_SupportsStreaming(t *testing.T) {
	// All known models support streaming
	for name, cap := range modelTable {
		if !cap.SupportsStreaming {
			t.Errorf("model %q has SupportsStreaming=false; all current models should support streaming", name)
		}
	}
}

func TestCapabilities_SupportsSystemPrompt(t *testing.T) {
	// All known models support system prompts
	for name, cap := range modelTable {
		if !cap.SupportsSystemPrompt {
			t.Errorf("model %q has SupportsSystemPrompt=false; all current models should support system prompts", name)
		}
	}
}

func TestDefaultCapabilities_HasNewFields(t *testing.T) {
	cap := GetCapabilities("totally-unknown-model-xyz")
	if !cap.SupportsTools {
		t.Error("default capabilities should have SupportsTools=true")
	}
	if !cap.SupportsStreaming {
		t.Error("default capabilities should have SupportsStreaming=true")
	}
	if !cap.SupportsSystemPrompt {
		t.Error("default capabilities should have SupportsSystemPrompt=true")
	}
}
