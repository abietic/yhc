package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/config"
)

const (
	RuleSettingsRows    = 256
	RuleSettingsText    = 4096
	RuleSettingsChanges = 32
)

type RuleSettingsChange struct {
	Operation string           `json:"operation"`
	Action    PermissionAction `json:"action"`
	Rule      string           `json:"rule"`
}

type RuleSettingsRow struct {
	Action       PermissionAction `json:"action"`
	Value        string           `json:"value"`
	Format       string           `json:"format"`
	Editable     bool             `json:"editable"`
	Truncated    bool             `json:"truncated"`
	ToolName     string           `json:"tool_name"`
	InputPattern string           `json:"input_pattern"`
}

type RuleSettings struct {
	Scope       string            `json:"scope"`
	Revision    string            `json:"revision"`
	Rules       []RuleSettingsRow `json:"rules"`
	HiddenRules int               `json:"hidden_rules"`
}

func ruleSettingsPath(projectDir, scope string) (string, error) {
	if scope == "user" {
		return config.UserConfigPath(), nil
	}
	if (scope != "project" && scope != "local") || !filepath.IsAbs(projectDir) || strings.ContainsRune(projectDir, 0) {
		return "", config.ErrSettingsInvalid
	}
	if scope == "project" {
		return config.ProjectConfigPath(projectDir), nil
	}
	return config.ProjectLocalConfigPath(projectDir), nil
}

// RuleSettingsSnapshot projects only the selected file's rule configuration.
// It is not the active engine's effective-policy snapshot or a permission grant.
func RuleSettingsSnapshot(projectDir, scope string) (RuleSettings, error) {
	path, err := ruleSettingsPath(projectDir, scope)
	if err != nil {
		return RuleSettings{}, err
	}
	fields, revision, err := config.ReadSettingsDocument(path)
	if err != nil {
		return RuleSettings{}, err
	}
	return projectRuleSettings(scope, revision, fields)
}

// RuleSettingsPatch applies one atomic add/remove batch. Legacy rules and all
// unrelated values are retained. This function does not mutate live engines.
func RuleSettingsPatch(projectDir, scope, revision string, changes []RuleSettingsChange) (RuleSettings, error) {
	return RuleSettingsPatchContext(context.Background(), projectDir, scope, revision, changes)
}

func RuleSettingsPatchContext(ctx context.Context, projectDir, scope, revision string, changes []RuleSettingsChange) (RuleSettings, error) {
	path, err := ruleSettingsPath(projectDir, scope)
	if err != nil {
		return RuleSettings{}, err
	}
	if len(changes) == 0 || len(changes) > RuleSettingsChanges {
		return RuleSettings{}, config.ErrSettingsInvalid
	}
	validated := append([]RuleSettingsChange(nil), changes...)
	for i, change := range validated {
		if (change.Operation != "add" && change.Operation != "remove") || (change.Action != ActionAllow && change.Action != ActionAsk && change.Action != ActionDeny) || !utf8.ValidString(change.Rule) || utf8.RuneCountInString(change.Rule) > RuleSettingsText || strings.ContainsRune(change.Rule, 0) || strings.TrimSpace(change.Rule) == "" {
			return RuleSettings{}, config.ErrSettingsInvalid
		}
		if change.Operation == "add" {
			validated[i].Rule = strings.TrimSpace(change.Rule)
		}
	}
	var result RuleSettings
	newRevision, err := config.UpdateSettingsDocumentRevisionContext(ctx, path, revision, func(fields map[string]json.RawMessage) (bool, error) {
		if _, err := projectRuleSettings(scope, revision, fields); err != nil {
			return false, err
		}
		changed := false
		for _, change := range validated {
			didChange, err := changePermissionSettings(fields, []string{change.Rule}, change.Action, change.Operation)
			if err != nil {
				return false, err
			}
			changed = changed || didChange
		}
		var err error
		result, err = projectRuleSettings(scope, revision, fields)
		return changed, err
	})
	if err != nil {
		return RuleSettings{}, err
	}
	result.Revision = newRevision
	return result, nil
}

func projectRuleSettings(scope, revision string, fields map[string]json.RawMessage) (RuleSettings, error) {
	perms, err := strictPermissionSettingsSection(fields)
	if err != nil {
		return RuleSettings{}, err
	}
	result := RuleSettings{Scope: scope, Revision: revision, Rules: []RuleSettingsRow{}}
	appendRow := func(value string, rule PermissionRule, format string) {
		if len(result.Rules) == RuleSettingsRows {
			result.HiddenRules++
			return
		}
		bounded := func(value string) string {
			runes := []rune(value)
			return string(runes[:min(len(runes), RuleSettingsText)])
		}
		truncated := utf8.RuneCountInString(value) > RuleSettingsText
		result.Rules = append(result.Rules, RuleSettingsRow{Action: rule.Action, Value: bounded(value), Format: format, Editable: format == "structured" && !truncated && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0), Truncated: truncated, ToolName: bounded(rule.ToolName), InputPattern: bounded(rule.InputPattern)})
	}
	for _, action := range []PermissionAction{ActionAllow, ActionDeny, ActionAsk} {
		for _, value := range permissionSettingsRules(perms[string(action)]) {
			appendRow(value, parseRuleValue(value, action, scope), "structured")
		}
	}
	var legacy []string
	if raw, ok := fields["permission_rules"]; ok {
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return RuleSettings{}, config.ErrSettingsInvalid
		}
	}
	for _, value := range legacy {
		rule, _ := ParseRuleString(value)
		appendRow(value, rule, "legacy")
	}
	return result, nil
}
