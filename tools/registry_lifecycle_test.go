package tools

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// --- Registry lifecycle hook tests ---

func TestOnRegisterHookFires(t *testing.T) {
	reg := NewRegistry()
	var called int32
	var capturedName string

	reg.OnRegister(func(name string, impl ToolImpl) {
		atomic.AddInt32(&called, 1)
		capturedName = name
	})

	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "TestTool", Desc: "A test tool"},
	}
	reg.Register(impl)

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("expected OnRegister hook called once, got %d", called)
	}
	if capturedName != "TestTool" {
		t.Errorf("expected hook to receive name 'TestTool', got %q", capturedName)
	}
}

func TestMultipleOnRegisterHooks(t *testing.T) {
	reg := NewRegistry()
	var count int32

	reg.OnRegister(func(name string, impl ToolImpl) { atomic.AddInt32(&count, 1) })
	reg.OnRegister(func(name string, impl ToolImpl) { atomic.AddInt32(&count, 1) })

	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Multi", Desc: "test"},
	}
	reg.Register(impl)

	if atomic.LoadInt32(&count) != 2 {
		t.Errorf("expected 2 hooks called, got %d", count)
	}
}

func TestOnUnregisterHookFires(t *testing.T) {
	reg := NewRegistry()
	var called int32
	var capturedName string

	reg.OnUnregister(func(name string, impl ToolImpl) {
		atomic.AddInt32(&called, 1)
		capturedName = name
	})

	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Removable", Desc: "will be removed"},
	}
	reg.Register(impl)
	reg.Unregister("Removable")

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("expected OnUnregister hook called once, got %d", called)
	}
	if capturedName != "Removable" {
		t.Errorf("expected hook to receive name 'Removable', got %q", capturedName)
	}
}

func TestUnregisterNonexistentNoHookFire(t *testing.T) {
	reg := NewRegistry()
	var called int32

	reg.OnUnregister(func(name string, impl ToolImpl) {
		atomic.AddInt32(&called, 1)
	})

	reg.Unregister("DoesNotExist")

	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("expected no hook call for non-existent tool, got %d", called)
	}
}

// --- Enable/Disable tests ---

func TestDisableHidesTool(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Disableable", Desc: "test"},
	}
	reg.Register(impl)

	// Before disable: tool is accessible.
	if _, ok := reg.Get("Disableable"); !ok {
		t.Fatal("tool should be accessible before disable")
	}

	reg.Disable("Disableable")

	// After disable: tool is not accessible via Get.
	if _, ok := reg.Get("Disableable"); ok {
		t.Error("disabled tool should not be returned by Get")
	}

	// But GetIncludeDisabled still finds it.
	if _, ok := reg.GetIncludeDisabled("Disableable"); !ok {
		t.Error("GetIncludeDisabled should find disabled tool")
	}

	// Not in List output.
	for _, info := range reg.List() {
		if info.Name == "Disableable" {
			t.Error("disabled tool should not appear in List")
		}
	}
}

func TestEnableRestoresTool(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Toggle", Desc: "test"},
	}
	reg.Register(impl)

	reg.Disable("Toggle")
	reg.Enable("Toggle")

	if _, ok := reg.Get("Toggle"); !ok {
		t.Error("re-enabled tool should be accessible via Get")
	}
}

func TestIsEnabled(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Check", Desc: "test"},
	}
	reg.Register(impl)

	if !reg.IsEnabled("Check") {
		t.Error("newly registered tool should be enabled")
	}

	reg.Disable("Check")
	if reg.IsEnabled("Check") {
		t.Error("disabled tool should not be enabled")
	}

	reg.Enable("Check")
	if !reg.IsEnabled("Check") {
		t.Error("re-enabled tool should be enabled")
	}

	// Non-existent tool.
	if reg.IsEnabled("Nope") {
		t.Error("non-existent tool should not be enabled")
	}
}

// --- Metadata tests ---

func TestMetadataRegisteredAtSet(t *testing.T) {
	reg := NewRegistry()
	before := time.Now()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Meta", Desc: "test"},
	}
	reg.Register(impl)
	after := time.Now()

	meta := reg.GetMetadata("Meta")
	if meta == nil {
		t.Fatal("metadata should exist after register")
		return
	}
	if meta.RegisteredAt.Before(before) || meta.RegisteredAt.After(after) {
		t.Errorf("RegisteredAt %v not between %v and %v", meta.RegisteredAt, before, after)
	}
	if meta.Name != "Meta" {
		t.Errorf("expected meta name 'Meta', got %q", meta.Name)
	}
	if !meta.Enabled {
		t.Error("newly registered tool metadata should be enabled")
	}
}

func TestRecordCallUpdatesMetadata(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Counter", Desc: "test"},
	}
	reg.Register(impl)

	reg.RecordCall("Counter", 100*time.Millisecond, nil)
	reg.RecordCall("Counter", 200*time.Millisecond, nil)

	meta := reg.GetMetadata("Counter")
	if meta == nil {
		t.Fatal("metadata should exist")
		return
	}
	if meta.CallCount != 2 {
		t.Errorf("expected CallCount=2, got %d", meta.CallCount)
	}

	avg := meta.AverageDuration()
	expected := 150 * time.Millisecond
	if avg != expected {
		t.Errorf("expected average duration %v, got %v", expected, avg)
	}
}

func TestRecordCallWithError(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Errs", Desc: "test"},
	}
	reg.Register(impl)

	testErr := fmt.Errorf("something failed")
	reg.RecordCall("Errs", 50*time.Millisecond, testErr)

	meta := reg.GetMetadata("Errs")
	if meta == nil {
		t.Fatal("metadata should exist")
		return
	}
	if meta.LastError != "something failed" {
		t.Errorf("expected LastError='something failed', got %q", meta.LastError)
	}
	if meta.LastErrorAt.IsZero() {
		t.Error("LastErrorAt should be set")
	}
}

func TestMetadataForNonexistentTool(t *testing.T) {
	reg := NewRegistry()
	if meta := reg.GetMetadata("ghost"); meta != nil {
		t.Error("expected nil metadata for non-existent tool")
	}
}

// --- Unregister tests ---

func TestUnregisterRemovesTool(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info:    &schema.ToolInfo{Name: "Temp", Desc: "temporary"},
		Aliases: []string{"TempAlias"},
	}
	reg.Register(impl)

	// Verify it exists.
	if _, ok := reg.Get("Temp"); !ok {
		t.Fatal("tool should exist before unregister")
	}
	if _, ok := reg.Get("TempAlias"); !ok {
		t.Fatal("alias should exist before unregister")
	}

	reg.Unregister("Temp")

	// Verify removed.
	if _, ok := reg.Get("Temp"); ok {
		t.Error("tool should not exist after unregister")
	}
	if _, ok := reg.Get("TempAlias"); ok {
		t.Error("alias should be removed after unregister")
	}
	if meta := reg.GetMetadata("Temp"); meta != nil {
		t.Error("metadata should be removed after unregister")
	}

	// Not in list.
	for _, info := range reg.List() {
		if info.Name == "Temp" {
			t.Error("unregistered tool should not appear in List")
		}
	}
}

// --- Concurrency test ---

func TestRegistryConcurrentAccess(t *testing.T) {
	reg := NewRegistry()
	reg.OnRegister(func(name string, impl ToolImpl) {})
	reg.OnUnregister(func(name string, impl ToolImpl) {})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("Tool%d", idx)
			impl := ToolImpl{
				Info: &schema.ToolInfo{Name: name, Desc: "concurrent test"},
			}
			reg.Register(impl)
			reg.Get(name)
			reg.RecordCall(name, time.Millisecond, nil)
			reg.GetMetadata(name)
			reg.IsEnabled(name)
			if idx%5 == 0 {
				reg.Disable(name)
				reg.Enable(name)
			}
			if idx%10 == 0 {
				reg.Unregister(name)
			}
		}(i)
	}
	wg.Wait()
}

// --- Update test ---

func TestUpdateReplacesImpl(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Updatable", Desc: "original"},
	}
	reg.Register(impl)

	impl2 := ToolImpl{
		Info: &schema.ToolInfo{Name: "Updatable", Desc: "updated"},
	}
	reg.Update("Updatable", impl2)

	got, ok := reg.Get("Updatable")
	if !ok {
		t.Fatal("tool should still exist after update")
	}
	if got.Info.Desc != "updated" {
		t.Errorf("expected updated desc, got %q", got.Info.Desc)
	}
}

func TestUpdateNonexistentNoOp(t *testing.T) {
	reg := NewRegistry()
	impl := ToolImpl{
		Info: &schema.ToolInfo{Name: "Ghost", Desc: "does not exist"},
	}
	reg.Update("Ghost", impl) // Should not panic or add the tool.
	if _, ok := reg.Get("Ghost"); ok {
		t.Error("Update should not add a non-existent tool")
	}
}

// --- Names test ---

func TestNamesReturnsRegistrationOrder(t *testing.T) {
	reg := NewRegistry()
	reg.Register(ToolImpl{Info: &schema.ToolInfo{Name: "B", Desc: "b"}})
	reg.Register(ToolImpl{Info: &schema.ToolInfo{Name: "A", Desc: "a"}})
	reg.Register(ToolImpl{Info: &schema.ToolInfo{Name: "C", Desc: "c"}})

	names := reg.Names()
	expected := []string{"B", "A", "C"}
	if len(names) != len(expected) {
		t.Fatalf("expected %d names, got %d", len(expected), len(names))
	}
	for i, n := range expected {
		if names[i] != n {
			t.Errorf("names[%d] = %q, want %q", i, names[i], n)
		}
	}
}
