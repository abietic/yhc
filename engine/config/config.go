// Package config provides configuration management for the YHC engine.
//
// It supports a two-tier configuration model:
//   - User-level settings stored in ~/.claude/settings.json
//   - Project-level overrides stored in <projectDir>/.claude/settings.json
//
// Project-level settings take precedence over user-level settings when both exist.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Config holds the full agent configuration. Fields are loaded from
// ~/.claude/settings.json (user-level) and optionally overridden by
// <project>/.claude/settings.json (project-level).
type Config struct {
	// Provider selects the model provider. Short and internal names are accepted.
	Provider string `json:"provider,omitempty"`
	// Model is the default model to use
	Model string `json:"model,omitempty"`
	// APIBaseURL overrides the selected provider endpoint.
	APIBaseURL string `json:"api_base_url,omitempty"`
	// FallbackModel is selected after bounded overload retries.
	FallbackModel string `json:"fallback_model,omitempty"`
	// ModelAliases maps user-facing model names to model identifiers.
	ModelAliases map[string]string `json:"model_aliases,omitempty"`
	// ModelProfile selects one user-owned portfolio profile.
	ModelProfile string `json:"model_profile,omitempty"`
	// ProviderAccounts contains only non-secret user-owned route definitions.
	ProviderAccounts map[string]ProviderAccountConfig `json:"provider_accounts,omitempty"`
	// ModelProfiles contains user-owned presentation/model bindings.
	ModelProfiles map[string]ModelProfileConfig `json:"model_profiles,omitempty"`
	// ModelRoles binds optional logical roles. Runtime role routing is delivered
	// by a later slice; P29.1 validates and snapshots these definitions only.
	ModelRoles map[string]string `json:"model_roles,omitempty"`
	// FailoverPolicies are validated and snapshotted but not executed in P29.1.
	FailoverPolicies map[string]FailoverPolicyConfig `json:"failover_policies,omitempty"`
	// MaxTurns is the maximum turns per interaction
	MaxTurns int `json:"max_turns"`
	// CustomSystemPrompt overrides the default system prompt
	CustomSystemPrompt string `json:"custom_system_prompt,omitempty"`
	// PermissionMode is the default permission mode
	PermissionMode string `json:"permission_mode,omitempty"`
	// PermissionRules are tool permission rules
	PermissionRules []string `json:"permission_rules,omitempty"`
	// AutoCompact enables proactive compaction
	AutoCompact bool `json:"auto_compact,omitempty"`
	// MCPServers configures MCP server connections
	MCPServers map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	// AllowedTools restricts which tools are available
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DisabledTools explicitly disables specific tools
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// Theme is the UI theme preference
	Theme string `json:"theme,omitempty"`
	// ReducedMotion disables non-essential TUI animation
	ReducedMotion bool `json:"reduced_motion,omitempty"`
	// Verbose enables verbose output
	Verbose bool `json:"verbose,omitempty"`
	// Goal gates supported saved-root Goal workflows. It is enabled by default,
	// and no token budget is implied unless a positive value is configured.
	Goal *GoalConfig `json:"goal,omitempty"`
	// Sandbox contains user-owned sandbox selection authority.
	Sandbox *SandboxConfig `json:"sandbox,omitempty"`

	maxTurnsSet            bool
	portfolioDefinitionSet bool
}

// GoalConfig is the field-presence-aware configuration for the opt-in Goal
// workflow. Pointer fields let project settings override one field without
// erasing a user-level choice for the other.
type GoalConfig struct {
	Enabled            *bool   `json:"enabled,omitempty"`
	DefaultTokenBudget *uint64 `json:"default_token_budget,omitempty"`
}

// MCPServerConfig describes how to connect to an MCP tool server.
type MCPServerConfig struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Transport string            `json:"transport,omitempty"`
}

// DefaultMaxTurns leaves agent loops unlimited. Independent cancellation,
// context, token, prompt, retry, and recursion controls remain in force.
const DefaultMaxTurns = 0

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	enabled := true
	return &Config{
		Model:          "default",
		MaxTurns:       DefaultMaxTurns,
		PermissionMode: "default",
		AutoCompact:    true,
		Theme:          "dark",
		Goal: &GoalConfig{
			Enabled: &enabled,
		},
	}
}

// ---------- Path helpers ----------

// UserConfigDir returns the path to the user-level configuration directory (~/.claude/).
func UserConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback: use $HOME directly (should not happen in practice).
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude")
}

// UserConfigPath returns the path to the user-level settings file (~/.claude/settings.json).
func UserConfigPath() string {
	return filepath.Join(UserConfigDir(), "settings.json")
}

// ProjectConfigPath returns the path to a project-level settings file
// (<projectDir>/.claude/settings.json).
func ProjectConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "settings.json")
}

// ProjectLocalConfigPath returns the path to a project-level local settings file
// (<projectDir>/.claude/settings.local.json). This file is not committed to git.
func ProjectLocalConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "settings.local.json")
}

// ---------- Loading ----------

// LoadUserConfig loads configuration from ~/.claude/settings.json.
// If the file does not exist, a nil config and nil error are returned.
func LoadUserConfig() (*Config, error) {
	return loadConfigFromPath(UserConfigPath())
}

// LoadProjectConfig loads project-specific configuration from .claude/settings.json
// relative to the given project directory.
// If the file does not exist, a nil config and nil error are returned.
func LoadProjectConfig(projectDir string) (*Config, error) {
	config, _, _, err := loadProjectConfigFromPath(ProjectConfigPath(projectDir))
	return config, err
}

// LoadProjectLocalConfig loads project-specific local configuration from
// .claude/settings.local.json relative to the given project directory.
// This file is intended for private per-developer settings not committed to git.
// If the file does not exist, a nil config and nil error are returned.
func LoadProjectLocalConfig(projectDir string) (*Config, error) {
	config, _, _, err := loadProjectConfigFromPath(ProjectLocalConfigPath(projectDir))
	return config, err
}

// LoadEffectiveConfig loads and merges user + project configurations.
// The result is the user config with project-level overrides applied on top.
// If neither file exists, the default configuration is returned.
func LoadEffectiveConfig(projectDir string) (*Config, error) {
	sources, err := LoadConfigSources(projectDir)
	if err != nil {
		return nil, err
	}
	return sources.Effective, nil
}

// LoadConfigSources preserves user/project layers until portfolio authority is
// checked. Forbidden project portfolio values are removed before typed decode.
func LoadConfigSources(projectDir string) (*ConfigSources, error) {
	userPath := UserConfigPath()
	projectPath := ProjectConfigPath(projectDir)
	user, err := loadConfigFromPath(userPath)
	if err != nil {
		return nil, err
	}
	project, forbidden, sandboxPresent, err := loadProjectConfigFromPath(projectPath)
	if err != nil {
		return nil, err
	}
	diagnostics := []PortfolioDiagnostic(nil)
	if len(forbidden) > 0 {
		diagnostics = append(diagnostics, PortfolioDiagnostic{
			Code:    "forbidden_project_portfolio_keys",
			Level:   "warning",
			Source:  "project",
			Path:    projectPath,
			Keys:    append([]string(nil), forbidden...),
			Message: "project portfolio fields were ignored as one authority subset",
		})
	}
	sandboxDiagnostics := []SandboxDiagnostic(nil)
	if sandboxPresent {
		sandboxDiagnostics = append(sandboxDiagnostics, SandboxDiagnostic{
			Code:    "forbidden_project_sandbox_keys",
			Level:   "warning",
			Source:  "project",
			Keys:    []string{"sandbox"},
			Message: "project sandbox settings were ignored",
		})
	}
	return &ConfigSources{
		User:                 user,
		Project:              project,
		Effective:            MergeConfigs(user, project),
		UserPath:             userPath,
		ProjectPath:          projectPath,
		ProjectForbiddenKeys: append([]string(nil), forbidden...),
		Diagnostics:          diagnostics,
		SandboxDiagnostics:   sandboxDiagnostics,
	}, nil
}

// ---------- Merging ----------

// MergeConfigs merges project config over user config (project wins on conflicts).
// Nil inputs are treated as empty configs. If both are nil, the default config is returned.
func MergeConfigs(user, project *Config) *Config {
	base := DefaultConfig()

	// Apply user-level settings onto defaults.
	if user != nil {
		applyOverrides(base, user, true)
	}

	// Apply project-level settings over user settings.
	if project != nil {
		allowProjectProfile := !hasForbiddenProjectPortfolio(project)
		applyOverrides(base, project, false)
		if allowProjectProfile && project.ModelProfile != "" {
			base.ModelProfile = project.ModelProfile
		}
	}

	return base
}

// applyOverrides applies non-zero fields from src onto dst.
func applyOverrides(dst, src *Config, allowPortfolioDefinitions bool) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.APIBaseURL != "" {
		dst.APIBaseURL = src.APIBaseURL
	}
	if src.FallbackModel != "" {
		dst.FallbackModel = src.FallbackModel
	}
	if src.ModelAliases != nil {
		if dst.ModelAliases == nil {
			dst.ModelAliases = make(map[string]string)
		}
		for name, model := range src.ModelAliases {
			dst.ModelAliases[name] = model
		}
	}
	if allowPortfolioDefinitions {
		if src.ModelProfile != "" {
			dst.ModelProfile = src.ModelProfile
		}
		if src.ProviderAccounts != nil {
			dst.ProviderAccounts = cloneMap(src.ProviderAccounts)
		}
		if src.ModelProfiles != nil {
			dst.ModelProfiles = cloneMap(src.ModelProfiles)
		}
		if src.ModelRoles != nil {
			dst.ModelRoles = cloneMap(src.ModelRoles)
		}
		if src.FailoverPolicies != nil {
			dst.FailoverPolicies = cloneMap(src.FailoverPolicies)
		}
		if src.Sandbox != nil {
			dst.Sandbox = &SandboxConfig{
				GuestProfile:   src.Sandbox.GuestProfile,
				ExtraReadRoots: append([]string(nil), src.Sandbox.ExtraReadRoots...),
			}
		}
	}
	if src.maxTurnsSet || src.MaxTurns != 0 {
		dst.MaxTurns = src.MaxTurns
	}
	if src.CustomSystemPrompt != "" {
		dst.CustomSystemPrompt = src.CustomSystemPrompt
	}
	if src.PermissionMode != "" {
		dst.PermissionMode = src.PermissionMode
	}
	if src.PermissionRules != nil {
		dst.PermissionRules = src.PermissionRules
	}
	// AutoCompact: bool — only override if the source explicitly sets it.
	// We use the JSON presence to determine intent; since Go unmarshals missing
	// booleans as false, we always apply the source value when loading from a file
	// that was successfully parsed. This means explicit "false" in a project config
	// can disable the default "true".
	// For a more granular approach we would use *bool, but the spec uses plain bool.
	// We handle this by always copying the AutoCompact value from src.
	dst.AutoCompact = src.AutoCompact

	if src.MCPServers != nil {
		if dst.MCPServers == nil {
			dst.MCPServers = make(map[string]MCPServerConfig)
		}
		for k, v := range src.MCPServers {
			dst.MCPServers[k] = v
		}
	}
	if src.AllowedTools != nil {
		dst.AllowedTools = src.AllowedTools
	}
	if src.DisabledTools != nil {
		dst.DisabledTools = src.DisabledTools
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.ReducedMotion {
		dst.ReducedMotion = true
	}
	if src.Verbose {
		dst.Verbose = true
	}
	if src.Goal != nil {
		if dst.Goal == nil {
			dst.Goal = &GoalConfig{}
		}
		if src.Goal.Enabled != nil {
			enabled := *src.Goal.Enabled
			dst.Goal.Enabled = &enabled
		}
		if src.Goal.DefaultTokenBudget != nil {
			budget := *src.Goal.DefaultTokenBudget
			dst.Goal.DefaultTokenBudget = &budget
		}
	}
}

// ---------- Saving ----------

// SaveUserConfig persists the user configuration to ~/.claude/settings.json.
// The parent directory is created if it does not exist.
func SaveUserConfig(cfg *Config) error {
	dir := UserConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(UserConfigPath(), data, 0o600)
}

// ---------- Internal ----------

// loadConfigFromPath reads and unmarshals a config file.
// Returns (nil, nil) when the file does not exist.
func loadConfigFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Goal != nil &&
		cfg.Goal.DefaultTokenBudget != nil &&
		*cfg.Goal.DefaultTokenBudget == 0 {
		return nil, fmt.Errorf("goal.default_token_budget must be positive")
	}
	return &cfg, nil
}

var forbiddenProjectPortfolioKeys = map[string]struct{}{
	"provider_accounts": {},
	"model_profiles":    {},
	"model_roles":       {},
	"failover_policies": {},
}

func loadProjectConfigFromPath(path string) (*Config, []string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, nil, false, err
	}
	var forbidden []string
	for key := range forbiddenProjectPortfolioKeys {
		if _, ok := fields[key]; ok {
			forbidden = append(forbidden, key)
		}
	}
	_, sandboxPresent := fields["sandbox"]
	delete(fields, "sandbox")
	sort.Strings(forbidden)
	if len(forbidden) > 0 {
		for _, key := range forbidden {
			delete(fields, key)
		}
		delete(fields, "model_profile")
	}
	sanitized, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, false, err
	}
	var config Config
	if err := json.Unmarshal(sanitized, &config); err != nil {
		return nil, nil, false, err
	}
	if err := validateGoalConfig(&config); err != nil {
		return nil, nil, false, err
	}
	return &config, forbidden, sandboxPresent, nil
}

func validateGoalConfig(config *Config) error {
	if config.Goal != nil &&
		config.Goal.DefaultTokenBudget != nil &&
		*config.Goal.DefaultTokenBudget == 0 {
		return fmt.Errorf("goal.default_token_budget must be positive")
	}
	return nil
}

func hasForbiddenProjectPortfolio(config *Config) bool {
	return config.portfolioDefinitionSet ||
		len(config.ProviderAccounts) > 0 ||
		len(config.ModelProfiles) > 0 ||
		len(config.ModelRoles) > 0 ||
		len(config.FailoverPolicies) > 0
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// UnmarshalJSON records whether max_turns was present so an explicit zero can
// override a finite value from a lower-priority settings file.
func (c *Config) UnmarshalJSON(data []byte) error {
	type configAlias Config
	var decoded configAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["provider_accounts"]; ok {
		if err := decodeStrictJSON(raw, &decoded.ProviderAccounts); err != nil {
			return fmt.Errorf("provider_accounts: %w", err)
		}
	}
	if raw, ok := fields["model_profiles"]; ok {
		if err := decodeStrictJSON(raw, &decoded.ModelProfiles); err != nil {
			return fmt.Errorf("model_profiles: %w", err)
		}
	}
	if raw, ok := fields["model_roles"]; ok {
		if err := decodeStrictJSON(raw, &decoded.ModelRoles); err != nil {
			return fmt.Errorf("model_roles: %w", err)
		}
	}
	if raw, ok := fields["failover_policies"]; ok {
		if err := decodeStrictJSON(raw, &decoded.FailoverPolicies); err != nil {
			return fmt.Errorf("failover_policies: %w", err)
		}
	}
	if raw, ok := fields["sandbox"]; ok {
		if err := decodeStrictJSON(raw, &decoded.Sandbox); err != nil {
			return fmt.Errorf("sandbox: invalid")
		}
		if err := validateSandboxConfig(decoded.Sandbox); err != nil {
			return err
		}
	}
	*c = Config(decoded)
	_, c.maxTurnsSet = fields["max_turns"]
	for key := range forbiddenProjectPortfolioKeys {
		if _, ok := fields[key]; ok {
			c.portfolioDefinitionSet = true
			break
		}
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
