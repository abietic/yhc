package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Hook Event Types
// ---------------------------------------------------------------------------

// HookEvent represents the type of event that triggers hooks.
// Mirrors HookEvent from the reference (src/entrypoints/sdk/coreTypes.ts).
type HookEvent string

const (
	HookEventPreToolUse         HookEvent = "PreToolUse"
	HookEventPostToolUse        HookEvent = "PostToolUse"
	HookEventPostToolUseFailure HookEvent = "PostToolUseFailure"
	HookEventNotification       HookEvent = "Notification"
	HookEventUserPromptSubmit   HookEvent = "UserPromptSubmit"
	HookEventSessionStart       HookEvent = "SessionStart"
	HookEventSessionEnd         HookEvent = "SessionEnd"
	HookEventStop               HookEvent = "Stop"
	HookEventStopFailure        HookEvent = "StopFailure"
	HookEventSubagentStart      HookEvent = "SubagentStart"
	HookEventSubagentStop       HookEvent = "SubagentStop"
	HookEventPreCompact         HookEvent = "PreCompact"
	HookEventPostCompact        HookEvent = "PostCompact"
	HookEventPermissionRequest  HookEvent = "PermissionRequest"
	HookEventPermissionDenied   HookEvent = "PermissionDenied"
	HookEventSetup              HookEvent = "Setup"
	HookEventTeammateIdle       HookEvent = "TeammateIdle"
	HookEventTaskCreated        HookEvent = "TaskCreated"
	HookEventTaskCompleted      HookEvent = "TaskCompleted"
	HookEventElicitation        HookEvent = "Elicitation"
	HookEventElicitationResult  HookEvent = "ElicitationResult"
	HookEventConfigChange       HookEvent = "ConfigChange"
	HookEventWorktreeCreate     HookEvent = "WorktreeCreate"
	HookEventWorktreeRemove     HookEvent = "WorktreeRemove"
	HookEventInstructionsLoaded HookEvent = "InstructionsLoaded"
	HookEventCwdChanged         HookEvent = "CwdChanged"
	HookEventFileChanged        HookEvent = "FileChanged"
)

// AllHookEvents is the list of all recognized hook events.
// Mirrors HOOK_EVENTS from the reference.
var AllHookEvents = []HookEvent{
	HookEventPreToolUse,
	HookEventPostToolUse,
	HookEventPostToolUseFailure,
	HookEventNotification,
	HookEventUserPromptSubmit,
	HookEventSessionStart,
	HookEventSessionEnd,
	HookEventStop,
	HookEventStopFailure,
	HookEventSubagentStart,
	HookEventSubagentStop,
	HookEventPreCompact,
	HookEventPostCompact,
	HookEventPermissionRequest,
	HookEventPermissionDenied,
	HookEventSetup,
	HookEventTeammateIdle,
	HookEventTaskCreated,
	HookEventTaskCompleted,
	HookEventElicitation,
	HookEventElicitationResult,
	HookEventConfigChange,
	HookEventWorktreeCreate,
	HookEventWorktreeRemove,
	HookEventInstructionsLoaded,
	HookEventCwdChanged,
	HookEventFileChanged,
}

// IsValidHookEvent returns true if the given string is a recognized hook event.
func IsValidHookEvent(event string) bool {
	for _, e := range AllHookEvents {
		if string(e) == event {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Hook Command Types (discriminated union by Type field)
// ---------------------------------------------------------------------------

// HookCommandType is the type discriminator for hook commands.
type HookCommandType string

const (
	HookCommandTypeCommand HookCommandType = "command"
	HookCommandTypeHTTP    HookCommandType = "http"
	HookCommandTypePrompt  HookCommandType = "prompt"
	HookCommandTypeAgent   HookCommandType = "agent"
)

// HookCommand represents a single hook action to execute.
// This is a Go discriminated union via the Type field.
// Mirrors HookCommand from the reference (src/schemas/hooks.ts).
type HookCommand struct {
	// Type discriminates the hook variant: "command", "http", "prompt", "agent".
	Type HookCommandType `json:"type"`

	// --- Command hook fields ---

	// Command is the shell command to run (only for type="command").
	Command string `json:"command,omitempty"`

	// Shell specifies the shell interpreter (only for type="command").
	// Defaults to "bash" if empty.
	Shell string `json:"shell,omitempty"`

	// --- HTTP hook fields ---

	// URL is the endpoint to POST to (only for type="http").
	URL string `json:"url,omitempty"`

	// Headers are additional HTTP headers (only for type="http").
	// Values may use $VAR_NAME syntax for env var interpolation.
	Headers map[string]string `json:"headers,omitempty"`

	// AllowedEnvVars lists env var names permitted for interpolation in headers
	// (only for type="http").
	AllowedEnvVars []string `json:"allowedEnvVars,omitempty"`

	// --- Prompt/Agent hook fields ---

	// Prompt is the LLM prompt text (only for type="prompt" or type="agent").
	Prompt string `json:"prompt,omitempty"`

	// Model specifies which model to use (only for type="prompt" or type="agent").
	Model string `json:"model,omitempty"`

	// --- Common fields ---

	// If is a permission-rule-syntax condition that gates when this hook fires.
	// When set, the hook only runs if the condition matches the current context.
	If string `json:"if,omitempty"`

	// Timeout is the max execution time in seconds. 0 means use default.
	Timeout int `json:"timeout,omitempty"`

	// StatusMessage is displayed as spinner text while the hook executes.
	StatusMessage string `json:"statusMessage,omitempty"`

	// Once means the hook runs once and is removed after execution.
	Once bool `json:"once,omitempty"`

	// Async means the hook runs in the background without blocking.
	Async bool `json:"async,omitempty"`

	// AsyncRewake means the hook runs in background and rewakes the model
	// on exit code 2 (blocking error). Implies Async.
	AsyncRewake bool `json:"asyncRewake,omitempty"`
}

// TimeoutDuration returns the timeout as a time.Duration.
// Returns DefaultShellHookTimeout if Timeout is 0.
func (h *HookCommand) TimeoutDuration() time.Duration {
	if h.Timeout > 0 {
		return time.Duration(h.Timeout) * time.Second
	}
	return DefaultShellHookTimeout
}

// ---------------------------------------------------------------------------
// Hook Matcher
// ---------------------------------------------------------------------------

// HookMatcher groups hooks under a matcher pattern for a given event.
// Mirrors HookMatcher from the reference (src/schemas/hooks.ts).
type HookMatcher struct {
	// Matcher is a pattern string to match against event-specific values
	// (e.g., tool names for PreToolUse, notification types for Notification).
	// Empty string means "match all".
	Matcher string `json:"matcher,omitempty"`

	// Hooks is the list of hook commands to execute when the matcher matches.
	Hooks []HookCommand `json:"hooks"`
}

// ---------------------------------------------------------------------------
// Hooks Config (top-level settings structure)
// ---------------------------------------------------------------------------

// HooksConfig represents the full hooks configuration loaded from a JSON file.
// The top-level keys are hook event names, each mapping to an array of matchers.
// Mirrors HooksSettings from the reference (src/utils/settings/types.ts).
type HooksConfig struct {
	// Events maps each hook event to its matcher configurations.
	Events map[HookEvent][]HookMatcher
}

// ---------------------------------------------------------------------------
// Config Source
// ---------------------------------------------------------------------------

// HookSource describes where a hook definition originated.
type HookSource string

const (
	HookSourceUser    HookSource = "userSettings"
	HookSourceProject HookSource = "projectSettings"
	HookSourceLocal   HookSource = "localSettings"
	HookSourcePlugin  HookSource = "pluginHook"
	HookSourceSession HookSource = "sessionHook"
	HookSourceBuiltin HookSource = "builtinHook"
)

// ResolvedHook is a fully resolved hook ready for matching and execution.
// It combines the hook command with its event, matcher pattern, and source.
type ResolvedHook struct {
	Event   HookEvent
	Command HookCommand
	Matcher string
	Source  HookSource
}

// ---------------------------------------------------------------------------
// Config Loading
// ---------------------------------------------------------------------------

// LoadHooksConfig reads a hooks configuration from a JSON file.
// The file format mirrors .claude/settings.json's "hooks" key or
// a standalone hooks.json with the same structure.
//
// Returns an empty config (not nil) if the file does not exist.
// Returns an error if the file exists but cannot be parsed.
func LoadHooksConfig(path string) (*HooksConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &HooksConfig{Events: make(map[HookEvent][]HookMatcher)}, nil
		}
		return nil, fmt.Errorf("read hooks config %s: %w", path, err)
	}

	return ParseHooksConfig(data)
}

// ParseHooksConfig parses hooks configuration from JSON bytes.
// The JSON is a map of event names to arrays of matcher objects.
func ParseHooksConfig(data []byte) (*HooksConfig, error) {
	// Parse as generic map first to handle the event-keyed structure.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse hooks config: %w", err)
	}

	config := &HooksConfig{Events: make(map[HookEvent][]HookMatcher)}

	for eventName, matchersJSON := range raw {
		if !IsValidHookEvent(eventName) {
			// Skip unknown events gracefully (forward compatibility).
			continue
		}

		var matchers []HookMatcher
		if err := json.Unmarshal(matchersJSON, &matchers); err != nil {
			return nil, fmt.Errorf("parse hooks config event %q: %w", eventName, err)
		}

		config.Events[HookEvent(eventName)] = matchers
	}

	return config, nil
}

// LoadHooksConfigFromDir looks for hooks configuration in standard locations
// within a directory: .claude/settings.json (hooks key), .claude/hooks.json.
// Returns the merged config from all found sources.
func LoadHooksConfigFromDir(dir string) (*HooksConfig, error) {
	merged := &HooksConfig{Events: make(map[HookEvent][]HookMatcher)}

	// Try .claude/hooks.json (standalone hooks file).
	hooksPath := filepath.Join(dir, ".claude", "hooks.json")
	cfg, err := LoadHooksConfig(hooksPath)
	if err != nil {
		return nil, err
	}
	mergeHooksConfig(merged, cfg)

	// Try .claude/settings.json "hooks" key.
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings struct {
			Hooks json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(data, &settings); err == nil && len(settings.Hooks) > 0 {
			if cfg, err := ParseHooksConfig(settings.Hooks); err == nil {
				mergeHooksConfig(merged, cfg)
			}
		}
	}

	return merged, nil
}

// mergeHooksConfig merges source into dest, appending matchers per event.
func mergeHooksConfig(dest, source *HooksConfig) {
	if source == nil {
		return
	}
	for event, matchers := range source.Events {
		dest.Events[event] = append(dest.Events[event], matchers...)
	}
}

// ---------------------------------------------------------------------------
// Config Manager
// ---------------------------------------------------------------------------

// HooksConfigManager manages the hooks configuration lifecycle.
// It loads config from multiple sources, maintains a snapshot, and provides
// hook resolution for the executor.
//
// Mirrors hooksConfigManager.ts / hooksConfigSnapshot.ts from the reference.
type HooksConfigManager struct {
	// configs holds loaded configs by source.
	configs map[HookSource]*HooksConfig

	// snapshot is the current merged/resolved hook list.
	snapshot []ResolvedHook
}

// NewHooksConfigManager creates a new config manager.
func NewHooksConfigManager() *HooksConfigManager {
	return &HooksConfigManager{
		configs: make(map[HookSource]*HooksConfig),
	}
}

// LoadFromPath loads hooks configuration from a file path and registers it
// under the given source. This replaces any previously loaded config for
// that source.
func (m *HooksConfigManager) LoadFromPath(source HookSource, path string) error {
	cfg, err := LoadHooksConfig(path)
	if err != nil {
		return err
	}
	m.configs[source] = cfg
	m.rebuildSnapshot()
	return nil
}

// LoadFromDir loads hooks from a directory (checking standard locations)
// and registers them under the given source.
func (m *HooksConfigManager) LoadFromDir(source HookSource, dir string) error {
	cfg, err := LoadHooksConfigFromDir(dir)
	if err != nil {
		return err
	}
	m.configs[source] = cfg
	m.rebuildSnapshot()
	return nil
}

// SetConfig directly sets a config for a source (useful for programmatic registration).
func (m *HooksConfigManager) SetConfig(source HookSource, cfg *HooksConfig) {
	if cfg == nil {
		delete(m.configs, source)
	} else {
		m.configs[source] = cfg
	}
	m.rebuildSnapshot()
}

// GetSnapshot returns the current resolved hook list.
func (m *HooksConfigManager) GetSnapshot() []ResolvedHook {
	return m.snapshot
}

// GetHooksForEvent returns all resolved hooks for a specific event.
func (m *HooksConfigManager) GetHooksForEvent(event HookEvent) []ResolvedHook {
	var result []ResolvedHook
	for _, h := range m.snapshot {
		if h.Event == event {
			result = append(result, h)
		}
	}
	return result
}

// Refresh reloads all configs from their original sources.
// This is a simplified version — full implementation would track paths.
func (m *HooksConfigManager) Refresh() {
	m.rebuildSnapshot()
}

// rebuildSnapshot flattens all loaded configs into a single resolved hook list.
func (m *HooksConfigManager) rebuildSnapshot() {
	var resolved []ResolvedHook

	// Process sources in priority order.
	sourceOrder := []HookSource{
		HookSourceUser,
		HookSourceProject,
		HookSourceLocal,
		HookSourcePlugin,
		HookSourceSession,
		HookSourceBuiltin,
	}

	for _, source := range sourceOrder {
		cfg, ok := m.configs[source]
		if !ok || cfg == nil {
			continue
		}
		for event, matchers := range cfg.Events {
			for _, matcher := range matchers {
				for _, hook := range matcher.Hooks {
					resolved = append(resolved, ResolvedHook{
						Event:   event,
						Command: hook,
						Matcher: matcher.Matcher,
						Source:  source,
					})
				}
			}
		}
	}

	m.snapshot = resolved
}

// ---------------------------------------------------------------------------
// Event Matcher
// ---------------------------------------------------------------------------

// EventMatcher routes hook events to the appropriate handlers based on
// event type and matcher patterns. This is the Go equivalent of the
// reference's groupHooksByEventAndMatcher + getHooksForMatcher logic.
type EventMatcher struct {
	manager *HooksConfigManager
}

// NewEventMatcher creates a matcher backed by the given config manager.
func NewEventMatcher(manager *HooksConfigManager) *EventMatcher {
	return &EventMatcher{manager: manager}
}

// MatchHooks finds all hooks that match the given event and field value.
// The fieldValue is the event-specific value to match against (e.g., tool name
// for PreToolUse, notification type for Notification).
//
// Matching semantics (mirrors the reference):
//   - Empty matcher ("") matches all events of that type.
//   - Non-empty matcher must match the fieldValue using matchEventPattern.
func (em *EventMatcher) MatchHooks(event HookEvent, fieldValue string) []ResolvedHook {
	hooks := em.manager.GetHooksForEvent(event)
	var matched []ResolvedHook

	for _, h := range hooks {
		if matchEventPattern(h.Matcher, fieldValue) {
			matched = append(matched, h)
		}
	}

	return matched
}

// MatchHooksMulti finds all hooks that match the given event and any of the
// provided field values. Useful when an event can match against multiple values.
func (em *EventMatcher) MatchHooksMulti(event HookEvent, fieldValues []string) []ResolvedHook {
	hooks := em.manager.GetHooksForEvent(event)
	var matched []ResolvedHook

	for _, h := range hooks {
		if h.Matcher == "" {
			// Empty matcher matches all.
			matched = append(matched, h)
			continue
		}
		for _, val := range fieldValues {
			if matchEventPattern(h.Matcher, val) {
				matched = append(matched, h)
				break
			}
		}
	}

	return matched
}

// matchEventPattern checks if a field value matches a matcher pattern.
//
// Pattern semantics (mirrors the reference's matcher behavior):
//   - Empty pattern: matches everything.
//   - Pipe-separated values (e.g., "Read|Write"): matches if fieldValue is in the list.
//   - Glob-style with * (e.g., "Bash*"): matches using prefix/suffix.
//   - Otherwise: exact match.
func matchEventPattern(pattern, fieldValue string) bool {
	if pattern == "" {
		return true
	}

	// Pipe-separated list check.
	if strings.Contains(pattern, "|") {
		parts := strings.Split(pattern, "|")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == fieldValue {
				return true
			}
			// Also check if individual parts have wildcards.
			if strings.Contains(p, "*") && matchGlob(p, fieldValue) {
				return true
			}
		}
		return false
	}

	// Wildcard / glob matching.
	if strings.Contains(pattern, "*") {
		return matchGlob(pattern, fieldValue)
	}

	// Exact match.
	return pattern == fieldValue
}

// matchGlob provides simple glob matching with * as wildcard.
// Supports patterns like "Bash*", "*Edit", "pre*post".
func matchGlob(pattern, value string) bool {
	// Use filepath.Match for standard glob patterns.
	matched, err := filepath.Match(pattern, value)
	if err == nil && matched {
		return true
	}

	// Fallback for simple prefix/suffix patterns.
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		// *contains*
		inner := pattern[1 : len(pattern)-1]
		return strings.Contains(value, inner)
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(value, pattern[1:])
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(value, pattern[:len(pattern)-1])
	}

	return false
}
