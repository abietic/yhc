package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func searchResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestWebSearchDeepSeekWireAndFiltering(t *testing.T) {
	var calls int
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.deepseek.com/v1/tools/search" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-search-key" {
			t.Fatalf("wrong search request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["query"] != "golang docs" || body["count"] != float64(10) || body["need_content"] != false || len(body) != 3 {
			t.Fatalf("unexpected wire schema: %v", body)
		}
		return searchResponse(200, `{"results":[{"title":"Go","url":"https://go.dev/doc/","summary":"Official Go docs","snippet":"short"},{"title":"Blocked","url":"https://blocked.go.dev/","summary":"blocked"},{"title":"Unsafe","url":"javascript:alert(1)"}]}`), nil
	})}
	tool := webSearchTool(webSearchConfig{apiKey: "test-search-key", client: client})
	got, err := tool.ExecuteCtx(t.Context(), `{"query":"golang docs","allowed_domains":["go.dev"],"blocked_domains":["blocked.go.dev"]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "Search results for: golang docs\n\n1. [Go](https://go.dev/doc/)\n   Official Go docs\n\n"
	if got != want || calls != 1 {
		t.Fatalf("got %q with %d calls", got, calls)
	}
}

func TestWebSearchFallbackNeverForwardsCredential(t *testing.T) {
	for _, first := range []string{"status", "malformed", "empty", "redirect", "long"} {
		t.Run(first, func(t *testing.T) {
			var paid, duck int
			client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "api.deepseek.com" {
					paid++
					switch first {
					case "status":
						return searchResponse(429, "test-search-key private server message"), nil
					case "malformed":
						return searchResponse(200, `{}`), nil
					case "empty":
						return searchResponse(200, `{"results":[]}`), nil
					case "redirect":
						response := searchResponse(307, "")
						response.Header.Set("Location", "https://evil.example/steal")
						return response, nil
					}
				}
				if r.URL.Host != "html.duckduckgo.com" || r.Header.Get("Authorization") != "" {
					t.Fatal("credential crossed search backend")
				}
				duck++
				return searchResponse(200, `<div class="result results_links"><div><a class="result__a" href="https://go.dev/doc/">Go</a><a class="result__snippet">Docs</a></div></div>`), nil
			})}
			query := "docs"
			if first == "long" {
				query = strings.Repeat("中", 129)
			}
			input, _ := json.Marshal(map[string]string{"query": query})
			got, err := webSearchTool(webSearchConfig{apiKey: "test-search-key", client: client}).ExecuteCtx(t.Context(), string(input))
			if err != nil || !strings.Contains(got, "https://go.dev/doc/") || strings.Contains(got, "test-search-key") || duck != 1 {
				t.Fatalf("fallback failed: %q %v paid=%d duck=%d", got, err, paid, duck)
			}
			if first == "long" && paid != 0 || first != "long" && paid != 1 {
				t.Fatal("unexpected paid retry")
			}
		})
	}
}

func TestWebSearchCancelledDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var calls int
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})}
	_, err := webSearchTool(webSearchConfig{apiKey: "key", client: client}).ExecuteCtx(ctx, `{"query":"docs"}`)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancel started fallback: %v %d", err, calls)
	}
}

func TestWebSearchBudgetWithoutRequestPriceUsesFreeFallback(t *testing.T) {
	run, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 1})
	ctx := execution.WithProviderUsageScope(t.Context(), run, false)
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "html.duckduckgo.com" {
			t.Fatal("unpriced paid dispatch escaped budget")
		}
		return searchResponse(200, ""), nil
	})}
	_, err := webSearchTool(webSearchConfig{apiKey: "key", client: client}).ExecuteCtx(ctx, `{"query":"docs"}`)
	if err != nil || run.Snapshot().ProviderCalls != 0 || run.Snapshot().DeniedCalls != 0 {
		t.Fatalf("optional search poisoned run: %v", err)
	}
}

func TestWebSearchBudgetRetainsUnknownFeeAndDoesNotFallback(t *testing.T) {
	fee := "0.005"
	run, err := execution.NewPricedRunUsage(execution.RunUsageLimits{MaxEstimatedCost: fee, ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 1}, execution.RunPriceCard{
		Version: 1, Currency: "CNY", Source: "https://example.org/pricing", AsOf: "2026-10-18T00:00:00Z", Window: "upper_bound",
		Routes: []execution.RunPriceRoute{{Provider: "agenticdeepseek", Model: "search", APIModel: "search", CachedInputPerMillion: "0", UncachedInputPerMillion: "0", OutputPerMillion: "0", PerRequest: &fee}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.deepseek.com" {
			t.Fatal("ambiguous paid search triggered fallback")
		}
		return searchResponse(200, `{"results":null}`), nil
	})}
	ctx := execution.WithProviderUsageScope(t.Context(), run, false)
	_, err = webSearchTool(webSearchConfig{apiKey: "key", client: client}).ExecuteCtx(ctx, `{"query":"docs"}`)
	if !execution.IsProviderUsageTerminalError(err) || calls != 1 {
		t.Fatalf("unknown fee did not stop: %v %d", err, calls)
	}
	s := run.Snapshot()
	if s.Cost.Reservation.RetainedAmount != "0.005000000000" || s.Cost.Complete || s.UnknownCalls != 1 {
		t.Fatal("lost unknown search fee")
	}
}
