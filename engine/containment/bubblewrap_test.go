package containment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBubblewrapProbeBindsOnlyAdapterOwnedAxes(t *testing.T) {
	identity := RootIdentity{Path: t.TempDir(), Device: 1, Inode: 2}
	adapter := newLinuxBubblewrapAdapter(bubblewrapDependencies{
		goos: "linux", goarch: "amd64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
		probe:       func(context.Context, *Snapshot) error { return nil },
	})
	policy := bubblewrapPolicyWithRoot(t, StateDegraded, identity)
	result := adapter.Probe(context.Background(), policy)
	if result.ReasonCode != "" || result.Proof.PolicyDigest != policy.Digest() || result.Proof.CapabilityGeneration != linuxBubblewrapGeneration || result.Proof.Enforced != adapterAllowedAxes {
		t.Fatalf("probe = %#v", result)
	}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, result.Proof)
	if err != nil {
		t.Fatal(err)
	}
	runtimeAxes := AxisRootIdentity | AxisDescendantCleanup | AxisWallTime | AxisOutput
	proof, err := NewExecutionProof(binding, runtimeAxes)
	if err != nil {
		t.Fatal(err)
	}
	identityProjection, err := ExecutionIdentityFor(binding, proof)
	if err != nil || identityProjection.Adapter != AdapterLinuxBubblewrap || identityProjection.Enforced != adapterAllowedAxes|runtimeAxes {
		t.Fatalf("identity = %#v, %v", identityProjection, err)
	}
}

func TestBubblewrapProbeReasonMapping(t *testing.T) {
	identity := RootIdentity{Path: t.TempDir(), Device: 1, Inode: 2}
	policy := bubblewrapPolicyWithRoot(t, StateDegraded, identity)
	unsupported := newLinuxBubblewrapAdapter(bubblewrapDependencies{goos: "darwin", goarch: "arm64"})
	if got := unsupported.Probe(context.Background(), policy).ReasonCode; got != ReasonUnsupportedPlatform {
		t.Fatalf("unsupported reason = %q", got)
	}
	missing := newLinuxBubblewrapAdapter(bubblewrapDependencies{
		goos: "linux", goarch: "amd64", executable: func() error { return errors.New("missing") },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
	})
	if got := missing.Probe(context.Background(), policy).ReasonCode; got != ReasonExecutableMissing {
		t.Fatalf("missing reason = %q", got)
	}
	changed := newLinuxBubblewrapAdapter(bubblewrapDependencies{
		goos: "linux", goarch: "amd64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return RootIdentity{Path: identity.Path, Device: 3, Inode: 4}, nil },
	})
	if got := changed.Probe(context.Background(), policy).ReasonCode; got != ReasonRootChanged {
		t.Fatalf("root reason = %q", got)
	}
	failed := newLinuxBubblewrapAdapter(bubblewrapDependencies{
		goos: "linux", goarch: "amd64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
		probe:       func(context.Context, *Snapshot) error { return errors.New("raw failure") },
	})
	if got := failed.Probe(context.Background(), policy).ReasonCode; got != ReasonProbeFailed {
		t.Fatalf("probe reason = %q", got)
	}
}

func TestBubblewrapPrepareUsesFixedBoundaryAndSeccompFD(t *testing.T) {
	root := t.TempDir()
	denied := filepath.Join(root, "control")
	if err := os.WriteFile(denied, []byte("fixed"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := RootIdentity{Path: root, Device: 1, Inode: 2}
	adapter := newLinuxBubblewrapAdapter(bubblewrapDependencies{
		goos: "linux", goarch: "arm64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
		seccompFile: func() (*os.File, error) {
			return os.CreateTemp(t.TempDir(), "filter-")
		},
	})
	policy := bubblewrapPolicyWithRoot(t, StateDegraded, identity)
	spec := policy.Spec()
	spec.DeniedRoots = []string{denied}
	policy, err := NewExecutionPolicySnapshot(&spec)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{
		PolicyDigest: policy.Digest(), CapabilityGeneration: adapter.CapabilityGeneration(), Enforced: adapterAllowedAxes,
	})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"Z=last", "A=first"}
	spawn, err := binding.Prepare(context.Background(), SpawnRequest{
		Binding: binding, Executable: "/bin/bash", Args: []string{"--norc"}, Dir: root, Env: env,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSpawnExtraFiles(spawn.ExtraFiles) })
	if spawn.Path != linuxBubblewrapExecutable || spawn.Dir != root || spawn.BindingDigest != binding.Digest() || len(spawn.ExtraFiles) != 1 {
		t.Fatalf("spawn = %#v", spawn)
	}
	for _, required := range []string{"--tmpfs", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-net", "--disable-userns", "--cap-drop", "--seccomp", "3", denied, "/bin/bash"} {
		if !slices.Contains(spawn.Args, required) {
			t.Fatalf("args missing %q: %#v", required, spawn.Args)
		}
	}
	if !slices.Equal(spawn.Env, env) {
		t.Fatalf("env = %#v", spawn.Env)
	}
	spawn.Env[0] = "changed"
	if env[0] != "Z=last" {
		t.Fatal("prepare retained environment alias")
	}
}

func bubblewrapPolicyWithRoot(t *testing.T, state State, root RootIdentity) *Snapshot {
	t.Helper()
	spec := Spec{
		Version: PolicyVersion, Profile: ProfileWorkspaceWrite, State: state,
		SelectionSource: SelectionDefault, Adapter: AdapterLinuxBubblewrap,
		Platform: "linux", Architecture: "amd64", CapabilityGeneration: linuxBubblewrapGeneration,
		CWD: root.Path, Root: root, ReadRoots: []string{root.Path}, WriteRoots: []string{root.Path},
		Network:     NetworkPolicy{Mode: NetworkDenied, ProjectionID: "guest-denied"},
		Environment: EnvironmentPolicy{ProjectionID: "ambient-env"},
		Credentials: CredentialPolicy{Mode: CredentialAmbientEnvironment, ProjectionID: "ambient-env"},
		Resources:   ResourceLimits{WallTimeMillis: 60_000, OutputBytes: 1 << 20},
		Descendants: DescendantPolicy{Mode: DescendantCleanupRequired},
		Entrypoint:  EntrypointTUI, Lineage: Lineage{RootID: "root"},
	}
	policy, err := NewExecutionPolicySnapshot(&spec)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
