package commands

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/permission"
)

func executePermissions(ctx *CommandContext, args string) (*CommandResult, error) {
	fields := append([]string(nil), ctx.Args...)
	if len(fields) == 0 {
		fields, _ = tokenize(strings.TrimSpace(args))
	}

	// Subcommand dispatch
	if len(fields) > 0 {
		switch strings.ToLower(fields[0]) {
		case "mode":
			return permissionsMode(ctx, fields[1:])
		case "bypass":
			return permissionsBypass(fields[1:])
		case "rules":
			return permissionsRules(ctx, fields[1:])
		case "add":
			return permissionsAdd(fields[1:])
		case "remove", "rm":
			return permissionsRemove(fields[1:])
		default:
			return nil, fmt.Errorf("unknown permissions operation %q\n%s", fields[0], permissionsUsage())
		}
	}

	return permissionsList(ctx)
}

type permissionModeReader interface {
	PermissionMode() permission.Mode
}

func permissionsMode(ctx *CommandContext, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{Output: currentPermissionMode(ctx)}, nil
	}
	target, err := parsePermissionMode(args[0])
	if err != nil {
		return nil, err
	}
	confirmed := false
	if target == permission.ModeBypassPermissions {
		if len(args) != 2 || !strings.EqualFold(args[1], "confirm") {
			return nil, fmt.Errorf(
				"bypassPermissions requires explicit confirmation: /permissions mode bypassPermissions confirm",
			)
		}
		confirmed = true
	} else if len(args) != 1 {
		return nil, fmt.Errorf("unexpected permission mode arguments: %s", strings.Join(args[1:], " "))
	}
	return permissionModeIntent(target, confirmed), nil
}

func permissionsBypass(args []string) (*CommandResult, error) {
	if len(args) != 1 || !strings.EqualFold(args[0], "confirm") {
		return nil, fmt.Errorf(
			"bypass mode requires explicit confirmation: /permissions bypass confirm",
		)
	}
	return permissionModeIntent(permission.ModeBypassPermissions, true), nil
}

func permissionModeIntent(mode permission.Mode, confirmed bool) *CommandResult {
	return &CommandResult{
		Output: "Applying permission mode...",
		Action: ActionChangeMode,
		Data: map[string]any{
			"mode":             string(mode),
			"bypass_confirmed": confirmed,
		},
	}
}

func parsePermissionMode(value string) (permission.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "default":
		return permission.ModeDefault, nil
	case "plan":
		return permission.ModePlan, nil
	case "acceptedits", "accept-edits":
		return permission.ModeAcceptEdits, nil
	case "dontask", "dont-ask":
		return permission.ModeDontAsk, nil
	case "auto":
		return permission.ModeAuto, nil
	case "bypasspermissions", "bypass":
		return permission.ModeBypassPermissions, nil
	default:
		return "", fmt.Errorf(
			"unsupported permission mode %q (valid: default, plan, acceptEdits, dontAsk, auto, bypassPermissions)",
			value,
		)
	}
}

func currentPermissionMode(ctx *CommandContext) string {
	mode := permission.ModeDefault
	if ctx != nil {
		if reader, ok := ctx.Engine.(permissionModeReader); ok {
			mode = reader.PermissionMode()
		}
	}
	return fmt.Sprintf("Effective permission mode: %s.", mode)
}

func permissionsRules(ctx *CommandContext, args []string) (*CommandResult, error) {
	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		if len(args) > 1 {
			return nil, fmt.Errorf("usage: /permissions rules list")
		}
		return permissionsList(ctx)
	}
	switch strings.ToLower(args[0]) {
	case "add":
		return permissionsAdd(args[1:])
	case "remove", "rm":
		return permissionsRemove(args[1:])
	default:
		return nil, fmt.Errorf("unknown permission rules operation %q", args[0])
	}
}

func permissionsList(ctx *CommandContext) (*CommandResult, error) {
	cwd := ""
	if ctx != nil {
		cwd = ctx.CWD
	}
	rules, err := permission.LoadPermissionRules(cwd)
	if err != nil {
		return nil, fmt.Errorf("load permission rules: %w", err)
	}

	if len(rules) == 0 {
		return &CommandResult{Output: currentPermissionMode(ctx) + "\nNo permission rules configured.\n\nUse /permissions rules add <allow|deny|ask> \"Rule\" to add rules."}, nil
	}

	// Group by action
	var allow, deny, ask []permission.PermissionRule
	for _, r := range rules {
		switch r.Action {
		case permission.ActionAllow:
			allow = append(allow, r)
		case permission.ActionDeny:
			deny = append(deny, r)
		case permission.ActionAsk:
			ask = append(ask, r)
		}
	}

	var sb strings.Builder
	sb.WriteString(currentPermissionMode(ctx))
	sb.WriteString("\n\n")
	sb.WriteString("Permission Rules\n")
	sb.WriteString("================\n\n")

	writeRuleSection(&sb, "Allow", allow)
	writeRuleSection(&sb, "Deny", deny)
	writeRuleSection(&sb, "Ask", ask)

	fmt.Fprintf(&sb, "Total: %d rule(s)\n", len(rules))

	return &CommandResult{Output: sb.String()}, nil
}

func writeRuleSection(sb *strings.Builder, label string, rules []permission.PermissionRule) {
	fmt.Fprintf(sb, "%s (%d):\n", label, len(rules))
	if len(rules) == 0 {
		sb.WriteString("  (none)\n")
	}
	for _, r := range rules {
		ruleStr := permission.FormatRuleString(r.ToolName, r.InputPattern)
		fmt.Fprintf(sb, "  %-40s [%s]\n", ruleStr, sourceLabel(r.Source))
	}
	sb.WriteString("\n")
}

func sourceLabel(source string) string {
	switch source {
	case permission.SourceProject:
		return "project"
	case permission.SourceLocal:
		return "local"
	case permission.SourceUser:
		return "user"
	default:
		return source
	}
}

// permissionsAdd handles "/permissions add <action> <rule> [--user|--project|--local]"
func permissionsAdd(args []string) (*CommandResult, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("usage: /permissions rules add <allow|deny|ask> \"Rule\" [--user|--project|--local]")
	}

	action, err := parsePermAction(args[0])
	if err != nil {
		return nil, err
	}

	// Collect rule and destination flag
	var rule string
	dest := permission.DestLocalSettings
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--user":
			dest = permission.DestUserSettings
		case "--project":
			dest = permission.DestProjectSettings
		case "--local":
			dest = permission.DestLocalSettings
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown permission destination %q", args[i])
			}
			if rule != "" {
				return nil, fmt.Errorf("permission rule must be one quoted argument")
			}
			rule = strings.TrimSpace(args[i])
		}
	}
	if rule == "" {
		return nil, fmt.Errorf("permission rule is required")
	}

	return &CommandResult{
		Output: fmt.Sprintf("Added %s rule: %s (to %s settings)", action, rule, dest),
		Action: ActionPermissions,
		Data: map[string]any{
			"operation":   "add",
			"rule_action": string(action),
			"rule":        rule,
			"destination": string(dest),
		},
	}, nil
}

// permissionsRemove handles "/permissions remove <action> <rule> [--user|--project|--local]"
func permissionsRemove(args []string) (*CommandResult, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("usage: /permissions rules remove <allow|deny|ask> \"Rule\" [--user|--project|--local]")
	}

	action, err := parsePermAction(args[0])
	if err != nil {
		return nil, err
	}

	var rule string
	dest := permission.DestLocalSettings
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--user":
			dest = permission.DestUserSettings
		case "--project":
			dest = permission.DestProjectSettings
		case "--local":
			dest = permission.DestLocalSettings
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown permission destination %q", args[i])
			}
			if rule != "" {
				return nil, fmt.Errorf("permission rule must be one quoted argument")
			}
			rule = strings.TrimSpace(args[i])
		}
	}
	if rule == "" {
		return nil, fmt.Errorf("permission rule is required")
	}

	return &CommandResult{
		Output: fmt.Sprintf("Removed %s rule: %s (from %s settings)", action, rule, dest),
		Action: ActionPermissions,
		Data: map[string]any{
			"operation":   "remove",
			"rule_action": string(action),
			"rule":        rule,
			"destination": string(dest),
		},
	}, nil
}

func parsePermAction(s string) (permission.PermissionAction, error) {
	switch strings.ToLower(s) {
	case "allow":
		return permission.ActionAllow, nil
	case "deny":
		return permission.ActionDeny, nil
	case "ask":
		return permission.ActionAsk, nil
	default:
		return "", fmt.Errorf("invalid action %q; must be: allow, deny, or ask", s)
	}
}

func permissionsUsage() string {
	return `Usage:
  /permissions                                           Show effective mode and rules
  /permissions mode <mode>                               Change a typed mode
  /permissions bypass confirm                            Enter bypass mode after explicit confirmation
  /permissions rules list                                List rules
  /permissions rules add <allow|deny|ask> "Rule"        Add a rule
  /permissions rules remove <allow|deny|ask> "Rule"     Remove a rule

Options:
  --user      Write to user settings (~/.claude/settings.json)
  --project   Write to project settings (.claude/settings.json)
  --local     Write to local settings (.claude/settings.local.json) [default]

Examples:
  /permissions mode plan
  /permissions mode default
  /permissions bypass confirm
  /permissions rules add allow "Bash(npm *)"
  /permissions rules add deny "Bash(rm -rf *)" --project
  /permissions rules remove allow "Read"`
}
