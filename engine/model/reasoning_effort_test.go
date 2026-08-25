package model

import (
	"reflect"
	"testing"
)

func TestDeepSeekV4RequestCapabilitiesExposeOnlyExactEfforts(t *testing.T) {
	t.Parallel()

	for _, modelID := range []string{
		"deepseek-v4-flash",
		"deepseek-v4-pro",
		"deepseek-v4-flash-vision-exp",
	} {
		efforts, ok := DefaultReasoningEfforts("agenticdeepseek", modelID)
		if !ok {
			t.Fatalf("%s reasoning capability is unknown", modelID)
		}
		if want := []string{"none", "high", "max"}; !reflect.DeepEqual(efforts, want) {
			t.Fatalf("%s efforts = %#v, want %#v", modelID, efforts, want)
		}
	}

	if efforts, ok := DefaultReasoningEfforts(
		"agenticdeepseek",
		"deepseek-reasoner",
	); ok || len(efforts) != 0 {
		t.Fatalf("legacy reasoner effort capability = %#v, %v", efforts, ok)
	}
	if efforts, ok := DefaultReasoningEfforts(
		"agenticdeepseek",
		"acme/deepseek-v4-flash-proxy",
	); ok || len(efforts) != 0 {
		t.Fatalf("substring-matched custom model capability = %#v, %v", efforts, ok)
	}
}

func TestResolveAdapterReasoningEffortSeparatesIntentFromWireDialect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		effort   string
		want     ResolvedReasoningEffort
		wantErr  bool
	}{
		{
			name:     "deepseek none uses Responses effort",
			provider: "deepseek",
			effort:   "none",
			want: ResolvedReasoningEffort{
				CanonicalEffort: "none",
				WireEffort:      "none",
				Dialect:         ReasoningDialectDeepSeek,
			},
		},
		{
			name:     "deepseek max enables exact wire effort",
			provider: "agenticdeepseek",
			effort:   "max",
			want: ResolvedReasoningEffort{
				CanonicalEffort: "max",
				WireEffort:      "max",
				Dialect:         ReasoningDialectDeepSeek,
			},
		},
		{
			name:     "deepseek compatibility aliases stay rejected",
			provider: "agenticdeepseek",
			effort:   "low",
			wantErr:  true,
		},
		{
			name:     "qwen remains unsupported",
			provider: "agenticqwen",
			effort:   "high",
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveAdapterReasoningEffort(test.provider, test.effort)
			if (err != nil) != test.wantErr {
				t.Fatalf("ResolveAdapterReasoningEffort() error = %v", err)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("ResolveAdapterReasoningEffort() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestValidateReasoningEffortAcceptsSafeAdapterOwnedIDs(t *testing.T) {
	t.Parallel()

	if got, err := ValidateReasoningEffort("  Ultra-2  "); err != nil || got != "ultra-2" {
		t.Fatalf("ValidateReasoningEffort() = %q, %v", got, err)
	}
	for _, raw := range []string{"default", "has space", "../max", "\nmax"} {
		if _, err := ValidateReasoningEffort(raw); err == nil {
			t.Fatalf("ValidateReasoningEffort(%q) succeeded", raw)
		}
	}
}
