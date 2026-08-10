package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/mcp"
	"github.com/cloudwego/eino/schema"
)

// MCPToolInfo describes a single tool provided by a connected MCP server.
type MCPToolInfo struct {
	// ServerName identifies which MCP server provides this tool.
	ServerName string
	// ToolName is the tool identifier used in tool calls.
	ToolName string
	// Description explains what the tool does.
	Description string
	// InputSchema is the JSON Schema describing the tool's parameters.
	InputSchema map[string]any
}

// MCPServerSnapshot is a stable read-only view of one manager-owned server.
type MCPServerSnapshot struct {
	Name       string
	Source     string
	Status     string
	Health     string
	Diagnostic string
	Tools      []*MCPToolInfo
}

// MCPInventorySnapshot is committed from one manager lock acquisition.
type MCPInventorySnapshot struct {
	Revision uint64
	Source   string
	Servers  []MCPServerSnapshot
}

// MCPApprovalResponse represents the user's decision on a new MCP server.
type MCPApprovalResponse int

const (
	MCPApprovalAllow       MCPApprovalResponse = iota // Allow for this session
	MCPApprovalAllowAlways                            // Always allow (persist)
	MCPApprovalDeny                                   // Deny connection
)

// MCPApprovalRequest contains information about a server requesting approval.
type MCPApprovalRequest struct {
	ServerName string
	Source     string   // command or URL
	Tools      []string // tool names discovered
}

// MCPToolManager manages connections to MCP servers and provides a unified
// interface for discovering and invoking their tools.
type MCPToolManager struct {
	lifecycleMu                         sync.Mutex
	mu                                  sync.RWMutex
	clients                             map[string]*mcp.MCPClient
	tools                               map[string]*MCPToolInfo // toolName -> info
	failures                            map[string]string
	configuredInspection                map[string]bool
	inventorySource                     string
	revision                            uint64
	registry                            *Registry
	serverOwners                        map[string]string
	serverGenerations                   map[string]uint64
	serverClientGenerations             map[string]uint64
	serverConfigs                       map[string]mcp.ServerConfig
	sessionServers                      map[string]SessionMCPServer
	managerID                           uint64
	serverOwnerSequence                 uint64
	serverGenerationSequence            uint64
	beforeOwnedGenerationPublishForTest func(*mcp.MCPClient)
	beforeOwnedRefreshPublishForTest    func()
	beforeSessionReconnectForTest       func([]*mcp.MCPClient)
	executionPolicy                     *containment.Snapshot
	executionBinding                    *containment.Binding

	// ApprovalFn is called before registering tools from a newly connected server.
	// If nil, all servers are auto-approved.
	ApprovalFn func(req MCPApprovalRequest) MCPApprovalResponse
}

// DefaultMCPManager is the global MCP tool manager instance used by MCPTool.
// Protected by globalMCPMu for safe concurrent engine creation.
var (
	DefaultMCPManager *MCPToolManager
	globalMCPMu       sync.Mutex
)

type mcpManagerCtxKey struct{}

// WithMCPManager returns a context carrying the given MCPToolManager.
func WithMCPManager(ctx context.Context, m *MCPToolManager) context.Context {
	return context.WithValue(ctx, mcpManagerCtxKey{}, m)
}

// MCPManagerFromCtx returns the per-engine MCPToolManager from context,
// falling back to DefaultMCPManager if not set.
func MCPManagerFromCtx(ctx context.Context) *MCPToolManager {
	if m, ok := ctx.Value(mcpManagerCtxKey{}).(*MCPToolManager); ok && m != nil {
		return m
	}
	return DefaultMCPManager
}

// NewMCPToolManager creates a new MCPToolManager with empty state.
func NewMCPToolManager() *MCPToolManager {
	return &MCPToolManager{
		clients:                 make(map[string]*mcp.MCPClient),
		tools:                   make(map[string]*MCPToolInfo),
		failures:                make(map[string]string),
		configuredInspection:    make(map[string]bool),
		inventorySource:         "runtime-manager",
		serverOwners:            make(map[string]string),
		serverGenerations:       make(map[string]uint64),
		serverClientGenerations: make(map[string]uint64),
		serverConfigs:           make(map[string]mcp.ServerConfig),
		sessionServers:          make(map[string]SessionMCPServer),
		managerID:               nextMCPManagerID(),
	}
}

// BindExecutionPolicy binds the immutable policy applied to every configured
// stdio process. Once bound, the manager rejects a different identity even if
// no process has started, so it cannot be shared and rebound across roots.
func (m *MCPToolManager) BindExecutionPolicy(policy *containment.Snapshot) error {
	if m == nil || policy == nil || policy.Digest() == "" {
		return fmt.Errorf("MCP execution policy must be valid")
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.executionBinding != nil && m.executionBinding.PolicyDigest() != policy.Digest() {
		return fmt.Errorf("MCP execution policy replacement rejected")
	}
	if m.executionPolicy != nil && m.executionPolicy.Digest() != policy.Digest() {
		return fmt.Errorf("MCP execution policy replacement rejected")
	}
	if m.executionPolicy == nil {
		m.executionPolicy = policy
	}
	return nil
}

// BindExecutionBinding pins the ambient process identity for every stdio MCP
// child managed by m. It intentionally does not turn HTTP/SSE into containment.
func (m *MCPToolManager) BindExecutionBinding(binding *containment.Binding) error {
	if m == nil || !validMCPExecutionBinding(binding) {
		return fmt.Errorf("MCP execution binding must be an available ambient stdio-mcp binding")
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.executionBinding != nil && m.executionBinding.Digest() != binding.Digest() {
		return fmt.Errorf("MCP execution binding replacement rejected")
	}
	if m.executionPolicy != nil && m.executionPolicy.Digest() != binding.PolicyDigest() {
		return fmt.Errorf("MCP execution policy replacement rejected")
	}
	m.executionBinding, m.executionPolicy = binding, binding.Policy()
	return nil
}

func validMCPExecutionBinding(binding *containment.Binding) bool {
	if binding == nil || binding.ProcessClass() != containment.ProcessClassStdioMCP || binding.Availability() != containment.BindingAvailable || binding.AdapterFamily() != containment.AdapterAmbientHost {
		return false
	}
	diagnostic := binding.Policy().Diagnostic()
	return diagnostic.Profile == containment.ProfileDangerFullAccess && diagnostic.State == containment.StateDisabled
}

func (m *MCPToolManager) ExecutionBindingDigest() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executionBinding.Digest()
}

// ExecutionPolicyDigest returns the manager's immutable process identity.
func (m *MCPToolManager) ExecutionPolicyDigest() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.executionPolicy == nil {
		return ""
	}
	return m.executionPolicy.Digest()
}

func (m *MCPToolManager) bindServerConfig(config *mcp.ServerConfig) (*mcp.ServerConfig, error) {
	if config == nil {
		return nil, fmt.Errorf("server configuration is unavailable")
	}
	bound := cloneMCPServerConfig(*config)
	m.mu.Lock()
	defer m.mu.Unlock()
	policy := bound.ExecutionPolicy
	binding := bound.ExecutionBinding
	if policy == nil {
		policy = m.executionPolicy
	}
	if policy == nil {
		policy = containment.DisabledCompatibilitySnapshot(bound.CWD, containment.EntrypointEmbedded)
	}
	if policy == nil || policy.Digest() == "" {
		return nil, fmt.Errorf("execution policy is unavailable")
	}
	if m.executionPolicy != nil && m.executionPolicy.Digest() != policy.Digest() {
		return nil, fmt.Errorf("execution policy mismatch")
	}
	if binding == nil {
		binding = m.executionBinding
	}
	if binding != nil {
		if !validMCPExecutionBinding(binding) || binding.PolicyDigest() != policy.Digest() || (m.executionBinding != nil && m.executionBinding.Digest() != binding.Digest()) {
			return nil, fmt.Errorf("execution binding mismatch")
		}
		if m.executionBinding == nil {
			m.executionBinding = binding
		}
	}
	if m.executionPolicy == nil {
		m.executionPolicy = policy
	}
	bound.ExecutionPolicy = policy
	bound.ExecutionBinding = binding
	return &bound, nil
}

// NewMCPInspectionManager creates a read-only manager inventory from resolved
// MCP configuration. It records only server names and enabled state; commands,
// URLs, arguments, headers, and environment values are neither retained in the
// snapshot nor connected.
func NewMCPInspectionManager(config *mcp.MCPConfig) *MCPToolManager {
	manager := NewMCPToolManager()
	manager.inventorySource = "configuration"
	if config == nil {
		return manager
	}
	for name, server := range config.Servers {
		if server == nil {
			continue
		}
		manager.configuredInspection[name] = server.Enabled
	}
	if len(manager.configuredInspection) > 0 {
		manager.revision = 1
	}
	return manager
}

// ConnectServer creates an MCPClient for the given server configuration,
// connects to the server, discovers its tools, and registers them in the manager.
// If the server supports tools.listChanged, a notification handler is registered
// to automatically refresh the tool list when the server's tools change.
func (m *MCPToolManager) ConnectServer(ctx context.Context, name string, config *mcp.ServerConfig) error {
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	if config == nil {
		m.recordFailure(name, "connect_failed")
		return fmt.Errorf("mcp_tool: server %q has no configuration", name)
	}
	config, err := m.bindServerConfig(config)
	if err != nil {
		m.recordFailure(name, "connect_failed")
		return fmt.Errorf("mcp_tool: server %q execution policy: %w", name, err)
	}
	client := mcp.NewMCPClient(*config)

	if err := client.Connect(ctx); err != nil {
		m.recordFailure(name, "connect_failed")
		return fmt.Errorf("mcp_tool: failed to connect to server %q: %w", name, err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Disconnect()
		m.recordFailure(name, "tool_discovery_failed")
		return fmt.Errorf("mcp_tool: failed to list tools from server %q: %w", name, err)
	}

	// Check approval before registering tools.
	if m.ApprovalFn != nil {
		toolNames := make([]string, 0, len(tools))
		for _, t := range tools {
			toolNames = append(toolNames, t.Name)
		}
		req := MCPApprovalRequest{
			ServerName: name,
			Source:     config.Command,
			Tools:      toolNames,
		}
		if config.URL != "" {
			req.Source = config.URL
		}
		response := m.ApprovalFn(req)
		if response == MCPApprovalDeny {
			_ = client.Disconnect()
			m.recordFailure(name, "approval_denied")
			return fmt.Errorf("mcp_tool: server %q denied by user", name)
		}
	}

	generation, owner := m.nextServerIdentity()
	candidate, err := newMCPServerGeneration(
		name,
		*config,
		client,
		tools,
		generation,
		owner,
	)
	if err != nil {
		_ = client.Disconnect()
		m.recordFailure(name, "tool_identity_invalid")
		return fmt.Errorf("mcp_tool: invalid tools from server %q: %w", name, err)
	}
	m.installGenerationCallbacks(candidate)

	m.mu.RLock()
	registry := m.registry
	retiredClient := m.clients[name]
	retiredOwner := m.serverOwners[name]
	beforePublish := m.beforeOwnedGenerationPublishForTest
	m.mu.RUnlock()

	if beforePublish != nil {
		beforePublish(client)
	}
	if !client.IsConnected() {
		_ = client.Disconnect()
		m.recordFailure(name, "connection_closed")
		return fmt.Errorf("mcp_tool: server %q closed before publication", name)
	}
	if registry != nil {
		expectedGeneration := registry.Generation()
		retiredOwners := nonEmptyStrings(retiredOwner)
		_, dispatchHooks, err := registry.replaceOwnedToolsDeferred(
			expectedGeneration,
			retiredOwners,
			candidate.implementations,
		)
		hookDispatches.add(dispatchHooks)
		if err != nil {
			_, dispatchHooks := registry.removeOwnedToolsDeferred(
				append(retiredOwners, owner)...,
			)
			hookDispatches.add(dispatchHooks)
			m.clearServerGeneration(name, retiredClient, retiredOwner, "registry_publish_failed")
			_ = client.Disconnect()
			disconnectMCPClientAsync(retiredClient)
			return fmt.Errorf("mcp_tool: failed to publish tools from server %q: %w", name, err)
		}
	}

	m.mu.Lock()
	for key, info := range m.tools {
		if info.ServerName == name {
			delete(m.tools, key)
		}
	}
	m.clients[name] = client
	m.serverOwners[name] = owner
	m.serverGenerations[name] = candidate.generation
	m.serverClientGenerations[name] = candidate.clientGeneration
	m.serverConfigs[name] = cloneMCPServerConfig(*config)
	delete(m.failures, name)
	for _, info := range candidate.infos {
		m.tools[mcpToolMapKey(name, info.ToolName)] = info
	}
	m.revision++
	m.mu.Unlock()
	disconnectMCPClientAsync(retiredClient)

	return nil
}

func (m *MCPToolManager) recordFailure(name, category string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.failures == nil {
		m.failures = make(map[string]string)
	}
	m.failures[name] = category
	m.revision++
	m.mu.Unlock()
}

// DisconnectAll disconnects all connected MCP servers and clears state.
func (m *MCPToolManager) DisconnectAll() error {
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	m.mu.Lock()
	clients := make(map[string]*mcp.MCPClient, len(m.clients))
	for name, client := range m.clients {
		clients[name] = client
	}
	owners := make([]string, 0, len(m.serverOwners))
	for _, owner := range m.serverOwners {
		owners = append(owners, owner)
	}
	registry := m.registry

	m.clients = make(map[string]*mcp.MCPClient)
	m.tools = make(map[string]*MCPToolInfo)
	m.failures = make(map[string]string)
	m.serverOwners = make(map[string]string)
	m.serverGenerations = make(map[string]uint64)
	m.serverClientGenerations = make(map[string]uint64)
	m.serverConfigs = make(map[string]mcp.ServerConfig)
	m.sessionServers = make(map[string]SessionMCPServer)
	m.revision++
	m.mu.Unlock()

	if registry != nil {
		_, dispatchHooks := registry.removeOwnedToolsDeferred(owners...)
		hookDispatches.add(dispatchHooks)
	}

	var errs []string
	for name, client := range clients {
		if err := client.Disconnect(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("mcp_tool: disconnect errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// DisconnectServer disconnects a single MCP server and removes its tools.
func (m *MCPToolManager) DisconnectServer(name string) error {
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	m.mu.Lock()
	client, ok := m.clients[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp_tool: server %q not found", name)
	}
	registry := m.registry
	owner := m.serverOwners[name]
	delete(m.clients, name)
	delete(m.failures, name)
	delete(m.serverOwners, name)
	delete(m.serverGenerations, name)
	delete(m.serverClientGenerations, name)
	delete(m.serverConfigs, name)
	delete(m.sessionServers, name)

	// Remove tools belonging to this server.
	for toolName, info := range m.tools {
		if info.ServerName == name {
			delete(m.tools, toolName)
		}
	}
	m.revision++
	m.mu.Unlock()

	if registry != nil {
		_, dispatchHooks := registry.removeOwnedToolsDeferred(owner)
		hookDispatches.add(dispatchHooks)
	}
	return client.Disconnect()
}

// ListAvailableTools returns all tools currently available from connected servers.
func (m *MCPToolManager) ListAvailableTools() []*MCPToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*MCPToolInfo, 0, len(m.tools))
	for _, info := range m.tools {
		result = append(result, info)
	}
	return result
}

// InventorySnapshot returns manager inventory, health and tools under one
// manager generation. Failed configured servers remain inspectable even when
// they never reached the live client map.
func (m *MCPToolManager) InventorySnapshot() MCPInventorySnapshot {
	if m == nil {
		return MCPInventorySnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make(
		map[string]struct{},
		len(m.clients)+len(m.failures)+len(m.configuredInspection),
	)
	for name := range m.clients {
		names[name] = struct{}{}
	}
	for name := range m.failures {
		names[name] = struct{}{}
	}
	for name := range m.configuredInspection {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	snapshot := MCPInventorySnapshot{
		Revision: m.revision,
		Source:   m.inventorySource,
		Servers:  make([]MCPServerSnapshot, 0, len(ordered)),
	}
	for _, name := range ordered {
		server := MCPServerSnapshot{
			Name:   name,
			Source: "runtime-manager",
			Status: "failed",
			Health: "unavailable",
		}
		if enabled, configured := m.configuredInspection[name]; configured {
			server.Source = "configuration"
			server.Status = "configured"
			if !enabled {
				server.Status = "disabled"
			}
			server.Health = "unprobed"
			server.Diagnostic = "inspection_only_no_connection"
		}
		if client := m.clients[name]; client != nil {
			server.Source = "runtime-manager"
			server.Status = string(client.Status())
			if client.IsConnected() {
				server.Health = "healthy"
			}
		}
		if failure := m.failures[name]; failure != "" {
			server.Source = "runtime-manager"
			server.Diagnostic = failure
			if server.Health == "healthy" {
				server.Health = "degraded"
			} else {
				server.Status = "failed"
				server.Health = "unavailable"
			}
		}
		for _, info := range m.tools {
			if info.ServerName != name {
				continue
			}
			cloned := *info
			cloned.InputSchema = cloneMCPInputSchema(info.InputSchema)
			server.Tools = append(server.Tools, &cloned)
		}
		sort.Slice(server.Tools, func(i, j int) bool {
			return server.Tools[i].ToolName < server.Tools[j].ToolName
		})
		snapshot.Servers = append(snapshot.Servers, server)
	}
	return snapshot
}

func cloneMCPInputSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	cloned := make(map[string]any, len(schema))
	for key, value := range schema {
		cloned[key] = cloneMCPInputValue(value)
	}
	return cloned
}

func cloneMCPInputValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMCPInputSchema(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i := range typed {
			cloned[i] = cloneMCPInputValue(typed[i])
		}
		return cloned
	default:
		return value
	}
}

// CallTool invokes the named tool on its owning MCP server with the given arguments.
func (m *MCPToolManager) CallTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	return m.CallServerTool(ctx, "", toolName, args)
}

// CallServerTool invokes one tool, optionally requiring an exact server name.
// Raw tool names that are supplied by more than one server are ambiguous
// unless the caller provides serverName.
func (m *MCPToolManager) CallServerTool(
	ctx context.Context,
	serverName string,
	toolName string,
	args map[string]any,
) (string, error) {
	m.mu.RLock()
	var info *MCPToolInfo
	for _, candidate := range m.tools {
		if candidate.ToolName != toolName ||
			serverName != "" && candidate.ServerName != serverName {
			continue
		}
		if info != nil {
			m.mu.RUnlock()
			return "", fmt.Errorf("mcp_tool: tool %q is ambiguous", toolName)
		}
		info = candidate
	}
	if info == nil {
		m.mu.RUnlock()
		return "", fmt.Errorf("mcp_tool: unknown tool %q", toolName)
	}
	client, clientOK := m.clients[info.ServerName]
	m.mu.RUnlock()

	if !clientOK {
		return "", fmt.Errorf("mcp_tool: server %q not connected", info.ServerName)
	}

	result, err := client.CallTool(ctx, toolName, args)
	if err != nil {
		return "", fmt.Errorf("mcp_tool: call %q failed: %w", toolName, err)
	}

	// Assemble text content from response blocks.
	var parts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}

	text := strings.Join(parts, "\n")
	if result.IsError {
		return "", fmt.Errorf("mcp_tool: tool %q returned error: %s", toolName, text)
	}

	return text, nil
}

// ListResources returns resources from all connected servers, optionally filtered by server name.
func (m *MCPToolManager) ListResources(ctx context.Context, serverFilter string) ([]mcp.MCPResource, error) {
	m.mu.RLock()
	clients := make(map[string]*mcp.MCPClient, len(m.clients))
	for k, v := range m.clients {
		clients[k] = v
	}
	m.mu.RUnlock()

	var all []mcp.MCPResource
	for name, client := range clients {
		if serverFilter != "" && name != serverFilter {
			continue
		}
		resources, err := client.ListResources(ctx)
		if err != nil {
			continue // individual server failures are non-fatal
		}
		all = append(all, resources...)
	}
	return all, nil
}

// ReadResource reads a resource from the named server.
func (m *MCPToolManager) ReadResource(ctx context.Context, serverName, uri string) ([]mcp.ResourceContent, error) {
	m.mu.RLock()
	client, ok := m.clients[serverName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("mcp: server %q not connected", serverName)
	}
	return client.ReadResource(ctx, uri)
}

// ListResourceTemplates returns resource templates from all connected servers, optionally filtered by server name.
func (m *MCPToolManager) ListResourceTemplates(ctx context.Context, serverFilter string) ([]mcp.MCPResourceTemplate, error) {
	m.mu.RLock()
	clients := make(map[string]*mcp.MCPClient, len(m.clients))
	for k, v := range m.clients {
		clients[k] = v
	}
	m.mu.RUnlock()

	var all []mcp.MCPResourceTemplate
	for name, client := range clients {
		if serverFilter != "" && name != serverFilter {
			continue
		}
		templates, err := client.ListResourceTemplates(ctx)
		if err != nil {
			continue
		}
		all = append(all, templates...)
	}
	return all, nil
}

// ListPrompts returns prompts from all connected servers, optionally filtered by server name.
func (m *MCPToolManager) ListPrompts(ctx context.Context, serverFilter string) ([]mcp.MCPPrompt, error) {
	m.mu.RLock()
	clients := make(map[string]*mcp.MCPClient, len(m.clients))
	for k, v := range m.clients {
		clients[k] = v
	}
	m.mu.RUnlock()

	var all []mcp.MCPPrompt
	for name, client := range clients {
		if serverFilter != "" && name != serverFilter {
			continue
		}
		prompts, err := client.ListPrompts(ctx)
		if err != nil {
			continue
		}
		all = append(all, prompts...)
	}
	return all, nil
}

// GetPrompt retrieves a specific prompt from the named server.
func (m *MCPToolManager) GetPrompt(ctx context.Context, serverName, promptName string, args map[string]string) (*mcp.MCPPromptResult, error) {
	m.mu.RLock()
	client, ok := m.clients[serverName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("mcp: server %q not connected", serverName)
	}
	return client.GetPrompt(ctx, promptName, args)
}

// ReconnectServer disconnects and reconnects a named server, re-discovering
// its tools. Useful for manual reconnection (e.g. /mcp restart <name>).
func (m *MCPToolManager) ReconnectServer(ctx context.Context, name string) error {
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	m.mu.RLock()
	config, configured := m.serverConfigs[name]
	retiredClient := m.clients[name]
	retiredGeneration := m.serverGenerations[name]
	retiredOwner := m.serverOwners[name]
	registry := m.registry
	m.mu.RUnlock()
	if !configured {
		return fmt.Errorf("mcp_tool: server %q not found", name)
	}

	client := mcp.NewMCPClient(cloneMCPServerConfig(config))
	if err := client.Connect(ctx); err != nil {
		hookDispatches.add(m.failOwnedServerGeneration(
			name,
			retiredClient,
			retiredGeneration,
			retiredOwner,
			"reconnect_failed",
		))
		if retiredClient == nil {
			m.recordFailure(name, "reconnect_failed")
		}
		return fmt.Errorf("mcp_tool: failed to reconnect to server %q: %w", name, err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Disconnect()
		hookDispatches.add(m.failOwnedServerGeneration(
			name,
			retiredClient,
			retiredGeneration,
			retiredOwner,
			"tool_discovery_failed",
		))
		if retiredClient == nil {
			m.recordFailure(name, "tool_discovery_failed")
		}
		return fmt.Errorf("mcp_tool: failed to list tools after reconnect %q: %w", name, err)
	}

	generation, owner := m.nextServerIdentity()
	candidate, err := newMCPServerGeneration(
		name,
		config,
		client,
		tools,
		generation,
		owner,
	)
	if err != nil {
		_ = client.Disconnect()
		hookDispatches.add(m.failOwnedServerGeneration(
			name,
			retiredClient,
			retiredGeneration,
			retiredOwner,
			"tool_identity_invalid",
		))
		if retiredClient == nil {
			m.recordFailure(name, "tool_identity_invalid")
		}
		return fmt.Errorf("mcp_tool: invalid tools after reconnect %q: %w", name, err)
	}
	m.installGenerationCallbacks(candidate)
	m.mu.RLock()
	beforePublish := m.beforeOwnedGenerationPublishForTest
	m.mu.RUnlock()
	if beforePublish != nil {
		beforePublish(client)
	}
	if !client.IsConnected() {
		_ = client.Disconnect()
		hookDispatches.add(m.failOwnedServerGeneration(
			name,
			retiredClient,
			retiredGeneration,
			retiredOwner,
			"connection_closed",
		))
		if retiredClient == nil {
			m.recordFailure(name, "connection_closed")
		}
		return fmt.Errorf("mcp_tool: server %q closed before reconnect publication", name)
	}
	if registry != nil {
		expectedGeneration := registry.Generation()
		retiredOwners := nonEmptyStrings(retiredOwner)
		_, dispatchHooks, err := registry.replaceOwnedToolsDeferred(
			expectedGeneration,
			retiredOwners,
			candidate.implementations,
		)
		hookDispatches.add(dispatchHooks)
		if err != nil {
			_, dispatchHooks := registry.removeOwnedToolsDeferred(
				append(retiredOwners, candidate.owner)...,
			)
			hookDispatches.add(dispatchHooks)
			m.clearServerGeneration(
				name,
				retiredClient,
				retiredOwner,
				"reconnect_failed",
			)
			_ = client.Disconnect()
			disconnectMCPClientAsync(retiredClient)
			return fmt.Errorf("mcp_tool: failed to publish reconnect for server %q: %w", name, err)
		}
	}

	m.mu.Lock()
	for toolName, info := range m.tools {
		if info.ServerName == name {
			delete(m.tools, toolName)
		}
	}
	m.clients[name] = client
	m.serverOwners[name] = candidate.owner
	m.serverGenerations[name] = candidate.generation
	m.serverClientGenerations[name] = candidate.clientGeneration
	m.serverConfigs[name] = cloneMCPServerConfig(config)
	for _, info := range candidate.infos {
		m.tools[mcpToolMapKey(name, info.ToolName)] = info
	}
	delete(m.failures, name)
	m.revision++
	m.mu.Unlock()
	disconnectMCPClientAsync(retiredClient)

	return nil
}

// ServerStatus returns the connection status of a named server.
func (m *MCPToolManager) ServerStatus(name string) (mcp.ServerStatus, error) {
	m.mu.RLock()
	client, ok := m.clients[name]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("mcp_tool: server %q not found", name)
	}
	return client.Status(), nil
}

// ListConnectedServerNames returns the names of all servers tracked by the manager.
func (m *MCPToolManager) ListConnectedServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// ServerToolCount returns the number of tools provided by the named server.
func (m *MCPToolManager) ServerToolCount(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, info := range m.tools {
		if info.ServerName == name {
			count++
		}
	}
	return count
}

// ServerTools returns all tools provided by the named server.
func (m *MCPToolManager) ServerTools(name string) []*MCPToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*MCPToolInfo
	for _, info := range m.tools {
		if info.ServerName == name {
			result = append(result, info)
		}
	}
	return result
}

// ListMcpResourcesTool returns a tool that lists resources from connected MCP servers.
// Mirrors reference ListMcpResourcesTool.
func ListMcpResourcesTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ListMcpResourcesTool",
			Desc: "Lists available resources from configured MCP servers.\n" +
				"Each resource object includes a 'server' field indicating which server it's from.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"server": {Type: schema.String, Desc: "Optional server name to filter resources by"},
			}),
		},
		Execute: func(input string) (string, error) {
			return executeListMCPResources(context.Background(), input, DefaultMCPManager)
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return executeListMCPResources(ctx, input, MCPManagerFromCtx(ctx))
		},
		IsConcurrencySafe: func(input map[string]any) bool { return true },
	}
}

func executeListMCPResources(ctx context.Context, input string, manager *MCPToolManager) (string, error) {
	var params struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if manager == nil {
		return "[]", nil
	}
	resources, err := manager.ListResources(ctx, params.Server)
	if err != nil {
		return "", err
	}
	if len(resources) == 0 {
		return "No resources found from connected MCP servers.", nil
	}
	out, err := json.Marshal(resources)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ReadMcpResourceTool returns a tool that reads a specific MCP resource.
// Mirrors reference ReadMcpResourceTool.
func ReadMcpResourceTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ReadMcpResourceTool",
			Desc: "Reads a specific resource from an MCP server.\n" +
				"- server: The name of the MCP server to read from\n" +
				"- uri: The URI of the resource to read",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"server": {Type: schema.String, Desc: "The MCP server name", Required: true},
				"uri":    {Type: schema.String, Desc: "The resource URI to read", Required: true},
			}),
		},
		Execute: func(input string) (string, error) {
			return executeReadMCPResource(context.Background(), input, DefaultMCPManager)
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return executeReadMCPResource(ctx, input, MCPManagerFromCtx(ctx))
		},
		IsConcurrencySafe: func(input map[string]any) bool { return true },
	}
}

func executeReadMCPResource(ctx context.Context, input string, manager *MCPToolManager) (string, error) {
	var params struct {
		Server string `json:"server"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if params.Server == "" {
		return "", fmt.Errorf("server parameter is required")
	}
	if params.URI == "" {
		return "", fmt.Errorf("uri parameter is required")
	}
	if manager == nil {
		return "", fmt.Errorf("MCP manager not initialized")
	}
	contents, err := manager.ReadResource(ctx, params.Server, params.URI)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"contents": contents})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// MCPTool returns a ToolImpl that allows the agent to invoke tools provided
// by connected MCP servers via DefaultMCPManager.
func MCPTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "mcp_tool",
			Desc: "Invoke a tool provided by a connected MCP server. " +
				"Use ListAvailableTools or tool search to discover available MCP tools, " +
				"then call them by name with the required arguments.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"server":    {Type: schema.String, Desc: "The MCP server name (optional, auto-resolved from tool name)"},
				"tool":      {Type: schema.String, Desc: "The name of the MCP tool to invoke", Required: true},
				"arguments": {Type: schema.Object, Desc: "The arguments to pass to the tool as a JSON object", Required: true},
			}),
		},
		Execute: executeMCPTool,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return executeMCPToolWithManager(ctx, input, MCPManagerFromCtx(ctx))
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
	}
}

func executeMCPTool(input string) (string, error) {
	return executeMCPToolWithManager(context.Background(), input, DefaultMCPManager)
}

func executeMCPToolWithManager(ctx context.Context, input string, manager *MCPToolManager) (string, error) {
	var params struct {
		Server    string         `json:"server"`
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("mcp_tool: invalid params: %w", err)
	}

	if params.Tool == "" {
		return "", fmt.Errorf("mcp_tool: tool parameter is required")
	}
	if params.Arguments == nil {
		params.Arguments = make(map[string]any)
	}

	if manager == nil {
		return "", fmt.Errorf("mcp_tool: MCP manager not initialized")
	}

	return manager.CallServerTool(ctx, params.Server, params.Tool, params.Arguments)
}

// RegisterToolsInRegistry binds an unpublished compatibility manager to a
// registry through one owned batch. Runtime initialization binds the registry
// before connecting servers and does not use this compatibility path.
//
// Deprecated: bind the registry through InitMCPManager or
// PrepareSessionMCPManager so every later lifecycle transition stays owned.
func (m *MCPToolManager) RegisterToolsInRegistry(registry *Registry) {
	if m == nil || registry == nil {
		return
	}
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	additions, err := m.registryToolImplementations()
	if err != nil {
		return
	}
	m.mu.RLock()
	owners := make([]string, 0, len(m.serverOwners))
	for _, owner := range m.serverOwners {
		owners = append(owners, owner)
	}
	previousRegistry := m.registry
	m.mu.RUnlock()
	expectedGeneration := registry.Generation()
	_, dispatchHooks, err := registry.replaceOwnedToolsDeferred(
		expectedGeneration,
		owners,
		additions,
	)
	hookDispatches.add(dispatchHooks)
	if err != nil {
		_, dispatchHooks := registry.removeOwnedToolsDeferred(owners...)
		hookDispatches.add(dispatchHooks)
		return
	}
	if previousRegistry != nil && previousRegistry != registry {
		_, dispatchHooks := previousRegistry.removeOwnedToolsDeferred(owners...)
		hookDispatches.add(dispatchHooks)
	}
	m.mu.Lock()
	m.registry = registry
	m.revision++
	m.mu.Unlock()
}

func executeRegisteredMCPTool(
	ctx context.Context,
	input string,
	registeredName string,
	toolInfo *MCPToolInfo,
	target *mcp.MCPToolCallTarget,
) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("mcp tool %s: invalid input: %w", registeredName, err)
	}
	if target == nil {
		return "", fmt.Errorf("mcp tool %s: connection generation unavailable", registeredName)
	}
	result, err := target.CallTool(ctx, toolInfo.ToolName, args)
	if err != nil {
		return "", fmt.Errorf("mcp tool %s: call failed: %w", registeredName, err)
	}
	var parts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		return "", fmt.Errorf("mcp tool %s: tool returned error: %s", registeredName, text)
	}
	return text, nil
}

// InitMCP loads MCP configuration from the project directory, connects to all
// enabled servers, and makes their tools available via DefaultMCPManager.
// If registry is non-nil, discovered tools are also registered as first-class
// entries with mcp__server__tool naming so they go through normal permission checks.
func InitMCP(ctx context.Context, projectDir string, registry *Registry) error {
	manager, err := InitMCPManager(ctx, projectDir, registry)
	if err != nil {
		return err
	}
	globalMCPMu.Lock()
	DefaultMCPManager = manager
	globalMCPMu.Unlock()
	return nil
}

// InitMCPManager initializes an engine-scoped MCP manager without mutating the
// process-wide compatibility singleton.
func InitMCPManager(ctx context.Context, projectDir string, registry *Registry, policies ...*containment.Snapshot) (*MCPToolManager, error) {
	var policy *containment.Snapshot
	if len(policies) > 0 {
		policy = policies[0]
	}
	return initMCPManager(ctx, projectDir, registry, policy, nil)
}

// InitMCPManagerWithBinding binds configured stdio MCP before any server is
// connected. HTTP and SSE retain the value only as identity metadata.
func InitMCPManagerWithBinding(ctx context.Context, projectDir string, registry *Registry, binding *containment.Binding) (*MCPToolManager, error) {
	if !validMCPExecutionBinding(binding) {
		return nil, fmt.Errorf("mcp_tool: bind execution binding: invalid binding")
	}
	return initMCPManager(ctx, projectDir, registry, binding.Policy(), binding)
}

func initMCPManager(ctx context.Context, projectDir string, registry *Registry, requestedPolicy *containment.Snapshot, binding *containment.Binding) (*MCPToolManager, error) {
	manager := NewMCPToolManager()
	policy := containment.DisabledCompatibilitySnapshot(projectDir, containment.EntrypointEmbedded)
	if requestedPolicy != nil {
		policy = requestedPolicy
	}
	if err := manager.BindExecutionPolicy(policy); err != nil {
		return manager, fmt.Errorf("mcp_tool: bind execution policy: %w", err)
	}
	if binding != nil {
		if err := manager.BindExecutionBinding(binding); err != nil {
			return manager, fmt.Errorf("mcp_tool: bind execution binding: %w", err)
		}
	}
	manager.mu.Lock()
	manager.registry = registry
	manager.mu.Unlock()
	cfg, err := mcp.LoadMCPConfig(projectDir)
	if err != nil {
		return manager, fmt.Errorf("mcp_tool: failed to load config: %w", err)
	}

	if cfg == nil || cfg.IsEmpty() {
		return manager, nil
	}

	enabledServers := cfg.EnabledServers()
	names := make([]string, 0, len(enabledServers))
	for name := range enabledServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		serverCfg := enabledServers[name]
		sc := serverCfg.ToServerConfig()
		if err := manager.ConnectServer(ctx, name, &sc); err != nil {
			// Log but don't fail on individual server connection errors.
			// The agent can still use tools from other successfully connected servers.
			continue
		}
	}

	return manager, nil
}

// McpAuthTool returns a pseudo-tool surfaced when an MCP server needs OAuth
// authentication. Triggering this tool starts the auth flow so the server's
// real tools become available. Mirrors the reference McpAuthTool.
func McpAuthTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "McpAuth",
			Desc: "Authenticate with an MCP server that requires OAuth or API key credentials. " +
				"Use this when an MCP server's tools are unavailable due to missing authentication. " +
				"After successful auth, the server's tools will become available.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"server_name": {Type: schema.String, Desc: "Name of the MCP server to authenticate with", Required: true},
				"auth_type":   {Type: schema.String, Desc: "Authentication type: 'oauth' or 'api_key'", Required: true},
				"credentials": {Type: schema.String, Desc: "API key value (for api_key auth type) or empty for OAuth flow"},
			}),
		},
		Execute: executeMcpAuth,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return executeMcpAuthWithManager(input, MCPManagerFromCtx(ctx))
		},
	}
}

func executeMcpAuth(input string) (string, error) {
	return executeMcpAuthWithManager(input, DefaultMCPManager)
}

func executeMcpAuthWithManager(input string, manager *MCPToolManager) (string, error) {
	var params struct {
		ServerName  string `json:"server_name"`
		AuthType    string `json:"auth_type"`
		Credentials string `json:"credentials"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid McpAuth input: %w", err)
	}
	if params.ServerName == "" {
		return "", fmt.Errorf("server_name is required")
	}
	if params.AuthType != "oauth" && params.AuthType != "api_key" {
		return "", fmt.Errorf("auth_type must be 'oauth' or 'api_key'")
	}

	if manager == nil {
		return "", fmt.Errorf("MCP manager not initialized")
	}

	manager.mu.RLock()
	client, exists := manager.clients[params.ServerName]
	manager.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("MCP server %q not found", params.ServerName)
	}

	switch params.AuthType {
	case "api_key":
		if params.Credentials == "" {
			return "", fmt.Errorf("credentials (API key) required for api_key auth type")
		}
		// Store credential and reconnect
		if err := client.SetAPIKey(params.Credentials); err != nil {
			return "", fmt.Errorf("failed to set API key: %w", err)
		}
		return fmt.Sprintf("API key set for MCP server %q. Tools should now be available.", params.ServerName), nil

	case "oauth":
		// Initiate OAuth flow — returns the authorization URL for the user to visit
		authURL, err := client.InitiateOAuth() //nolint:staticcheck
		if err != nil {                        //nolint:staticcheck // InitiateOAuth is a stub that always errors
			return "", fmt.Errorf("failed to initiate OAuth for %q: %w", params.ServerName, err)
		}
		if authURL == "" { //nolint:staticcheck // InitiateOAuth is a stub; will be meaningful once implemented
			return fmt.Sprintf("OAuth flow initiated for %q but no URL returned. The server may complete auth automatically.", params.ServerName), nil
		}
		return fmt.Sprintf("Please visit this URL to authorize MCP server %q:\n%s\n\nAfter authorization, the server's tools will become available.", params.ServerName, authURL), nil

	default:
		return "", fmt.Errorf("unsupported auth_type: %s", params.AuthType)
	}
}
