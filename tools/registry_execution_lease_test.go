package tools

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestRegistryExecutionLeaseSerializesMutationWithDispatch(t *testing.T) {
	registry := NewRegistry()
	var executions atomic.Int32
	implementation := ToolImpl{
		Info: &schema.ToolInfo{Name: "leased"},
		Execute: func(string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
		Capabilities: ToolCapabilities{
			Declared:   true,
			Origin:     ToolOriginBuiltin,
			ActionKind: ToolActionRuntimeState,
		},
	}
	registry.Register(implementation)
	resolution := registry.Resolve("leased")
	lease, err := registry.AcquireExecution(
		"leased",
		"leased",
		resolution.Generation,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Cancel)

	mutated := make(chan struct{})
	go func() {
		registry.Disable("leased")
		close(mutated)
	}()
	select {
	case <-mutated:
		t.Fatal("registry mutation crossed an unconsumed execution lease")
	case <-time.After(25 * time.Millisecond):
	}
	if result, err := lease.Execute(
		context.Background(),
		`{}`,
	); err != nil || result != "ok" {
		t.Fatalf("lease execution = (%q, %v)", result, err)
	}
	select {
	case <-mutated:
	case <-time.After(time.Second):
		t.Fatal("registry mutation remained blocked after dispatch")
	}
	if executions.Load() != 1 || registry.IsEnabled("leased") {
		t.Fatalf(
			"execution/enabled = %d/%v",
			executions.Load(),
			registry.IsEnabled("leased"),
		)
	}
}

func TestRegistryExecutionLeaseRejectsStaleGenerationAndCanonicalName(
	t *testing.T,
) {
	registry := NewRegistry()
	implementation := ToolImpl{
		Info:    &schema.ToolInfo{Name: "canonical"},
		Aliases: []string{"alias"},
		Capabilities: ToolCapabilities{
			Declared:   true,
			Origin:     ToolOriginBuiltin,
			ActionKind: ToolActionRuntimeState,
		},
	}
	registry.Register(implementation)
	resolution := registry.Resolve("alias")
	registry.Update("canonical", implementation)
	if lease, err := registry.AcquireExecution(
		"alias",
		"canonical",
		resolution.Generation,
	); err == nil {
		lease.Cancel()
		t.Fatal("stale generation acquired an execution lease")
	}
	if lease, err := registry.AcquireExecution(
		"alias",
		"other",
		registry.Generation(),
	); err == nil {
		lease.Cancel()
		t.Fatal("mismatched canonical identity acquired an execution lease")
	}
}
