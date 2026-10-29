package tools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
)

// One explicit opt-in request; no LLM, retries, or raw result logging. The native
// fee cap includes the request even when its response becomes ambiguous.
func TestLiveDeepSeekWebSearch(t *testing.T) {
	if os.Getenv("YHC_WEBSEARCH_LIVE_TEST") != "1" {
		t.Skip("explicit live search opt-in required")
	}
	if os.Getenv("YHC_DEEPSEEK_SEARCH_API_KEY") == "" {
		t.Fatal("search API key required")
	}
	fee := "0.005"
	run, err := execution.NewPricedRunUsage(execution.RunUsageLimits{MaxEstimatedCost: fee, ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 1, MaxProviderCalls: 1}, execution.RunPriceCard{
		Version: 1, Currency: "CNY", Source: "https://api-docs.deepseek.com/zh-cn/guides/search/", AsOf: time.Now().UTC().Format(time.RFC3339), Window: "upper_bound",
		Routes: []execution.RunPriceRoute{{Provider: "agenticdeepseek", Model: "search", APIModel: "search", CachedInputPerMillion: "0", UncachedInputPerMillion: "0", OutputPerMillion: "0", PerRequest: &fee}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if path := os.Getenv("YHC_WEBSEARCH_RECEIPT_PATH"); path != "" {
			data, err := json.MarshalIndent(run.Snapshot(), "", "  ")
			if err == nil {
				err = os.WriteFile(path, data, 0o600)
			}
			if err != nil {
				t.Error("write sanitized usage receipt:", err)
			}
		}
	}()
	ctx := execution.WithProviderUsageScope(t.Context(), run, false)
	result, err := WebSearchTool().ExecuteCtx(ctx, `{"query":"DeepSeek V4.1 Flash 1017 site:api-docs.deepseek.com","allowed_domains":["api-docs.deepseek.com"]}`)
	if err != nil {
		t.Fatal(err)
	}
	s := run.Snapshot()
	if !strings.Contains(result, "https://api-docs.deepseek.com/") || s.ProviderCalls != 1 || s.KnownCalls != 1 || !s.Cost.Complete || s.Cost.KnownEstimatedAmount != "0.005000000000" {
		t.Fatal("official search did not return a priced result; raw content omitted")
	}
}
