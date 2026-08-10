package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const managedSourceFileName = ".eino-plugin-source"

// Manager provides bounded local plugin install/uninstall workflows.
// It intentionally covers local directory installs only; marketplace clone,
// update/reconcile, trust UI, and plugin data cleanup remain higher-level
// product workflows.
type Manager struct {
	installDir string
	loader     *Loader
}

// InstallResult describes a local plugin install operation.
type InstallResult struct {
	Name      string
	Version   string
	SourceDir string
	TargetDir string
	Replaced  bool
}

// UninstallResult describes a managed plugin uninstall operation.
type UninstallResult struct {
	Name      string
	TargetDir string
	Removed   bool
}

// ReconcileResult describes a local plugin reconcile operation.
type ReconcileResult struct {
	Name      string
	SourceDir string
	TargetDir string
	Updated   bool
}

// NewManager creates a plugin manager rooted at installDir. The loader scans
// installDir plus any optional extra directories.
func NewManager(installDir string, extraDirs ...string) *Manager {
	dirs := append([]string{installDir}, extraDirs...)
	return &Manager{
		installDir: installDir,
		loader:     NewLoader(dirs...),
	}
}

// Load refreshes the managed plugin loader.
func (m *Manager) Load() error {
	if m == nil || m.loader == nil {
		return fmt.Errorf("plugins: manager not initialized")
	}
	return m.loader.Load()
}

// Loader returns the current loader.
func (m *Manager) Loader() *Loader {
	if m == nil {
		return nil
	}
	return m.loader
}

// InstallLocal installs a plugin from a local directory into the manager's
// install directory and refreshes the loader. Existing managed plugins with the
// same manifest name are atomically replaced.
func (m *Manager) InstallLocal(sourceDir string) (*InstallResult, error) {
	if m == nil || m.installDir == "" {
		return nil, fmt.Errorf("plugins: install directory not configured")
	}
	if err := Validate(sourceDir); err != nil {
		return nil, fmt.Errorf("plugins: validate source: %w", err)
	}
	plugin, err := loadPluginFromDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("plugins: load source: %w", err)
	}

	targetDir := filepath.Join(m.installDir, sanitizePluginDirName(plugin.Name))
	replaced := false
	if _, err := os.Stat(targetDir); err == nil {
		replaced = true
		if err := os.RemoveAll(targetDir); err != nil {
			return nil, fmt.Errorf("plugins: replace existing plugin: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("plugins: inspect target: %w", err)
	}

	if err := copyDir(sourceDir, targetDir); err != nil {
		return nil, fmt.Errorf("plugins: copy local plugin: %w", err)
	}
	if err := writeManagedSource(targetDir, sourceDir); err != nil {
		return nil, err
	}
	if err := m.Load(); err != nil {
		return nil, err
	}

	return &InstallResult{
		Name:      plugin.Name,
		Version:   plugin.Version,
		SourceDir: sourceDir,
		TargetDir: targetDir,
		Replaced:  replaced,
	}, nil
}

// Uninstall removes a plugin managed under installDir and refreshes the loader.
func (m *Manager) Uninstall(name string) (*UninstallResult, error) {
	if m == nil || m.installDir == "" {
		return nil, fmt.Errorf("plugins: install directory not configured")
	}
	normalized := pluginKey(name)
	if normalized == "" {
		return nil, fmt.Errorf("plugins: plugin name is required")
	}
	if err := m.Load(); err != nil {
		return nil, err
	}
	plugin, ok := m.loader.Get(normalized)
	if !ok {
		return nil, fmt.Errorf("plugins: plugin %q not installed", name)
	}
	targetDir := plugin.Directory
	if !isPathWithin(targetDir, m.installDir) {
		return nil, fmt.Errorf("plugins: plugin %q is not managed by install directory", name)
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, fmt.Errorf("plugins: uninstall: %w", err)
	}
	m.loader = NewLoader(m.installDir)
	if err := m.Load(); err != nil {
		return nil, err
	}
	return &UninstallResult{Name: plugin.Name, TargetDir: targetDir, Removed: true}, nil
}

// ReconcileLocal refreshes an installed local plugin from the source directory
// recorded during InstallLocal.
func (m *Manager) ReconcileLocal(name string) (*ReconcileResult, error) {
	if m == nil || m.installDir == "" {
		return nil, fmt.Errorf("plugins: install directory not configured")
	}
	if err := m.Load(); err != nil {
		return nil, err
	}
	plugin, ok := m.loader.Get(name)
	if !ok {
		return nil, fmt.Errorf("plugins: plugin %q not installed", name)
	}
	targetDir := plugin.Directory
	if !isPathWithin(targetDir, m.installDir) {
		return nil, fmt.Errorf("plugins: plugin %q is not managed by install directory", name)
	}
	sourceDir, err := readManagedSource(targetDir)
	if err != nil {
		return nil, err
	}
	result, err := m.InstallLocal(sourceDir)
	if err != nil {
		return nil, err
	}
	return &ReconcileResult{
		Name:      result.Name,
		SourceDir: result.SourceDir,
		TargetDir: result.TargetDir,
		Updated:   true,
	}, nil
}

func loadPluginFromDir(dir string) (*Plugin, error) {
	return NewLoader().loadPlugin(dir)
}

func sanitizePluginDirName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "plugin"
	}
	return out
}

func isPathWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}

	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func writeManagedSource(targetDir, sourceDir string) error {
	abs, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("plugins: resolve source path: %w", err)
	}
	return os.WriteFile(filepath.Join(targetDir, managedSourceFileName), []byte(abs), 0o644)
}

func readManagedSource(targetDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(targetDir, managedSourceFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("plugins: managed source metadata missing")
		}
		return "", fmt.Errorf("plugins: read managed source metadata: %w", err)
	}
	sourceDir := strings.TrimSpace(string(data))
	if sourceDir == "" {
		return "", fmt.Errorf("plugins: managed source metadata empty")
	}
	if err := Validate(sourceDir); err != nil {
		return "", fmt.Errorf("plugins: validate managed source: %w", err)
	}
	return sourceDir, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck
	_, err = io.Copy(out, in)
	return err
}
