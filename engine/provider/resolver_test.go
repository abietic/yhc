package provider

import (
	"strings"
	"testing"
)

// getenvMap returns a Getenv stub backed by a deterministic map.
// Missing keys behave like unset environment variables.
func getenvMap(m map[string]string) func(string) string {
	return func(k string) string {
		return m[k]
	}
}

// credStore returns a CredentialLookup stub backed by a map keyed by the
// canonical credential ID (e.g. "anthropic", "openai", "deepseek").
func credStore(creds map[string]string) CredentialLookup {
	return func(provider string) (string, bool, error) {
		key, ok := creds[provider]
		return key, ok, nil
	}
}

func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		input Provider
		want  Provider
	}{
		{"anthropic", ProviderAgenticClaude},
		{"claude", ProviderAgenticClaude},
		{"agenticclaude", ProviderAgenticClaude},
		{"openai", ProviderAgenticOpenAI},
		{"agenticopenai", ProviderAgenticOpenAI},
		{"google", ProviderAgenticGemini},
		{"gemini", ProviderAgenticGemini},
		{"agenticgemini", ProviderAgenticGemini},
		{"deepseek", ProviderAgenticDeepSeek},
		{"agenticdeepseek", ProviderAgenticDeepSeek},
		{"qwen", ProviderAgenticQwen},
		{"dashscope", ProviderAgenticQwen},
		{"agenticqwen", ProviderAgenticQwen},
		{"ark", ProviderAgenticArk},
		{"volcengine", ProviderAgenticArk},
		{"agenticark", ProviderAgenticArk},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			got, err := NormalizeProvider(tt.input)
			if err != nil {
				t.Fatalf("NormalizeProvider(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeProvider(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeProviderUnknown(t *testing.T) {
	_, err := NormalizeProvider("unknown")
	if err == nil {
		t.Error("NormalizeProvider(\"unknown\") expected error, got nil")
	}
}

func TestResolveConfig(t *testing.T) {
	tests := []struct {
		name      string
		input     ResolveInput
		want      ResolvedConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "explicit provider/model/key/base URL beat env and config and store",
			input: ResolveInput{
				Explicit: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o-mini",
					APIKey:   "explicit-key",
					BaseURL:  "https://explicit.example/v1",
				},
				Configured: Config{
					Provider: ProviderAgenticClaude,
					Model:    "claude-sonnet-4-6",
					APIKey:   "config-key",
					BaseURL:  "https://config.example",
				},
				Getenv: getenvMap(map[string]string{
					"PROV":          "anthropic",
					"PROV_MODEL":    "claude-opus-4-6",
					"PROV_API_KEY":  "prov-key",
					"PROV_BASE_URL": "https://prov.example",
				}),
				CredentialLookup: credStore(map[string]string{"anthropic": "stored-key"}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o-mini",
					APIKey:   "explicit-key",
					BaseURL:  "https://explicit.example/v1",
				},
				Sources: ResolutionSources{
					Provider: "explicit",
					Model:    "explicit",
					APIKey:   "explicit",
					BaseURL:  "explicit",
				},
			},
		},
		{
			name: "PROV provider/model/key beat config",
			input: ResolveInput{
				Configured: Config{
					Provider: ProviderAgenticClaude,
					Model:    "claude-sonnet-4-6",
					APIKey:   "config-key",
					BaseURL:  "https://config.example",
				},
				Getenv: getenvMap(map[string]string{
					"PROV":          "openai",
					"PROV_MODEL":    "gpt-4o",
					"PROV_API_KEY":  "prov-key",
					"PROV_BASE_URL": "https://prov.example",
				}),
				CredentialLookup: credStore(map[string]string{"anthropic": "stored-key"}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   "prov-key",
					BaseURL:  "https://prov.example",
				},
				Sources: ResolutionSources{
					Provider: "env:PROV",
					Model:    "env:PROV_MODEL",
					APIKey:   "env:PROV_API_KEY",
					BaseURL:  "env:PROV_BASE_URL",
				},
			},
		},
		{
			name: "provider-specific key without generic key",
			input: ResolveInput{
				Configured: Config{
					Provider: ProviderAgenticDeepSeek,
					Model:    "deepseek-chat",
				},
				Getenv: getenvMap(map[string]string{
					"DEEPSEEK_API_KEY": "deepseek-key",
				}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticDeepSeek,
					Model:    "deepseek-chat",
					APIKey:   "deepseek-key",
					BaseURL:  "https://api.deepseek.com",
				},
				Sources: ResolutionSources{
					Provider: "config",
					Model:    "config",
					APIKey:   "env:DEEPSEEK_API_KEY",
					BaseURL:  "provider-default",
				},
			},
		},
		{
			name: "explicit model infers provider and overrides lower-priority PROV",
			input: ResolveInput{
				Explicit: Config{
					Model:  "gpt-4o",
					APIKey: "explicit-key",
				},
				Getenv: getenvMap(map[string]string{
					"PROV":         "anthropic",
					"PROV_API_KEY": "prov-key",
				}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   "explicit-key",
					BaseURL:  "https://api.openai.com/v1",
				},
				Sources: ResolutionSources{
					Provider: "explicit:model",
					Model:    "explicit",
					APIKey:   "explicit",
					BaseURL:  "provider-default",
				},
			},
		},
		{
			name: "generic key bound to overridden PROV is not reused",
			input: ResolveInput{
				Explicit: Config{Model: "gpt-4o"},
				Getenv: getenvMap(map[string]string{
					"PROV":           "anthropic",
					"PROV_API_KEY":   "anthropic-generic-key",
					"OPENAI_API_KEY": "openai-key",
				}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   "openai-key",
					BaseURL:  "https://api.openai.com/v1",
				},
				Sources: ResolutionSources{
					Provider: "explicit:model",
					Model:    "explicit",
					APIKey:   "env:OPENAI_API_KEY",
					BaseURL:  "provider-default",
				},
			},
		},
		{
			name: "explicit provider uses default when lower-priority config model is incompatible",
			input: ResolveInput{
				Explicit: Config{
					Provider: ProviderAgenticOpenAI,
					APIKey:   "explicit-key",
				},
				Configured: Config{
					Model: "claude-sonnet-4-6",
				},
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   "explicit-key",
					BaseURL:  "https://api.openai.com/v1",
				},
				Sources: ResolutionSources{
					Provider: "explicit",
					Model:    "provider-default",
					APIKey:   "explicit",
					BaseURL:  "provider-default",
				},
			},
		},
		{
			name: "provider-qualified user alias",
			input: ResolveInput{
				Explicit: Config{
					Model: "fast",
				},
				Configured: Config{
					ModelAliases: map[string]string{
						"fast": "openai:gpt-4o",
					},
				},
				Getenv: getenvMap(map[string]string{
					"PROV_API_KEY": "prov-key",
				}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   "prov-key",
					BaseURL:  "https://api.openai.com/v1",
					ModelAliases: map[string]string{
						"fast": "openai:gpt-4o",
					},
				},
				Sources: ResolutionSources{
					Provider: "explicit:prefix",
					Model:    "explicit",
					APIKey:   "env:PROV_API_KEY",
					BaseURL:  "provider-default",
				},
			},
		},
		{
			name: "credential store provides key and source omits value",
			input: ResolveInput{
				Getenv:           getenvMap(map[string]string{}),
				CredentialLookup: credStore(map[string]string{"deepseek": "stored-deepseek-key"}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticDeepSeek,
					Model:    "deepseek-v4-flash",
					APIKey:   "stored-deepseek-key",
					BaseURL:  "https://api.deepseek.com",
				},
				Sources: ResolutionSources{
					Provider: "credential-store",
					Model:    "provider-default",
					APIKey:   "credential-store",
					BaseURL:  "provider-default",
				},
			},
		},
		{
			name: "provider-specific base URL env",
			input: ResolveInput{
				Explicit: Config{
					Provider: ProviderAgenticDeepSeek,
				},
				Getenv: getenvMap(map[string]string{
					"DEEPSEEK_API_KEY":  "deepseek-key",
					"DEEPSEEK_BASE_URL": "https://deepseek.example",
				}),
			},
			want: ResolvedConfig{
				Config: Config{
					Provider: ProviderAgenticDeepSeek,
					Model:    "deepseek-v4-flash",
					APIKey:   "deepseek-key",
					BaseURL:  "https://deepseek.example",
				},
				Sources: ResolutionSources{
					Provider: "explicit",
					Model:    "provider-default",
					APIKey:   "env:DEEPSEEK_API_KEY",
					BaseURL:  "env:DEEPSEEK_BASE_URL",
				},
			},
		},
		{
			name: "unknown explicit provider errors",
			input: ResolveInput{
				Explicit: Config{
					Provider: "unknown",
				},
			},
			wantErr:   true,
			errSubstr: "unknown provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveConfig(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveConfig expected error containing %q, got nil", tt.errSubstr)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("ResolveConfig error = %q, want substring %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveConfig unexpected error: %v", err)
			}

			if got.Provider != tt.want.Provider {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.want.Provider)
			}
			if got.Model != tt.want.Model {
				t.Errorf("Model = %q, want %q", got.Model, tt.want.Model)
			}
			if got.APIKey != tt.want.APIKey {
				t.Errorf("APIKey = %q, want %q", got.APIKey, tt.want.APIKey)
			}
			if got.BaseURL != tt.want.BaseURL {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tt.want.BaseURL)
			}

			if got.Sources.Provider != tt.want.Sources.Provider {
				t.Errorf("Sources.Provider = %q, want %q", got.Sources.Provider, tt.want.Sources.Provider)
			}
			if got.Sources.Model != tt.want.Sources.Model {
				t.Errorf("Sources.Model = %q, want %q", got.Sources.Model, tt.want.Sources.Model)
			}
			if got.Sources.APIKey != tt.want.Sources.APIKey {
				t.Errorf("Sources.APIKey = %q, want %q", got.Sources.APIKey, tt.want.Sources.APIKey)
			}
			if got.Sources.BaseURL != tt.want.Sources.BaseURL {
				t.Errorf("Sources.BaseURL = %q, want %q", got.Sources.BaseURL, tt.want.Sources.BaseURL)
			}

			// Sources record where a value came from, never the credential value.
			assertSourceNotKey(t, got.Sources.APIKey, got.APIKey)
		})
	}
}

func TestMergeAliasesNormalizesAndOverrides(t *testing.T) {
	if got := mergeAliases(nil, nil); got != nil {
		t.Fatalf("mergeAliases(nil, nil) = %#v, want nil", got)
	}

	got := mergeAliases(
		map[string]string{
			" GPT ":      " configured ",
			"Configured": " configured-only ",
		},
		map[string]string{
			"gPt":      " explicit ",
			"Explicit": " explicit-only ",
		},
	)
	want := map[string]string{
		"gpt":        "explicit",
		"configured": "configured-only",
		"explicit":   "explicit-only",
	}
	if len(got) != len(want) {
		t.Fatalf("mergeAliases length = %d, want %d: %#v", len(got), len(want), got)
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("mergeAliases[%q] = %q, want %q", name, got[name], value)
		}
	}
}

func assertSourceNotKey(t *testing.T, source, key string) {
	t.Helper()
	if source == key {
		t.Errorf("source %q must not equal the API key value", source)
	}
}
