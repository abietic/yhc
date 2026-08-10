package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
)

// SandboxSelection is a trusted, immutable composition input. A nil selection
// preserves the ambient compatibility path for embedded callers; production
// CLI and ACP roots always provide an explicit selection.
type SandboxSelection struct {
	Profile        containment.Profile
	Source         containment.SelectionSource
	ExtraReadRoots []string
}

const (
	guestWallTimeMillis  = int64(10 * 60 * 1000)
	guestRetainedOutput  = int64(150000)
	guestNetworkIdentity = "network-denied"
	ambientEnvIdentity   = "ambient-environment"
)

// NewSandboxSelection validates selection authority and detaches caller-owned
// roots. Only explicit user configuration or CLI input may widen Guest to
// ambient danger-full-access.
func NewSandboxSelection(
	profile containment.Profile,
	source containment.SelectionSource,
	extraReadRoots []string,
) (*SandboxSelection, error) {
	if profile != containment.ProfileWorkspaceWrite && profile != containment.ProfileDangerFullAccess {
		return nil, fmt.Errorf("sandbox selection profile is unsupported")
	}
	switch source {
	case containment.SelectionDefault, containment.SelectionUserConfig, containment.SelectionCLI:
	default:
		return nil, fmt.Errorf("sandbox selection source is unsupported")
	}
	if profile == containment.ProfileDangerFullAccess &&
		source != containment.SelectionUserConfig && source != containment.SelectionCLI {
		return nil, fmt.Errorf("danger-full-access requires user-owned selection")
	}
	canonicalRoots, err := canonicalSelectionRoots(extraReadRoots)
	if err != nil {
		return nil, err
	}
	return &SandboxSelection{
		Profile:        profile,
		Source:         source,
		ExtraReadRoots: canonicalRoots,
	}, nil
}

// Clone returns a detached selection suitable for child/config construction.
func (s *SandboxSelection) Clone() *SandboxSelection {
	if s == nil {
		return nil
	}
	return &SandboxSelection{
		Profile:        s.Profile,
		Source:         s.Source,
		ExtraReadRoots: append([]string(nil), s.ExtraReadRoots...),
	}
}

func canonicalSelectionRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("sandbox extra read roots are invalid")
	}
	home = filepath.Clean(home)
	broad := map[string]struct{}{
		"/": {}, "/System": {}, "/usr": {}, "/bin": {}, "/sbin": {},
		"/Library": {}, "/Applications": {}, "/Users": {}, "/Volumes": {},
		"/private": {}, "/var": {}, "/etc": {}, "/opt": {},
	}
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, fmt.Errorf("sandbox extra read roots are invalid")
		}
		cleaned := filepath.Clean(root)
		if _, denied := broad[cleaned]; denied || cleaned == home || pathContains(cleaned, home) {
			return nil, fmt.Errorf("sandbox extra read roots are invalid")
		}
		info, statErr := os.Lstat(cleaned)
		resolved, resolveErr := filepath.EvalSymlinks(cleaned)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || resolveErr != nil || resolved != cleaned {
			return nil, fmt.Errorf("sandbox extra read roots are invalid")
		}
		result = append(result, cleaned)
	}
	sort.Strings(result)
	compacted := result[:0]
	for _, root := range result {
		if len(compacted) == 0 || compacted[len(compacted)-1] != root {
			compacted = append(compacted, root)
		}
	}
	return append([]string(nil), compacted...), nil
}

func pathContains(ancestor, path string) bool {
	if ancestor == path {
		return true
	}
	return strings.HasPrefix(path, ancestor+string(filepath.Separator))
}

// ResolveDisabledExecutionPolicy resolves the P42.0 compatibility identity at
// a composition root. It describes ambient host authority and never claims
// that execution is sandboxed.
func ResolveDisabledExecutionPolicy(cwd string, entrypoint commands.Entrypoint) *containment.Snapshot {
	return containment.DisabledCompatibilitySnapshot(cwd, containmentEntrypoint(entrypoint))
}

// ResolveDisabledExecutionBindings constructs three explicit ambient process
// classes for compatibility callers. Distinct binding digests prevent one
// class from being substituted for another, but none carries a proof.
func ResolveDisabledExecutionBindings(cwd string, entrypoint commands.Entrypoint) (*containment.Bindings, error) {
	policy := ResolveDisabledExecutionPolicy(cwd, entrypoint)
	return ambientBindingsForPolicy(policy)
}

func ambientBindingsForPolicy(policy *containment.Snapshot) (*containment.Bindings, error) {
	if policy == nil {
		return nil, fmt.Errorf("ambient policy is unavailable")
	}
	spec := policy.Spec()
	if spec.Adapter != containment.AdapterAmbientHost || spec.Profile != containment.ProfileDangerFullAccess || spec.State != containment.StateDisabled ||
		(spec.SelectionSource != containment.SelectionCompatibilityDefault && spec.SelectionSource != containment.SelectionUserConfig && spec.SelectionSource != containment.SelectionCLI) {
		return nil, fmt.Errorf("ambient policy authority is invalid")
	}
	adapter := containment.NewAmbientHostAdapter()
	guest, err := containment.NewBinding(containment.ProcessClassGuest, policy, adapter, containment.AdapterProof{})
	if err != nil {
		return nil, fmt.Errorf("resolve disabled Guest binding: %w", err)
	}
	hooksBinding, err := containment.NewBinding(containment.ProcessClassShellHooks, policy, adapter, containment.AdapterProof{})
	if err != nil {
		return nil, fmt.Errorf("resolve disabled hook binding: %w", err)
	}
	mcpBinding, err := containment.NewBinding(containment.ProcessClassStdioMCP, policy, adapter, containment.AdapterProof{})
	if err != nil {
		return nil, fmt.Errorf("resolve disabled MCP binding: %w", err)
	}
	return containment.NewBindings(guest, hooksBinding, mcpBinding)
}

// ResolveExecutionBindings resolves one immutable process-class matrix. A
// workspace-write selection never falls back to ambient Guest authority: an
// unsupported host or failed fixed-adapter probe yields an unavailable Guest
// binding while hooks and configured stdio MCP remain explicitly ambient.
func ResolveExecutionBindings(
	ctx context.Context,
	cwd string,
	entrypoint commands.Entrypoint,
	selection *SandboxSelection,
) (*containment.Bindings, error) {
	if selection == nil {
		return ResolveDisabledExecutionBindings(cwd, entrypoint)
	}
	selection = selection.Clone()
	selection, err := NewSandboxSelection(selection.Profile, selection.Source, selection.ExtraReadRoots)
	if err != nil {
		return nil, err
	}
	canonicalCWD, err := filepath.Abs(filepath.Clean(firstPath(cwd)))
	if err != nil {
		return nil, fmt.Errorf("sandbox working directory is invalid")
	}
	if resolved, resolveErr := canonicalExistingDirectory(canonicalCWD); resolveErr == nil {
		canonicalCWD = resolved
	}
	containmentEntrypoint := containmentEntrypoint(entrypoint)
	extensionPolicy, err := containment.NewAmbientHostSnapshot(
		canonicalCWD,
		containmentEntrypoint,
		containment.SelectionCompatibilityDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve extension policy: %w", err)
	}
	extensionAdapter := containment.NewAmbientHostAdapter()
	hooksBinding, err := containment.NewBinding(containment.ProcessClassShellHooks, extensionPolicy, extensionAdapter, containment.AdapterProof{})
	if err != nil {
		return nil, fmt.Errorf("resolve hook binding: %w", err)
	}
	mcpBinding, err := containment.NewBinding(containment.ProcessClassStdioMCP, extensionPolicy, extensionAdapter, containment.AdapterProof{})
	if err != nil {
		return nil, fmt.Errorf("resolve MCP binding: %w", err)
	}

	var guest *containment.Binding
	switch selection.Profile {
	case containment.ProfileDangerFullAccess:
		policy, policyErr := containment.NewAmbientHostSnapshot(canonicalCWD, containmentEntrypoint, selection.Source)
		if policyErr != nil {
			return nil, fmt.Errorf("resolve ambient Guest policy: %w", policyErr)
		}
		guest, err = containment.NewBinding(containment.ProcessClassGuest, policy, containment.NewAmbientHostAdapter(), containment.AdapterProof{})
	case containment.ProfileWorkspaceWrite:
		guest, err = resolveWorkspaceGuestBinding(ctx, canonicalCWD, containmentEntrypoint, selection)
	default:
		err = fmt.Errorf("sandbox profile is unsupported")
	}
	if err != nil {
		return nil, err
	}
	return containment.NewBindings(guest, hooksBinding, mcpBinding)
}

func firstPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "."
	}
	return path
}

func resolveWorkspaceGuestBinding(
	ctx context.Context,
	cwd string,
	entrypoint containment.Entrypoint,
	selection *SandboxSelection,
) (*containment.Binding, error) {
	adapter := containment.NewDarwinSeatbeltAdapter()
	reason := containment.ReasonCode("")
	root := containment.RootIdentity{Path: cwd}
	if runtime.GOOS != "darwin" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		reason = containment.ReasonUnsupportedPlatform
	} else {
		captured, err := containment.CaptureRootIdentity(cwd)
		if err != nil {
			reason = containment.ReasonRootChanged
		} else {
			root = captured
			cwd = captured.Path
		}
	}
	readRoots, tempRoots, deniedRoots, rootsErr := workspaceGuestRoots(cwd, selection.ExtraReadRoots)
	if rootsErr != nil && reason == "" {
		reason = containment.ReasonProfileInvalid
	}
	state := containment.StateDegraded
	if reason != "" {
		state = containment.StateUnavailable
		if root.Device == 0 || root.Inode == 0 {
			root = containment.RootIdentity{Path: cwd}
		}
	}
	spec := workspaceGuestSpec(cwd, entrypoint, selection.Source, adapter.CapabilityGeneration(), state, root, readRoots, []string{cwd}, tempRoots, deniedRoots)
	policy, err := containment.NewExecutionPolicySnapshot(&spec)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace Guest policy: %w", err)
	}
	if state == containment.StateUnavailable {
		return containment.NewUnavailableBinding(containment.ProcessClassGuest, policy, adapter, reason)
	}
	probe := adapter.Probe(ctx, policy)
	if probe.ReasonCode == "" {
		return containment.NewBinding(containment.ProcessClassGuest, policy, adapter, probe.Proof)
	}
	unavailable := policy.Spec()
	unavailable.State = containment.StateUnavailable
	unavailablePolicy, err := containment.NewExecutionPolicySnapshot(&unavailable)
	if err != nil {
		return nil, fmt.Errorf("resolve unavailable Guest policy: %w", err)
	}
	return containment.NewUnavailableBinding(containment.ProcessClassGuest, unavailablePolicy, adapter, probe.ReasonCode)
}

func workspaceGuestSpec(
	cwd string,
	entrypoint containment.Entrypoint,
	source containment.SelectionSource,
	generation string,
	state containment.State,
	root containment.RootIdentity,
	readRoots, writeRoots, tempRoots, deniedRoots []string,
) containment.Spec {
	return containment.Spec{
		Version: containment.PolicyVersion, Profile: containment.ProfileWorkspaceWrite, State: state,
		SelectionSource: source, Adapter: containment.AdapterDarwinSeatbelt,
		Platform: runtime.GOOS, Architecture: runtime.GOARCH, CapabilityGeneration: generation,
		CWD: cwd, Root: root, ReadRoots: readRoots, WriteRoots: writeRoots, TempRoots: tempRoots, DeniedRoots: deniedRoots,
		Network:     containment.NetworkPolicy{Mode: containment.NetworkDenied, ProjectionID: guestNetworkIdentity},
		Environment: containment.EnvironmentPolicy{ProjectionID: ambientEnvIdentity},
		Credentials: containment.CredentialPolicy{Mode: containment.CredentialAmbientEnvironment, ProjectionID: ambientEnvIdentity},
		Resources:   containment.ResourceLimits{WallTimeMillis: guestWallTimeMillis, OutputBytes: guestRetainedOutput},
		Descendants: containment.DescendantPolicy{Mode: containment.DescendantCleanupRequired},
		Entrypoint:  entrypoint, Lineage: containment.Lineage{RootID: "root"},
	}
}

func workspaceGuestRoots(cwd string, extraReadRoots []string) ([]string, []string, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sandbox home directory is unavailable")
	}
	home, err = canonicalExistingDirectory(home)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sandbox home directory is unavailable")
	}
	readRoots := append([]string{cwd}, extraReadRoots...)
	for _, candidate := range []string{
		"/System", "/usr", "/bin", "/sbin", "/Library", "/opt/homebrew", "/usr/local",
		goToolchainRoot(), filepath.Join(home, "go", "pkg", "mod"),
	} {
		if root, rootErr := canonicalExistingDirectory(candidate); rootErr == nil {
			readRoots = append(readRoots, root)
		}
	}
	gitConfigRoots := []string(nil)
	for _, candidate := range []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config", "git", "config"),
		"/etc/gitconfig",
	} {
		if root, rootErr := canonicalExistingPath(candidate); rootErr == nil {
			readRoots = append(readRoots, root)
			gitConfigRoots = append(gitConfigRoots, root)
		}
	}
	tempRoots := make([]string, 0, 2)
	for _, candidate := range []string{
		os.TempDir(),
		filepath.Join(home, "Library", "Caches", "go-build"),
	} {
		if root, rootErr := canonicalExistingDirectory(candidate); rootErr == nil {
			tempRoots = append(tempRoots, root)
		}
	}
	if len(tempRoots) == 0 {
		return nil, nil, nil, fmt.Errorf("sandbox temporary directory is unavailable")
	}
	deniedRoots := []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".claude", "agents"),
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".codex", "agents"),
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config", "git", "config"),
		filepath.Join(cwd, ".eino-agent"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(cwd, ".claude", "agents"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(cwd, ".codex", "agents"),
		filepath.Join(cwd, ".git", "config"),
		filepath.Join(cwd, ".git", "hooks"),
	}
	deniedRoots = append(deniedRoots, gitConfigRoots...)
	sort.Strings(readRoots)
	sort.Strings(tempRoots)
	sort.Strings(deniedRoots)
	return compactPaths(readRoots), compactPaths(tempRoots), compactPaths(deniedRoots), nil
}

func goToolchainRoot() string {
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(goExecutable)
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(resolved))
}

func canonicalExistingPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(firstPath(path))
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("file is unavailable")
	}
	return resolved, nil
}

func compactPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	result := paths[:1]
	for _, path := range paths[1:] {
		if path != result[len(result)-1] {
			result = append(result, path)
		}
	}
	return append([]string(nil), result...)
}

func canonicalExistingDirectory(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(firstPath(path))
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("directory is unavailable")
	}
	return resolved, nil
}

// DeriveChildExecutionBindings keeps extension authority in the exact parent
// ShellHooks/StdioMCP bindings while deriving and, when available, reproving a
// distinct equal-or-narrower Guest binding for the child root.
func DeriveChildExecutionBindings(
	ctx context.Context,
	parent *containment.Bindings,
	cwd string,
	childIdentity string,
) (*containment.Bindings, error) {
	if parent == nil || parent.Guest() == nil || parent.ShellHooks() == nil || parent.StdioMCP() == nil {
		return nil, fmt.Errorf("parent execution bindings are unavailable")
	}
	parentGuest := parent.Guest()
	var childGuest *containment.Binding
	var err error
	switch parentGuest.AdapterFamily() {
	case containment.AdapterAmbientHost:
		childGuest, err = deriveAmbientChildGuest(parentGuest, cwd, childIdentity)
	case containment.AdapterDarwinSeatbelt:
		childGuest, err = deriveSeatbeltChildGuest(ctx, parentGuest, cwd, childIdentity)
	default:
		err = fmt.Errorf("parent Guest adapter is unsupported")
	}
	if err != nil {
		return nil, err
	}
	return containment.NewBindings(childGuest, parent.ShellHooks(), parent.StdioMCP())
}

// DeriveRestoredExecutionBindings narrows only Guest execution to a restored
// working directory. Shell hooks and stdio MCP retain their independent,
// already-bound ambient identities; persisted session data cannot turn either
// extension class into Guest authority or widen the parent Guest write roots.
func DeriveRestoredExecutionBindings(
	ctx context.Context,
	parent *containment.Bindings,
	cwd string,
	restoreIdentity string,
) (*containment.Bindings, error) {
	if parent == nil || parent.Guest() == nil || parent.ShellHooks() == nil || parent.StdioMCP() == nil {
		return nil, fmt.Errorf("restore parent execution bindings are unavailable")
	}
	canonicalCWD, err := canonicalExistingDirectory(cwd)
	if err != nil {
		return nil, fmt.Errorf("restored Guest root identity is unavailable")
	}
	parentGuest := parent.Guest()
	if canonicalCWD == parentGuest.Policy().Spec().CWD && parentGuest.AdapterFamily() == containment.AdapterAmbientHost {
		return parent, nil
	}

	var restoredGuest *containment.Binding
	switch parentGuest.AdapterFamily() {
	case containment.AdapterAmbientHost:
		restoredGuest, err = deriveAmbientRestoredGuest(parentGuest, canonicalCWD, restoreIdentity)
	case containment.AdapterDarwinSeatbelt:
		parentSpec := parentGuest.Policy().Spec()
		if canonicalCWD == parentSpec.CWD && parentSpec.Root.Device != 0 && parentSpec.Root.Inode != 0 {
			if err := containment.RevalidateRootIdentity(parentSpec.Root); err != nil {
				return nil, fmt.Errorf("restored Guest root identity changed")
			}
		}
		restoredGuest, err = deriveSeatbeltGuest(
			ctx,
			parentGuest,
			canonicalCWD,
			parentSpec.Entrypoint,
			parentSpec.SelectionSource,
			"restore:"+restoreIdentity,
		)
	default:
		err = fmt.Errorf("restored Guest adapter is unsupported")
	}
	if err != nil {
		return nil, err
	}
	return containment.NewBindings(restoredGuest, parent.ShellHooks(), parent.StdioMCP())
}

func deriveAmbientChildGuest(parent *containment.Binding, cwd, childIdentity string) (*containment.Binding, error) {
	if parent.Availability() != containment.BindingAvailable {
		return nil, fmt.Errorf("ambient parent Guest binding is unavailable")
	}
	spec := parent.Policy().Spec()
	spec.CWD = firstPath(cwd)
	spec.Entrypoint = containment.EntrypointChildAgent
	spec.Lineage.ParentDigest = parent.Digest()
	spec.Lineage.ChildID = opaqueChildIdentity(childIdentity)
	policy, err := containment.NewExecutionPolicySnapshot(&spec)
	if err != nil {
		return nil, fmt.Errorf("derive ambient child Guest policy: %w", err)
	}
	return containment.NewBinding(containment.ProcessClassGuest, policy, containment.NewAmbientHostAdapter(), containment.AdapterProof{})
}

func deriveAmbientRestoredGuest(parent *containment.Binding, cwd, restoreIdentity string) (*containment.Binding, error) {
	if parent.Availability() != containment.BindingAvailable {
		return nil, fmt.Errorf("ambient parent Guest binding is unavailable")
	}
	spec := parent.Policy().Spec()
	spec.CWD = cwd
	spec.Lineage.ParentDigest = parent.Digest()
	spec.Lineage.ChildID = opaqueChildIdentity("restore:" + restoreIdentity)
	policy, err := containment.NewExecutionPolicySnapshot(&spec)
	if err != nil {
		return nil, fmt.Errorf("derive restored ambient Guest policy: %w", err)
	}
	return containment.NewBinding(containment.ProcessClassGuest, policy, containment.NewAmbientHostAdapter(), containment.AdapterProof{})
}

func deriveSeatbeltChildGuest(ctx context.Context, parent *containment.Binding, cwd, childIdentity string) (*containment.Binding, error) {
	return deriveSeatbeltGuest(
		ctx,
		parent,
		cwd,
		containment.EntrypointChildAgent,
		containment.SelectionChild,
		childIdentity,
	)
}

func deriveSeatbeltGuest(
	ctx context.Context,
	parent *containment.Binding,
	cwd string,
	entrypoint containment.Entrypoint,
	source containment.SelectionSource,
	lineageIdentity string,
) (*containment.Binding, error) {
	parentSpec := parent.Policy().Spec()
	canonicalCWD, err := filepath.Abs(filepath.Clean(firstPath(cwd)))
	if err != nil {
		return nil, fmt.Errorf("child Guest write root is invalid")
	}
	adapter := containment.NewDarwinSeatbeltAdapter()
	if adapter.CapabilityGeneration() != parent.CapabilityGeneration() {
		return nil, fmt.Errorf("child Guest adapter generation mismatch")
	}
	root := containment.RootIdentity{Path: canonicalCWD}
	if parent.Availability() == containment.BindingAvailable || runtime.GOOS == "darwin" {
		captured, captureErr := containment.CaptureRootIdentity(canonicalCWD)
		if captureErr != nil {
			return nil, fmt.Errorf("child Guest root identity is unavailable")
		}
		root = captured
		canonicalCWD = captured.Path
	}
	if !pathWithinAny(canonicalCWD, parentSpec.WriteRoots) {
		return nil, fmt.Errorf("child Guest canonical root is not a subset")
	}
	childSpec := parentSpec
	childSpec.CWD = canonicalCWD
	childSpec.Root = root
	childSpec.WriteRoots = []string{canonicalCWD}
	childSpec.SelectionSource = source
	childSpec.Entrypoint = entrypoint
	childSpec.Lineage.ParentDigest = parent.Digest()
	childSpec.Lineage.ChildID = opaqueChildIdentity(lineageIdentity)
	if parent.Availability() == containment.BindingUnavailable {
		childSpec.State = containment.StateUnavailable
		if root.Device == 0 || root.Inode == 0 {
			childSpec.Root = containment.RootIdentity{Path: canonicalCWD}
		}
		policy, policyErr := containment.NewExecutionPolicySnapshot(&childSpec)
		if policyErr != nil {
			return nil, fmt.Errorf("derive unavailable child Guest policy: %w", policyErr)
		}
		return containment.NewUnavailableBinding(containment.ProcessClassGuest, policy, adapter, parent.ReasonCode())
	}
	childSpec.State = containment.StateDegraded
	policy, err := containment.NewExecutionPolicySnapshot(&childSpec)
	if err != nil {
		return nil, fmt.Errorf("derive child Guest policy: %w", err)
	}
	probe := adapter.Probe(ctx, policy)
	if probe.ReasonCode == "" {
		return containment.NewBinding(containment.ProcessClassGuest, policy, adapter, probe.Proof)
	}
	if probe.ReasonCode == containment.ReasonRootChanged {
		return nil, fmt.Errorf("child Guest root identity changed")
	}
	unavailable := policy.Spec()
	unavailable.State = containment.StateUnavailable
	unavailablePolicy, err := containment.NewExecutionPolicySnapshot(&unavailable)
	if err != nil {
		return nil, fmt.Errorf("derive unavailable child Guest policy: %w", err)
	}
	return containment.NewUnavailableBinding(containment.ProcessClassGuest, unavailablePolicy, adapter, probe.ReasonCode)
}

func pathWithinAny(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func opaqueChildIdentity(identity string) string {
	if strings.TrimSpace(identity) == "" {
		return "child"
	}
	sum := sha256.Sum256([]byte(identity))
	return "child:" + hex.EncodeToString(sum[:16])
}

func containmentEntrypoint(entrypoint commands.Entrypoint) containment.Entrypoint {
	switch entrypoint {
	case commands.EntrypointTUI:
		return containment.EntrypointTUI
	case commands.EntrypointPlain:
		return containment.EntrypointPlain
	case commands.EntrypointHeadless:
		return containment.EntrypointHeadless
	case commands.EntrypointHeadlessGoal:
		return containment.EntrypointHeadlessGoal
	case commands.EntrypointACP:
		return containment.EntrypointACP
	default:
		return containment.EntrypointEmbedded
	}
}

func bindExecutionPolicy(config *QueryEngineConfig) {
	if config == nil {
		panic("engine: execution containment config is required")
	}
	if config.SandboxSelection != nil && config.ExecutionPolicy != nil {
		panic("engine: sandbox selection cannot replace an explicit execution policy")
	}
	bindings := config.ExecutionBindings
	if bindings != nil && config.ExecutionPolicy != nil {
		guest := bindings.Guest()
		if guest == nil || guest.PolicyDigest() != config.ExecutionPolicy.Digest() {
			panic("engine: execution policy and Guest binding mismatch")
		}
	}
	var err error
	switch {
	case bindings != nil:
	case config.SandboxSelection != nil:
		bindings, err = ResolveExecutionBindings(context.Background(), config.CWD, config.CommandEntrypoint, config.SandboxSelection)
	case config.ExecutionPolicy != nil:
		bindings, err = ambientBindingsForPolicy(config.ExecutionPolicy)
	default:
		bindings, err = ResolveDisabledExecutionBindings(config.CWD, config.CommandEntrypoint)
	}
	if err != nil {
		panic(fmt.Sprintf("engine: resolve execution bindings: %v", err))
	}
	if err := validateExecutionBindings(bindings); err != nil {
		panic(fmt.Sprintf("engine: invalid execution bindings: %v", err))
	}
	if config.SandboxSelection != nil {
		guestSpec := bindings.Guest().Policy().Spec()
		if guestSpec.Profile != config.SandboxSelection.Profile || guestSpec.SelectionSource != config.SandboxSelection.Source {
			panic("engine: sandbox selection and Guest binding mismatch")
		}
	}
	config.ExecutionBindings = bindings
	config.ExecutionPolicy = bindings.Guest().Policy()
}

func validateExecutionBindings(bindings *containment.Bindings) error {
	if bindings == nil || bindings.Guest() == nil || bindings.ShellHooks() == nil || bindings.StdioMCP() == nil {
		return fmt.Errorf("all process classes are required")
	}
	guest := bindings.Guest()
	if guest.ProcessClass() != containment.ProcessClassGuest || guest.Digest() == "" {
		return fmt.Errorf("guest binding is invalid")
	}
	if guest.AdapterFamily() == containment.AdapterAmbientHost && guest.Availability() != containment.BindingAvailable {
		return fmt.Errorf("ambient Guest must be available")
	}
	for _, binding := range []*containment.Binding{bindings.ShellHooks(), bindings.StdioMCP()} {
		if binding.Availability() != containment.BindingAvailable || binding.AdapterFamily() != containment.AdapterAmbientHost {
			return fmt.Errorf("extension binding must be available ambient-host")
		}
		diagnostic := binding.Policy().Diagnostic()
		if diagnostic.Profile != containment.ProfileDangerFullAccess || diagnostic.State != containment.StateDisabled {
			return fmt.Errorf("extension binding policy is invalid")
		}
	}
	return nil
}

// ExecutionPolicySnapshot returns the immutable snapshot bound to this engine.
func (e *QueryEngine) ExecutionPolicySnapshot() *containment.Snapshot {
	if e == nil {
		return nil
	}
	return e.executionPolicy
}

// ExecutionBindingMatrix returns the immutable process-class identities bound
// before this engine's process owners were initialized.
func (e *QueryEngine) ExecutionBindingMatrix() *containment.Bindings {
	if e == nil {
		return nil
	}
	return e.executionBindings
}

// GuestExecutionProof returns the adapter/runtime axis proof for available
// Seatbelt Guest execution. Ambient and unavailable Guests return zero proof.
func (e *QueryEngine) GuestExecutionProof() containment.ExecutionProof {
	if e == nil || e.shellManager == nil {
		return containment.ExecutionProof{}
	}
	return e.shellManager.GuestExecutionProof()
}

// ExecutionContainmentStartupDiagnostic returns one redacted, bounded startup
// diagnostic for user-facing composition roots. It never treats a permission
// mode as containment and never includes workspace paths or environment data.
func (e *QueryEngine) ExecutionContainmentStartupDiagnostic() (string, string) {
	if e == nil || e.executionBindings == nil || e.executionBindings.Guest() == nil {
		return "", ""
	}
	guest := e.executionBindings.Guest()
	switch {
	case guest.AdapterFamily() == containment.AdapterAmbientHost:
		return "sandbox-danger-full-access", "Guest Bash runs with ambient host authority; permission mode does not provide containment"
	case guest.Availability() == containment.BindingUnavailable:
		return "sandbox-guest-unavailable", fmt.Sprintf("Guest Bash is unavailable before spawn (%s); no ambient fallback will be attempted", guest.ReasonCode())
	case guest.AdapterFamily() == containment.AdapterDarwinSeatbelt:
		proof := e.GuestExecutionProof()
		if proof.BindingDigest != guest.Digest() || proof.PolicyDigest != guest.PolicyDigest() {
			return "sandbox-guest-unavailable", "Guest Bash containment proof is unavailable; no ambient fallback will be attempted"
		}
		return "sandbox-workspace-write-degraded", "Guest Bash is confined by Darwin Seatbelt for filesystem, network, root identity, and descendants; environment credentials plus hard memory, file-descriptor, and process-count limits remain ambient"
	default:
		return "sandbox-guest-unavailable", "Guest Bash containment is unavailable; no ambient fallback will be attempted"
	}
}
