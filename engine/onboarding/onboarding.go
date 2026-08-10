// Package onboarding provides the first-run setup flow for YHC.
//
// It detects whether the user has completed initial configuration and guides
// them through API key setup, model selection, permission preferences, and
// project scaffolding.
package onboarding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/config"
)

// OnboardingState captures the current setup status of the user's environment.
type OnboardingState struct {
	IsFirstRun bool
	HasAPIKey  bool
	HasConfig  bool
	ConfigDir  string
	NeedsSetup []string // list of things that need configuration
}

// OnboardingStep represents a single step in the onboarding flow.
type OnboardingStep struct {
	ID          string
	Title       string
	Description string
	Required    bool
	Completed   bool
}

// CheckOnboardingNeeded determines if the user needs to go through setup.
func CheckOnboardingNeeded() *OnboardingState {
	configDir := config.UserConfigDir()

	state := &OnboardingState{
		ConfigDir: configDir,
	}

	// Check if config directory exists.
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		state.IsFirstRun = true
		state.NeedsSetup = append(state.NeedsSetup, "config_directory")
	}

	// Check if settings.json exists.
	settingsPath := config.UserConfigPath()
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		state.NeedsSetup = append(state.NeedsSetup, "settings")
	} else {
		state.HasConfig = true
	}

	// Check if API key is configured.
	if GetAPIKey() != "" {
		state.HasAPIKey = true
	} else {
		state.NeedsSetup = append(state.NeedsSetup, "api_key")
	}

	return state
}

// GetOnboardingSteps returns the list of onboarding steps based on current state.
func GetOnboardingSteps(state *OnboardingState) []OnboardingStep {
	steps := []OnboardingStep{
		{
			ID:          "api_key",
			Title:       "API Key Configuration",
			Description: "Configure your Anthropic API key to enable model access.",
			Required:    true,
			Completed:   state.HasAPIKey,
		},
		{
			ID:          "model_selection",
			Title:       "Model Selection",
			Description: "Choose your default model (default: claude-sonnet-4-20250514).",
			Required:    false,
			Completed:   state.HasConfig,
		},
		{
			ID:          "permission_mode",
			Title:       "Permission Mode",
			Description: "Set the default permission mode for tool execution (default: \"default\").",
			Required:    false,
			Completed:   state.HasConfig,
		},
		{
			ID:          "claude_md",
			Title:       "Create Project CLAUDE.md",
			Description: "Create a CLAUDE.md template in the current project for project-specific instructions.",
			Required:    false,
			Completed:   false,
		},
	}

	return steps
}

// SetupConfigDirectory creates the ~/.claude/ directory structure.
func SetupConfigDirectory() error {
	configDir := config.UserConfigDir()

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", configDir, err)
	}

	return nil
}

// SetupAPIKey stores the API key in the configuration.
func SetupAPIKey(apiKey string) error {
	if err := ValidateAPIKey(apiKey); err != nil {
		return err
	}

	// Ensure the config directory exists.
	if err := SetupConfigDirectory(); err != nil {
		return err
	}

	// Store the API key in a dedicated credentials file.
	credPath := filepath.Join(config.UserConfigDir(), "credentials.json")

	creds := map[string]string{
		"anthropic_api_key": apiKey,
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

// SetupDefaultModel sets the default model preference.
func SetupDefaultModel(model string) error {
	if model == "" {
		return errors.New("model name cannot be empty")
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("failed to load user config: %w", err)
	}

	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	cfg.Model = model

	if err := config.SaveUserConfig(cfg); err != nil {
		return fmt.Errorf("failed to save user config: %w", err)
	}

	return nil
}

// SetupPermissionMode sets the default permission mode.
func SetupPermissionMode(mode string) error {
	if mode == "" {
		return errors.New("permission mode cannot be empty")
	}

	validModes := map[string]bool{
		"default":    true,
		"permissive": true,
		"strict":     true,
	}

	if !validModes[mode] {
		return fmt.Errorf("invalid permission mode %q: must be one of default, permissive, strict", mode)
	}

	cfg, err := config.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("failed to load user config: %w", err)
	}

	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	cfg.PermissionMode = mode

	if err := config.SaveUserConfig(cfg); err != nil {
		return fmt.Errorf("failed to save user config: %w", err)
	}

	return nil
}

// CreateClaudeMdTemplate creates a template CLAUDE.md in the current project.
func CreateClaudeMdTemplate(projectDir string) error {
	if projectDir == "" {
		return errors.New("project directory cannot be empty")
	}

	claudeMdPath := filepath.Join(projectDir, "CLAUDE.md")

	// Don't overwrite an existing CLAUDE.md.
	if _, err := os.Stat(claudeMdPath); err == nil {
		return fmt.Errorf("CLAUDE.md already exists at %s", claudeMdPath)
	}

	template := `# CLAUDE.md

## Project Overview

<!-- Describe your project here -->

## Code Style

<!-- Describe your code style preferences -->

## Architecture

<!-- Describe the high-level architecture -->

## Important Patterns

<!-- Describe important patterns or conventions used in this project -->

## Testing

<!-- Describe how to run tests and testing conventions -->

## Common Commands

<!-- List frequently used commands -->
` + "```" + `bash
# Build
# go build ./...

# Test
# go test ./...

# Lint
# golangci-lint run
` + "```" + `
`

	if err := os.WriteFile(claudeMdPath, []byte(template), 0o644); err != nil {
		return fmt.Errorf("failed to create CLAUDE.md: %w", err)
	}

	return nil
}

// ValidateAPIKey checks if an API key looks valid (basic format check).
func ValidateAPIKey(key string) error {
	if key == "" {
		return errors.New("API key cannot be empty")
	}

	if len(key) < 20 {
		return fmt.Errorf("API key is too short (got %d characters, minimum 20)", len(key))
	}

	// Check for known prefixes.
	knownPrefixes := []string{"sk-ant-"}
	hasKnownPrefix := false
	for _, prefix := range knownPrefixes {
		if strings.HasPrefix(key, prefix) {
			hasKnownPrefix = true
			break
		}
	}

	if !hasKnownPrefix {
		return fmt.Errorf("API key has unrecognized prefix: expected one of %v", knownPrefixes)
	}

	return nil
}

// GetAPIKey returns the configured API key from env or config.
// Priority: ANTHROPIC_API_KEY env > config file.
func GetAPIKey() string {
	// First, check environment variable.
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return key
	}

	// Second, check credentials file.
	credPath := filepath.Join(config.UserConfigDir(), "credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return ""
	}

	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		return ""
	}

	return creds["anthropic_api_key"]
}
