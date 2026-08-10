package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statepath"
)

// SupportedConfigSettings lists the known config settings with their types.
// Reference: src/tools/ConfigTool/supportedSettings.ts
var SupportedConfigSettings = map[string]ConfigSettingDef{
	"model":                   {Type: "string", Desc: "The default model to use"},
	"theme":                   {Type: "string", Desc: "UI theme", Options: []string{"dark", "light", "auto"}},
	"permissions.defaultMode": {Type: "string", Desc: "Default permission mode", Options: []string{"default", "acceptEdits", "bypassPermissions", "plan", "auto"}},
	"permissions.allow":       {Type: "array", Desc: "Allowed permission rules"},
	"permissions.deny":        {Type: "array", Desc: "Denied permission rules"},
	"compact.threshold":       {Type: "number", Desc: "Token threshold for auto-compaction"},
	"compact.strategy":        {Type: "string", Desc: "Compaction strategy", Options: []string{"auto", "manual"}},
	"hooks.preToolUse":        {Type: "array", Desc: "Pre-tool-use hook commands"},
	"hooks.postToolUse":       {Type: "array", Desc: "Post-tool-use hook commands"},
	"memory.enabled":          {Type: "boolean", Desc: "Enable memory system"},
	"provider":                {Type: "string", Desc: "LLM provider name"},
	"provider.apiKey":         {Type: "string", Desc: "Provider API key"},
	"provider.baseURL":        {Type: "string", Desc: "Provider base URL"},
}

// ConfigSettingDef describes a known config setting.
type ConfigSettingDef struct {
	Type    string // "string", "boolean", "number", "array"
	Desc    string
	Options []string // Valid values (empty = any value)
}

// ConfigTool returns a tool that reads and writes agent configuration settings.
//
// Reference: src/tools/ConfigTool/ConfigTool.ts (467 lines)
// Supports both the reference schema (setting/value) and legacy schema (action/key/value).
func ConfigTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Config",
			Desc: `Read or write agent configuration settings. Provide a setting key to get its current value. Provide both setting and value to change it.

Supported settings: model, theme, permissions.defaultMode, permissions.allow, permissions.deny, compact.threshold, compact.strategy, hooks.preToolUse, hooks.postToolUse, memory.enabled, provider, provider.apiKey, provider.baseURL`,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"setting": {Type: schema.String, Desc: "The setting key (e.g., 'model', 'permissions.defaultMode')", Required: true},
				"value":   {Type: schema.String, Desc: "The new value. Omit to get current value."},
			}),
		},
		ValidateInput: validateConfigInput,
		Execute:       executeConfig,
	}
}

func validateConfigInput(input map[string]any) error {
	setting, _ := input["setting"].(string)
	if setting == "" {
		// Try legacy 'key' field
		setting, _ = input["key"].(string)
	}
	if strings.TrimSpace(setting) == "" {
		return fmt.Errorf("'setting' is required")
	}
	return nil
}

func executeConfig(input string) (string, error) {
	var params struct {
		Setting string           `json:"setting"`
		Value   *json.RawMessage `json:"value,omitempty"`
		// Legacy fields
		Action string `json:"action"`
		Key    string `json:"key"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid Config input: %w", err)
	}

	// Support legacy action/key schema
	setting := params.Setting
	if setting == "" {
		setting = params.Key
	}

	configDir, err := resolveConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}

	// Legacy "list" action
	if params.Action == "list" {
		return listConfig(configDir)
	}

	if setting == "" {
		return "", fmt.Errorf("'setting' is required")
	}

	// GET operation (no value provided)
	if params.Value == nil && params.Action != "set" {
		val, err := getConfigValue(configDir, setting)
		if err != nil {
			return "", err
		}
		result := map[string]any{
			"success":   true,
			"operation": "get",
			"setting":   setting,
			"value":     val,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data), nil
	}

	// SET operation
	var valueStr string
	if params.Value != nil {
		valueStr = string(*params.Value)
		// Unquote string values
		var s string
		if err := json.Unmarshal(*params.Value, &s); err == nil {
			valueStr = s
		}
	}

	// Validate against known settings
	if def, known := SupportedConfigSettings[setting]; known {
		if def.Type == "boolean" {
			lower := strings.ToLower(strings.TrimSpace(valueStr))
			if lower != "true" && lower != "false" {
				return "", fmt.Errorf("%s requires true or false", setting)
			}
		}
		if len(def.Options) > 0 {
			valid := false
			for _, opt := range def.Options {
				if strings.EqualFold(opt, valueStr) {
					valid = true
					break
				}
			}
			if !valid {
				return "", fmt.Errorf("invalid value %q for %s. Options: %s", valueStr, setting, strings.Join(def.Options, ", "))
			}
		}
	}

	result, err := setConfigValue(configDir, setting, valueStr)
	if err != nil {
		return "", err
	}
	output := map[string]any{
		"success":   true,
		"operation": "set",
		"setting":   setting,
		"newValue":  result,
	}
	data, _ := json.MarshalIndent(output, "", "  ")
	return string(data), nil
}

func resolveConfigDir() (configDirResolution, error) {
	pair := identity.RuntimeEnvConfigDir.Pair()
	value, _, present := identity.LookupEnv(pair)
	if present && value != "" {
		cwd, err := os.Getwd()
		if err != nil {
			return configDirResolution{}, errors.New("config root is unavailable")
		}
		defaults, err := statepath.ProjectRoots(cwd)
		if err != nil {
			return configDirResolution{}, errors.New("config root is unavailable")
		}
		selection, err := statepath.ResolveOverride(pair, defaults)
		if err != nil {
			return configDirResolution{}, errors.New("config root is invalid")
		}
		return configDirResolution{path: selection.Effective}, nil
	}

	defaults, err := defaultConfigRoots()
	if err != nil {
		return configDirResolution{}, err
	}
	selection, err := statepath.ResolveOverride(pair, defaults)
	if err != nil {
		return configDirResolution{}, errors.New("config root is invalid")
	}
	return configDirResolution{
		path:             selection.Effective,
		canonicalDefault: true,
	}, nil
}

func defaultConfigRoots() (statepath.Roots, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return statepath.Roots{}, errors.New("config root is unavailable")
	}
	projectRoots, err := statepath.ProjectRoots(cwd)
	if err != nil {
		return statepath.Roots{}, errors.New("config root is unavailable")
	}
	if exists, err := safeConfigRootExists(projectRoots.Canonical); err != nil {
		return statepath.Roots{}, err
	} else if exists {
		return projectRoots, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return statepath.Roots{}, errors.New("config root is unavailable")
	}
	userRoots, err := statepath.UserRoots(home)
	if err != nil {
		return statepath.Roots{}, errors.New("config root is unavailable")
	}
	if _, err := safeConfigRootExists(userRoots.Canonical); err != nil {
		return statepath.Roots{}, err
	}
	return userRoots, nil
}

func safeConfigRootExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("config root is invalid")
	}
	return true, nil
}

func listConfig(configDir configDirResolution) (string, error) {
	store, exists, err := openConfigStore(configDir, false)
	if err != nil {
		return "", err
	}
	if !exists {
		return "{}", nil
	}
	defer store.Close() //nolint:errcheck
	data, exists, err := store.readSettings()
	if err != nil {
		return "", fmt.Errorf("read settings: %w", err)
	}
	if !exists {
		return "{}", nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return string(data), nil
	}
	pretty, _ := json.MarshalIndent(raw, "", "  ")
	return string(pretty), nil
}

func getConfigValue(configDir configDirResolution, key string) (any, error) {
	store, exists, err := openConfigStore(configDir, false)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("key %q not found (no settings file)", key)
	}
	defer store.Close() //nolint:errcheck
	data, exists, err := store.readSettings()
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("key %q not found (no settings file)", key)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	val := getNestedValue(raw, strings.Split(key, "."))
	if val == nil {
		return nil, fmt.Errorf("key %q not found", key)
	}
	return val, nil
}

func setConfigValue(configDir configDirResolution, key, value string) (any, error) {
	store, _, err := openConfigStore(configDir, true)
	if err != nil {
		return nil, fmt.Errorf("open config directory: %w", err)
	}
	defer store.Close() //nolint:errcheck
	return setConfigValueInStore(store, key, value)
}

func setConfigValueInStore(store *pinnedConfigStore, key, value string) (any, error) {
	var raw map[string]any
	data, exists, err := store.readSettings()
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	if exists {
		if err := json.Unmarshal(data, &raw); err != nil {
			raw = make(map[string]any)
		}
	} else {
		raw = make(map[string]any)
	}

	var parsedValue any
	if err := json.Unmarshal([]byte(value), &parsedValue); err != nil {
		parsedValue = value
	}

	setNestedValue(raw, strings.Split(key, "."), parsedValue)

	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode settings: %w", err)
	}
	if err := store.writeSettings(encoded); err != nil {
		return nil, fmt.Errorf("write settings: %w", err)
	}
	return parsedValue, nil
}

func getNestedValue(m map[string]any, keys []string) any {
	if len(keys) == 0 {
		return nil
	}
	val, ok := m[keys[0]]
	if !ok {
		return nil
	}
	if len(keys) == 1 {
		return val
	}
	nested, ok := val.(map[string]any)
	if !ok {
		return nil
	}
	return getNestedValue(nested, keys[1:])
}

func setNestedValue(m map[string]any, keys []string, value any) {
	if len(keys) == 0 {
		return
	}
	if len(keys) == 1 {
		m[keys[0]] = value
		return
	}
	nested, ok := m[keys[0]].(map[string]any)
	if !ok {
		nested = make(map[string]any)
		m[keys[0]] = nested
	}
	setNestedValue(nested, keys[1:], value)
}
