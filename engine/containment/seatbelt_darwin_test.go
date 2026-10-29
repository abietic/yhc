package containment

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSeatbeltDarwinSysctlAllowlist(t *testing.T) {
	if os.Getenv("YHC_SEATBELT_SYSCTL_HELPER") == "1" {
		for _, name := range []string{"kern.hostname", "hw.machine"} {
			if _, err := unix.Sysctl(name); err != nil {
				t.Fatalf("required sysctl %s denied", name)
			}
		}
		for _, name := range []string{"hw.model", "kern.boottime"} {
			if _, err := unix.Sysctl(name); !errors.Is(err, unix.EPERM) {
				t.Fatalf("unlisted sysctl %s not denied with EPERM", name)
			}
		}
		return // Never print the returned host metadata.
	}
	if err := verifySeatbeltExecutable(); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	for _, name := range []string{"hw.model", "kern.boottime"} {
		if _, err := unix.Sysctl(name); err != nil {
			t.Fatalf("negative-control sysctl %s unavailable outside Guest", name)
		}
	}
	identity, err := CaptureRootIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := seatbeltPolicyWithRoot(t, StateDegraded, identity)
	adapter := NewDarwinSeatbeltAdapter()
	result := adapter.Probe(context.Background(), policy)
	if result.ReasonCode != "" {
		t.Fatalf("probe = %#v", result)
	}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, result.Proof)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(identity.Path, "sysctl-helper")
	if err := os.WriteFile(helper, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	spawn, err := binding.Prepare(context.Background(), SpawnRequest{
		Binding: binding, Executable: helper, Dir: identity.Path,
		Args: []string{"-test.run=^TestSeatbeltDarwinSysctlAllowlist$"},
		// Go's cover-mode test main creates temporary coverage data at exit.
		// Keep that housekeeping inside the declared root, not ambient /tmp.
		Env: []string{"YHC_SEATBELT_SYSCTL_HELPER=1", "TMPDIR=" + identity.Path},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A copied Go test binary may cold-start slowly while other packages build.
	// Keep the production prepare path, not the capability probe's 5s budget.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, spawn.Path, spawn.Args...)
	command.Dir, command.Env = spawn.Dir, spawn.Env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Guest sysctl allowlist subprocess failed: %v (context: %v): %s", err, ctx.Err(), output)
	}
}
