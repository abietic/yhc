package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// registry_parity_test.go — Final parity verification tests for the tool registry.
// Covers:
// - Schema compliance for all registered tools
// - Tool execution contract (ctx.Done handling)
// - Tool output normalization
// - Registry concurrency safety
// - Alias lookup, enable/disable, metadata tracking

// TestAllRegisteredToolsHaveValidSchemas verifies that every tool registered by
// RegisterDefaults has a non-nil schema with at least a Name and valid ParamsOneOf.
func TestAllRegisteredToolsHaveValidSchemas(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	names := reg.Names()
	if len(names) < 30 {
		t.Fatalf("expected at least 30 registered tools, got %d", len(names))
	}

	for _, name := range names {
		impl, ok := reg.GetIncludeDisabled(name)
		if !ok {
			t.Errorf("tool %q listed in Names but not found via GetIncludeDisabled", name)
			continue
		}
		if impl.Info == nil {
			t.Errorf("tool %q has nil Info", name)
			continue
		}
		if impl.Info.Name == "" {
			t.Errorf("tool %q has empty Info.Name", name)
		}
		if impl.Info.Name != name {
			t.Errorf("tool registered as %q but Info.Name is %q", name, impl.Info.Name)
		}
		// Every tool must have a description
		if impl.Info.Desc == "" {
			t.Errorf("tool %q has empty description", name)
		}
	}
}

// TestAllToolSchemasProduceValidJSON ensures ParamsOneOf serializes without error.
func TestAllToolSchemasProduceValidJSON(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	for _, name := range reg.Names() {
		impl, _ := reg.GetIncludeDisabled(name)
		if impl.Info == nil || impl.Info.ParamsOneOf == nil {
			continue // Some tools (like EnterPlanMode) may have empty schemas
		}
		jsonSchema, err := impl.Info.ToJSONSchema()
		if err != nil {
			t.Errorf("tool %q: ParamsOneOf.ToJSONSchema() error: %v", name, err)
			continue
		}
		if jsonSchema == nil {
			t.Errorf("tool %q: ParamsOneOf.ToJSONSchema() returned nil", name)
			continue
		}
		// Must be JSON-serializable
		bytes, err := json.Marshal(jsonSchema)
		if err != nil {
			t.Errorf("tool %q: schema not JSON-serializable: %v", name, err)
			continue
		}
		if !json.Valid(bytes) {
			t.Errorf("tool %q: schema JSON is not valid", name)
		}
	}
}

// TestRegistryToolCountMatchesExpected verifies we have the expected tool count
// from RegisterDefaults (mirrors the reference's 40+ tools).
func TestRegistryToolCountMatchesExpected(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	names := reg.Names()
	// RegisterDefaults registers 39 tools as of the current implementation.
	// Ensure we have at least 35 to catch accidental removal.
	if len(names) < 35 {
		t.Errorf("expected at least 35 registered tools, got %d: %v", len(names), names)
	}
}

func TestProcessGlobalWorktreeToolsCannotBeRegistered(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	for _, name := range []string{"EnterWorktree", "ExitWorktree"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("%s must not be in the default registry", name)
		}
		called := false
		reg.Register(ToolImpl{
			Info: &schema.ToolInfo{Name: name, Desc: "unsafe compatibility implementation"},
			Execute: func(string) (string, error) {
				called = true
				return "unexpected", nil
			},
		})
		if _, ok := reg.Get(name); ok {
			t.Fatalf("%s reserved name was re-registered", name)
		}
		if called {
			t.Fatalf("%s implementation executed during rejected registration", name)
		}
		reason, unavailable := UnavailableBuiltinToolReason(name)
		if !unavailable || !strings.Contains(reason, "Agent") {
			t.Fatalf("%s unavailable reason = %q, %v", name, reason, unavailable)
		}

		primary := "Safe" + name
		reg.Register(ToolImpl{
			Info:    &schema.ToolInfo{Name: primary, Desc: "alias bypass attempt"},
			Aliases: []string{name},
			Execute: func(string) (string, error) {
				return "unexpected", nil
			},
		})
		if _, ok := reg.Get(primary); ok {
			t.Fatalf("%s registration with reserved alias must reject the whole tool", primary)
		}
		if _, ok := reg.Get(name); ok {
			t.Fatalf("%s reserved alias was registered", name)
		}
	}

	if _, ok := reg.Get("Agent"); !ok {
		t.Fatal("Agent isolation tool must remain registered")
	}
}

// TestToolExecuteOrExecuteCtxIsSet verifies every tool has at least one execute path.
// Some tools (Agent, Task) may have deferred execution via registry-level hooks,
// but they should still declare either Execute or ExecuteCtx.
func TestToolExecuteOrExecuteCtxIsSet(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	// Tools that legitimately have no local executor (they delegate to runtime).
	noExecExpected := map[string]bool{
		"Agent":       true,
		"Task":        true,
		"SendMessage": true,
	}

	for _, name := range reg.Names() {
		impl, _ := reg.GetIncludeDisabled(name)
		if impl.Execute == nil && impl.ExecuteCtx == nil {
			if !noExecExpected[name] {
				t.Errorf("tool %q has neither Execute nor ExecuteCtx", name)
			}
		}
	}
}

// TestToolContractClassificationsAreComplete verifies a read-only operation
// needs permission only when its declared effects cross a separate dynamic,
// network, or interaction boundary.
func TestToolContractClassificationsAreComplete(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	for _, name := range reg.Names() {
		impl, _ := reg.GetIncludeDisabled(name)
		if impl.IsReadOnly && impl.NeedsPermissions &&
			(!impl.Capabilities.Declared ||
				!impl.Capabilities.Network &&
					!impl.Capabilities.Dynamic &&
					!impl.Capabilities.RequiresUserInteraction) {
			t.Errorf(
				"read-only tool %q needs permission without a declared boundary: %+v",
				name,
				impl.Capabilities,
			)
		}
	}
}

// TestToolExecuteCtxRespectsContextCancellation verifies that tools with
// ExecuteCtx properly observe context cancellation (ctx.Done).
func TestToolExecuteCtxRespectsContextCancellation(t *testing.T) {
	// Use BashTool which has ExecuteCtx and supports timeout/cancellation.
	bash := BashTool()
	if bash.ExecuteCtx == nil {
		t.Skip("BashTool does not have ExecuteCtx")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// A long-running command with a cancelled context should error or return quickly.
	start := time.Now()
	_, err := bash.ExecuteCtx(ctx, `{"command": "sleep 10", "timeout": 30000}`)
	elapsed := time.Since(start)

	// Should return quickly (within 5s), not wait for 10s.
	if elapsed > 5*time.Second {
		t.Errorf("BashTool did not respect context cancellation, took %v", elapsed)
	}
	// The result might be an error or a message about cancellation
	_ = err
}

// TestRegistryDisablePreventslookup verifies disabled tools are not returned by Get.
func TestRegistryDisablePreventslookup(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	// Disable Read tool
	reg.Disable("Read")

	_, ok := reg.Get("Read")
	if ok {
		t.Error("disabled tool 'Read' should not be returned by Get")
	}

	// GetIncludeDisabled should still find it
	_, ok = reg.GetIncludeDisabled("Read")
	if !ok {
		t.Error("disabled tool 'Read' should be returned by GetIncludeDisabled")
	}

	// Re-enable
	reg.Enable("Read")
	_, ok = reg.Get("Read")
	if !ok {
		t.Error("re-enabled tool 'Read' should be returned by Get")
	}
}

// TestRegistryListExcludesDisabled verifies List() does not include disabled tools.
func TestRegistryListExcludesDisabled(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	allCount := len(reg.List())
	reg.Disable("Read")
	reg.Disable("Write")

	afterCount := len(reg.List())
	if afterCount != allCount-2 {
		t.Errorf("expected %d tools after disabling 2, got %d", allCount-2, afterCount)
	}

	// Verify disabled tools are not in the list
	for _, info := range reg.List() {
		if info.Name == "Read" || info.Name == "Write" {
			t.Errorf("disabled tool %q should not appear in List()", info.Name)
		}
	}
}

// TestRegistryRecordCallUpdatesMetadata verifies RecordCall tracking.
func TestRegistryRecordCallUpdatesMetadata(t *testing.T) {
	reg := NewRegistry()
	reg.Register(ToolImpl{
		Info: &schema.ToolInfo{Name: "TestTool", Desc: "test"},
		Execute: func(input string) (string, error) {
			return "ok", nil
		},
	})

	// Record several calls
	reg.RecordCall("TestTool", 100*time.Millisecond, nil)
	reg.RecordCall("TestTool", 200*time.Millisecond, nil)
	reg.RecordCall("TestTool", 300*time.Millisecond, nil)

	meta := reg.GetMetadata("TestTool")
	if meta == nil {
		t.Fatal("expected metadata for TestTool")
		return
	}
	if meta.CallCount != 3 {
		t.Errorf("expected 3 calls, got %d", meta.CallCount)
	}
	avg := meta.AverageDuration()
	if avg != 200*time.Millisecond {
		t.Errorf("expected avg duration 200ms, got %v", avg)
	}
}

// TestRegistryRecordCallTracksErrors verifies error tracking in metadata.
func TestRegistryRecordCallTracksErrors(t *testing.T) {
	reg := NewRegistry()
	reg.Register(ToolImpl{
		Info: &schema.ToolInfo{Name: "ErrTool", Desc: "test"},
		Execute: func(input string) (string, error) {
			return "", nil
		},
	})

	testErr := &testError{msg: "something failed"}
	reg.RecordCall("ErrTool", 50*time.Millisecond, testErr)

	meta := reg.GetMetadata("ErrTool")
	if meta == nil {
		t.Fatal("expected metadata")
		return
	}
	if meta.LastError != "something failed" {
		t.Errorf("expected last error 'something failed', got %q", meta.LastError)
	}
	if meta.LastErrorAt.IsZero() {
		t.Error("expected non-zero LastErrorAt")
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// TestRegistryHooksFireCorrectly verifies OnRegister and OnUnregister hooks.
func TestRegistryHooksFireCorrectly(t *testing.T) {
	reg := NewRegistry()

	var registered []string
	var unregistered []string
	var mu sync.Mutex

	reg.OnRegister(func(name string, impl ToolImpl) {
		mu.Lock()
		registered = append(registered, name)
		mu.Unlock()
	})
	reg.OnUnregister(func(name string, impl ToolImpl) {
		mu.Lock()
		unregistered = append(unregistered, name)
		mu.Unlock()
	})

	reg.Register(ToolImpl{
		Info: &schema.ToolInfo{Name: "HookTest", Desc: "hook test"},
		Execute: func(input string) (string, error) {
			return "ok", nil
		},
	})

	mu.Lock()
	if len(registered) != 1 || registered[0] != "HookTest" {
		t.Errorf("expected register hook for 'HookTest', got %v", registered)
	}
	mu.Unlock()

	reg.Unregister("HookTest")

	mu.Lock()
	if len(unregistered) != 1 || unregistered[0] != "HookTest" {
		t.Errorf("expected unregister hook for 'HookTest', got %v", unregistered)
	}
	mu.Unlock()
}

// TestRegistryAliasLookup verifies alias registration and lookup.
func TestRegistryAliasLookup(t *testing.T) {
	reg := NewRegistry()
	reg.Register(ToolImpl{
		Info:    &schema.ToolInfo{Name: "Original", Desc: "original tool"},
		Aliases: []string{"Alias1", "Alias2"},
		Execute: func(input string) (string, error) {
			return "from-original", nil
		},
	})

	// Primary name lookup
	impl, ok := reg.Get("Original")
	if !ok {
		t.Fatal("expected to find tool by primary name")
	}
	result, _ := impl.Execute("")
	if result != "from-original" {
		t.Errorf("expected 'from-original', got %q", result)
	}

	// Alias lookups
	impl, ok = reg.Get("Alias1")
	if !ok {
		t.Fatal("expected to find tool by alias 'Alias1'")
	}
	result, _ = impl.Execute("")
	if result != "from-original" {
		t.Errorf("expected 'from-original' via alias, got %q", result)
	}

	impl, ok = reg.Get("Alias2")
	if !ok {
		t.Fatal("expected to find tool by alias 'Alias2'")
	}
}

// TestRegistryUnregisterRemovesAliases verifies unregister cleans up aliases.
func TestRegistryUnregisterRemovesAliases(t *testing.T) {
	reg := NewRegistry()
	reg.Register(ToolImpl{
		Info:    &schema.ToolInfo{Name: "WithAliases", Desc: "test"},
		Aliases: []string{"AliasA", "AliasB"},
		Execute: func(input string) (string, error) {
			return "ok", nil
		},
	})

	reg.Unregister("WithAliases")

	_, ok := reg.Get("WithAliases")
	if ok {
		t.Error("unregistered tool should not be found")
	}
	_, ok = reg.Get("AliasA")
	if ok {
		t.Error("alias 'AliasA' should be removed on unregister")
	}
	_, ok = reg.Get("AliasB")
	if ok {
		t.Error("alias 'AliasB' should be removed on unregister")
	}
}

// TestRegistryConcurrentAccessSafe verifies the registry handles concurrent
// register/get/disable/enable without race conditions.
func TestRegistryConcurrentAccessSafe(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	var wg sync.WaitGroup
	const goroutines = 20

	// Concurrent reads
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.List()
			_ = reg.Names()
			_, _ = reg.Get("Read")
			_ = reg.IsEnabled("Write")
			_ = reg.GetMetadata("Bash")
		}()
	}

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := "ConcurrentTool" + strings.Repeat("x", idx%5)
			reg.Register(ToolImpl{
				Info:    &schema.ToolInfo{Name: name, Desc: "concurrent"},
				Execute: func(input string) (string, error) { return "ok", nil },
			})
			reg.RecordCall(name, time.Millisecond, nil)
			reg.Disable(name)
			reg.Enable(name)
		}(i)
	}

	wg.Wait()
}

// TestRegistryPreservesInsertionOrder verifies that Names() returns tools in
// registration order (critical for deterministic tool list to model).
func TestRegistryPreservesInsertionOrder(t *testing.T) {
	reg := NewRegistry()
	expected := []string{"Alpha", "Beta", "Gamma", "Delta"}
	for _, name := range expected {
		reg.Register(ToolImpl{
			Info:    &schema.ToolInfo{Name: name, Desc: "test"},
			Execute: func(input string) (string, error) { return "ok", nil },
		})
	}

	names := reg.Names()
	if len(names) != len(expected) {
		t.Fatalf("expected %d names, got %d", len(expected), len(names))
	}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("position %d: expected %q, got %q", i, expected[i], name)
		}
	}
}

// TestRegistryUpdateReplacesImplementation verifies Update replaces impl without
// changing order or metadata.
func TestRegistryUpdateReplacesImplementation(t *testing.T) {
	reg := NewRegistry()
	reg.Register(ToolImpl{
		Info:    &schema.ToolInfo{Name: "Updatable", Desc: "original"},
		Execute: func(input string) (string, error) { return "v1", nil },
	})

	// Record some calls first
	reg.RecordCall("Updatable", time.Millisecond, nil)

	// Update the implementation
	reg.Update("Updatable", ToolImpl{
		Info:    &schema.ToolInfo{Name: "Updatable", Desc: "updated"},
		Execute: func(input string) (string, error) { return "v2", nil },
	})

	impl, ok := reg.Get("Updatable")
	if !ok {
		t.Fatal("expected tool after update")
	}
	result, _ := impl.Execute("")
	if result != "v2" {
		t.Errorf("expected 'v2' after update, got %q", result)
	}

	// Metadata should be preserved
	meta := reg.GetMetadata("Updatable")
	if meta == nil || meta.CallCount != 1 {
		t.Error("metadata should be preserved across Update")
	}
}

// TestValidateToolInputEnumConstraint verifies enum validation works correctly.
func TestValidateToolInputEnumConstraint(t *testing.T) {
	info := &schema.ToolInfo{
		Name: "EnumTool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"mode": {Type: schema.String, Desc: "mode", Required: true, Enum: []string{"fast", "slow", "auto"}},
		}),
	}

	// Valid enum value
	err := ValidateToolInput(info, map[string]any{"mode": "fast"})
	if err != nil {
		t.Errorf("expected no error for valid enum value, got: %v", err)
	}

	// Invalid enum value
	err = ValidateToolInput(info, map[string]any{"mode": "turbo"})
	if err == nil {
		t.Error("expected error for invalid enum value 'turbo'")
	} else if !strings.Contains(err.Error(), "turbo") {
		t.Errorf("error should mention invalid value 'turbo', got: %s", err.Error())
	}
}
