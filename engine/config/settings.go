package config

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// Settings holds the runtime-resolved configuration after merging global settings,
// project-level overrides, and environment variable overrides.
//
// This mirrors the resolved settings concept from the reference implementation
// (src/utils/settings/settings.ts), where multiple sources are merged into a single
// effective configuration for the runtime to use.
type Settings struct {
	// Model is the model identifier to use for inference.
	Model string `json:"model,omitempty"`
	// MaxTurns is the maximum number of agent turns per interaction. Zero is unlimited.
	MaxTurns int `json:"max_turns,omitempty"`
	// MaxTokens is the maximum output tokens per model call.
	MaxTokens int `json:"max_tokens,omitempty"`
	// Temperature controls the model's sampling temperature.
	Temperature float64 `json:"temperature,omitempty"`
	// PermissionMode controls the permission behavior (e.g., "default", "plan", "bypass").
	PermissionMode string `json:"permission_mode,omitempty"`
	// AutoCompact enables proactive conversation compaction.
	AutoCompact bool `json:"auto_compact,omitempty"`
	// CustomInstructions is additional system-level instructions appended to the prompt.
	CustomInstructions string `json:"custom_instructions,omitempty"`
	// AllowedTools restricts which tools are available to the agent.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DeniedTools explicitly disables specific tools.
	DeniedTools []string `json:"denied_tools,omitempty"`
	// Env holds environment variables to inject into tool execution.
	Env map[string]string `json:"env,omitempty"`
	// Verbose enables verbose/debug output.
	Verbose bool `json:"verbose,omitempty"`
	// APIBaseURL overrides the default API endpoint.
	APIBaseURL string `json:"api_base_url,omitempty"`
	// Timeout is the maximum duration for a single model call.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// DefaultSettings returns a Settings instance populated with sensible defaults.
// These defaults mirror the reference implementation's behavior.
func DefaultSettings() *Settings {
	return &Settings{
		Model:          "claude-sonnet-4-20250514",
		MaxTurns:       DefaultMaxTurns,
		MaxTokens:      16384,
		Temperature:    1.0,
		PermissionMode: "default",
		AutoCompact:    true,
		Timeout:        2 * time.Minute,
	}
}

// LoadSettings loads the final resolved settings for the given project directory.
//
// The resolution order (lowest to highest priority):
//  1. Built-in defaults (DefaultSettings)
//  2. Global user settings (~/.claude/settings.json)
//  3. Project-level settings (<projectDir>/.claude/settings.json)
//  4. Local settings (<projectDir>/.claude/settings.local.json) — not committed to git
//  5. Environment variable overrides (CLAUDE_MODEL, CLAUDE_MAX_TURNS, etc.)
//
// This mirrors the reference implementation's getInitialSettings() which merges
// settings from multiple sources in priority order.
func LoadSettings(projectDir string) (*Settings, error) {
	s := DefaultSettings()

	// Load and apply global user config.
	userCfg, err := LoadUserConfig()
	if err != nil {
		return nil, fmt.Errorf("loading user config: %w", err)
	}
	if userCfg != nil {
		s.applyConfig(userCfg)
	}

	// Load and apply project-level config (overrides global).
	if projectDir != "" {
		projectCfg, err := LoadProjectConfig(projectDir)
		if err != nil {
			return nil, fmt.Errorf("loading project config: %w", err)
		}
		if projectCfg != nil {
			s.applyConfig(projectCfg)
		}

		// Load and apply local settings (highest file priority, not committed to git).
		localCfg, err := LoadProjectLocalConfig(projectDir)
		if err != nil {
			return nil, fmt.Errorf("loading local config: %w", err)
		}
		if localCfg != nil {
			s.applyConfig(localCfg)
		}
	}

	// Apply environment variable overrides (highest priority).
	if err := s.applyEnvOverrides(); err != nil {
		return nil, err
	}

	return s, nil
}

// ValidateSettings checks a Settings instance for potential issues and returns
// a list of human-readable warning strings. An empty slice means no warnings.
func ValidateSettings(s *Settings) []string {
	var warnings []string

	if s.Model == "" {
		warnings = append(warnings, "model is empty; no model will be used for inference")
	}

	if s.MaxTurns < 0 {
		warnings = append(warnings, fmt.Sprintf("max_turns is %d; must be zero (unlimited) or positive", s.MaxTurns))
	}

	if s.MaxTokens < 1 {
		warnings = append(warnings, fmt.Sprintf("max_tokens is %d; must be at least 1", s.MaxTokens))
	}

	if s.Temperature < 0 || s.Temperature > 2 {
		warnings = append(warnings, fmt.Sprintf("temperature %.2f is outside the recommended range [0, 2]", s.Temperature))
	}

	validModes := map[string]bool{
		"default": true,
		"plan":    true,
		"bypass":  true,
		"auto":    true,
	}
	if s.PermissionMode != "" && !validModes[s.PermissionMode] {
		warnings = append(warnings, fmt.Sprintf("unknown permission_mode %q; expected one of: default, plan, bypass, auto", s.PermissionMode))
	}

	if s.Timeout < 0 {
		warnings = append(warnings, "timeout is negative")
	}

	return warnings
}

// MergeFrom applies non-zero fields from other onto s. Fields in other that are
// zero-valued (empty string, 0, nil slice/map, false for bool, zero Duration)
// are skipped, preserving the existing value in s.
//
// This is used to layer settings from multiple sources, where each successive
// source overrides only the fields it explicitly sets.
func (s *Settings) MergeFrom(other *Settings) {
	if other == nil {
		return
	}
	if other.Model != "" {
		s.Model = other.Model
	}
	s.MaxTurns = other.MaxTurns
	if other.MaxTokens != 0 {
		s.MaxTokens = other.MaxTokens
	}
	if other.Temperature != 0 {
		s.Temperature = other.Temperature
	}
	if other.PermissionMode != "" {
		s.PermissionMode = other.PermissionMode
	}
	// AutoCompact: always apply from source since it's meaningful as false.
	// The caller is responsible for only calling MergeFrom when the source
	// was actually loaded (non-nil), so we unconditionally copy.
	s.AutoCompact = other.AutoCompact
	if other.CustomInstructions != "" {
		s.CustomInstructions = other.CustomInstructions
	}
	if other.AllowedTools != nil {
		s.AllowedTools = other.AllowedTools
	}
	if other.DeniedTools != nil {
		s.DeniedTools = other.DeniedTools
	}
	if other.Env != nil {
		if s.Env == nil {
			s.Env = make(map[string]string)
		}
		for k, v := range other.Env {
			s.Env[k] = v
		}
	}
	if other.Verbose {
		s.Verbose = true
	}
	if other.APIBaseURL != "" {
		s.APIBaseURL = other.APIBaseURL
	}
	if other.Timeout != 0 {
		s.Timeout = other.Timeout
	}
}

// applyConfig maps fields from a raw Config (loaded from JSON) into the Settings.
func (s *Settings) applyConfig(cfg *Config) {
	if cfg.Model != "" {
		s.Model = cfg.Model
	}
	if cfg.APIBaseURL != "" {
		s.APIBaseURL = cfg.APIBaseURL
	}
	if cfg.maxTurnsSet || cfg.MaxTurns != 0 {
		s.MaxTurns = cfg.MaxTurns
	}
	if cfg.CustomSystemPrompt != "" {
		s.CustomInstructions = cfg.CustomSystemPrompt
	}
	if cfg.PermissionMode != "" {
		s.PermissionMode = cfg.PermissionMode
	}
	// AutoCompact: apply unconditionally from a loaded config, since explicit
	// false in a project config should disable the default true.
	s.AutoCompact = cfg.AutoCompact
	if cfg.AllowedTools != nil {
		s.AllowedTools = cfg.AllowedTools
	}
	if cfg.DisabledTools != nil {
		s.DeniedTools = cfg.DisabledTools
	}
	if cfg.Verbose {
		s.Verbose = true
	}
}

// applyEnvOverrides reads well-known environment variables and overrides
// the corresponding Settings fields. Environment variables take the highest
// priority in the settings resolution chain.
//
// Supported variables:
//   - CLAUDE_MODEL → Model
//   - CLAUDE_MAX_TURNS → MaxTurns
//   - CLAUDE_MAX_TOKENS → MaxTokens
//   - CLAUDE_PERMISSION_MODE → PermissionMode
//   - CLAUDE_CUSTOM_INSTRUCTIONS → CustomInstructions
func (s *Settings) applyEnvOverrides() error {
	if v := os.Getenv("CLAUDE_MODEL"); v != "" {
		s.Model = v
	}
	if v := os.Getenv("CLAUDE_MAX_TURNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("CLAUDE_MAX_TURNS must be zero (unlimited) or positive")
		}
		s.MaxTurns = n
	}
	if v := os.Getenv("CLAUDE_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.MaxTokens = n
		}
	}
	if v := os.Getenv("CLAUDE_PERMISSION_MODE"); v != "" {
		s.PermissionMode = v
	}
	if v := os.Getenv("CLAUDE_CUSTOM_INSTRUCTIONS"); v != "" {
		s.CustomInstructions = v
	}
	return nil
}

// ---------------------------------------------------------------------------
// Settings hot-reload watcher
// ---------------------------------------------------------------------------

// SettingsChangeCallback is called when settings files change.
type SettingsChangeCallback func(newSettings *Settings)

// SettingsWatcher polls settings files for changes and invokes a callback
// when the effective settings have changed. Uses polling (not fsnotify) for
// maximum portability.
type SettingsWatcher struct {
	projectDir string
	interval   time.Duration
	callback   SettingsChangeCallback
	stop       chan struct{}
	mu         sync.Mutex
	lastHash   string
	running    bool
}

// NewSettingsWatcher creates a watcher that checks for settings changes every interval.
// Default interval is 5 seconds if zero.
func NewSettingsWatcher(projectDir string, interval time.Duration, cb SettingsChangeCallback) *SettingsWatcher {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &SettingsWatcher{
		projectDir: projectDir,
		interval:   interval,
		callback:   cb,
		stop:       make(chan struct{}),
	}
}

// Start begins the polling loop in a background goroutine.
func (w *SettingsWatcher) Start() {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	// Capture initial hash.
	w.lastHash = w.computeHash()
	w.mu.Unlock()

	go w.pollLoop()
}

// Stop terminates the polling loop.
func (w *SettingsWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return
	}
	w.running = false
	close(w.stop)
}

func (w *SettingsWatcher) pollLoop() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			newHash := w.computeHash()
			w.mu.Lock()
			changed := newHash != w.lastHash
			if changed {
				w.lastHash = newHash
			}
			w.mu.Unlock()

			if changed && w.callback != nil {
				settings, err := LoadSettings(w.projectDir)
				if err == nil && settings != nil {
					w.callback(settings)
				}
			}
		}
	}
}

// computeHash creates a simple hash from file modification times of all settings files.
func (w *SettingsWatcher) computeHash() string {
	paths := []string{
		UserConfigPath(),
		ProjectConfigPath(w.projectDir),
		ProjectLocalConfigPath(w.projectDir),
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
