// Package mcp provides infrastructure for connecting to external tool servers
// via the Model Context Protocol (MCP). It manages server lifecycles, tool
// discovery, and proxies tool invocations to the appropriate server using
// the official MCP Go SDK.
package mcp

import (
	"os"
	"strconv"
	"time"

	"github.com/abietic/yhc/engine/containment"
)

// ServerStatus represents the connection state of an MCP server.
type ServerStatus string

const (
	StatusDisconnected ServerStatus = "disconnected"
	StatusConnecting   ServerStatus = "connecting"
	StatusConnected    ServerStatus = "connected"
	StatusError        ServerStatus = "error"
	StatusFailed       ServerStatus = "failed"
	StatusNeedsAuth    ServerStatus = "needs-auth"
	StatusDisabled     ServerStatus = "disabled"
)

// MCPTool describes a tool provided by an MCP server.
type MCPTool struct {
	// Name is the tool identifier used in tool calls.
	Name string `json:"name"`
	// Description explains what the tool does.
	Description string `json:"description"`
	// InputSchema is the JSON Schema describing the tool's parameters.
	InputSchema map[string]any `json:"inputSchema"`
	// ServerName identifies which MCP server provides this tool.
	ServerName string `json:"-"`

	// Annotations from the MCP server (optional, per MCP spec).
	Annotations ToolAnnotations `json:"-"`

	// SearchHint is extra keyword text for tool-search scoring (from _meta['anthropic/searchHint']).
	SearchHint string `json:"-"`
	// AlwaysLoad means this tool should never be deferred (from _meta['anthropic/alwaysLoad']).
	AlwaysLoad bool `json:"-"`
}

// ToolAnnotations holds MCP tool annotation hints from the server.
type ToolAnnotations struct {
	// Title is the human-friendly display name.
	Title string
	// ReadOnlyHint indicates the tool does not modify state.
	ReadOnlyHint bool
	// DestructiveHint indicates the tool may destroy data.
	DestructiveHint bool
	// OpenWorldHint indicates the tool accesses external/network resources.
	OpenWorldHint bool
}

// MCPServer represents a connected (or configured) MCP server instance.
type MCPServer struct {
	// Name is the display name of this server.
	Name string
	// Command is the shell command to start the server (stdio transport).
	Command string
	// Args are command-line arguments for the server process.
	Args []string
	// Env are additional environment variables passed to the server process.
	Env map[string]string
	// Status is the current connection status.
	Status ServerStatus
	// ErrorMsg holds the last error message when Status is StatusError.
	ErrorMsg string
}

// ServerConfig holds the configuration needed to create an MCPClient.
type ServerConfig struct {
	// Name is the unique identifier for this server.
	Name string
	// Command is the executable to launch (stdio transport).
	Command string
	// Args are command-line arguments.
	Args []string
	// Env are additional environment variables for the server process.
	Env map[string]string
	// CWD is the working directory for the server process.
	CWD string
	// Timeout is the maximum time to wait for tool call responses.
	Timeout time.Duration
	// Type is the transport type: "stdio" (default), "sse", or "http".
	Type string
	// URL is the server URL (for "sse" and "http" transport types).
	URL string
	// Headers are custom HTTP headers (for "sse" and "http" transport types).
	Headers map[string]string
	// ExecutionPolicy is the immutable identity bound before any configured
	// stdio server process starts. Network transports retain it as metadata only.
	ExecutionPolicy *containment.Snapshot
	// ExecutionBinding is the explicit identity for a stdio child. HTTP and SSE
	// retain it only as metadata and never claim containment.
	ExecutionBinding *containment.Binding
}

// ContentBlock represents a single content block in a tool result.
type ContentBlock struct {
	// Type is the content type, typically "text".
	Type string `json:"type"`
	// Text is the text content of this block.
	Text string `json:"text"`
}

// ToolResult represents the result of calling an MCP tool.
type ToolResult struct {
	// Content contains the response content blocks.
	Content []ContentBlock `json:"content"`
	// IsError indicates whether the tool call resulted in an error.
	IsError bool `json:"isError,omitempty"`
}

// MCPResource describes a resource provided by an MCP server.
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	MimeType    string `json:"mimeType,omitempty"`
	Description string `json:"description,omitempty"`
	ServerName  string `json:"server,omitempty"`
}

// ResourceContent describes a single content entry from resources/read.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// MCPResourceTemplate describes a resource template from an MCP server.
type MCPResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	ServerName  string `json:"server,omitempty"`
}

// MCPPrompt describes a prompt provided by an MCP server.
type MCPPrompt struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Arguments   []MCPPromptArgument `json:"arguments,omitempty"`
	ServerName  string              `json:"server,omitempty"`
}

// MCPPromptArgument describes an argument that a prompt accepts.
type MCPPromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// MCPPromptMessage represents a single message in a prompt result.
type MCPPromptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// MCPPromptResult is the result of getting a prompt from a server.
type MCPPromptResult struct {
	Description string             `json:"description,omitempty"`
	Messages    []MCPPromptMessage `json:"messages"`
}

// defaultToolTimeout is used when no timeout is configured and MCP_TOOL_TIMEOUT is not set.
const defaultToolTimeout = 60 * time.Second

// defaultMaxMCPOutputTokens is the default token cap for MCP tool results (matches TS reference).
const defaultMaxMCPOutputTokens = 25000

// bytesPerToken is a conservative estimate for chars-per-token conversion.
const bytesPerToken = 4

// truncationMessage is appended when content is truncated.
const truncationMessage = "\n\n[OUTPUT TRUNCATED - exceeded token limit]\n\nThe tool output was truncated. " +
	"If this MCP server provides pagination or filtering tools, use them to retrieve specific portions of the data. " +
	"If pagination is not available, inform the user that you are working with truncated output and results may be incomplete."

// getMaxMCPOutputChars returns the max character count for MCP tool results.
// Checks MAX_MCP_OUTPUT_TOKENS env var, falls back to default 25000 tokens × 4 = 100000 chars.
func getMaxMCPOutputChars() int {
	maxTokens := defaultMaxMCPOutputTokens
	if envVal := os.Getenv("MAX_MCP_OUTPUT_TOKENS"); envVal != "" {
		if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 {
			maxTokens = parsed
		}
	}
	return maxTokens * bytesPerToken
}

// TruncateToolResult truncates tool result content blocks if the total text
// exceeds the MCP output token limit. Returns the result unchanged if within limits.
func TruncateToolResult(result *ToolResult) *ToolResult {
	if result == nil || len(result.Content) == 0 {
		return result
	}

	maxChars := getMaxMCPOutputChars()

	// Calculate total text size.
	totalSize := 0
	for _, block := range result.Content {
		totalSize += len(block.Text)
	}

	if totalSize <= maxChars {
		return result
	}

	// Truncate: walk blocks and cut when budget exhausted.
	truncated := make([]ContentBlock, 0, len(result.Content)+1)
	remaining := maxChars
	for _, block := range result.Content {
		if block.Type != "text" {
			truncated = append(truncated, block)
			continue
		}
		if remaining <= 0 {
			break
		}
		if len(block.Text) <= remaining {
			truncated = append(truncated, block)
			remaining -= len(block.Text)
		} else {
			truncated = append(truncated, ContentBlock{
				Type: "text",
				Text: block.Text[:remaining],
			})
			remaining = 0
		}
	}

	// Append truncation notice.
	truncated = append(truncated, ContentBlock{
		Type: "text",
		Text: truncationMessage,
	})

	return &ToolResult{
		Content: truncated,
		IsError: result.IsError,
	}
}
