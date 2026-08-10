package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	webSearchMaxResults = 10
	webSearchTimeout    = 30 * time.Second
	webSearchUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// WebSearchTool returns a tool that searches the web using DuckDuckGo and returns results.
func WebSearchTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "WebSearch",
			Desc: "Searches the web and returns results to inform responses.\n\n" +
				"- Provides up-to-date information for current events and recent data\n" +
				"- Returns search result information including titles, snippets, and URLs as markdown hyperlinks\n" +
				"- Use this tool for accessing information beyond the model's knowledge cutoff\n" +
				"- After answering, include a \"Sources:\" section with relevant URLs as markdown hyperlinks\n" +
				"- Use the correct current year in search queries for recent information",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"query":           {Type: schema.String, Desc: "The search query to use", Required: true},
				"allowed_domains": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "Only include search results from these domains"},
				"blocked_domains": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "Never include search results from these domains"},
			}),
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute: executeWebSearch,
	}
}

func executeWebSearch(input string) (string, error) {
	var params struct {
		Query          string   `json:"query"`
		AllowedDomains []string `json:"allowed_domains"`
		BlockedDomains []string `json:"blocked_domains"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("websearch: invalid params: %w", err)
	}
	if strings.TrimSpace(params.Query) == "" {
		return "", fmt.Errorf("websearch: query is required and must be non-empty")
	}

	// Construct the DuckDuckGo HTML search URL.
	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(params.Query)

	// Fetch the search results page.
	body, err := fetchSearchPage(searchURL)
	if err != nil {
		return "", fmt.Errorf("websearch: failed to fetch search results: %w", err)
	}

	// Parse search results from the HTML.
	results := parseDuckDuckGoResults(body)

	// Apply domain filtering.
	results = filterByDomain(results, params.AllowedDomains, params.BlockedDomains)

	// Limit to max results.
	if len(results) > webSearchMaxResults {
		results = results[:webSearchMaxResults]
	}

	if len(results) == 0 {
		return fmt.Sprintf("No search results found for query: %s", params.Query), nil
	}

	// Format results as a numbered list.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for: %s\n\n", params.Query)
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. [%s](%s)\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", r.Snippet)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// searchResult holds a single parsed search result.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// fetchSearchPage fetches the search URL and returns the response body.
func fetchSearchPage(searchURL string) (string, error) {
	client := &http.Client{
		Timeout: webSearchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, searchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024)) // 2MB limit
	if err != nil {
		return "", fmt.Errorf("reading response body: %w", err)
	}

	return string(bodyBytes), nil
}

// Precompiled regex patterns for DuckDuckGo HTML result parsing.
var (
	// Match individual result blocks.
	reResultBlock = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*result[^"]*"[^>]*>.*?</div>\s*</div>`)
	// Match the result title link.
	reResultTitle = regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	// Match the result snippet.
	reResultSnippet = regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	// Match HTML tags for stripping.
	reHTMLTags = regexp.MustCompile(`<[^>]+>`)
)

// parseDuckDuckGoResults extracts search results from DuckDuckGo HTML response.
func parseDuckDuckGoResults(html string) []searchResult {
	var results []searchResult

	blocks := reResultBlock.FindAllString(html, -1)
	for _, block := range blocks {
		// Extract title and URL from the result link.
		titleMatch := reResultTitle.FindStringSubmatch(block)
		if titleMatch == nil {
			continue
		}

		rawHref := titleMatch[1]
		rawTitle := titleMatch[2]

		// Clean the title (strip HTML tags).
		title := strings.TrimSpace(reHTMLTags.ReplaceAllString(rawTitle, ""))
		title = decodeHTMLEntities(title)
		if title == "" {
			continue
		}

		// Decode the URL from DuckDuckGo redirect format.
		resultURL := decodeDDGURL(rawHref)
		if resultURL == "" {
			continue
		}

		// Extract snippet.
		var snippet string
		snippetMatch := reResultSnippet.FindStringSubmatch(block)
		if snippetMatch != nil {
			snippet = strings.TrimSpace(reHTMLTags.ReplaceAllString(snippetMatch[1], ""))
			snippet = decodeHTMLEntities(snippet)
		}

		results = append(results, searchResult{
			Title:   title,
			URL:     resultURL,
			Snippet: snippet,
		})
	}

	return results
}

// decodeDDGURL extracts the actual URL from a DuckDuckGo redirect link.
// DuckDuckGo href format: //duckduckgo.com/l/?uddg=<encoded_url>&rut=...
func decodeDDGURL(href string) string {
	href = strings.TrimSpace(href)
	href = decodeHTMLEntities(href)

	// Handle DuckDuckGo redirect URLs.
	if strings.Contains(href, "duckduckgo.com/l/?") {
		// Ensure it has a scheme for parsing.
		if strings.HasPrefix(href, "//") {
			href = "https:" + href
		}
		parsed, err := url.Parse(href)
		if err != nil {
			return ""
		}
		uddg := parsed.Query().Get("uddg")
		if uddg != "" {
			return uddg
		}
	}

	// Handle direct URLs.
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}

	return ""
}

// filterByDomain applies allowed and blocked domain filtering to results.
func filterByDomain(results []searchResult, allowed, blocked []string) []searchResult {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}

	// Build lookup sets for efficient matching.
	allowedSet := make(map[string]bool, len(allowed))
	for _, d := range allowed {
		allowedSet[strings.ToLower(strings.TrimSpace(d))] = true
	}
	blockedSet := make(map[string]bool, len(blocked))
	for _, d := range blocked {
		blockedSet[strings.ToLower(strings.TrimSpace(d))] = true
	}

	var filtered []searchResult
	for _, r := range results {
		host := extractHost(r.URL)
		if host == "" {
			continue
		}

		// Check blocked domains.
		if matchesDomainSet(host, blockedSet) {
			continue
		}

		// Check allowed domains (if specified, only include matching).
		if len(allowedSet) > 0 && !matchesDomainSet(host, allowedSet) {
			continue
		}

		filtered = append(filtered, r)
	}
	return filtered
}

// matchesDomainSet checks if a host matches any domain in the set.
// Supports both exact match and suffix match (e.g., "docs.example.com" matches "example.com").
func matchesDomainSet(host string, domainSet map[string]bool) bool {
	host = strings.ToLower(host)
	if domainSet[host] {
		return true
	}
	// Check if host is a subdomain of any domain in the set.
	for domain := range domainSet {
		if strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// extractHost extracts the hostname from a URL string.
func extractHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
