package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestP242bNormalizeGoalProviderUsage(t *testing.T) {
	t.Run("nil usage is an error", func(t *testing.T) {
		got, err := normalizeGoalProviderUsage(nil)
		if err == nil {
			t.Fatal("expected error for nil usage, got nil")
		}
		if got != (normalizedGoalProviderUsage{}) {
			t.Fatalf("expected zero result on error, got %+v", got)
		}
	})

	type want struct {
		prompt     uint64
		cached     uint64
		completion uint64
		reasoning  uint64
		total      uint64
		billable   uint64
	}

	tests := []struct {
		name    string
		usage   schema.TokenUsage
		want    want
		wantErr string
	}{
		{
			name:  "all-zero usage is valid",
			usage: schema.TokenUsage{},
			want:  want{},
		},
		{
			name: "known plain usage",
			usage: schema.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 40,
				TotalTokens:      140,
			},
			want: want{prompt: 100, completion: 40, total: 140, billable: 140},
		},
		{
			name: "cached tokens reduce billable",
			usage: schema.TokenUsage{
				PromptTokens:       100,
				PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 60},
				CompletionTokens:   40,
				TotalTokens:        140,
			},
			want: want{prompt: 100, cached: 60, completion: 40, total: 140, billable: 80},
		},
		{
			name: "total-only usage keeps provider total",
			usage: schema.TokenUsage{
				TotalTokens: 200,
			},
			want: want{total: 200, billable: 200},
		},
		{
			name: "breakdown total exceeds missing provider total",
			usage: schema.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 40,
			},
			want: want{prompt: 100, completion: 40, total: 140, billable: 140},
		},
		{
			name: "provider total above breakdown is preserved",
			usage: schema.TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      500,
			},
			want: want{prompt: 10, completion: 5, total: 500, billable: 500},
		},
		{
			name: "reasoning within completion is accepted",
			usage: schema.TokenUsage{
				PromptTokens:            50,
				CompletionTokens:        30,
				TotalTokens:             80,
				CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 12},
			},
			want: want{prompt: 50, completion: 30, reasoning: 12, total: 80, billable: 80},
		},
		{
			name:    "negative prompt tokens rejected",
			usage:   schema.TokenUsage{PromptTokens: -1},
			wantErr: "negative prompt tokens",
		},
		{
			name: "negative cached tokens rejected",
			usage: schema.TokenUsage{
				PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: -1},
			},
			wantErr: "negative cached prompt tokens",
		},
		{
			name:    "negative completion tokens rejected",
			usage:   schema.TokenUsage{CompletionTokens: -1},
			wantErr: "negative completion tokens",
		},
		{
			name:    "negative total tokens rejected",
			usage:   schema.TokenUsage{TotalTokens: -1},
			wantErr: "negative total tokens",
		},
		{
			name: "negative reasoning tokens rejected",
			usage: schema.TokenUsage{
				CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: -1},
			},
			wantErr: "negative reasoning tokens",
		},
		{
			name: "cached exceeding prompt rejected",
			usage: schema.TokenUsage{
				PromptTokens:       10,
				PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 11},
			},
			wantErr: "cached prompt tokens 11 exceed prompt tokens 10",
		},
		{
			name: "reasoning exceeding completion rejected",
			usage: schema.TokenUsage{
				CompletionTokens:        10,
				CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 11},
			},
			wantErr: "reasoning tokens 11 exceed completion tokens 10",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeGoalProviderUsage(&tc.usage)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
				}
				if got != (normalizedGoalProviderUsage{}) {
					t.Fatalf("expected zero result on error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := normalizedGoalProviderUsage{
				PromptTokens:       tc.want.prompt,
				CachedPromptTokens: tc.want.cached,
				CompletionTokens:   tc.want.completion,
				ReasoningTokens:    tc.want.reasoning,
				TotalTokens:        tc.want.total,
				BillableTokens:     tc.want.billable,
			}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}

	t.Run("overflow-safe near max int", func(t *testing.T) {
		// prompt+completion cannot overflow uint64 for any int pair, but the
		// sum must still be computed exactly near the int ceiling.
		usage := &schema.TokenUsage{
			PromptTokens:       math.MaxInt - 1,
			PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 3},
			CompletionTokens:   math.MaxInt - 1,
		}
		got, err := normalizeGoalProviderUsage(usage)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantTotal := 2 * (uint64(math.MaxInt) - 1)
		if got.TotalTokens != wantTotal {
			t.Fatalf("TotalTokens = %d, want %d", got.TotalTokens, wantTotal)
		}
		if got.BillableTokens != wantTotal-3 {
			t.Fatalf("BillableTokens = %d, want %d", got.BillableTokens, wantTotal-3)
		}
	})

	t.Run("input not mutated", func(t *testing.T) {
		usage := &schema.TokenUsage{
			PromptTokens:       10,
			PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 4},
			CompletionTokens:   6,
			TotalTokens:        16,
		}
		before := *usage
		if _, err := normalizeGoalProviderUsage(usage); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if *usage != before {
			t.Fatalf("input mutated: before %+v, after %+v", before, *usage)
		}
	})
}
