//go:build linux

package containment

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestBubblewrapLinuxIntegration(t *testing.T) {
	if os.Getenv("YHC_BWRAP_UNIX_HELPER") == "1" {
		connection, err := net.Dial("unix", os.Getenv("YHC_BWRAP_UNIX_PATH"))
		if connection != nil {
			_ = connection.Close()
		}
		if err == nil {
			t.Fatal("sandboxed Unix socket connected")
		}
		return
	}
	if err := verifyBubblewrapExecutable(); err != nil {
		if os.Getenv("YHC_REQUIRE_LINUX_BWRAP") == "1" {
			t.Fatalf("required bubblewrap unavailable: %v", err)
		}
		t.Skip("fixed bubblewrap unavailable")
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
	control := filepath.Join(root, "control")
	if err := os.WriteFile(control, []byte("fixed"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	policy := bubblewrapPolicyWithRoot(t, StateDegraded, identity)
	spec := policy.Spec()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec.ReadRoots = append(existingLinuxRuntimeRoots(), root, executable)
	spec.DeniedRoots = []string{control}
	policy, err = NewExecutionPolicySnapshot(&spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewLinuxBubblewrapAdapter()
	result := adapter.Probe(context.Background(), policy)
	if result.ReasonCode != "" {
		if os.Getenv("YHC_REQUIRE_LINUX_BWRAP") == "1" {
			detail := adapter.(*linuxBubblewrapAdapter).probeCapabilities(context.Background(), policy)
			t.Fatalf("required bubblewrap probe failed: %#v; detail: %v", result, detail)
		}
		t.Skipf("bubblewrap enforcement unavailable: %s", result.ReasonCode)
	}
	binding, err := NewBinding(ProcessClassGuest, policy, adapter, result.Proof)
	if err != nil {
		t.Fatal(err)
	}

	inside := filepath.Join(root, "inside")
	if err := runLinuxBubblewrap(t, binding, root, "/bin/sh", []string{"-c", "printf allowed > \"$1\"", "sh", inside}, os.Environ()); err != nil {
		t.Fatalf("declared write failed: %v", err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("declared write missing: %v", err)
	}
	outsideWrite := filepath.Join(outside, "escape")
	if err := runLinuxBubblewrap(t, binding, root, "/bin/sh", []string{"-c", "printf denied > \"$1\"", "sh", outsideWrite}, os.Environ()); err == nil {
		t.Fatal("outside write succeeded")
	}
	if _, err := os.Stat(outsideWrite); !os.IsNotExist(err) {
		t.Fatalf("outside write target exists: %v", err)
	}
	if err := runLinuxBubblewrap(t, binding, root, "/bin/sh", []string{"-c", "printf changed > \"$1\"", "sh", control}, os.Environ()); err == nil {
		t.Fatal("denied control-plane write succeeded")
	}
	if data, err := os.ReadFile(control); err != nil || string(data) != "fixed" {
		t.Fatalf("control-plane bytes = %q, %v", data, err)
	}

	unixPath := filepath.Join(root, "listener.sock")
	listener, err := net.Listen("unix", unixPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	env := append(os.Environ(), "YHC_BWRAP_UNIX_HELPER=1", "YHC_BWRAP_UNIX_PATH="+unixPath)
	if err := runLinuxBubblewrap(t, binding, root, executable, []string{"-test.run=^TestBubblewrapLinuxIntegration$"}, env); err != nil {
		t.Fatalf("Unix socket denial helper failed: %v", err)
	}
}

func runLinuxBubblewrap(t *testing.T, binding *Binding, dir, executable string, args, env []string) error {
	t.Helper()
	spawn, err := binding.Prepare(context.Background(), SpawnRequest{
		Binding: binding, Executable: executable, Args: args, Dir: dir, Env: env,
	})
	if err != nil {
		return err
	}
	return runBubblewrapSpawn(context.Background(), spawn)
}
