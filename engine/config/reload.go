package config

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// ConfigChangeEvent describes a configuration change that was detected.
type ConfigChangeEvent struct {
	// Timestamp is when the change was detected.
	Timestamp time.Time
	// PreviousSettings holds the settings before the change (nil on first load).
	PreviousSettings *Settings
	// NewSettings holds the newly loaded settings.
	NewSettings *Settings
	// ValidationResults holds any validation issues with the new config.
	ValidationResults *ValidationResults
	// Source describes what triggered the reload (e.g., "file_change", "explicit_reload").
	Source string
}

// ConfigReloadCallback is called when configuration is reloaded.
type ConfigReloadCallback func(event ConfigChangeEvent)

// ConfigReloader monitors configuration files and provides hot-reload capability.
// It extends the SettingsWatcher concept with:
//   - Event emission for config changes
//   - Validation of new configuration before applying
//   - Graceful handling of invalid config (keeps previous valid config)
//   - Explicit ReloadConfig() method for on-demand reload
//   - Multiple listener support
type ConfigReloader struct {
	mu              sync.RWMutex
	projectDir      string
	interval        time.Duration
	listeners       []ConfigReloadCallback
	currentSettings *Settings
	lastHash        string
	stop            chan struct{}
	running         bool
}

// NewConfigReloader creates a new config reloader for the given project directory.
// The interval specifies how often to check for changes (default: 5s).
func NewConfigReloader(projectDir string, interval time.Duration) *ConfigReloader {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &ConfigReloader{
		projectDir: projectDir,
		interval:   interval,
		stop:       make(chan struct{}),
	}
}

// OnChange registers a callback that will be invoked when config changes.
// Multiple callbacks can be registered; they are called in registration order.
func (cr *ConfigReloader) OnChange(cb ConfigReloadCallback) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.listeners = append(cr.listeners, cb)
}

// CurrentSettings returns the most recently loaded valid settings.
// Returns nil if no settings have been loaded yet.
func (cr *ConfigReloader) CurrentSettings() *Settings {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	return cr.currentSettings
}

// Start begins watching configuration files for changes.
// Performs an initial load before starting the watch loop.
func (cr *ConfigReloader) Start() error {
	cr.mu.Lock()
	if cr.running {
		cr.mu.Unlock()
		return nil
	}

	// Perform initial load.
	settings, err := LoadSettings(cr.projectDir)
	if err != nil {
		cr.mu.Unlock()
		return fmt.Errorf("initial config load failed: %w", err)
	}

	cr.currentSettings = settings
	cr.lastHash = computeSettingsHash(cr.projectDir)
	cr.running = true
	cr.mu.Unlock()

	go cr.pollLoop()
	return nil
}

// Stop terminates the config reload watcher.
func (cr *ConfigReloader) Stop() {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if !cr.running {
		return
	}
	cr.running = false
	close(cr.stop)
}

// ReloadConfig explicitly triggers a config reload, regardless of file changes.
// Returns the new settings and any validation results.
// If the new config is invalid (has errors), the previous valid config is retained.
func (cr *ConfigReloader) ReloadConfig() (*Settings, *ValidationResults, error) {
	newSettings, err := LoadSettings(cr.projectDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reloading config: %w", err)
	}

	// Validate the new configuration.
	vr := ValidateConfig(newSettings)

	cr.mu.Lock()
	previousSettings := cr.currentSettings

	// Only apply the new settings if there are no errors.
	// Warnings are acceptable — only hard errors prevent adoption.
	if !vr.HasErrors() {
		cr.currentSettings = newSettings
		cr.lastHash = computeSettingsHash(cr.projectDir)
	}
	cr.mu.Unlock()

	// Emit change event.
	event := ConfigChangeEvent{
		Timestamp:         time.Now(),
		PreviousSettings:  previousSettings,
		NewSettings:       newSettings,
		ValidationResults: vr,
		Source:            "explicit_reload",
	}
	cr.notifyListeners(event)

	if vr.HasErrors() {
		return previousSettings, vr, nil
	}
	return newSettings, vr, nil
}

// pollLoop checks for config file changes at the configured interval.
func (cr *ConfigReloader) pollLoop() {
	ticker := time.NewTicker(cr.interval)
	defer ticker.Stop()

	for {
		select {
		case <-cr.stop:
			return
		case <-ticker.C:
			cr.checkForChanges()
		}
	}
}

// checkForChanges detects file modifications and triggers reload if needed.
func (cr *ConfigReloader) checkForChanges() {
	newHash := computeSettingsHash(cr.projectDir)

	cr.mu.RLock()
	changed := newHash != cr.lastHash
	cr.mu.RUnlock()

	if !changed {
		return
	}

	newSettings, err := LoadSettings(cr.projectDir)
	if err != nil {
		// Can't load — keep current settings, skip this cycle.
		return
	}

	// Validate the new configuration.
	vr := ValidateConfig(newSettings)

	cr.mu.Lock()
	previousSettings := cr.currentSettings

	if !vr.HasErrors() {
		cr.currentSettings = newSettings
	}
	// Update hash regardless to avoid re-triggering every cycle for invalid configs.
	cr.lastHash = newHash
	cr.mu.Unlock()

	// Emit change event (even for invalid configs, so listeners can react/log).
	event := ConfigChangeEvent{
		Timestamp:         time.Now(),
		PreviousSettings:  previousSettings,
		NewSettings:       newSettings,
		ValidationResults: vr,
		Source:            "file_change",
	}
	cr.notifyListeners(event)
}

// notifyListeners calls all registered callbacks with the event.
func (cr *ConfigReloader) notifyListeners(event ConfigChangeEvent) {
	cr.mu.RLock()
	listeners := make([]ConfigReloadCallback, len(cr.listeners))
	copy(listeners, cr.listeners)
	cr.mu.RUnlock()

	for _, cb := range listeners {
		cb(event)
	}
}

// computeSettingsHash builds a hash string from config file modification times.
func computeSettingsHash(projectDir string) string {
	paths := []string{
		UserConfigPath(),
	}
	if projectDir != "" {
		paths = append(paths, ProjectConfigPath(projectDir))
		paths = append(paths, ProjectLocalConfigPath(projectDir))
	}

	var hash string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			hash += p + ":absent;"
		} else {
			hash += fmt.Sprintf("%s:%d;", p, info.ModTime().UnixNano())
		}
	}
	return hash
}
