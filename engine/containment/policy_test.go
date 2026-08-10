package containment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"testing"
)

func TestSnapshotCanonicalDigestAndCopiesCallerValues(t *testing.T) {
	first := testSpec(t)
	first.ReadRoots = []string{"./second", "./first", "./second"}
	first.Environment.Names = []string{"PATH", "HOME", "PATH"}
	first.Credentials.SocketIDs = []string{"socket-b", "socket-a", "socket-b"}
	second := first
	second.ReadRoots = []string{"./first", "./second"}
	second.Environment.Names = []string{"HOME", "PATH"}
	second.Credentials.SocketIDs = []string{"socket-a", "socket-b"}

	left, err := NewExecutionPolicySnapshot(&first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewExecutionPolicySnapshot(&second)
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest() != right.Digest() {
		t.Fatalf("digest changed with order/dedup: %s != %s", left.Digest(), right.Digest())
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(left.Digest()) {
		t.Fatalf("digest is not sha256 hex: %q", left.Digest())
	}

	before := left.Spec()
	first.ReadRoots[0] = "/caller-mutation"
	first.Environment.Names[0] = "CALLER_MUTATION"
	first.Credentials.SocketIDs[0] = "caller-mutation"
	if got := left.Spec(); !reflect.DeepEqual(got, before) {
		t.Fatalf("snapshot retained caller alias: %#v", got)
	}
	copy := left.Spec()
	copy.ReadRoots[0] = "/returned-copy-mutation"
	if got := left.Spec(); !reflect.DeepEqual(got, before) {
		t.Fatalf("Spec returned an internal alias: %#v", got)
	}
}

func TestSnapshotRejectsInvalidProfileEntrypointIdentityAndLimits(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"profile-state", func(spec *Spec) { spec.Profile = ProfileReadOnly }},
		{"entrypoint", func(spec *Spec) { spec.Entrypoint = "unknown" }},
		{"identity", func(spec *Spec) { spec.Platform = "invalid/path" }},
		{"environment-value", func(spec *Spec) { spec.Environment.Names = []string{"TOKEN=secret"} }},
		{"negative-resource", func(spec *Spec) { spec.Resources.OutputBytes = -1 }},
		{"negative-descendants", func(spec *Spec) { spec.Descendants.MaxDescendants = -1 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			spec := testSpec(t)
			test.mutate(&spec)
			_, err := NewExecutionPolicySnapshot(&spec)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
		})
	}
	if snapshot, err := NewExecutionPolicySnapshot(nil); err == nil || snapshot != nil {
		t.Fatalf("nil spec = (%v, %v), want error", snapshot, err)
	}
}

func TestDiagnosticIsRedactedAndCompatibilityIsDisabled(t *testing.T) {
	spec := testSpec(t)
	spec.CWD = "/diagnostic-root-sentinel"
	spec.ReadRoots = []string{"/read-root-sentinel"}
	spec.WriteRoots = []string{"/write-root-sentinel"}
	spec.Environment.Names = []string{"SENTINEL_ENV"}
	snapshot, err := NewExecutionPolicySnapshot(&spec)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := fmt.Sprintf("%+v", snapshot.Diagnostic())
	for _, forbidden := range []string{"diagnostic-root-sentinel", "read-root-sentinel", "write-root-sentinel", "SENTINEL_ENV"} {
		if contains(diagnostic, forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, diagnostic)
		}
	}
	compatibility := DisabledCompatibility("", EntrypointTUI)
	if compatibility == nil || compatibility.Digest() == "" {
		t.Fatal("compatibility constructor returned no usable snapshot")
	}
	if got := compatibility.Spec(); got.Profile != ProfileDangerFullAccess || got.State != StateDisabled || got.Adapter != AdapterAmbientHost || got.SelectionSource != "compatibility-default" {
		t.Fatalf("compatibility policy = %#v", got)
	}
	if compatibility.Diagnostic().State == StateEnforced {
		t.Fatal("compatibility policy claimed enforcement")
	}
	if DisabledCompatibility("", "not-an-entrypoint").Spec().Entrypoint != EntrypointEmbedded {
		t.Fatal("invalid compatibility entrypoint did not normalize")
	}
}

func TestDisabledChildDerivationIsExactOrRejected(t *testing.T) {
	parent := DisabledCompatibility("/parent-cwd", EntrypointTUI)
	child, err := parent.DeriveChild("/child-cwd", nil)
	if err != nil {
		t.Fatal(err)
	}
	childSpec := child.Spec()
	if childSpec.CWD == parent.Spec().CWD || childSpec.Entrypoint != EntrypointChildAgent ||
		childSpec.Lineage.ParentDigest != parent.Digest() || childSpec.Lineage.ChildID == "" {
		t.Fatalf("child did not retain exact derivation identity: %#v", childSpec)
	}
	if _, err := parent.DeriveChild("/child-cwd", &childSpec); err != nil {
		t.Fatalf("exact requested child rejected: %v", err)
	}
	left, err := parent.DeriveExactChildFor("/child-cwd", "agent-left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := parent.DeriveExactChildFor("/child-cwd", "agent-right")
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest() == right.Digest() || left.Spec().Lineage.ChildID == "agent-left" {
		t.Fatalf("child identities were not opaque and distinct: left=%#v right=%#v", left.Spec().Lineage, right.Spec().Lineage)
	}

	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"roots", func(spec *Spec) { spec.ReadRoots = []string{"/broader-root"} }},
		{"network", func(spec *Spec) { spec.Network.Mode = NetworkDenied }},
		{"environment", func(spec *Spec) { spec.Environment.Names = []string{"PATH"} }},
		{"credentials", func(spec *Spec) { spec.Credentials.SocketIDs = []string{"new-socket"} }},
		{"resources", func(spec *Spec) { spec.Resources.OutputBytes = 1 }},
		{"profile", func(spec *Spec) { spec.Profile = ProfileWorkspaceWrite }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			requested := child.Spec()
			test.mutate(&requested)
			_, err := parent.DeriveChild("/child-cwd", &requested)
			var derivation *DerivationError
			if !errors.As(err, &derivation) {
				t.Fatalf("error = %v, want DerivationError", err)
			}
		})
	}
	var nilSnapshot *ExecutionPolicySnapshot
	if _, err := nilSnapshot.DeriveChild("/child-cwd", nil); err == nil {
		t.Fatal("nil parent derivation succeeded")
	}
}

func TestContextBindingDoesNotSynthesizePolicy(t *testing.T) {
	//nolint:staticcheck // Nil-context tolerance is an explicit compatibility contract.
	if snapshot, ok := FromContext(nil); snapshot != nil || ok {
		t.Fatalf("nil context = (%v, %v), want absent", snapshot, ok)
	}
	if snapshot, ok := FromContext(context.Background()); snapshot != nil || ok {
		t.Fatalf("unbound context = (%v, %v), want absent", snapshot, ok)
	}
	bound := DisabledCompatibilitySnapshot("", EntrypointPlain)
	//nolint:staticcheck // Nil-context tolerance is an explicit compatibility contract.
	got, ok := FromContext(WithSnapshot(nil, bound))
	if !ok || got != bound {
		t.Fatalf("bound snapshot = (%v, %v), want exact pointer", got, ok)
	}
}

func TestP511PolicyIdentityExcludesProofAndRedactsRootIdentity(t *testing.T) {
	workspace := t.TempDir()
	spec := darwinWorkspaceSpec(t, workspace, StateDegraded)
	left, err := NewExecutionPolicySnapshot(&spec)
	if err != nil {
		t.Fatal(err)
	}
	withoutProof := spec
	withoutProof.CapabilityGeneration = "generation-2"
	right, err := NewExecutionPolicySnapshot(&withoutProof)
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest() == right.Digest() {
		t.Fatal("capability generation did not affect policy identity")
	}
	if got := fmt.Sprintf("%+v", left.Diagnostic()); contains(got, workspace) || contains(got, "RootIdentity") || contains(got, "Device:") || contains(got, "Inode:") {
		t.Fatalf("diagnostic leaked root identity: %s", got)
	}
	legacy := testSpec(t)
	legacy.Version = LegacyDisabledPolicyVersion
	legacy.CapabilityGeneration = ""
	legacy.Credentials.Mode = ""
	if _, err := NewExecutionPolicySnapshot(&legacy); err != nil {
		t.Fatalf("legacy compatibility policy rejected: %v", err)
	}
	legacy.CapabilityGeneration = "generation-1"
	if _, err := NewExecutionPolicySnapshot(&legacy); err == nil {
		t.Fatal("legacy capability claim accepted")
	}
}

func TestP511PolicyRejectsUnsupportedSeatbeltPlatformAndSelection(t *testing.T) {
	spec := darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)
	spec.Platform = "linux"
	if _, err := NewExecutionPolicySnapshot(&spec); err == nil {
		t.Fatal("non-Darwin Seatbelt policy accepted")
	}
	spec = darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)
	spec.Architecture = "386"
	if _, err := NewExecutionPolicySnapshot(&spec); err == nil {
		t.Fatal("unsupported Darwin architecture accepted")
	}
	spec = darwinWorkspaceSpec(t, t.TempDir(), StateUnavailable)
	spec.Platform, spec.Architecture = "linux", "amd64"
	spec.Root.Device, spec.Root.Inode = 0, 0
	if _, err := NewExecutionPolicySnapshot(&spec); err != nil {
		t.Fatalf("unsupported platform unavailable policy rejected: %v", err)
	}
	spec = darwinWorkspaceSpec(t, t.TempDir(), StateDegraded)
	spec.SelectionSource = "untrusted"
	if _, err := NewExecutionPolicySnapshot(&spec); err == nil {
		t.Fatal("unknown selection source accepted")
	}
}

func testSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{
		Version: PolicyVersion, Profile: ProfileDangerFullAccess, State: StateDisabled,
		SelectionSource: "compatibility-default", Adapter: AdapterAmbientHost,
		Platform: "darwin", Architecture: "arm64", CapabilityGeneration: "generation-1",
		CWD: t.TempDir(), Network: NetworkPolicy{Mode: NetworkAmbient, ProjectionID: "ambient-host"},
		Environment: EnvironmentPolicy{ProjectionID: "ambient-host"},
		Credentials: CredentialPolicy{Mode: CredentialAmbientEnvironment, ProjectionID: "ambient-host"},
		Descendants: DescendantPolicy{Mode: DescendantAmbient}, Entrypoint: EntrypointTUI,
		Lineage: Lineage{RootID: "root-1"},
	}
}

func darwinWorkspaceSpec(t *testing.T, workspace string, state State) Spec {
	t.Helper()
	return Spec{
		Version: PolicyVersion, Profile: ProfileWorkspaceWrite, State: state,
		SelectionSource: "compatibility-default", Adapter: AdapterDarwinSeatbelt,
		Platform: "darwin", Architecture: "arm64", CapabilityGeneration: "generation-1",
		CWD: workspace, WriteRoots: []string{workspace}, Network: NetworkPolicy{Mode: NetworkDenied, ProjectionID: "network-denied"},
		Environment: EnvironmentPolicy{ProjectionID: "ambient-environment"},
		Credentials: CredentialPolicy{Mode: CredentialAmbientEnvironment, ProjectionID: "ambient-environment"},
		Descendants: DescendantPolicy{Mode: DescendantCleanupRequired}, Entrypoint: EntrypointTUI,
		Lineage: Lineage{RootID: "root-1"}, Root: RootIdentity{Path: workspace, Device: 1, Inode: 2},
	}
}

func contains(value, fragment string) bool {
	return len(fragment) <= len(value) && (fragment == "" || containsAt(value, fragment))
}

func containsAt(value, fragment string) bool {
	for index := 0; index <= len(value)-len(fragment); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
