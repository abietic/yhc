package containment

import (
	"context"
	"runtime"
)

// AmbientHostAdapter is the explicit no-containment compatibility adapter.
// It preserves a spawn request and never emits an enforcement proof.
type AmbientHostAdapter struct{}

func NewAmbientHostAdapter() *AmbientHostAdapter { return &AmbientHostAdapter{} }

func (*AmbientHostAdapter) Family() AdapterFamily        { return AdapterAmbientHost }
func (*AmbientHostAdapter) CapabilityGeneration() string { return "" }
func (*AmbientHostAdapter) Probe(_ context.Context, policy *Snapshot) ProbeResult {
	if policy == nil {
		return ProbeResult{}
	}
	return ProbeResult{Diagnostic: policy.Diagnostic()}
}

func (*AmbientHostAdapter) Prepare(_ context.Context, request SpawnRequest) (SpawnSpec, error) {
	if request.Binding == nil || request.Binding.AdapterFamily() != AdapterAmbientHost {
		return SpawnSpec{}, invalid("binding", "ambient binding required")
	}
	return SpawnSpec{
		Path:          request.Executable,
		Args:          append([]string(nil), request.Args...),
		Dir:           request.Dir,
		Env:           append([]string(nil), request.Env...),
		BindingDigest: request.Binding.Digest(),
	}, nil
}

// NewAmbientHostSnapshot constructs an explicit ambient identity. Only a
// compatibility default or a user-owned selection may choose full host access.
func NewAmbientHostSnapshot(cwd string, entrypoint Entrypoint, source SelectionSource) (*Snapshot, error) {
	if source != SelectionCompatibilityDefault && source != SelectionUserConfig && source != SelectionCLI {
		return nil, invalid("selection_source", "ambient authority requires user ownership")
	}
	return NewExecutionPolicySnapshot(&Spec{
		Version:         PolicyVersion,
		Profile:         ProfileDangerFullAccess,
		State:           StateDisabled,
		SelectionSource: source,
		Adapter:         AdapterAmbientHost,
		Platform:        runtime.GOOS,
		Architecture:    runtime.GOARCH,
		CWD:             cwd,
		Network:         NetworkPolicy{Mode: NetworkAmbient, ProjectionID: "ambient-host"},
		Environment:     EnvironmentPolicy{ProjectionID: "ambient-host"},
		Credentials:     CredentialPolicy{Mode: CredentialAmbientEnvironment, ProjectionID: "ambient-host"},
		Descendants:     DescendantPolicy{Mode: DescendantAmbient},
		Entrypoint:      entrypoint,
		Lineage:         Lineage{RootID: "root"},
	})
}
