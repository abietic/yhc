package model

import (
	"reflect"
	"testing"
)

func TestDefaultRegistry_All(t *testing.T) {
	reg := DefaultRegistry()
	all := reg.All()
	if len(all) < 20 {
		t.Errorf("expected at least 20 models, got %d", len(all))
	}
}

func TestDefaultRegistry_ByProvider(t *testing.T) {
	reg := DefaultRegistry()

	tests := []struct {
		provider string
		minCount int
	}{
		{"Anthropic", 5},
		{"OpenAI", 4},
		{"Google", 2},
		{"DeepSeek", 3},
		{"Qwen", 2},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			models := reg.ByProvider(tt.provider)
			if len(models) < tt.minCount {
				t.Errorf("ByProvider(%q) returned %d models, want at least %d", tt.provider, len(models), tt.minCount)
			}
			for _, m := range models {
				if m.Provider != tt.provider {
					t.Errorf("ByProvider(%q) returned model with provider %q", tt.provider, m.Provider)
				}
			}
		})
	}
}

func TestDefaultRegistryPublishesOnlyCurrentDeepSeekResponsesModels(t *testing.T) {
	t.Parallel()

	models := DefaultRegistry().ByProvider("DeepSeek")
	got := make([]string, 0, len(models))
	for _, entry := range models {
		got = append(got, entry.ModelID)
	}
	want := []string{
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"deepseek-v4-flash-vision-exp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeepSeek registry = %#v, want %#v", got, want)
	}
}

func TestDefaultRegistry_ByProvider_CaseInsensitive(t *testing.T) {
	reg := DefaultRegistry()
	models := reg.ByProvider("anthropic")
	if len(models) == 0 {
		t.Error("ByProvider with lowercase should still match")
	}
}

func TestDefaultRegistry_WithToolCalls(t *testing.T) {
	reg := DefaultRegistry()
	models := reg.WithToolCalls()
	if len(models) == 0 {
		t.Error("expected some models with tool call support")
	}
	for _, m := range models {
		if !m.SupportsToolCalls {
			t.Errorf("WithToolCalls returned model %q without tool call support", m.ModelID)
		}
	}
}

func TestDefaultRegistry_WithThinking(t *testing.T) {
	reg := DefaultRegistry()
	models := reg.WithThinking()
	if len(models) == 0 {
		t.Error("expected some models with thinking support")
	}
	for _, m := range models {
		if !m.SupportsThinking {
			t.Errorf("WithThinking returned model %q without thinking support", m.ModelID)
		}
	}
}

func TestDefaultRegistry_WithMedia(t *testing.T) {
	reg := DefaultRegistry()
	models := reg.WithMedia()
	if len(models) == 0 {
		t.Error("expected some models with media support")
	}
	for _, m := range models {
		if !m.SupportsMedia {
			t.Errorf("WithMedia returned model %q without media support", m.ModelID)
		}
	}
}

func TestDefaultRegistry_GroupedByProvider(t *testing.T) {
	reg := DefaultRegistry()
	groups := reg.GroupedByProvider()

	if len(groups) < 4 {
		t.Errorf("expected at least 4 provider groups, got %d", len(groups))
	}

	// First group should be Anthropic (preferred order).
	if groups[0].Provider != "Anthropic" {
		t.Errorf("first group should be Anthropic, got %q", groups[0].Provider)
	}

	// Verify each group has non-empty models.
	for _, g := range groups {
		if len(g.Models) == 0 {
			t.Errorf("provider group %q has no models", g.Provider)
		}
		for _, m := range g.Models {
			if m.Provider != g.Provider {
				t.Errorf("model %q in group %q has provider %q", m.ModelID, g.Provider, m.Provider)
			}
		}
	}
}

func TestDefaultRegistry_GroupedByProvider_SortOrder(t *testing.T) {
	reg := DefaultRegistry()
	groups := reg.GroupedByProvider()

	// Verify within each group, premium models come first.
	for _, g := range groups {
		if len(g.Models) < 2 {
			continue
		}
		tierOrder := map[CostTier]int{
			CostTierPremium:  0,
			CostTierStandard: 1,
			CostTierBudget:   2,
			CostTierFree:     3,
		}
		for i := 1; i < len(g.Models); i++ {
			prev := tierOrder[g.Models[i-1].CostTier]
			curr := tierOrder[g.Models[i].CostTier]
			if prev > curr {
				t.Errorf("provider %q: model %q (tier %s) should come after %q (tier %s)",
					g.Provider, g.Models[i-1].DisplayName, g.Models[i-1].CostTier,
					g.Models[i].DisplayName, g.Models[i].CostTier)
			}
		}
	}
}

func TestDefaultRegistry_Lookup(t *testing.T) {
	reg := DefaultRegistry()

	tests := []struct {
		modelID     string
		wantDisplay string
		wantNil     bool
	}{
		{"gpt-4o", "GPT-4o", false},
		{"claude-sonnet-4-6", "Claude Sonnet 4.6", false},
		{"gemini-2.5-pro", "Gemini 2.5 Pro", false},
		{"nonexistent-model-xyz", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			entry := reg.Lookup(tt.modelID)
			if tt.wantNil {
				if entry != nil {
					t.Errorf("Lookup(%q) should return nil, got %+v", tt.modelID, entry)
				}
				return
			}
			if entry == nil {
				t.Fatalf("Lookup(%q) returned nil", tt.modelID)
				return
			}
			if entry.DisplayName != tt.wantDisplay {
				t.Errorf("Lookup(%q).DisplayName = %q, want %q", tt.modelID, entry.DisplayName, tt.wantDisplay)
			}
		})
	}
}

func TestDefaultRegistry_Lookup_Alias(t *testing.T) {
	reg := DefaultRegistry()
	// "claude-opus-4" is an alias for "claude-opus-4-20250514"
	entry := reg.Lookup("claude-opus-4")
	if entry == nil {
		t.Fatal("Lookup via alias should find the model")
		return
	}
	if entry.ModelID != "claude-opus-4-20250514" {
		t.Errorf("expected modelID claude-opus-4-20250514, got %q", entry.ModelID)
	}
}

func TestDefaultRegistry_Providers(t *testing.T) {
	reg := DefaultRegistry()
	providers := reg.Providers()
	if len(providers) < 4 {
		t.Errorf("expected at least 4 providers, got %d", len(providers))
	}
	// Should be sorted alphabetically.
	for i := 1; i < len(providers); i++ {
		if providers[i-1] > providers[i] {
			t.Errorf("providers not sorted: %q > %q", providers[i-1], providers[i])
		}
	}
}

func TestDefaultRegistry_EntryCapabilities(t *testing.T) {
	reg := DefaultRegistry()

	// Verify a specific known model's capabilities.
	entry := reg.Lookup("claude-opus-4-6")
	if entry == nil {
		t.Fatal("claude-opus-4-6 not found")
		return
	}
	if !entry.SupportsThinking {
		t.Error("claude-opus-4-6 should support thinking")
	}
	if !entry.SupportsMedia {
		t.Error("claude-opus-4-6 should support media")
	}
	if !entry.SupportsToolCalls {
		t.Error("claude-opus-4-6 should support tool calls")
	}
	if !entry.SupportsStreaming {
		t.Error("claude-opus-4-6 should support streaming")
	}
	if entry.MaxContextTokens != 200000 {
		t.Errorf("claude-opus-4-6 MaxContextTokens = %d, want 200000", entry.MaxContextTokens)
	}
	if entry.MaxOutputTokens != 128000 {
		t.Errorf("claude-opus-4-6 MaxOutputTokens = %d, want 128000", entry.MaxOutputTokens)
	}
	if entry.CostTier != CostTierPremium {
		t.Errorf("claude-opus-4-6 CostTier = %q, want premium", entry.CostTier)
	}
}

func TestDeriveCostTier(t *testing.T) {
	tests := []struct {
		name       string
		outputCost float64
		want       CostTier
	}{
		{"zero cost", 0, CostTierFree},
		{"very cheap (< $2/Mtok)", 0.0000006, CostTierBudget},
		{"standard ($5/Mtok)", 0.000005, CostTierStandard},
		{"standard ($15/Mtok)", 0.000015, CostTierStandard},
		{"premium ($25/Mtok)", 0.000025, CostTierPremium},
		{"premium ($75/Mtok)", 0.000075, CostTierPremium},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := &ModelCapabilities{CostPerOutputToken: tt.outputCost}
			got := deriveCostTier(cap)
			if got != tt.want {
				t.Errorf("deriveCostTier(output=%v) = %q, want %q", tt.outputCost, got, tt.want)
			}
		})
	}
}

func TestDefaultRegistry_ByCapability_Custom(t *testing.T) {
	reg := DefaultRegistry()

	// Custom predicate: models with >500k context.
	largeContext := reg.ByCapability(func(e RegistryEntry) bool {
		return e.MaxContextTokens > 500000
	})
	if len(largeContext) == 0 {
		t.Error("expected some models with >500k context")
	}
	for _, m := range largeContext {
		if m.MaxContextTokens <= 500000 {
			t.Errorf("model %q has context %d, expected >500k", m.ModelID, m.MaxContextTokens)
		}
	}
}
