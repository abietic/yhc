package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// MarketplaceClient communicates with a remote plugin marketplace API.
//
// Reference: src/utils/plugins/marketplaceManager.ts (~2,643 lines)
// This is a minimal client covering the core API surface: list, search, and
// metadata. The reference has additional features: GCS download, install counts,
// startup checks, and headless install that remain future work.
type MarketplaceClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// MarketplaceSearchResult represents a plugin from the marketplace API.
type MarketplaceSearchResult struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Downloads   int    `json:"downloads"`
	Source      string `json:"source"`
	Homepage    string `json:"homepage"`
}

// MarketplaceListResponse is the response from the list/search endpoint.
type MarketplaceListResponse struct {
	Plugins    []MarketplaceSearchResult `json:"plugins"`
	TotalCount int                       `json:"totalCount"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"pageSize"`
}

// NewMarketplaceClient creates a client for the given marketplace URL.
func NewMarketplaceClient(baseURL string) *MarketplaceClient {
	return &MarketplaceClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Search queries the marketplace for plugins matching the given term.
func (c *MarketplaceClient) Search(ctx context.Context, query string, page, pageSize int) (*MarketplaceListResponse, error) {
	u, err := url.Parse(c.BaseURL + "/plugins")
	if err != nil {
		return nil, fmt.Errorf("marketplace: invalid URL: %w", err)
	}
	q := u.Query()
	if query != "" {
		q.Set("q", query)
	}
	if page > 0 {
		q.Set("page", fmt.Sprintf("%d", page))
	}
	if pageSize > 0 {
		q.Set("pageSize", fmt.Sprintf("%d", pageSize))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("marketplace: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("marketplace: HTTP %d: %s", resp.StatusCode, body)
	}

	var result MarketplaceListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("marketplace: decode response: %w", err)
	}
	return &result, nil
}

// GetPlugin retrieves detailed metadata for a specific plugin by name.
func (c *MarketplaceClient) GetPlugin(ctx context.Context, name string) (*MarketplaceSearchResult, error) {
	u := c.BaseURL + "/plugins/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("marketplace: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("marketplace: plugin %q not found", name)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("marketplace: HTTP %d: %s", resp.StatusCode, body)
	}

	var result MarketplaceSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("marketplace: decode response: %w", err)
	}
	return &result, nil
}
