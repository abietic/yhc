package model

import "testing"

func TestResolvePortfolioMetadataPerFieldProvenance(t *testing.T) {
	window := 321000
	images := false
	metadata, err := ResolvePortfolioMetadata("claude-sonnet-4-6", MetadataOverrides{
		ContextWindowTokens: &window,
		Capabilities:        CapabilityOverrides{Images: &images},
		CostTier:            "budget",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ContextWindowTokens.Value != window || metadata.ContextWindowTokens.Source != "profile-override" {
		t.Fatalf("context override = %#v", metadata.ContextWindowTokens)
	}
	if metadata.MaxOutputTokens.Value == 0 || metadata.MaxOutputTokens.Source != "built-in" {
		t.Fatalf("built-in output metadata = %#v", metadata.MaxOutputTokens)
	}
	if metadata.Images.Value || metadata.Images.Source != "profile-override" {
		t.Fatalf("image override = %#v", metadata.Images)
	}
	if metadata.Tools.Source != "built-in" {
		t.Fatalf("tool metadata = %#v", metadata.Tools)
	}
	if metadata.CostTier.Value != "budget" || metadata.CostTier.Source != "profile-override" {
		t.Fatalf("cost override = %#v", metadata.CostTier)
	}
}

func TestResolvePortfolioMetadataUnknownRemainsUnknown(t *testing.T) {
	tools := true
	metadata, err := ResolvePortfolioMetadata("custom-model-without-registry-facts", MetadataOverrides{
		Capabilities: CapabilityOverrides{Tools: &tools},
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ContextWindowTokens.Source != "unknown" || metadata.ContextWindowTokens.Value != 0 {
		t.Fatalf("unknown context metadata = %#v", metadata.ContextWindowTokens)
	}
	if !metadata.Tools.Value || metadata.Tools.Source != "profile-override" {
		t.Fatalf("explicit tool metadata = %#v", metadata.Tools)
	}
	if metadata.Images.Source != "unknown" {
		t.Fatalf("unknown image metadata = %#v", metadata.Images)
	}
}

func TestResolvePortfolioMetadataRejectsInvalidOverrides(t *testing.T) {
	zero := 0
	for name, overrides := range map[string]MetadataOverrides{
		"context":   {ContextWindowTokens: &zero},
		"output":    {MaxOutputTokens: &zero},
		"reasoning": {SupportedReasoningEfforts: []string{"high", "turbo"}},
		"duplicate": {SupportedReasoningEfforts: []string{"high", "HIGH"}},
		"cost":      {CostTier: "expensive"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolvePortfolioMetadata("gpt-4o", overrides); err == nil {
				t.Fatal("invalid metadata override should fail")
			}
		})
	}
	if _, err := ValidateReasoningEffort("turbo"); err == nil {
		t.Fatal("invalid default reasoning effort should fail")
	}
}
