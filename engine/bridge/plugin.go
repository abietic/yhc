package bridge

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Plugin is the interface that external components implement to participate in
// the agent's lifecycle through the bridge service layer.
type Plugin interface {
	// Name returns the unique plugin identifier.
	Name() string

	// Version returns the plugin version string.
	Version() string

	// Capabilities returns the list of capabilities this plugin provides.
	Capabilities() []string

	// Dependencies returns the names of other plugins that must be started
	// before this one. The PluginManager respects this ordering during startup.
	Dependencies() []string

	// Start begins the plugin. The provided context carries the configured
	// timeout; plugins should respect context cancellation.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the plugin. The provided context carries
	// the configured timeout; plugins should respect context cancellation.
	Stop(ctx context.Context) error

	// Health returns nil if the plugin is healthy, or an error describing
	// the problem.
	Health() error
}

// PluginContext provides plugins with access to the bridge infrastructure.
// It is passed during plugin initialization (before Start).
type PluginContext struct {
	// Store is the centralized state store for reading/writing state.
	Store *StateStore

	// Logger is a structured logger namespaced to the plugin.
	Logger *log.Logger

	// Config holds plugin-specific configuration (from manifest or programmatic).
	Config map[string]any
}

// HookType identifies the category of hook a plugin wants to register.
type HookType string

const (
	HookTypePreQuery     HookType = "pre_query"
	HookTypePostQuery    HookType = "post_query"
	HookTypePreTool      HookType = "pre_tool"
	HookTypePostTool     HookType = "post_tool"
	HookTypeSessionStart HookType = "session_start"
	HookTypeSessionEnd   HookType = "session_end"
	HookTypeTurnStart    HookType = "turn_start"
	HookTypeTurnEnd      HookType = "turn_end"
)

// PluginHook represents a namespaced hook registration from a plugin.
type PluginHook struct {
	// PluginName is the owning plugin.
	PluginName string

	// Type is the hook category.
	Type HookType

	// Handler is the hook function. The concrete type depends on HookType.
	Handler any
}

// PluginManagerConfig configures the PluginManager.
type PluginManagerConfig struct {
	// StartTimeout is the maximum time to wait for a plugin to start.
	// Defaults to 30 seconds if zero.
	StartTimeout time.Duration

	// StopTimeout is the maximum time to wait for a plugin to stop.
	// Defaults to 10 seconds if zero.
	StopTimeout time.Duration

	// Store is the state store provided to plugins via PluginContext.
	Store *StateStore
}

// PluginManager manages plugin lifecycle: registration, dependency-ordered
// startup, graceful shutdown, panic isolation, and health monitoring.
type PluginManager struct {
	config   PluginManagerConfig
	registry *ServiceRegistry

	mu      sync.RWMutex
	plugins map[string]Plugin        // name -> plugin instance
	order   []string                 // registration order (for reverse shutdown)
	hooks   map[string][]*PluginHook // plugin name -> registered hooks

	started bool
	stopped bool
}

// NewPluginManager creates a new PluginManager with the given configuration.
func NewPluginManager(config PluginManagerConfig) *PluginManager {
	if config.StartTimeout == 0 {
		config.StartTimeout = 30 * time.Second
	}
	if config.StopTimeout == 0 {
		config.StopTimeout = 10 * time.Second
	}

	return &PluginManager{
		config:   config,
		registry: NewServiceRegistry(),
		plugins:  make(map[string]Plugin),
		hooks:    make(map[string][]*PluginHook),
	}
}

// Registry returns the underlying ServiceRegistry.
func (pm *PluginManager) Registry() *ServiceRegistry {
	return pm.registry
}

// Register adds a plugin to the manager. The plugin transitions to
// ServiceStateRegistered. Returns an error if a plugin with the same name
// already exists.
func (pm *PluginManager) Register(p Plugin) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	name := p.Name()
	if _, exists := pm.plugins[name]; exists {
		return fmt.Errorf("plugin %q already registered", name)
	}

	if err := pm.registry.Register(name, p.Version(), p.Capabilities()); err != nil {
		return err
	}

	pm.plugins[name] = p
	pm.order = append(pm.order, name)
	return nil
}

// Unregister removes a plugin from the manager. The plugin must be in a
// terminal state (stopped, registered, or errored).
func (pm *PluginManager) Unregister(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if _, exists := pm.plugins[name]; !exists {
		return fmt.Errorf("plugin %q not registered", name)
	}

	if err := pm.registry.Unregister(name); err != nil {
		return err
	}

	delete(pm.plugins, name)
	delete(pm.hooks, name)

	// Remove from ordering.
	newOrder := make([]string, 0, len(pm.order)-1)
	for _, n := range pm.order {
		if n != name {
			newOrder = append(newOrder, n)
		}
	}
	pm.order = newOrder
	return nil
}

// StartAll starts all registered plugins in dependency order. Plugins whose
// dependencies fail to start are skipped and marked as errored. Panics in
// plugin Start() are recovered and the plugin is marked errored.
func (pm *PluginManager) StartAll(ctx context.Context) error {
	pm.mu.Lock()
	if pm.started {
		pm.mu.Unlock()
		return nil
	}
	pm.started = true
	pm.mu.Unlock()

	ordered, err := pm.resolveDependencyOrder()
	if err != nil {
		return err
	}

	started := make(map[string]bool)

	for _, name := range ordered {
		pm.mu.RLock()
		p, exists := pm.plugins[name]
		pm.mu.RUnlock()
		if !exists {
			continue
		}

		// Check all dependencies have started successfully.
		deps := p.Dependencies()
		depsFailed := false
		for _, dep := range deps {
			if !started[dep] {
				depsFailed = true
				break
			}
		}
		if depsFailed {
			_ = pm.registry.SetError(name, fmt.Errorf("dependency not started"))
			continue
		}

		if err := pm.startPlugin(ctx, name, p); err != nil {
			_ = pm.registry.SetError(name, err)
			continue
		}
		started[name] = true
	}

	return nil
}

// StopAll stops all running plugins in reverse registration order.
// Panics in plugin Stop() are recovered. Returns the first error encountered.
func (pm *PluginManager) StopAll(ctx context.Context) error {
	pm.mu.Lock()
	if pm.stopped {
		pm.mu.Unlock()
		return nil
	}
	pm.stopped = true
	order := make([]string, len(pm.order))
	copy(order, pm.order)
	pm.mu.Unlock()

	var firstErr error

	// Stop in reverse registration order.
	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]

		pm.mu.RLock()
		p, exists := pm.plugins[name]
		pm.mu.RUnlock()
		if !exists {
			continue
		}

		info := pm.registry.Get(name)
		if info == nil || info.State != ServiceStateRunning {
			continue
		}

		if err := pm.stopPlugin(ctx, name, p); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// StartPlugin starts a single plugin by name. Returns an error if the plugin
// is not registered or cannot be started.
func (pm *PluginManager) StartPlugin(ctx context.Context, name string) error {
	pm.mu.RLock()
	p, exists := pm.plugins[name]
	pm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("plugin %q not registered", name)
	}

	return pm.startPlugin(ctx, name, p)
}

// StopPlugin stops a single plugin by name. Returns an error if the plugin
// is not registered or cannot be stopped.
func (pm *PluginManager) StopPlugin(ctx context.Context, name string) error {
	pm.mu.RLock()
	p, exists := pm.plugins[name]
	pm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("plugin %q not registered", name)
	}

	return pm.stopPlugin(ctx, name, p)
}

// startPlugin handles the actual start of a single plugin with panic recovery
// and timeout management.
func (pm *PluginManager) startPlugin(ctx context.Context, name string, p Plugin) (retErr error) {
	// Set state to starting.
	_ = pm.registry.SetState(name, ServiceStateStarting)

	// Recover from panics in plugin code.
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("plugin %q panicked during start: %v", name, r)
			_ = pm.registry.SetError(name, retErr)
		}
	}()

	startCtx, cancel := context.WithTimeout(ctx, pm.config.StartTimeout)
	defer cancel()

	if err := p.Start(startCtx); err != nil {
		_ = pm.registry.SetError(name, err)
		return fmt.Errorf("plugin %q failed to start: %w", name, err)
	}

	_ = pm.registry.SetState(name, ServiceStateRunning)
	return nil
}

// stopPlugin handles the actual stop of a single plugin with panic recovery
// and timeout management.
func (pm *PluginManager) stopPlugin(ctx context.Context, name string, p Plugin) (retErr error) {
	_ = pm.registry.SetState(name, ServiceStateStopping)

	// Recover from panics in plugin code.
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("plugin %q panicked during stop: %v", name, r)
			_ = pm.registry.SetError(name, retErr)
		}
	}()

	stopCtx, cancel := context.WithTimeout(ctx, pm.config.StopTimeout)
	defer cancel()

	if err := p.Stop(stopCtx); err != nil {
		_ = pm.registry.SetError(name, err)
		return fmt.Errorf("plugin %q failed to stop: %w", name, err)
	}

	_ = pm.registry.SetState(name, ServiceStateStopped)
	return nil
}

// HealthCheck runs Health() on all running plugins. Returns a map of plugin
// names to their health status (nil = healthy, non-nil = error description).
func (pm *PluginManager) HealthCheck() map[string]error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	results := make(map[string]error)
	for name, p := range pm.plugins {
		info := pm.registry.Get(name)
		if info == nil || info.State != ServiceStateRunning {
			continue
		}
		results[name] = pm.safeHealth(p)
	}
	return results
}

// safeHealth calls Health() with panic recovery.
func (pm *PluginManager) safeHealth(p Plugin) (result error) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Errorf("panic in health check: %v", r)
		}
	}()
	return p.Health()
}

// RegisterHook registers a namespaced hook from a plugin.
func (pm *PluginManager) RegisterHook(pluginName string, hookType HookType, handler any) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if _, exists := pm.plugins[pluginName]; !exists {
		return fmt.Errorf("plugin %q not registered", pluginName)
	}

	hook := &PluginHook{
		PluginName: pluginName,
		Type:       hookType,
		Handler:    handler,
	}
	pm.hooks[pluginName] = append(pm.hooks[pluginName], hook)
	return nil
}

// Hooks returns all registered hooks of the given type across all plugins.
func (pm *PluginManager) Hooks(hookType HookType) []*PluginHook {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var result []*PluginHook
	for _, hooks := range pm.hooks {
		for _, h := range hooks {
			if h.Type == hookType {
				result = append(result, h)
			}
		}
	}
	return result
}

// HooksForPlugin returns all hooks registered by a specific plugin.
func (pm *PluginManager) HooksForPlugin(pluginName string) []*PluginHook {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	hooks := pm.hooks[pluginName]
	result := make([]*PluginHook, len(hooks))
	copy(result, hooks)
	return result
}

// resolveDependencyOrder returns plugin names in topological order respecting
// declared dependencies. Returns an error if a dependency cycle is detected.
func (pm *PluginManager) resolveDependencyOrder() ([]string, error) {
	// Build adjacency: plugin -> its dependencies.
	graph := make(map[string][]string)
	for name, p := range pm.plugins {
		graph[name] = p.Dependencies()
	}

	// Kahn's algorithm for topological sort.
	inDegree := make(map[string]int)
	for name := range graph {
		if _, ok := inDegree[name]; !ok {
			inDegree[name] = 0
		}
		for _, dep := range graph[name] {
			// Only count in-degree for known plugins.
			if _, exists := pm.plugins[dep]; exists {
				if _, exists := inDegree[dep]; !exists {
					inDegree[dep] = 0
				}
			}
		}
	}
	// Recompute: for each node, its in-degree is the number of other nodes
	// that depend on it (i.e., it appears as a dependency).
	// Actually for topological sort we want: inDegree[node] = number of
	// dependencies of node that are in the graph.
	inDegree = make(map[string]int)
	for name := range graph {
		inDegree[name] = 0
	}
	for name, deps := range graph {
		count := 0
		for _, dep := range deps {
			if _, exists := pm.plugins[dep]; exists {
				count++
			}
		}
		inDegree[name] = count
	}

	// Queue starts with nodes that have no dependencies (in-degree 0).
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	// Build reverse adjacency: dep -> list of plugins that depend on dep.
	reverseDeps := make(map[string][]string)
	for name, deps := range graph {
		for _, dep := range deps {
			if _, exists := pm.plugins[dep]; exists {
				reverseDeps[dep] = append(reverseDeps[dep], name)
			}
		}
	}

	var sorted []string
	for len(queue) > 0 {
		// Pop from queue.
		node := queue[0]
		queue = queue[1:]
		sorted = append(sorted, node)

		// For each plugin that depends on this node, decrement its in-degree.
		for _, dependent := range reverseDeps[node] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(sorted) != len(graph) {
		return nil, fmt.Errorf("dependency cycle detected among plugins")
	}

	return sorted, nil
}

// GetPlugin returns the plugin instance by name, or nil if not registered.
func (pm *PluginManager) GetPlugin(name string) Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.plugins[name]
}

// PluginNames returns the names of all registered plugins in registration order.
func (pm *PluginManager) PluginNames() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]string, len(pm.order))
	copy(result, pm.order)
	return result
}
