//go:build darwin || linux

package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/containment"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const stdioTransportHelperEnv = "YHC_STDIO_TRANSPORT_HELPER"

func TestStdioProcessTransportNormalExitAndExactProcessInput(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	cwd := t.TempDir()
	policy := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointACP)
	transport, err := newStdioProcessTransport(ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestStdioTransportHelperProcess$", "--", "exit", record, "argv-sentinel"},
		CWD:     cwd,
		Env: map[string]string{
			"YHC_STDIO_ENV_SENTINEL": "overlay-sentinel",
			stdioTransportHelperEnv:  "1",
		},
		Type:            "stdio",
		ExecutionPolicy: policy,
	})
	if err != nil {
		t.Fatalf("newStdioProcessTransport() error = %v", err)
	}
	if transport.executionPolicyDigest() != policy.Digest() {
		t.Fatalf("transport policy = %q, want %q", transport.executionPolicyDigest(), policy.Digest())
	}

	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	content, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read helper record: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 3 {
		t.Fatalf("helper record lines = %d, want 3", len(lines))
	}
	wantCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("resolve test cwd: %v", err)
	}
	if lines[0] != wantCWD {
		t.Fatalf("child cwd = %q, want %q", lines[0], wantCWD)
	}
	if lines[1] != "overlay-sentinel" {
		t.Fatalf("child env overlay = %q", lines[1])
	}
	if lines[2] != "exit,"+record+",argv-sentinel" {
		t.Fatalf("child argv tail = %q", lines[2])
	}
}

func TestStdioProcessTransportCloseKillsStubbornProcessGroup(t *testing.T) {
	pidsFile := filepath.Join(t.TempDir(), "pids")
	transport := newTestStdioTransport(t, "stubborn", pidsFile)
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := waitForFile(pidsFile); err != nil {
		t.Fatal(err)
	}
	pids := readPIDs(t, pidsFile)
	if len(pids) != 2 {
		t.Fatalf("recorded pids = %v, want parent and descendant", pids)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for _, pid := range pids {
		if err := waitForProcessGone(pid); err != nil {
			t.Error(err)
		}
	}
}

func TestStdioProcessTransportCancelledBeforeLaunch(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	transport := newTestStdioTransport(t, "exit", record)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := transport.Connect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper launched after cancellation: stat error = %v", err)
	}
}

func TestMCPClientConnectFailureClosesStdioProcessTree(t *testing.T) {
	pidsFile := filepath.Join(t.TempDir(), "pids")
	client := NewMCPClient(ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestStdioTransportHelperProcess$", "--", "stubborn", pidsFile},
		Env:     map[string]string{stdioTransportHelperEnv: "1"},
		Type:    "stdio",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := client.Connect(ctx)
	if err == nil {
		t.Fatal("Connect() unexpectedly succeeded")
	}
	if err.Error() != "mcp: stdio connection failed" {
		t.Fatalf("Connect() error = %q, want sanitized stdio failure", err)
	}
	if err := waitForFile(pidsFile); err != nil {
		t.Fatal(err)
	}
	for _, pid := range readPIDs(t, pidsFile) {
		if err := waitForProcessGone(pid); err != nil {
			t.Error(err)
		}
	}
}

func TestMCPClientUnexpectedExitClosesStdioProcessTree(t *testing.T) {
	pidsFile := filepath.Join(t.TempDir(), "pids")
	exitFile := filepath.Join(t.TempDir(), "exit")
	client := NewMCPClient(ServerConfig{
		Command: os.Args[0],
		Args: []string{
			"-test.run=^TestStdioTransportHelperProcess$",
			"--",
			"mcp-exit-tree",
			pidsFile,
			exitFile,
		},
		Env:  map[string]string{stdioTransportHelperEnv: "1"},
		Type: "stdio",
	})
	t.Cleanup(func() { _ = client.Disconnect() })
	if err := client.Connect(t.Context()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := client.ListTools(t.Context()); err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if err := waitForFile(pidsFile); err != nil {
		t.Fatal(err)
	}
	pids := readPIDs(t, pidsFile)
	if len(pids) != 2 {
		t.Fatalf("recorded pids = %v, want parent and descendant", pids)
	}
	if err := os.WriteFile(exitFile, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		if err := waitForProcessGone(pid); err != nil {
			t.Error(err)
		}
	}
}

func TestStdioProcessTransportStartFailureDoesNotExposeCommand(t *testing.T) {
	const invalidCommand = "/definitely/not/a-stdio-mcp-command"
	transport, err := newStdioProcessTransport(ServerConfig{Command: invalidCommand, Type: "stdio"})
	if err != nil {
		t.Fatalf("newStdioProcessTransport() error = %v", err)
	}
	_, err = transport.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect() succeeded for an invalid command")
	}
	if strings.Contains(err.Error(), invalidCommand) {
		t.Fatalf("start error leaked command: %v", err)
	}
}

func TestMCPClientBuildTransportUsesOwnedStdioTransport(t *testing.T) {
	client := NewMCPClient(ServerConfig{Command: os.Args[0], Type: "stdio"})
	transport, err := client.buildTransport()
	if err != nil {
		t.Fatalf("buildTransport() error = %v", err)
	}
	if _, ok := transport.(*stdioProcessTransport); !ok {
		t.Fatalf("buildTransport() type = %T, want *stdioProcessTransport", transport)
	}
}

func TestInheritedEnvironmentWithOverlay(t *testing.T) {
	inherited := []string{"KEEP=one", "REPLACE=old-a", "REPLACE=old-b"}
	overlay := map[string]string{"REPLACE": "new", "ADDED": "two"}
	inheritedBefore := append([]string(nil), inherited...)
	overlayBefore := maps.Clone(overlay)

	result := inheritedEnvironmentWithOverlay(inherited, overlay)
	want := []string{"KEEP=one", "ADDED=two", "REPLACE=new"}
	if !slices.Equal(result, want) {
		t.Fatalf("environment overlay = %#v, want %#v", result, want)
	}
	if !slices.Equal(inherited, inheritedBefore) {
		t.Fatalf("inherited environment mutated: %#v", inherited)
	}
	if !maps.Equal(overlay, overlayBefore) {
		t.Fatalf("environment overlay input mutated: %#v", overlay)
	}
}

func newTestStdioTransport(t *testing.T, mode, record string, extra ...string) *stdioProcessTransport {
	t.Helper()
	args := []string{"-test.run=^TestStdioTransportHelperProcess$", "--", mode, record}
	args = append(args, extra...)
	transport, err := newStdioProcessTransport(ServerConfig{
		Command: os.Args[0],
		Args:    args,
		Env:     map[string]string{stdioTransportHelperEnv: "1"},
		Type:    "stdio",
	})
	if err != nil {
		t.Fatalf("newStdioProcessTransport() error = %v", err)
	}
	return transport
}

func TestStdioTransportHelperProcess(*testing.T) {
	if os.Getenv(stdioTransportHelperEnv) != "1" {
		return
	}
	args := os.Args
	marker := -1
	for i, arg := range args {
		if arg == "--" {
			marker = i
			break
		}
	}
	if marker < 0 || len(args) < marker+3 {
		os.Exit(2)
	}
	mode, record := args[marker+1], args[marker+2]
	tail := args[marker+1:]

	switch mode {
	case "exit":
		cwd, _ := os.Getwd()
		content := strings.Join([]string{
			cwd,
			os.Getenv("YHC_STDIO_ENV_SENTINEL"),
			strings.Join(tail, ","),
		}, "\n")
		_ = os.WriteFile(record, []byte(content), 0o600)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(os.Args[0], "-test.run=^TestStdioTransportHelperProcess$", "--", "descendant", record)
		child.Env = append(os.Environ(), stdioTransportHelperEnv+"=1")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := writePIDRecord(record, os.Getpid(), child.Process.Pid); err != nil {
			os.Exit(7)
		}
		for {
			time.Sleep(time.Second)
		}
	case "descendant":
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(time.Second)
		}
	case "mcp-exit-tree":
		if len(args) < marker+4 {
			os.Exit(5)
		}
		child := exec.Command(
			os.Args[0],
			"-test.run=^TestStdioTransportHelperProcess$",
			"--",
			"descendant",
			record,
		)
		child.Env = append(os.Environ(), stdioTransportHelperEnv+"=1")
		if err := child.Start(); err != nil {
			os.Exit(6)
		}
		if err := writePIDRecord(record, os.Getpid(), child.Process.Pid); err != nil {
			os.Exit(7)
		}
		server := sdkmcp.NewServer(
			&sdkmcp.Implementation{Name: "exit-tree-test", Version: "1"},
			nil,
		)
		server.AddTool(
			&sdkmcp.Tool{
				Name:        "echo",
				Description: "test tool",
				InputSchema: map[string]any{"type": "object"},
			},
			func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{
						&sdkmcp.TextContent{Text: "ok"},
					},
				}, nil
			},
		)
		_, err := server.Connect(
			context.Background(),
			&sdkmcp.StdioTransport{},
			nil,
		)
		if err != nil {
			os.Exit(7)
		}
		exitFile := args[marker+3]
		for {
			if _, err := os.Stat(exitFile); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(4)
	}
}

func writePIDRecord(path string, pids ...int) error {
	var content strings.Builder
	for _, pid := range pids {
		fmt.Fprintf(&content, "%d\n", pid)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func waitForFile(path string) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for helper process record")
}

func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pids: %v", err)
	}
	var pids []int
	for _, line := range strings.Fields(string(content)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("invalid helper pid %q: %v", line, err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func waitForProcessGone(pid int) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process %d survived stdio transport close", pid)
}
