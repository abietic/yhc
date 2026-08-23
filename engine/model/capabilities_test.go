package model

import "testing"

func TestGetCapabilities_KnownModel(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		wantContext    int
		wantMaxOutput  int
		wantImages     bool
		wantThinking   bool
		wantFirstParty bool
	}{
		{
			name:           "claude-sonnet-4-20250514",
			model:          "claude-sonnet-4-20250514",
			wantContext:    200000,
			wantMaxOutput:  64000,
			wantImages:     true,
			wantThinking:   true,
			wantFirstParty: true,
		},
		{
			name:           "claude-opus-4-6",
			model:          "claude-opus-4-6",
			wantContext:    200000,
			wantMaxOutput:  128000,
			wantImages:     true,
			wantThinking:   true,
			wantFirstParty: true,
		},
		{
			name:           "gpt-4o",
			model:          "gpt-4o",
			wantContext:    128000,
			wantMaxOutput:  16384,
			wantImages:     true,
			wantThinking:   false,
			wantFirstParty: false,
		},
		{
			name:           "gemini-2.5-pro",
			model:          "gemini-2.5-pro",
			wantContext:    1000000,
			wantMaxOutput:  65536,
			wantImages:     true,
			wantThinking:   true,
			wantFirstParty: false,
		},
		{
			name:           "deepseek-r1",
			model:          "deepseek-r1",
			wantContext:    128000,
			wantMaxOutput:  8192,
			wantImages:     false,
			wantThinking:   true,
			wantFirstParty: false,
		},
		{
			name:           "deepseek-v4-pro",
			model:          "deepseek-v4-pro",
			wantContext:    1000000,
			wantMaxOutput:  384000,
			wantImages:     false,
			wantThinking:   true,
			wantFirstParty: false,
		},
		{
			name:           "deepseek-v4-flash-vision-exp",
			model:          "deepseek-v4-flash-vision-exp",
			wantContext:    1000000,
			wantMaxOutput:  384000,
			wantImages:     true,
			wantThinking:   true,
			wantFirstParty: false,
		},
		{
			name:           "claude-3-5-haiku-20241022",
			model:          "claude-3-5-haiku-20241022",
			wantContext:    200000,
			wantMaxOutput:  8192,
			wantImages:     true,
			wantThinking:   false,
			wantFirstParty: true,
		},
		{
			name:           "o4-mini",
			model:          "o4-mini",
			wantContext:    200000,
			wantMaxOutput:  100000,
			wantImages:     true,
			wantThinking:   true,
			wantFirstParty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := GetCapabilities(tt.model)
			if cap.ContextWindow != tt.wantContext {
				t.Errorf("ContextWindow = %d, want %d", cap.ContextWindow, tt.wantContext)
			}
			if cap.MaxOutputTokens != tt.wantMaxOutput {
				t.Errorf("MaxOutputTokens = %d, want %d", cap.MaxOutputTokens, tt.wantMaxOutput)
			}
			if cap.SupportsImages != tt.wantImages {
				t.Errorf("SupportsImages = %v, want %v", cap.SupportsImages, tt.wantImages)
			}
			if cap.SupportsThinking != tt.wantThinking {
				t.Errorf("SupportsThinking = %v, want %v", cap.SupportsThinking, tt.wantThinking)
			}
			if cap.IsFirstParty != tt.wantFirstParty {
				t.Errorf("IsFirstParty = %v, want %v", cap.IsFirstParty, tt.wantFirstParty)
			}
		})
	}
}

func TestGetCapabilities_UnknownModel(t *testing.T) {
	cap := GetCapabilities("some-random-model-xyz")
	if cap.ContextWindow != 200000 {
		t.Errorf("unknown model ContextWindow = %d, want 200000", cap.ContextWindow)
	}
	if cap.MaxOutputTokens != 32000 {
		t.Errorf("unknown model MaxOutputTokens = %d, want 32000", cap.MaxOutputTokens)
	}
	if cap.Name != "some-random-model-xyz" {
		t.Errorf("unknown model Name = %q, want %q", cap.Name, "some-random-model-xyz")
	}
}

func TestGetCapabilities_PartialNameMatching(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantContext int
		wantOutput  int
	}{
		{
			name:        "short alias claude-sonnet-4 matches dated version",
			input:       "claude-sonnet-4",
			wantContext: 200000,
			wantOutput:  64000,
		},
		{
			name:        "alias claude-3-5-sonnet matches",
			input:       "claude-3-5-sonnet",
			wantContext: 200000,
			wantOutput:  8192,
		},
		{
			name:        "bedrock ARN with model name",
			input:       "us.anthropic.claude-sonnet-4-20250514-v1:0",
			wantContext: 200000,
			wantOutput:  64000,
		},
		{
			name:        "alias claude-opus-4-5 resolves",
			input:       "claude-opus-4-5",
			wantContext: 200000,
			wantOutput:  64000,
		},
		{
			name:        "case insensitive",
			input:       "Claude-Sonnet-4-20250514",
			wantContext: 200000,
			wantOutput:  64000,
		},
		{
			name:        "explicit 1m suffix overrides base capability",
			input:       "claude-opus-4-6[1m]",
			wantContext: 1000000,
			wantOutput:  128000,
		},
		{
			name:        "provider-prefixed deepseek v4 pro",
			input:       "agenticdeepseek:deepseek-v4-pro",
			wantContext: 1000000,
			wantOutput:  384000,
		},
		{
			name:        "unknown non-anthropic model with explicit 1m suffix",
			input:       "vendor-new-model[1m]",
			wantContext: 1000000,
			wantOutput:  32000,
		},
		{
			name:        "deepseek alias",
			input:       "deepseek",
			wantContext: 128000,
			wantOutput:  8192,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := GetCapabilities(tt.input)
			if cap.ContextWindow != tt.wantContext {
				t.Errorf("ContextWindow = %d, want %d", cap.ContextWindow, tt.wantContext)
			}
			if cap.MaxOutputTokens != tt.wantOutput {
				t.Errorf("MaxOutputTokens = %d, want %d", cap.MaxOutputTokens, tt.wantOutput)
			}
		})
	}
}

func TestContextWindow(t *testing.T) {
	if got := ContextWindow("claude-opus-4-6"); got != 200000 {
		t.Errorf("ContextWindow(claude-opus-4-6) = %d, want 200000", got)
	}
	if got := ContextWindow("gemini-2.0-flash"); got != 1000000 {
		t.Errorf("ContextWindow(gemini-2.0-flash) = %d, want 1000000", got)
	}
	if got := ContextWindow("deepseek-v4-pro"); got != 1000000 {
		t.Errorf("ContextWindow(deepseek-v4-pro) = %d, want 1000000", got)
	}
	if got := ContextWindow("gpt-4"); got != 8192 {
		t.Errorf("ContextWindow(gpt-4) = %d, want 8192", got)
	}
	if got := ContextWindow("unknown"); got != 200000 {
		t.Errorf("ContextWindow(unknown) = %d, want 200000 (default)", got)
	}
}

func TestKnownContextWindowRequiresAuthoritativeMatch(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  int
		known bool
	}{
		{name: "exact", model: "gpt-4o", want: 128000, known: true},
		{name: "provider-prefixed", model: "openai/gpt-4o", want: 128000, known: true},
		{name: "explicit suffix", model: "custom-model[1m]", want: 1000000, known: true},
		{name: "unknown", model: "custom-model", known: false},
		{name: "ambiguous partial", model: "gpt", known: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, known := KnownContextWindow(test.model)
			if got != test.want || known != test.known {
				t.Fatalf("KnownContextWindow(%q) = (%d, %v), want (%d, %v)", test.model, got, known, test.want, test.known)
			}
		})
	}
}

func TestMaxOutputTokens(t *testing.T) {
	if got := MaxOutputTokens("claude-opus-4-6"); got != 128000 {
		t.Errorf("MaxOutputTokens(claude-opus-4-6) = %d, want 128000", got)
	}
	if got := MaxOutputTokens("gpt-4-turbo"); got != 4096 {
		t.Errorf("MaxOutputTokens(gpt-4-turbo) = %d, want 4096", got)
	}
	if got := MaxOutputTokens("o3"); got != 100000 {
		t.Errorf("MaxOutputTokens(o3) = %d, want 100000", got)
	}
}

func TestEmptyModelName(t *testing.T) {
	cap := GetCapabilities("")
	if cap.ContextWindow != 200000 {
		t.Errorf("empty model ContextWindow = %d, want 200000", cap.ContextWindow)
	}
}

func TestModelTableCompleteness(t *testing.T) {
	// Ensure the table has a reasonable number of models
	count := len(modelTable)
	if count < 30 {
		t.Errorf("modelTable has %d entries, expected at least 30", count)
	}
}

func TestStripContextSuffix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-6[1m]", "claude-opus-4-6"},
		{"claude-sonnet-4[2m]", "claude-sonnet-4"},
		{"gpt-4o", "gpt-4o"},
		{"claude-opus-4-6", "claude-opus-4-6"},
	}
	for _, tt := range tests {
		got := stripContextSuffix(tt.input)
		if got != tt.want {
			t.Errorf("stripContextSuffix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSplitContextSuffix(t *testing.T) {
	tests := []struct {
		input      string
		wantModel  string
		wantWindow int
	}{
		{"deepseek-v4-pro[1m]", "deepseek-v4-pro", 1000000},
		{"vendor/model[2M]", "vendor/model", 2000000},
		{"gpt-4o", "gpt-4o", 0},
	}
	for _, tt := range tests {
		model, window := splitContextSuffix(tt.input)
		if model != tt.wantModel || window != tt.wantWindow {
			t.Errorf("splitContextSuffix(%q) = (%q, %d), want (%q, %d)", tt.input, model, window, tt.wantModel, tt.wantWindow)
		}
	}
}
