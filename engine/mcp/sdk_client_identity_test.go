package mcp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpClientIdentityHelperEnv = "YHC_MCP_CLIENT_IDENTITY_HELPER"

func TestMCPClientInitializeDeclaresYHC(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name:    "identity-probe",
		Type:    "stdio",
		Command: os.Args[0],
		Args: []string{
			"-test.run=^TestMCPClientInitializeIdentityHelperProcess$",
			"--",
		},
		Env: map[string]string{
			mcpClientIdentityHelperEnv: "1",
		},
		CWD: t.TempDir(),
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("production MCP client Connect: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(); err != nil {
			t.Errorf("disconnect MCP client identity probe: %v", err)
		}
	})

	result, err := client.CallTool(ctx, "reported_client_name", nil)
	if err != nil {
		t.Fatalf("read MCP client initialize name: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != identity.CommandName {
		t.Fatalf("MCP client initialize name result = %#v, want %q", result, identity.CommandName)
	}
}

func TestMCPClientInitializeIdentityHelperProcess(*testing.T) {
	if os.Getenv(mcpClientIdentityHelperEnv) != "1" {
		return
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "identity-probe", Version: "1"},
		nil,
	)
	server.AddTool(
		&sdkmcp.Tool{
			Name:        "reported_client_name",
			Description: "returns the peer name from the initialize handshake",
			InputSchema: map[string]any{"type": "object"},
		},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			name := request.Session.InitializeParams().ClientInfo.Name
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: name},
			}}, nil
		},
	)
	session, err := server.Connect(
		context.Background(),
		&sdkmcp.StdioTransport{},
		nil,
	)
	if err != nil {
		os.Exit(1)
	}
	if err := session.Wait(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestYHCProtocolMigrationCharacterizesIdentityIndependentBehavior(t *testing.T) {
	t.Run("peer name does not affect tools", characterizeMCPClientIdentityIndependentTools)
	t.Run("configuration merge precedence", characterizeMCPConfigMergePrecedence)
	t.Run("transport selection", characterizeMCPTransportSelection)
}

func characterizeMCPTransportSelection(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdio, err := NewMCPClient(ServerConfig{
		Type: "stdio", Command: executable, CWD: t.TempDir(),
	}).buildTransport()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stdio.(*stdioProcessTransport); !ok {
		t.Fatalf("stdio transport = %T", stdio)
	}

	httpTransport, err := NewMCPClient(ServerConfig{
		Type: "http", URL: "https://mcp.invalid/rpc",
	}).buildTransport()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := httpTransport.(*sdkmcp.StreamableClientTransport); !ok {
		t.Fatalf("http transport = %T", httpTransport)
	}

	sse, err := NewMCPClient(ServerConfig{
		Type: "sse", URL: "https://mcp.invalid/events",
	}).buildTransport()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sse.(*sdkmcp.SSEClientTransport); !ok {
		t.Fatalf("sse transport = %T", sse)
	}

	if _, err := NewMCPClient(ServerConfig{Type: "unsupported"}).buildTransport(); err == nil {
		t.Fatal("unsupported transport was accepted")
	}
}
