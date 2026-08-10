package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PluginManifest describes a plugin discovered from a manifest file on disk.
// Manifest files are JSON files with the ".json" extension.
type PluginManifest struct {
	// Name is the unique plugin identifier.
	Name string `json:"name"`

	// Version is the plugin version string.
	Version string `json:"version"`

	// EntryPoint is the Go import path or binary path for the plugin.
	EntryPoint string `json:"entry_point"`

	// Capabilities declares what this plugin provides.
	Capabilities []string `json:"capabilities"`

	// Dependencies are plugin names that must be started before this one.
	Dependencies []string `json:"dependencies"`

	// Enabled controls whether the plugin should be loaded. Defaults to true
	// if omitted from the manifest.
	Enabled *bool `json:"enabled,omitempty"`

	// Config holds plugin-specific configuration.
	Config map[string]any `json:"config,omitempty"`

	// FilePath is the path to the manifest file (set during discovery, not serialized).
	FilePath string `json:"-"`
}

// IsEnabled returns true if the plugin is enabled. A nil Enabled field
// is treated as true (enabled by default).
func (m *PluginManifest) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// DiscoveryConfig configures plugin discovery behavior.
type DiscoveryConfig struct {
	// Dirs is the list of directories to scan for plugin manifests.
	Dirs []string

	// DisabledPlugins is a set of plugin names that should be treated as
	// disabled regardless of their manifest's Enabled field.
	DisabledPlugins map[string]bool
}

// DiscoveryResult holds the output of a plugin discovery scan.
type DiscoveryResult struct {
	// Manifests contains all valid, enabled plugin manifests found.
	Manifests []*PluginManifest

	// Errors contains discovery errors keyed by file path.
	Errors map[string]error
}

// DiscoverPlugins scans the configured directories for plugin manifest JSON
// files and returns the discovery results. It handles:
// - Missing directories (skipped without error)
// - Invalid JSON manifests (recorded in Errors)
// - Duplicate plugin names (first wins, duplicates recorded in Errors)
// - Disabled plugins (excluded from Manifests)
func DiscoverPlugins(config DiscoveryConfig) *DiscoveryResult {
	result := &DiscoveryResult{
		Errors: make(map[string]error),
	}

	seen := make(map[string]string) // plugin name -> manifest file path

	for _, dir := range config.Dirs {
		discoverDir(dir, config, result, seen)
	}

	return result
}

// discoverDir scans a single directory for plugin manifest files.
func discoverDir(dir string, config DiscoveryConfig, result *DiscoveryResult, seen map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing directory is not an error — silently skip.
			return
		}
		result.Errors[dir] = fmt.Errorf("failed to read directory: %w", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}

		filePath := filepath.Join(dir, name)
		manifest, err := loadManifest(filePath)
		if err != nil {
			result.Errors[filePath] = err
			continue
		}

		// Validate required fields.
		if err := validateManifest(manifest); err != nil {
			result.Errors[filePath] = err
			continue
		}

		manifest.FilePath = filePath

		// Check for duplicates.
		if existingPath, exists := seen[manifest.Name]; exists {
			result.Errors[filePath] = fmt.Errorf(
				"duplicate plugin name %q (first seen at %s)", manifest.Name, existingPath,
			)
			continue
		}
		seen[manifest.Name] = filePath

		// Check if disabled via config.
		if config.DisabledPlugins[manifest.Name] {
			continue
		}

		// Check if disabled via manifest.
		if !manifest.IsEnabled() {
			continue
		}

		result.Manifests = append(result.Manifests, manifest)
	}
}

// loadManifest reads and parses a single manifest JSON file.
func loadManifest(path string) (*PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("invalid JSON in manifest: %w", err)
	}

	return &manifest, nil
}

// validateManifest checks that required fields are present in a manifest.
func validateManifest(m *PluginManifest) error {
	if m.Name == "" {
		return fmt.Errorf("manifest missing required field: name")
	}
	if m.Version == "" {
		return fmt.Errorf("manifest missing required field: version")
	}
	return nil
}

// ManifestPlugin is a Plugin implementation backed by a PluginManifest.
// It serves as a placeholder for discovered plugins whose actual implementation
// is loaded at a higher layer. The Start/Stop/Health methods are no-ops.
type ManifestPlugin struct {
	manifest *PluginManifest
}

// NewManifestPlugin creates a Plugin from a discovered manifest.
func NewManifestPlugin(manifest *PluginManifest) *ManifestPlugin {
	return &ManifestPlugin{manifest: manifest}
}

// Name returns the plugin name from the manifest.
func (p *ManifestPlugin) Name() string {
	return p.manifest.Name
}

// Version returns the plugin version from the manifest.
func (p *ManifestPlugin) Version() string {
	return p.manifest.Version
}

// Capabilities returns the plugin capabilities from the manifest.
func (p *ManifestPlugin) Capabilities() []string {
	caps := make([]string, len(p.manifest.Capabilities))
	copy(caps, p.manifest.Capabilities)
	return caps
}

// Dependencies returns the plugin dependencies from the manifest.
func (p *ManifestPlugin) Dependencies() []string {
	deps := make([]string, len(p.manifest.Dependencies))
	copy(deps, p.manifest.Dependencies)
	return deps
}

// Start is a no-op for manifest-backed plugins.
func (p *ManifestPlugin) Start(ctx context.Context) error {
	return nil
}

// Stop is a no-op for manifest-backed plugins.
func (p *ManifestPlugin) Stop(ctx context.Context) error {
	return nil
}

// Health always returns nil (healthy) for manifest-backed plugins.
func (p *ManifestPlugin) Health() error {
	return nil
}

// Manifest returns the underlying PluginManifest.
func (p *ManifestPlugin) Manifest() *PluginManifest {
	return p.manifest
}
