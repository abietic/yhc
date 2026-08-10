package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cloudwego/eino/schema"
)

const mcpServerIdentityHelperEnv = "YHC_MCP_SERVER_IDENTITY_HELPER"

func TestMCPServerInitializeDeclaresYHC(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.Command(
		os.Args[0],
		"-test.run=^TestMCPServerInitializeIdentityHelperProcess$",
		"--",
	)
	cmd.Env = append(
		os.Environ(),
		mcpServerIdentityHelperEnv+"=1",
		"MCP_PERMISSION_MODE=open",
	)
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "identity-probe", Version: "1"},
		nil,
	)
	session, err := client.Connect(
		ctx,
		&sdkmcp.CommandTransport{Command: cmd},
		nil,
	)
	if err != nil {
		t.Fatalf("connect to production MCP server: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP server identity probe: %v", err)
		}
	})

	initialize := session.InitializeResult()
	if initialize == nil || initialize.ServerInfo == nil {
		t.Fatalf("initialize result = %#v", initialize)
	}
	if got := initialize.ServerInfo.Name; got != identity.CommandName {
		t.Fatalf("MCP server initialize name = %q, want %q", got, identity.CommandName)
	}
}

func TestMCPServerInitializeIdentityHelperProcess(*testing.T) {
	if os.Getenv(mcpServerIdentityHelperEnv) != "1" {
		return
	}
	if err := Serve(context.Background(), Config{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDefaultMCPToolHookLogsMetadataWithoutPayloads(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	hook := DefaultMCPToolHook{}
	hook.PreToolCall(context.Background(), "Bash", `{"api_key":"sk-secret","command":"cat private"}`)
	hook.PostToolCall(context.Background(), "Bash", 42, time.Second, errors.New("secret result"))

	logs := output.String()
	for _, forbidden := range []string{"sk-secret", "cat private", "secret result"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("default hook leaked %q: %q", forbidden, logs)
		}
	}
	for _, required := range []string{"pre-tool: Bash argument_bytes=", "post-tool: Bash outcome=error"} {
		if !strings.Contains(logs, required) {
			t.Fatalf("default hook logs missing %q: %q", required, logs)
		}
	}
}

func TestStandaloneMCPExposesExactExplicitOwnerAllowlist(t *testing.T) {
	characterizeStandaloneMCPAllowlist(t)
}

func characterizeStandaloneMCPAllowlist(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)

	exposed := make(map[string]struct{})
	for _, info := range registry.List() {
		impl, ok := registry.Get(info.Name)
		if ok && standaloneMCPToolExposable(impl) {
			exposed[info.Name] = struct{}{}
		}
	}
	if len(exposed) != len(standaloneMCPToolAllowlist) {
		t.Fatalf(
			"standalone MCP exposed %d tools, want exact allowlist %d: %v",
			len(exposed),
			len(standaloneMCPToolAllowlist),
			exposed,
		)
	}
	for name := range standaloneMCPToolAllowlist {
		impl, ok := registry.Get(name)
		if !ok {
			t.Fatalf("default registry missing %s", name)
		}
		if !standaloneMCPToolExposable(impl) {
			t.Fatalf("standalone MCP excluded allowlisted %s", name)
		}
		if _, ok := exposed[name]; !ok {
			t.Fatalf("standalone MCP registration omitted %s", name)
		}
	}
	for _, name := range []string{
		"Read",
		"Bash",
		"BashOutput",
		"KillShell",
		"Agent",
		"EnterPlanMode",
		"ExitPlanMode",
		tools.GetGoalToolName,
		tools.UpdateGoalToolName,
	} {
		impl, ok := registry.Get(name)
		if !ok {
			t.Fatalf("default registry missing %s", name)
		}
		if standaloneMCPToolExposable(impl) {
			t.Fatalf("standalone MCP exposed non-allowlisted %s", name)
		}
	}
}

func TestStandaloneMCPRuntimeIsFreshPerServeInvocation(t *testing.T) {
	characterizeStandaloneMCPRuntimeIsolation(t)
}

func characterizeStandaloneMCPRuntimeIsolation(t *testing.T) {
	left := newStandaloneMCPRuntime()
	right := newStandaloneMCPRuntime()
	if left == right ||
		left.todoAuthority == right.todoAuthority ||
		left.taskManager == right.taskManager ||
		left.logicalWorkScope == right.logicalWorkScope {
		t.Fatal("standalone MCP invocations share runtime ownership")
	}
	if _, err := tools.TodoWriteTool().ExecuteCtx(
		left.bind(context.Background()),
		`{"todos":[{"content":"left","status":"pending","activeForm":"writing left"}]}`,
	); err != nil {
		t.Fatal(err)
	}
	leftItems, err := left.todoAuthority.Todos(tools.TodoScope{
		SessionID: left.logicalWorkScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leftItems) != 1 || leftItems[0].Content != "left" {
		t.Fatalf("left Todo state = %#v", leftItems)
	}
	rightItems, err := right.todoAuthority.Todos(tools.TodoScope{
		SessionID: right.logicalWorkScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rightItems != nil {
		t.Fatalf("left Todo state leaked right: %#v", rightItems)
	}
	if _, err := tools.TaskCreateTool().ExecuteCtx(
		left.bind(context.Background()),
		`{"subject":"left task","description":"left only"}`,
	); err != nil {
		t.Fatal(err)
	}
	if len(left.taskManager.List()) != 1 ||
		len(right.taskManager.List()) != 0 {
		t.Fatalf(
			"standalone Task state leaked: left=%#v right=%#v",
			left.taskManager.List(),
			right.taskManager.List(),
		)
	}
}

func TestYHCProtocolMigrationCharacterizesIdentityIndependentBehavior(t *testing.T) {
	t.Run("exact explicit allowlist", characterizeStandaloneMCPAllowlist)
	t.Run("fresh runtime per serve", characterizeStandaloneMCPRuntimeIsolation)
}

// =============================================================================
// executeTool Tests
// =============================================================================

func TestExecuteTool_WithExecuteFunc(t *testing.T) {
	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "test-echo",
			Desc: "Echoes input",
		},
		Execute: func(input string) (string, error) {
			var args map[string]string
			_ = json.Unmarshal([]byte(input), &args)
			return "echo: " + args["text"], nil
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      "test-echo",
			Arguments: json.RawMessage(`{"text":"hello"}`),
		},
	}

	result, err := executeTool(context.Background(), impl, "TestTool", standalonePermissionOpen, DefaultMCPToolHook{}, req)
	if err != nil {
		t.Fatalf("executeTool returned error: %v", err)
		return
	}
	if result.IsError {
		t.Fatal("expected successful result")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}

	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if tc.Text != "echo: hello" {
		t.Fatalf("expected %q, got %q", "echo: hello", tc.Text)
	}
}

func TestExecuteTool_WithExecuteCtxFunc(t *testing.T) {
	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ctx-tool",
			Desc: "Uses context",
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return "ctx result: " + input, nil
		},
		// Also set Execute to verify ExecuteCtx takes precedence
		Execute: func(input string) (string, error) {
			return "wrong function called", nil
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      "ctx-tool",
			Arguments: json.RawMessage(`{"key":"value"}`),
		},
	}

	result, err := executeTool(context.Background(), impl, "TestTool", standalonePermissionOpen, DefaultMCPToolHook{}, req)
	if err != nil {
		t.Fatalf("executeTool returned error: %v", err)
		return
	}
	if result.IsError {
		t.Fatal("expected successful result")
	}

	tc := result.Content[0].(*sdkmcp.TextContent)
	if !strings.HasPrefix(tc.Text, "ctx result:") {
		t.Fatalf("expected ExecuteCtx to take precedence, got %q", tc.Text)
	}
}

func TestExecuteTool_NoExecuteFunction(t *testing.T) {
	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "no-exec",
			Desc: "Has no execute function",
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name: "no-exec",
		},
	}

	result, err := executeTool(context.Background(), impl, "TestTool", standalonePermissionOpen, DefaultMCPToolHook{}, req)
	if err != nil {
		t.Fatalf("executeTool returned unexpected error: %v", err)
		return
	}
	if !result.IsError {
		t.Fatal("expected IsError for tool without execute function")
	}

	tc := result.Content[0].(*sdkmcp.TextContent)
	if !strings.Contains(tc.Text, "no execute function") {
		t.Fatalf("expected 'no execute function' message, got %q", tc.Text)
	}
}

func TestExecuteTool_ExecuteReturnsError(t *testing.T) {
	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "err-tool",
			Desc: "Returns error",
		},
		Execute: func(input string) (string, error) {
			return "", fmt.Errorf("tool execution failed")
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      "err-tool",
			Arguments: json.RawMessage(`{}`),
		},
	}

	result, err := executeTool(context.Background(), impl, "TestTool", standalonePermissionOpen, DefaultMCPToolHook{}, req)
	if err != nil {
		t.Fatalf("executeTool should not return error from tool failures: %v", err)
		return
	}
	if !result.IsError {
		t.Fatal("expected IsError when tool returns error")
	}
}

func TestExecuteTool_NilArguments(t *testing.T) {
	var receivedInput string
	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "nil-args",
			Desc: "Handles nil args",
		},
		Execute: func(input string) (string, error) {
			receivedInput = input
			return "ok", nil
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      "nil-args",
			Arguments: nil,
		},
	}

	result, err := executeTool(context.Background(), impl, "TestTool", standalonePermissionOpen, DefaultMCPToolHook{}, req)
	if err != nil {
		t.Fatalf("executeTool returned error: %v", err)
		return
	}
	if result.IsError {
		t.Fatal("expected successful result")
	}
	// When arguments are nil, should pass "{}" as input
	if receivedInput != "{}" {
		t.Fatalf("expected input %q for nil arguments, got %q", "{}", receivedInput)
	}
}

// =============================================================================
// Integration Test: Full MCP Server with In-Memory Transport
// =============================================================================

func TestServe_Integration_ToolRegistrationAndCalling(t *testing.T) {
	// Create a custom registry with controlled tools for testing.
	reg := tools.NewRegistry()

	// Register a simple echo tool
	reg.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "test_echo",
			Desc: "Echoes the provided text",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"text": {
					Desc:     "Text to echo",
					Type:     schema.String,
					Required: true,
				},
			}),
		},
		Execute: func(input string) (string, error) {
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal([]byte(input), &args)
			return "echo: " + args.Text, nil
		},
	})

	// Register a read-only tool
	reg.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "test_read",
			Desc: "Reads a value",
		},
		IsReadOnly: true,
		Execute: func(input string) (string, error) {
			return "read result", nil
		},
	})

	// Register a hidden tool (should not appear in MCP)
	reg.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "test_hidden",
			Desc: "Should not be exposed",
		},
		IsHidden: true,
		Execute: func(input string) (string, error) {
			return "hidden", nil
		},
	})

	// Register a destructive tool
	reg.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "test_delete",
			Desc: "Deletes something",
		},
		IsDestructive: true,
		Execute: func(input string) (string, error) {
			return "deleted", nil
		},
	})

	// Build the MCP server manually (replicating Serve logic without stdio)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "eino-agent",
		Version: "1.0.0",
	}, nil)

	for _, info := range reg.List() {
		impl, ok := reg.Get(info.Name)
		if !ok || impl.IsHidden {
			continue
		}

		tool := &sdkmcp.Tool{
			Name:        info.Name,
			Description: info.Desc,
		}

		if info.ParamsOneOf != nil {
			jsonSchema, err := info.ToJSONSchema()
			if err == nil && jsonSchema != nil {
				data, err := json.Marshal(jsonSchema)
				if err == nil {
					var schemaMap map[string]any
					if json.Unmarshal(data, &schemaMap) == nil {
						tool.InputSchema = schemaMap
					}
				}
			}
		}

		// SDK requires an InputSchema; provide minimal object schema if none set.
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		if impl.IsReadOnly || impl.IsDestructive {
			tool.Annotations = &sdkmcp.ToolAnnotations{
				ReadOnlyHint: impl.IsReadOnly,
			}
			if impl.IsDestructive {
				destructive := true
				tool.Annotations.DestructiveHint = &destructive
			}
		}

		toolImpl := impl
		server.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return executeTool(ctx, toolImpl, "TestTool", standalonePermissionOpen, DefaultMCPToolHook{}, req)
		})
	}

	// Connect with in-memory transport
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()

	_, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect failed: %v", err)
		return
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "test-client",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
		return
	}
	defer session.Close() //nolint:errcheck

	// Test: List tools — hidden tool should not appear
	t.Run("ListTools_ExcludesHidden", func(t *testing.T) {
		var toolNames []string
		for tool, err := range session.Tools(context.Background(), &sdkmcp.ListToolsParams{}) {
			if err != nil {
				t.Fatalf("tools/list failed: %v", err)
				return
			}
			toolNames = append(toolNames, tool.Name)
		}

		// Should have echo, read, delete but NOT hidden
		nameSet := map[string]bool{}
		for _, name := range toolNames {
			nameSet[name] = true
		}

		if !nameSet["test_echo"] {
			t.Fatal("expected test_echo in tool list")
		}
		if !nameSet["test_read"] {
			t.Fatal("expected test_read in tool list")
		}
		if !nameSet["test_delete"] {
			t.Fatal("expected test_delete in tool list")
		}
		if nameSet["test_hidden"] {
			t.Fatal("test_hidden should NOT appear in tool list")
		}
	})

	// Test: Call echo tool
	t.Run("CallTool_Echo", func(t *testing.T) {
		result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name:      "test_echo",
			Arguments: map[string]any{"text": "world"},
		})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
			return
		}
		if result.IsError {
			t.Fatal("expected successful result")
		}
		if len(result.Content) == 0 {
			t.Fatal("expected non-empty content")
		}

		tc, ok := result.Content[0].(*sdkmcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %T", result.Content[0])
		}
		if tc.Text != "echo: world" {
			t.Fatalf("expected %q, got %q", "echo: world", tc.Text)
		}
	})

	// Test: Call read-only tool
	t.Run("CallTool_ReadOnly", func(t *testing.T) {
		result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name: "test_read",
		})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
			return
		}
		if result.IsError {
			t.Fatal("expected successful result")
		}
		tc := result.Content[0].(*sdkmcp.TextContent)
		if tc.Text != "read result" {
			t.Fatalf("expected %q, got %q", "read result", tc.Text)
		}
	})

	// Test: Call destructive tool
	t.Run("CallTool_Destructive", func(t *testing.T) {
		result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name: "test_delete",
		})
		if err != nil {
			t.Fatalf("CallTool failed: %v", err)
			return
		}
		if result.IsError {
			t.Fatal("expected successful result")
		}
		tc := result.Content[0].(*sdkmcp.TextContent)
		if tc.Text != "deleted" {
			t.Fatalf("expected %q, got %q", "deleted", tc.Text)
		}
	})

	// Test: Tool schema is exposed correctly
	t.Run("ToolSchema_Exposed", func(t *testing.T) {
		var echoTool *sdkmcp.Tool
		for tool, err := range session.Tools(context.Background(), &sdkmcp.ListToolsParams{}) {
			if err != nil {
				t.Fatalf("tools/list failed: %v", err)
				return
			}
			if tool.Name == "test_echo" {
				echoTool = tool
				break
			}
		}

		if echoTool == nil {
			t.Fatal("test_echo tool not found")
			return
		}
		if echoTool.Description != "Echoes the provided text" {
			t.Fatalf("unexpected description: %q", echoTool.Description)
		}
		// The input schema should have been set
		if echoTool.InputSchema == nil {
			t.Fatal("expected InputSchema to be set for test_echo")
			return
		}
	})

	// Test: Tool annotations are propagated
	t.Run("ToolAnnotations_Propagated", func(t *testing.T) {
		var readTool *sdkmcp.Tool
		var deleteTool *sdkmcp.Tool
		for tool, err := range session.Tools(context.Background(), &sdkmcp.ListToolsParams{}) {
			if err != nil {
				t.Fatalf("tools/list failed: %v", err)
				return
			}
			switch tool.Name {
			case "test_read":
				readTool = tool
			case "test_delete":
				deleteTool = tool
			}
		}

		if readTool == nil || deleteTool == nil {
			t.Fatal("expected both test_read and test_delete tools")
			return
		}

		if readTool.Annotations == nil {
			t.Fatal("expected annotations on test_read")
			return
		}
		if !readTool.Annotations.ReadOnlyHint {
			t.Fatal("expected ReadOnlyHint=true on test_read")
		}

		if deleteTool.Annotations == nil {
			t.Fatal("expected annotations on test_delete")
			return
		}
		if deleteTool.Annotations.DestructiveHint == nil || !*deleteTool.Annotations.DestructiveHint {
			t.Fatal("expected DestructiveHint=true on test_delete")
			return
		}
	})
}

// =============================================================================
// Config Tests
// =============================================================================

func TestConfig_Fields(t *testing.T) {
	cfg := Config{CWD: "/workspace"}
	if cfg.CWD != "/workspace" {
		t.Fatalf("expected CWD %q, got %q", "/workspace", cfg.CWD)
	}
}

func TestParseStandalonePermissionPolicy(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		want       standalonePermissionPolicy
		wantErr    bool
		escapedCtl string
	}{
		{name: "empty defaults open", raw: "", want: standalonePermissionOpen},
		{name: "open", raw: "open", want: standalonePermissionOpen},
		{name: "strict", raw: "strict", want: standalonePermissionStrict},
		{name: "typo", raw: "strcit", wantErr: true},
		{name: "upper case open", raw: "OPEN", wantErr: true},
		{name: "upper case", raw: "Strict", wantErr: true},
		{name: "leading space", raw: " strict", wantErr: true},
		{name: "trailing space", raw: "strict ", wantErr: true},
		{name: "newline", raw: "strict\n", wantErr: true, escapedCtl: `\n`},
		{name: "tab", raw: "strict\t", wantErr: true, escapedCtl: `\t`},
		{name: "nul", raw: "strict\x00", wantErr: true, escapedCtl: `\x00`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStandalonePermissionPolicy(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected parser error")
				}
				if !strings.Contains(err.Error(), "MCP_PERMISSION_MODE") {
					t.Fatalf("error does not name MCP_PERMISSION_MODE: %q", err)
				}
				if tt.escapedCtl != "" {
					if strings.Contains(err.Error(), tt.raw) {
						t.Fatalf("error contains unescaped control byte: %q", err)
					}
					if !strings.Contains(err.Error(), tt.escapedCtl) {
						t.Fatalf("error does not safely escape control byte %q: %q", tt.escapedCtl, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStandalonePermissionPolicy(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("policy = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServeInvalidPermissionPolicyWinsBeforeCancelledContext(t *testing.T) {
	for _, raw := range []string{"strcit", "OPEN", "Strict", " strict", "strict ", "strict\n", "strict\t"} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			t.Setenv("MCP_PERMISSION_MODE", raw)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := Serve(ctx, Config{})
			if err == nil {
				t.Fatal("Serve returned nil for invalid permission mode")
			}
			if errors.Is(err, context.Canceled) {
				t.Fatalf("Serve returned context cancellation instead of configuration error: %v", err)
			}
			if !strings.Contains(err.Error(), "MCP_PERMISSION_MODE") {
				t.Fatalf("Serve error does not name MCP_PERMISSION_MODE: %q", err)
			}
			if strings.ContainsAny(raw, "\n\t") && strings.Contains(err.Error(), raw) {
				t.Fatalf("Serve error contains unescaped control byte: %q", err)
			}
		})
	}
}

func TestServeValidPermissionPolicyPreservesCancelledContext(t *testing.T) {
	for _, raw := range []string{"", "open", "strict"} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			t.Setenv("MCP_PERMISSION_MODE", raw)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := Serve(ctx, Config{})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Serve error = %v, want context cancellation", err)
			}
		})
	}
}

func TestExecuteToolStandalonePermissionPolicy(t *testing.T) {
	invalidPolicy := standalonePermissionPolicy(99)
	tests := []struct {
		name       string
		policy     standalonePermissionPolicy
		readOnly   bool
		wantDenied bool
		strict     bool
	}{
		{name: "open read", policy: standalonePermissionOpen, readOnly: true},
		{name: "open write", policy: standalonePermissionOpen},
		{name: "strict read", policy: standalonePermissionStrict, readOnly: true},
		{name: "strict write", policy: standalonePermissionStrict, wantDenied: true, strict: true},
		{name: "invalid read", policy: invalidPolicy, readOnly: true, wantDenied: true},
		{name: "invalid write", policy: invalidPolicy, wantDenied: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var executeCalls, executeCtxCalls int
			hook := &recordingHook{}
			impl := tools.ToolImpl{
				Info:       &schema.ToolInfo{Name: "policy-test", Desc: "Tests standalone policy"},
				IsReadOnly: tt.readOnly,
				Execute: func(string) (string, error) {
					executeCalls++
					return "execute", nil
				},
				ExecuteCtx: func(context.Context, string) (string, error) {
					executeCtxCalls++
					return "execute context", nil
				},
			}
			var req *sdkmcp.CallToolRequest
			if !tt.wantDenied {
				req = &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{
					Name:      "policy-test",
					Arguments: json.RawMessage(`{"key":"value"}`),
				}}
			}

			result, err := executeTool(context.Background(), impl, "policy-test", tt.policy, hook, req)
			if err != nil {
				t.Fatalf("executeTool returned error: %v", err)
			}
			if result.IsError != tt.wantDenied {
				t.Fatalf("IsError = %v, want %v", result.IsError, tt.wantDenied)
			}

			hook.mu.Lock()
			preCalls, postCalls := len(hook.preCalls), len(hook.postCalls)
			hook.mu.Unlock()
			if tt.wantDenied {
				if preCalls != 0 || postCalls != 0 || executeCalls != 0 || executeCtxCalls != 0 {
					t.Fatalf("denied tool had side effects: pre=%d post=%d execute=%d executeCtx=%d", preCalls, postCalls, executeCalls, executeCtxCalls)
				}
				if tt.strict {
					text := result.Content[0].(*sdkmcp.TextContent).Text
					want := `tool "policy-test" is not allowed in strict permission mode (read-only tools only)`
					if text != want {
						t.Fatalf("strict denial = %q, want %q", text, want)
					}
				} else {
					text := result.Content[0].(*sdkmcp.TextContent).Text
					if !strings.Contains(text, "invalid standalone MCP permission policy") {
						t.Fatalf("invalid-policy denial = %q", text)
					}
				}
				return
			}
			if preCalls != 1 || postCalls != 1 || executeCalls != 0 || executeCtxCalls != 1 {
				t.Fatalf("allowed tool did not preserve hooks and ExecuteCtx precedence: pre=%d post=%d execute=%d executeCtx=%d", preCalls, postCalls, executeCalls, executeCtxCalls)
			}
		})
	}
}

// =============================================================================
// Tool Hook Tests
// =============================================================================

type recordingHook struct {
	mu        sync.Mutex
	preCalls  []hookPreCall
	postCalls []hookPostCall
}

type hookPreCall struct {
	ToolName  string
	Arguments string
}

type hookPostCall struct {
	ToolName  string
	ResultLen int
	Duration  time.Duration
	Err       error
}

func (h *recordingHook) PreToolCall(_ context.Context, toolName, arguments string) {
	h.mu.Lock()
	h.preCalls = append(h.preCalls, hookPreCall{ToolName: toolName, Arguments: arguments})
	h.mu.Unlock()
}

func (h *recordingHook) PostToolCall(_ context.Context, toolName string, resultLen int, duration time.Duration, err error) {
	h.mu.Lock()
	h.postCalls = append(h.postCalls, hookPostCall{ToolName: toolName, ResultLen: resultLen, Duration: duration, Err: err})
	h.mu.Unlock()
}

func TestMCPServerPrePostToolHooks(t *testing.T) {
	hook := &recordingHook{}

	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "hook-test",
			Desc: "Tests hooks",
		},
		Execute: func(input string) (string, error) {
			time.Sleep(5 * time.Millisecond)
			return "hook result", nil
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      "hook-test",
			Arguments: json.RawMessage(`{"key":"val"}`),
		},
	}

	result, err := executeTool(context.Background(), impl, "hook-test", standalonePermissionOpen, hook, req)
	if err != nil {
		t.Fatalf("executeTool error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}

	hook.mu.Lock()
	defer hook.mu.Unlock()

	if len(hook.preCalls) != 1 {
		t.Fatalf("expected 1 pre-call, got %d", len(hook.preCalls))
	}
	if hook.preCalls[0].ToolName != "hook-test" {
		t.Errorf("pre-call tool name: got %q want %q", hook.preCalls[0].ToolName, "hook-test")
	}
	if hook.preCalls[0].Arguments != `{"key":"val"}` {
		t.Errorf("pre-call arguments: got %q", hook.preCalls[0].Arguments)
	}

	if len(hook.postCalls) != 1 {
		t.Fatalf("expected 1 post-call, got %d", len(hook.postCalls))
	}
	if hook.postCalls[0].ToolName != "hook-test" {
		t.Errorf("post-call tool name: got %q want %q", hook.postCalls[0].ToolName, "hook-test")
	}
	if hook.postCalls[0].ResultLen != len("hook result") {
		t.Errorf("post-call result len: got %d want %d", hook.postCalls[0].ResultLen, len("hook result"))
	}
	if hook.postCalls[0].Duration < 5*time.Millisecond {
		t.Errorf("post-call duration too short: %v", hook.postCalls[0].Duration)
	}
	if hook.postCalls[0].Err != nil {
		t.Errorf("post-call unexpected error: %v", hook.postCalls[0].Err)
	}
}

func TestMCPServerPrePostToolHooks_Error(t *testing.T) {
	hook := &recordingHook{}

	impl := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "fail-tool",
			Desc: "Fails",
		},
		Execute: func(input string) (string, error) {
			return "", fmt.Errorf("intentional failure")
		},
	}

	req := &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      "fail-tool",
			Arguments: json.RawMessage(`{}`),
		},
	}

	result, err := executeTool(context.Background(), impl, "fail-tool", standalonePermissionOpen, hook, req)
	if err != nil {
		t.Fatalf("executeTool error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}

	hook.mu.Lock()
	defer hook.mu.Unlock()

	if len(hook.postCalls) != 1 {
		t.Fatalf("expected 1 post-call, got %d", len(hook.postCalls))
	}
	if hook.postCalls[0].Err == nil {
		t.Error("expected post-call to report error")
	}
	if hook.postCalls[0].Err.Error() != "intentional failure" {
		t.Errorf("unexpected error: %v", hook.postCalls[0].Err)
	}
}

func TestMCPServerToolAnnotationsComplete(t *testing.T) {
	reg := tools.NewRegistry()

	reg.Register(tools.ToolImpl{
		Info:             &schema.ToolInfo{Name: "perm-tool", Desc: "Needs permissions"},
		NeedsPermissions: true,
		Execute:          func(string) (string, error) { return "", nil },
	})
	reg.Register(tools.ToolImpl{
		Info:              &schema.ToolInfo{Name: "concurrent-tool", Desc: "Concurrency safe"},
		IsConcurrencySafe: func(map[string]any) bool { return true },
		Execute:           func(string) (string, error) { return "", nil },
	})
	reg.Register(tools.ToolImpl{
		Info:       &schema.ToolInfo{Name: "read-tool", Desc: "Read only"},
		IsReadOnly: true,
		Execute:    func(string) (string, error) { return "", nil },
	})
	reg.Register(tools.ToolImpl{
		Info:          &schema.ToolInfo{Name: "destroy-tool", Desc: "Destructive"},
		IsDestructive: true,
		Execute:       func(string) (string, error) { return "", nil },
	})

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	for _, info := range reg.List() {
		impl, ok := reg.Get(info.Name)
		if !ok || impl.IsHidden {
			continue
		}
		tool := &sdkmcp.Tool{
			Name:        info.Name,
			Description: info.Desc,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		}
		ann := &sdkmcp.ToolAnnotations{ReadOnlyHint: impl.IsReadOnly}
		if impl.IsDestructive {
			d := true
			ann.DestructiveHint = &d
		}
		if impl.NeedsPermissions {
			o := true
			ann.OpenWorldHint = &o
		}
		if impl.IsConcurrencySafe != nil {
			ann.IdempotentHint = impl.IsConcurrencySafe(nil)
		}
		tool.Annotations = ann

		toolImpl := impl
		server.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return executeTool(ctx, toolImpl, info.Name, standalonePermissionOpen, DefaultMCPToolHook{}, req)
		})
	}

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close() //nolint:errcheck

	toolMap := map[string]*sdkmcp.Tool{}
	for tool, err := range session.Tools(context.Background(), &sdkmcp.ListToolsParams{}) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		toolMap[tool.Name] = tool
	}

	// perm-tool: OpenWorldHint=true
	if pt := toolMap["perm-tool"]; pt == nil {
		t.Fatal("perm-tool not found")
	} else if pt.Annotations == nil || pt.Annotations.OpenWorldHint == nil || !*pt.Annotations.OpenWorldHint {
		t.Error("perm-tool: expected OpenWorldHint=true")
	}

	// concurrent-tool: IdempotentHint=true
	if ct := toolMap["concurrent-tool"]; ct == nil {
		t.Fatal("concurrent-tool not found")
	} else if ct.Annotations == nil || !ct.Annotations.IdempotentHint {
		t.Error("concurrent-tool: expected IdempotentHint=true")
	}

	// read-tool: ReadOnlyHint=true
	if rt := toolMap["read-tool"]; rt == nil {
		t.Fatal("read-tool not found")
	} else if rt.Annotations == nil || !rt.Annotations.ReadOnlyHint {
		t.Error("read-tool: expected ReadOnlyHint=true")
	}

	// destroy-tool: DestructiveHint=true
	if dt := toolMap["destroy-tool"]; dt == nil {
		t.Fatal("destroy-tool not found")
	} else if dt.Annotations == nil || dt.Annotations.DestructiveHint == nil || !*dt.Annotations.DestructiveHint {
		t.Error("destroy-tool: expected DestructiveHint=true")
	}
}
