package containment

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	darwinSeatbeltExecutable = "/usr/bin/sandbox-exec"
	darwinSeatbeltGeneration = "darwin-seatbelt-v1"
	seatbeltProbeTimeout     = 5 * time.Second
	seatbeltProfileSource    = `(version 1)
(deny default)

;; Adapted from Codex seatbelt_base_policy.sbpl: keep only execution, signal,
;; read runtime, and device primitives required by a dynamically linked child.
(allow process-exec)
(allow process-fork)
(allow process-info* (target same-sandbox))
(allow signal (target same-sandbox))
(allow sysctl-read
  (sysctl-name "hw.activecpu")
  (sysctl-name "hw.memsize")
  (sysctl-name "hw.ncpu")
  (sysctl-name "hw.pagesize")
  (sysctl-name "hw.pagesize_compat")
  (sysctl-name "kern.osproductversion")
  (sysctl-name "kern.osrelease")
  (sysctl-name "kern.ostype")
  (sysctl-name "kern.version")
  (sysctl-name-prefix "hw.optional.arm.")
  (sysctl-name-prefix "kern.proc.pid."))
(allow iokit-open (iokit-registry-entry-class "RootDomainUserClient"))
(allow mach-lookup (global-name "com.apple.system.opendirectoryd.libinfo"))
(allow mach-lookup (global-name "com.apple.PowerManagement.control"))
(allow ipc-posix-sem)
(allow pseudo-tty)
(allow ipc-posix-shm-read* (ipc-posix-name-prefix "apple.cfprefs."))
(allow mach-lookup
  (global-name "com.apple.cfprefsd.daemon")
  (global-name "com.apple.cfprefsd.agent")
  (local-name "com.apple.cfprefsd.agent"))
(allow user-preference-read)
(allow file-read* (subpath "/System"))
(allow file-read* file-test-existence
  (subpath "/Library/Apple")
  (subpath "/Library/Filesystems/NetFSPlugins")
  (subpath "/Library/Preferences")
  (subpath "/private/var/db/timezone"))
(allow file-map-executable
  (subpath "/Library/Apple/System/Library/Frameworks")
  (subpath "/Library/Apple/System/Library/PrivateFrameworks")
  (subpath "/Library/Apple/usr/lib")
  (subpath "/System/Library/Extensions")
  (subpath "/System/Library/Frameworks")
  (subpath "/System/Library/PrivateFrameworks")
  (subpath "/System/Library/SubFrameworks")
  (subpath "/System/iOSSupport/System/Library/Frameworks")
  (subpath "/System/iOSSupport/System/Library/PrivateFrameworks")
  (subpath "/System/iOSSupport/System/Library/SubFrameworks")
  (subpath "/usr/lib"))
(allow system-mac-syscall (mac-policy-name "vnguard"))
(allow system-mac-syscall
  (require-all (mac-policy-name "Sandbox") (mac-syscall-number 67)))
(allow system-fsctl (fsctl-command FSIOC_CAS_BSDFLAGS))
(allow file-read-metadata file-test-existence
  (literal "/etc")
  (literal "/tmp")
  (literal "/var")
  (literal "/private/etc/localtime")
  (path-ancestors "/System/Volumes/Data/private"))
(allow file-read* file-test-existence (literal "/"))
(allow file-read* (literal "/private/etc/localtime"))
(allow file-read* (subpath "/usr/lib"))
(allow file-read* (subpath "/usr/share"))
(allow file-read* (subpath "/usr/bin"))
(allow file-read* (subpath "/bin"))
(allow file-read* (literal "/dev/null"))
(allow file-write* (literal "/dev/null"))
(allow file-read* (literal "/dev/urandom"))
(allow file-read* (literal "/dev/random"))
(allow file-read* file-test-existence (literal "/dev/autofs_nowait"))
(allow file-read* file-test-existence (literal "/dev/zero"))
(allow file-write-data (literal "/dev/zero"))
(allow file-read-data file-test-existence file-write-data (subpath "/dev/fd"))
(allow file-read* file-test-existence file-write-data file-ioctl (literal "/dev/dtracehelper"))
(allow file-read* (regex "^/dev/fd/(0|1|2)$"))
(allow file-write* (regex "^/dev/fd/(1|2)$"))
(allow file-read-metadata (literal "/dev"))
(allow file-read-metadata (regex "^/dev/.*$"))
(allow file-read-metadata (literal "/dev/stdin"))
(allow file-read-metadata (literal "/dev/stdout"))
(allow file-read-metadata (literal "/dev/stderr"))
(allow file-read-metadata (regex "^/dev/tty[^/]*$"))
(allow file-read-metadata (regex "^/dev/pty[^/]*$"))
(allow file-read* file-write* (regex "^/dev/ttys[0-9]+$"))
(allow file-read* file-write* (literal "/dev/ptmx"))
(allow file-ioctl (regex "^/dev/ttys[0-9]+$"))
{{ROOT_RULES}}
`
)

var (
	errSeatbeltUnsupported = errors.New("seatbelt: unsupported platform")
	errSeatbeltExecutable  = errors.New("seatbelt: executable unavailable")
	errSeatbeltProfile     = errors.New("seatbelt: profile invalid")
	errSeatbeltProbe       = errors.New("seatbelt: probe failed")
	errSeatbeltRootChanged = errors.New("seatbelt: root changed")
	errSeatbeltRequest     = errors.New("seatbelt: request rejected")
)

type seatbeltDependencies struct {
	goos        string
	goarch      string
	executable  func() error
	captureRoot func(string) (RootIdentity, error)
	run         func(context.Context, SpawnSpec) error
	probe       func(context.Context, *Snapshot) error
}

// NewDarwinSeatbeltAdapter returns the fixed-binary Darwin Seatbelt adapter.
// On unsupported hosts Probe fails closed and CaptureRootIdentity returns no identity.
func NewDarwinSeatbeltAdapter() SpawnAdapter {
	return newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: runtimeGOOS(), goarch: runtimeGOARCH(), executable: verifySeatbeltExecutable,
		captureRoot: CaptureRootIdentity, run: runSeatbeltSpawn,
	})
}

type darwinSeatbeltAdapter struct{ deps seatbeltDependencies }

func newDarwinSeatbeltAdapter(deps seatbeltDependencies) *darwinSeatbeltAdapter {
	return &darwinSeatbeltAdapter{deps: deps}
}

func (a *darwinSeatbeltAdapter) Family() AdapterFamily        { return AdapterDarwinSeatbelt }
func (a *darwinSeatbeltAdapter) CapabilityGeneration() string { return darwinSeatbeltGeneration }

func (a *darwinSeatbeltAdapter) Probe(ctx context.Context, policy *Snapshot) ProbeResult {
	if !a.supported() {
		return a.failed(policy, ReasonUnsupportedPlatform)
	}
	if !validSeatbeltPolicy(policy, a.CapabilityGeneration()) {
		return a.failed(policy, ReasonProfileInvalid)
	}
	if a.deps.executable == nil || a.deps.executable() != nil {
		return a.failed(policy, ReasonExecutableMissing)
	}
	if err := a.revalidate(policy.Spec().Root); err != nil {
		return a.failed(policy, ReasonRootChanged)
	}
	probeCtx, cancel := boundedSeatbeltContext(ctx)
	defer cancel()
	var probeErr error
	if a.deps.probe != nil {
		probeErr = a.deps.probe(probeCtx, policy)
	} else {
		probeErr = a.probeCapabilities(probeCtx, policy)
	}
	if probeErr != nil {
		return a.failed(policy, ReasonProbeFailed)
	}
	return ProbeResult{Proof: AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: a.CapabilityGeneration(), Enforced: adapterAllowedAxes}, Diagnostic: policy.Diagnostic()}
}

func (a *darwinSeatbeltAdapter) probeCapabilities(ctx context.Context, policy *Snapshot) error {
	if a == nil || policy == nil || a.deps.run == nil || a.deps.captureRoot == nil {
		return errSeatbeltProbe
	}
	base, err := os.MkdirTemp("/tmp", "eino-seatbelt-probe-")
	if err != nil {
		return errSeatbeltProbe
	}
	defer os.RemoveAll(base) //nolint:errcheck // private bounded probe root
	allowed := filepath.Join(base, "allowed")
	denied := filepath.Join(base, "denied")
	if os.Mkdir(allowed, 0o700) != nil || os.Mkdir(denied, 0o700) != nil {
		return errSeatbeltProbe
	}
	identity, err := a.deps.captureRoot(allowed)
	if err != nil {
		return errSeatbeltProbe
	}
	probeSpec := policy.Spec()
	probeSpec.CWD = identity.Path
	probeSpec.Root = identity
	probeSpec.ReadRoots = []string{identity.Path}
	probeSpec.WriteRoots = []string{identity.Path}
	probeSpec.TempRoots = nil
	probeSpec.DeniedRoots = nil
	probePolicy, err := NewExecutionPolicySnapshot(&probeSpec)
	if err != nil {
		return errSeatbeltProbe
	}
	run := func(executable string, args ...string) error {
		spawn, spawnErr := a.spawn(probePolicy, SpawnRequest{Executable: executable, Args: args, Dir: identity.Path})
		if spawnErr != nil {
			return errSeatbeltProbe
		}
		return a.deps.run(ctx, spawn)
	}

	allowedFile := filepath.Join(allowed, "allowed")
	if run("/bin/sh", "-c", "printf allowed > \"$1\"", "sh", allowedFile) != nil {
		return errSeatbeltProbe
	}
	if run("/bin/cat", allowedFile) != nil {
		return errSeatbeltProbe
	}
	deniedWrite := filepath.Join(denied, "write")
	if run("/bin/sh", "-c", "printf denied > \"$1\"", "sh", deniedWrite) == nil {
		return errSeatbeltProbe
	}
	if _, statErr := os.Stat(deniedWrite); !os.IsNotExist(statErr) {
		return errSeatbeltProbe
	}
	deniedRead := filepath.Join(denied, "read")
	if os.WriteFile(deniedRead, []byte("secret"), 0o600) != nil || run("/bin/cat", deniedRead) == nil {
		return errSeatbeltProbe
	}

	var listenConfig net.ListenConfig
	tcpListener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return errSeatbeltProbe
	}
	closeTCPListener := closeListenerOnContext(ctx, tcpListener)
	defer closeTCPListener()
	port := strconv.Itoa(tcpListener.Addr().(*net.TCPAddr).Port)
	if run("/usr/bin/nc", "-z", "127.0.0.1", port) == nil {
		return errSeatbeltProbe
	}

	unixPath := filepath.Join(allowed, "listener.sock")
	unixListener, err := listenConfig.Listen(ctx, "unix", unixPath)
	if err != nil {
		return errSeatbeltProbe
	}
	closeUnixListener := closeListenerOnContext(ctx, unixListener)
	defer closeUnixListener()
	accepted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := unixListener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
			accepted <- struct{}{}
		}
	}()
	if run("/usr/bin/nc", "-U", "-z", unixPath) == nil {
		return errSeatbeltProbe
	}
	select {
	case <-accepted:
		return errSeatbeltProbe
	default:
	}

	descendantWrite := filepath.Join(denied, "descendant")
	if run("/bin/sh", "-c", "/bin/sh -c 'printf denied > \"$1\"' sh \"$1\"", "sh", descendantWrite) == nil {
		return errSeatbeltProbe
	}
	if _, statErr := os.Stat(descendantWrite); !os.IsNotExist(statErr) {
		return errSeatbeltProbe
	}
	return nil
}

func closeListenerOnContext(ctx context.Context, listener net.Listener) func() {
	stop := context.AfterFunc(ctx, func() {
		_ = listener.Close()
	})
	return func() {
		stop()
		_ = listener.Close()
	}
}

func boundedSeatbeltContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, seatbeltProbeTimeout)
}

func (a *darwinSeatbeltAdapter) Prepare(_ context.Context, request SpawnRequest) (SpawnSpec, error) {
	if !a.supported() {
		return SpawnSpec{}, errSeatbeltUnsupported
	}
	if request.Binding == nil || request.Binding.Policy() == nil || !validSeatbeltPolicy(request.Binding.Policy(), a.CapabilityGeneration()) || request.Dir != request.Binding.Policy().Spec().CWD {
		return SpawnSpec{}, errSeatbeltRequest
	}
	if a.deps.executable == nil || a.deps.executable() != nil {
		return SpawnSpec{}, errSeatbeltExecutable
	}
	if err := a.revalidate(request.Binding.Policy().Spec().Root); err != nil {
		return SpawnSpec{}, err
	}
	return a.spawn(request.Binding.Policy(), request)
}

func (a *darwinSeatbeltAdapter) spawn(policy *Snapshot, request SpawnRequest) (SpawnSpec, error) {
	profile, definitions, err := renderSeatbeltProfile(policy.Spec())
	if err != nil {
		return SpawnSpec{}, errSeatbeltProfile
	}
	args := make([]string, 0, 4+len(definitions)*2+len(request.Args))
	args = append(args, "-p", profile)
	for _, definition := range definitions {
		args = append(args, "-D", definition)
	}
	args = append(args, "--", request.Executable)
	args = append(args, request.Args...)
	return SpawnSpec{Path: darwinSeatbeltExecutable, Args: args, Dir: request.Dir, Env: append([]string(nil), request.Env...), BindingDigest: bindingDigest(request.Binding)}, nil
}

func (a *darwinSeatbeltAdapter) supported() bool {
	return a != nil && a.deps.goos == "darwin" && (a.deps.goarch == "amd64" || a.deps.goarch == "arm64")
}

func (a *darwinSeatbeltAdapter) revalidate(identity RootIdentity) error {
	if a == nil || a.deps.captureRoot == nil {
		return errSeatbeltRootChanged
	}
	current, err := a.deps.captureRoot(identity.Path)
	if err != nil || current != identity {
		return errSeatbeltRootChanged
	}
	return nil
}

func (a *darwinSeatbeltAdapter) failed(policy *Snapshot, reason ReasonCode) ProbeResult {
	var diagnostic Diagnostic
	if policy != nil {
		diagnostic = policy.Diagnostic()
	}
	return ProbeResult{Diagnostic: diagnostic, ReasonCode: reason}
}

// RevalidateRootIdentity ensures the resolved path and host object are unchanged.
// It deliberately exposes no path or filesystem metadata in its errors.
func RevalidateRootIdentity(identity RootIdentity) error {
	if identity.Path == "" || identity.Device == 0 || identity.Inode == 0 {
		return errSeatbeltRootChanged
	}
	current, err := CaptureRootIdentity(identity.Path)
	if err != nil || current != identity {
		return errSeatbeltRootChanged
	}
	return nil
}

func validSeatbeltPolicy(policy *Snapshot, generation string) bool {
	if policy == nil {
		return false
	}
	spec := policy.Spec()
	return spec.Adapter == AdapterDarwinSeatbelt && spec.State == StateDegraded && spec.Profile == ProfileWorkspaceWrite &&
		spec.Platform == "darwin" && (spec.Architecture == "amd64" || spec.Architecture == "arm64") &&
		spec.CapabilityGeneration == generation && spec.Network.Mode == NetworkDenied && spec.Root.Path == spec.CWD &&
		spec.Root.Device != 0 && spec.Root.Inode != 0
}

func bindingDigest(binding *Binding) string {
	if binding == nil {
		return ""
	}
	return binding.Digest()
}

func renderSeatbeltProfile(spec Spec) (string, []string, error) {
	var rules strings.Builder
	definitions := make([]string, 0, len(spec.ReadRoots)+len(spec.WriteRoots)+len(spec.TempRoots))
	appendRoots := func(prefix string, roots []string, writable bool) {
		for i, root := range roots {
			name := prefix + "_" + itoa(i)
			rules.WriteString("(allow file-read* (subpath (param \"" + name + "\")))\n")
			if writable {
				rules.WriteString("(allow file-write* (subpath (param \"" + name + "\")))\n")
			}
			definitions = append(definitions, name+"="+root)
		}
	}
	appendRoots("READ_ROOT", spec.ReadRoots, false)
	appendRoots("WRITE_ROOT", spec.WriteRoots, true)
	appendRoots("TEMP_ROOT", spec.TempRoots, true)
	for i, root := range spec.DeniedRoots {
		name := "DENY_ROOT_" + itoa(i)
		rules.WriteString("(deny file-write* (subpath (param \"" + name + "\")))\n")
		definitions = append(definitions, name+"="+root)
	}
	if strings.ContainsAny(strings.Join(definitions, ""), "\x00\n\r") {
		return "", nil, errSeatbeltProfile
	}
	return strings.Replace(seatbeltProfileSource, "{{ROOT_RULES}}", rules.String(), 1), definitions, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var bytes [20]byte
	i := len(bytes)
	for value > 0 {
		i--
		bytes[i] = byte('0' + value%10)
		value /= 10
	}
	return string(bytes[i:])
}
