package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/mcp"
)

const sessionMCPLaunchConcurrency = 4

var mcpManagerSequence atomic.Uint64

var (
	errMCPToolIdentityInvalid = errors.New("MCP tool registration identity is invalid")
	errMCPToolNameCollision   = errors.New("MCP tool registration identity collides")
)

type mcpRegistryHookDispatches []func()

func (d *mcpRegistryHookDispatches) add(dispatch func()) {
	if dispatch != nil {
		*d = append(*d, dispatch)
	}
}

func (d mcpRegistryHookDispatches) run() {
	for _, dispatch := range d {
		dispatch()
	}
}

// SessionMCPServer is one already-validated, transient ACP stdio descriptor.
// DescriptorIndex is the only request identity admitted into setup errors.
type SessionMCPServer struct {
	DescriptorIndex int
	Name            string
	Config          mcp.ServerConfig
}

// SessionMCPSetupError is deliberately bounded so command, arguments,
// environment values, and project configuration facts cannot reach ACP errors.
type SessionMCPSetupError struct {
	DescriptorIndex int
	Reason          string
}

func (e *SessionMCPSetupError) Error() string {
	if e == nil {
		return "session MCP setup failed"
	}
	return fmt.Sprintf(
		"session MCP descriptor %d failed: %s",
		e.DescriptorIndex,
		e.Reason,
	)
}

func nextMCPManagerID() uint64 {
	return mcpManagerSequence.Add(1)
}

func mcpToolMapKey(serverName, toolName string) string {
	return serverName + "\x00" + toolName
}

func mcpRegisteredToolName(serverName, toolName string) string {
	return fmt.Sprintf(
		"mcp__%s__%s",
		mcp.NormalizeNameForMCP(serverName),
		mcp.NormalizeNameForMCP(toolName),
	)
}

func (m *MCPToolManager) nextServerIdentity() (uint64, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverOwnerSequence++
	m.serverGenerationSequence++
	return m.serverGenerationSequence, fmt.Sprintf(
		"mcp-manager-%d-generation-%d",
		m.managerID,
		m.serverOwnerSequence,
	)
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func cloneMCPServerConfig(config mcp.ServerConfig) mcp.ServerConfig {
	cloned := config
	cloned.Args = append([]string(nil), config.Args...)
	if config.Env != nil {
		cloned.Env = make(map[string]string, len(config.Env))
		for name, value := range config.Env {
			cloned.Env[name] = value
		}
	}
	if config.Headers != nil {
		cloned.Headers = make(map[string]string, len(config.Headers))
		for name, value := range config.Headers {
			cloned.Headers[name] = value
		}
	}
	cloned.ExecutionBinding = config.ExecutionBinding
	return cloned
}

// PrepareSessionMCPManagerWithBinding initializes session MCP before engine
// construction with an explicit stdio binding. The legacy variadic policy API
// remains available for embedded callers.
func PrepareSessionMCPManagerWithBinding(ctx context.Context, projectDir string, registry *Registry, servers []SessionMCPServer, binding *containment.Binding) (*MCPToolManager, error) {
	if !validMCPExecutionBinding(binding) {
		return nil, &SessionMCPSetupError{DescriptorIndex: 0, Reason: "execution_binding_invalid"}
	}
	return prepareSessionMCPManager(ctx, projectDir, registry, servers, binding.Policy(), binding)
}

type mcpServerGeneration struct {
	name             string
	config           mcp.ServerConfig
	client           *mcp.MCPClient
	generation       uint64
	clientGeneration uint64
	owner            string
	infos            []*MCPToolInfo
	implementations  []ToolImpl
}

func newMCPServerGeneration(
	name string,
	config mcp.ServerConfig,
	client *mcp.MCPClient,
	discovered []mcp.MCPTool,
	generation uint64,
	owner string,
) (*mcpServerGeneration, error) {
	if client == nil || generation == 0 || owner == "" {
		return nil, errMCPToolIdentityInvalid
	}
	target, err := client.BindToolCallTarget()
	if err != nil {
		return nil, err
	}
	infos := make([]*MCPToolInfo, 0, len(discovered))
	for _, tool := range discovered {
		infos = append(infos, &MCPToolInfo{
			ServerName:  name,
			ToolName:    tool.Name,
			Description: tool.Description,
			InputSchema: cloneMCPInputSchema(tool.InputSchema),
		})
	}
	if err := validateMCPToolInfos(infos); err != nil {
		return nil, err
	}
	implementations := make([]ToolImpl, 0, len(infos))
	for _, info := range infos {
		implementations = append(
			implementations,
			registeredMCPToolImpl(target, info, owner),
		)
	}
	return &mcpServerGeneration{
		name:             name,
		config:           cloneMCPServerConfig(config),
		client:           client,
		generation:       generation,
		clientGeneration: target.Generation(),
		owner:            owner,
		infos:            infos,
		implementations:  implementations,
	}, nil
}

type preparedSessionMCPClient struct {
	spec   SessionMCPServer
	client *mcp.MCPClient
	tools  []mcp.MCPTool
}

// PrepareSessionMCPManager creates one unpublished combined project/client
// manager, prepares all client descriptors with bounded concurrency, and
// publishes one collision-free registry generation. The caller owns the
// returned manager and must DisconnectAll on abort.
func PrepareSessionMCPManager(
	ctx context.Context,
	projectDir string,
	registry *Registry,
	servers []SessionMCPServer,
	policies ...*containment.Snapshot,
) (*MCPToolManager, error) {
	var policy *containment.Snapshot
	if len(policies) > 0 {
		policy = policies[0]
	}
	return prepareSessionMCPManager(ctx, projectDir, registry, servers, policy, nil)
}

func prepareSessionMCPManager(ctx context.Context, projectDir string, registry *Registry, servers []SessionMCPServer, requestedPolicy *containment.Snapshot, binding *containment.Binding) (*MCPToolManager, error) {
	if len(servers) == 0 {
		return nil, errors.New("session MCP setup requires at least one descriptor")
	}
	if registry == nil {
		return nil, &SessionMCPSetupError{
			DescriptorIndex: servers[0].DescriptorIndex,
			Reason:          "registry_unavailable",
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	projectConfig, err := mcp.LoadMCPConfig(projectDir)
	if err != nil {
		return nil, &SessionMCPSetupError{
			DescriptorIndex: servers[0].DescriptorIndex,
			Reason:          "project_config_invalid",
		}
	}
	if conflict := sessionMCPSourceConflict(projectConfig, servers); conflict >= 0 {
		return nil, &SessionMCPSetupError{
			DescriptorIndex: conflict,
			Reason:          "server_name_conflict",
		}
	}

	policy := containment.DisabledCompatibilitySnapshot(projectDir, containment.EntrypointEmbedded)
	if requestedPolicy != nil {
		policy = requestedPolicy
	}
	manager := NewMCPToolManager()
	if err := manager.BindExecutionPolicy(policy); err != nil {
		return nil, &SessionMCPSetupError{
			DescriptorIndex: servers[0].DescriptorIndex,
			Reason:          "execution_policy_invalid",
		}
	}
	if binding != nil {
		if err := manager.BindExecutionBinding(binding); err != nil {
			return nil, &SessionMCPSetupError{DescriptorIndex: servers[0].DescriptorIndex, Reason: "execution_binding_invalid"}
		}
	}
	boundServers := make([]SessionMCPServer, len(servers))
	for index := range servers {
		boundServers[index] = cloneSessionMCPServer(servers[index])
		boundServers[index].Config.ExecutionPolicy = policy
		boundServers[index].Config.ExecutionBinding = binding
	}
	servers = boundServers
	expectedGeneration := registry.Generation()
	if projectConfig != nil {
		projectNames := make([]string, 0, len(projectConfig.EnabledServers()))
		for name := range projectConfig.EnabledServers() {
			projectNames = append(projectNames, name)
		}
		sort.Strings(projectNames)
		for _, name := range projectNames {
			config := projectConfig.EnabledServers()[name].ToServerConfig()
			// Generic project configuration retains its tolerant failure policy.
			_ = manager.ConnectServer(ctx, name, &config)
		}
	}

	prepared, setupErr := prepareSessionMCPClients(ctx, servers)
	if setupErr != nil {
		_ = manager.DisconnectAll()
		return nil, setupErr
	}

	sessionGenerations := make([]*mcpServerGeneration, 0, len(prepared))
	for _, result := range prepared {
		managerGeneration, owner := manager.nextServerIdentity()
		generation, buildErr := newMCPServerGeneration(
			result.spec.Name,
			result.spec.Config,
			result.client,
			result.tools,
			managerGeneration,
			owner,
		)
		if buildErr != nil {
			_ = manager.DisconnectAll()
			disconnectPreparedSessionClients(prepared)
			reason := "tool_name_invalid"
			if errors.Is(buildErr, errMCPToolNameCollision) {
				reason = "tool_name_collision"
			}
			return nil, &SessionMCPSetupError{
				DescriptorIndex: result.spec.DescriptorIndex,
				Reason:          reason,
			}
		}
		manager.installGenerationCallbacks(generation)
		sessionGenerations = append(sessionGenerations, generation)
	}

	manager.lifecycleMu.Lock()
	additions, buildErr := manager.registryToolImplementations()
	if buildErr != nil {
		manager.lifecycleMu.Unlock()
		_ = manager.DisconnectAll()
		disconnectPreparedSessionClients(prepared)
		reason := "tool_name_invalid"
		if errors.Is(buildErr, errMCPToolNameCollision) {
			reason = "tool_name_collision"
		}
		return nil, &SessionMCPSetupError{
			DescriptorIndex: servers[0].DescriptorIndex,
			Reason:          reason,
		}
	}
	for _, generation := range sessionGenerations {
		if !generation.client.IsConnected() {
			manager.lifecycleMu.Unlock()
			_ = manager.DisconnectAll()
			disconnectPreparedSessionClients(prepared)
			return nil, &SessionMCPSetupError{
				DescriptorIndex: sessionDescriptorIndex(servers, generation.name),
				Reason:          "connection_closed",
			}
		}
		additions = append(additions, generation.implementations...)
	}
	_, dispatchHooks, err := registry.replaceOwnedToolsDeferred(
		expectedGeneration,
		nil,
		additions,
	)
	if err != nil {
		manager.lifecycleMu.Unlock()
		_ = manager.DisconnectAll()
		disconnectPreparedSessionClients(prepared)
		reason := "registry_changed"
		if errors.Is(err, ErrRegistryNameCollision) {
			reason = "tool_name_collision"
		}
		return nil, &SessionMCPSetupError{
			DescriptorIndex: servers[0].DescriptorIndex,
			Reason:          reason,
		}
	}
	manager.mu.Lock()
	manager.registry = registry
	for index, generation := range sessionGenerations {
		spec := cloneSessionMCPServer(prepared[index].spec)
		manager.clients[generation.name] = generation.client
		manager.serverOwners[generation.name] = generation.owner
		manager.serverGenerations[generation.name] = generation.generation
		manager.serverClientGenerations[generation.name] = generation.clientGeneration
		manager.serverConfigs[generation.name] = cloneMCPServerConfig(generation.config)
		manager.sessionServers[generation.name] = spec
		delete(manager.failures, generation.name)
		for _, info := range generation.infos {
			manager.tools[mcpToolMapKey(generation.name, info.ToolName)] = info
		}
	}
	manager.revision++
	manager.mu.Unlock()
	manager.lifecycleMu.Unlock()
	if dispatchHooks != nil {
		dispatchHooks()
	}
	return manager, nil
}

func sessionDescriptorIndex(servers []SessionMCPServer, name string) int {
	for _, server := range servers {
		if server.Name == name {
			return server.DescriptorIndex
		}
	}
	if len(servers) == 0 {
		return 0
	}
	return servers[0].DescriptorIndex
}

func sessionMCPSourceConflict(
	projectConfig *mcp.MCPConfig,
	servers []SessionMCPServer,
) int {
	raw := make(map[string]struct{})
	normalized := make(map[string]struct{})
	if projectConfig != nil {
		for name := range projectConfig.EnabledServers() {
			raw[name] = struct{}{}
			normalized[mcp.NormalizeNameForMCP(name)] = struct{}{}
		}
	}
	for _, server := range servers {
		if _, exists := raw[server.Name]; exists {
			return server.DescriptorIndex
		}
		if _, exists := normalized[mcp.NormalizeNameForMCP(server.Name)]; exists {
			return server.DescriptorIndex
		}
		raw[server.Name] = struct{}{}
		normalized[mcp.NormalizeNameForMCP(server.Name)] = struct{}{}
	}
	return -1
}

func prepareSessionMCPClients(
	ctx context.Context,
	servers []SessionMCPServer,
) ([]preparedSessionMCPClient, error) {
	results := make([]preparedSessionMCPClient, len(servers))
	reasons := make([]string, len(servers))
	launchSlots := make(chan struct{}, sessionMCPLaunchConcurrency)
	var wait sync.WaitGroup
	for index := range servers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case launchSlots <- struct{}{}:
				defer func() { <-launchSlots }()
			case <-ctx.Done():
				reasons[index] = sessionMCPContextReason(ctx)
				return
			}
			if err := ctx.Err(); err != nil {
				reasons[index] = sessionMCPContextReason(ctx)
				return
			}
			spec := cloneSessionMCPServer(servers[index])
			client := mcp.NewMCPClient(spec.Config)
			if err := client.Connect(ctx); err != nil {
				reasons[index] = sessionMCPConnectReason(ctx)
				return
			}
			tools, err := client.ListTools(ctx)
			if err != nil {
				_ = client.Disconnect()
				reasons[index] = sessionMCPDiscoveryReason(ctx)
				return
			}
			results[index] = preparedSessionMCPClient{
				spec:   spec,
				client: client,
				tools:  tools,
			}
		}()
	}
	wait.Wait()

	for index, reason := range reasons {
		if reason == "" {
			continue
		}
		for _, result := range results {
			if result.client != nil {
				_ = result.client.Disconnect()
			}
		}
		return nil, &SessionMCPSetupError{
			DescriptorIndex: servers[index].DescriptorIndex,
			Reason:          reason,
		}
	}
	return results, nil
}

func sessionMCPContextReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "setup_timeout"
	}
	return "setup_canceled"
}

func sessionMCPConnectReason(ctx context.Context) string {
	if ctx.Err() != nil {
		return sessionMCPContextReason(ctx)
	}
	return "connect_failed"
}

func sessionMCPDiscoveryReason(ctx context.Context) string {
	if ctx.Err() != nil {
		return sessionMCPContextReason(ctx)
	}
	return "tool_discovery_failed"
}

func cloneSessionMCPServer(server SessionMCPServer) SessionMCPServer {
	cloned := server
	cloned.Config = cloneMCPServerConfig(server.Config)
	return cloned
}

func (m *MCPToolManager) installGenerationCallbacks(
	generation *mcpServerGeneration,
) {
	if m == nil || generation == nil || generation.client == nil {
		return
	}
	generation.client.SetOnToolsChangedWithGeneration(func(observed uint64) {
		if observed != generation.clientGeneration {
			return
		}
		m.refreshServerToolsGeneration(
			generation.name,
			generation.client,
			generation.generation,
			generation.owner,
		)
	})
	generation.client.SetOnCloseWithGeneration(func(observed uint64) {
		if observed != generation.clientGeneration {
			return
		}
		m.handleServerCloseGeneration(
			generation.name,
			generation.client,
			generation.generation,
			generation.owner,
		)
	})
}

func (m *MCPToolManager) handleServerCloseGeneration(
	serverName string,
	client *mcp.MCPClient,
	generation uint64,
	owner string,
) {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	m.mu.RLock()
	current := m.clients[serverName] == client &&
		m.serverGenerations[serverName] == generation &&
		m.serverOwners[serverName] == owner
	registry := m.registry
	m.mu.RUnlock()
	if !current {
		return
	}
	if registry != nil {
		_, dispatchHooks := registry.removeOwnedToolsDeferred(owner)
		hookDispatches.add(dispatchHooks)
	}
	m.clearServerGeneration(
		serverName,
		client,
		owner,
		"connection_closed",
	)
}

func (m *MCPToolManager) clearServerGeneration(
	serverName string,
	client *mcp.MCPClient,
	owner string,
	failure string,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if client != nil && m.clients[serverName] != client {
		return false
	}
	if owner != "" && m.serverOwners[serverName] != owner {
		return false
	}
	for key, info := range m.tools {
		if info.ServerName == serverName {
			delete(m.tools, key)
		}
	}
	delete(m.clients, serverName)
	delete(m.serverOwners, serverName)
	delete(m.serverGenerations, serverName)
	delete(m.serverClientGenerations, serverName)
	if m.failures == nil {
		m.failures = make(map[string]string)
	}
	if failure == "" {
		delete(m.failures, serverName)
	} else {
		m.failures[serverName] = failure
	}
	m.revision++
	return true
}

func (m *MCPToolManager) registryToolImplementations() ([]ToolImpl, error) {
	m.mu.RLock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	type snapshot struct {
		name             string
		client           *mcp.MCPClient
		generation       uint64
		clientGeneration uint64
		owner            string
		infos            []*MCPToolInfo
	}
	snapshots := make([]snapshot, 0, len(names))
	for _, name := range names {
		item := snapshot{
			name:             name,
			client:           m.clients[name],
			generation:       m.serverGenerations[name],
			clientGeneration: m.serverClientGenerations[name],
			owner:            m.serverOwners[name],
		}
		for _, info := range m.tools {
			if info.ServerName != name {
				continue
			}
			cloned := *info
			cloned.InputSchema = cloneMCPInputSchema(info.InputSchema)
			item.infos = append(item.infos, &cloned)
		}
		snapshots = append(snapshots, item)
	}
	m.mu.RUnlock()

	additions := make([]ToolImpl, 0)
	for _, item := range snapshots {
		if item.client == nil || item.generation == 0 || item.owner == "" {
			return nil, errMCPToolIdentityInvalid
		}
		target, err := item.client.BindToolCallTarget()
		if err != nil || target.Generation() != item.clientGeneration {
			return nil, errMCPToolIdentityInvalid
		}
		if err := validateMCPToolInfos(item.infos); err != nil {
			return nil, err
		}
		sort.Slice(item.infos, func(i, j int) bool {
			return mcpRegisteredToolName(
				item.infos[i].ServerName,
				item.infos[i].ToolName,
			) < mcpRegisteredToolName(
				item.infos[j].ServerName,
				item.infos[j].ToolName,
			)
		})
		for _, info := range item.infos {
			additions = append(
				additions,
				registeredMCPToolImpl(target, info, item.owner),
			)
		}
	}
	return additions, nil
}

func validateMCPToolInfos(infos []*MCPToolInfo) error {
	names := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		if info == nil ||
			info.ServerName == "" ||
			info.ToolName == "" ||
			mcp.NormalizeNameForMCP(info.ToolName) == "" {
			return errMCPToolIdentityInvalid
		}
		name := mcpRegisteredToolName(info.ServerName, info.ToolName)
		if _, duplicate := names[name]; duplicate {
			return errMCPToolNameCollision
		}
		names[name] = struct{}{}
	}
	return nil
}

func registeredMCPToolImpl(
	target *mcp.MCPToolCallTarget,
	toolInfo *MCPToolInfo,
	owner string,
) ToolImpl {
	registeredName := mcpRegisteredToolName(
		toolInfo.ServerName,
		toolInfo.ToolName,
	)
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: registeredName,
			Desc: toolInfo.Description,
		},
		Execute: func(input string) (string, error) {
			return executeRegisteredMCPTool(
				context.Background(),
				input,
				registeredName,
				toolInfo,
				target,
			)
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return executeRegisteredMCPTool(
				ctx,
				input,
				registeredName,
				toolInfo,
				target,
			)
		},
		IsConcurrencySafe: func(map[string]any) bool {
			return true
		},
		Capabilities: ToolCapabilities{
			Declared:   true,
			Origin:     ToolOriginMCP,
			ActionKind: ToolActionDynamic,
			Network:    true,
			Dynamic:    true,
		},
		RegistrationOwner: owner,
	}
	if toolInfo.InputSchema == nil {
		return impl
	}
	properties, ok := toolInfo.InputSchema["properties"].(map[string]any)
	if !ok {
		return impl
	}
	params := make(map[string]*schema.ParameterInfo)
	for name, value := range properties {
		parameter := &schema.ParameterInfo{}
		if definition, ok := value.(map[string]any); ok {
			if description, ok := definition["description"].(string); ok {
				parameter.Desc = description
			}
			switch definition["type"] {
			case "string":
				parameter.Type = schema.String
			case "number":
				parameter.Type = schema.Number
			case "integer":
				parameter.Type = schema.Integer
			case "boolean":
				parameter.Type = schema.Boolean
			case "array":
				parameter.Type = schema.Array
			case "object":
				parameter.Type = schema.Object
			}
		}
		params[name] = parameter
	}
	if required, ok := toolInfo.InputSchema["required"].([]any); ok {
		for _, value := range required {
			name, ok := value.(string)
			if !ok {
				continue
			}
			if parameter := params[name]; parameter != nil {
				parameter.Required = true
			}
		}
	}
	impl.Info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
	return impl
}

// EnsureSessionServers reconnects only missing members of the exact
// process-local descriptor set. Every new child is prepared before one atomic
// registry publication, so a failed reconnect cannot expose a partial set.
func (m *MCPToolManager) EnsureSessionServers(ctx context.Context) error {
	if m == nil {
		return errors.New("session MCP manager is unavailable")
	}
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	m.mu.RLock()
	var missing []SessionMCPServer
	for name, spec := range m.sessionServers {
		client := m.clients[name]
		if client == nil || !client.IsConnected() {
			missing = append(missing, cloneSessionMCPServer(spec))
		}
	}
	registry := m.registry
	m.mu.RUnlock()
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i].DescriptorIndex < missing[j].DescriptorIndex
	})
	if registry == nil {
		return &SessionMCPSetupError{
			DescriptorIndex: missing[0].DescriptorIndex,
			Reason:          "registry_unavailable",
		}
	}

	prepared, err := prepareSessionMCPClients(ctx, missing)
	if err != nil {
		return err
	}
	generations := make([]*mcpServerGeneration, 0, len(prepared))
	for _, result := range prepared {
		managerGeneration, owner := m.nextServerIdentity()
		generation, buildErr := newMCPServerGeneration(
			result.spec.Name,
			result.spec.Config,
			result.client,
			result.tools,
			managerGeneration,
			owner,
		)
		if buildErr != nil {
			disconnectPreparedSessionClients(prepared)
			reason := "tool_name_invalid"
			if errors.Is(buildErr, errMCPToolNameCollision) {
				reason = "tool_name_collision"
			}
			return &SessionMCPSetupError{
				DescriptorIndex: result.spec.DescriptorIndex,
				Reason:          reason,
			}
		}
		m.installGenerationCallbacks(generation)
		generations = append(generations, generation)
	}
	expectedGeneration := registry.Generation()
	m.mu.RLock()
	beforePublish := m.beforeSessionReconnectForTest
	m.mu.RUnlock()
	if beforePublish != nil {
		clients := make([]*mcp.MCPClient, 0, len(prepared))
		for _, result := range prepared {
			clients = append(clients, result.client)
		}
		beforePublish(clients)
	}

	additions := make([]ToolImpl, 0)
	retiredOwners := make([]string, 0, len(generations))
	retiredClients := make([]*mcp.MCPClient, 0, len(generations))
	m.mu.RLock()
	for _, generation := range generations {
		if retiredOwner := m.serverOwners[generation.name]; retiredOwner != "" {
			retiredOwners = append(retiredOwners, retiredOwner)
		}
		if retiredClient := m.clients[generation.name]; retiredClient != nil {
			retiredClients = append(retiredClients, retiredClient)
		}
	}
	m.mu.RUnlock()
	for _, generation := range generations {
		if generation.client.IsConnected() {
			additions = append(additions, generation.implementations...)
			continue
		}
		_, dispatchHooks := registry.removeOwnedToolsDeferred(retiredOwners...)
		hookDispatches.add(dispatchHooks)
		m.failSessionReconnect(generations, "connection_closed")
		disconnectPreparedSessionClients(prepared)
		for _, retiredClient := range retiredClients {
			disconnectMCPClientAsync(retiredClient)
		}
		return &SessionMCPSetupError{
			DescriptorIndex: sessionDescriptorIndex(missing, generation.name),
			Reason:          "connection_closed",
		}
	}
	_, dispatchHooks, replaceErr := registry.replaceOwnedToolsDeferred(
		expectedGeneration,
		retiredOwners,
		additions,
	)
	hookDispatches.add(dispatchHooks)
	if replaceErr != nil {
		_, dispatchHooks := registry.removeOwnedToolsDeferred(retiredOwners...)
		hookDispatches.add(dispatchHooks)
		m.failSessionReconnect(generations, "reconnect_failed")
		disconnectPreparedSessionClients(prepared)
		for _, retiredClient := range retiredClients {
			disconnectMCPClientAsync(retiredClient)
		}
		reason := "registry_changed"
		if errors.Is(replaceErr, ErrRegistryNameCollision) {
			reason = "tool_name_collision"
		}
		return &SessionMCPSetupError{
			DescriptorIndex: missing[0].DescriptorIndex,
			Reason:          reason,
		}
	}
	m.mu.Lock()
	for _, generation := range generations {
		for key, info := range m.tools {
			if info.ServerName == generation.name {
				delete(m.tools, key)
			}
		}
		m.clients[generation.name] = generation.client
		m.serverOwners[generation.name] = generation.owner
		m.serverGenerations[generation.name] = generation.generation
		m.serverClientGenerations[generation.name] = generation.clientGeneration
		m.serverConfigs[generation.name] = cloneMCPServerConfig(generation.config)
		delete(m.failures, generation.name)
		for _, info := range generation.infos {
			m.tools[mcpToolMapKey(generation.name, info.ToolName)] = info
		}
	}
	m.revision++
	m.mu.Unlock()
	for _, retiredClient := range retiredClients {
		disconnectMCPClientAsync(retiredClient)
	}
	return nil
}

func (m *MCPToolManager) failSessionReconnect(
	generations []*mcpServerGeneration,
	failure string,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, generation := range generations {
		for key, info := range m.tools {
			if info.ServerName == generation.name {
				delete(m.tools, key)
			}
		}
		delete(m.clients, generation.name)
		delete(m.serverOwners, generation.name)
		delete(m.serverGenerations, generation.name)
		delete(m.serverClientGenerations, generation.name)
		m.failures[generation.name] = failure
	}
	m.revision++
}

func disconnectPreparedSessionClients(prepared []preparedSessionMCPClient) {
	for _, result := range prepared {
		_ = result.client.Disconnect()
	}
}

func (m *MCPToolManager) refreshServerToolsGeneration(
	name string,
	client *mcp.MCPClient,
	generation uint64,
	owner string,
) {
	if m == nil || client == nil {
		return
	}
	m.lifecycleMu.Lock()
	var hookDispatches mcpRegistryHookDispatches
	defer func() {
		hookDispatches.run()
	}()
	defer m.lifecycleMu.Unlock()

	m.mu.RLock()
	current := m.clients[name] == client &&
		m.serverGenerations[name] == generation &&
		m.serverOwners[name] == owner
	registry := m.registry
	config := cloneMCPServerConfig(m.serverConfigs[name])
	beforePublish := m.beforeOwnedRefreshPublishForTest
	m.mu.RUnlock()
	if !current {
		return
	}

	discovered, err := client.ListTools(context.Background())
	if err != nil {
		hookDispatches.add(m.failOwnedServerGeneration(
			name,
			client,
			generation,
			owner,
			"tool_refresh_failed",
		))
		return
	}
	candidate, err := newMCPServerGeneration(
		name,
		config,
		client,
		discovered,
		generation,
		owner,
	)
	if err != nil || candidate.generation != generation {
		hookDispatches.add(m.failOwnedServerGeneration(
			name,
			client,
			generation,
			owner,
			"tool_refresh_failed",
		))
		return
	}
	var expectedGeneration uint64
	if registry != nil {
		expectedGeneration = registry.Generation()
	}
	if beforePublish != nil {
		beforePublish()
	}

	m.mu.RLock()
	current = m.clients[name] == client &&
		m.serverGenerations[name] == generation &&
		m.serverOwners[name] == owner
	m.mu.RUnlock()
	if !current {
		return
	}
	if registry != nil {
		_, dispatchHooks, err := registry.replaceOwnedToolsDeferred(
			expectedGeneration,
			[]string{owner},
			candidate.implementations,
		)
		hookDispatches.add(dispatchHooks)
		if err != nil {
			hookDispatches.add(m.failOwnedServerGeneration(
				name,
				client,
				generation,
				owner,
				"tool_refresh_failed",
			))
			return
		}
	}

	m.mu.Lock()
	if m.clients[name] != client ||
		m.serverGenerations[name] != generation ||
		m.serverOwners[name] != owner {
		m.mu.Unlock()
		if registry != nil {
			_, dispatchHooks := registry.removeOwnedToolsDeferred(owner)
			hookDispatches.add(dispatchHooks)
		}
		return
	}
	for key, info := range m.tools {
		if info.ServerName == name {
			delete(m.tools, key)
		}
	}
	for _, info := range candidate.infos {
		m.tools[mcpToolMapKey(name, info.ToolName)] = info
	}
	delete(m.failures, name)
	m.revision++
	m.mu.Unlock()
}

func (m *MCPToolManager) failOwnedServerGeneration(
	name string,
	client *mcp.MCPClient,
	generation uint64,
	owner string,
	category string,
) func() {
	m.mu.RLock()
	current := m.clients[name] == client &&
		m.serverGenerations[name] == generation &&
		m.serverOwners[name] == owner
	registry := m.registry
	m.mu.RUnlock()
	if !current {
		return nil
	}
	var dispatchHooks func()
	if registry != nil {
		_, dispatchHooks = registry.removeOwnedToolsDeferred(owner)
	}
	if m.clearServerGeneration(name, client, owner, category) {
		disconnectMCPClientAsync(client)
	}
	return dispatchHooks
}

func disconnectMCPClientAsync(client *mcp.MCPClient) {
	if client == nil {
		return
	}
	go func() {
		_ = client.Disconnect()
	}()
}
