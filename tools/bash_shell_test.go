package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/containment"
)

type p51ShellAdapter struct {
	request        containment.SpawnRequest
	preparedDigest string
	root           containment.RootIdentity
	rootInfo       os.FileInfo
}

func (a *p51ShellAdapter) captureRootIdentity(path string) (containment.RootIdentity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return containment.RootIdentity{}, fmt.Errorf("resolve test root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return containment.RootIdentity{}, fmt.Errorf("stat test root: %w", err)
	}
	identity := a.root
	identity.Path = resolved
	if resolved != a.root.Path || !os.SameFile(a.rootInfo, info) {
		identity.Inode++
	}
	return identity, nil
}

func (*p51ShellAdapter) Family() containment.AdapterFamily { return containment.AdapterDarwinSeatbelt }
func (*p51ShellAdapter) CapabilityGeneration() string      { return "p51" }
func (*p51ShellAdapter) Probe(context.Context, *containment.Snapshot) containment.ProbeResult {
	return containment.ProbeResult{}
}

func (a *p51ShellAdapter) Prepare(_ context.Context, request containment.SpawnRequest) (containment.SpawnSpec, error) {
	a.request = request
	digest := a.preparedDigest
	if digest == "" {
		digest = request.Binding.Digest()
	}
	return containment.SpawnSpec{Path: request.Executable, Args: request.Args, Dir: request.Dir, Env: request.Env, BindingDigest: digest}, nil
}

func p51GuestBinding(t *testing.T) (*containment.Binding, *p51ShellAdapter) {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(cwd)
	if err != nil {
		t.Fatal(err)
	}
	root := containment.RootIdentity{Path: cwd, Device: 1, Inode: 1}
	policy, err := containment.NewExecutionPolicySnapshot(&containment.Spec{
		Version: containment.PolicyVersion, Profile: containment.ProfileWorkspaceWrite, State: containment.StateDegraded,
		SelectionSource: "compatibility-default", Adapter: containment.AdapterDarwinSeatbelt, Platform: "darwin", Architecture: "arm64", CapabilityGeneration: "p51", CWD: cwd,
		WriteRoots: []string{cwd}, Network: containment.NetworkPolicy{Mode: containment.NetworkDenied, ProjectionID: "n"}, Environment: containment.EnvironmentPolicy{ProjectionID: "e"}, Credentials: containment.CredentialPolicy{Mode: containment.CredentialAmbientEnvironment, ProjectionID: "e"},
		Root: root, Descendants: containment.DescendantPolicy{Mode: containment.DescendantCleanupRequired}, Resources: containment.ResourceLimits{WallTimeMillis: 100, OutputBytes: 128}, Entrypoint: containment.EntrypointTUI, Lineage: containment.Lineage{RootID: "p51"},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &p51ShellAdapter{root: root, rootInfo: rootInfo}
	binding, err := containment.NewBinding(containment.ProcessClassGuest, policy, adapter, containment.AdapterProof{PolicyDigest: policy.Digest(), CapabilityGeneration: "p51", Enforced: containment.AxisFilesystemRead | containment.AxisFilesystemWrite | containment.AxisNetworkDenied | containment.AxisRootIdentity | containment.AxisDescendantConfinement})
	if err != nil {
		t.Fatal(err)
	}
	return binding, adapter
}

func p51GuestShellManager(t *testing.T, binding *containment.Binding, adapter *p51ShellAdapter) *ShellManager {
	t.Helper()
	mgr, err := NewShellManagerWithGuestBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	mgr.captureRootIdentityForTest = adapter.captureRootIdentity
	return mgr
}

func TestP511GuestBindingPreparesExactLaunchAndBoundsOutput(t *testing.T) {
	binding, adapter := p51GuestBinding(t)
	mgr := p51GuestShellManager(t, binding, adapter)
	t.Cleanup(func() { _ = mgr.KillAll() })
	starts := 0
	mgr.start = func(cmd *exec.Cmd) error { starts++; return cmd.Start() }
	result, err := mgr.ExecuteAt(context.Background(), "guest", binding.Policy().Spec().CWD, "printf '%0200d' 0", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || adapter.request.Executable != "/bin/bash" || strings.Join(adapter.request.Args, " ") != "--noediting --noprofile --norc" || adapter.request.Dir != binding.Policy().Spec().CWD || adapter.request.Binding != binding || !equalStringSlices(adapter.request.Env, os.Environ()) {
		t.Fatalf("guest launch = %#v starts=%d", adapter.request, starts)
	}
	if len(result.Stdout) > 160 || !result.Truncated {
		t.Fatalf("unbounded guest output: %d %#v", len(result.Stdout), result)
	}
	recovered, err := mgr.Execute(context.Background(), "guest", "echo recovered", time.Second)
	if err != nil || recovered.Stdout != "recovered" {
		t.Fatalf("persistent recovery = %#v, %v", recovered, err)
	}
}

func TestP511GuestOperationalAliasSpawnsAtCanonicalBindingRoot(t *testing.T) {
	binding, adapter := p51GuestBinding(t)
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(binding.Policy().Spec().CWD, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mgr := p51GuestShellManager(t, binding, adapter)
	t.Cleanup(func() { _ = mgr.KillAll() })
	result, err := mgr.ExecuteAt(context.Background(), "alias", alias, "echo canonical", time.Second)
	if err != nil || result.Stdout != "canonical" {
		t.Fatalf("operational alias execution = %#v, %v", result, err)
	}
	if got, want := adapter.request.Dir, binding.Policy().Spec().CWD; got != want || result.CWD != want {
		t.Fatalf("spawn/result CWD = %q/%q, want canonical binding root %q", got, result.CWD, want)
	}
}

func TestP511AmbientGuestBindingPreservesCompatibility(t *testing.T) {
	cwd := t.TempDir()
	policy, err := containment.NewAmbientHostSnapshot(cwd, containment.EntrypointHeadless, containment.SelectionCLI)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := containment.NewBinding(containment.ProcessClassGuest, policy, containment.NewAmbientHostAdapter(), containment.AdapterProof{})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := NewShellManagerWithGuestBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.KillAll() })
	starts := 0
	mgr.start = func(cmd *exec.Cmd) error { starts++; return cmd.Start() }
	result, err := mgr.ExecuteAt(context.Background(), "ambient", cwd, "echo ambient", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || result.Stdout != "ambient" || mgr.GuestExecutionProof() != (containment.ExecutionProof{}) {
		t.Fatalf("ambient execution = %#v starts=%d proof=%#v", result, starts, mgr.GuestExecutionProof())
	}
}

func TestP511GuestBindingFailuresMakeZeroStartAttempts(t *testing.T) {
	t.Run("prepare digest mismatch", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		adapter.preparedDigest = "mismatch"
		mgr := p51GuestShellManager(t, binding, adapter)
		starts := 0
		mgr.start = func(*exec.Cmd) error { starts++; return nil }
		if _, err := mgr.ExecuteAt(context.Background(), "guest", binding.Policy().Spec().CWD, "true", time.Second); err == nil {
			t.Fatal("digest mismatch reached execution")
		}
		if starts != 0 {
			t.Fatalf("start attempts = %d, want zero", starts)
		}
	})

	t.Run("root replacement", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		root := binding.Policy().Spec().CWD
		moved := root + "-moved"
		if err := os.Rename(root, moved); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(moved) })
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		mgr := p51GuestShellManager(t, binding, adapter)
		starts := 0
		mgr.start = func(*exec.Cmd) error { starts++; return nil }
		if _, err := mgr.ExecuteAt(context.Background(), "guest", root, "true", time.Second); err == nil {
			t.Fatal("replaced root reached execution")
		}
		if starts != 0 {
			t.Fatalf("start attempts = %d, want zero", starts)
		}
	})

	t.Run("root replacement immediately before start", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		root := binding.Policy().Spec().CWD
		moved := root + "-moved"
		mgr := p51GuestShellManager(t, binding, adapter)
		mgr.beforeGuestStartForTest = func() {
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { _ = os.RemoveAll(moved) })
		starts := 0
		mgr.start = func(*exec.Cmd) error { starts++; return nil }
		if _, err := mgr.ExecuteAt(context.Background(), "guest", root, "true", time.Second); err == nil {
			t.Fatal("final root replacement reached execution")
		}
		if starts != 0 {
			t.Fatalf("start attempts = %d, want zero", starts)
		}
	})

	t.Run("unavailable binding", func(t *testing.T) {
		available, adapter := p51GuestBinding(t)
		spec := available.Policy().Spec()
		spec.State = containment.StateUnavailable
		policy, err := containment.NewExecutionPolicySnapshot(&spec)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := containment.NewUnavailableBinding(containment.ProcessClassGuest, policy, adapter, containment.ReasonProbeFailed)
		if err != nil {
			t.Fatal(err)
		}
		mgr := p51GuestShellManager(t, binding, adapter)
		starts := 0
		mgr.start = func(*exec.Cmd) error { starts++; return nil }
		if _, err := mgr.ExecuteAt(context.Background(), "guest", spec.CWD, "true", time.Second); err == nil {
			t.Fatal("unavailable binding reached execution")
		}
		if starts != 0 {
			t.Fatalf("start attempts = %d, want zero", starts)
		}
	})
}

func TestP511GuestCommandLifecycleIsBoundedAndCompatible(t *testing.T) {
	t.Run("marker split across reader buffer", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		mgr := p51GuestShellManager(t, binding, adapter)
		t.Cleanup(func() { _ = mgr.KillAll() })
		result, err := mgr.ExecuteAt(context.Background(), "guest", binding.Policy().Spec().CWD, "printf '%065520d' 0", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if result.TimedOut || !result.Truncated {
			t.Fatalf("split marker result = %#v", result)
		}
	})

	t.Run("stderr stays merged and bounded", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		mgr := p51GuestShellManager(t, binding, adapter)
		t.Cleanup(func() { _ = mgr.KillAll() })
		command := `{ printf '%0200d' 0 >&2; }`
		result, err := mgr.ExecuteAt(context.Background(), "guest", binding.Policy().Spec().CWD, command, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if result.Stderr != "" || !result.Truncated || len(result.Stdout) > 160 {
			t.Fatalf("stderr compatibility = %#v", result)
		}
	})

	t.Run("cwd is captured without an unbounded round trip", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		root := binding.Policy().Spec().CWD
		child := filepath.Join(root, "child")
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		mgr := p51GuestShellManager(t, binding, adapter)
		t.Cleanup(func() { _ = mgr.KillAll() })
		result, err := mgr.ExecuteAt(context.Background(), "guest", root, "cd child", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if result.CWD != child {
			t.Fatalf("cwd = %q, want %q", result.CWD, child)
		}
		next, err := mgr.Execute(context.Background(), "guest", `[ "${PWD##*/}" = child ] && echo preserved`, time.Second)
		if err != nil || next.Stdout != "preserved" || next.CWD != child {
			t.Fatalf("persistent cwd = %#v, %v", next, err)
		}
	})

	t.Run("policy wall time overrides a broader caller timeout", func(t *testing.T) {
		binding, adapter := p51GuestBinding(t)
		mgr := p51GuestShellManager(t, binding, adapter)
		t.Cleanup(func() { _ = mgr.KillAll() })
		started := time.Now()
		result, err := mgr.ExecuteAt(context.Background(), "guest", binding.Policy().Spec().CWD, "sleep 5", 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if !result.TimedOut || time.Since(started) > 2*time.Second {
			t.Fatalf("hard wall time result=%#v elapsed=%s", result, time.Since(started))
		}
	})
}

func TestShellManagerTimeoutRetiresShellAndRecovers(t *testing.T) {
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })

	original, err := mgr.GetOrCreate("")
	if err != nil {
		t.Fatal(err)
		return
	}

	start := time.Now()
	result, err := mgr.Execute(context.Background(), "", "sleep 10", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("timed command failed: %v", err)
		return
	}
	if !result.TimedOut || result.Canceled {
		t.Fatalf("unexpected cancellation flags: timed_out=%v canceled=%v", result.TimedOut, result.Canceled)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout retirement took too long: %v", time.Since(start))
	}

	commands := []string{"true", "echo recovered", "pwd", "sleep 0.01"}
	for _, command := range commands {
		assertShellCommandCompletes(t, mgr, command)
	}

	replacement, err := mgr.GetOrCreate("")
	if err != nil {
		t.Fatal(err)
		return
	}
	if replacement == original {
		t.Fatal("timed-out shell was reused")
	}
}

func TestShellManagerContextCancellationRetiresShell(t *testing.T) {
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := mgr.Execute(ctx, "", "sleep 10", 10*time.Second)
	if err != nil {
		t.Fatalf("canceled command failed: %v", err)
		return
	}
	if result.TimedOut || !result.Canceled {
		t.Fatalf("unexpected cancellation flags: timed_out=%v canceled=%v", result.TimedOut, result.Canceled)
	}
	if !strings.Contains(result.Stdout, "canceled") {
		t.Fatalf("cancellation result was mislabeled: %q", result.Stdout)
	}

	assertShellCommandCompletes(t, mgr, "echo after-cancel")
}

func TestShellManagerRepeatedTimeoutRecovery(t *testing.T) {
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })

	for i := 0; i < 5; i++ {
		result, err := mgr.Execute(context.Background(), "", "sleep 10", 30*time.Millisecond)
		if err != nil {
			t.Fatalf("cycle %d timeout failed: %v", i, err)
			return
		}
		if !result.TimedOut {
			t.Fatalf("cycle %d did not time out", i)
		}
		assertShellCommandCompletes(t, mgr, "echo cycle-recovered")
	}
}

func TestShellManagerWaitingCallerUsesReplacementShell(t *testing.T) {
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })

	timedOut := make(chan error, 1)
	go func() {
		result, err := mgr.Execute(context.Background(), "", "sleep 10", 100*time.Millisecond)
		if err == nil && !result.TimedOut {
			err = context.DeadlineExceeded
		}
		timedOut <- err
	}()

	time.Sleep(20 * time.Millisecond)
	recovered := make(chan error, 1)
	go func() {
		result, err := mgr.Execute(context.Background(), "", "echo concurrent-recovery", time.Second)
		if err == nil && !strings.Contains(result.Stdout, "concurrent-recovery") {
			err = context.Canceled
		}
		recovered <- err
	}()

	if err := <-timedOut; err != nil {
		t.Fatalf("timed command failed: %v", err)
		return
	}
	select {
	case err := <-recovered:
		if err != nil {
			t.Fatalf("waiting caller did not recover: %v", err)
			return
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting caller hung on retired shell")
	}
}

func TestShellManagerSuccessfulCommandsReuseShell(t *testing.T) {
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })

	before, err := mgr.GetOrCreate("")
	if err != nil {
		t.Fatal(err)
		return
	}
	assertShellCommandCompletes(t, mgr, "export EINO_BASH_REUSE=preserved")
	result, err := mgr.Execute(context.Background(), "", "echo \"$EINO_BASH_REUSE\"", time.Second)
	if err != nil {
		t.Fatal(err)
		return
	}
	if result.Stdout != "preserved" {
		t.Fatalf("persistent state was not preserved: %q", result.Stdout)
	}
	after, err := mgr.GetOrCreate("")
	if err != nil {
		t.Fatal(err)
		return
	}
	if after != before {
		t.Fatal("successful command replaced the persistent shell")
	}
}

func TestP420ShellPinsPolicyBeforeSpawnAndRejectsLateReplacement(t *testing.T) {
	cwd := t.TempDir()
	policy := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointHeadless)
	mgr := NewShellManagerWithExecutionPolicy(policy)
	t.Cleanup(func() { _ = mgr.KillAll() })
	shell, err := mgr.GetOrCreateAt("p42", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if shell.ExecutionPolicyDigest() != policy.Digest() || mgr.ExecutionPolicyDigest() != policy.Digest() {
		t.Fatalf("shell policy=%q manager=%q want=%q", shell.ExecutionPolicyDigest(), mgr.ExecutionPolicyDigest(), policy.Digest())
	}
	replacement := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointPlain)
	if err := mgr.BindExecutionPolicy(replacement); err == nil {
		t.Fatal("late shell execution policy replacement succeeded")
	}
	ctx := containment.WithSnapshot(context.Background(), replacement)
	if _, err := mgr.ExecuteAt(ctx, "p42", cwd, "true", time.Second); err == nil {
		t.Fatal("mismatched dispatch policy reached the pinned shell")
	}
}

func TestP420ShellFirstSpawnCannotRacePolicyBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("persistent shell assertions require bash")
	}
	cwd := t.TempDir()
	first := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointHeadless)
	second := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointPlain)
	mgr := NewShellManager()
	t.Cleanup(func() { _ = mgr.KillAll() })

	reachedCommitGap := make(chan struct{})
	releaseCommitGap := make(chan struct{})
	mgr.beforeFirstPolicyCommitForTest = func() {
		close(reachedCommitGap)
		<-releaseCommitGap
	}

	firstResult := make(chan error, 1)
	go func() {
		_, err := mgr.getOrCreateAt("first", cwd, first)
		firstResult <- err
	}()
	select {
	case <-reachedCommitGap:
	case <-time.After(time.Second):
		t.Fatal("first spawn did not reach the policy commit barrier")
	}
	if err := mgr.BindExecutionPolicy(second); err != nil {
		t.Fatalf("concurrent root bind failed: %v", err)
	}
	close(releaseCommitGap)
	select {
	case err := <-firstResult:
		if err == nil || !strings.Contains(err.Error(), "execution policy mismatch") {
			t.Fatalf("racing first spawn error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("racing first spawn did not return")
	}

	mgr.mu.RLock()
	spawned := len(mgr.shells)
	mgr.mu.RUnlock()
	if spawned != 0 {
		t.Fatalf("mismatched first policy spawned %d shells", spawned)
	}
	mgr.beforeFirstPolicyCommitForTest = nil
	shell, err := mgr.getOrCreateAt("second", cwd, second)
	if err != nil {
		t.Fatal(err)
	}
	if shell.ExecutionPolicyDigest() != second.Digest() ||
		mgr.ExecutionPolicyDigest() != second.Digest() {
		t.Fatalf(
			"winning policy shell=%q manager=%q want=%q",
			shell.ExecutionPolicyDigest(),
			mgr.ExecutionPolicyDigest(),
			second.Digest(),
		)
	}
}

func assertShellCommandCompletes(t *testing.T, mgr *ShellManager, command string) {
	t.Helper()
	start := time.Now()
	result, err := mgr.Execute(context.Background(), "", command, time.Second)
	if err != nil {
		t.Fatalf("command %q failed: %v", command, err)
		return
	}
	if result.TimedOut || result.Canceled || result.ExitCode != 0 {
		t.Fatalf("command %q did not complete: %+v", command, result)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("command %q completed too slowly: %v", command, time.Since(start))
	}
}
