//go:build darwin || linux

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	enginemcp "github.com/abietic/yhc/engine/mcp"
)

const (
	ownedRefreshHelperEnv     = "YHC_OWNED_REFRESH_HELPER"
	ownedRefreshControlEnv    = "YHC_OWNED_REFRESH_CONTROL"
	ownedRefreshProcessIDEnv  = "YHC_OWNED_REFRESH_PID"
	ownedRefreshDescendantEnv = "YHC_OWNED_REFRESH_DESCENDANT_PID"
	ownedRefreshChildEnv      = "YHC_OWNED_REFRESH_CHILD"
	ownedRefreshInitialTool   = "old"
	ownedRefreshReplacement   = "new"
	ownedRefreshRegisteredOld = "mcp__dynamic__old"
	ownedRefreshRegisteredNew = "mcp__dynamic__new"
	boundedLaunchHelperEnv    = "YHC_BOUNDED_MCP_HELPER"
	boundedLaunchDirectoryEnv = "YHC_BOUNDED_MCP_DIRECTORY"
	boundedLaunchIDEnv        = "YHC_BOUNDED_MCP_ID"
	boundedLaunchReleaseEnv   = "YHC_BOUNDED_MCP_RELEASE"
)

func TestOwnedMCPRefreshGenerationRaceRemovesOnlyServerRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.Register(ToolImpl{Info: &schema.ToolInfo{Name: "builtin"}})
	manager, err := PrepareSessionMCPManager(
		t.Context(),
		t.TempDir(),
		registry,
		[]SessionMCPServer{{
			DescriptorIndex: 0,
			Name:            "dynamic",
			Config: enginemcp.ServerConfig{
				Name:    "dynamic",
				Command: command,
				Args:    []string{"-test.run=^TestOwnedMCPRefreshHelper$"},
				Env: map[string]string{
					ownedRefreshHelperEnv:    "1",
					ownedRefreshControlEnv:   controlPath,
					ownedRefreshProcessIDEnv: pidPath,
				},
				Type: "stdio",
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	pid := waitForOwnedRefreshPID(t, pidPath)
	if !registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("initial dynamic tool was not published")
	}

	var mutateOnce sync.Once
	manager.mu.Lock()
	manager.beforeOwnedRefreshPublishForTest = func() {
		mutateOnce.Do(func() {
			registry.Register(ToolImpl{
				Info: &schema.ToolInfo{Name: "unrelated"},
			})
		})
	}
	manager.mu.Unlock()
	if err := os.WriteFile(controlPath, []byte("replace"), 0o600); err != nil {
		t.Fatal(err)
	}

	waitForOwnedRegistryCondition(t, func() bool {
		return !registry.Resolve(ownedRefreshRegisteredOld).Registered &&
			!registry.Resolve(ownedRefreshRegisteredNew).Registered &&
			registry.Resolve("unrelated").Registered
	})
	waitForOwnedRefreshProcessGone(t, pid)
	if !registry.Resolve("builtin").Registered {
		t.Fatal("refresh compare failure removed a built-in row")
	}
	if count := manager.ServerToolCount("dynamic"); count != 0 {
		t.Fatalf("failed refresh retained %d manager tools", count)
	}
}

func TestEnsureSessionServersRejectsClientClosedBeforePublication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	manager, err := PrepareSessionMCPManager(
		t.Context(),
		t.TempDir(),
		registry,
		[]SessionMCPServer{{
			DescriptorIndex: 0,
			Name:            "dynamic",
			Config: enginemcp.ServerConfig{
				Name:    "dynamic",
				Command: command,
				Args:    []string{"-test.run=^TestOwnedMCPRefreshHelper$"},
				Env: map[string]string{
					ownedRefreshHelperEnv:    "1",
					ownedRefreshControlEnv:   controlPath,
					ownedRefreshProcessIDEnv: pidPath,
				},
				Type: "stdio",
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	firstPID := waitForOwnedRefreshPID(t, pidPath)
	if !registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("initial dynamic tool was not published")
	}

	if err := os.WriteFile(controlPath, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForOwnedRefreshProcessGone(t, firstPID)
	waitForOwnedRegistryCondition(t, func() bool {
		return !registry.Resolve(ownedRefreshRegisteredOld).Registered
	})
	if err := os.WriteFile(controlPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	manager.beforeSessionReconnectForTest = func(clients []*enginemcp.MCPClient) {
		secondPID := waitForOwnedRefreshPIDChange(t, pidPath, firstPID)
		if err := os.WriteFile(controlPath, []byte("exit"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForOwnedRefreshProcessGone(t, secondPID)
		deadline := time.Now().Add(5 * time.Second)
		for clients[0].IsConnected() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if clients[0].IsConnected() {
			t.Fatal("reconnect client remained connected after helper exit")
		}
	}
	manager.mu.Unlock()

	err = manager.EnsureSessionServers(t.Context())
	var setupErr *SessionMCPSetupError
	if !errors.As(err, &setupErr) ||
		setupErr.DescriptorIndex != 0 ||
		setupErr.Reason != "connection_closed" {
		t.Fatalf("EnsureSessionServers() error = %#v", err)
	}
	if registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("dead reconnect published a registry row")
	}
	if count := manager.ServerToolCount("dynamic"); count != 0 {
		t.Fatalf("dead reconnect retained %d manager tools", count)
	}
	manager.mu.RLock()
	_, published := manager.clients["dynamic"]
	manager.mu.RUnlock()
	if published {
		t.Fatal("dead reconnect published a manager client")
	}
}

func TestSessionMCPPrepareToleratesProjectServerClosing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectControlPath := filepath.Join(t.TempDir(), "project-control")
	projectPIDPath := filepath.Join(t.TempDir(), "project-pid")
	projectDescendantPIDPath := filepath.Join(t.TempDir(), "project-descendant-pid")
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	projectConfig := map[string]any{
		"mcpServers": map[string]any{
			"project-flaky": map[string]any{
				"command": command,
				"args":    []string{"-test.run=^TestOwnedMCPRefreshHelper$"},
				"env": map[string]string{
					ownedRefreshHelperEnv:     "1",
					ownedRefreshControlEnv:    projectControlPath,
					ownedRefreshProcessIDEnv:  projectPIDPath,
					ownedRefreshDescendantEnv: projectDescendantPIDPath,
				},
			},
		},
	}
	encoded, err := json.Marshal(projectConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, ".mcp.json"),
		encoded,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	launchDirectory := t.TempDir()
	releasePath := filepath.Join(t.TempDir(), "release")
	registry := NewRegistry()
	type prepareResult struct {
		manager *MCPToolManager
		err     error
	}
	resultCh := make(chan prepareResult, 1)
	go func() {
		manager, err := PrepareSessionMCPManager(
			t.Context(),
			projectDir,
			registry,
			[]SessionMCPServer{{
				DescriptorIndex: 0,
				Name:            "required",
				Config: enginemcp.ServerConfig{
					Name:    "required",
					Command: command,
					Args:    []string{"-test.run=^TestBoundedMCPLaunchHelper$"},
					Env: map[string]string{
						boundedLaunchHelperEnv:    "1",
						boundedLaunchDirectoryEnv: launchDirectory,
						boundedLaunchIDEnv:        "required",
						boundedLaunchReleaseEnv:   releasePath,
					},
					Type: "stdio",
				},
			}},
		)
		resultCh <- prepareResult{manager: manager, err: err}
	}()

	projectPID := waitForOwnedRefreshPID(t, projectPIDPath)
	projectDescendantPID := waitForOwnedRefreshPID(t, projectDescendantPIDPath)
	waitForBoundedLaunchCount(t, launchDirectory, 1)
	if err := os.WriteFile(projectControlPath, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForOwnedRefreshProcessGone(t, projectPID)
	waitForOwnedRefreshProcessGone(t, projectDescendantPID)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-resultCh
	if result.err != nil {
		t.Fatalf("PrepareSessionMCPManager() error = %v", result.err)
	}
	if result.manager == nil {
		t.Fatal("combined setup returned nil manager")
	}
	t.Cleanup(func() { _ = result.manager.DisconnectAll() })
	if registry.Resolve("mcp__project-flaky__old").Registered {
		t.Fatal("closed project server was published")
	}
	if !registry.Resolve("mcp__required__echo").Registered {
		t.Fatal("required session server was not published")
	}
	result.manager.mu.RLock()
	_, projectPublished := result.manager.clients["project-flaky"]
	_, requiredPublished := result.manager.clients["required"]
	result.manager.mu.RUnlock()
	if projectPublished || !requiredPublished {
		t.Fatalf(
			"published clients: project=%t required=%t",
			projectPublished,
			requiredPublished,
		)
	}
}

func TestSessionMCPPrepareCapsConcurrentLaunchesAtFour(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	launchDirectory := t.TempDir()
	releasePath := filepath.Join(t.TempDir(), "release")
	servers := make([]SessionMCPServer, 8)
	for index := range servers {
		name := "bounded-" + strconv.Itoa(index)
		servers[index] = SessionMCPServer{
			DescriptorIndex: index,
			Name:            name,
			Config: enginemcp.ServerConfig{
				Name:    name,
				Command: command,
				Args:    []string{"-test.run=^TestBoundedMCPLaunchHelper$"},
				Env: map[string]string{
					boundedLaunchHelperEnv:    "1",
					boundedLaunchDirectoryEnv: launchDirectory,
					boundedLaunchIDEnv:        strconv.Itoa(index),
					boundedLaunchReleaseEnv:   releasePath,
				},
				Type: "stdio",
			},
		}
	}
	type prepareResult struct {
		manager *MCPToolManager
		err     error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	projectDir := t.TempDir()
	resultCh := make(chan prepareResult, 1)
	go func() {
		manager, err := PrepareSessionMCPManager(
			ctx,
			projectDir,
			NewRegistry(),
			servers,
		)
		resultCh <- prepareResult{manager: manager, err: err}
	}()

	waitForBoundedLaunchCount(t, launchDirectory, sessionMCPLaunchConcurrency)
	time.Sleep(200 * time.Millisecond)
	entries, err := os.ReadDir(launchDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != sessionMCPLaunchConcurrency {
		t.Fatalf(
			"concurrent MCP launches = %d, want %d",
			len(entries),
			sessionMCPLaunchConcurrency,
		)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-resultCh
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.manager == nil {
		t.Fatal("bounded setup returned nil manager")
	}
	waitForBoundedLaunchCount(t, launchDirectory, len(servers))
	if err := result.manager.DisconnectAll(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedMCPRefreshHelper(*testing.T) {
	if os.Getenv(ownedRefreshHelperEnv) != "1" {
		return
	}
	if descendantPath := os.Getenv(ownedRefreshDescendantEnv); descendantPath != "" {
		child := exec.Command(
			os.Args[0],
			"-test.run=^TestOwnedMCPRefreshDescendantHelper$",
		)
		child.Env = append(os.Environ(), ownedRefreshChildEnv+"=1")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(
			descendantPath,
			[]byte(strconv.Itoa(child.Process.Pid)),
			0o600,
		)
	}
	_ = os.WriteFile(
		os.Getenv(ownedRefreshProcessIDEnv),
		[]byte(strconv.Itoa(os.Getpid())),
		0o600,
	)
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "owned-refresh-test", Version: "1"},
		&sdkmcp.ServerOptions{Capabilities: &sdkmcp.ServerCapabilities{
			Tools: &sdkmcp.ToolCapabilities{ListChanged: true},
		}},
	)
	addOwnedRefreshTool(server, ownedRefreshInitialTool)
	go func() {
		for {
			content, err := os.ReadFile(os.Getenv(ownedRefreshControlEnv))
			if err == nil {
				switch strings.TrimSpace(string(content)) {
				case "replace":
					server.RemoveTools(ownedRefreshInitialTool)
					addOwnedRefreshTool(server, ownedRefreshReplacement)
					return
				case "empty":
					server.RemoveTools(ownedRefreshInitialTool)
					return
				case "exit":
					os.Exit(0)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	session, err := server.Connect(
		context.Background(),
		&sdkmcp.StdioTransport{},
		nil,
	)
	if err != nil {
		os.Exit(2)
	}
	_ = session.Wait()
	os.Exit(0)
}

func TestOwnedMCPRefreshDescendantHelper(*testing.T) {
	if os.Getenv(ownedRefreshChildEnv) != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	for {
		time.Sleep(time.Second)
	}
}

func TestBoundedMCPLaunchHelper(*testing.T) {
	if os.Getenv(boundedLaunchHelperEnv) != "1" {
		return
	}
	recordPath := filepath.Join(
		os.Getenv(boundedLaunchDirectoryEnv),
		os.Getenv(boundedLaunchIDEnv)+".pid",
	)
	_ = os.WriteFile(recordPath, []byte(strconv.Itoa(os.Getpid())), 0o600)
	for {
		if _, err := os.Stat(os.Getenv(boundedLaunchReleaseEnv)); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "bounded-launch-test", Version: "1"},
		nil,
	)
	addOwnedRefreshTool(server, "echo")
	session, err := server.Connect(
		context.Background(),
		&sdkmcp.StdioTransport{},
		nil,
	)
	if err != nil {
		os.Exit(2)
	}
	_ = session.Wait()
	os.Exit(0)
}

func addOwnedRefreshTool(server *sdkmcp.Server, name string) {
	server.AddTool(
		&sdkmcp.Tool{
			Name:        name,
			Description: "owned refresh tool",
			InputSchema: map[string]any{"type": "object"},
		},
		func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{
						Text: "pid:" + strconv.Itoa(os.Getpid()),
					},
				},
			}, nil
		},
	)
}

func waitForOwnedRefreshPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for helper PID")
	return 0
}

func waitForOwnedRefreshPIDChange(t *testing.T, path string, previous int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
			if parseErr == nil && pid > 0 && pid != previous {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for helper PID to change from %d", previous)
	return 0
}

func waitForOwnedRegistryCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for registry refresh result")
}

func waitForOwnedRefreshProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned refresh helper %d survived cleanup", pid)
}

func waitForBoundedLaunchCount(t *testing.T, directory string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err == nil && len(entries) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d bounded MCP launches", want)
}
