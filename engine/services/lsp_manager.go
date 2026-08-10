package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// LSPServerState represents the lifecycle state of an LSP server.
type LSPServerState int

const (
	LSPServerStopped LSPServerState = iota
	LSPServerStarting
	LSPServerRunning
	LSPServerStopping
)

func (s LSPServerState) String() string {
	switch s {
	case LSPServerStopped:
		return "stopped"
	case LSPServerStarting:
		return "starting"
	case LSPServerRunning:
		return "running"
	case LSPServerStopping:
		return "stopping"
	default:
		return "unknown"
	}
}

// LSPServerConfig describes how to start an LSP server.
type LSPServerConfig struct {
	Language string
	Command  string
	Args     []string
	RootDir  string
}

// LSPServer tracks a running LSP server process.
type LSPServer struct {
	Config    LSPServerConfig
	State     LSPServerState
	StartedAt time.Time
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	done      chan struct{}
}

// LSPServiceManager manages the lifecycle of LSP servers.
// It auto-starts servers on first use for a detected project language
// and keeps them running across tool calls within a session.
type LSPServiceManager struct {
	mu      sync.Mutex
	servers map[string]*LSPServer
	configs map[string]LSPServerConfig
}

// NewLSPServiceManager creates a new LSP service manager.
func NewLSPServiceManager() *LSPServiceManager {
	return &LSPServiceManager{
		servers: make(map[string]*LSPServer),
		configs: make(map[string]LSPServerConfig),
	}
}

// RegisterServer registers an LSP server configuration for a language.
func (m *LSPServiceManager) RegisterServer(cfg LSPServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[cfg.Language] = cfg
}

// Start starts the LSP server for the given language. If already running,
// this is a no-op. Returns an error if the server fails to start.
func (m *LSPServiceManager) Start(ctx context.Context, language string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if srv, ok := m.servers[language]; ok && srv.State == LSPServerRunning {
		return nil
	}

	cfg, ok := m.configs[language]
	if !ok {
		return fmt.Errorf("lsp: no server configured for language %q", language)
	}

	if _, err := exec.LookPath(cfg.Command); err != nil {
		return fmt.Errorf("lsp: command %q not found: %w", cfg.Command, err)
	}

	serverCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(serverCtx, cfg.Command, cfg.Args...)
	if cfg.RootDir != "" {
		cmd.Dir = cfg.RootDir
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("lsp: start %q failed: %w", cfg.Language, err)
	}

	srv := &LSPServer{
		Config:    cfg,
		State:     LSPServerRunning,
		StartedAt: time.Now(),
		cmd:       cmd,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.servers[language] = srv

	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if s, ok := m.servers[language]; ok && s == srv {
			s.State = LSPServerStopped
		}
		close(srv.done)
		m.mu.Unlock()
	}()

	log.Printf("[lsp] started %s server (pid %d)", language, cmd.Process.Pid)
	return nil
}

// Stop stops the LSP server for the given language.
func (m *LSPServiceManager) Stop(language string) error {
	m.mu.Lock()
	srv, ok := m.servers[language]
	if !ok || srv.State != LSPServerRunning {
		m.mu.Unlock()
		return nil
	}
	srv.State = LSPServerStopping
	m.mu.Unlock()

	srv.cancel()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-srv.done:
	case <-timer.C:
		if srv.cmd.Process != nil {
			_ = srv.cmd.Process.Kill()
		}
		<-srv.done
	}

	m.mu.Lock()
	if current, exists := m.servers[language]; exists && current == srv {
		srv.State = LSPServerStopped
	}
	m.mu.Unlock()

	log.Printf("[lsp] stopped %s server", language)
	return nil
}

// Restart stops and starts the LSP server for the given language.
func (m *LSPServiceManager) Restart(ctx context.Context, language string) error {
	if err := m.Stop(language); err != nil {
		return err
	}
	return m.Start(ctx, language)
}

// EnsureRunning starts the server if not already running (auto-start behavior).
func (m *LSPServiceManager) EnsureRunning(ctx context.Context, language string) error {
	m.mu.Lock()
	srv, ok := m.servers[language]
	running := ok && srv.State == LSPServerRunning
	m.mu.Unlock()

	if running {
		return nil
	}
	return m.Start(ctx, language)
}

// GetState returns the current state of the server for a language.
func (m *LSPServiceManager) GetState(language string) LSPServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	srv, ok := m.servers[language]
	if !ok {
		return LSPServerStopped
	}
	return srv.State
}

// StopAll stops all running LSP servers.
func (m *LSPServiceManager) StopAll() {
	m.mu.Lock()
	languages := make([]string, 0, len(m.servers))
	for lang, srv := range m.servers {
		if srv.State == LSPServerRunning {
			languages = append(languages, lang)
		}
	}
	m.mu.Unlock()

	for _, lang := range languages {
		_ = m.Stop(lang)
	}
}

// ListRunning returns the languages with running servers.
func (m *LSPServiceManager) ListRunning() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var running []string
	for lang, srv := range m.servers {
		if srv.State == LSPServerRunning {
			running = append(running, lang)
		}
	}
	return running
}

// DetectProjectLanguages inspects the project directory for common language
// indicators and returns suggested languages for auto-start.
func DetectProjectLanguages(rootDir string) []string {
	indicators := map[string][]string{
		"go":         {"go.mod", "go.sum"},
		"typescript": {"tsconfig.json", "package.json"},
		"python":     {"pyproject.toml", "setup.py", "requirements.txt"},
		"rust":       {"Cargo.toml"},
		"java":       {"pom.xml", "build.gradle"},
	}

	var detected []string
	for lang, files := range indicators {
		for _, f := range files {
			if fileExists(fmt.Sprintf("%s/%s", rootDir, f)) {
				detected = append(detected, lang)
				break
			}
		}
	}
	return detected
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
