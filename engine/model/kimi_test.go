package model

import (
	"reflect"
	"testing"
)

func TestKimiMetadataPublishesOnlyImplementedExactContract(t *testing.T) {
	for _, provider := range []string{"kimi", "moonshot", "agentickimi"} {
		metadata, err := ResolvePortfolioMetadataForProvider(provider, "kimi-k3", MetadataOverrides{})
		if err != nil {
			t.Fatal(err)
		}
		if !metadata.Tools.Value || !metadata.Streaming.Value || !metadata.Thinking.Value || metadata.Images.Value || metadata.PDFs.Value || metadata.CostTier.Source != "unknown" {
			t.Fatalf("metadata overclaims supported facts: %#v", metadata)
		}
		if !reflect.DeepEqual(metadata.SupportedReasoningEfforts.Value, []string{"low", "high", "max"}) {
			t.Fatal("wrong effort vocabulary")
		}
	}
	for _, id := range []string{"kimi-k2.6", "kimi-k3.1-pro", "acme/kimi-k3"} {
		if values, known := DefaultReasoningEfforts("kimi", id); known || values != nil {
			t.Fatalf("unimplemented model %s inherited effort support", id)
		}
	}
	if SupportsAdapterReasoningEffort("kimi", "medium") || SupportsAdapterReasoningEffort("kimi", "none") {
		t.Fatal("Kimi accepted invalid effort")
	}
	entries := DefaultRegistry().ByProvider("Kimi")
	if len(entries) != 1 || entries[0].ModelID != "kimi-k3" || entries[0].SupportsMedia || entries[0].CostTier != CostTierUnknown {
		t.Fatalf("curated Kimi catalog=%#v", entries)
	}
}
