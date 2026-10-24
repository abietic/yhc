package execution

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRunUsageCacheWritePresenceAndSubsets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		written   int
		observe   bool
		receipt   int
		ttl       string
		wantKnown bool
		wantWrite bool
	}{
		{"missing-zero", 0, false, 0, "", true, false},
		{"reported-zero", 0, true, 0, "5m", true, true},
		{"positive-schema", 3, false, 0, "", true, true},
		{"positive-receipt", 3, true, 3, "1h", true, true},
		{"receipt-mismatch", 3, true, 0, "5m", false, false},
		{"negative", -1, false, 0, "", false, false},
		{"overlapping-subsets", 7, false, 0, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := NewRunUsage(RunUsageLimits{})
			c, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
			if tc.observe {
				ObserveProviderCacheWrite(withProviderUsageResponseObserver(t.Context(), c), tc.receipt, tc.ttl)
			}
			u := &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10, PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 2, CacheWriteTokens: tc.written}}
			if err := c.CompleteProviderUsage(u); err != nil {
				t.Fatal(err)
			}
			s := r.Snapshot()
			record := s.CallLedger.Records[0]
			if (record.State == "known") != tc.wantKnown {
				t.Fatalf("record=%+v", record)
			}
			if !tc.wantKnown {
				if record.Tokens != nil || s.UnknownCalls != 1 || s.CacheWriteReportedCalls != 0 {
					t.Fatalf("invalid usage became known: %+v", s)
				}
				return
			}
			if (record.Tokens.CacheWritePromptTokens != nil) != tc.wantWrite || s.TotalTokens != 10 || record.Tokens.UncachedPromptTokens != 6 {
				t.Fatalf("presence or token partition changed: %+v", s)
			}
			if tc.wantWrite && (*record.Tokens.CacheWritePromptTokens != uint64(tc.written) || s.CacheWritePromptTokens != uint64(tc.written) || s.CacheWriteReportedCalls != 1 || s.Routes[0].CacheWriteReportedCalls != 1) {
				t.Fatalf("cache write lost: %+v", s)
			}
			if tc.observe && record.CacheTTL != tc.ttl {
				t.Fatalf("ttl=%q", record.CacheTTL)
			}
			data, _ := json.Marshal(record.Tokens)
			if strings.Contains(string(data), `"cache_write_prompt_tokens"`) != tc.wantWrite {
				t.Fatalf("JSON loses unknown versus zero: %s", data)
			}
		})
	}
}

func TestRunUsageCacheWriteObservationIsolationAndConflicts(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{MaxTotalTokens: 100})
	c, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	ctx := withProviderUsageResponseObserver(t.Context(), c)
	ObserveProviderCacheWrite(ctx, 3, "5m")
	ObserveProviderCacheWrite(ctx, 3, "1h")
	u := &schema.TokenUsage{PromptTokens: 8, PromptTokenDetails: schema.PromptTokenDetails{CacheWriteTokens: 3}}
	if err := c.CompleteProviderUsage(u); err != nil {
		t.Fatal(err)
	}
	ObserveProviderCacheWrite(ctx, 9, "private response text")
	s := r.Snapshot()
	if s.CallLedger.Records[0].CacheTTL != "" || *s.CallLedger.Records[0].Tokens.CacheWritePromptTokens != 3 {
		t.Fatalf("conflicting TTL or late observation changed receipt: %+v", s)
	}
	*s.CallLedger.Records[0].Tokens.CacheWritePromptTokens = 999
	if *r.Snapshot().CallLedger.Records[0].Tokens.CacheWritePromptTokens != 3 {
		t.Fatal("snapshot aliases cache-write pointer")
	}
	bad, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	ctx = withProviderUsageResponseObserver(t.Context(), bad)
	ObserveProviderCacheWrite(ctx, 3, "5m")
	ObserveProviderCacheWrite(ctx, 4, "5m")
	if err := bad.CompleteProviderUsage(u); err != ErrRunUsageUnknown {
		t.Fatalf("conflicting receipt did not stop token budget: %v", err)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{}); err == nil {
		t.Fatal("unknown usage allowed another budgeted call")
	}
}

func TestRunUsageCacheWriteCompositeBoundedAndOverflow(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	strict := &p242bUsageReporter{}
	call, err := CombineProviderUsage(r, strict).AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	if err != nil {
		t.Fatal(err)
	}
	ObserveProviderCacheWrite(withProviderUsageResponseObserver(t.Context(), call), 0, "untrusted metadata")
	if err := call.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.CacheWriteReportedCalls != 1 || s.CallLedger.Records[0].CacheTTL != "" {
		t.Fatalf("composite dropped presence or leaked metadata: %+v", s)
	}
	// Totals must survive the bounded ledger dropping later records.
	r.mu.Lock()
	r.records = make([]RunUsageRecord, MaxRunUsageRecords)
	r.mu.Unlock()
	call, _ = r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	ObserveProviderCacheWrite(withProviderUsageResponseObserver(t.Context(), call), 0, "5m")
	call.CompleteProviderUsage(&schema.TokenUsage{})
	if s := r.Snapshot(); s.CacheWriteReportedCalls != 2 || s.CallLedger.DroppedRecords != 1 {
		t.Fatalf("dropped record suppressed total: %+v", s.RunUsageTotals)
	}
	if canAddRunTokens(RunUsageTotals{CacheWritePromptTokens: math.MaxUint64}, RunUsageTotals{CacheWritePromptTokens: 1}) || canAddRunTokens(RunUsageTotals{CacheWriteReportedCalls: math.MaxUint64}, RunUsageTotals{CacheWriteReportedCalls: 1}) {
		t.Fatal("cache-write overflow accepted")
	}
}

func TestRunUsageCacheWriteDoesNotAddTokensOrCost(t *testing.T) {
	r, err := NewPricedRunUsage(RunUsageLimits{}, fixturePriceCard())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"})
	if err != nil {
		t.Fatal(err)
	}
	usage := &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14, PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 6, CacheWriteTokens: 3}}
	if err := call.CompleteProviderUsage(usage); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	// Six reads at 1 + four other input tokens at 2 + four output at 4.
	if s.TotalTokens != 14 || s.Cost.KnownEstimatedAmount != "0.000030000000" || s.CallLedger.Records[0].Tokens.UncachedPromptTokens != 4 {
		t.Fatalf("cache write was charged twice: %+v", s)
	}
}

type cacheWriteOnlyFixtureError struct{}

func (*cacheWriteOnlyFixtureError) Error() string { return "independent cache detail" }
func (*cacheWriteOnlyFixtureError) ProviderCacheWriteReceipt() (int, bool, string) {
	return 0, true, "5m"
}

func TestCacheWriteReceiptMustBelongToFinalUsageError(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	call, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	primary := &finalUsageFixtureError{cause: errors.New("terminal failure"), usage: &schema.TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}
	cause := errors.Join(primary, &cacheWriteOnlyFixtureError{})
	if err := MarkProviderUsageAmbiguous(call, cause); !errors.Is(err, primary) {
		t.Fatal(err)
	}
	s := r.Snapshot()
	record := s.CallLedger.Records[0]
	if s.KnownCalls != 1 || s.TotalTokens != 3 || s.CacheWriteReportedCalls != 0 || record.Tokens.CacheWritePromptTokens != nil || record.CacheTTL != "" {
		t.Fatalf("unrelated joined error invented cache-write presence: %+v", s)
	}
}
