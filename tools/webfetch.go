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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

const (
	webFetchMaxResponseBytes = 100 * 1024 // 100KB text limit
	webFetchTimeout          = 30 * time.Second
	webFetchUserAgent        = "Mozilla/5.0 (compatible; YHC/1.0)"
)

// WebFetchSideModel is the model used for AI processing mode in WebFetch.
// Set this at startup to enable AI summarization of fetched content.
var WebFetchSideModel model.BaseChatModel

type webFetchModelCtxKey struct{}

// WithWebFetchModel returns a context carrying the WebFetch side model.
func WithWebFetchModel(ctx context.Context, m model.BaseChatModel) context.Context {
	return context.WithValue(ctx, webFetchModelCtxKey{}, m)
}

// WebFetchModelFromCtx returns the per-engine WebFetch model from context,
// falling back to WebFetchSideModel if not set.
func WebFetchModelFromCtx(ctx context.Context) model.BaseChatModel {
	if m, ok := ctx.Value(webFetchModelCtxKey{}).(model.BaseChatModel); ok && m != nil {
		return m
	}
	return WebFetchSideModel
}

func WebFetchTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "WebFetch",
			Desc: "Fetches content from a specified URL with optional AI processing.\n\n" +
				"This tool has two modes:\n" +
				"1. AI Processing Mode (default): Fetches URL content and uses an AI model to extract/analyze information based on your prompt\n" +
				"2. Raw Content Mode: Returns the complete fetched content directly without AI processing (set raw_mode=true)\n\n" +
				"Usage notes:\n" +
				"  - The URL must be a fully-formed valid URL\n" +
				"  - HTTP URLs will be automatically upgraded to HTTPS\n" +
				"  - In AI processing mode, the prompt describes what information you want to extract from the page\n" +
				"  - In raw content mode, the prompt parameter is optional and will be ignored if provided\n" +
				"  - This tool is read-only and does not modify any files\n" +
				"  - Results may be summarized if the content is very large (in AI processing mode)\n" +
				"  - When a URL redirects to a different host, the tool will inform you and provide the redirect URL",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"url":      {Type: schema.String, Desc: "The URL to fetch content from", Required: true},
				"prompt":   {Type: schema.String, Desc: "The prompt to run on the fetched content", Required: true},
				"raw_mode": {Type: schema.Boolean, Desc: "Set to true to return raw content without AI processing. Defaults to false (AI processing mode)"},
			}),
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute:    executeWebFetch,
		ExecuteCtx: executeWebFetchCtx,
	}
}

func executeWebFetch(input string) (string, error) {
	return executeWebFetchCtx(context.Background(), input)
}

func executeWebFetchCtx(ctx context.Context, input string) (string, error) {
	var params struct {
		URI     string `json:"url"`
		Prompt  string `json:"prompt"`
		RawMode *bool  `json:"raw_mode"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("webfetch: invalid params: %w", err)
	}
	if params.URI == "" {
		return "", fmt.Errorf("webfetch: url is required")
	}
	if params.Prompt == "" {
		return "", fmt.Errorf("webfetch: prompt is required")
	}

	rawMode := params.RawMode != nil && *params.RawMode

	// Validate and normalize the URL.
	fetchURL, err := normalizeURL(params.URI)
	if err != nil {
		return "", fmt.Errorf("webfetch: invalid URL %q: %w", params.URI, err)
	}

	// Fetch the page content.
	body, finalURL, err := fetchPage(fetchURL)
	if err != nil {
		return "", fmt.Errorf("webfetch: failed to fetch %q: %w", fetchURL, err)
	}

	// Detect cross-host redirect.
	origHost := mustParseHost(fetchURL)
	finalHost := mustParseHost(finalURL)
	if origHost != finalHost {
		return fmt.Sprintf(
			"The URL redirected to a different host.\n\nOriginal URL: %s\nRedirect URL: %s\n\nPlease make a new request with the redirect URL to fetch the content.",
			fetchURL, finalURL,
		), nil
	}

	// Convert HTML to readable text/markdown.
	text := htmlToText(body)

	// Truncate if too large.
	if len(text) > webFetchMaxResponseBytes {
		text = text[:webFetchMaxResponseBytes] + "\n\n[Content truncated at 100KB]"
	}

	if rawMode {
		return text, nil
	}

	sideModel := WebFetchModelFromCtx(ctx)
	// If no side model configured, fall back to returning content with prompt context.
	if sideModel == nil {
		var result strings.Builder
		result.WriteString("Content fetched from: ")
		result.WriteString(finalURL)
		result.WriteString("\n\n")
		result.WriteString("Prompt: ")
		result.WriteString(params.Prompt)
		result.WriteString("\n\n")
		result.WriteString("--- Page Content ---\n\n")
		result.WriteString(text)
		return result.String(), nil
	}

	// Call the side model with the fetched content and user prompt.
	aiResult, err := webFetchAIProcess(
		ctx,
		sideModel,
		params.Prompt,
		text,
		finalURL,
	)
	if err != nil {
		if execution.IsProviderUsageTerminalError(err) {
			return "", err
		}
		// On model failure, fall back to raw content with prompt.
		var result strings.Builder
		result.WriteString("Content fetched from: ")
		result.WriteString(finalURL)
		result.WriteString("\n\n(AI processing failed: ")
		result.WriteString(err.Error())
		result.WriteString(")\n\n--- Page Content ---\n\n")
		result.WriteString(text)
		return result.String(), nil
	}

	return aiResult, nil
}

// normalizeURL validates and normalizes the URL, upgrading http to https.
func normalizeURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	// Auto-upgrade HTTP to HTTPS.
	if parsed.Scheme == "http" {
		parsed.Scheme = "https"
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("missing host in URL")
	}
	return parsed.String(), nil
}

// fetchPage fetches the URL and returns the response body and final URL after redirects.
func fetchPage(fetchURL string) (string, string, error) {
	client := &http.Client{
		Timeout: webFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fetchURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", webFetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	// Read body with a size limit to avoid OOM on very large pages.
	limitedReader := io.LimitReader(resp.Body, 5*1024*1024) // 5MB raw limit
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", "", fmt.Errorf("reading response body: %w", err)
	}

	finalURL := resp.Request.URL.String()
	return string(bodyBytes), finalURL, nil
}

// mustParseHost extracts the host from a URL string, returning empty on failure.
func mustParseHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// htmlToText converts HTML content to a readable text/markdown format.
func htmlToText(html string) string {
	// Remove script and style tags entirely (including content).
	html = reScript.ReplaceAllString(html, "")
	html = reStyle.ReplaceAllString(html, "")

	// Remove HTML comments.
	html = reComment.ReplaceAllString(html, "")

	// Convert headers to markdown.
	html = reH1.ReplaceAllString(html, "\n# $1\n")
	html = reH2.ReplaceAllString(html, "\n## $1\n")
	html = reH3.ReplaceAllString(html, "\n### $1\n")
	html = reH4.ReplaceAllString(html, "\n#### $1\n")
	html = reH5.ReplaceAllString(html, "\n##### $1\n")
	html = reH6.ReplaceAllString(html, "\n###### $1\n")

	// Convert links to markdown format.
	html = reLink.ReplaceAllString(html, "[$2]($1)")

	// Convert line-break tags.
	html = reBr.ReplaceAllString(html, "\n")

	// Convert paragraph and div boundaries to double newlines.
	html = reBlockOpen.ReplaceAllString(html, "\n\n")
	html = reBlockClose.ReplaceAllString(html, "\n\n")

	// Convert list items.
	html = reLi.ReplaceAllString(html, "\n- ")

	// Strip all remaining HTML tags.
	html = reTag.ReplaceAllString(html, "")

	// Decode common HTML entities.
	html = decodeHTMLEntities(html)

	// Normalize whitespace: collapse multiple blank lines.
	html = reMultiNewline.ReplaceAllString(html, "\n\n")

	// Trim leading/trailing whitespace from each line and overall.
	lines := strings.Split(html, "\n")
	var cleaned []string
	for _, line := range lines {
		cleaned = append(cleaned, strings.TrimRight(line, " \t"))
	}
	result := strings.Join(cleaned, "\n")
	return strings.TrimSpace(result)
}

// decodeHTMLEntities handles common HTML entities.
func decodeHTMLEntities(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
		"&nbsp;", " ",
		"&#x27;", "'",
		"&#x2F;", "/",
		"&mdash;", "\u2014",
		"&ndash;", "\u2013",
		"&hellip;", "\u2026",
		"&laquo;", "\u00AB",
		"&raquo;", "\u00BB",
		"&copy;", "\u00A9",
		"&reg;", "\u00AE",
		"&trade;", "\u2122",
	)
	return replacer.Replace(s)
}

// Precompiled regex patterns for HTML-to-text conversion.
var (
	reScript       = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle        = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reComment      = regexp.MustCompile(`(?s)<!--.*?-->`)
	reH1           = regexp.MustCompile(`(?i)<h1[^>]*>(.*?)</h1>`)
	reH2           = regexp.MustCompile(`(?i)<h2[^>]*>(.*?)</h2>`)
	reH3           = regexp.MustCompile(`(?i)<h3[^>]*>(.*?)</h3>`)
	reH4           = regexp.MustCompile(`(?i)<h4[^>]*>(.*?)</h4>`)
	reH5           = regexp.MustCompile(`(?i)<h5[^>]*>(.*?)</h5>`)
	reH6           = regexp.MustCompile(`(?i)<h6[^>]*>(.*?)</h6>`)
	reLink         = regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']*)["'][^>]*>(.*?)</a>`)
	reBr           = regexp.MustCompile(`(?i)<br\s*/?>`)
	reBlockOpen    = regexp.MustCompile(`(?i)<(?:p|div|article|section|main|header|footer|nav|blockquote|ul|ol|table|tr)[^>]*>`)
	reBlockClose   = regexp.MustCompile(`(?i)</(?:p|div|article|section|main|header|footer|nav|blockquote|ul|ol|table|tr)>`)
	reLi           = regexp.MustCompile(`(?i)<li[^>]*>`)
	reTag          = regexp.MustCompile(`<[^>]+>`)
	reMultiNewline = regexp.MustCompile(`\n{3,}`)
)

// webFetchAIProcess calls the side model to analyze/summarize fetched content.
func webFetchAIProcess(
	ctx context.Context,
	sideModel model.BaseChatModel,
	prompt string,
	content string,
	sourceURL string,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Trim content for the model context window (keep ~50KB for the model).
	const maxContentForModel = 50000
	modelContent := content
	if len(modelContent) > maxContentForModel {
		modelContent = modelContent[:maxContentForModel] + "\n\n[...content truncated for processing...]"
	}

	userMsg := &schema.Message{
		Role: schema.User,
		Content: fmt.Sprintf("Source URL: %s\n\nUser's prompt: %s\n\n--- Web Page Content ---\n\n%s",
			sourceURL, prompt, modelContent),
	}

	providerUsage, required := execution.ProviderUsageScopeFromContext(ctx)
	if required && providerUsage == nil {
		return "", fmt.Errorf(
			"goal provider usage capability is required for WebFetch AI processing",
		)
	}
	var logicalRoundID string
	if providerUsage != nil {
		logicalRoundID = providerUsage.NewLogicalRoundID()
	}
	resp, err := execution.SideQueryWithRetry(
		ctx,
		sideModel,
		execution.SideQueryOptions{
			SystemPrompt: "You are a content analysis assistant. The user will provide web page content and a question/prompt. " +
				"Analyze the content and provide a focused, concise answer to the user's prompt. " +
				"Only include information that is directly relevant to what was asked.",
			Messages:            []*schema.Message{userMsg},
			QuerySource:         "webfetch_ai_processing",
			ProviderUsage:       providerUsage,
			UsageLogicalRoundID: logicalRoundID,
		},
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("model call failed: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("model returned empty response")
	}

	return resp.Content, nil
}
