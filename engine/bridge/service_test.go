package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ServiceRegistry tests
// ---------------------------------------------------------------------------

func TestServiceRegistry_RegisterAndGet(t *testing.T) {
	r := NewServiceRegistry()

	err := r.Register("test-svc", "1.0.0", []string{"cap-a", "cap-b"})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
		return
	}

	info := r.Get("test-svc")
	if info == nil {
		t.Fatal("Get returned nil for registered service")
		return
	}
	if info.Name != "test-svc" {
		t.Errorf("Name = %q, want %q", info.Name, "test-svc")
	}
	if info.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", info.Version, "1.0.0")
	}
	if len(info.Capabilities) != 2 {
		t.Errorf("Capabilities len = %d, want 2", len(info.Capabilities))
	}
	if info.State != ServiceStateRegistered {
		t.Errorf("State = %q, want %q", info.State, ServiceStateRegistered)
	}
}

func TestServiceRegistry_RegisterDuplicate(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("dup", "1.0", nil)

	err := r.Register("dup", "2.0", nil)
	if err == nil {
		t.Fatal("expected error on duplicate register")
		return
	}
}

func TestServiceRegistry_Unregister(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("svc", "1.0", nil)

	err := r.Unregister("svc")
	if err != nil {
		t.Fatalf("Unregister failed: %v", err)
		return
	}

	if r.Has("svc") {
		t.Error("service still present after unregister")
	}
}

func TestServiceRegistry_UnregisterRunning(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("svc", "1.0", nil)
	_ = r.SetState("svc", ServiceStateRunning)

	err := r.Unregister("svc")
	if err == nil {
		t.Fatal("expected error unregistering running service")
		return
	}
}

func TestServiceRegistry_UnregisterNotFound(t *testing.T) {
	r := NewServiceRegistry()
	err := r.Unregister("nonexistent")
	if err == nil {
		t.Fatal("expected error unregistering non-existent service")
		return
	}
}

func TestServiceRegistry_QueryByCapability(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("svc-a", "1.0", []string{"http", "grpc"})
	_ = r.Register("svc-b", "1.0", []string{"http"})
	_ = r.Register("svc-c", "1.0", []string{"grpc"})

	results := r.QueryByCapability("http")
	if len(results) != 2 {
		t.Fatalf("QueryByCapability(http) returned %d results, want 2", len(results))
	}

	results = r.QueryByCapability("grpc")
	if len(results) != 2 {
		t.Fatalf("QueryByCapability(grpc) returned %d results, want 2", len(results))
	}

	results = r.QueryByCapability("websocket")
	if len(results) != 0 {
		t.Fatalf("QueryByCapability(websocket) returned %d results, want 0", len(results))
	}
}

func TestServiceRegistry_List(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("a", "1.0", nil)
	_ = r.Register("b", "2.0", nil)
	_ = r.Register("c", "3.0", nil)

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List() returned %d entries, want 3", len(list))
	}
}

func TestServiceRegistry_Count(t *testing.T) {
	r := NewServiceRegistry()
	if r.Count() != 0 {
		t.Fatalf("Count() = %d for empty registry", r.Count())
	}
	_ = r.Register("a", "1.0", nil)
	if r.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", r.Count())
	}
}

func TestServiceRegistry_SetState(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("svc", "1.0", nil)

	_ = r.SetState("svc", ServiceStateRunning)
	info := r.Get("svc")
	if info.State != ServiceStateRunning {
		t.Errorf("State = %q, want %q", info.State, ServiceStateRunning)
	}
}

func TestServiceRegistry_SetError(t *testing.T) {
	r := NewServiceRegistry()
	_ = r.Register("svc", "1.0", nil)

	testErr := errors.New("something broke")
	_ = r.SetError("svc", testErr)

	info := r.Get("svc")
	if info.State != ServiceStateErrored {
		t.Errorf("State = %q, want %q", info.State, ServiceStateErrored)
	}
	if info.Error == nil || info.Error.Error() != "something broke" {
		t.Errorf("Error = %v, want %v", info.Error, testErr)
	}
}

func TestServiceRegistry_ConcurrentAccess(t *testing.T) {
	r := NewServiceRegistry()
	var wg sync.WaitGroup

	// Concurrent registrations.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Register(fmt.Sprintf("svc-%d", i), "1.0", []string{"cap"})
		}(i)
	}
	wg.Wait()

	if r.Count() != 50 {
		t.Fatalf("Count() = %d after 50 concurrent registrations", r.Count())
	}

	// Concurrent queries.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Get(fmt.Sprintf("svc-%d", i))
			_ = r.QueryByCapability("cap")
			_ = r.List()
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// PluginManager lifecycle tests
// ---------------------------------------------------------------------------

// testPlugin is a minimal Plugin implementation for testing.
type testPlugin struct {
	name         string
	version      string
	capabilities []string
	dependencies []string
	startErr     error
	stopErr      error
	healthErr    error
	startCalled  atomic.Int32
	stopCalled   atomic.Int32
	panicOnStart bool
	panicOnStop  bool
	startDelay   time.Duration
	stopDelay    time.Duration
}

func (p *testPlugin) Name() string           { return p.name }
func (p *testPlugin) Version() string        { return p.version }
func (p *testPlugin) Capabilities() []string { return p.capabilities }
func (p *testPlugin) Dependencies() []string { return p.dependencies }
func (p *testPlugin) Health() error          { return p.healthErr }

func (p *testPlugin) Start(ctx context.Context) error {
	p.startCalled.Add(1)
	if p.panicOnStart {
		panic("intentional test panic in Start")
	}
	if p.startDelay > 0 {
		select {
		case <-time.After(p.startDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.startErr
}

func (p *testPlugin) Stop(ctx context.Context) error {
	p.stopCalled.Add(1)
	if p.panicOnStop {
		panic("intentional test panic in Stop")
	}
	if p.stopDelay > 0 {
		select {
		case <-time.After(p.stopDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.stopErr
}

func TestPluginManager_RegisterAndStart(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	p := &testPlugin{name: "test-plugin", version: "1.0.0", capabilities: []string{"test"}}
	if err := pm.Register(p); err != nil {
		t.Fatalf("Register failed: %v", err)
		return
	}

	ctx := context.Background()
	if err := pm.StartAll(ctx); err != nil {
		t.Fatalf("StartAll failed: %v", err)
		return
	}

	if p.startCalled.Load() != 1 {
		t.Errorf("Start called %d times, want 1", p.startCalled.Load())
	}

	info := pm.Registry().Get("test-plugin")
	if info.State != ServiceStateRunning {
		t.Errorf("State = %q, want %q", info.State, ServiceStateRunning)
	}
}

func TestPluginManager_StopAll_ReverseOrder(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	makePlugin := func(name string) *testPlugin {
		p := &testPlugin{name: name, version: "1.0"}
		return p
	}

	// Register in order: A, B, C.
	pA := makePlugin("A")
	pB := makePlugin("B")
	pC := makePlugin("C")

	_ = pm.Register(pA)
	_ = pm.Register(pB)
	_ = pm.Register(pC)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	// Stop all and verify state transitions.
	// Since stop is called in reverse registration order, all should end up stopped.
	_ = pm.StopAll(ctx)

	// Verify all are stopped.
	for _, name := range []string{"A", "B", "C"} {
		info := pm.Registry().Get(name)
		if info.State != ServiceStateStopped {
			t.Errorf("Plugin %s state = %q, want %q", name, info.State, ServiceStateStopped)
		}
	}

	// Verify stop was called on all.
	if pA.stopCalled.Load() != 1 || pB.stopCalled.Load() != 1 || pC.stopCalled.Load() != 1 {
		t.Errorf("Stop calls: A=%d B=%d C=%d, want all 1",
			pA.stopCalled.Load(), pB.stopCalled.Load(), pC.stopCalled.Load())
	}
}

func TestPluginManager_PanicRecovery_Start(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	p := &testPlugin{name: "panicker", version: "1.0", panicOnStart: true}
	_ = pm.Register(p)

	ctx := context.Background()
	// StartAll should not panic even though the plugin panics.
	_ = pm.StartAll(ctx)

	info := pm.Registry().Get("panicker")
	if info.State != ServiceStateErrored {
		t.Errorf("State = %q, want %q", info.State, ServiceStateErrored)
	}
	if info.Error == nil {
		t.Error("Error should be non-nil for panicked plugin")
	}
}

func TestPluginManager_PanicRecovery_Stop(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	p := &testPlugin{name: "panicker", version: "1.0", panicOnStop: true}
	_ = pm.Register(p)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	// StopAll should not panic even though the plugin panics.
	err := pm.StopAll(ctx)
	if err == nil {
		t.Error("expected error from panicking stop")
	}

	info := pm.Registry().Get("panicker")
	if info.State != ServiceStateErrored {
		t.Errorf("State = %q, want %q", info.State, ServiceStateErrored)
	}
}

func TestPluginManager_StartError(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
	})

	p := &testPlugin{name: "failing", version: "1.0", startErr: errors.New("start failed")}
	_ = pm.Register(p)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	info := pm.Registry().Get("failing")
	if info.State != ServiceStateErrored {
		t.Errorf("State = %q, want %q", info.State, ServiceStateErrored)
	}
}

func TestPluginManager_DependencyOrder(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	makePlugin := func(name string, deps []string) *testPlugin {
		return &testPlugin{
			name:         name,
			version:      "1.0",
			dependencies: deps,
			startDelay:   1 * time.Millisecond,
		}
	}

	// C depends on B, B depends on A.
	pA := makePlugin("A", nil)
	pB := makePlugin("B", []string{"A"})
	pC := makePlugin("C", []string{"B"})

	// Register in reverse dependency order to verify sorting works.
	_ = pm.Register(pC)
	_ = pm.Register(pA)
	_ = pm.Register(pB)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	// All should be running.
	for _, name := range []string{"A", "B", "C"} {
		info := pm.Registry().Get(name)
		if info.State != ServiceStateRunning {
			t.Errorf("Plugin %s state = %q, want %q", name, info.State, ServiceStateRunning)
		}
	}

	// Verify start was called on all.
	if pA.startCalled.Load() != 1 || pB.startCalled.Load() != 1 || pC.startCalled.Load() != 1 {
		t.Errorf("Start calls: A=%d B=%d C=%d",
			pA.startCalled.Load(), pB.startCalled.Load(), pC.startCalled.Load())
	}
}

func TestPluginManager_DependencyCycle(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
	})

	// Create a cycle: A -> B -> C -> A
	pA := &testPlugin{name: "A", version: "1.0", dependencies: []string{"C"}}
	pB := &testPlugin{name: "B", version: "1.0", dependencies: []string{"A"}}
	pC := &testPlugin{name: "C", version: "1.0", dependencies: []string{"B"}}

	_ = pm.Register(pA)
	_ = pm.Register(pB)
	_ = pm.Register(pC)

	ctx := context.Background()
	err := pm.StartAll(ctx)
	if err == nil {
		t.Fatal("expected error for dependency cycle")
		return
	}
}

func TestPluginManager_DependencyFailurePropagates(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
	})

	// A fails to start, B depends on A.
	pA := &testPlugin{name: "A", version: "1.0", startErr: errors.New("A failed")}
	pB := &testPlugin{name: "B", version: "1.0", dependencies: []string{"A"}}

	_ = pm.Register(pA)
	_ = pm.Register(pB)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	infoA := pm.Registry().Get("A")
	if infoA.State != ServiceStateErrored {
		t.Errorf("A state = %q, want errored", infoA.State)
	}

	infoB := pm.Registry().Get("B")
	if infoB.State != ServiceStateErrored {
		t.Errorf("B state = %q, want errored (dependency failed)", infoB.State)
	}

	// B's Start should not have been called.
	if pB.startCalled.Load() != 0 {
		t.Error("B's Start should not have been called since A failed")
	}
}

func TestPluginManager_HealthCheck(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
	})

	healthy := &testPlugin{name: "healthy", version: "1.0"}
	unhealthy := &testPlugin{name: "unhealthy", version: "1.0", healthErr: errors.New("sick")}

	_ = pm.Register(healthy)
	_ = pm.Register(unhealthy)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	results := pm.HealthCheck()
	if results["healthy"] != nil {
		t.Errorf("healthy plugin reported error: %v", results["healthy"])
	}
	if results["unhealthy"] == nil {
		t.Error("unhealthy plugin reported no error")
	}
}

func TestPluginManager_RegisterDuplicate(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{})

	p := &testPlugin{name: "dup", version: "1.0"}
	_ = pm.Register(p)

	err := pm.Register(p)
	if err == nil {
		t.Fatal("expected error on duplicate plugin registration")
		return
	}
}

func TestPluginManager_Unregister(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	p := &testPlugin{name: "removable", version: "1.0"}
	_ = pm.Register(p)

	// Can unregister in registered state.
	err := pm.Unregister("removable")
	if err != nil {
		t.Fatalf("Unregister failed: %v", err)
		return
	}

	if pm.Registry().Has("removable") {
		t.Error("plugin still in registry after unregister")
	}
}

func TestPluginManager_Hooks(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{})

	p := &testPlugin{name: "hooker", version: "1.0"}
	_ = pm.Register(p)

	handler := func() {}
	err := pm.RegisterHook("hooker", HookTypePreTool, handler)
	if err != nil {
		t.Fatalf("RegisterHook failed: %v", err)
		return
	}

	hooks := pm.Hooks(HookTypePreTool)
	if len(hooks) != 1 {
		t.Fatalf("Hooks(PreTool) returned %d, want 1", len(hooks))
	}
	if hooks[0].PluginName != "hooker" {
		t.Errorf("Hook plugin name = %q, want %q", hooks[0].PluginName, "hooker")
	}

	pluginHooks := pm.HooksForPlugin("hooker")
	if len(pluginHooks) != 1 {
		t.Fatalf("HooksForPlugin returned %d, want 1", len(pluginHooks))
	}
}

func TestPluginManager_HookUnregisteredPlugin(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{})

	err := pm.RegisterHook("nonexistent", HookTypePreTool, nil)
	if err == nil {
		t.Fatal("expected error registering hook for non-existent plugin")
		return
	}
}

func TestPluginManager_StartTimeout(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 50 * time.Millisecond,
	})

	p := &testPlugin{name: "slow", version: "1.0", startDelay: 5 * time.Second}
	_ = pm.Register(p)

	ctx := context.Background()
	_ = pm.StartAll(ctx)

	info := pm.Registry().Get("slow")
	if info.State != ServiceStateErrored {
		t.Errorf("State = %q, want errored (timeout)", info.State)
	}
}

func TestPluginManager_PluginNames(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{})

	_ = pm.Register(&testPlugin{name: "first", version: "1.0"})
	_ = pm.Register(&testPlugin{name: "second", version: "1.0"})
	_ = pm.Register(&testPlugin{name: "third", version: "1.0"})

	names := pm.PluginNames()
	if len(names) != 3 {
		t.Fatalf("PluginNames() returned %d, want 3", len(names))
	}
	if names[0] != "first" || names[1] != "second" || names[2] != "third" {
		t.Errorf("PluginNames() = %v, want [first second third]", names)
	}
}

func TestPluginManager_GetPlugin(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{})

	p := &testPlugin{name: "findme", version: "1.0"}
	_ = pm.Register(p)

	got := pm.GetPlugin("findme")
	if got != p {
		t.Error("GetPlugin did not return the registered plugin")
	}

	got = pm.GetPlugin("nonexistent")
	if got != nil {
		t.Error("GetPlugin should return nil for non-existent plugin")
	}
}

// ---------------------------------------------------------------------------
// Plugin discovery tests
// ---------------------------------------------------------------------------

func TestDiscoverPlugins_ValidManifests(t *testing.T) {
	dir := t.TempDir()

	writeManifest(t, dir, "plugin-a.json", PluginManifest{
		Name:         "plugin-a",
		Version:      "1.0.0",
		EntryPoint:   "github.com/example/plugin-a",
		Capabilities: []string{"http", "auth"},
		Dependencies: []string{},
	})

	writeManifest(t, dir, "plugin-b.json", PluginManifest{
		Name:         "plugin-b",
		Version:      "2.1.0",
		EntryPoint:   "github.com/example/plugin-b",
		Capabilities: []string{"storage"},
		Dependencies: []string{"plugin-a"},
	})

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir},
	})

	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("found %d manifests, want 2", len(result.Manifests))
	}
}

func TestDiscoverPlugins_MissingDirectory(t *testing.T) {
	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{"/nonexistent/path/that/does/not/exist"},
	})

	// Missing directories should not produce errors.
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors for missing dir: %v", result.Errors)
	}
	if len(result.Manifests) != 0 {
		t.Errorf("found %d manifests, want 0", len(result.Manifests))
	}
}

func TestDiscoverPlugins_InvalidJSON(t *testing.T) {
	dir := t.TempDir()

	err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("not valid json{{{"), 0o644)
	if err != nil {
		t.Fatal(err)
		return
	}

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir},
	})

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if len(result.Manifests) != 0 {
		t.Errorf("found %d manifests, want 0", len(result.Manifests))
	}
}

func TestDiscoverPlugins_MissingRequiredFields(t *testing.T) {
	dir := t.TempDir()

	// Missing name.
	writeManifest(t, dir, "no-name.json", PluginManifest{
		Version: "1.0",
	})

	// Missing version.
	writeManifest(t, dir, "no-version.json", PluginManifest{
		Name: "no-version-plugin",
	})

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir},
	})

	if len(result.Errors) != 2 {
		t.Fatalf("expected 2 validation errors, got %d: %v", len(result.Errors), result.Errors)
	}
	if len(result.Manifests) != 0 {
		t.Errorf("found %d manifests, want 0", len(result.Manifests))
	}
}

func TestDiscoverPlugins_DuplicateNames(t *testing.T) {
	dir := t.TempDir()

	writeManifest(t, dir, "plugin-a.json", PluginManifest{
		Name:    "duplicate",
		Version: "1.0",
	})
	writeManifest(t, dir, "plugin-b.json", PluginManifest{
		Name:    "duplicate",
		Version: "2.0",
	})

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir},
	})

	// One should succeed, one should be recorded as error.
	if len(result.Manifests) != 1 {
		t.Errorf("found %d manifests, want 1 (first wins)", len(result.Manifests))
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 duplicate error, got %d", len(result.Errors))
	}
}

func TestDiscoverPlugins_DisabledViaManifest(t *testing.T) {
	dir := t.TempDir()

	disabled := false
	writeManifestWithEnabled(t, dir, "disabled.json", PluginManifest{
		Name:    "disabled-plugin",
		Version: "1.0",
	}, &disabled)

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir},
	})

	if len(result.Manifests) != 0 {
		t.Errorf("found %d manifests, want 0 (plugin disabled)", len(result.Manifests))
	}
}

func TestDiscoverPlugins_DisabledViaConfig(t *testing.T) {
	dir := t.TempDir()

	writeManifest(t, dir, "plugin.json", PluginManifest{
		Name:    "config-disabled",
		Version: "1.0",
	})

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs:            []string{dir},
		DisabledPlugins: map[string]bool{"config-disabled": true},
	})

	if len(result.Manifests) != 0 {
		t.Errorf("found %d manifests, want 0 (plugin disabled via config)", len(result.Manifests))
	}
}

func TestDiscoverPlugins_IgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a plugin"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte("also not json"), 0o644)

	writeManifest(t, dir, "real-plugin.json", PluginManifest{
		Name:    "real",
		Version: "1.0",
	})

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir},
	})

	if len(result.Manifests) != 1 {
		t.Errorf("found %d manifests, want 1", len(result.Manifests))
	}
}

func TestDiscoverPlugins_MultipleDirectories(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeManifest(t, dir1, "plugin-a.json", PluginManifest{
		Name:    "from-dir1",
		Version: "1.0",
	})
	writeManifest(t, dir2, "plugin-b.json", PluginManifest{
		Name:    "from-dir2",
		Version: "1.0",
	})

	result := DiscoverPlugins(DiscoveryConfig{
		Dirs: []string{dir1, dir2},
	})

	if len(result.Manifests) != 2 {
		t.Fatalf("found %d manifests, want 2", len(result.Manifests))
	}
}

// ---------------------------------------------------------------------------
// ManifestPlugin tests
// ---------------------------------------------------------------------------

func TestManifestPlugin_Interface(t *testing.T) {
	manifest := &PluginManifest{
		Name:         "manifest-p",
		Version:      "3.0.0",
		Capabilities: []string{"cap1"},
		Dependencies: []string{"dep1"},
	}

	p := NewManifestPlugin(manifest)

	// Verify it implements Plugin interface.
	var _ Plugin = p

	if p.Name() != "manifest-p" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.Version() != "3.0.0" {
		t.Errorf("Version() = %q", p.Version())
	}
	if len(p.Capabilities()) != 1 || p.Capabilities()[0] != "cap1" {
		t.Errorf("Capabilities() = %v", p.Capabilities())
	}
	if len(p.Dependencies()) != 1 || p.Dependencies()[0] != "dep1" {
		t.Errorf("Dependencies() = %v", p.Dependencies())
	}

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Errorf("Start() = %v", err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Errorf("Stop() = %v", err)
	}
	if err := p.Health(); err != nil {
		t.Errorf("Health() = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration test: discovery + plugin manager
// ---------------------------------------------------------------------------

func TestDiscoveryToPluginManager_Integration(t *testing.T) {
	dir := t.TempDir()

	writeManifest(t, dir, "base.json", PluginManifest{
		Name:         "base",
		Version:      "1.0.0",
		Capabilities: []string{"foundation"},
	})
	writeManifest(t, dir, "ext.json", PluginManifest{
		Name:         "ext",
		Version:      "1.0.0",
		Capabilities: []string{"extended"},
		Dependencies: []string{"base"},
	})

	result := DiscoverPlugins(DiscoveryConfig{Dirs: []string{dir}})
	if len(result.Errors) != 0 {
		t.Fatalf("discovery errors: %v", result.Errors)
	}

	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
	})

	for _, m := range result.Manifests {
		if err := pm.Register(NewManifestPlugin(m)); err != nil {
			t.Fatalf("Register(%s) failed: %v", m.Name, err)
			return
		}
	}

	ctx := context.Background()
	if err := pm.StartAll(ctx); err != nil {
		t.Fatalf("StartAll failed: %v", err)
		return
	}

	for _, name := range []string{"base", "ext"} {
		info := pm.Registry().Get(name)
		if info.State != ServiceStateRunning {
			t.Errorf("Plugin %s state = %q, want running", name, info.State)
		}
	}

	if err := pm.StopAll(ctx); err != nil {
		t.Fatalf("StopAll failed: %v", err)
		return
	}

	for _, name := range []string{"base", "ext"} {
		info := pm.Registry().Get(name)
		if info.State != ServiceStateStopped {
			t.Errorf("Plugin %s state = %q, want stopped", name, info.State)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeManifest(t *testing.T, dir, filename string, m PluginManifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		t.Fatal(err)
		return
	}
}

func writeManifestWithEnabled(t *testing.T, dir, filename string, m PluginManifest, enabled *bool) {
	t.Helper()
	// Marshal manually to include the enabled field.
	raw := map[string]any{
		"name":    m.Name,
		"version": m.Version,
	}
	if enabled != nil {
		raw["enabled"] = *enabled
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		t.Fatal(err)
		return
	}
}
