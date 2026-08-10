package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RemoteInstallResult describes a plugin installed from a remote marketplace.
type RemoteInstallResult struct {
	Name      string
	Version   string
	Source    string
	TargetDir string
	Replaced  bool
}

// UpdateCheckResult describes the result of checking for plugin updates.
type UpdateCheckResult struct {
	Name             string
	InstalledVersion string
	LatestVersion    string
	UpdateAvailable  bool
}

// InstallFromMarketplace searches the marketplace for a plugin by name,
// downloads/clones it, and installs it into the managed install directory.
func (m *Manager) InstallFromMarketplace(ctx context.Context, client *MarketplaceClient, name string) (*RemoteInstallResult, error) {
	if m == nil || m.installDir == "" {
		return nil, fmt.Errorf("plugins: install directory not configured")
	}
	if client == nil {
		return nil, fmt.Errorf("plugins: marketplace client is nil")
	}

	info, err := client.GetPlugin(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("plugins: marketplace lookup: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "eino-plugin-install-*")
	if err != nil {
		return nil, fmt.Errorf("plugins: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	sourceDir := filepath.Join(tmpDir, "plugin")
	if err := fetchPluginSource(ctx, info.Source, sourceDir); err != nil {
		return nil, fmt.Errorf("plugins: fetch source: %w", err)
	}

	if err := Validate(sourceDir); err != nil {
		return nil, fmt.Errorf("plugins: validate fetched plugin: %w", err)
	}

	result, err := m.InstallLocal(sourceDir)
	if err != nil {
		return nil, err
	}

	if err := writeRemoteSource(result.TargetDir, info.Source, info.Version); err != nil {
		return nil, err
	}

	return &RemoteInstallResult{
		Name:      result.Name,
		Version:   info.Version,
		Source:    info.Source,
		TargetDir: result.TargetDir,
		Replaced:  result.Replaced,
	}, nil
}

// CheckForUpdates queries the marketplace for the latest version of an
// installed plugin and reports whether an update is available.
func (m *Manager) CheckForUpdates(ctx context.Context, client *MarketplaceClient, name string) (*UpdateCheckResult, error) {
	if m == nil {
		return nil, fmt.Errorf("plugins: manager not initialized")
	}
	if err := m.Load(); err != nil {
		return nil, err
	}
	plugin, ok := m.loader.Get(name)
	if !ok {
		return nil, fmt.Errorf("plugins: plugin %q not installed", name)
	}

	info, err := client.GetPlugin(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("plugins: marketplace lookup: %w", err)
	}

	return &UpdateCheckResult{
		Name:             plugin.Name,
		InstalledVersion: plugin.Version,
		LatestVersion:    info.Version,
		UpdateAvailable:  info.Version != "" && info.Version != plugin.Version,
	}, nil
}

// UpdatePlugin fetches the latest version of a plugin from the marketplace
// and re-installs it.
func (m *Manager) UpdatePlugin(ctx context.Context, client *MarketplaceClient, name string) (*RemoteInstallResult, error) {
	check, err := m.CheckForUpdates(ctx, client, name)
	if err != nil {
		return nil, err
	}
	if !check.UpdateAvailable {
		return &RemoteInstallResult{
			Name:    check.Name,
			Version: check.InstalledVersion,
		}, nil
	}
	return m.InstallFromMarketplace(ctx, client, name)
}

const remoteSourceFileName = ".eino-plugin-remote"

type remoteSourceMeta struct {
	Source    string `json:"source"`
	Version   string `json:"version"`
	Installed string `json:"installed"`
}

func writeRemoteSource(targetDir, source, version string) error {
	meta := remoteSourceMeta{
		Source:    source,
		Version:   version,
		Installed: time.Now().Format(time.RFC3339),
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("plugins: marshal remote source: %w", err)
	}
	return os.WriteFile(filepath.Join(targetDir, remoteSourceFileName), data, 0o644)
}

func fetchPluginSource(ctx context.Context, source, destDir string) error {
	if source == "" {
		return fmt.Errorf("empty source")
	}

	if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") {
		if strings.HasSuffix(source, ".git") || strings.Contains(source, "github.com/") {
			return gitClone(ctx, source, destDir)
		}
		return httpDownloadAndExtract(ctx, source, destDir)
	}

	if strings.HasPrefix(source, "git@") || strings.HasPrefix(source, "ssh://") {
		return gitClone(ctx, source, destDir)
	}

	if info, err := os.Stat(source); err == nil && info.IsDir() {
		return copyDir(source, destDir)
	}

	return fmt.Errorf("unsupported source: %s", source)
}

func gitClone(ctx context.Context, url, destDir string) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", url, destDir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %w\n%s", err, output)
	}
	os.RemoveAll(filepath.Join(destDir, ".git"))
	return nil
}

func httpDownloadAndExtract(ctx context.Context, url, destDir string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	manifestPath := filepath.Join(destDir, "plugin.json")

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var manifest map[string]any
	if json.Unmarshal(data, &manifest) == nil {
		if _, ok := manifest["name"]; ok {
			return os.WriteFile(manifestPath, data, 0o644)
		}
	}

	indexPath := filepath.Join(destDir, "plugin-archive")
	return os.WriteFile(indexPath, data, 0o644)
}
