package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/internal/identity"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ElicitationHandler is a callback invoked when an MCP server requests user
// input mid-call via the elicitation/create protocol method.
type ElicitationHandler func(ctx context.Context, message string, schema map[string]any) (action string, content map[string]any, err error)

// MCPClient wraps the official MCP SDK client for a single server connection.
// It provides the same public API as the previous hand-rolled implementation
// while delegating protocol handling to the SDK.
type MCPClient struct {
	config  ServerConfig
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession

	mu        sync.Mutex
	status    ServerStatus
	connected bool
	tools     []MCPTool

	connectionGeneration uint64
	nextGeneration       uint64
	onClose              func(uint64)
	onToolsChanged       func(uint64)
	elicitationHandler   ElicitationHandler

	// done is closed when the session-monitor goroutine exits.
	done chan struct{}

	// notifications dispatches server notifications to registered handlers.
	notifications *NotificationDispatcher

	// reconnManager handles automatic reconnection with backoff.
	reconnManager *reconnectionManager
}

// NewMCPClient creates a new MCPClient for the given server configuration.
// The client is not connected until Connect is called.
func NewMCPClient(cfg ServerConfig) *MCPClient {
	if cfg.ExecutionPolicy == nil {
		cfg.ExecutionPolicy = containment.DisabledCompatibilitySnapshot(
			cfg.CWD,
			containment.EntrypointEmbedded,
		)
	}
	return &MCPClient{
		config:        cfg,
		status:        StatusDisconnected,
		done:          make(chan struct{}),
		notifications: NewNotificationDispatcher(),
	}
}

// Connect establishes a connection to the MCP server, performs the protocol
// handshake, and discovers available tools.
func (c *MCPClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	c.status = StatusConnecting
	elicitationHandler := c.elicitationHandler
	// Reserve a generation before building the SDK client so every callback
	// installed on this connection captures one immutable token. Failed
	// connection attempts may leave gaps, but successful connections are always
	// strictly monotonic and never reuse a prior session's token.
	c.nextGeneration++
	generation := c.nextGeneration
	c.mu.Unlock()

	// Build SDK client with notification handlers.
	opts := &sdkmcp.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, _ *sdkmcp.ToolListChangedRequest) {
			c.dispatchToolsChanged(generation)
		},
		ProgressNotificationHandler: func(ctx context.Context, req *sdkmcp.ProgressNotificationClientRequest) {
			c.notifications.Dispatch(ctx, NotificationProgress, &ProgressNotification{
				ProgressToken: req.Params.ProgressToken,
				Progress:      req.Params.Progress,
				Total:         req.Params.Total,
				Message:       req.Params.Message,
			})
		},
		ResourceUpdatedHandler: func(ctx context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) {
			c.notifications.Dispatch(ctx, NotificationResourceUpdated, &ResourceUpdatedNotification{
				URI: req.Params.URI,
			})
		},
		LoggingMessageHandler: func(ctx context.Context, req *sdkmcp.LoggingMessageRequest) {
			c.notifications.Dispatch(ctx, NotificationLogging, &LoggingNotification{
				Level:  string(req.Params.Level),
				Logger: req.Params.Logger,
				Data:   req.Params.Data,
			})
		},
	}

	// Wire elicitation handler if set.
	if elicitationHandler != nil {
		handler := elicitationHandler
		opts.ElicitationHandler = func(ctx context.Context, req *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
			var schema map[string]any
			if req.Params.RequestedSchema != nil {
				data, _ := json.Marshal(req.Params.RequestedSchema)
				_ = json.Unmarshal(data, &schema)
			}
			action, content, err := handler(ctx, req.Params.Message, schema)
			if err != nil {
				return &sdkmcp.ElicitResult{Action: "decline"}, nil
			}
			return &sdkmcp.ElicitResult{Action: action, Content: content}, nil
		}
	}

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{
			Name:    identity.CommandName,
			Version: "1.0.0",
		},
		opts,
	)

	// Build transport from config.
	transport, err := c.buildTransport()
	if err != nil {
		c.mu.Lock()
		c.status = StatusFailed
		c.mu.Unlock()
		return fmt.Errorf("mcp: failed to build transport: %w", err)
	}

	// Connect (SDK performs initialize handshake automatically).
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if closer, ok := transport.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		c.mu.Lock()
		c.status = StatusFailed
		c.mu.Unlock()
		if c.config.Type == "stdio" || c.config.Type == "" {
			return fmt.Errorf("mcp: stdio connection failed")
		}
		return fmt.Errorf("mcp: connection failed: %w", err)
	}

	c.mu.Lock()
	c.client = client
	c.session = session
	c.connectionGeneration = generation
	c.status = StatusConnected
	c.connected = true
	done := make(chan struct{})
	c.done = done
	c.mu.Unlock()

	// Monitor session for disconnection in background.
	// The protocol session intentionally outlives the request-scoped Connect
	// context and is terminated through Disconnect or owned transport cleanup.
	//nolint:gosec // Background lifecycle is required after Connect returns.
	go c.monitorSession(session, generation, done)

	return nil
}

// monitorSession waits for the session to end and invokes onClose.
func (c *MCPClient) monitorSession(session *sdkmcp.ClientSession, generation uint64, done chan struct{}) {
	defer close(done)
	if session == nil {
		return
	}
	// Wait blocks until the session is closed (by us or by the server).
	_ = session.Wait()

	c.mu.Lock()
	if c.session != session {
		c.mu.Unlock()
		return
	}
	wasConnected := c.connected
	c.connected = false
	c.status = StatusFailed
	onClose := c.onClose
	reconnManager := c.reconnManager
	c.mu.Unlock()

	// Wait returning only proves the direct protocol session ended. Closing
	// the session also closes the owned transport, which is responsible for
	// terminating any descendants left behind by a stdio server.
	_ = session.Close()

	if wasConnected && onClose != nil {
		onClose(generation)
	}

	// Trigger automatic reconnection if enabled.
	if wasConnected && reconnManager != nil && reconnManager.IsActive() {
		reconnManager.TriggerReconnect(context.Background(), fmt.Errorf("session closed unexpectedly"))
	}
}

// dispatchToolsChanged invokes the current callback for the exact connection
// generation that received the SDK notification.
func (c *MCPClient) dispatchToolsChanged(generation uint64) {
	c.mu.Lock()
	handler := c.onToolsChanged
	c.mu.Unlock()
	if handler != nil {
		handler(generation)
	}
}

// Disconnect closes the connection to the MCP server.
func (c *MCPClient) Disconnect() error {
	c.mu.Lock()
	session := c.session
	c.session = nil
	c.connected = false
	c.status = StatusDisconnected
	c.tools = nil
	c.mu.Unlock()

	if session != nil {
		return session.Close()
	}
	return nil
}

// IsConnected returns whether the client is currently connected.
func (c *MCPClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Status returns the current connection status.
func (c *MCPClient) Status() ServerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Reconnect disconnects and re-connects to the server.
func (c *MCPClient) Reconnect(ctx context.Context) error {
	c.mu.Lock()
	done := c.done
	hadSession := c.session != nil
	c.mu.Unlock()

	_ = c.Disconnect()
	// Wait for monitor goroutine to finish only if a session was active.
	if hadSession {
		<-done
	}
	return c.Connect(ctx)
}

// ListTools fetches the available tools from the server.
// Uses the paginated iterator to automatically handle pagination.
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	// Use paginated iterator for auto-pagination.
	var tools []MCPTool
	for tool, err := range session.Tools(ctx, &sdkmcp.ListToolsParams{}) {
		if err != nil {
			return nil, fmt.Errorf("mcp: tools/list failed: %w", err)
		}
		tools = append(tools, sdkToolToMCPTool(tool, c.config.Name))
	}

	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()

	return tools, nil
}

// CallTool invokes a tool on the MCP server with the given arguments.
// A timeout is enforced from the server config, MCP_TOOL_TIMEOUT env var, or default (60s).
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	target, err := c.BindToolCallTarget()
	if err != nil {
		return nil, err
	}
	return target.CallTool(ctx, name, args)
}

// MCPToolCallTarget binds a tool call to one exact MCP SDK session.
// It remains valid for that session even after the client reconnects; it never
// resolves through MCPClient's mutable current session.
type MCPToolCallTarget struct {
	session    *sdkmcp.ClientSession
	generation uint64
	timeout    time.Duration
}

// BindToolCallTarget captures the current connected SDK session for one exact
// tool-call dispatch target.
func (c *MCPClient) BindToolCallTarget() (*MCPToolCallTarget, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	target := &MCPToolCallTarget{
		session:    c.session,
		generation: c.connectionGeneration,
		timeout:    c.config.Timeout,
	}
	c.mu.Unlock()
	return target, nil
}

// Generation returns the immutable connection generation captured by this target.
func (t *MCPToolCallTarget) Generation() uint64 {
	if t == nil {
		return 0
	}
	return t.generation
}

// CallTool invokes a tool on the exact SDK session captured by the target.
func (t *MCPToolCallTarget) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	if t == nil || t.session == nil {
		return nil, fmt.Errorf("mcp: tool call target is unavailable")
	}
	timeout := t.timeout

	// Determine timeout.
	if timeout <= 0 {
		timeout = getToolTimeout()
	}

	// Apply timeout to context.
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := t.session.CallTool(callCtx, &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/call failed: %w", err)
	}

	// Convert SDK result to our ToolResult.
	toolResult := sdkResultToToolResult(result)

	// Apply truncation.
	return TruncateToolResult(toolResult), nil
}

// ListResources sends a resources/list request and returns available resources.
func (c *MCPClient) ListResources(ctx context.Context) ([]MCPResource, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	result, err := session.ListResources(ctx, &sdkmcp.ListResourcesParams{})
	if err != nil {
		return nil, fmt.Errorf("mcp: resources/list failed: %w", err)
	}

	var resources []MCPResource
	for _, r := range result.Resources {
		resources = append(resources, MCPResource{
			URI:         r.URI,
			Name:        r.Name,
			MimeType:    r.MIMEType,
			Description: r.Description,
			ServerName:  c.config.Name,
		})
	}
	return resources, nil
}

// ReadResource reads a specific resource by URI.
func (c *MCPClient) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	result, err := session.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("mcp: resources/read failed: %w", err)
	}

	var contents []ResourceContent
	for _, rc := range result.Contents {
		contents = append(contents, ResourceContent{
			URI:      rc.URI,
			MimeType: rc.MIMEType,
			Text:     rc.Text,
		})
	}
	return contents, nil
}

// SupportsToolsListChanged returns true since the SDK handles this automatically.
func (c *MCPClient) SupportsToolsListChanged() bool {
	return true
}

// SetOnClose sets a callback invoked when the connection is lost.
func (c *MCPClient) SetOnClose(fn func()) {
	if fn == nil {
		c.SetOnCloseWithGeneration(nil)
		return
	}
	c.SetOnCloseWithGeneration(func(uint64) { fn() })
}

// SetOnToolsChanged sets a callback invoked when tools/list_changed notification arrives.
func (c *MCPClient) SetOnToolsChanged(fn func()) {
	if fn == nil {
		c.SetOnToolsChangedWithGeneration(nil)
		return
	}
	c.SetOnToolsChangedWithGeneration(func(uint64) { fn() })
}

// SetOnCloseWithGeneration sets a callback invoked when the exact connection
// generation is lost.
func (c *MCPClient) SetOnCloseWithGeneration(fn func(uint64)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onClose = fn
}

// SetOnToolsChangedWithGeneration sets a callback invoked when an exact
// connection generation receives tools/list_changed.
func (c *MCPClient) SetOnToolsChangedWithGeneration(fn func(uint64)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onToolsChanged = fn
}

// SetElicitationHandler sets a callback invoked when the server requests user input.
// Must be set before Connect is called.
func (c *MCPClient) SetElicitationHandler(fn ElicitationHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elicitationHandler = fn
}

// ListResourceTemplates fetches resource templates from the server.
func (c *MCPClient) ListResourceTemplates(ctx context.Context) ([]MCPResourceTemplate, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	result, err := session.ListResourceTemplates(ctx, &sdkmcp.ListResourceTemplatesParams{})
	if err != nil {
		return nil, fmt.Errorf("mcp: resources/templates/list failed: %w", err)
	}

	var templates []MCPResourceTemplate
	for _, t := range result.ResourceTemplates {
		templates = append(templates, MCPResourceTemplate{
			URITemplate: t.URITemplate,
			Name:        t.Name,
			Description: t.Description,
			MimeType:    t.MIMEType,
			ServerName:  c.config.Name,
		})
	}
	return templates, nil
}

// ListPrompts fetches available prompts from the server.
func (c *MCPClient) ListPrompts(ctx context.Context) ([]MCPPrompt, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	result, err := session.ListPrompts(ctx, &sdkmcp.ListPromptsParams{})
	if err != nil {
		return nil, fmt.Errorf("mcp: prompts/list failed: %w", err)
	}

	var prompts []MCPPrompt
	for _, p := range result.Prompts {
		prompt := MCPPrompt{
			Name:        p.Name,
			Description: p.Description,
			ServerName:  c.config.Name,
		}
		for _, arg := range p.Arguments {
			prompt.Arguments = append(prompt.Arguments, MCPPromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			})
		}
		prompts = append(prompts, prompt)
	}
	return prompts, nil
}

// GetPrompt retrieves a specific prompt by name with optional arguments.
func (c *MCPClient) GetPrompt(ctx context.Context, name string, args map[string]string) (*MCPPromptResult, error) {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	result, err := session.GetPrompt(ctx, &sdkmcp.GetPromptParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: prompts/get failed: %w", err)
	}

	pr := &MCPPromptResult{
		Description: result.Description,
	}
	for _, msg := range result.Messages {
		var text string
		if tc, ok := msg.Content.(*sdkmcp.TextContent); ok {
			text = tc.Text
		} else {
			data, _ := json.Marshal(msg.Content)
			text = string(data)
		}
		pr.Messages = append(pr.Messages, MCPPromptMessage{
			Role:    string(msg.Role),
			Content: text,
		})
	}
	return pr, nil
}

// --- Transport construction ---

func (c *MCPClient) buildTransport() (sdkmcp.Transport, error) {
	switch c.config.Type {
	case "stdio", "":
		return newStdioProcessTransport(c.config)

	case "http":
		if c.config.URL == "" {
			return nil, fmt.Errorf("mcp: http transport requires a URL")
		}
		transport := &sdkmcp.StreamableClientTransport{
			Endpoint: c.config.URL,
		}
		if len(c.config.Headers) > 0 {
			transport.HTTPClient = httpClientWithHeaders(c.config.Headers)
		}
		return transport, nil

	case "sse":
		if c.config.URL == "" {
			return nil, fmt.Errorf("mcp: sse transport requires a URL")
		}
		transport := &sdkmcp.SSEClientTransport{
			Endpoint: c.config.URL,
		}
		if len(c.config.Headers) > 0 {
			transport.HTTPClient = httpClientWithHeaders(c.config.Headers)
		}
		return transport, nil

	default:
		return nil, fmt.Errorf("mcp: unsupported transport type %q", c.config.Type)
	}
}

// --- Conversion helpers ---

// sdkToolToMCPTool converts an SDK Tool to our internal MCPTool representation.
func sdkToolToMCPTool(t *sdkmcp.Tool, serverName string) MCPTool {
	tool := MCPTool{
		Name:        t.Name,
		Description: t.Description,
		ServerName:  serverName,
	}

	// Convert InputSchema (any) to map[string]any.
	if t.InputSchema != nil {
		data, err := json.Marshal(t.InputSchema)
		if err == nil {
			var schema map[string]any
			if json.Unmarshal(data, &schema) == nil {
				tool.InputSchema = schema
			}
		}
	}

	// Extract annotations.
	if t.Annotations != nil {
		tool.Annotations.Title = t.Annotations.Title
		tool.Annotations.ReadOnlyHint = t.Annotations.ReadOnlyHint
		if t.Annotations.DestructiveHint != nil {
			tool.Annotations.DestructiveHint = *t.Annotations.DestructiveHint
		}
		if t.Annotations.OpenWorldHint != nil {
			tool.Annotations.OpenWorldHint = *t.Annotations.OpenWorldHint
		}
	}

	// Extract _meta fields (searchHint, alwaysLoad).
	meta := t.GetMeta()
	if meta != nil {
		if hint, ok := meta["anthropic/searchHint"].(string); ok {
			tool.SearchHint = hint
		}
		if al, ok := meta["anthropic/alwaysLoad"].(bool); ok {
			tool.AlwaysLoad = al
		}
	}

	return tool
}

// sdkResultToToolResult converts an SDK CallToolResult to our internal ToolResult.
func sdkResultToToolResult(result *sdkmcp.CallToolResult) *ToolResult {
	tr := &ToolResult{
		IsError: result.IsError,
	}
	for _, c := range result.Content {
		switch ct := c.(type) {
		case *sdkmcp.TextContent:
			tr.Content = append(tr.Content, ContentBlock{Type: "text", Text: ct.Text})
		case *sdkmcp.ImageContent:
			// Encode binary image data as base64 content block.
			bc, err := EncodeBinaryContent(ct.Data, ct.MIMEType)
			if err != nil {
				// If encoding fails (e.g., too large), include error description.
				tr.Content = append(tr.Content, ContentBlock{Type: "text", Text: fmt.Sprintf("[image error: %v]", err)})
			} else {
				tr.Content = append(tr.Content, ContentBlock{Type: "image", Text: bc.Base64})
			}
		case *sdkmcp.AudioContent:
			// Encode binary audio data as base64 content block.
			bc, err := EncodeBinaryContent(ct.Data, ct.MIMEType)
			if err != nil {
				tr.Content = append(tr.Content, ContentBlock{Type: "text", Text: fmt.Sprintf("[audio error: %v]", err)})
			} else {
				tr.Content = append(tr.Content, ContentBlock{Type: "resource", Text: bc.Base64})
			}
		default:
			// Unknown content type, try to get text representation.
			data, _ := json.Marshal(c)
			tr.Content = append(tr.Content, ContentBlock{Type: "text", Text: string(data)})
		}
	}
	return tr
}

// --- HTTP helpers ---

// headerTransport wraps an http.RoundTripper to inject custom headers.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// httpClientWithHeaders creates an HTTP client that injects custom headers.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	return &http.Client{
		Transport: &headerTransport{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}

// --- Auth helpers ---

// SetAPIKey stores an API key credential for this MCP server.
// The key is stored in the config's environment for the next reconnection.
func (c *MCPClient) SetAPIKey(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.Env == nil {
		c.config.Env = make(map[string]string)
	}
	c.config.Env["MCP_API_KEY"] = key
	return nil
}

// InitiateOAuth is a placeholder for OAuth flow initiation.
// Full OAuth PKCE support requires the SDK's auth package integration.
func (c *MCPClient) InitiateOAuth() (string, error) {
	return "", fmt.Errorf("mcp: OAuth flow not yet implemented with SDK")
}

// --- Timeout helpers ---

// getToolTimeout returns the tool call timeout from env or default.
func getToolTimeout() time.Duration {
	if envVal := os.Getenv("MCP_TOOL_TIMEOUT"); envVal != "" {
		if secs, err := strconv.Atoi(envVal); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultToolTimeout
}

// --- Notification helpers ---

// Notifications returns the notification dispatcher for this client.
// Use it to register handlers for server notifications.
func (c *MCPClient) Notifications() *NotificationDispatcher {
	return c.notifications
}

// --- Ping / Subscribe helpers ---

// Ping sends a ping request to the server to check connectivity.
func (c *MCPClient) Ping(ctx context.Context) error {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	return session.Ping(ctx, &sdkmcp.PingParams{})
}

// Subscribe subscribes to resource update notifications for the given URI.
func (c *MCPClient) Subscribe(ctx context.Context, uri string) error {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	err := session.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: uri})
	if err != nil {
		return fmt.Errorf("mcp: resources/subscribe failed: %w", err)
	}

	// Track subscription for reconnection state restoration.
	if c.reconnManager != nil {
		c.reconnManager.AddSubscription(uri)
	}
	return nil
}

// Unsubscribe unsubscribes from resource update notifications for the given URI.
func (c *MCPClient) Unsubscribe(ctx context.Context, uri string) error {
	c.mu.Lock()
	if !c.connected || c.session == nil {
		c.mu.Unlock()
		return fmt.Errorf("mcp: client is not connected")
	}
	session := c.session
	c.mu.Unlock()

	err := session.Unsubscribe(ctx, &sdkmcp.UnsubscribeParams{URI: uri})
	if err != nil {
		return fmt.Errorf("mcp: resources/unsubscribe failed: %w", err)
	}

	// Remove from tracked subscriptions.
	if c.reconnManager != nil {
		c.reconnManager.RemoveSubscription(uri)
	}
	return nil
}

// --- Reconnection helpers ---

// EnableReconnect enables automatic reconnection with the given configuration.
// Must be called after a successful Connect. The reconnection manager will
// monitor connection health and attempt to reconnect on failure.
func (c *MCPClient) EnableReconnect(ctx context.Context, config ReconnectConfig) {
	c.mu.Lock()
	if c.reconnManager != nil {
		c.reconnManager.Stop()
	}
	c.reconnManager = newReconnectionManager(c, config)
	c.mu.Unlock()

	c.reconnManager.Start(ctx)
}

// DisableReconnect stops automatic reconnection behavior.
func (c *MCPClient) DisableReconnect() {
	c.mu.Lock()
	rm := c.reconnManager
	c.mu.Unlock()

	if rm != nil {
		rm.Stop()
	}
}

// ReconnectState returns the current reconnection state, or nil if
// reconnection is not enabled.
func (c *MCPClient) ReconnectState() *ReconnectState {
	c.mu.Lock()
	rm := c.reconnManager
	c.mu.Unlock()

	if rm == nil {
		return nil
	}
	state := rm.State()
	return &state
}
