// Package containment defines immutable host-execution policy identities.
//
// This package currently defines immutable containment identity and proof
// ownership only. Platform enforcement and process-launch wiring remain out of
// scope; the disabled compatibility adapter never claims a sandbox.
package containment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	PolicyVersion               = "p42.1"
	LegacyDisabledPolicyVersion = "p42.0"
)

const (
	maxIdentityLength = 128
	maxSetEntries     = 256
)

// Profile describes the requested host authority envelope.
type Profile string

const (
	ProfileReadOnly         Profile = "read-only"
	ProfileWorkspaceWrite   Profile = "workspace-write"
	ProfileDangerFullAccess Profile = "danger-full-access"
)

// State describes the enforcement status independently of the requested profile.
type State string

const (
	StateEnforced    State = "enforced"
	StateDegraded    State = "degraded"
	StateUnavailable State = "unavailable"
	StateDisabled    State = "disabled"
)

// Entrypoint identifies the composition root that resolved a policy.
type Entrypoint string

const (
	EntrypointTUI           Entrypoint = "tui"
	EntrypointPlain         Entrypoint = "plain"
	EntrypointHeadless      Entrypoint = "headless"
	EntrypointHeadlessGoal  Entrypoint = "headless-goal"
	EntrypointACP           Entrypoint = "acp"
	EntrypointChildAgent    Entrypoint = "child-agent"
	EntrypointEmbedded      Entrypoint = "embedded"
	EntrypointStandaloneMCP Entrypoint = "standalone-mcp"
)

// AdapterFamily identifies the platform adapter, not a permission decision.
type AdapterFamily string

const (
	AdapterAmbientHost    AdapterFamily = "ambient-host"
	AdapterDarwinSeatbelt AdapterFamily = "darwin-seatbelt"
)

// SelectionSource identifies the stable owner of a policy choice.
type SelectionSource string

const (
	SelectionCompatibilityDefault SelectionSource = "compatibility-default"
	SelectionDefault              SelectionSource = "default"
	SelectionUserConfig           SelectionSource = "user-config"
	SelectionCLI                  SelectionSource = "cli"
	SelectionChild                SelectionSource = "child"
)

// NetworkMode is an explicit, value-free network projection identity.
type NetworkMode string

const (
	NetworkDenied  NetworkMode = "denied"
	NetworkAmbient NetworkMode = "ambient"
)

// DescendantMode identifies the process-descendant cleanup policy.
type DescendantMode string

const (
	DescendantCleanupRequired DescendantMode = "cleanup-required"
	DescendantAmbient         DescendantMode = "ambient"
)

// NetworkPolicy contains no URLs, proxy settings, or request payloads.
type NetworkPolicy struct {
	Mode         NetworkMode
	ProjectionID string
}

// EnvironmentPolicy permits names and an opaque projection identity only.
// It intentionally has no field in which an environment value can be supplied.
type EnvironmentPolicy struct {
	Names        []string
	ProjectionID string
}

// CredentialPolicy contains opaque credential and socket projection identities,
// never credential material.
type CredentialPolicy struct {
	Mode         CredentialMode
	ProjectionID string
	SocketIDs    []string
}

// CredentialMode records a value-free credential compatibility boundary.
type CredentialMode string

// CredentialAmbientEnvironment is a policy label, not credential material.
const CredentialAmbientEnvironment CredentialMode = "ambient-environment" //nolint:gosec

// RootIdentity pins the workspace object used to resolve the policy. Its path
// and host-local device/inode values are identity data, never diagnostics.
type RootIdentity struct {
	Path   string
	Device uint64
	Inode  uint64
}

// ResourceLimits are declarative identities. Each runtime owner must prove the
// subset it actually enforces before a consumer may rely on an axis.
type ResourceLimits struct {
	WallTimeMillis  int64
	MemoryBytes     int64
	FileDescriptors int64
	ProcessCount    int64
	OutputBytes     int64
}

// DescendantPolicy describes declarative descendant handling.
type DescendantPolicy struct {
	Mode           DescendantMode
	MaxDescendants int64
}

// Lineage identifies policy inheritance without carrying session content.
type Lineage struct {
	RootID       string
	ChildID      string
	ParentDigest string
}

// Spec is the complete caller-owned input to NewExecutionPolicySnapshot.
// Slices are copied and canonicalized before a snapshot is returned.
type Spec struct {
	Version              string
	Profile              Profile
	State                State
	SelectionSource      SelectionSource
	Adapter              AdapterFamily
	Platform             string
	Architecture         string
	CapabilityGeneration string
	CWD                  string
	Root                 RootIdentity
	ReadRoots            []string
	WriteRoots           []string
	TempRoots            []string
	DeniedRoots          []string
	Network              NetworkPolicy
	Environment          EnvironmentPolicy
	Credentials          CredentialPolicy
	Resources            ResourceLimits
	Descendants          DescendantPolicy
	Entrypoint           Entrypoint
	Lineage              Lineage
}

// ValidationError reports an invalid policy input without exposing its value.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "invalid execution policy"
	}
	return fmt.Sprintf("invalid execution policy %s: %s", e.Field, e.Reason)
}

// DerivationError reports a rejected attempted child policy-axis change.
type DerivationError struct{ Axis string }

func (e *DerivationError) Error() string {
	if e == nil || e.Axis == "" {
		return "execution policy child derivation rejected"
	}
	return "execution policy child derivation rejected: " + e.Axis
}

// ExecutionPolicySnapshot is immutable after successful construction.
type ExecutionPolicySnapshot struct {
	spec   Spec
	digest string
}

// Snapshot is the short public name for an immutable execution-policy snapshot.
type Snapshot = ExecutionPolicySnapshot

type snapshotContextKey struct{}

// WithSnapshot binds an immutable snapshot to an execution context. A nil
// snapshot is retained as an explicit absent value; a nil context becomes a
// background context so this boundary cannot panic during compatibility wiring.
func WithSnapshot(ctx context.Context, snapshot *Snapshot) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, snapshotContextKey{}, snapshot)
}

// FromContext returns the bound snapshot, if any. It never synthesizes a
// policy, because only a composition root may resolve one.
func FromContext(ctx context.Context) (*Snapshot, bool) {
	if ctx == nil {
		return nil, false
	}
	snapshot, ok := ctx.Value(snapshotContextKey{}).(*ExecutionPolicySnapshot)
	return snapshot, ok && snapshot != nil
}

// NewExecutionPolicySnapshot validates, copies, canonicalizes, and identifies spec.
func NewExecutionPolicySnapshot(spec *Spec) (*ExecutionPolicySnapshot, error) {
	if spec == nil {
		return nil, &ValidationError{Field: "spec", Reason: "must not be nil"}
	}
	canonical, err := canonicalSpec(*spec)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("encode execution policy: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return &ExecutionPolicySnapshot{
		spec: canonical, digest: hex.EncodeToString(sum[:]),
	}, nil
}

// DisabledCompatibility returns the ambient-host compatibility policy.
// It is intentionally total: an empty CWD or invalid entrypoint is normalized to
// a truthful local default rather than panicking.
func DisabledCompatibility(cwd string, entrypoint Entrypoint) *ExecutionPolicySnapshot {
	if !validEntrypoint(entrypoint) {
		entrypoint = EntrypointEmbedded
	}
	spec := Spec{
		Version:         PolicyVersion,
		Profile:         ProfileDangerFullAccess,
		State:           StateDisabled,
		SelectionSource: SelectionCompatibilityDefault,
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
	}
	snapshot, err := NewExecutionPolicySnapshot(&spec)
	if err == nil {
		return snapshot
	}
	// A deleted process CWD can make a relative/empty path unresolvable. The
	// compatibility identity remains truthful by using the filesystem root as
	// a bounded fallback; all other fields above are static invariants.
	spec.CWD = string(filepath.Separator)
	snapshot, err = NewExecutionPolicySnapshot(&spec)
	if err != nil {
		panic("containment: static disabled compatibility policy is invalid")
	}
	return snapshot
}

// DisabledCompatibilitySnapshot is the explicit snapshot-named compatibility
// constructor used at composition roots.
func DisabledCompatibilitySnapshot(cwd string, entrypoint Entrypoint) *Snapshot {
	return DisabledCompatibility(cwd, entrypoint)
}

// Digest returns the canonical SHA-256 identity, or an empty string for nil.
func (s *ExecutionPolicySnapshot) Digest() string {
	if s == nil {
		return ""
	}
	return s.digest
}

// Spec returns a detached deep copy of the canonical input.
func (s *ExecutionPolicySnapshot) Spec() Spec {
	if s == nil {
		return Spec{}
	}
	return cloneSpec(s.spec)
}

// Diagnostic is intentionally path- and secret-free.
type Diagnostic struct {
	Version               string
	Profile               Profile
	State                 State
	Adapter               AdapterFamily
	Entrypoint            Entrypoint
	Digest                string
	ReadRootCount         int
	WriteRootCount        int
	TempRootCount         int
	DeniedRootCount       int
	EnvironmentNameCount  int
	CredentialSocketCount int
}

// Diagnostic returns a redacted status projection suitable for logs.
func (s *ExecutionPolicySnapshot) Diagnostic() Diagnostic {
	if s == nil {
		return Diagnostic{}
	}
	return Diagnostic{
		Version: s.spec.Version, Profile: s.spec.Profile, State: s.spec.State,
		Adapter: s.spec.Adapter, Entrypoint: s.spec.Entrypoint, Digest: s.digest,
		ReadRootCount: len(s.spec.ReadRoots), WriteRootCount: len(s.spec.WriteRoots),
		TempRootCount: len(s.spec.TempRoots), DeniedRootCount: len(s.spec.DeniedRoots),
		EnvironmentNameCount:  len(s.spec.Environment.Names),
		CredentialSocketCount: len(s.spec.Credentials.SocketIDs),
	}
}

// DeriveChild creates the only P42.0 child form supported by the disabled
// adapter. requested may be nil; a non-nil request must equal the derived
// policy exactly, so any policy-axis change fails closed.
func (s *ExecutionPolicySnapshot) DeriveChild(cwd string, requested *Spec) (*ExecutionPolicySnapshot, error) {
	return s.deriveChild(cwd, "child", requested)
}

func (s *ExecutionPolicySnapshot) deriveChild(cwd, childID string, requested *Spec) (*ExecutionPolicySnapshot, error) {
	if s == nil {
		return nil, &DerivationError{Axis: "parent"}
	}
	if s.spec.Adapter != AdapterAmbientHost || s.spec.State != StateDisabled ||
		s.spec.Profile != ProfileDangerFullAccess {
		return nil, &DerivationError{Axis: "adapter"}
	}
	child := s.Spec()
	child.CWD = cwd
	child.Entrypoint = EntrypointChildAgent
	child.Lineage.ParentDigest = s.digest
	child.Lineage.ChildID = childID
	if requested != nil {
		candidate, err := canonicalSpec(*requested)
		if err != nil {
			return nil, &DerivationError{Axis: "requested"}
		}
		expected, err := canonicalSpec(child)
		if err != nil {
			return nil, &DerivationError{Axis: "cwd"}
		}
		if axis := differingAxis(expected, candidate); axis != "" {
			return nil, &DerivationError{Axis: axis}
		}
	}
	return NewExecutionPolicySnapshot(&child)
}

// DeriveExactChild derives a child without admitting any replacement policy.
func (s *ExecutionPolicySnapshot) DeriveExactChild(cwd string) (*Snapshot, error) {
	return s.deriveChild(cwd, "child", nil)
}

// DeriveExactChildFor derives an exact-authority child and records only an
// opaque hash of the caller's child identity in the immutable lineage.
func (s *ExecutionPolicySnapshot) DeriveExactChildFor(cwd, childIdentity string) (*Snapshot, error) {
	childID := "child"
	if childIdentity != "" {
		sum := sha256.Sum256([]byte(childIdentity))
		childID = "child:" + hex.EncodeToString(sum[:16])
	}
	return s.deriveChild(cwd, childID, nil)
}

func canonicalSpec(spec Spec) (Spec, error) {
	if spec.Version != PolicyVersion && spec.Version != LegacyDisabledPolicyVersion {
		return Spec{}, invalid("version", "unsupported")
	}
	if !validProfile(spec.Profile) || !validState(spec.State) {
		return Spec{}, invalid("profile/state", "unsupported")
	}
	if spec.State == StateDisabled && spec.Profile != ProfileDangerFullAccess {
		return Spec{}, invalid("profile/state", "disabled requires danger-full-access")
	}
	if !validEntrypoint(spec.Entrypoint) {
		return Spec{}, invalid("entrypoint", "unsupported")
	}
	if spec.Adapter != AdapterAmbientHost && spec.Adapter != AdapterDarwinSeatbelt {
		return Spec{}, invalid("adapter", "unsupported")
	}
	if spec.Adapter == AdapterAmbientHost && (spec.Profile != ProfileDangerFullAccess || spec.State != StateDisabled) {
		return Spec{}, invalid("adapter", "ambient-host requires disabled danger-full-access")
	}
	if spec.Adapter == AdapterDarwinSeatbelt {
		if spec.Profile != ProfileWorkspaceWrite || (spec.State != StateDegraded && spec.State != StateUnavailable) {
			return Spec{}, invalid("adapter", "darwin-seatbelt requires degraded or unavailable workspace-write")
		}
		if spec.Network.Mode != NetworkDenied || spec.Credentials.Mode != CredentialAmbientEnvironment || !validIdentity(spec.CapabilityGeneration) {
			return Spec{}, invalid("adapter", "darwin-seatbelt requires denied network, ambient credentials, and generation")
		}
	}
	if !validSelectionSource(spec.SelectionSource) {
		return Spec{}, invalid("selection_source", "unsupported")
	}
	if spec.Version == LegacyDisabledPolicyVersion &&
		(spec.Profile != ProfileDangerFullAccess || spec.State != StateDisabled || spec.Adapter != AdapterAmbientHost ||
			spec.SelectionSource != SelectionCompatibilityDefault || spec.CapabilityGeneration != "" || spec.Credentials.Mode != "" || spec.Root != (RootIdentity{})) {
		return Spec{}, invalid("legacy", "only disabled ambient compatibility is accepted")
	}
	if spec.Version == PolicyVersion && spec.Credentials.Mode != CredentialAmbientEnvironment {
		return Spec{}, invalid("credentials.mode", "ambient-environment is required")
	}
	if !validIdentity(spec.Platform) || !validIdentity(spec.Architecture) ||
		!validOptionalIdentity(spec.CapabilityGeneration) ||
		!validOptionalIdentity(spec.Network.ProjectionID) ||
		!validOptionalIdentity(spec.Environment.ProjectionID) ||
		!validOptionalIdentity(spec.Credentials.ProjectionID) ||
		!validOptionalIdentity(spec.Lineage.RootID) || !validOptionalIdentity(spec.Lineage.ChildID) ||
		!validOptionalDigest(spec.Lineage.ParentDigest) {
		return Spec{}, invalid("identity", "invalid or too long")
	}
	if spec.Network.Mode != NetworkAmbient && spec.Network.Mode != NetworkDenied {
		return Spec{}, invalid("network.mode", "unsupported")
	}
	if spec.Descendants.Mode != DescendantAmbient && spec.Descendants.Mode != DescendantCleanupRequired {
		return Spec{}, invalid("descendants.mode", "unsupported")
	}
	if err := validateLimits(spec.Resources, spec.Descendants); err != nil {
		return Spec{}, err
	}
	var err error
	if spec.CWD, err = canonicalPath(spec.CWD); err != nil {
		return Spec{}, invalid("cwd", "cannot canonicalize")
	}
	if spec.Root.Path != "" {
		if spec.Root.Path, err = canonicalPath(spec.Root.Path); err != nil {
			return Spec{}, invalid("root", "cannot canonicalize")
		}
	}
	if spec.Adapter == AdapterDarwinSeatbelt {
		if spec.Root.Path != spec.CWD {
			return Spec{}, invalid("root", "darwin-seatbelt requires workspace root identity")
		}
		if spec.State == StateDegraded && (spec.Root.Device == 0 || spec.Root.Inode == 0) {
			return Spec{}, invalid("root", "available darwin-seatbelt requires immutable workspace identity")
		}
		if spec.State == StateUnavailable && (spec.Root.Device == 0) != (spec.Root.Inode == 0) {
			return Spec{}, invalid("root", "partial workspace identity is not accepted")
		}
	}
	if spec.Adapter == AdapterDarwinSeatbelt && spec.State == StateDegraded && (spec.Platform != "darwin" || (spec.Architecture != "amd64" && spec.Architecture != "arm64")) {
		return Spec{}, invalid("adapter", "darwin-seatbelt requires darwin amd64 or arm64")
	}
	if spec.ReadRoots, err = canonicalPaths(spec.ReadRoots); err != nil {
		return Spec{}, err
	}
	if spec.WriteRoots, err = canonicalPaths(spec.WriteRoots); err != nil {
		return Spec{}, err
	}
	if spec.TempRoots, err = canonicalPaths(spec.TempRoots); err != nil {
		return Spec{}, err
	}
	if spec.DeniedRoots, err = canonicalPaths(spec.DeniedRoots); err != nil {
		return Spec{}, err
	}
	if spec.Environment.Names, err = canonicalNames(spec.Environment.Names, "environment.names"); err != nil {
		return Spec{}, err
	}
	if spec.Credentials.SocketIDs, err = canonicalIDs(spec.Credentials.SocketIDs, "credentials.socket_ids"); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func cloneSpec(spec Spec) Spec {
	spec.ReadRoots = append([]string(nil), spec.ReadRoots...)
	spec.WriteRoots = append([]string(nil), spec.WriteRoots...)
	spec.TempRoots = append([]string(nil), spec.TempRoots...)
	spec.DeniedRoots = append([]string(nil), spec.DeniedRoots...)
	spec.Environment.Names = append([]string(nil), spec.Environment.Names...)
	spec.Credentials.SocketIDs = append([]string(nil), spec.Credentials.SocketIDs...)
	return spec
}

func canonicalPath(path string) (string, error) {
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("path contains NUL")
	}
	if path == "" {
		path = "."
	}
	return filepath.Abs(filepath.Clean(path))
}

func canonicalPaths(paths []string) ([]string, error) {
	if len(paths) > maxSetEntries {
		return nil, invalid("roots", "too many entries")
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, invalid("roots", "empty path")
		}
		canonical, err := canonicalPath(path)
		if err != nil {
			return nil, invalid("roots", "cannot canonicalize")
		}
		result = append(result, canonical)
	}
	return canonicalStrings(result), nil
}

func canonicalNames(names []string, field string) ([]string, error) {
	if len(names) > maxSetEntries {
		return nil, invalid(field, "too many entries")
	}
	for _, name := range names {
		if !validEnvironmentName(name) {
			return nil, invalid(field, "values or invalid names are not accepted")
		}
	}
	return canonicalStrings(append([]string(nil), names...)), nil
}

func canonicalIDs(ids []string, field string) ([]string, error) {
	if len(ids) > maxSetEntries {
		return nil, invalid(field, "too many entries")
	}
	for _, id := range ids {
		if !validIdentity(id) {
			return nil, invalid(field, "invalid identity")
		}
	}
	return canonicalStrings(append([]string(nil), ids...)), nil
}

func canonicalStrings(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func validateLimits(resources ResourceLimits, descendants DescendantPolicy) error {
	if resources.WallTimeMillis < 0 || resources.MemoryBytes < 0 ||
		resources.FileDescriptors < 0 || resources.ProcessCount < 0 ||
		resources.OutputBytes < 0 || descendants.MaxDescendants < 0 {
		return invalid("resources", "must not be negative")
	}
	return nil
}

func differingAxis(expected, candidate Spec) string {
	if expected.CWD != candidate.CWD {
		return "cwd"
	}
	if expected.Entrypoint != candidate.Entrypoint {
		return "entrypoint"
	}
	if expected.Lineage != candidate.Lineage {
		return "lineage"
	}
	left, right := expected, candidate
	left.CWD, right.CWD = "", ""
	left.Entrypoint, right.Entrypoint = "", ""
	left.Lineage, right.Lineage = Lineage{}, Lineage{}
	leftEncoded, _ := json.Marshal(left)
	rightEncoded, _ := json.Marshal(right)
	if !bytes.Equal(leftEncoded, rightEncoded) {
		return "policy-axis"
	}
	return ""
}

func validProfile(value Profile) bool {
	return value == ProfileReadOnly || value == ProfileWorkspaceWrite || value == ProfileDangerFullAccess
}

func validState(value State) bool {
	return value == StateEnforced || value == StateDegraded || value == StateUnavailable || value == StateDisabled
}

func validEntrypoint(value Entrypoint) bool {
	switch value {
	case EntrypointTUI, EntrypointPlain, EntrypointHeadless, EntrypointHeadlessGoal, EntrypointACP, EntrypointChildAgent, EntrypointEmbedded, EntrypointStandaloneMCP:
		return true
	default:
		return false
	}
}

func validSelectionSource(value SelectionSource) bool {
	switch value {
	case SelectionCompatibilityDefault, SelectionDefault, SelectionUserConfig, SelectionCLI, SelectionChild:
		return true
	default:
		return false
	}
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > maxIdentityLength || strings.Contains(value, "=") {
		return false
	}
	for index, char := range value {
		valid := char == '_' || char >= 'A' && char <= 'Z' ||
			index > 0 && char >= '0' && char <= '9'
		if !valid {
			return false
		}
	}
	return true
}
func validIdentity(value string) bool { return validOptionalIdentity(value) && value != "" }
func validOptionalIdentity(value string) bool {
	if len(value) > maxIdentityLength {
		return false
	}
	for _, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '.' || char == '_' ||
			char == '-' || char == ':'
		if !valid {
			return false
		}
	}
	return true
}

func validOptionalDigest(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }
