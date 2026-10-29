package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	engineconfig "github.com/abietic/yhc/engine/config"
)

// SupportedConfigSettings describes the shared editor schema, not the legacy
// state importer. Execution derives its allowlist from the settings owner.
var SupportedConfigSettings = sharedConfigDefinitions()

// ConfigSettingDef describes one field from the shared settings owner.
type ConfigSettingDef struct {
	Type    string
	Desc    string
	Options []string
}

func sharedConfigDefinitions() map[string]ConfigSettingDef {
	result := make(map[string]ConfigSettingDef)
	for key, field := range engineconfig.SettingsSchema() {
		result[key] = ConfigSettingDef{Type: field.Type, Desc: field.Description, Options: field.Options}
	}
	return result
}

// ConfigTool uses the same non-secret, persisted defaults as TUI/Desktop/Web.
// The execution host supplies the project directory; input never supplies paths.
// Changes do not rebind an already running session's model or permission mode.
func ConfigTool() ToolImpl {
	keys := make([]string, 0)
	for key := range engineconfig.SettingsSchema() {
		keys = append(keys, key)
	}
	keys = append(keys, "permissions.defaultMode", "provider.baseURL")
	sort.Strings(keys)
	// ParameterInfo cannot describe a scalar union. Use Eino's JSON Schema
	// form so canonical admission accepts bool/int/null, not only SDK dispatch.
	parameters, _ := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"setting":  {Type: schema.String, Desc: "Shared setting key: " + strings.Join(keys, ", ") + ". Omitted only for action=list."},
		"key":      {Type: schema.String, Desc: "Legacy alias for setting; conflicting keys are rejected."},
		"scope":    {Type: schema.String, Desc: "Storage scope: user or project; defaults to project."},
		"action":   {Type: schema.String, Desc: "Optional operation: get, set or list; otherwise inferred from value presence."},
		"revision": {Type: schema.String, Desc: "Optional revision from a previous read; stale writes fail without retry."},
	}).ToJSONSchema()
	parameters.Properties.Set("value", &jsonschema.Schema{
		Description: "New string, boolean or integer value. Native integers are limited to 9007199254740991. Boolean/integer JSON text is also accepted and preserves larger integers exactly. Omit to read; JSON null resets the override.",
		AnyOf:       []*jsonschema.Schema{{Type: "string"}, {Type: "boolean"}, {Type: "integer"}, {Type: "null"}},
	})
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name:        "Config",
			Desc:        "Read or change shared non-secret defaults for new sessions. Scope defaults to project; select user explicitly for user-wide defaults. Omit value to read; null removes an override. List returns only editable settings and a revision. Credentials, permission rules, hooks, compaction and memory use their dedicated configuration owners. Current-session execution settings are unchanged.",
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parameters),
		},
		ValidateInput: validateConfigInput,
		Execute:       executeConfig,
		ExecuteCtx:    executeConfigContext,
	}
}

type configRequest struct {
	setting, action, scope, revision string
	value                            json.RawMessage
}

func invalidConfigRequest() error {
	return fmt.Errorf("%w: Config requires a shared setting, user/project scope, and a compatible operation", engineconfig.ErrSettingsInvalid)
}

func parseConfigRequest(input string) (configRequest, error) {
	if len(input) > 16<<10 {
		return configRequest{}, invalidConfigRequest()
	}
	object, err := decodeStrictJSONObject([]byte(input))
	if err != nil {
		return configRequest{}, invalidConfigRequest()
	}
	request := configRequest{scope: "project"}
	var key string
	for name, raw := range object {
		var target *string
		switch name {
		case "setting":
			target = &request.setting
		case "key":
			target = &key
		case "action":
			target = &request.action
		case "scope":
			target = &request.scope
		case "revision":
			target = &request.revision
		case "value":
			request.value = raw
			continue
		default:
			return configRequest{}, invalidConfigRequest()
		}
		if isJSONNull(raw) || json.Unmarshal(raw, target) != nil {
			return configRequest{}, invalidConfigRequest()
		}
	}
	request.setting, key = canonicalConfigKey(request.setting), canonicalConfigKey(key)
	if key != "" && request.setting != "" && key != request.setting {
		return configRequest{}, invalidConfigRequest()
	}
	if request.setting == "" {
		request.setting = key
	}
	if request.action == "" {
		request.action = "get"
		if request.value != nil {
			request.action = "set"
		}
	}
	if request.scope != "user" && request.scope != "project" {
		return configRequest{}, invalidConfigRequest()
	}
	if request.action == "list" {
		// Preserve the old list placeholder without interpreting it as a key.
		if (request.setting != "" && request.setting != "_") || request.value != nil || request.revision != "" {
			return configRequest{}, invalidConfigRequest()
		}
		return request, nil
	}
	field, supported := engineconfig.SettingsSchema()[request.setting]
	if !supported {
		return configRequest{}, fmt.Errorf("%w: Config supports only shared non-secret defaults; configure credentials, permission rules, hooks, compaction and memory through their dedicated owners", engineconfig.ErrSettingsInvalid)
	}
	if request.action != "get" && request.action != "set" || request.action == "get" && (request.value != nil || request.revision != "") || request.action == "set" && request.value == nil {
		return configRequest{}, invalidConfigRequest()
	}
	// String-shaped model parameters remain compatible for boolean/integer
	// fields; the shared service remains the sole business-validation owner.
	if request.action == "set" && field.Type != "string" && !isJSONNull(request.value) {
		var text string
		if json.Unmarshal(request.value, &text) == nil {
			request.value = json.RawMessage(strings.TrimSpace(text))
		} else if field.Type == "integer" {
			// Canonical tool admission represents native JSON numbers as
			// float64. Reject unsafe integers at both entrypoints rather than
			// silently saving a rounded value; string form stays exact.
			const maxSafeInteger = 1<<53 - 1
			var number int64
			if json.Unmarshal(request.value, &number) == nil && (number > maxSafeInteger || number < -maxSafeInteger) {
				return configRequest{}, fmt.Errorf("%w: Config native integers exceed the safe transport range; use string-form JSON for an exact integer", engineconfig.ErrSettingsInvalid)
			}
		}
	}
	return request, nil
}

func canonicalConfigKey(key string) string {
	switch key {
	case "permissions.defaultMode":
		return "permission_mode"
	case "provider.baseURL":
		return "api_base_url"
	default:
		return key
	}
}

func validateConfigInput(input map[string]any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return invalidConfigRequest()
	}
	_, err = parseConfigRequest(string(encoded))
	return err
}

func executeConfig(input string) (string, error) {
	return executeConfigContext(context.Background(), input)
}

func executeConfigContext(ctx context.Context, input string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request, err := parseConfigRequest(input)
	if err != nil {
		return "", err
	}
	project, err := effectiveExecutionCWD(ctx)
	if err != nil {
		return "", engineconfig.ErrSettingsUnsafePath
	}
	snapshot, err := engineconfig.SettingsSnapshot(project, request.scope)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if request.action == "set" {
		revision := request.revision
		if revision == "" {
			revision = snapshot.Revision
		}
		snapshot, err = engineconfig.SettingsPatchContext(ctx, project, request.scope, revision, map[string]json.RawMessage{request.setting: request.value})
		if err != nil {
			return "", err
		}
	}
	result := map[string]any{
		"success": true, "operation": request.action, "scope": request.scope,
		"revision": snapshot.Revision, "apply": "new_session",
	}
	if request.action == "list" {
		result["fields"] = snapshot.Fields
	} else {
		field := snapshot.Fields[request.setting]
		result["setting"], result["value"], result["field"] = request.setting, field.EffectiveValue, field
		if request.action == "set" {
			result["newValue"] = field.StoredValue
		}
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", invalidConfigRequest()
	}
	return string(encoded), nil
}
