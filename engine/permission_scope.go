package engine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/permission"
)

func permissionInvocation(cwd, toolName string, input map[string]any) (command, path, fingerprint string) {
	if toolName == "Bash" {
		return strings.TrimSpace(stringInput(input, "command")), "", ""
	}
	if rawPath := toolPath(toolName, input, cwd); rawPath != "" {
		return "", permission.ResolvePermissionPath(rawPath, cwd).Effective(), ""
	}
	return "", "", canonicalInput(input)
}

func sessionApprovalKey(cwd, toolName string, input map[string]any) (permission.ApprovalKey, string, error) {
	key := permission.ApprovalKey{ToolName: toolName}
	switch toolName {
	case "Bash":
		command := strings.TrimSpace(stringInput(input, "command"))
		if command == "" {
			return key, "", fmt.Errorf("cannot approve Bash without a command")
		}
		key.CommandPattern = command
		key.ExactCommand = true
		return key, fmt.Sprintf("only Bash command %q", command), nil
	case "Read":
		path := canonicalPath(cwd, stringInput(input, "file_path"))
		if path == "" {
			return key, "", fmt.Errorf("cannot approve Read without a file path")
		}
		dir := filepath.Dir(path)
		key.PathPattern = dir
		key.RecursivePath = true
		return key, fmt.Sprintf("Read under %s", dir), nil
	case "Grep", "Glob":
		path := canonicalPath(cwd, stringInput(input, "path"))
		if path == "" {
			path = canonicalPath(cwd, cwd)
		}
		key.PathPattern = path
		key.RecursivePath = true
		return key, fmt.Sprintf("%s searches under %s", toolName, path), nil
	case "Write", "Edit":
		path := canonicalPath(cwd, stringInput(input, "file_path"))
		if path == "" {
			return key, "", fmt.Errorf("cannot approve %s without a file path", toolName)
		}
		key.PathPattern = path
		key.ExactPath = true
		return key, fmt.Sprintf("only %s on %s", toolName, path), nil
	case "NotebookEdit":
		path := canonicalPath(cwd, stringInput(input, "notebook_path"))
		if path == "" {
			return key, "", fmt.Errorf("cannot approve NotebookEdit without a notebook path")
		}
		key.PathPattern = path
		key.ExactPath = true
		return key, fmt.Sprintf("only NotebookEdit on %s", path), nil
	default:
		fingerprint := canonicalInput(input)
		if fingerprint == "" {
			return key, "", fmt.Errorf("cannot create an unscoped approval for %s", toolName)
		}
		key.InputFingerprint = fingerprint
		return key, "this exact tool input", nil
	}
}

func isAllowedWorkingDirectoryRead(roots []string, toolName string, input map[string]any) bool {
	if toolName != "Read" && toolName != "Grep" && toolName != "Glob" {
		return false
	}
	if len(roots) == 0 {
		return false
	}
	requested := toolPath(toolName, input, roots[0])
	if requested == "" {
		return false
	}
	return permission.PermissionPathsWithinRoots(permission.ResolvePermissionPath(requested, roots[0]), roots)
}

func evaluatePermissionRuleDecision(
	rules *permission.RulesEngine,
	cwd string,
	requestedToolName string,
	canonicalToolName string,
	input map[string]any,
) permission.RuleDecision {
	if rules == nil {
		return permission.RuleDecision{Action: permission.ActionAsk}
	}
	toolNames := []string{requestedToolName}
	if canonicalToolName != "" && canonicalToolName != requestedToolName {
		toolNames = append(toolNames, canonicalToolName)
	}
	inputs := []map[string]any{cloneInputMap(input)}
	if canonicalToolName == "Bash" {
		if command := strings.TrimSpace(stringInput(input, "command")); command != "" &&
			command != stringInput(input, "command") {
			normalizedInput := cloneInputMap(input)
			normalizedInput["command"] = command
			inputs = append(inputs, normalizedInput)
		}
	}
	rawPath := toolPath(canonicalToolName, input, cwd)
	if rawPath != "" {
		key := "path"
		switch canonicalToolName {
		case "Read", "Write", "Edit":
			key = "file_path"
		case "NotebookEdit":
			key = "notebook_path"
		}
		for _, resolvedPath := range permission.ResolvePermissionPath(rawPath, cwd).Paths {
			resolvedInput := cloneInputMap(input)
			resolvedInput[key] = resolvedPath
			inputs = append(inputs, resolvedInput)
		}
	}

	var winner permission.RuleDecision
	for _, toolName := range toolNames {
		for _, candidateInput := range inputs {
			decision := rules.EvaluateDecision(toolName, candidateInput)
			if !decision.Matched {
				continue
			}
			if !winner.Matched || permissionRuleDecisionBetter(decision, winner) {
				winner = decision
			}
		}
	}
	if !winner.Matched {
		return permission.RuleDecision{Action: permission.ActionAsk}
	}
	return winner
}

func permissionRuleDecisionBetter(candidate, current permission.RuleDecision) bool {
	priority := func(action permission.PermissionAction) int {
		switch action {
		case permission.ActionDeny:
			return 3
		case permission.ActionAsk:
			return 2
		case permission.ActionAllow:
			return 1
		default:
			return 0
		}
	}
	if candidatePriority, currentPriority := priority(candidate.Action), priority(current.Action); candidatePriority != currentPriority {
		return candidatePriority > currentPriority
	}
	if candidate.ToolExact != current.ToolExact {
		return candidate.ToolExact
	}
	candidateHasInput := candidate.Rule != nil && candidate.Rule.InputPattern != ""
	currentHasInput := current.Rule != nil && current.Rule.InputPattern != ""
	if candidateHasInput != currentHasInput {
		return candidateHasInput
	}
	if candidate.Specificity != current.Specificity {
		return candidate.Specificity > current.Specificity
	}
	if candidate.InputExact != current.InputExact {
		return candidate.InputExact
	}
	return false
}

func toolPath(toolName string, input map[string]any, cwd string) string {
	switch toolName {
	case "Read", "Write", "Edit":
		return stringInput(input, "file_path")
	case "NotebookEdit":
		return stringInput(input, "notebook_path")
	case "Grep", "Glob":
		if path := stringInput(input, "path"); path != "" {
			return path
		}
		return cwd
	}
	return ""
}

func stringInput(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func canonicalInput(input map[string]any) string {
	if input == nil {
		return "{}"
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func canonicalPath(cwd, path string) string {
	return permission.ResolvePermissionPath(path, cwd).Effective()
}
