package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PluginOption defines a configurable option declared by a plugin.
type PluginOption struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"` // "string", "boolean", "number", "select"
	Default     any      `json:"default,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Choices     []string `json:"choices,omitempty"` // for "select" type
}

// PluginConfig holds persisted configuration values for a plugin.
type PluginConfig struct {
	PluginName string         `json:"pluginName"`
	Values     map[string]any `json:"values"`
}

// PluginConfigStore manages plugin configuration persistence.
type PluginConfigStore struct {
	configDir string
}

// NewPluginConfigStore creates a store rooted at configDir.
// Each plugin's config is stored as <configDir>/<pluginName>.json.
func NewPluginConfigStore(configDir string) *PluginConfigStore {
	return &PluginConfigStore{configDir: configDir}
}

// Load reads persisted config for a plugin, returning an empty map if none exists.
func (s *PluginConfigStore) Load(pluginName string) (*PluginConfig, error) {
	path := s.configPath(pluginName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PluginConfig{PluginName: pluginName, Values: make(map[string]any)}, nil
		}
		return nil, fmt.Errorf("plugins: read config: %w", err)
	}

	var cfg PluginConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &PluginConfig{PluginName: pluginName, Values: make(map[string]any)}, nil
	}
	if cfg.Values == nil {
		cfg.Values = make(map[string]any)
	}
	cfg.PluginName = pluginName
	return &cfg, nil
}

// Save persists plugin configuration to disk.
func (s *PluginConfigStore) Save(cfg *PluginConfig) error {
	if cfg == nil || cfg.PluginName == "" {
		return fmt.Errorf("plugins: config requires a plugin name")
	}
	if err := os.MkdirAll(s.configDir, 0o700); err != nil {
		return fmt.Errorf("plugins: create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("plugins: marshal config: %w", err)
	}
	return os.WriteFile(s.configPath(cfg.PluginName), data, 0o600)
}

// Delete removes persisted config for a plugin.
func (s *PluginConfigStore) Delete(pluginName string) error {
	path := s.configPath(pluginName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("plugins: delete config: %w", err)
	}
	return nil
}

// SetOption sets a single option value and saves the config.
func (s *PluginConfigStore) SetOption(pluginName, optionName string, value any) error {
	cfg, err := s.Load(pluginName)
	if err != nil {
		return err
	}
	cfg.Values[optionName] = value
	return s.Save(cfg)
}

// GetOption returns the value of a single option, falling back to the default
// from the schema if no persisted value exists.
func (s *PluginConfigStore) GetOption(pluginName, optionName string, schema []PluginOption) any {
	cfg, err := s.Load(pluginName)
	if err != nil {
		return optionDefault(optionName, schema)
	}
	if v, ok := cfg.Values[optionName]; ok {
		return v
	}
	return optionDefault(optionName, schema)
}

// ResolveAll returns a complete map of option name -> value for a plugin,
// applying persisted values over defaults.
func (s *PluginConfigStore) ResolveAll(pluginName string, schema []PluginOption) map[string]any {
	result := make(map[string]any)
	for _, opt := range schema {
		if opt.Default != nil {
			result[opt.Name] = opt.Default
		}
	}
	cfg, err := s.Load(pluginName)
	if err == nil {
		for k, v := range cfg.Values {
			result[k] = v
		}
	}
	return result
}

// ValidateOption checks if a value is valid for the given option schema.
func ValidateOption(opt PluginOption, value any) error {
	if value == nil && opt.Required {
		return fmt.Errorf("option %q is required", opt.Name)
	}
	if value == nil {
		return nil
	}

	switch opt.Type {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("option %q must be a string", opt.Name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("option %q must be a boolean", opt.Name)
		}
	case "number":
		switch value.(type) {
		case float64, int, int64:
		default:
			return fmt.Errorf("option %q must be a number", opt.Name)
		}
	case "select":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("option %q must be a string", opt.Name)
		}
		valid := false
		for _, c := range opt.Choices {
			if c == s {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("option %q must be one of: %s", opt.Name, strings.Join(opt.Choices, ", "))
		}
	}
	return nil
}

// SubstituteVars replaces ${PLUGIN_CONFIG:<option>} placeholders in a string
// with resolved option values.
func SubstituteVars(input string, resolved map[string]any) string {
	for name, val := range resolved {
		placeholder := "${PLUGIN_CONFIG:" + name + "}"
		input = strings.ReplaceAll(input, placeholder, fmt.Sprintf("%v", val))
	}
	return input
}

func (s *PluginConfigStore) configPath(pluginName string) string {
	safe := strings.ReplaceAll(pluginName, "/", "_")
	safe = strings.ReplaceAll(safe, " ", "_")
	return filepath.Join(s.configDir, safe+".json")
}

func optionDefault(name string, schema []PluginOption) any {
	for _, opt := range schema {
		if opt.Name == name {
			return opt.Default
		}
	}
	return nil
}
