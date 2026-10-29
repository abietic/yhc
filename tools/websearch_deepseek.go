package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/execution"
)

// Standalone Search uses the documented JSON API; no model or server-side tool
// support is required. Never retry a paid request or redirect its bearer key.
func searchDeepSeek(ctx context.Context, query string, config webSearchConfig) ([]searchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	owner, required := execution.ProviderUsageScopeFromContext(ctx)
	d := execution.ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "search", QuerySource: "web_search", Request: true}
	if required && owner == nil || !execution.RequestUsageAvailable(owner, d) {
		return nil, nil
	}
	body, _ := json.Marshal(struct {
		Query       string `json:"query"`
		Count       int    `json:"count"`
		NeedContent bool   `json:"need_content"`
	}{query, webSearchMaxResults, false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.deepseek.com/v1/tools/search", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("websearch: invalid search request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.apiKey)
	// Avoid replaying an authenticated POST on a reused connection.
	req.GetBody = nil
	req.Close = true
	var call execution.ProviderUsageCall
	if owner != nil {
		d.LogicalRoundID = owner.NewLogicalRoundID()
		call, err = owner.AdmitProviderUsage(ctx, d)
		if err != nil {
			return nil, err
		}
		if call == nil || call.ProviderCallID() == "" {
			return nil, execution.ReleaseProviderUsageBeforeDispatch(call, &execution.ProviderUsageTerminalError{Err: fmt.Errorf("websearch: missing request accounting identity")})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, execution.ReleaseProviderUsageBeforeDispatch(call, err)
	}
	response, err := searchHTTPClient(config.client, true).Do(req)
	if err != nil {
		// URL errors and server bodies are excluded from diagnostics.
		if ctx.Err() != nil {
			return nil, execution.MarkProviderUsageAmbiguous(call, ctx.Err())
		}
		return nil, execution.MarkProviderUsageAmbiguous(call, fmt.Errorf("websearch: DeepSeek transport failed"))
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode != http.StatusOK {
		if err := execution.CompleteRequestUsage(call, false); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("websearch: DeepSeek HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	var payload struct {
		Results *[]struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Summary string `json:"summary"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err != nil || len(data) > 2*1024*1024 || json.Unmarshal(data, &payload) != nil || payload.Results == nil {
		return nil, execution.MarkProviderUsageAmbiguous(call, fmt.Errorf("websearch: invalid DeepSeek search response"))
	}
	if err := execution.CompleteRequestUsage(call, len(*payload.Results) != 0); err != nil {
		return nil, err
	}
	var results []searchResult
	for _, item := range *payload.Results {
		u, err := url.Parse(item.URL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		snippet := item.Summary
		if snippet == "" {
			snippet = item.Snippet
		}
		results = append(results, searchResult{Title: item.Title, URL: item.URL, Snippet: snippet})
	}
	return results, nil
}
