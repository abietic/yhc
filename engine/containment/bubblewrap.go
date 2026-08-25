package containment

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

const (
	linuxBubblewrapExecutable = "/usr/bin/bwrap"
	linuxBubblewrapGeneration = "linux-bubblewrap-v1"
	bubblewrapProbeTimeout    = 8 * time.Second
)

var (
	errBubblewrapUnsupported = errors.New("bubblewrap: unsupported platform")
	errBubblewrapExecutable  = errors.New("bubblewrap: executable unavailable")
	errBubblewrapPolicy      = errors.New("bubblewrap: policy invalid")
	errBubblewrapProbe       = errors.New("bubblewrap: probe failed")
	errBubblewrapRootChanged = errors.New("bubblewrap: root changed")
	errBubblewrapRequest     = errors.New("bubblewrap: request rejected")
)

type bubblewrapDependencies struct {
	goos        string
	goarch      string
	executable  func() error
	captureRoot func(string) (RootIdentity, error)
	seccompFile func() (*os.File, error)
	run         func(context.Context, SpawnSpec) error
	probe       func(context.Context, *Snapshot) error
}

// NewLinuxBubblewrapAdapter returns the fixed-binary Linux Guest adapter. It
// never searches PATH and never falls back to ambient execution.
func NewLinuxBubblewrapAdapter() SpawnAdapter {
	return newLinuxBubblewrapAdapter(bubblewrapDependencies{
		goos: runtimeGOOS(), goarch: runtimeGOARCH(), executable: verifyBubblewrapExecutable,
		captureRoot: CaptureRootIdentity, seccompFile: newSocketDenySeccompFile,
		run: runBubblewrapSpawn,
	})
}

type linuxBubblewrapAdapter struct{ deps bubblewrapDependencies }

func newLinuxBubblewrapAdapter(deps bubblewrapDependencies) *linuxBubblewrapAdapter {
	return &linuxBubblewrapAdapter{deps: deps}
}

func (*linuxBubblewrapAdapter) Family() AdapterFamily { return AdapterLinuxBubblewrap }

func (*linuxBubblewrapAdapter) CapabilityGeneration() string {
	return linuxBubblewrapGeneration
}

func (a *linuxBubblewrapAdapter) supported() bool {
	return a != nil && a.deps.goos == "linux" && (a.deps.goarch == "amd64" || a.deps.goarch == "arm64")
}

func (a *linuxBubblewrapAdapter) Probe(ctx context.Context, policy *Snapshot) ProbeResult {
	if !a.supported() {
		return a.failed(policy, ReasonUnsupportedPlatform)
	}
	if !validBubblewrapPolicy(policy, a.CapabilityGeneration()) {
		return a.failed(policy, ReasonProfileInvalid)
	}
	if a.deps.executable == nil || a.deps.executable() != nil {
		return a.failed(policy, ReasonExecutableMissing)
	}
	if err := a.revalidate(policy.Spec().Root); err != nil {
		return a.failed(policy, ReasonRootChanged)
	}
	probeCtx, cancel := boundedBubblewrapContext(ctx)
	defer cancel()
	var err error
	if a.deps.probe != nil {
		err = a.deps.probe(probeCtx, policy)
	} else {
		err = a.probeCapabilities(probeCtx, policy)
	}
	if err != nil {
		return a.failed(policy, ReasonProbeFailed)
	}
	return ProbeResult{Proof: AdapterProof{
		PolicyDigest: policy.Digest(), CapabilityGeneration: a.CapabilityGeneration(), Enforced: adapterAllowedAxes,
	}, Diagnostic: policy.Diagnostic()}
}

func (a *linuxBubblewrapAdapter) probeCapabilities(ctx context.Context, policy *Snapshot) error {
	if a == nil || policy == nil || a.deps.run == nil || a.deps.captureRoot == nil || a.deps.seccompFile == nil {
		return errBubblewrapProbe
	}
	base, err := os.MkdirTemp("", "yhc-bwrap-probe-")
	if err != nil {
		return errBubblewrapProbe
	}
	defer os.RemoveAll(base) //nolint:errcheck // private bounded probe root
	allowed := filepath.Join(base, "allowed")
	denied := filepath.Join(base, "denied")
	if os.Mkdir(allowed, 0o700) != nil || os.Mkdir(denied, 0o700) != nil {
		return errBubblewrapProbe
	}
	identity, err := a.deps.captureRoot(allowed)
	if err != nil {
		return errBubblewrapProbe
	}
	probeSpec := policy.Spec()
	probeSpec.CWD = identity.Path
	probeSpec.Root = identity
	probeSpec.ReadRoots = append(existingLinuxRuntimeRoots(), identity.Path)
	probeSpec.WriteRoots = []string{identity.Path}
	probeSpec.TempRoots = nil
	probeSpec.DeniedRoots = nil
	probePolicy, err := NewExecutionPolicySnapshot(&probeSpec)
	if err != nil {
		return errBubblewrapProbe
	}
	run := func(executable string, args ...string) error {
		spawn, spawnErr := a.spawn(probePolicy, SpawnRequest{Executable: executable, Args: args, Dir: identity.Path})
		if spawnErr != nil {
			return errBubblewrapProbe
		}
		return a.deps.run(ctx, spawn)
	}

	allowedFile := filepath.Join(allowed, "allowed")
	if run("/bin/sh", "-c", "printf allowed > \"$1\"", "sh", allowedFile) != nil || run("/bin/cat", allowedFile) != nil {
		return errBubblewrapProbe
	}
	deniedWrite := filepath.Join(denied, "write")
	if run("/bin/sh", "-c", "printf denied > \"$1\"", "sh", deniedWrite) == nil {
		return errBubblewrapProbe
	}
	if _, statErr := os.Stat(deniedWrite); !os.IsNotExist(statErr) {
		return errBubblewrapProbe
	}
	deniedRead := filepath.Join(denied, "read")
	if os.WriteFile(deniedRead, []byte("secret"), 0o600) != nil || run("/bin/cat", deniedRead) == nil {
		return errBubblewrapProbe
	}

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return errBubblewrapProbe
	}
	closeListener := closeListenerOnContext(ctx, listener)
	defer closeListener()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if run("/bin/bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/"+port) == nil {
		return errBubblewrapProbe
	}
	if run("/bin/bash", "-c", "printf denied >/dev/udp/127.0.0.1/9") == nil {
		return errBubblewrapProbe
	}
	descendantWrite := filepath.Join(denied, "descendant")
	if run("/bin/sh", "-c", "/bin/sh -c 'printf denied > \"$1\"' sh \"$1\"", "sh", descendantWrite) == nil {
		return errBubblewrapProbe
	}
	if _, statErr := os.Stat(descendantWrite); !os.IsNotExist(statErr) {
		return errBubblewrapProbe
	}
	return nil
}

func (a *linuxBubblewrapAdapter) Prepare(_ context.Context, request SpawnRequest) (SpawnSpec, error) {
	if !a.supported() {
		return SpawnSpec{}, errBubblewrapUnsupported
	}
	if request.Binding == nil || request.Binding.Policy() == nil || !validBubblewrapPolicy(request.Binding.Policy(), a.CapabilityGeneration()) || request.Dir != request.Binding.Policy().Spec().CWD {
		return SpawnSpec{}, errBubblewrapRequest
	}
	if a.deps.executable == nil || a.deps.executable() != nil {
		return SpawnSpec{}, errBubblewrapExecutable
	}
	if err := a.revalidate(request.Binding.Policy().Spec().Root); err != nil {
		return SpawnSpec{}, err
	}
	return a.spawn(request.Binding.Policy(), request)
}

func (a *linuxBubblewrapAdapter) spawn(policy *Snapshot, request SpawnRequest) (SpawnSpec, error) {
	if a == nil || a.deps.seccompFile == nil {
		return SpawnSpec{}, errBubblewrapRequest
	}
	filter, err := a.deps.seccompFile()
	if err != nil {
		return SpawnSpec{}, errBubblewrapProbe
	}
	args, err := renderBubblewrapArgs(policy.Spec(), request.Executable, request.Args)
	if err != nil {
		_ = filter.Close()
		return SpawnSpec{}, err
	}
	return SpawnSpec{
		Path: linuxBubblewrapExecutable, Args: args, Dir: request.Dir,
		Env: append([]string(nil), request.Env...), BindingDigest: bindingDigest(request.Binding),
		ExtraFiles: []*os.File{filter},
	}, nil
}

func (a *linuxBubblewrapAdapter) revalidate(identity RootIdentity) error {
	if a == nil || a.deps.captureRoot == nil {
		return errBubblewrapRootChanged
	}
	current, err := a.deps.captureRoot(identity.Path)
	if err != nil || current != identity {
		return errBubblewrapRootChanged
	}
	return nil
}

func (a *linuxBubblewrapAdapter) failed(policy *Snapshot, reason ReasonCode) ProbeResult {
	var diagnostic Diagnostic
	if policy != nil {
		diagnostic = policy.Diagnostic()
	}
	return ProbeResult{Diagnostic: diagnostic, ReasonCode: reason}
}

func validBubblewrapPolicy(policy *Snapshot, generation string) bool {
	if policy == nil {
		return false
	}
	spec := policy.Spec()
	return spec.Adapter == AdapterLinuxBubblewrap && spec.State == StateDegraded && spec.Profile == ProfileWorkspaceWrite &&
		spec.Platform == "linux" && (spec.Architecture == "amd64" || spec.Architecture == "arm64") &&
		spec.CapabilityGeneration == generation && spec.Network.Mode == NetworkDenied && spec.Root.Path == spec.CWD &&
		spec.Root.Device != 0 && spec.Root.Inode != 0
}

func boundedBubblewrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, bubblewrapProbeTimeout)
}

func renderBubblewrapArgs(spec Spec, executable string, commandArgs []string) ([]string, error) {
	if executable == "" || spec.CWD == "" {
		return nil, errBubblewrapPolicy
	}
	args := []string{"--new-session", "--die-with-parent", "--tmpfs", "/", "--dev", "/dev"}
	appendMounts := func(flag string, roots []string, existingOnly bool) {
		roots = append([]string(nil), roots...)
		sort.Slice(roots, func(i, j int) bool {
			if len(roots[i]) == len(roots[j]) {
				return roots[i] < roots[j]
			}
			return len(roots[i]) < len(roots[j])
		})
		for _, root := range roots {
			if root == "" || (existingOnly && !pathExists(root)) {
				continue
			}
			args = append(args, flag, root, root)
		}
	}
	appendMounts("--ro-bind", spec.ReadRoots, true)
	appendMounts("--bind", append(append([]string(nil), spec.WriteRoots...), spec.TempRoots...), true)
	appendMounts("--ro-bind", spec.DeniedRoots, true)
	args = append(args,
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-net", "--disable-userns",
		"--proc", "/proc", "--chdir", spec.CWD, "--cap-drop", "ALL", "--seccomp", "3", "--", executable,
	)
	args = append(args, commandArgs...)
	return args, nil
}

func existingLinuxRuntimeRoots() []string {
	candidates := []string{"/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/nix/store", "/run/current-system/sw"}
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if pathExists(candidate) {
			roots = append(roots, candidate)
		}
	}
	return roots
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func closeSpawnExtraFiles(files []*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
