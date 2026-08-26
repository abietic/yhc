package containment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
)

// EnforcementAxes identifies independently-owned enforcement claims.
type EnforcementAxes uint64

const (
	AxisFilesystemRead EnforcementAxes = 1 << iota
	AxisFilesystemWrite
	AxisNetworkDenied
	AxisRootIdentity
	AxisDescendantConfinement
	AxisDescendantCleanup
	AxisWallTime
	AxisOutput
	AxisMemory
	AxisFileDescriptors
	AxisProcessCount
)

const (
	adapterAllowedAxes = AxisFilesystemRead | AxisFilesystemWrite | AxisNetworkDenied | AxisRootIdentity | AxisDescendantConfinement
	runtimeAllowedAxes = AxisRootIdentity | AxisDescendantCleanup | AxisWallTime | AxisOutput
)

type AdapterProof struct {
	PolicyDigest         string
	CapabilityGeneration string
	Enforced             EnforcementAxes
}

type ExecutionProof struct {
	BindingDigest        string
	PolicyDigest         string
	CapabilityGeneration string
	AdapterAxes          EnforcementAxes
	RuntimeAxes          EnforcementAxes
	Enforced             EnforcementAxes
}

// ExecutionIdentity is a detached projection of the Guest facts a permission
// consumer may compare. Root remains host-local identity data and is excluded
// from Diagnostic.
type ExecutionIdentity struct {
	ProcessClass         ProcessClass
	PolicyDigest         string
	BindingDigest        string
	Profile              Profile
	State                State
	Adapter              AdapterFamily
	Network              NetworkMode
	CredentialMode       CredentialMode
	CapabilityGeneration string
	Availability         BindingAvailability
	ReasonCode           ReasonCode
	AdapterAxes          EnforcementAxes
	RuntimeAxes          EnforcementAxes
	Enforced             EnforcementAxes
	Root                 RootIdentity
}

// ExecutionIdentityDiagnostic is safe for diagnostics; it contains no root
// path or filesystem object identity.
type ExecutionIdentityDiagnostic struct {
	ProcessClass         ProcessClass
	PolicyDigest         string
	BindingDigest        string
	Profile              Profile
	State                State
	Adapter              AdapterFamily
	Network              NetworkMode
	CredentialMode       CredentialMode
	CapabilityGeneration string
	Availability         BindingAvailability
	ReasonCode           ReasonCode
	AdapterAxes          EnforcementAxes
	RuntimeAxes          EnforcementAxes
	Enforced             EnforcementAxes
}

// Diagnostic returns the redacted execution identity projection.
func (i ExecutionIdentity) Diagnostic() ExecutionIdentityDiagnostic {
	return ExecutionIdentityDiagnostic{
		ProcessClass: i.ProcessClass, PolicyDigest: i.PolicyDigest, BindingDigest: i.BindingDigest,
		Profile: i.Profile, State: i.State, Adapter: i.Adapter, Network: i.Network,
		CredentialMode: i.CredentialMode, CapabilityGeneration: i.CapabilityGeneration,
		Availability: i.Availability, ReasonCode: i.ReasonCode, AdapterAxes: i.AdapterAxes,
		RuntimeAxes: i.RuntimeAxes, Enforced: i.Enforced,
	}
}

type ProcessClass string

const (
	ProcessClassGuest      ProcessClass = "guest"
	ProcessClassShellHooks ProcessClass = "shell-hooks"
	ProcessClassStdioMCP   ProcessClass = "stdio-mcp"
)

type (
	BindingAvailability string
	ReasonCode          string
)

const (
	BindingAvailable   BindingAvailability = "available"
	BindingUnavailable BindingAvailability = "unavailable"

	ReasonUnsupportedPlatform     ReasonCode = "unsupported_platform"
	ReasonExecutableMissing       ReasonCode = "executable_missing"
	ReasonProfileInvalid          ReasonCode = "profile_invalid"
	ReasonProbeFailed             ReasonCode = "probe_failed"
	ReasonRequiredAxisUnavailable ReasonCode = "required_axis_unavailable"
	ReasonRootChanged             ReasonCode = "root_changed"
	ReasonBindingExpired          ReasonCode = "binding_expired"
	ReasonOperationDenied         ReasonCode = "operation_denied"
)

type SpawnAdapter interface {
	Family() AdapterFamily
	CapabilityGeneration() string
	Probe(context.Context, *Snapshot) ProbeResult
	Prepare(context.Context, SpawnRequest) (SpawnSpec, error)
}

type ProbeResult struct {
	Proof      AdapterProof
	Diagnostic Diagnostic
	ReasonCode ReasonCode
}

type SpawnRequest struct {
	Binding    *Binding
	Executable string
	Args       []string
	Dir        string
	Env        []string
}

type SpawnSpec struct {
	Path          string
	Args          []string
	Dir           string
	Env           []string
	BindingDigest string
	ExtraFiles    []*os.File
}

// Binding is an immutable policy-to-adapter association for one process class.
type Binding struct {
	processClass         ProcessClass
	policy               *Snapshot
	adapter              SpawnAdapter
	adapterFamily        AdapterFamily
	capabilityGeneration string
	adapterProof         AdapterProof
	availability         BindingAvailability
	reasonCode           ReasonCode
	digest               string
}

type BindingDiagnostic struct {
	ProcessClass   ProcessClass
	PolicyDigest   string
	Adapter        AdapterFamily
	CredentialMode CredentialMode
	Availability   BindingAvailability
	ReasonCode     ReasonCode
	Digest         string
}

func NewBinding(class ProcessClass, policy *Snapshot, adapter SpawnAdapter, proof AdapterProof) (*Binding, error) {
	if err := validateClassPolicyAdapter(class, policy, adapter); err != nil {
		return nil, err
	}
	if policy.spec.Adapter == AdapterAmbientHost {
		if proof != (AdapterProof{}) {
			return nil, invalid("proof", "ambient binding has no proof")
		}
		return newBinding(class, policy, adapter, proof, BindingAvailable, ""), nil
	}
	if proof.PolicyDigest != policy.Digest() || proof.CapabilityGeneration != adapter.CapabilityGeneration() || proof.CapabilityGeneration != policy.spec.CapabilityGeneration {
		return nil, invalid("proof", "policy or capability generation mismatch")
	}
	if proof.Enforced&^adapterAllowedAxes != 0 {
		return nil, invalid("proof", "adapter claimed an unowned axis")
	}
	if isContainedAdapter(policy.spec.Adapter) && (class != ProcessClassGuest || policy.spec.State != StateDegraded || proof.Enforced != adapterAllowedAxes) {
		return nil, invalid("proof", "contained binding requires all adapter axes")
	}
	return newBinding(class, policy, adapter, proof, BindingAvailable, ""), nil
}

func NewUnavailableBinding(class ProcessClass, policy *Snapshot, adapter SpawnAdapter, reason ReasonCode) (*Binding, error) {
	if err := validateClassPolicyAdapter(class, policy, adapter); err != nil {
		return nil, err
	}
	if class != ProcessClassGuest || policy.spec.State != StateUnavailable || !isContainedAdapter(policy.spec.Adapter) {
		return nil, invalid("binding", "unavailable requires unavailable contained policy")
	}
	if !validUnavailableReasonCode(reason) {
		return nil, invalid("reason", "must be bounded non-empty code")
	}
	return newBinding(class, policy, adapter, AdapterProof{}, BindingUnavailable, reason), nil
}

func newBinding(class ProcessClass, policy *Snapshot, adapter SpawnAdapter, proof AdapterProof, availability BindingAvailability, reason ReasonCode) *Binding {
	binding := &Binding{processClass: class, policy: policy, adapter: adapter, adapterFamily: adapter.Family(), capabilityGeneration: adapter.CapabilityGeneration(), adapterProof: proof, availability: availability, reasonCode: reason}
	encoded, _ := json.Marshal(struct {
		Class        ProcessClass
		PolicyDigest string
		Adapter      AdapterFamily
		Generation   string
		Axes         EnforcementAxes
		Availability BindingAvailability
		Reason       ReasonCode
	}{class, policy.Digest(), binding.adapterFamily, binding.capabilityGeneration, proof.Enforced, availability, reason})
	sum := sha256.Sum256(encoded)
	binding.digest = hex.EncodeToString(sum[:])
	return binding
}

func validateClassPolicyAdapter(class ProcessClass, policy *Snapshot, adapter SpawnAdapter) error {
	if !validProcessClass(class) || policy == nil || adapter == nil {
		return invalid("binding", "class, policy, and adapter are required")
	}
	if adapter.Family() != policy.spec.Adapter || adapter.CapabilityGeneration() != policy.spec.CapabilityGeneration {
		return invalid("adapter", "family or generation mismatch")
	}
	if (class == ProcessClassShellHooks || class == ProcessClassStdioMCP) && policy.spec.Adapter != AdapterAmbientHost {
		return invalid("binding", "hooks and stdio MCP require ambient-host")
	}
	return nil
}

func (b *Binding) Digest() string {
	if b == nil {
		return ""
	}
	return b.digest
}

func (b *Binding) PolicyDigest() string {
	if b == nil || b.policy == nil {
		return ""
	}
	return b.policy.Digest()
}

func (b *Binding) Policy() *Snapshot {
	if b == nil {
		return nil
	}
	return b.policy
}

func (b *Binding) AdapterFamily() AdapterFamily {
	if b == nil {
		return ""
	}
	return b.adapterFamily
}

func (b *Binding) CapabilityGeneration() string {
	if b == nil {
		return ""
	}
	return b.capabilityGeneration
}

func (b *Binding) ReasonCode() ReasonCode {
	if b == nil {
		return ""
	}
	return b.reasonCode
}

func (b *Binding) AdapterAxes() EnforcementAxes {
	if b == nil {
		return 0
	}
	return b.adapterProof.Enforced
}

func (b *Binding) Availability() BindingAvailability {
	if b == nil {
		return ""
	}
	return b.availability
}

func (b *Binding) ProcessClass() ProcessClass {
	if b == nil {
		return ""
	}
	return b.processClass
}

func (b *Binding) Diagnostic() BindingDiagnostic {
	if b == nil {
		return BindingDiagnostic{}
	}
	return BindingDiagnostic{b.processClass, b.PolicyDigest(), b.adapterFamily, b.policy.spec.Credentials.Mode, b.availability, b.reasonCode, b.digest}
}

func (b *Binding) Prepare(ctx context.Context, request SpawnRequest) (SpawnSpec, error) {
	if b == nil || b.availability != BindingAvailable {
		return SpawnSpec{}, invalid("binding", "unavailable")
	}
	if request.Binding != b {
		return SpawnSpec{}, invalid("binding", "request binding mismatch")
	}
	if b.adapter.Family() != b.adapterFamily || b.adapter.CapabilityGeneration() != b.capabilityGeneration {
		return SpawnSpec{}, invalid("binding", "adapter capability drift")
	}
	spec, err := b.adapter.Prepare(ctx, request)
	if err != nil {
		return SpawnSpec{}, err
	}
	if spec.BindingDigest != b.digest {
		closeSpawnExtraFiles(spec.ExtraFiles)
		return SpawnSpec{}, invalid("binding", "adapter returned mismatched digest")
	}
	spec.Args, spec.Env = append([]string(nil), spec.Args...), append([]string(nil), spec.Env...)
	spec.ExtraFiles = append([]*os.File(nil), spec.ExtraFiles...)
	return spec, nil
}

func NewExecutionProof(binding *Binding, runtime EnforcementAxes) (ExecutionProof, error) {
	if binding == nil || binding.availability != BindingAvailable {
		return ExecutionProof{}, invalid("execution_proof", "available binding required")
	}
	if binding.policy.spec.Version == LegacyDisabledPolicyVersion || binding.adapterFamily == AdapterAmbientHost {
		return ExecutionProof{}, invalid("execution_proof", "ambient or legacy policy cannot satisfy proof")
	}
	if runtime&^runtimeAllowedAxes != 0 {
		return ExecutionProof{}, invalid("execution_proof", "runtime claimed an unowned axis")
	}
	combined := binding.adapterProof.Enforced | runtime
	if binding.adapterProof.Enforced&AxisRootIdentity == 0 || runtime&AxisRootIdentity == 0 {
		combined &^= AxisRootIdentity
	}
	return ExecutionProof{binding.Digest(), binding.PolicyDigest(), binding.capabilityGeneration, binding.adapterProof.Enforced, runtime, combined}, nil
}

// ExecutionIdentityFor detaches Guest containment facts from a Binding. It
// accepts only complete, binding-owned platform containment proofs; ambient and
// unavailable Guest bindings deliberately project zero proof axes so consumers
// reject them.
func ExecutionIdentityFor(binding *Binding, proof ExecutionProof) (ExecutionIdentity, error) {
	if binding == nil || binding.processClass != ProcessClassGuest || binding.policy == nil {
		return ExecutionIdentity{}, invalid("execution_identity", "guest binding required")
	}
	spec := binding.policy.Spec()
	identity := ExecutionIdentity{
		ProcessClass: binding.processClass, PolicyDigest: binding.PolicyDigest(), BindingDigest: binding.Digest(),
		Profile: spec.Profile, State: spec.State, Adapter: binding.adapterFamily, Network: spec.Network.Mode,
		CredentialMode: spec.Credentials.Mode, CapabilityGeneration: binding.capabilityGeneration,
		Availability: binding.availability, ReasonCode: binding.reasonCode, Root: spec.Root,
	}
	if binding.adapterFamily == AdapterAmbientHost {
		if proof != (ExecutionProof{}) {
			return ExecutionIdentity{}, invalid("execution_identity", "ambient binding has no execution proof")
		}
		return identity, nil
	}
	if !isContainedAdapter(binding.adapterFamily) {
		return ExecutionIdentity{}, invalid("execution_identity", "unsupported guest adapter")
	}
	if binding.availability == BindingUnavailable {
		if proof != (ExecutionProof{}) {
			return ExecutionIdentity{}, invalid("execution_identity", "unavailable binding has no execution proof")
		}
		return identity, nil
	}
	if binding.availability != BindingAvailable || identity.PolicyDigest == "" || identity.BindingDigest == "" || identity.CapabilityGeneration == "" ||
		proof.BindingDigest != identity.BindingDigest || proof.PolicyDigest != identity.PolicyDigest || proof.CapabilityGeneration != identity.CapabilityGeneration ||
		proof.AdapterAxes != binding.adapterProof.Enforced || proof.AdapterAxes != adapterAllowedAxes || proof.AdapterAxes&^adapterAllowedAxes != 0 ||
		proof.RuntimeAxes&^runtimeAllowedAxes != 0 || proof.Enforced != proof.AdapterAxes|proof.RuntimeAxes {
		return ExecutionIdentity{}, invalid("execution_identity", "binding or proof mismatch")
	}
	identity.AdapterAxes, identity.RuntimeAxes, identity.Enforced = proof.AdapterAxes, proof.RuntimeAxes, proof.Enforced
	return identity, nil
}

// IsContainedAdapter reports the platform adapters that may produce the fixed
// workspace-write proof axes. Permission consumers still decide which exact
// adapter families they admit automatically.
func IsContainedAdapter(adapter AdapterFamily) bool {
	return isContainedAdapter(adapter)
}

func isContainedAdapter(adapter AdapterFamily) bool {
	return adapter == AdapterDarwinSeatbelt || adapter == AdapterLinuxBubblewrap
}

type Bindings struct{ guest, shellHooks, stdioMCP *Binding }

func NewBindings(guest, shellHooks, stdioMCP *Binding) (*Bindings, error) {
	if guest == nil || shellHooks == nil || stdioMCP == nil || guest.processClass != ProcessClassGuest || shellHooks.processClass != ProcessClassShellHooks || stdioMCP.processClass != ProcessClassStdioMCP {
		return nil, invalid("bindings", "all process classes are required")
	}
	return &Bindings{guest, shellHooks, stdioMCP}, nil
}

func (b *Bindings) Guest() *Binding {
	if b == nil {
		return nil
	}
	return b.guest
}

func (b *Bindings) ShellHooks() *Binding {
	if b == nil {
		return nil
	}
	return b.shellHooks
}

func (b *Bindings) StdioMCP() *Binding {
	if b == nil {
		return nil
	}
	return b.stdioMCP
}

func validProcessClass(value ProcessClass) bool {
	return value == ProcessClassGuest || value == ProcessClassShellHooks || value == ProcessClassStdioMCP
}

func validUnavailableReasonCode(value ReasonCode) bool {
	return value == ReasonUnsupportedPlatform || value == ReasonExecutableMissing || value == ReasonProfileInvalid || value == ReasonProbeFailed || value == ReasonRequiredAxisUnavailable || value == ReasonRootChanged
}
