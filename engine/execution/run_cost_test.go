package execution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func fixturePriceCard() RunPriceCard {
	return RunPriceCard{Version: 1, Currency: "CNY", Source: "https://example.org/pricing", AsOf: "2026-10-11T00:00:00Z", Window: "fixed", Routes: []RunPriceRoute{{Provider: "agenticdeepseek", Model: "flash", CachedInputPerMillion: "1", UncachedInputPerMillion: "2", OutputPerMillion: "4"}}}
}

func TestRunCostExactThresholdAndPriceLock(t *testing.T) {
	card := fixturePriceCard()
	r, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: "0.000024"}, card)
	if err != nil {
		t.Fatal(err)
	}
	card.Routes[0].OutputPerMillion = "999"
	c, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"})
	if err != nil {
		t.Fatal(err)
	}
	u := &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14}
	u.PromptTokenDetails.CachedTokens = 6
	u.CompletionTokensDetails.ReasoningTokens = 2
	if err := c.CompleteProviderUsage(u); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteProviderUsage(u); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"}); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatalf("next admission=%v", err)
	}
	s := r.Snapshot()
	if s.Cost.KnownEstimatedAmount != "0.000030000000" || !s.Cost.Complete || s.Cost.PricedCalls != 1 || s.Cost.ActualBilledAmount != nil {
		t.Fatalf("cost=%+v", s.Cost)
	}
	if s.CallLedger.Records[0].Cost.EstimatedAmount == nil || *s.CallLedger.Records[0].Cost.EstimatedAmount != "0.000030000000" {
		t.Fatalf("record=%+v", s.CallLedger.Records[0])
	}
	s.Cost.PriceCard.Routes[0].OutputPerMillion = "100"
	*s.CallLedger.Records[0].Cost.EstimatedAmount = "100"
	if r.Snapshot().Cost.PriceCard.Routes[0].OutputPerMillion != "4" || *r.Snapshot().CallLedger.Records[0].Cost.EstimatedAmount != "0.000030000000" {
		t.Fatal("snapshot aliases collector")
	}
}

func TestRunCostUnknownReleaseAndDisabled(t *testing.T) {
	r, _ := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: "1"}, fixturePriceCard())
	d := ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"}
	c, _ := r.AdmitProviderUsage(t.Context(), d)
	_ = c.ReleaseProviderUsageBeforeDispatch()
	if s := r.Snapshot(); s.Cost.UnpricedCalls != 0 || s.Cost.KnownEstimatedAmount != "0.000000000000" || !s.Cost.Complete {
		t.Fatalf("release=%+v", s.Cost)
	}
	c, _ = r.AdmitProviderUsage(t.Context(), d)
	if err := c.MarkProviderUsageAmbiguous(errors.New("transport")); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("unknown=%v", err)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("next=%v", err)
	}
	if s := r.Snapshot(); s.Cost.Complete || s.Cost.UnpricedCalls != 1 || s.CallLedger.Records[1].Cost.EstimatedAmount != nil {
		t.Fatalf("unknown=%+v", s)
	}
	r, _ = NewPricedRunUsage(RunUsageLimits{}, fixturePriceCard())
	c, _ = r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Model: "unpriced"})
	if err := c.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunUsage(RunUsageLimits{MaxEstimatedCost: "1"}); err == nil {
		t.Fatal("cap without card accepted")
	}
	r, _ = NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: "1"}, fixturePriceCard())
	if _, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Model: "flash"}); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("missing provider=%v", err)
	}
}

func TestRunCostConcurrentSettlementAndFreshSegment(t *testing.T) {
	r, _ := NewPricedRunUsage(RunUsageLimits{}, fixturePriceCard())
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"})
			if err != nil {
				t.Error(err)
				return
			}
			if err := c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 1000000, TotalTokens: 1000000}); err != nil {
				t.Error(err)
			}
			_ = r.Snapshot()
		}()
	}
	wg.Wait()
	s := r.Snapshot()
	if s.Cost.KnownEstimatedAmount != "60.000000000000" || s.Cost.PricedCalls != 30 || !s.Cost.Complete {
		t.Fatalf("cost=%+v", s.Cost)
	}
	next, _ := NewPricedRunUsage(RunUsageLimits{}, fixturePriceCard())
	if next.Snapshot().Cost.KnownEstimatedAmount != "0.000000000000" || next.Snapshot().Cost.SegmentID == s.Cost.SegmentID {
		t.Fatal("fresh grant inherited cost or identity")
	}
}

func TestRunPriceCardValidationAndExactLargeArithmetic(t *testing.T) {
	for _, invalid := range []string{"-1", "NaN", "1e3", "1.", ".1", "0.0000000000001"} {
		if _, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: invalid}, fixturePriceCard()); err == nil {
			t.Fatalf("accepted amount %q", invalid)
		}
	}
	for _, invalid := range []string{`{"version":1,"api_key":"private"}`, `{}`, `{}` + `{}`, strings.Repeat(" ", 65537)} {
		if _, err := DecodeRunPriceCard(strings.NewReader(invalid)); err == nil {
			t.Fatal("accepted invalid public card")
		}
	}
	for _, mutate := range []func(*RunPriceCard){
		func(c *RunPriceCard) { c.Currency = "cny" },
		func(c *RunPriceCard) { c.Source = "https://user:secret@example.org" },
		func(c *RunPriceCard) { c.Source = "https://example.org/?key=secret" },
		func(c *RunPriceCard) { c.AsOf = "yesterday" },
		func(c *RunPriceCard) { c.AsOf = "2026-10-11T00:00:00." + strings.Repeat("1", 1000) + "Z" },
		func(c *RunPriceCard) { c.Routes = append(c.Routes, c.Routes[0]) },
		func(c *RunPriceCard) { c.Routes[0].OutputPerMillion = "0.0000001" },
	} {
		card := fixturePriceCard()
		mutate(&card)
		if _, err := NewPricedRunUsage(RunUsageLimits{}, card); err == nil {
			t.Fatal("accepted invalid card")
		}
	}
	card := fixturePriceCard()
	card.Routes[0].UncachedInputPerMillion = "0.000001"
	r, _ := NewPricedRunUsage(RunUsageLimits{}, card)
	c, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"})
	_ = c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 9223372036854775807, TotalTokens: 9223372036854775807})
	if r.Snapshot().Cost.KnownEstimatedAmount != "9223372.036854775807" {
		t.Fatalf("rounded cost=%s", r.Snapshot().Cost.KnownEstimatedAmount)
	}
}
