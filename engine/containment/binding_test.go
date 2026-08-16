package containment

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type testAdapter struct {
	family     AdapterFamily
	generation string
}

func (a *testAdapter) Family() AdapterFamily        { return a.family }
func (a *testAdapter) CapabilityGeneration() string { return a.generation }
func (a *testAdapter) Probe(context.Context, *Snapshot) ProbeResult {
	return ProbeResult{ReasonCode: ReasonProbeFailed}
}

func (a *testAdapter) Prepare(_ context.Context, request SpawnRequest) (SpawnSpec, error) {
	return SpawnSpec{Path: request.Executable, Args: append([]string(nil), request.Args...), Dir: request.Dir, Env: append([]string(nil), request.Env...), BindingDigest: request.Binding.Digest()}, nil
}

func TestBindingValidatesDarwinProofAndIsImmutable(t *testing.T) {
	policy, err := NewExecutionPolicySnapshot(ptr(darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-1"}
	proof := AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "generation-1", Enforced: adapterAllowedAxes}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, proof)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Digest() == "" || binding.PolicyDigest() != policy.Digest() || binding.AdapterAxes() != adapterAllowedAxes {
		t.Fatalf("binding = %#v", binding.Diagnostic())
	}
	if _, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "generation-1", Enforced: AxisMemory}); err == nil {
		t.Fatal("memory proof accepted")
	}
	if _, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "generation-1", Enforced: AxisFilesystemRead}); err == nil {
		t.Fatal("partial Darwin proof accepted")
	}
	before := binding.Digest()
	proof.Enforced = 0
	if binding.Digest() != before || binding.AdapterAxes() != adapterAllowedAxes {
		t.Fatal("binding retained proof alias")
	}
	if got := fmt.Sprintf("%+v", binding.Diagnostic()); contains(got, policy.Spec().CWD) || contains(got, "generation-1") {
		t.Fatalf("binding diagnostic leaked protected identity: %s", got)
	}
	adapter.generation = "generation-2"
	if _, err := binding.Prepare(context.Background(), SpawnRequest{Binding: binding, Executable: "/bin/sh"}); err == nil {
		t.Fatal("adapter generation drift prepared a process")
	}
}

func TestBindingUnavailableAndBindingsAreFailClosed(t *testing.T) {
	policySpec := darwinWorkspaceSpec(t, t.TempDir(), StateUnavailable)
	policy, err := NewExecutionPolicySnapshot(&policySpec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-1"}
	unavailable, err := NewUnavailableBinding(ProcessClassGuest, policy, adapter, ReasonProbeFailed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unavailable.Prepare(context.Background(), SpawnRequest{Binding: unavailable, Executable: "/bin/sh"}); err == nil {
		t.Fatal("unavailable binding prepared")
	}
	if _, err := NewUnavailableBinding(ProcessClassGuest, policy, adapter, ""); err == nil {
		t.Fatal("empty reason accepted")
	}

	ambient := DisabledCompatibilitySnapshot(t.TempDir(), EntrypointTUI)
	ambientAdapter := &testAdapter{family: AdapterAmbientHost}
	hooks, err := NewBinding(ProcessClassShellHooks, ambient, ambientAdapter, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := NewBinding(ProcessClassStdioMCP, ambient, ambientAdapter, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := NewBindings(unavailable, hooks, mcp)
	if err != nil {
		t.Fatal(err)
	}
	if bindings.Guest() != unavailable || bindings.ShellHooks() != hooks || bindings.StdioMCP() != mcp {
		t.Fatal("bindings lost process-class identity")
	}
	if _, err := NewBindings(hooks, hooks, mcp); err == nil {
		t.Fatal("misclassified guest accepted")
	}
}

func TestBindingDigestAndExecutionProofAreOneWay(t *testing.T) {
	policy, err := NewExecutionPolicySnapshot(ptr(darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-1"}
	proof := AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "generation-1", Enforced: adapterAllowedAxes}
	guest, err := NewBinding(ProcessClassGuest, policy, adapter, proof)
	if err != nil {
		t.Fatal(err)
	}
	ambient := DisabledCompatibilitySnapshot(t.TempDir(), EntrypointTUI)
	ambientAdapter := &testAdapter{family: AdapterAmbientHost}
	other, err := NewBinding(ProcessClassShellHooks, ambient, ambientAdapter, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	if guest.Digest() == other.Digest() {
		t.Fatal("process class did not affect binding digest")
	}
	unavailableSpec := darwinWorkspaceSpec(t, t.TempDir(), StateUnavailable)
	unavailablePolicy, err := NewExecutionPolicySnapshot(&unavailableSpec)
	if err != nil {
		t.Fatal(err)
	}
	unavailable, err := NewUnavailableBinding(ProcessClassGuest, unavailablePolicy, adapter, ReasonProbeFailed)
	if err != nil {
		t.Fatal(err)
	}
	if guest.Digest() == unavailable.Digest() {
		t.Fatal("availability did not affect binding digest")
	}
	execution, err := NewExecutionProof(guest, AxisRootIdentity|AxisDescendantCleanup|AxisWallTime|AxisOutput)
	if err != nil {
		t.Fatal(err)
	}
	if execution.BindingDigest != guest.Digest() || execution.PolicyDigest != policy.Digest() || execution.Enforced&AxisRootIdentity == 0 {
		t.Fatalf("execution proof = %#v", execution)
	}
	if policy.Digest() != guest.PolicyDigest() {
		t.Fatal("execution proof fed back into policy")
	}
	if _, err := NewExecutionProof(guest, AxisMemory); err == nil {
		t.Fatal("runtime memory proof accepted")
	}
	if _, err := NewExecutionProof(guest, AxisFilesystemRead); err == nil {
		t.Fatal("cross-owner runtime proof accepted")
	}
	var validation *ValidationError
	if _, err := NewExecutionProof(guest, AxisMemory); !errors.As(err, &validation) {
		t.Fatalf("error = %v, want ValidationError", err)
	}
}

func TestBindingRejectsAmbientProofAndUnavailableReasons(t *testing.T) {
	ambient := DisabledCompatibilitySnapshot(t.TempDir(), EntrypointTUI)
	adapter := &testAdapter{family: AdapterAmbientHost}
	guest, err := NewBinding(ProcessClassGuest, ambient, adapter, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutionProof(guest, AxisWallTime); err == nil {
		t.Fatal("ambient binding generated execution proof")
	}
	if _, err := NewBinding(ProcessClassGuest, ambient, adapter, AdapterProof{Enforced: AxisFilesystemRead}); err == nil {
		t.Fatal("ambient proof accepted")
	}
	policySpec := darwinWorkspaceSpec(t, t.TempDir(), StateUnavailable)
	policy, err := NewExecutionPolicySnapshot(&policySpec)
	if err != nil {
		t.Fatal(err)
	}
	seatbelt := &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-1"}
	if _, err := NewUnavailableBinding(ProcessClassGuest, policy, seatbelt, ReasonRootChanged); err != nil {
		t.Fatalf("root-change unavailable reason rejected: %v", err)
	}
	if _, err := NewUnavailableBinding(ProcessClassGuest, policy, seatbelt, ReasonOperationDenied); err == nil {
		t.Fatal("runtime-only unavailable reason accepted")
	}
	if _, err := NewBinding(ProcessClassShellHooks, policy, seatbelt, AdapterProof{}); err == nil {
		t.Fatal("seatbelt hook accepted")
	}
}

func TestP512ExecutionIdentityBindsCompleteGuestProof(t *testing.T) {
	policy, err := NewExecutionPolicySnapshot(ptr(darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-1"}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "generation-1", Enforced: adapterAllowedAxes})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewExecutionProof(binding, runtimeAllowedAxes)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ExecutionIdentityFor(binding, proof)
	if err != nil {
		t.Fatal(err)
	}
	if identity.BindingDigest != binding.Digest() || identity.PolicyDigest != policy.Digest() || identity.Enforced != adapterAllowedAxes|runtimeAllowedAxes || identity.Root != policy.Spec().Root {
		t.Fatalf("identity = %#v", identity)
	}
	if got := fmt.Sprintf("%+v", identity.Diagnostic()); contains(got, identity.Root.Path) || contains(got, "Device:") || contains(got, "Inode:") {
		t.Fatalf("identity diagnostic leaked root: %s", got)
	}
}

func TestP512ExecutionIdentityRejectsInvalidProofs(t *testing.T) {
	policy, err := NewExecutionPolicySnapshot(ptr(darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-1"}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "generation-1", Enforced: adapterAllowedAxes})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewExecutionProof(binding, runtimeAllowedAxes)
	if err != nil {
		t.Fatal(err)
	}
	otherSpec := policy.Spec()
	otherSpec.CapabilityGeneration = "generation-2"
	other, err := NewExecutionPolicySnapshot(&otherSpec)
	if err != nil {
		t.Fatal(err)
	}
	otherBinding, err := NewBinding(ProcessClassGuest, other, &testAdapter{family: AdapterDarwinSeatbelt, generation: "generation-2"}, AdapterProof{PolicyDigest: other.Digest(), CapabilityGeneration: "generation-2", Enforced: adapterAllowedAxes})
	if err != nil {
		t.Fatal(err)
	}
	otherProof, err := NewExecutionProof(otherBinding, runtimeAllowedAxes)
	if err != nil {
		t.Fatal(err)
	}
	ambient := DisabledCompatibilitySnapshot(t.TempDir(), EntrypointTUI)
	hooks, err := NewBinding(ProcessClassShellHooks, ambient, &testAdapter{family: AdapterAmbientHost}, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		binding *Binding
		proof   ExecutionProof
	}{
		"nil":                 {nil, proof},
		"non-guest":           {hooks, ExecutionProof{}},
		"cross-binding":       {binding, otherProof},
		"overclaimed adapter": {binding, ExecutionProof{BindingDigest: proof.BindingDigest, PolicyDigest: proof.PolicyDigest, CapabilityGeneration: proof.CapabilityGeneration, AdapterAxes: proof.AdapterAxes | AxisMemory, RuntimeAxes: proof.RuntimeAxes, Enforced: proof.Enforced | AxisMemory}},
		"overclaimed runtime": {binding, ExecutionProof{BindingDigest: proof.BindingDigest, PolicyDigest: proof.PolicyDigest, CapabilityGeneration: proof.CapabilityGeneration, AdapterAxes: proof.AdapterAxes, RuntimeAxes: proof.RuntimeAxes | AxisMemory, Enforced: proof.Enforced | AxisMemory}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ExecutionIdentityFor(tc.binding, tc.proof); err == nil {
				t.Fatal("invalid execution identity accepted")
			}
		})
	}
	unavailableSpec := policy.Spec()
	unavailableSpec.State = StateUnavailable
	unavailable, err := NewExecutionPolicySnapshot(&unavailableSpec)
	if err != nil {
		t.Fatal(err)
	}
	unavailableBinding, err := NewUnavailableBinding(ProcessClassGuest, unavailable, adapter, ReasonProbeFailed)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		binding *Binding
		proof   ExecutionProof
	}{
		"unavailable": {unavailableBinding, ExecutionProof{}},
		"ambient":     {mustGuestAmbientBinding(t), ExecutionProof{}},
	} {
		t.Run(name, func(t *testing.T) {
			identity, err := ExecutionIdentityFor(tc.binding, tc.proof)
			if err != nil || identity.Enforced != 0 || identity.AdapterAxes != 0 || identity.RuntimeAxes != 0 {
				t.Fatalf("zero-proof identity = %#v, %v", identity, err)
			}
		})
	}
}

func mustGuestAmbientBinding(t *testing.T) *Binding {
	t.Helper()
	policy := DisabledCompatibilitySnapshot(t.TempDir(), EntrypointTUI)
	binding, err := NewBinding(ProcessClassGuest, policy, &testAdapter{family: AdapterAmbientHost}, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func ptr(value Spec) *Spec { return &value }
