package containment

import (
	"context"
	"reflect"
	"testing"
)

func TestAmbientAdapterPreservesSpawnRequestWithoutProof(t *testing.T) {
	policy, err := NewAmbientHostSnapshot(t.TempDir(), EntrypointHeadless, SelectionCLI)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAmbientHostAdapter()
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	request := SpawnRequest{
		Binding:    binding,
		Executable: "/bin/bash",
		Args:       []string{"--noprofile", "--norc"},
		Dir:        policy.Spec().CWD,
		Env:        []string{"DUP=first", "DUP=second", "EMPTY="},
	}
	spec, err := binding.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Path != request.Executable || spec.Dir != request.Dir || spec.BindingDigest != binding.Digest() ||
		!reflect.DeepEqual(spec.Args, request.Args) || !reflect.DeepEqual(spec.Env, request.Env) {
		t.Fatalf("ambient spawn spec = %#v", spec)
	}
	request.Args[0], request.Env[0] = "mutated", "MUTATED=1"
	if spec.Args[0] != "--noprofile" || spec.Env[0] != "DUP=first" {
		t.Fatal("ambient spawn spec retained caller slices")
	}
	probe := adapter.Probe(context.Background(), policy)
	if probe.Proof != (AdapterProof{}) || probe.ReasonCode != "" {
		t.Fatalf("ambient probe fabricated proof: %#v", probe)
	}
	if _, err := NewExecutionProof(binding, AxisWallTime); err == nil {
		t.Fatal("ambient binding generated execution proof")
	}
}

func TestAmbientHostSnapshotRecordsOnlyUserOwnedSelection(t *testing.T) {
	for _, source := range []SelectionSource{SelectionCompatibilityDefault, SelectionUserConfig, SelectionCLI} {
		policy, err := NewAmbientHostSnapshot(t.TempDir(), EntrypointACP, source)
		if err != nil {
			t.Fatalf("source %q: %v", source, err)
		}
		spec := policy.Spec()
		if spec.SelectionSource != source || spec.Profile != ProfileDangerFullAccess || spec.State != StateDisabled || spec.Adapter != AdapterAmbientHost {
			t.Fatalf("source %q ambient policy = %#v", source, spec)
		}
	}
	if _, err := NewAmbientHostSnapshot(t.TempDir(), EntrypointACP, SelectionDefault); err == nil {
		t.Fatal("default source selected danger-full-access")
	}
	if _, err := NewAmbientHostSnapshot(t.TempDir(), EntrypointACP, SelectionChild); err == nil {
		t.Fatal("child source selected danger-full-access")
	}
}
