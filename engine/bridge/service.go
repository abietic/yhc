package bridge

import (
	"fmt"
	"sync"
)

// ServiceState represents the lifecycle state of a registered service.
type ServiceState string

const (
	ServiceStateRegistered ServiceState = "registered"
	ServiceStateStarting   ServiceState = "starting"
	ServiceStateRunning    ServiceState = "running"
	ServiceStateStopping   ServiceState = "stopping"
	ServiceStateStopped    ServiceState = "stopped"
	ServiceStateErrored    ServiceState = "errored"
)

// ServiceInfo holds metadata about a registered service.
type ServiceInfo struct {
	// Name uniquely identifies this service.
	Name string

	// Version is the service version string.
	Version string

	// Capabilities declares what this service provides.
	Capabilities []string

	// State is the current lifecycle state.
	State ServiceState

	// Error holds the last error if state is ServiceStateErrored.
	Error error
}

// ServiceRegistry manages named services (plugins, extensions, integrations).
// It is thread-safe and supports dynamic registration/unregistration,
// as well as querying by name or capability.
type ServiceRegistry struct {
	mu       sync.RWMutex
	services map[string]*ServiceInfo
}

// NewServiceRegistry creates a new empty ServiceRegistry.
func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{
		services: make(map[string]*ServiceInfo),
	}
}

// Register adds a service to the registry. Returns an error if a service with
// the same name is already registered.
func (r *ServiceRegistry) Register(name, version string, capabilities []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.services[name]; exists {
		return fmt.Errorf("service %q already registered", name)
	}

	caps := make([]string, len(capabilities))
	copy(caps, capabilities)

	r.services[name] = &ServiceInfo{
		Name:         name,
		Version:      version,
		Capabilities: caps,
		State:        ServiceStateRegistered,
	}
	return nil
}

// Unregister removes a service from the registry. Returns an error if the
// service is not registered or is currently running (must be stopped first).
func (r *ServiceRegistry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.services[name]
	if !exists {
		return fmt.Errorf("service %q not registered", name)
	}

	if info.State == ServiceStateRunning || info.State == ServiceStateStarting {
		return fmt.Errorf("service %q is %s; stop it before unregistering", name, info.State)
	}

	delete(r.services, name)
	return nil
}

// Get returns the ServiceInfo for a named service, or nil if not found.
func (r *ServiceRegistry) Get(name string) *ServiceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.services[name]
	if !ok {
		return nil
	}
	// Return a copy to avoid data races on mutable fields.
	copied := *info
	caps := make([]string, len(info.Capabilities))
	copy(caps, info.Capabilities)
	copied.Capabilities = caps
	return &copied
}

// Has returns true if a service with the given name exists in the registry.
func (r *ServiceRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.services[name]
	return ok
}

// QueryByCapability returns all services that declare the given capability.
func (r *ServiceRegistry) QueryByCapability(capability string) []*ServiceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*ServiceInfo
	for _, info := range r.services {
		for _, cap := range info.Capabilities {
			if cap == capability {
				copied := *info
				caps := make([]string, len(info.Capabilities))
				copy(caps, info.Capabilities)
				copied.Capabilities = caps
				result = append(result, &copied)
				break
			}
		}
	}
	return result
}

// List returns all currently registered services.
func (r *ServiceRegistry) List() []*ServiceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*ServiceInfo, 0, len(r.services))
	for _, info := range r.services {
		copied := *info
		caps := make([]string, len(info.Capabilities))
		copy(caps, info.Capabilities)
		copied.Capabilities = caps
		result = append(result, &copied)
	}
	return result
}

// Count returns the number of registered services.
func (r *ServiceRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.services)
}

// SetState atomically updates the state of a named service.
// Returns an error if the service is not registered.
func (r *ServiceRegistry) SetState(name string, state ServiceState) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.services[name]
	if !exists {
		return fmt.Errorf("service %q not registered", name)
	}
	info.State = state
	info.Error = nil
	return nil
}

// SetError atomically sets the service state to errored with the given error.
// Returns an error if the service is not registered.
func (r *ServiceRegistry) SetError(name string, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.services[name]
	if !exists {
		return fmt.Errorf("service %q not registered", name)
	}
	info.State = ServiceStateErrored
	info.Error = err
	return nil
}
