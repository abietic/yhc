package hooks

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/containment"
)

type recordingAmbientAdapter struct {
	prepared chan struct{}
}

func (*recordingAmbientAdapter) Family() containment.AdapterFamily {
	return containment.AdapterAmbientHost
}

func (*recordingAmbientAdapter) CapabilityGeneration() string { return "" }

func (*recordingAmbientAdapter) Probe(_ context.Context, policy *containment.Snapshot) containment.ProbeResult {
	return containment.ProbeResult{Diagnostic: policy.Diagnostic()}
}

func (a *recordingAmbientAdapter) Prepare(_ context.Context, request containment.SpawnRequest) (containment.SpawnSpec, error) {
	a.prepared <- struct{}{}
	return containment.SpawnSpec{
		Path:          request.Executable,
		Args:          append([]string(nil), request.Args...),
		Dir:           request.Dir,
		Env:           append([]string(nil), request.Env...),
		BindingDigest: request.Binding.Digest(),
	}, nil
}

func TestExecutorBindExecutionBindingRejectsWrongClassAndPolicyReplacement(t *testing.T) {
	policy, err := containment.NewAmbientHostSnapshot("", containment.EntrypointEmbedded, containment.SelectionCompatibilityDefault)
	if err != nil {
		t.Fatal(err)
	}
	adapter := containment.NewAmbientHostAdapter()
	wrong, err := containment.NewBinding(containment.ProcessClassStdioMCP, policy, adapter, containment.AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	if err := executor.BindExecutionBinding(wrong); err == nil {
		t.Fatal("wrong process class was accepted")
	}
	binding, err := containment.NewBinding(containment.ProcessClassShellHooks, policy, adapter, containment.AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.BindExecutionBinding(binding); err != nil {
		t.Fatal(err)
	}
	if got := executor.ExecutionBindingDigest(); got != binding.Digest() {
		t.Fatalf("binding digest = %q, want %q", got, binding.Digest())
	}
	other, err := containment.NewAmbientHostSnapshot("other", containment.EntrypointEmbedded, containment.SelectionCompatibilityDefault)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.BindExecutionPolicy(other); err == nil {
		t.Fatal("policy replacement was accepted after binding")
	}
}

func TestExecutorBoundAsyncContextCapturesBinding(t *testing.T) {
	policy, _ := containment.NewAmbientHostSnapshot("", containment.EntrypointEmbedded, containment.SelectionCompatibilityDefault)
	binding, _ := containment.NewBinding(containment.ProcessClassShellHooks, policy, containment.NewAmbientHostAdapter(), containment.AdapterProof{})
	executor := NewExecutor()
	if err := executor.BindExecutionBinding(binding); err != nil {
		t.Fatal(err)
	}
	ctx := executor.asyncShellContext(context.Background())
	if got := executionBinding(ctx); got != binding {
		t.Fatal("async context did not capture executor binding")
	}
}

func TestExecutorExplicitHookBindingOwnsAsyncProcessIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertion requires a POSIX shell")
	}
	policy, err := containment.NewAmbientHostSnapshot(t.TempDir(), containment.EntrypointACP, containment.SelectionCompatibilityDefault)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &recordingAmbientAdapter{prepared: make(chan struct{}, 1)}
	binding, err := containment.NewBinding(containment.ProcessClassShellHooks, policy, adapter, containment.AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor()
	if err := executor.BindExecutionBinding(binding); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = executor.ShutdownAsyncShellHooks(ctx)
	})

	updates := make(chan AsyncShellHookCompletion, 4)
	executor.SetAsyncShellCompletionHandler(func(update AsyncShellHookCompletion) { updates <- update })
	executor.RegisterShellHooks(&ShellHookConfig{PreToolHooks: []ShellHook{{
		Command: "printf bound", ToolPattern: "Bash", Async: true,
	}}})
	guestContext := containment.WithSnapshot(
		context.Background(),
		containment.DisabledCompatibilitySnapshot(t.TempDir(), containment.EntrypointACP),
	)
	executor.ExecutePreTool(guestContext, "Bash", "tool-bound", map[string]any{"command": "pwd"})
	_ = waitAsyncShellUpdate(t, updates, "running")
	completed := waitAsyncShellUpdate(t, updates, "completed")
	if completed.Result.StartFailed || completed.Result.Stdout != "bound" {
		t.Fatalf("bound async hook result = %#v", completed.Result)
	}
	select {
	case <-adapter.prepared:
	case <-time.After(time.Second):
		t.Fatal("explicit hook binding did not prepare the asynchronous process")
	}
}
