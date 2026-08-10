package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLSPServiceManager_RegisterAndStart(t *testing.T) {
	mgr := NewLSPServiceManager()
	mgr.RegisterServer(LSPServerConfig{
		Language: "test",
		Command:  "sleep",
		Args:     []string{"10"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mgr.Start(ctx, "test"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if state := mgr.GetState("test"); state != LSPServerRunning {
		t.Errorf("expected running, got %v", state)
	}

	mgr.mu.Lock()
	done := mgr.servers["test"].done
	mgr.mu.Unlock()

	running := mgr.ListRunning()
	if len(running) != 1 || running[0] != "test" {
		t.Errorf("ListRunning: got %v", running)
	}

	if err := mgr.Stop("test"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if state := mgr.GetState("test"); state != LSPServerStopped {
		t.Errorf("expected stopped, got %v", state)
	}
	select {
	case <-done:
	default:
		t.Error("Stop returned before the process wait owner finished")
	}
}

func TestLSPServiceManager_StartAlreadyRunning(t *testing.T) {
	mgr := NewLSPServiceManager()
	mgr.RegisterServer(LSPServerConfig{
		Language: "test",
		Command:  "sleep",
		Args:     []string{"10"},
	})

	ctx := context.Background()
	if err := mgr.Start(ctx, "test"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.StopAll()

	if err := mgr.Start(ctx, "test"); err != nil {
		t.Errorf("second Start should be no-op, got: %v", err)
	}
}

func TestLSPServiceManager_StartUnregistered(t *testing.T) {
	mgr := NewLSPServiceManager()
	if err := mgr.Start(context.Background(), "unknown"); err == nil {
		t.Error("expected error for unregistered language")
	}
}

func TestLSPServiceManager_StartCommandNotFound(t *testing.T) {
	mgr := NewLSPServiceManager()
	mgr.RegisterServer(LSPServerConfig{
		Language: "test",
		Command:  "nonexistent-lsp-binary-xyz",
	})
	if err := mgr.Start(context.Background(), "test"); err == nil {
		t.Error("expected error for missing command")
	}
}

func TestLSPServiceManager_Restart(t *testing.T) {
	mgr := NewLSPServiceManager()
	mgr.RegisterServer(LSPServerConfig{
		Language: "test",
		Command:  "sleep",
		Args:     []string{"10"},
	})

	ctx := context.Background()
	if err := mgr.Start(ctx, "test"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := mgr.Restart(ctx, "test"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	defer mgr.StopAll()

	if state := mgr.GetState("test"); state != LSPServerRunning {
		t.Errorf("expected running after restart, got %v", state)
	}
}

func TestLSPServiceManager_EnsureRunning(t *testing.T) {
	mgr := NewLSPServiceManager()
	mgr.RegisterServer(LSPServerConfig{
		Language: "test",
		Command:  "sleep",
		Args:     []string{"10"},
	})

	ctx := context.Background()
	if err := mgr.EnsureRunning(ctx, "test"); err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer mgr.StopAll()

	if state := mgr.GetState("test"); state != LSPServerRunning {
		t.Errorf("expected running, got %v", state)
	}
}

func TestLSPServiceManager_StopAll(t *testing.T) {
	mgr := NewLSPServiceManager()
	mgr.RegisterServer(LSPServerConfig{Language: "a", Command: "sleep", Args: []string{"10"}})
	mgr.RegisterServer(LSPServerConfig{Language: "b", Command: "sleep", Args: []string{"10"}})

	ctx := context.Background()
	mgr.Start(ctx, "a")
	mgr.Start(ctx, "b")

	mgr.StopAll()

	if len(mgr.ListRunning()) != 0 {
		t.Error("expected no running servers after StopAll")
	}
}

func TestLSPServiceManager_StopNotRunning(t *testing.T) {
	mgr := NewLSPServiceManager()
	if err := mgr.Stop("nonexistent"); err != nil {
		t.Errorf("Stop nonexistent should be no-op, got: %v", err)
	}
}

func TestDetectProjectLanguages(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0o644)
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]"), 0o644)

	detected := DetectProjectLanguages(dir)
	hasGo, hasRust := false, false
	for _, lang := range detected {
		if lang == "go" {
			hasGo = true
		}
		if lang == "rust" {
			hasRust = true
		}
	}
	if !hasGo {
		t.Error("expected go detection from go.mod")
	}
	if !hasRust {
		t.Error("expected rust detection from Cargo.toml")
	}
}

func TestLSPServerState_String(t *testing.T) {
	tests := []struct {
		state LSPServerState
		want  string
	}{
		{LSPServerStopped, "stopped"},
		{LSPServerStarting, "starting"},
		{LSPServerRunning, "running"},
		{LSPServerStopping, "stopping"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
