package permission

// ShadowType identifies the kind of shadowing making a rule unreachable.
type ShadowType string

const (
	ShadowTypeAsk  ShadowType = "ask"
	ShadowTypeDeny ShadowType = "deny"
)

// UnreachableRule represents a permission rule that can never fire because
// a broader rule of higher precedence already covers it.
//
// Reference: src/utils/permissions/shadowedRuleDetection.ts (234 lines)
type UnreachableRule struct {
	Rule       PermissionRule
	Reason     string
	ShadowedBy PermissionRule
	ShadowType ShadowType
	Fix        string
}

// DetectUnreachableRulesOptions configures shadowed rule detection.
type DetectUnreachableRulesOptions struct {
	SandboxAutoAllowEnabled bool
}

// IsSharedSettingSource returns true if the source is shared (visible to other users).
func IsSharedSettingSource(source string) bool {
	return source == "projectSettings" || source == "policySettings" || source == "command"
}

// DetectUnreachableRules finds allow rules that are shadowed by broader
// deny or ask rules, making them effectively unreachable.
func DetectUnreachableRules(
	allowRules, askRules, denyRules []PermissionRule,
	opts DetectUnreachableRulesOptions,
) []UnreachableRule {
	var unreachable []UnreachableRule

	for _, allowRule := range allowRules {
		if allowRule.InputPattern == "" {
			continue
		}

		if shadower, ok := findToolWideDenyRule(allowRule.ToolName, denyRules); ok {
			unreachable = append(unreachable, UnreachableRule{
				Rule:       allowRule,
				Reason:     "Blocked by \"" + shadower.ToolName + "\" deny rule (from " + shadower.Source + ")",
				ShadowedBy: shadower,
				ShadowType: ShadowTypeDeny,
				Fix:        generateShadowFixSuggestion(ShadowTypeDeny, shadower, allowRule),
			})
			continue
		}

		if shadower, ok := findToolWideAskRule(allowRule.ToolName, askRules, opts); ok {
			unreachable = append(unreachable, UnreachableRule{
				Rule:       allowRule,
				Reason:     "Shadowed by \"" + shadower.ToolName + "\" ask rule (from " + shadower.Source + ")",
				ShadowedBy: shadower,
				ShadowType: ShadowTypeAsk,
				Fix:        generateShadowFixSuggestion(ShadowTypeAsk, shadower, allowRule),
			})
		}
	}

	return unreachable
}

func findToolWideDenyRule(toolName string, denyRules []PermissionRule) (PermissionRule, bool) {
	for _, r := range denyRules {
		if r.ToolName == toolName && r.InputPattern == "" {
			return r, true
		}
	}
	return PermissionRule{}, false
}

func findToolWideAskRule(toolName string, askRules []PermissionRule, opts DetectUnreachableRulesOptions) (PermissionRule, bool) {
	for _, r := range askRules {
		if r.ToolName == toolName && r.InputPattern == "" {
			if toolName == "Bash" && opts.SandboxAutoAllowEnabled {
				if !IsSharedSettingSource(r.Source) {
					continue
				}
			}
			return r, true
		}
	}
	return PermissionRule{}, false
}

func generateShadowFixSuggestion(shadowType ShadowType, shadower, shadowed PermissionRule) string {
	if shadowType == ShadowTypeDeny {
		return "Remove the \"" + shadower.ToolName + "\" deny rule from " + shadower.Source +
			", or remove the specific allow rule from " + shadowed.Source
	}
	return "Remove the \"" + shadower.ToolName + "\" ask rule from " + shadower.Source +
		", or remove the specific allow rule from " + shadowed.Source
}
