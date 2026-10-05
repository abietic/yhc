package model

import "testing"

func TestDeepSeekFlashCatalogAndLegacyCapabilities(t *testing.T) {
	t.Parallel()
	if got := GetProviderEnvConfig(ProviderDeepSeek); got.DefaultModel != "deepseek-flash" {
		t.Errorf("DeepSeek default model = %q", got.DefaultModel)
	}
	if got := ResolveModelAlias("deepseek"); got != "deepseek-flash" {
		t.Errorf("DeepSeek short alias = %q", got)
	}
	for _, id := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		cap := GetCapabilities(id)
		if cap.Name != id || cap.ContextWindow != 1_048_576 || cap.MaxOutputTokens != 393_216 ||
			!cap.SupportsImages || !cap.SupportsThinking || !cap.SupportsTools {
			t.Errorf("%s: missing current Flash capability", id)
		}
		if cap.CostPerInputToken != 0.0000003 || cap.CostPerOutputToken != 0.0000012 {
			t.Errorf("%s: missing current peak USD pricing", id)
		}
		if id != "deepseek-flash" && (!cap.IsDeprecated() || cap.Successor != "deepseek-flash") {
			t.Errorf("%s: missing migration guidance", id)
		}
	}
	if entry := DefaultRegistry().Lookup("deepseek-flash"); entry == nil || !entry.SupportsMedia ||
		entry.MaxContextTokens != 1_048_576 || entry.MaxOutputTokens != 393_216 {
		t.Fatal("current Flash model is absent from the media-capable registry")
	}
}

func TestDeepSeekProCurrentMetadataRemainsTextOnly(t *testing.T) {
	t.Parallel()
	cap := GetCapabilities("deepseek-v4-pro")
	if cap.ContextWindow != 1_048_576 || cap.MaxOutputTokens != 393_216 || cap.SupportsImages ||
		cap.CostPerInputToken != 0.00000132 || cap.CostPerOutputToken != 0.00000396 {
		t.Fatal("V4 Pro metadata differs from the current official catalog or peak USD pricing")
	}
}

func TestLookupExactCapabilitiesRejectsSubstringAndReturnsDetachedMetadata(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"acme/deepseek-flash", "deepseek-flash-proxy", "unknown-model"} {
		if _, ok := LookupExactCapabilities(id); ok {
			t.Errorf("custom model %q received exact built-in authority", id)
		}
	}
	cap, ok := LookupExactCapabilities(" deepseek[1m] ")
	if !ok || cap.Name != "deepseek-flash" || cap.ContextWindow != 1_000_000 {
		t.Fatal("known alias with context suffix lost exact capabilities")
	}
	// A suffix is an explicit caller limit; the unsuffixed alias retains the
	// provider's exact binary-token capacity rather than a decimal approximation.
	plain, ok := LookupExactCapabilities("deepseek")
	if !ok || plain.ContextWindow != 1_048_576 || plain.MaxOutputTokens != 393_216 {
		t.Fatal("unsuffixed alias lost the official token limits")
	}
	cap.SupportsImages = false
	again, _ := LookupExactCapabilities("deepseek-flash")
	if !again.SupportsImages {
		t.Fatal("caller mutated the built-in catalog")
	}
}
