package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestMCPHistoryRendererDynamicToolProjections(t *testing.T) {
	tool := &ToolMessage{
		toolCallID: "mcp-call-1",
		name:       "mcp__github__search_code",
		input:      `{"query":"HistoryItem","limit":3,"access_token":"top-secret"}`,
		output:     `{"count":2,"repository":"eino-agent","result":{"path":"internal/tui/chat.go"}}`,
		status:     ToolSuccess,
		version:    2,
	}
	rendered := tool.Render(96, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{"MCP", "called", "github.search_code", "query:", "[redacted]", "count", "repository"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("MCP rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "top-secret") {
		t.Fatalf("MCP rich leaked sensitive header input: %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 96)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 72, Styles: defaultStyles()}))
	for _, want := range []string{"Arguments:", "top-secret", "Result:", `"internal/tui/chat.go"`} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("MCP expanded missing %q: %q", want, expanded)
		}
	}
	assertHistoryLinesFit(t, tool.RenderExpanded(HistoryRenderContext{Width: 72, Styles: defaultStyles()}), 72)

	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 40, Styles: defaultStyles()})
	for _, want := range []string{"MCP github.search_code", "Status: called", "Input:", "top-secret", "Result:", "internal/tui/chat.go"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("MCP transcript missing %q: %q", want, transcript)
		}
	}
	if strings.Contains(transcript, "\x1b[") {
		t.Fatalf("MCP transcript contains ANSI: %q", transcript)
	}
}

func TestMCPHistoryRendererLegacyGatewayUnwrapsAndBoundsText(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&body, "message-%02d\n", i)
	}
	output := fmt.Sprintf(`{"messages":%q,"next_cursor":"cursor-2"}`, body.String())
	tool := &ToolMessage{
		name:   "mcp_tool",
		input:  `{"server":"slack","tool":"read_channel","arguments":{"channel":"engineering"}}`,
		output: output, status: ToolSuccess, version: 1,
	}
	plain := stripANSIForTest(tool.Render(68, defaultStyles()))
	for _, want := range []string{
		"slack.read_channel", "channel:", "next_cursor: cursor-2", "message-01", "+6 lines", "message-12",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("legacy MCP rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "message-06") {
		t.Fatalf("legacy MCP output was not bounded: %q", plain)
	}
	assertHistoryLinesFit(t, tool.Render(68, defaultStyles()), 68)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 68, Styles: defaultStyles()}))
	if !strings.Contains(expanded, "message-06") || strings.Contains(expanded, "expand for details") {
		t.Fatalf("legacy MCP expanded = %q", expanded)
	}
}

func TestMCPHistoryRendererContentBlocksAndProtocolFailure(t *testing.T) {
	output := `{"content":[{"type":"text","text":"remote rejected request"},{"type":"image","data":"base64-payload"},{"type":"resource","resource":{"uri":"file:///tmp/result.txt"}}],"isError":true}`
	tool := &ToolMessage{
		name: "mcp__assets__generate", input: `{"prompt":"diagram"}`,
		output: output, status: ToolSuccess, version: 1,
	}
	plain := stripANSIForTest(tool.Render(72, defaultStyles()))
	for _, want := range []string{"failed", "assets.generate", "remote rejected request", "[image content]", "embedded resource: file:///tmp/result.txt"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("MCP protocol failure missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "base64-payload") {
		t.Fatalf("MCP rich rendered image payload: %q", plain)
	}

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 64, Styles: defaultStyles()}))
	if !strings.Contains(expanded, "base64-payload") || !strings.Contains(expanded, `"isError": true`) {
		t.Fatalf("MCP expanded did not retain payload: %q", expanded)
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{})
	if !strings.Contains(transcript, output) || !strings.Contains(transcript, "Status: failed") {
		t.Fatalf("MCP failure transcript = %q", transcript)
	}
}

func TestMCPHistoryRendererNamingFallbackMalformedAndNoContent(t *testing.T) {
	if _, ok := toolHistoryRendererFor("mcp__docs__lookup").(mcpToolHistoryRenderer); !ok {
		t.Fatalf("double-underscore MCP renderer = %T", toolHistoryRendererFor("mcp__docs__lookup"))
	}
	if _, ok := toolHistoryRendererFor("mcp_docs_lookup").(mcpToolHistoryRenderer); !ok {
		t.Fatalf("single-underscore MCP renderer = %T", toolHistoryRendererFor("mcp_docs_lookup"))
	}
	if _, ok := toolHistoryRendererFor("mcp_tool").(mcpToolHistoryRenderer); !ok {
		t.Fatalf("legacy MCP renderer = %T", toolHistoryRendererFor("mcp_tool"))
	}
	if _, ok := toolHistoryRendererFor("mcp__invalid").(genericToolHistoryRenderer); !ok {
		t.Fatalf("invalid MCP name renderer = %T", toolHistoryRendererFor("mcp__invalid"))
	}

	malformed := &ToolMessage{
		name: "mcp__docs__lookup", input: `{bad`, output: "transport failure",
		status: ToolError, version: 1,
	}
	rendered := malformed.Render(24, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "MCP") || !strings.Contains(plain, "failed") || !strings.Contains(plain, "transport failure") {
		t.Fatalf("malformed MCP = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 24)

	noContent := &ToolMessage{
		name: "mcp_docs_lookup", input: `{}`, status: ToolSuccess, version: 1,
	}
	if plain := stripANSIForTest(noContent.Render(48, defaultStyles())); !strings.Contains(plain, "docs.lookup") || !strings.Contains(plain, "(no content)") {
		t.Fatalf("no-content MCP = %q", plain)
	}
}

func TestMCPHistoryRendererLargeResponseWarning(t *testing.T) {
	tool := &ToolMessage{
		name: "mcp__logs__tail", input: `{}`,
		output: strings.Repeat("x", mcpHistoryLargeResponseThreshold+1),
		status: ToolSuccess, version: 1,
	}
	plain := stripANSIForTest(tool.Render(72, defaultStyles()))
	if !strings.Contains(plain, "Large MCP response") || !strings.Contains(plain, "expand deliberately") ||
		!strings.Contains(plain, "content clipped (expand for details)") {
		t.Fatalf("large MCP warning = %q", plain)
	}
	assertHistoryLinesFit(t, tool.Render(72, defaultStyles()), 72)
}
