package containment

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSeatbeltAdapterRejectsUnsupportedPlatformWithoutIdentity(t *testing.T) {
	adapter := newDarwinSeatbeltAdapter(seatbeltDependencies{goos: "linux", goarch: "amd64"})
	policy := seatbeltPolicy(t, StateUnavailable)
	result := adapter.Probe(context.Background(), policy)
	if result.ReasonCode != ReasonUnsupportedPlatform {
		t.Fatalf("reason = %q, want unsupported platform", result.ReasonCode)
	}
	if proof := result.Proof; proof != (AdapterProof{}) {
		t.Fatalf("unsupported probe proof = %#v", proof)
	}
	if runtimeGOOS() != "darwin" && runtimeGOOS() != "linux" {
		if _, err := CaptureRootIdentity(t.TempDir()); err == nil {
			t.Fatal("non-Darwin root identity was fabricated")
		}
	}
}

func TestSeatbeltProbeListenerClosesOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closeListener := closeListenerOnContext(ctx, listener)
	t.Cleanup(closeListener)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if connection != nil {
			_ = connection.Close()
		}
		acceptErr <- err
	}()

	cancel()
	select {
	case err := <-acceptErr:
		if err == nil {
			t.Fatal("listener accepted a connection after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("listener remained open after cancellation")
	}
}

func TestSeatbeltProfileUsesParametersAndDeniesNetwork(t *testing.T) {
	profile, definitions, err := renderSeatbeltProfile(Spec{
		ReadRoots:   []string{"/read"},
		WriteRoots:  []string{"/write"},
		TempRoots:   []string{"/tmp-root"},
		DeniedRoots: []string{"/write/control"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/read", "/write", "/tmp-root", "network*", "system-socket", "unix-socket"} {
		if contains(profile, forbidden) {
			t.Fatalf("profile leaked root or allowed forbidden operation %q: %s", forbidden, profile)
		}
	}
	if len(definitions) != 4 || definitions[0] != "READ_ROOT_0=/read" || definitions[2] != "TEMP_ROOT_0=/tmp-root" || definitions[3] != "DENY_ROOT_0=/write/control" {
		t.Fatalf("definitions = %#v", definitions)
	}
	if !strings.Contains(profile, `(deny file-write* (subpath (param "DENY_ROOT_0")))`) {
		t.Fatalf("profile omitted control-plane deny: %s", profile)
	}
}

func TestSeatbeltProbeBindsOnlyAdapterOwnedAxes(t *testing.T) {
	root := t.TempDir()
	identity := RootIdentity{Path: root, Device: 1, Inode: 2}
	adapter := newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: "darwin", goarch: "arm64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
		run:         func(context.Context, SpawnSpec) error { return nil },
		probe:       func(context.Context, *Snapshot) error { return nil },
	})
	policy := seatbeltPolicyWithRoot(t, StateDegraded, identity)
	result := adapter.Probe(context.Background(), policy)
	if result.ReasonCode != "" || result.Proof.PolicyDigest != policy.Digest() || result.Proof.CapabilityGeneration != darwinSeatbeltGeneration || result.Proof.Enforced != adapterAllowedAxes {
		t.Fatalf("probe = %#v", result)
	}
}

func TestSeatbeltProbeReasonMappingAndRedaction(t *testing.T) {
	root := t.TempDir()
	identity := RootIdentity{Path: root, Device: 1, Inode: 2}
	policy := seatbeltPolicyWithRoot(t, StateDegraded, identity)
	missing := newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: "darwin", goarch: "arm64", executable: func() error { return errors.New("binary detail") },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil }, run: func(context.Context, SpawnSpec) error { return nil },
	})
	if got := missing.Probe(context.Background(), policy).ReasonCode; got != ReasonExecutableMissing {
		t.Fatalf("missing reason = %q", got)
	}
	rootChanged := newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: "darwin", goarch: "arm64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return RootIdentity{Path: root, Device: 9, Inode: 9}, nil }, run: func(context.Context, SpawnSpec) error { return nil },
	})
	result := rootChanged.Probe(context.Background(), policy)
	if result.ReasonCode != ReasonRootChanged {
		t.Fatalf("root reason = %q", result.ReasonCode)
	}
	if text := fmt.Sprintf("%+v", result); contains(text, root) || contains(text, "binary detail") || contains(text, "profile") {
		t.Fatalf("probe result leaked sensitive detail: %s", text)
	}
	if got := rootChanged.Probe(context.Background(), nil).ReasonCode; got != ReasonProfileInvalid {
		t.Fatalf("profile reason = %q", got)
	}
	failed := newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: "darwin", goarch: "arm64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
		probe: func(ctx context.Context, _ *Snapshot) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("probe context had no deadline")
			}
			return errors.New("raw subprocess output")
		},
	})
	if got := failed.Probe(context.Background(), policy).ReasonCode; got != ReasonProbeFailed {
		t.Fatalf("probe reason = %q", got)
	}
}

func TestSeatbeltPreparePreservesRequestBoundary(t *testing.T) {
	root := t.TempDir()
	identity := RootIdentity{Path: root, Device: 1, Inode: 2}
	adapter := newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: "darwin", goarch: "arm64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return identity, nil },
	})
	policy := seatbeltPolicyWithRoot(t, StateDegraded, identity)
	proof := AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: adapter.CapabilityGeneration(), Enforced: adapterAllowedAxes}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, proof)
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"Z=last", "A=first", "A=duplicate"}
	request := SpawnRequest{Binding: binding, Executable: "/usr/bin/true", Args: []string{"-v"}, Dir: root, Env: env}
	spawn, err := binding.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if spawn.Path != darwinSeatbeltExecutable || spawn.Dir != root || spawn.BindingDigest != binding.Digest() {
		t.Fatalf("spawn identity = %#v", spawn)
	}
	if len(spawn.Args) < 5 || spawn.Args[0] != "-p" || spawn.Args[len(spawn.Args)-3] != "--" || spawn.Args[len(spawn.Args)-2] != request.Executable || spawn.Args[len(spawn.Args)-1] != "-v" {
		t.Fatalf("args = %#v", spawn.Args)
	}
	if len(spawn.Env) != len(env) {
		t.Fatalf("env = %#v", spawn.Env)
	}
	for i := range env {
		if spawn.Env[i] != env[i] {
			t.Fatalf("env = %#v", spawn.Env)
		}
	}
	spawn.Env[0] = "changed"
	if env[0] != "Z=last" {
		t.Fatal("prepare retained environment alias")
	}
}

func TestSeatbeltPrepareRejectsRootChangeWithoutLeak(t *testing.T) {
	root := t.TempDir()
	identity := RootIdentity{Path: root, Device: 1, Inode: 2}
	adapter := newDarwinSeatbeltAdapter(seatbeltDependencies{
		goos: "darwin", goarch: "arm64", executable: func() error { return nil },
		captureRoot: func(string) (RootIdentity, error) { return RootIdentity{Path: root, Device: 3, Inode: 4}, nil },
	})
	policy := seatbeltPolicyWithRoot(t, StateDegraded, identity)
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: adapter.CapabilityGeneration(), Enforced: adapterAllowedAxes})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Prepare(context.Background(), SpawnRequest{Binding: binding, Executable: "/usr/bin/true", Dir: root, Env: []string{"SECRET=value"}})
	if err == nil || !contains(err.Error(), "root changed") || contains(err.Error(), root) || contains(err.Error(), "SECRET") {
		t.Fatalf("root change error = %v", err)
	}
	if !errors.Is(err, errSeatbeltRootChanged) {
		t.Fatalf("error = %v", err)
	}
}

func seatbeltPolicy(t *testing.T, state State) *Snapshot {
	t.Helper()
	root := t.TempDir()
	return seatbeltPolicyWithRoot(t, state, RootIdentity{Path: root, Device: 1, Inode: 2})
}

func seatbeltPolicyWithRoot(t *testing.T, state State, root RootIdentity) *Snapshot {
	t.Helper()
	spec := darwinWorkspaceSpec(t, root.Path, state)
	spec.Root = root
	spec.CapabilityGeneration = darwinSeatbeltGeneration
	policy, err := NewExecutionPolicySnapshot(&spec)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestSeatbeltDarwinIntegration(t *testing.T) {
	if runtimeGOOS() != "darwin" {
		t.Skip("Darwin only")
	}
	info, err := os.Lstat(darwinSeatbeltExecutable)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Skip("sandbox-exec unavailable")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	policy := seatbeltPolicyWithRoot(t, StateDegraded, identity)
	root = policy.Spec().CWD
	adapter := NewDarwinSeatbeltAdapter()
	result := adapter.Probe(context.Background(), policy)
	if result.ReasonCode != "" {
		t.Fatalf("probe = %#v", result)
	}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, result.Proof)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside")
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", "echo allowed > \"$1\"", "sh", inside}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("declared write denied: %v", err)
	}
	if err := runDarwinSeatbelt(t, binding, root, "/bin/cat", []string{inside}); err != nil {
		t.Fatalf("declared read denied: %v", err)
	}

	writeOutside := filepath.Join(outside, "write-outside")
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", "echo denied > \"$1\"", "sh", writeOutside}); err == nil {
		t.Fatal("write outside writable roots succeeded")
	}
	if _, err := os.Stat(writeOutside); !os.IsNotExist(err) {
		t.Fatalf("outside target exists or stat failed: %v", err)
	}

	readOutside := filepath.Join(outside, "read-outside")
	if err := os.WriteFile(readOutside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDarwinSeatbelt(t, binding, root, "/bin/cat", []string{readOutside}); err == nil {
		t.Fatal("read outside read roots succeeded")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := runDarwinSeatbelt(t, binding, root, "/usr/bin/nc", []string{"-z", "127.0.0.1", port}); err == nil {
		t.Fatal("TCP connection to live listener succeeded")
	}

	udpListener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpListener.Close()
	udpPort := strconv.Itoa(udpListener.LocalAddr().(*net.UDPAddr).Port)
	if err := runDarwinSeatbelt(t, binding, root, "/usr/bin/nc", []string{"-u", "-z", "-w", "1", "127.0.0.1", udpPort}); err == nil {
		t.Fatal("UDP operation succeeded")
	}

	unixDir, err := os.MkdirTemp("/tmp", "eino-p51-unix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(unixDir) })
	unixPath := filepath.Join(unixDir, "listener.sock")
	unixListener, err := net.Listen("unix", unixPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unixListener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := unixListener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
			accepted <- struct{}{}
		}
	}()
	if err := runDarwinSeatbelt(t, binding, root, "/usr/bin/nc", []string{"-U", "-z", unixPath}); err == nil {
		t.Fatal("Unix-socket connection succeeded")
	}
	select {
	case <-accepted:
		t.Fatal("Unix-socket listener accepted a sandboxed connection")
	default:
	}

	symlinkEscape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, symlinkEscape); err != nil {
		t.Fatal(err)
	}
	symlinkOutside := filepath.Join(outside, "symlink-outside")
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", "echo denied > escape/symlink-outside"}); err == nil {
		t.Fatal("symlink escape write succeeded")
	}
	if _, err := os.Stat(symlinkOutside); !os.IsNotExist(err) {
		t.Fatalf("symlink escape target exists or stat failed: %v", err)
	}

	traversalOutside := filepath.Join(outside, "traversal-outside")
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", "echo denied > ../outside/traversal-outside"}); err == nil {
		t.Fatal("parent traversal write succeeded")
	}
	if _, err := os.Stat(traversalOutside); !os.IsNotExist(err) {
		t.Fatalf("parent traversal target exists or stat failed: %v", err)
	}

	substitutionOutside := filepath.Join(outside, "substitution-outside")
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", `target="$(printf ../outside/substitution-outside)"; echo denied > "$target"`}); err == nil {
		t.Fatal("command-substitution write succeeded")
	}
	if _, err := os.Stat(substitutionOutside); !os.IsNotExist(err) {
		t.Fatalf("command-substitution target exists or stat failed: %v", err)
	}

	childOutside := filepath.Join(outside, "child-outside")
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", "/bin/sh -c 'echo denied > \"$1\"' sh \"$1\"", "sh", childOutside}); err == nil {
		t.Fatal("nested descendant write outside roots succeeded")
	}
	if _, err := os.Stat(childOutside); !os.IsNotExist(err) {
		t.Fatalf("nested descendant outside target exists or stat failed: %v", err)
	}
	if err := runDarwinSeatbelt(t, binding, root, "/bin/sh", []string{"-c", "(echo denied > \"$1\") & wait", "sh", childOutside}); err != nil {
		t.Fatalf("background descendant supervisor did not finish: %v", err)
	}
	if _, err := os.Stat(childOutside); !os.IsNotExist(err) {
		t.Fatalf("background descendant outside target exists or stat failed: %v", err)
	}
}

func TestCaptureRootIdentityRejectsFiles(t *testing.T) {
	if runtimeGOOS() != "darwin" {
		t.Skip("Darwin only")
	}
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureRootIdentity(path); err == nil {
		t.Fatal("file root identity accepted")
	}
}

func runDarwinSeatbelt(t *testing.T, binding *Binding, dir, executable string, args []string) error {
	t.Helper()
	spawn, err := binding.Prepare(context.Background(), SpawnRequest{Binding: binding, Executable: executable, Args: args, Dir: dir})
	if err != nil {
		return err
	}
	return runSeatbeltSpawn(context.Background(), spawn)
}
