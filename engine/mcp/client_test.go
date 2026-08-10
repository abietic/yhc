package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// =============================================================================
// MCPClient Creation and Configuration Tests
// =============================================================================

func TestNewMCPClient_DefaultState(t *testing.T) {
	cfg := ServerConfig{
		Name:    "test-server",
		Command: "echo",
		Args:    []string{"hello"},
	}

	client := NewMCPClient(cfg)
	if client == nil {
		t.Fatal("expected non-nil client")
		return
	}
	if client.Status() != StatusDisconnected {
		t.Fatalf("expected status %q, got %q", StatusDisconnected, client.Status())
	}
	if client.IsConnected() {
		t.Fatal("expected client to not be connected initially")
	}
}

func TestNewMCPClient_ConfigPreserved(t *testing.T) {
	cfg := ServerConfig{
		Name:    "my-server",
		Command: "/usr/bin/server",
		Args:    []string{"--port", "8080"},
		Env:     map[string]string{"API_KEY": "secret"},
		CWD:     "/tmp",
		Timeout: 30 * time.Second,
		Type:    "stdio",
	}

	client := NewMCPClient(cfg)
	if client.config.Name != "my-server" {
		t.Fatalf("expected name %q, got %q", "my-server", client.config.Name)
	}
	if client.config.Command != "/usr/bin/server" {
		t.Fatalf("expected command %q, got %q", "/usr/bin/server", client.config.Command)
	}
	if client.config.Timeout != 30*time.Second {
		t.Fatalf("expected timeout %v, got %v", 30*time.Second, client.config.Timeout)
	}
}

// =============================================================================
// MCPClient Error Handling (Not Connected)
// =============================================================================

func TestMCPClient_ListTools_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error when listing tools on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestMCPClient_CallTool_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.CallTool(context.Background(), "some-tool", map[string]any{"arg": "value"})
	if err == nil {
		t.Fatal("expected error when calling tool on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestMCPClient_ListResources_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.ListResources(context.Background())
	if err == nil {
		t.Fatal("expected error when listing resources on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestMCPClient_ListResourceTemplates_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.ListResourceTemplates(context.Background())
	if err == nil {
		t.Fatal("expected error when listing resource templates on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestMCPClient_ListPrompts_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.ListPrompts(context.Background())
	if err == nil {
		t.Fatal("expected error when listing prompts on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestMCPClient_GetPrompt_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.GetPrompt(context.Background(), "prompt-name", nil)
	if err == nil {
		t.Fatal("expected error when getting prompt on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestMCPClient_ReadResource_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.ReadResource(context.Background(), "file:///test.txt")
	if err == nil {
		t.Fatal("expected error when reading resource on disconnected client")
		return
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

// =============================================================================
// MCPClient Transport Build Errors
// =============================================================================

func TestMCPClient_Connect_StdioNoCommand(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name: "no-cmd",
		Type: "stdio",
	})

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when connecting with no command")
		return
	}
	if !strings.Contains(err.Error(), "requires a command") {
		t.Fatalf("expected 'requires a command' error, got: %v", err)
	}
	if client.Status() != StatusFailed {
		t.Fatalf("expected status %q after failure, got %q", StatusFailed, client.Status())
	}
}

func TestMCPClient_Connect_HTTPNoURL(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name: "no-url",
		Type: "http",
	})

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when connecting with http type but no URL")
		return
	}
	if !strings.Contains(err.Error(), "requires a URL") {
		t.Fatalf("expected 'requires a URL' error, got: %v", err)
	}
	if client.Status() != StatusFailed {
		t.Fatalf("expected status %q after failure, got %q", StatusFailed, client.Status())
	}
}

func TestMCPClient_Connect_SSENoURL(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name: "no-url",
		Type: "sse",
	})

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when connecting with sse type but no URL")
		return
	}
	if !strings.Contains(err.Error(), "requires a URL") {
		t.Fatalf("expected 'requires a URL' error, got: %v", err)
	}
}

func TestMCPClient_Connect_UnsupportedTransport(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name: "bad-transport",
		Type: "websocket",
	})

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when connecting with unsupported transport type")
		return
	}
	if !strings.Contains(err.Error(), "unsupported transport type") {
		t.Fatalf("expected 'unsupported transport type' error, got: %v", err)
	}
}

// =============================================================================
// MCPClient Callbacks
// =============================================================================

func TestMCPClient_SetOnClose(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	called := false
	client.SetOnClose(func() { called = true })

	// Verify callback was stored (we can't easily trigger it without a real connection)
	client.mu.Lock()
	hasCb := client.onClose != nil
	client.mu.Unlock()

	if !hasCb {
		t.Fatal("expected onClose callback to be set")
	}
	_ = called // just verify setup doesn't panic
}

func TestMCPClient_SetOnToolsChanged(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	client.SetOnToolsChanged(func() {})

	client.mu.Lock()
	hasCb := client.onToolsChanged != nil
	client.mu.Unlock()

	if !hasCb {
		t.Fatal("expected onToolsChanged callback to be set")
	}
}

func TestMCPClient_SetAPIKey(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	err := client.SetAPIKey("test-key-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	client.mu.Lock()
	key := client.config.Env["MCP_API_KEY"]
	client.mu.Unlock()

	if key != "test-key-123" {
		t.Fatalf("expected API key %q, got %q", "test-key-123", key)
	}
}

func TestMCPClient_SetAPIKey_NilEnv(t *testing.T) {
	// Verify it creates the env map if nil
	client := NewMCPClient(ServerConfig{Name: "test"})
	client.config.Env = nil

	err := client.SetAPIKey("key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if client.config.Env == nil {
		t.Fatal("expected env map to be created")
		return
	}
	if client.config.Env["MCP_API_KEY"] != "key" {
		t.Fatalf("expected key in env, got %v", client.config.Env)
	}
}

func TestMCPClient_InitiateOAuth_NotImplemented(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	_, err := client.InitiateOAuth()
	if err == nil {
		t.Fatal("expected error from unimplemented OAuth")
		return
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("expected 'not yet implemented' error, got: %v", err)
	}
}

func TestMCPClient_SupportsToolsListChanged(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	if !client.SupportsToolsListChanged() {
		t.Fatal("expected SupportsToolsListChanged to return true")
	}
}

func TestMCPClient_Disconnect_WhenNotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	// Disconnect on a not-connected client should succeed without error
	err := client.Disconnect()
	if err != nil {
		t.Fatalf("unexpected error disconnecting idle client: %v", err)
		return
	}
	if client.Status() != StatusDisconnected {
		t.Fatalf("expected %q status, got %q", StatusDisconnected, client.Status())
	}
}

func TestMCPClient_Connect_AlreadyConnected(t *testing.T) {
	// Simulate already connected state
	client := NewMCPClient(ServerConfig{Name: "test"})
	client.mu.Lock()
	client.connected = true
	client.status = StatusConnected
	client.mu.Unlock()

	// Should return nil immediately without re-connecting
	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("expected nil error for already-connected client, got: %v", err)
		return
	}
}

// =============================================================================
// MCPClient Integration with SDK (in-memory transport)
// =============================================================================

func TestMCPClient_Integration_ListToolsAndCallTool(t *testing.T) {
	characterizeMCPClientIdentityIndependentTools(t)
}

func characterizeMCPClientIdentityIndependentTools(t *testing.T) {
	// Set up an MCP server with a test tool using in-memory transports.
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "identity-independent-peer",
		Version: "1.0.0",
	}, nil)

	server.AddTool(&sdkmcp.Tool{
		Name:        "echo",
		Description: "Echoes input text",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
		},
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var args map[string]any
		if req.Params.Arguments != nil {
			_ = json.Unmarshal(req.Params.Arguments, &args)
		}
		text, _ := args["text"].(string)
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "echo: " + text},
			},
		}, nil
	})

	server.AddTool(&sdkmcp.Tool{
		Name:        "fail",
		Description: "Always fails",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "something went wrong"},
			},
		}, nil
	})

	// Create in-memory transports
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()

	// Connect server
	_, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect server: %v", err)
		return
	}

	// Manually connect the MCPClient by wiring in the in-memory transport
	mcpClient := NewMCPClient(ServerConfig{Name: "test-server", Timeout: 5 * time.Second})

	// Use the SDK client directly with our in-memory transport
	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "identity-independent-client",
		Version: "1.0.0",
	}, nil)

	session, err := sdkClient.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect client: %v", err)
		return
	}
	defer session.Close() //nolint:errcheck

	// Inject the session into our MCPClient
	mcpClient.mu.Lock()
	mcpClient.client = sdkClient
	mcpClient.session = session
	mcpClient.connected = true
	mcpClient.status = StatusConnected
	mcpClient.mu.Unlock()

	// Test ListTools
	t.Run("ListTools", func(t *testing.T) {
		tools, err := mcpClient.ListTools(context.Background())
		if err != nil {
			t.Fatalf("ListTools failed: %v", err)
			return
		}
		if len(tools) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(tools))
		}

		// Check tool names
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.Name] = true
			if tool.ServerName != "test-server" {
				t.Fatalf("expected server name %q, got %q", "test-server", tool.ServerName)
			}
		}
		if !names["echo"] {
			t.Fatal("expected 'echo' tool in list")
		}
		if !names["fail"] {
			t.Fatal("expected 'fail' tool in list")
		}
	})

	// Test CallTool - success case
	t.Run("CallTool_Success", func(t *testing.T) {
		result, err := mcpClient.CallTool(context.Background(), "echo", map[string]any{"text": "hello world"})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
			return
		}
		if result.IsError {
			t.Fatal("expected success result, got error")
		}
		if len(result.Content) == 0 {
			t.Fatal("expected non-empty content")
		}
		if result.Content[0].Text != "echo: hello world" {
			t.Fatalf("expected %q, got %q", "echo: hello world", result.Content[0].Text)
		}
	})

	// Test CallTool - tool reports error via IsError
	t.Run("CallTool_ToolError", func(t *testing.T) {
		result, err := mcpClient.CallTool(context.Background(), "fail", map[string]any{})
		if err != nil {
			t.Fatalf("CallTool failed unexpectedly: %v", err)
			return
		}
		if !result.IsError {
			t.Fatal("expected IsError to be true")
		}
		if len(result.Content) == 0 {
			t.Fatal("expected error content")
		}
		if !strings.Contains(result.Content[0].Text, "something went wrong") {
			t.Fatalf("expected error message, got %q", result.Content[0].Text)
		}
	})
}

func TestMCPClient_Integration_ListResources(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "resource-server",
		Version: "1.0.0",
	}, nil)

	server.AddResource(&sdkmcp.Resource{
		URI:         "file:///test.txt",
		Name:        "test file",
		Description: "A test file",
		MIMEType:    "text/plain",
	}, func(ctx context.Context, req *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{
			Contents: []*sdkmcp.ResourceContents{
				{URI: req.Params.URI, MIMEType: "text/plain", Text: "file content here"},
			},
		}, nil
	})

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()

	_, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect server: %v", err)
		return
	}

	mcpClient := NewMCPClient(ServerConfig{Name: "resource-server"})
	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := sdkClient.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect client: %v", err)
		return
	}
	defer session.Close() //nolint:errcheck

	mcpClient.mu.Lock()
	mcpClient.client = sdkClient
	mcpClient.session = session
	mcpClient.connected = true
	mcpClient.status = StatusConnected
	mcpClient.mu.Unlock()

	// Test ListResources
	resources, err := mcpClient.ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
		return
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].URI != "file:///test.txt" {
		t.Fatalf("expected URI %q, got %q", "file:///test.txt", resources[0].URI)
	}
	if resources[0].Name != "test file" {
		t.Fatalf("expected name %q, got %q", "test file", resources[0].Name)
	}
	if resources[0].ServerName != "resource-server" {
		t.Fatalf("expected server name %q, got %q", "resource-server", resources[0].ServerName)
	}

	// Test ReadResource
	contents, err := mcpClient.ReadResource(context.Background(), "file:///test.txt")
	if err != nil {
		t.Fatalf("ReadResource failed: %v", err)
		return
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(contents))
	}
	if contents[0].Text != "file content here" {
		t.Fatalf("expected text %q, got %q", "file content here", contents[0].Text)
	}
}

func TestMCPClient_Integration_ListPrompts(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "prompt-server",
		Version: "1.0.0",
	}, nil)

	server.AddPrompt(&sdkmcp.Prompt{
		Name:        "greeting",
		Description: "Generates a greeting",
		Arguments: []*sdkmcp.PromptArgument{
			{Name: "name", Description: "Person to greet", Required: true},
		},
	}, func(ctx context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
		name := req.Params.Arguments["name"]
		return &sdkmcp.GetPromptResult{
			Description: "A greeting prompt",
			Messages: []*sdkmcp.PromptMessage{
				{Role: "user", Content: &sdkmcp.TextContent{Text: "Hello, " + name + "!"}},
			},
		}, nil
	})

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect server: %v", err)
		return
	}

	mcpClient := NewMCPClient(ServerConfig{Name: "prompt-server"})
	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := sdkClient.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect client: %v", err)
		return
	}
	defer session.Close() //nolint:errcheck

	mcpClient.mu.Lock()
	mcpClient.client = sdkClient
	mcpClient.session = session
	mcpClient.connected = true
	mcpClient.status = StatusConnected
	mcpClient.mu.Unlock()

	// Test ListPrompts
	prompts, err := mcpClient.ListPrompts(context.Background())
	if err != nil {
		t.Fatalf("ListPrompts failed: %v", err)
		return
	}
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	if prompts[0].Name != "greeting" {
		t.Fatalf("expected prompt name %q, got %q", "greeting", prompts[0].Name)
	}
	if len(prompts[0].Arguments) != 1 {
		t.Fatalf("expected 1 argument, got %d", len(prompts[0].Arguments))
	}
	if prompts[0].Arguments[0].Name != "name" {
		t.Fatalf("expected argument name %q, got %q", "name", prompts[0].Arguments[0].Name)
	}
	if !prompts[0].Arguments[0].Required {
		t.Fatal("expected argument to be required")
	}

	// Test GetPrompt
	promptResult, err := mcpClient.GetPrompt(context.Background(), "greeting", map[string]string{"name": "World"})
	if err != nil {
		t.Fatalf("GetPrompt failed: %v", err)
		return
	}
	if promptResult.Description != "A greeting prompt" {
		t.Fatalf("expected description %q, got %q", "A greeting prompt", promptResult.Description)
	}
	if len(promptResult.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(promptResult.Messages))
	}
	if promptResult.Messages[0].Content != "Hello, World!" {
		t.Fatalf("expected content %q, got %q", "Hello, World!", promptResult.Messages[0].Content)
	}
}

// =============================================================================
// TruncateToolResult Tests
// =============================================================================

func TestTruncateToolResult_NilInput(t *testing.T) {
	result := TruncateToolResult(nil)
	if result != nil {
		t.Fatal("expected nil result for nil input")
		return
	}
}

func TestTruncateToolResult_EmptyContent(t *testing.T) {
	result := TruncateToolResult(&ToolResult{Content: []ContentBlock{}})
	if len(result.Content) != 0 {
		t.Fatal("expected empty content for empty input")
	}
}

func TestTruncateToolResult_WithinLimit(t *testing.T) {
	result := TruncateToolResult(&ToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: "short text"},
		},
	})
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "short text" {
		t.Fatalf("expected text preserved, got %q", result.Content[0].Text)
	}
}

func TestTruncateToolResult_ExceedsLimit(t *testing.T) {
	// Set a low token limit via env
	_ = os.Setenv("MAX_MCP_OUTPUT_TOKENS", "10")
	defer os.Unsetenv("MAX_MCP_OUTPUT_TOKENS") //nolint:errcheck

	// 10 tokens * 4 bytes = 40 chars max
	longText := strings.Repeat("x", 100)
	result := TruncateToolResult(&ToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: longText},
		},
	})

	// Should have the truncated text + truncation message
	if len(result.Content) < 2 {
		t.Fatalf("expected at least 2 content blocks (truncated + notice), got %d", len(result.Content))
	}

	// First block should be truncated to 40 chars
	if len(result.Content[0].Text) != 40 {
		t.Fatalf("expected truncated text to be 40 chars, got %d", len(result.Content[0].Text))
	}

	// Last block should be truncation notice
	lastBlock := result.Content[len(result.Content)-1]
	if !strings.Contains(lastBlock.Text, "OUTPUT TRUNCATED") {
		t.Fatalf("expected truncation notice, got %q", lastBlock.Text)
	}
}

func TestTruncateToolResult_MultipleBlocks(t *testing.T) {
	_ = os.Setenv("MAX_MCP_OUTPUT_TOKENS", "10")
	defer os.Unsetenv("MAX_MCP_OUTPUT_TOKENS") //nolint:errcheck

	// 40 char budget, two blocks of 30 chars each
	result := TruncateToolResult(&ToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: strings.Repeat("a", 30)},
			{Type: "text", Text: strings.Repeat("b", 30)},
		},
	})

	// First block fits entirely (30 chars <= 40 budget)
	// Second block gets truncated to 10 chars
	if result.Content[0].Text != strings.Repeat("a", 30) {
		t.Fatal("expected first block to be fully preserved")
	}
	if len(result.Content[1].Text) != 10 {
		t.Fatalf("expected second block truncated to 10 chars, got %d", len(result.Content[1].Text))
	}
	// Truncation notice should be appended
	lastBlock := result.Content[len(result.Content)-1]
	if !strings.Contains(lastBlock.Text, "OUTPUT TRUNCATED") {
		t.Fatal("expected truncation notice at end")
	}
}

func TestTruncateToolResult_PreservesIsError(t *testing.T) {
	_ = os.Setenv("MAX_MCP_OUTPUT_TOKENS", "5")
	defer os.Unsetenv("MAX_MCP_OUTPUT_TOKENS") //nolint:errcheck

	result := TruncateToolResult(&ToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: strings.Repeat("e", 100)},
		},
		IsError: true,
	})

	if !result.IsError {
		t.Fatal("expected IsError to be preserved after truncation")
	}
}

func TestTruncateToolResult_NonTextBlocksPassThrough(t *testing.T) {
	_ = os.Setenv("MAX_MCP_OUTPUT_TOKENS", "5")
	defer os.Unsetenv("MAX_MCP_OUTPUT_TOKENS") //nolint:errcheck

	// 20 chars budget
	result := TruncateToolResult(&ToolResult{
		Content: []ContentBlock{
			{Type: "image", Text: strings.Repeat("x", 100)}, // non-text, passed through
			{Type: "text", Text: strings.Repeat("t", 30)},   // text, truncated
		},
	})

	// Image block should pass through regardless of size
	if result.Content[0].Type != "image" {
		t.Fatal("expected image block to be preserved")
	}
	if result.Content[0].Text != strings.Repeat("x", 100) {
		t.Fatal("expected image text to be preserved unchanged")
	}
}

func TestGetMaxMCPOutputChars_Default(t *testing.T) {
	_ = os.Unsetenv("MAX_MCP_OUTPUT_TOKENS")

	chars := getMaxMCPOutputChars()
	// Default: 25000 * 4 = 100000
	if chars != 100000 {
		t.Fatalf("expected 100000 default chars, got %d", chars)
	}
}

func TestGetMaxMCPOutputChars_EnvOverride(t *testing.T) {
	_ = os.Setenv("MAX_MCP_OUTPUT_TOKENS", "50000")
	defer os.Unsetenv("MAX_MCP_OUTPUT_TOKENS") //nolint:errcheck

	chars := getMaxMCPOutputChars()
	if chars != 200000 {
		t.Fatalf("expected 200000 chars (50000*4), got %d", chars)
	}
}

func TestGetMaxMCPOutputChars_InvalidEnv(t *testing.T) {
	_ = os.Setenv("MAX_MCP_OUTPUT_TOKENS", "not-a-number")
	defer os.Unsetenv("MAX_MCP_OUTPUT_TOKENS") //nolint:errcheck

	chars := getMaxMCPOutputChars()
	// Should fall back to default
	if chars != 100000 {
		t.Fatalf("expected default 100000 chars for invalid env, got %d", chars)
	}
}

// =============================================================================
// NormalizeNameForMCP Tests
// =============================================================================

func TestNormalizeNameForMCP_SimpleValid(t *testing.T) {
	result := NormalizeNameForMCP("my-tool_123")
	if result != "my-tool_123" {
		t.Fatalf("expected %q, got %q", "my-tool_123", result)
	}
}

func TestNormalizeNameForMCP_ReplacesInvalidChars(t *testing.T) {
	result := NormalizeNameForMCP("my tool@v2.0")
	if result != "my_tool_v2_0" {
		t.Fatalf("expected %q, got %q", "my_tool_v2_0", result)
	}
}

func TestNormalizeNameForMCP_ClaudeAI_CollapsesUnderscores(t *testing.T) {
	result := NormalizeNameForMCP("claude.ai my   tool")
	// "claude.ai " prefix triggers collapse: spaces become _, then collapsed
	if strings.Contains(result, "__") {
		t.Fatalf("expected collapsed underscores for claude.ai name, got %q", result)
	}
	// Leading/trailing underscores should be trimmed
	if strings.HasPrefix(result, "_") || strings.HasSuffix(result, "_") {
		t.Fatalf("expected no leading/trailing underscores, got %q", result)
	}
}

func TestNormalizeNameForMCP_Truncation(t *testing.T) {
	longName := strings.Repeat("a", 100)
	result := NormalizeNameForMCP(longName)
	if len(result) != 64 {
		t.Fatalf("expected truncation to 64 chars, got %d chars", len(result))
	}
}

func TestNormalizeNameForMCP_EmptyString(t *testing.T) {
	result := NormalizeNameForMCP("")
	if result != "" {
		t.Fatalf("expected empty string, got %q", result)
	}
}

// =============================================================================
// Config Loading Tests
// =============================================================================

func TestLoadMCPConfig_NoFiles(t *testing.T) {
	// Point to a non-existent directory
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nonexistent")

	// Override the home directory so user config is not found
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", filepath.Join(dir, "fake-home"))
	defer os.Setenv("HOME", origHome) //nolint:errcheck

	config, err := LoadMCPConfig(subdir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if config == nil {
		t.Fatal("expected non-nil config")
		return
	}
	if !config.IsEmpty() {
		t.Fatal("expected empty config")
	}
	if config.GlobalTimeout != defaultGlobalTimeout {
		t.Fatalf("expected default timeout %v, got %v", defaultGlobalTimeout, config.GlobalTimeout)
	}
}

func TestLoadMCPConfig_ProjectLevelMCPJSON(t *testing.T) {
	dir := t.TempDir()

	// Override HOME to avoid user-level config
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", filepath.Join(dir, "fake-home"))
	defer os.Setenv("HOME", origHome) //nolint:errcheck

	// Create .mcp.json in project directory
	mcpJSON := `{
		"mcpServers": {
			"my-server": {
				"command": "node",
				"args": ["server.js", "--port", "3000"],
				"env": {"NODE_ENV": "production"},
				"timeout": 120
			}
		}
	}`
	err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcpJSON), 0o644)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
		return
	}

	config, err := LoadMCPConfig(dir)
	if err != nil {
		t.Fatalf("LoadMCPConfig failed: %v", err)
		return
	}
	if config.IsEmpty() {
		t.Fatal("expected non-empty config")
	}

	srv, ok := config.Servers["my-server"]
	if !ok {
		t.Fatal("expected 'my-server' in config")
	}
	if srv.Command != "node" {
		t.Fatalf("expected command %q, got %q", "node", srv.Command)
	}
	if len(srv.Args) != 3 || srv.Args[0] != "server.js" {
		t.Fatalf("unexpected args: %v", srv.Args)
	}
	if srv.Env["NODE_ENV"] != "production" {
		t.Fatalf("expected env NODE_ENV=production, got %v", srv.Env)
	}
	if srv.Timeout != 120*time.Second {
		t.Fatalf("expected timeout 120s, got %v", srv.Timeout)
	}
	if !srv.Enabled {
		t.Fatal("expected server to be enabled by default")
	}
}

func TestLoadMCPConfig_DisabledServer(t *testing.T) {
	dir := t.TempDir()

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", filepath.Join(dir, "fake-home"))
	defer os.Setenv("HOME", origHome) //nolint:errcheck

	mcpJSON := `{
		"mcpServers": {
			"disabled-srv": {
				"command": "myserver",
				"disabled": true
			}
		}
	}`
	_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcpJSON), 0o644)

	config, err := LoadMCPConfig(dir)
	if err != nil {
		t.Fatalf("LoadMCPConfig failed: %v", err)
		return
	}

	srv, ok := config.Servers["disabled-srv"]
	if !ok {
		t.Fatal("expected 'disabled-srv' in config")
	}
	if srv.Enabled {
		t.Fatal("expected server to be disabled")
	}
}

func TestLoadMCPConfig_MergeUserAndProject(t *testing.T) {
	characterizeMCPConfigMergePrecedence(t)
}

func characterizeMCPConfigMergePrecedence(t *testing.T) {
	dir := t.TempDir()
	fakeHome := filepath.Join(dir, "home")
	_ = os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755)

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", fakeHome)
	defer os.Setenv("HOME", origHome) //nolint:errcheck

	// User-level config
	userJSON := `{
		"mcpServers": {
			"user-server": {"command": "user-cmd"},
			"shared-server": {"command": "user-version"}
		}
	}`
	_ = os.WriteFile(filepath.Join(fakeHome, ".claude", "mcp_servers.json"), []byte(userJSON), 0o644)

	// Project-level .mcp.json (overrides shared-server)
	projectJSON := `{
		"mcpServers": {
			"shared-server": {"command": "project-version"},
			"project-only": {"command": "project-cmd"}
		}
	}`
	_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(projectJSON), 0o644)

	config, err := LoadMCPConfig(dir)
	if err != nil {
		t.Fatalf("LoadMCPConfig failed: %v", err)
		return
	}

	// user-server should exist from user config
	if _, ok := config.Servers["user-server"]; !ok {
		t.Fatal("expected 'user-server' from user config")
	}

	// project-only should exist from project config
	if _, ok := config.Servers["project-only"]; !ok {
		t.Fatal("expected 'project-only' from project config")
	}

	// shared-server should be the project version (project overrides user)
	srv := config.Servers["shared-server"]
	if srv.Command != "project-version" {
		t.Fatalf("expected project override for shared-server, got command %q", srv.Command)
	}
}

func TestLoadMCPConfig_GlobalTimeout(t *testing.T) {
	dir := t.TempDir()

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", filepath.Join(dir, "fake-home"))
	defer os.Setenv("HOME", origHome) //nolint:errcheck

	mcpJSON := `{
		"mcpServers": {
			"srv": {"command": "test"}
		},
		"globalTimeout": 90
	}`
	_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcpJSON), 0o644)

	config, err := LoadMCPConfig(dir)
	if err != nil {
		t.Fatalf("LoadMCPConfig failed: %v", err)
		return
	}
	if config.GlobalTimeout != 90*time.Second {
		t.Fatalf("expected global timeout 90s, got %v", config.GlobalTimeout)
	}
	// Server without explicit timeout should inherit global
	if config.Servers["srv"].Timeout != 90*time.Second {
		t.Fatalf("expected server to inherit global timeout, got %v", config.Servers["srv"].Timeout)
	}
}

// =============================================================================
// ResolveEnvVars Tests
// =============================================================================

func TestResolveEnvVars_BraceSyntax(t *testing.T) {
	_ = os.Setenv("TEST_VAR_A", "hello")
	defer os.Unsetenv("TEST_VAR_A") //nolint:errcheck

	result := ResolveEnvVars("prefix-${TEST_VAR_A}-suffix")
	if result != "prefix-hello-suffix" {
		t.Fatalf("expected %q, got %q", "prefix-hello-suffix", result)
	}
}

func TestResolveEnvVars_DollarSyntax(t *testing.T) {
	_ = os.Setenv("MY_VAR", "world")
	defer os.Unsetenv("MY_VAR") //nolint:errcheck

	result := ResolveEnvVars("hello $MY_VAR")
	if result != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", result)
	}
}

func TestResolveEnvVars_UnsetVar(t *testing.T) {
	_ = os.Unsetenv("UNSET_VAR_XYZ")

	result := ResolveEnvVars("value=${UNSET_VAR_XYZ}")
	if result != "value=" {
		t.Fatalf("expected empty expansion, got %q", result)
	}
}

func TestResolveEnvVars_NoVars(t *testing.T) {
	result := ResolveEnvVars("no variables here")
	if result != "no variables here" {
		t.Fatalf("expected unchanged string, got %q", result)
	}
}

func TestResolveEnvVars_MultipleVars(t *testing.T) {
	_ = os.Setenv("VAR1", "one")
	_ = os.Setenv("VAR2", "two")
	defer os.Unsetenv("VAR1") //nolint:errcheck
	defer os.Unsetenv("VAR2") //nolint:errcheck

	result := ResolveEnvVars("$VAR1 and ${VAR2}")
	if result != "one and two" {
		t.Fatalf("expected %q, got %q", "one and two", result)
	}
}

// =============================================================================
// ValidateServerName Tests
// =============================================================================

func TestValidateServerName_Valid(t *testing.T) {
	validNames := []string{"my-server", "server_1", "ABC", "a-b_c"}
	for _, name := range validNames {
		if err := ValidateServerName(name); err != nil {
			t.Fatalf("expected valid name %q, got error: %v", name, err)
			return
		}
	}
}

func TestValidateServerName_Invalid(t *testing.T) {
	invalidNames := []string{"", "has space", "has.dot", "has@symbol", "a/b"}
	for _, name := range invalidNames {
		err := ValidateServerName(name)
		if err == nil {
			t.Fatalf("expected error for invalid name %q", name)
			return
		}
	}
}

// =============================================================================
// MCPConfig Helper Methods Tests
// =============================================================================

func TestMCPConfig_EnabledServers(t *testing.T) {
	config := &MCPConfig{
		Servers: map[string]*MCPServerConfig{
			"enabled-1":  {Name: "enabled-1", Enabled: true},
			"disabled-1": {Name: "disabled-1", Enabled: false},
			"enabled-2":  {Name: "enabled-2", Enabled: true},
		},
	}

	enabled := config.EnabledServers()
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled servers, got %d", len(enabled))
	}
	if _, ok := enabled["enabled-1"]; !ok {
		t.Fatal("expected 'enabled-1' in enabled servers")
	}
	if _, ok := enabled["enabled-2"]; !ok {
		t.Fatal("expected 'enabled-2' in enabled servers")
	}
	if _, ok := enabled["disabled-1"]; ok {
		t.Fatal("did not expect 'disabled-1' in enabled servers")
	}
}

func TestMCPConfig_ServerNames(t *testing.T) {
	config := &MCPConfig{
		Servers: map[string]*MCPServerConfig{
			"alpha": {Name: "alpha"},
			"beta":  {Name: "beta"},
		},
	}

	names := config.ServerNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
}

func TestMCPConfig_IsEmpty(t *testing.T) {
	empty := &MCPConfig{Servers: map[string]*MCPServerConfig{}}
	if !empty.IsEmpty() {
		t.Fatal("expected IsEmpty() to return true for empty config")
	}

	nonEmpty := &MCPConfig{Servers: map[string]*MCPServerConfig{"s": {}}}
	if nonEmpty.IsEmpty() {
		t.Fatal("expected IsEmpty() to return false for non-empty config")
	}
}

func TestMCPConfig_String(t *testing.T) {
	config := &MCPConfig{Servers: map[string]*MCPServerConfig{
		"srv": {Name: "srv", Enabled: true},
	}}
	s := config.String()
	if !strings.Contains(s, "srv") {
		t.Fatalf("expected config string to contain server name, got %q", s)
	}

	nilCfg := (*MCPConfig)(nil)
	if nilCfg.String() != "MCPConfig{servers: none}" {
		t.Fatalf("unexpected nil config string: %q", nilCfg.String())
	}
}

func TestMCPServerConfig_ToServerConfig(t *testing.T) {
	cfg := &MCPServerConfig{
		Name:    "test",
		Command: "/bin/server",
		Args:    []string{"--flag"},
		Env:     map[string]string{"KEY": "VAL"},
		CWD:     "/tmp",
		Timeout: 45 * time.Second,
		Type:    "http",
		URL:     "http://localhost:8080",
		Headers: map[string]string{"Auth": "Bearer token"},
	}

	sc := cfg.ToServerConfig()
	if sc.Name != "test" || sc.Command != "/bin/server" {
		t.Fatal("basic fields not preserved")
	}
	if sc.Timeout != 45*time.Second {
		t.Fatalf("timeout not preserved: %v", sc.Timeout)
	}
	if sc.Type != "http" || sc.URL != "http://localhost:8080" {
		t.Fatal("type/URL not preserved")
	}
	if sc.Headers["Auth"] != "Bearer token" {
		t.Fatal("headers not preserved")
	}
}

// =============================================================================
// AddServerToProjectConfig / RemoveServerFromProjectConfig Tests
// =============================================================================

func TestAddServerToProjectConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	err := AddServerToProjectConfig(dir, "new-server", "my-binary", []string{"--port", "9090"}, map[string]string{"KEY": "val"})
	if err != nil {
		t.Fatalf("AddServerToProjectConfig failed: %v", err)
		return
	}

	// Verify the file was created
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("failed to read created config: %v", err)
		return
	}

	var cfg mcpConfigFileJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse config: %v", err)
		return
	}

	srv, ok := cfg.MCPServers["new-server"]
	if !ok {
		t.Fatal("expected 'new-server' in config file")
	}
	if srv.Command != "my-binary" {
		t.Fatalf("expected command %q, got %q", "my-binary", srv.Command)
	}
}

func TestAddServerToProjectConfig_DuplicateError(t *testing.T) {
	dir := t.TempDir()

	// Add first
	err := AddServerToProjectConfig(dir, "srv", "cmd", nil, nil)
	if err != nil {
		t.Fatalf("first add failed: %v", err)
		return
	}

	// Add duplicate should fail
	err = AddServerToProjectConfig(dir, "srv", "cmd2", nil, nil)
	if err == nil {
		t.Fatal("expected error for duplicate server")
		return
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got: %v", err)
	}
}

func TestAddServerToProjectConfig_InvalidName(t *testing.T) {
	dir := t.TempDir()

	err := AddServerToProjectConfig(dir, "invalid name!", "cmd", nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid server name")
		return
	}
}

func TestRemoveServerFromProjectConfig(t *testing.T) {
	dir := t.TempDir()

	// Add a server first
	_ = AddServerToProjectConfig(dir, "to-remove", "cmd", nil, nil)

	// Remove it
	err := RemoveServerFromProjectConfig(dir, "to-remove")
	if err != nil {
		t.Fatalf("RemoveServerFromProjectConfig failed: %v", err)
		return
	}

	// Verify it's gone
	data, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	var cfg mcpConfigFileJSON
	_ = json.Unmarshal(data, &cfg)
	if _, ok := cfg.MCPServers["to-remove"]; ok {
		t.Fatal("expected server to be removed")
	}
}

func TestRemoveServerFromProjectConfig_NotFound(t *testing.T) {
	dir := t.TempDir()

	err := RemoveServerFromProjectConfig(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent server")
		return
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

// =============================================================================
// ValidateConfig Tests
// =============================================================================

func TestValidateConfig_NilConfig(t *testing.T) {
	warnings := ValidateConfig(nil)
	if warnings != nil {
		t.Fatalf("expected nil warnings for nil config, got %v", warnings)
		return
	}
}

func TestValidateConfig_EmptyCommand(t *testing.T) {
	config := &MCPConfig{
		Servers: map[string]*MCPServerConfig{
			"empty": {Name: "empty", Command: "", Enabled: true},
		},
	}

	warnings := ValidateConfig(config)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "command is empty") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'command is empty' warning, got %v", warnings)
	}
}

func TestValidateConfig_DisabledServer(t *testing.T) {
	config := &MCPConfig{
		Servers: map[string]*MCPServerConfig{
			"off": {Name: "off", Command: "fake-cmd", Enabled: false},
		},
	}

	warnings := ValidateConfig(config)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "disabled") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'disabled' warning, got %v", warnings)
	}
}

// =============================================================================
// findMCPJSONFile Tests
// =============================================================================

func TestFindMCPJSONFile_InCurrentDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644)

	found := findMCPJSONFile(dir)
	if found != filepath.Join(dir, ".mcp.json") {
		t.Fatalf("expected to find .mcp.json in %s, got %q", dir, found)
	}
}

func TestFindMCPJSONFile_InParentDir(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "subdir")
	_ = os.MkdirAll(child, 0o755)
	_ = os.WriteFile(filepath.Join(parent, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644)

	found := findMCPJSONFile(child)
	if found != filepath.Join(parent, ".mcp.json") {
		t.Fatalf("expected to find .mcp.json in parent, got %q", found)
	}
}

func TestFindMCPJSONFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "deep", "nested")
	_ = os.MkdirAll(child, 0o755)

	found := findMCPJSONFile(child)
	if found != "" {
		t.Fatalf("expected empty string when not found, got %q", found)
	}
}

// =============================================================================
// HTTP Header Transport Tests
// =============================================================================

func TestHTTPClientWithHeaders(t *testing.T) {
	headers := map[string]string{
		"Authorization": "Bearer token123",
		"X-Custom":      "value",
	}

	client := httpClientWithHeaders(headers)
	if client == nil {
		t.Fatal("expected non-nil HTTP client")
		return
	}

	ht, ok := client.Transport.(*headerTransport)
	if !ok {
		t.Fatal("expected headerTransport type")
	}
	if ht.headers["Authorization"] != "Bearer token123" {
		t.Fatal("expected Authorization header to be set")
	}
}

// =============================================================================
// Timeout Helper Tests
// =============================================================================

func TestGetToolTimeout_Default(t *testing.T) {
	_ = os.Unsetenv("MCP_TOOL_TIMEOUT")

	timeout := getToolTimeout()
	if timeout != defaultToolTimeout {
		t.Fatalf("expected default timeout %v, got %v", defaultToolTimeout, timeout)
	}
}

func TestGetToolTimeout_EnvOverride(t *testing.T) {
	_ = os.Setenv("MCP_TOOL_TIMEOUT", "120")
	defer os.Unsetenv("MCP_TOOL_TIMEOUT") //nolint:errcheck

	timeout := getToolTimeout()
	if timeout != 120*time.Second {
		t.Fatalf("expected 120s timeout, got %v", timeout)
	}
}

func TestGetToolTimeout_InvalidEnv(t *testing.T) {
	_ = os.Setenv("MCP_TOOL_TIMEOUT", "invalid")
	defer os.Unsetenv("MCP_TOOL_TIMEOUT") //nolint:errcheck

	timeout := getToolTimeout()
	if timeout != defaultToolTimeout {
		t.Fatalf("expected default timeout for invalid env, got %v", timeout)
	}
}

// =============================================================================
// sdkToolToMCPTool Conversion Tests
// =============================================================================

func TestSdkToolToMCPTool_BasicConversion(t *testing.T) {
	sdkTool := &sdkmcp.Tool{
		Name:        "test-tool",
		Description: "A test tool",
	}

	result := sdkToolToMCPTool(sdkTool, "my-server")
	if result.Name != "test-tool" {
		t.Fatalf("expected name %q, got %q", "test-tool", result.Name)
	}
	if result.Description != "A test tool" {
		t.Fatalf("expected description %q, got %q", "A test tool", result.Description)
	}
	if result.ServerName != "my-server" {
		t.Fatalf("expected server name %q, got %q", "my-server", result.ServerName)
	}
}

func TestSdkToolToMCPTool_WithAnnotations(t *testing.T) {
	destructive := true
	openWorld := true
	sdkTool := &sdkmcp.Tool{
		Name:        "rm-tool",
		Description: "Removes files",
		Annotations: &sdkmcp.ToolAnnotations{
			Title:           "Remove Tool",
			ReadOnlyHint:    false,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}

	result := sdkToolToMCPTool(sdkTool, "srv")
	if result.Annotations.Title != "Remove Tool" {
		t.Fatalf("expected title %q, got %q", "Remove Tool", result.Annotations.Title)
	}
	if result.Annotations.ReadOnlyHint {
		t.Fatal("expected ReadOnlyHint to be false")
	}
	if !result.Annotations.DestructiveHint {
		t.Fatal("expected DestructiveHint to be true")
	}
	if !result.Annotations.OpenWorldHint {
		t.Fatal("expected OpenWorldHint to be true")
	}
}

// =============================================================================
// sdkResultToToolResult Conversion Tests
// =============================================================================

func TestSdkResultToToolResult_TextContent(t *testing.T) {
	sdkResult := &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "hello"},
			&sdkmcp.TextContent{Text: "world"},
		},
	}

	result := sdkResultToToolResult(sdkResult)
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" || result.Content[0].Text != "hello" {
		t.Fatalf("unexpected first block: %+v", result.Content[0])
	}
	if result.Content[1].Type != "text" || result.Content[1].Text != "world" {
		t.Fatalf("unexpected second block: %+v", result.Content[1])
	}
	if result.IsError {
		t.Fatal("expected IsError to be false")
	}
}

func TestSdkResultToToolResult_WithError(t *testing.T) {
	sdkResult := &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "error occurred"},
		},
	}

	result := sdkResultToToolResult(sdkResult)
	if !result.IsError {
		t.Fatal("expected IsError to be true")
	}
}

func TestSdkResultToToolResult_ImageContent(t *testing.T) {
	sdkResult := &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.ImageContent{Data: []byte("base64data"), MIMEType: "image/png"},
		},
	}

	result := sdkResultToToolResult(sdkResult)
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Type != "image" {
		t.Fatalf("expected type 'image', got %q", result.Content[0].Type)
	}
}
