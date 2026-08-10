package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/abietic/yhc/internal/buildinfo"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPToolHook provides an extension point for observing tool execution in
// the MCP server. Implementations can log, meter, or audit tool calls.
type MCPToolHook interface {
	PreToolCall(ctx context.Context, toolName, arguments string)
	PostToolCall(ctx context.Context, toolName string, resultLen int, duration time.Duration, err error)
}

// DefaultMCPToolHook logs tool calls and results to the standard logger.
type DefaultMCPToolHook struct{}

func (h DefaultMCPToolHook) PreToolCall(_ context.Context, toolName, arguments string) {
	log.Printf("[mcp] pre-tool: %s argument_bytes=%d", toolName, len(arguments))
}

func (h DefaultMCPToolHook) PostToolCall(_ context.Context, toolName string, resultLen int, duration time.Duration, err error) {
	if err != nil {
		log.Printf("[mcp] post-tool: %s outcome=error duration=%v", toolName, duration)
	} else {
		log.Printf("[mcp] post-tool: %s result_len=%d duration=%v", toolName, resultLen, duration)
	}
}

// Config holds configuration for the MCP server mode.
type Config struct {
	// CWD is the working directory for tool execution.
	CWD string
	// ToolHook is an optional hook for observing tool execution.
	// If nil, DefaultMCPToolHook is used.
	ToolHook MCPToolHook
}

type standalonePermissionPolicy uint8

type standaloneMCPRuntime struct {
	logicalWorkScope string
	todoAuthority    *tools.EphemeralTodoAuthority
	taskManager      *tools.TaskManager
}

var standaloneMCPToolAllowlist = map[string]struct{}{
	"Task":       {},
	"TaskCreate": {},
	"TaskGet":    {},
	"TaskList":   {},
	"TaskOutput": {},
	"TaskStop":   {},
	"TaskUpdate": {},
	"TodoWrite":  {},
}

func newStandaloneMCPRuntime() *standaloneMCPRuntime {
	return &standaloneMCPRuntime{
		logicalWorkScope: "standalone-mcp:" + uuid.NewString(),
		todoAuthority:    tools.NewEphemeralTodoAuthority(),
		taskManager:      tools.NewTaskManager(),
	}
}

func (r *standaloneMCPRuntime) bind(ctx context.Context) context.Context {
	if r == nil {
		return ctx
	}
	ctx = tools.WithNonSessionLogicalWorkScope(ctx, r.logicalWorkScope)
	ctx = tools.WithLogicalWorkAuthority(ctx, r.todoAuthority)
	return tools.WithTaskManager(ctx, r.taskManager)
}

const (
	_ standalonePermissionPolicy = iota
	standalonePermissionOpen
	standalonePermissionStrict
)

func parseStandalonePermissionPolicy(raw string) (standalonePermissionPolicy, error) {
	switch raw {
	case "", "open":
		return standalonePermissionOpen, nil
	case "strict":
		return standalonePermissionStrict, nil
	default:
		return 0, fmt.Errorf(
			"invalid MCP_PERMISSION_MODE value %q: expected %q or %q",
			raw,
			"open",
			"strict",
		)
	}
}

func (p standalonePermissionPolicy) allows(readOnly bool) bool {
	switch p {
	case standalonePermissionOpen:
		return true
	case standalonePermissionStrict:
		return readOnly
	default:
		return false
	}
}

func (p standalonePermissionPolicy) valid() bool {
	return p == standalonePermissionOpen || p == standalonePermissionStrict
}

// Serve starts an MCP server over stdio, exposing only the explicit-owner
// Task/Todo compatibility allowlist.
// It blocks until the context is cancelled or the client disconnects.
// Each Serve invocation owns fresh process-lifetime Task/Todo state and never
// names or writes a durable Session.
//
// Permission policy is controlled by MCP_PERMISSION_MODE env var:
//   - "open" (default): all tools are allowed
//   - "strict": only read-only tools are allowed; write tools return an error
//   - any other non-empty value: startup returns a configuration error
func Serve(ctx context.Context, cfg Config) error {
	policy, err := parseStandalonePermissionPolicy(os.Getenv("MCP_PERMISSION_MODE"))
	if err != nil {
		return err
	}

	// Build tool registry with all defaults.
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	hook := cfg.ToolHook
	if hook == nil {
		hook = DefaultMCPToolHook{}
	}

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    identity.CommandName,
		Version: buildinfo.Current().Version,
	}, nil)
	runtime := newStandaloneMCPRuntime()

	// Register each tool that is safe to expose without QueryEngine ownership.
	for _, info := range reg.List() {
		impl, ok := reg.Get(info.Name)
		if !ok || !standaloneMCPToolExposable(impl) {
			continue
		}

		// Build MCP tool definition.
		tool := &sdkmcp.Tool{
			Name:        info.Name,
			Description: info.Desc,
		}

		// Convert InputSchema from ParamsOneOf to map[string]any.
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

		// Set annotations from tool contract metadata.
		ann := &sdkmcp.ToolAnnotations{
			ReadOnlyHint: impl.IsReadOnly,
		}
		if impl.IsDestructive {
			destructive := true
			ann.DestructiveHint = &destructive
		}
		if impl.NeedsPermissions {
			openWorld := true
			ann.OpenWorldHint = &openWorld
		}
		if impl.IsConcurrencySafe != nil {
			ann.IdempotentHint = impl.IsConcurrencySafe(nil)
		}
		tool.Annotations = ann

		// Capture impl for closure.
		toolImpl := impl
		toolName := info.Name
		server.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			ctx = runtime.bind(ctx)
			return executeTool(ctx, toolImpl, toolName, policy, hook, req)
		})
	}

	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

func standaloneMCPToolExposable(impl tools.ToolImpl) bool {
	if impl.Info == nil {
		return false
	}
	_, admitted := standaloneMCPToolAllowlist[impl.Info.Name]
	return admitted &&
		!impl.IsHidden &&
		!impl.IsPlanModeTransition &&
		!impl.RequiresQueryEngine &&
		(impl.Execute != nil || impl.ExecuteCtx != nil)
}

// executeTool dispatches a CallToolRequest to the appropriate tool implementation.
// It enforces the permission policy and logs tool invocations.
func executeTool(ctx context.Context, impl tools.ToolImpl, toolName string, policy standalonePermissionPolicy, hook MCPToolHook, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	// Enforce permission policy.
	if !policy.valid() {
		log.Printf("[mcp] denied %s (invalid standalone permission policy)", toolName)
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: fmt.Sprintf("tool %q is not allowed: invalid standalone MCP permission policy", toolName)},
			},
		}, nil
	}
	if !policy.allows(impl.IsReadOnly) {
		log.Printf("[mcp] denied %s (strict mode: write tools not allowed)", toolName)
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: fmt.Sprintf("tool %q is not allowed in strict permission mode (read-only tools only)", toolName)},
			},
		}, nil
	}

	// Marshal arguments back to JSON string for the tool executor.
	var input string
	if req.Params.Arguments != nil {
		input = string(req.Params.Arguments)
	} else {
		input = "{}"
	}

	// Pre-tool hook.
	hook.PreToolCall(ctx, toolName, input)
	start := time.Now()

	// Execute the tool.
	var result string
	var err error
	if impl.ExecuteCtx != nil {
		result, err = impl.ExecuteCtx(ctx, input)
	} else if impl.Execute != nil {
		result, err = impl.Execute(input)
	} else {
		hook.PostToolCall(ctx, toolName, 0, time.Since(start), fmt.Errorf("no execute function"))
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: fmt.Sprintf("tool %q has no execute function", req.Params.Name)},
			},
		}, nil
	}

	// Post-tool hook.
	hook.PostToolCall(ctx, toolName, len(result), time.Since(start), err)

	if err != nil {
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: err.Error()},
			},
		}, nil
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: result},
		},
	}, nil
}
