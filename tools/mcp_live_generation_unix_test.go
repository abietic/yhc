//go:build darwin || linux

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	enginemcp "github.com/abietic/yhc/engine/mcp"
)

func TestInitMCPManagerPublishesAndRefreshesOwnedGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	projectDir := writeOwnedMCPProjectConfig(t, controlPath, pidPath)
	registry := NewRegistry()
	registry.Register(ToolImpl{Info: &schema.ToolInfo{Name: "builtin"}})

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	waitForOwnedRefreshPID(t, pidPath)

	initial := registry.Resolve(ownedRefreshRegisteredOld)
	if !initial.Registered || initial.Implementation.RegistrationOwner == "" {
		t.Fatalf("initial owned MCP resolution = %+v", initial)
	}
	beforeRefresh := registry.Generation()
	if err := os.WriteFile(controlPath, []byte("replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForOwnedRegistryCondition(t, func() bool {
		return !registry.Resolve(ownedRefreshRegisteredOld).Registered &&
			registry.Resolve(ownedRefreshRegisteredNew).Registered
	})

	refreshed := registry.Resolve(ownedRefreshRegisteredNew)
	if refreshed.Implementation.RegistrationOwner != initial.Implementation.RegistrationOwner {
		t.Fatalf(
			"refresh owner = %q, want same connection owner %q",
			refreshed.Implementation.RegistrationOwner,
			initial.Implementation.RegistrationOwner,
		)
	}
	if got := registry.Generation(); got != beforeRefresh+1 {
		t.Fatalf("refresh registry generation = %d, want %d", got, beforeRefresh+1)
	}
	if !registry.Resolve("builtin").Registered {
		t.Fatal("refresh removed unrelated built-in")
	}
	if count := manager.ServerToolCount("dynamic"); count != 1 {
		t.Fatalf("refreshed manager tool count = %d, want 1", count)
	}
}

func TestMCPRegistryHooksCanReenterManagerLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	manager := NewMCPToolManager()
	manager.mu.Lock()
	manager.registry = registry
	manager.mu.Unlock()
	t.Cleanup(func() { _ = manager.DisconnectAll() })

	registerHookDone := make(chan error, 1)
	unregisterHookDone := make(chan error, 1)
	registry.OnRegister(func(string, ToolImpl) {
		registerHookDone <- manager.DisconnectAll()
	})
	registry.OnUnregister(func(string, ToolImpl) {
		unregisterHookDone <- manager.DisconnectAll()
	})
	connectDone := make(chan error, 1)
	go func() {
		connectDone <- manager.ConnectServer(
			t.Context(),
			"dynamic",
			&enginemcp.ServerConfig{
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
		)
	}()
	pid := waitForOwnedRefreshPID(t, pidPath)
	select {
	case err := <-connectDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registry hook reentry deadlocked the manager lifecycle")
	}
	if err := <-registerHookDone; err != nil {
		t.Fatal(err)
	}
	if err := <-unregisterHookDone; err != nil {
		t.Fatal(err)
	}
	waitForOwnedRefreshProcessGone(t, pid)
	if registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("reentrant disconnect retained the registered MCP row")
	}
}

func TestConnectServerRejectsCloseBeforePublication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	registry := NewRegistry()
	manager := NewMCPToolManager()
	manager.mu.Lock()
	manager.registry = registry
	manager.beforeOwnedGenerationPublishForTest = func(client *enginemcp.MCPClient) {
		if err := client.Disconnect(); err != nil {
			t.Errorf("disconnect unpublished client: %v", err)
		}
	}
	manager.mu.Unlock()

	err := manager.ConnectServer(
		t.Context(),
		"dynamic",
		ownedMCPServerConfig(t, controlPath, pidPath),
	)
	if err == nil {
		t.Fatal("connection closed before publication was accepted")
	}
	pid := waitForOwnedRefreshPID(t, pidPath)
	waitForOwnedRefreshProcessGone(t, pid)
	if registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("closed candidate published an MCP row")
	}
	manager.mu.RLock()
	_, connected := manager.clients["dynamic"]
	failure := manager.failures["dynamic"]
	manager.mu.RUnlock()
	if connected || failure != "connection_closed" {
		t.Fatalf(
			"closed candidate state: connected=%t failure=%q",
			connected,
			failure,
		)
	}
}

func TestReconnectServerRejectsCloseBeforePublication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	registry := NewRegistry()
	manager := NewMCPToolManager()
	manager.mu.Lock()
	manager.registry = registry
	manager.mu.Unlock()
	t.Cleanup(func() { _ = manager.DisconnectAll() })

	if err := manager.ConnectServer(
		t.Context(),
		"dynamic",
		ownedMCPServerConfig(t, controlPath, pidPath),
	); err != nil {
		t.Fatal(err)
	}
	firstPID := waitForOwnedRefreshPID(t, pidPath)
	manager.mu.Lock()
	manager.beforeOwnedGenerationPublishForTest = func(client *enginemcp.MCPClient) {
		if err := client.Disconnect(); err != nil {
			t.Errorf("disconnect unpublished reconnect client: %v", err)
		}
	}
	manager.mu.Unlock()

	if err := manager.ReconnectServer(t.Context(), "dynamic"); err == nil {
		t.Fatal("reconnect closed before publication was accepted")
	}
	secondPID := waitForOwnedRefreshPIDChange(t, pidPath, firstPID)
	waitForOwnedRefreshProcessGone(t, firstPID)
	waitForOwnedRefreshProcessGone(t, secondPID)
	if registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("failed reconnect retained or published an MCP row")
	}
	manager.mu.RLock()
	_, connected := manager.clients["dynamic"]
	failure := manager.failures["dynamic"]
	manager.mu.RUnlock()
	if connected || failure != "connection_closed" {
		t.Fatalf(
			"failed reconnect state: connected=%t failure=%q",
			connected,
			failure,
		)
	}
}

func TestInitMCPManagerEmptyRefreshRemovesCompleteContribution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	projectDir := writeOwnedMCPProjectConfig(t, controlPath, pidPath)
	registry := NewRegistry()

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	waitForOwnedRefreshPID(t, pidPath)
	beforeRefresh := registry.Generation()

	if err := os.WriteFile(controlPath, []byte("empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForOwnedRegistryCondition(t, func() bool {
		return !registry.Resolve(ownedRefreshRegisteredOld).Registered &&
			manager.ServerToolCount("dynamic") == 0
	})
	if got := registry.Generation(); got != beforeRefresh+1 {
		t.Fatalf("empty refresh registry generation = %d, want %d", got, beforeRefresh+1)
	}
	if status, err := manager.ServerStatus("dynamic"); err != nil || status == "" {
		t.Fatalf("empty refresh disconnected live server: status=%q err=%v", status, err)
	}
}

func TestInitMCPManagerUnexpectedCloseRemovesExactOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	projectDir := writeOwnedMCPProjectConfig(t, controlPath, pidPath)
	registry := NewRegistry()
	registry.Register(ToolImpl{Info: &schema.ToolInfo{Name: "builtin"}})

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	pid := waitForOwnedRefreshPID(t, pidPath)
	if err := os.WriteFile(controlPath, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForOwnedRefreshProcessGone(t, pid)
	waitForOwnedRegistryCondition(t, func() bool {
		return !registry.Resolve(ownedRefreshRegisteredOld).Registered &&
			manager.ServerToolCount("dynamic") == 0
	})

	if !registry.Resolve("builtin").Registered {
		t.Fatal("unexpected close removed unrelated built-in")
	}
	if count := manager.ServerToolCount("dynamic"); count != 0 {
		t.Fatalf("closed manager retained %d tools", count)
	}
	snapshot := manager.InventorySnapshot()
	if len(snapshot.Servers) != 1 ||
		snapshot.Servers[0].Diagnostic != "connection_closed" {
		t.Fatalf("closed inventory snapshot = %+v", snapshot)
	}
}

func TestUnexpectedClosePreservesOtherMCPServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	alphaControl := filepath.Join(t.TempDir(), "alpha-control")
	alphaPID := filepath.Join(t.TempDir(), "alpha-pid")
	betaControl := filepath.Join(t.TempDir(), "beta-control")
	betaPID := filepath.Join(t.TempDir(), "beta-pid")
	projectDir := writeOwnedMCPProjectConfigs(t, map[string]ownedMCPProjectServer{
		"alpha": {controlPath: alphaControl, pidPath: alphaPID},
		"beta":  {controlPath: betaControl, pidPath: betaPID},
	})
	registry := NewRegistry()

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	alphaProcess := waitForOwnedRefreshPID(t, alphaPID)
	waitForOwnedRefreshPID(t, betaPID)
	if err := os.WriteFile(alphaControl, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForOwnedRefreshProcessGone(t, alphaProcess)
	waitForOwnedRegistryCondition(t, func() bool {
		return !registry.Resolve("mcp__alpha__old").Registered
	})

	if !registry.Resolve("mcp__beta__old").Registered {
		t.Fatal("alpha close removed beta MCP contribution")
	}
	if manager.ServerToolCount("alpha") != 0 ||
		manager.ServerToolCount("beta") != 1 {
		t.Fatalf(
			"manager counts after alpha close = alpha:%d beta:%d",
			manager.ServerToolCount("alpha"),
			manager.ServerToolCount("beta"),
		)
	}
}

func TestReconnectServerRotatesExactTargetAndRejectsLateCallbacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	projectDir := writeOwnedMCPProjectConfig(t, controlPath, pidPath)
	registry := NewRegistry()

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	firstPID := waitForOwnedRefreshPID(t, pidPath)
	oldResolution := registry.Resolve(ownedRefreshRegisteredOld)

	manager.mu.RLock()
	oldClient := manager.clients["dynamic"]
	oldGeneration := manager.serverGenerations["dynamic"]
	oldOwner := manager.serverOwners["dynamic"]
	manager.mu.RUnlock()
	if oldClient == nil || oldGeneration == 0 || oldOwner == "" {
		t.Fatal("initial connection generation was not recorded")
	}

	if err := manager.ReconnectServer(t.Context(), "dynamic"); err != nil {
		t.Fatal(err)
	}
	secondPID := waitForOwnedRefreshPIDChange(t, pidPath, firstPID)
	waitForOwnedRefreshProcessGone(t, firstPID)
	newResolution := registry.Resolve(ownedRefreshRegisteredOld)
	if !newResolution.Registered {
		t.Fatal("reconnect did not publish replacement tool")
	}

	manager.mu.RLock()
	newClient := manager.clients["dynamic"]
	newGeneration := manager.serverGenerations["dynamic"]
	newOwner := manager.serverOwners["dynamic"]
	manager.mu.RUnlock()
	if newClient == oldClient || newGeneration == oldGeneration || newOwner == oldOwner {
		t.Fatalf(
			"reconnect did not rotate identity: client_same=%t generation=%d/%d owner=%q/%q",
			newClient == oldClient,
			oldGeneration,
			newGeneration,
			oldOwner,
			newOwner,
		)
	}

	manager.handleServerCloseGeneration(
		"dynamic",
		oldClient,
		oldGeneration,
		oldOwner,
	)
	manager.refreshServerToolsGeneration(
		"dynamic",
		oldClient,
		oldGeneration,
		oldOwner,
	)
	if !registry.Resolve(ownedRefreshRegisteredOld).Registered {
		t.Fatal("late retired callback removed the current generation")
	}

	if _, err := oldResolution.Implementation.ExecuteCtx(
		t.Context(),
		`{}`,
	); err == nil {
		t.Fatal("retired exact implementation routed to a replacement connection")
	}
	output, err := newResolution.Implementation.ExecuteCtx(t.Context(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pid:" + strconv.Itoa(secondPID); output != want {
		t.Fatalf("replacement result = %q, want %q", output, want)
	}
	if _, err := registry.AcquireExecution(
		oldResolution.RequestedName,
		oldResolution.CanonicalName,
		oldResolution.Generation,
	); err == nil {
		t.Fatal("permission generation from before reconnect remained executable")
	}
}

func TestReconnectWaitsForLeaseAndLeaseNeverRoutesToReplacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	projectDir := writeOwnedMCPProjectConfig(t, controlPath, pidPath)
	registry := NewRegistry()

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	firstPID := waitForOwnedRefreshPID(t, pidPath)
	resolution := registry.Resolve(ownedRefreshRegisteredOld)
	lease, err := registry.AcquireExecution(
		resolution.RequestedName,
		resolution.CanonicalName,
		resolution.Generation,
	)
	if err != nil {
		t.Fatal(err)
	}

	reconnectDone := make(chan error, 1)
	go func() {
		reconnectDone <- manager.ReconnectServer(t.Context(), "dynamic")
	}()
	secondPID := waitForOwnedRefreshPIDChange(t, pidPath, firstPID)
	select {
	case err := <-reconnectDone:
		t.Fatalf("reconnect crossed an unconsumed lease: %v", err)
	default:
	}

	output, executeErr := lease.Execute(t.Context(), `{}`)
	if executeErr == nil {
		if replacement := "pid:" + strconv.Itoa(secondPID); output == replacement {
			t.Fatalf("old lease routed to replacement result %q", output)
		}
		if original := "pid:" + strconv.Itoa(firstPID); output != original {
			t.Fatalf("old lease result = %q, want %q or a closed-session error", output, original)
		}
	}
	if err := <-reconnectDone; err != nil {
		t.Fatal(err)
	}
	waitForOwnedRefreshProcessGone(t, firstPID)
	newResolution := registry.Resolve(ownedRefreshRegisteredOld)
	newOutput, err := newResolution.Implementation.ExecuteCtx(t.Context(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pid:" + strconv.Itoa(secondPID); newOutput != want {
		t.Fatalf("new generation result = %q, want %q", newOutput, want)
	}
}

func TestInitMCPManagerCollisionFailsClosedWithoutRemovingUnrelatedRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	controlPath := filepath.Join(t.TempDir(), "control")
	pidPath := filepath.Join(t.TempDir(), "pid")
	projectDir := writeOwnedMCPProjectConfig(t, controlPath, pidPath)
	registry := NewRegistry()
	registry.Register(ToolImpl{
		Info: &schema.ToolInfo{Name: ownedRefreshRegisteredOld},
		ExecuteCtx: func(_ context.Context, _ string) (string, error) {
			return "unrelated", nil
		},
	})

	manager, err := InitMCPManager(t.Context(), projectDir, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DisconnectAll() })
	pid := waitForOwnedRefreshPID(t, pidPath)
	waitForOwnedRefreshProcessGone(t, pid)

	resolution := registry.Resolve(ownedRefreshRegisteredOld)
	if !resolution.Registered ||
		resolution.Implementation.RegistrationOwner != "" {
		t.Fatalf("collision changed unrelated resolution = %+v", resolution)
	}
	if count := manager.ServerToolCount("dynamic"); count != 0 {
		t.Fatalf("collision retained %d manager tools", count)
	}
}

func TestValidateMCPToolInfosRejectsNormalizedDuplicate(t *testing.T) {
	err := validateMCPToolInfos([]*MCPToolInfo{
		{ServerName: "dynamic", ToolName: "same.name"},
		{ServerName: "dynamic", ToolName: "same/name"},
	})
	if !errors.Is(err, errMCPToolNameCollision) {
		t.Fatalf("normalized duplicate error = %v", err)
	}
}

func writeOwnedMCPProjectConfig(
	t *testing.T,
	controlPath string,
	pidPath string,
) string {
	t.Helper()
	return writeOwnedMCPProjectConfigs(t, map[string]ownedMCPProjectServer{
		"dynamic": {controlPath: controlPath, pidPath: pidPath},
	})
}

type ownedMCPProjectServer struct {
	controlPath string
	pidPath     string
}

func ownedMCPServerConfig(
	t *testing.T,
	controlPath string,
	pidPath string,
) *enginemcp.ServerConfig {
	t.Helper()
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return &enginemcp.ServerConfig{
		Name:    "dynamic",
		Command: command,
		Args:    []string{"-test.run=^TestOwnedMCPRefreshHelper$"},
		Env: map[string]string{
			ownedRefreshHelperEnv:    "1",
			ownedRefreshControlEnv:   controlPath,
			ownedRefreshProcessIDEnv: pidPath,
		},
		Type: "stdio",
	}
}

func writeOwnedMCPProjectConfigs(
	t *testing.T,
	servers map[string]ownedMCPProjectServer,
) string {
	t.Helper()
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	projectDir := t.TempDir()
	configured := make(map[string]any, len(servers))
	for name, server := range servers {
		configured[name] = map[string]any{
			"command": command,
			"args":    []string{"-test.run=^TestOwnedMCPRefreshHelper$"},
			"env": map[string]string{
				ownedRefreshHelperEnv:    "1",
				ownedRefreshControlEnv:   server.controlPath,
				ownedRefreshProcessIDEnv: server.pidPath,
			},
		}
	}
	config := map[string]any{
		"mcpServers": configured,
	}
	encoded, err := json.Marshal(config)
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
	return projectDir
}
