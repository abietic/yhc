package keybindings

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// ValidationSeverity determines whether a config can be applied.
type ValidationSeverity string

const (
	ValidationWarning ValidationSeverity = "warning"
	ValidationError   ValidationSeverity = "error"
)

// ValidationIssue describes one actionable keybinding config problem.
type ValidationIssue struct {
	Severity ValidationSeverity
	Type     string
	Message  string
	Key      string
	Context  Context
	Action   Action
}

type userBindingBlock struct {
	Context  string             `json:"context"`
	Bindings map[string]*string `json:"bindings"`
}

type reservedShortcut struct {
	Reason   string
	Severity ValidationSeverity
}

var ReservedShortcuts = map[string]reservedShortcut{
	"ctrl+c":  {Reason: "interrupt/exit is non-rebindable", Severity: ValidationError},
	"ctrl+d":  {Reason: "exit is non-rebindable", Severity: ValidationError},
	"ctrl+m":  {Reason: "terminals encode it as Enter", Severity: ValidationError},
	"ctrl+z":  {Reason: "Unix terminals normally reserve it for suspend", Severity: ValidationWarning},
	"ctrl+\\": {Reason: "Unix terminals normally reserve it for SIGQUIT", Severity: ValidationError},
}

// IsReserved reports whether a canonical key is terminal/product reserved.
func IsReserved(key string) bool {
	normalized, err := NormalizeKeyPattern(key)
	if err != nil {
		return false
	}
	_, ok := reservedForPlatform()[normalized]
	return ok
}

// ValidateBindings validates already-decoded user blocks.
func ValidateBindings(blocks []Block) []ValidationIssue {
	userBlocks := make([]userBindingBlock, len(blocks))
	for i, block := range blocks {
		bindings := make(map[string]*string, len(block.Bindings))
		for key, action := range block.Bindings {
			value := string(action)
			bindings[key] = &value
		}
		userBlocks[i] = userBindingBlock{Context: string(block.Context), Bindings: bindings}
	}
	return validateUserBindingBlocks(userBlocks)
}

// HasValidationErrors reports whether findings require falling back to the last
// valid configuration.
func HasValidationErrors(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Severity == ValidationError {
			return true
		}
	}
	return false
}

// FormatValidationIssues returns a compact user-facing config diagnostic.
func FormatValidationIssues(issues []ValidationIssue) string {
	if len(issues) == 0 {
		return ""
	}
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		lines = append(lines, fmt.Sprintf("%s: %s", issue.Severity, issue.Message))
	}
	return strings.Join(lines, "\n")
}

func validateUserBindingBlocks(blocks []userBindingBlock) []ValidationIssue {
	validContexts := make(map[Context]bool, len(AllContexts))
	for _, context := range AllContexts {
		validContexts[context] = true
	}
	reserved := reservedForPlatform()
	seen := make(map[string]Action)
	var issues []ValidationIssue

	for _, block := range blocks {
		context := Context(block.Context)
		if !validContexts[context] {
			issues = append(issues, ValidationIssue{
				Severity: ValidationError, Type: "invalid_context", Context: context,
				Message: fmt.Sprintf("unknown keybinding context %q", block.Context),
			})
			continue
		}
		keys := make([]string, 0, len(block.Bindings))
		for key := range block.Bindings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			actionValue := block.Bindings[key]
			normalized, err := NormalizeKeyPattern(key)
			if err != nil {
				issues = append(issues, ValidationIssue{
					Severity: ValidationError, Type: "parse_error", Key: key, Context: context,
					Message: err.Error(),
				})
				continue
			}
			if chord, _ := ParseChord(normalized); chordUsesSuper(chord) {
				issues = append(issues, ValidationIssue{
					Severity: ValidationError, Type: "unsupported_modifier", Key: key, Context: context,
					Message: fmt.Sprintf("%q uses super/cmd, which Bubble Tea v1 cannot report reliably", key),
				})
			}
			if entry, ok := reserved[normalized]; ok {
				issues = append(issues, ValidationIssue{
					Severity: entry.Severity, Type: "reserved", Key: key, Context: context,
					Message: fmt.Sprintf("%q is reserved: %s", key, entry.Reason),
				})
			}

			action := Action("")
			if actionValue != nil {
				action = Action(*actionValue)
				if _, known := SupportedActionContexts[action]; !known {
					issues = append(issues, ValidationIssue{
						Severity: ValidationError, Type: "invalid_action", Key: key, Context: context, Action: action,
						Message: fmt.Sprintf("action %q is not implemented in the active TUI", action),
					})
				} else if !SupportsAction(context, action) {
					issues = append(issues, ValidationIssue{
						Severity: ValidationError, Type: "invalid_action_context", Key: key, Context: context, Action: action,
						Message: fmt.Sprintf("action %q is not supported in context %q", action, context),
					})
				}
			}

			identity := string(context) + "\x00" + normalized
			if previous, exists := seen[identity]; exists && previous != action {
				issues = append(issues, ValidationIssue{
					Severity: ValidationError, Type: "conflict", Key: key, Context: context, Action: action,
					Message: fmt.Sprintf("%q has conflicting actions %q and %q in context %q", key, previous, action, context),
				})
			}
			seen[identity] = action
		}
	}
	return deduplicateIssues(issues)
}

func reservedForPlatform() map[string]reservedShortcut {
	reserved := make(map[string]reservedShortcut, len(ReservedShortcuts)+7)
	for key, value := range ReservedShortcuts {
		reserved[key] = value
	}
	if runtime.GOOS == "darwin" {
		for _, key := range []string{"super+c", "super+v", "super+x", "super+q", "super+w", "super+tab", "super+space"} {
			reserved[key] = reservedShortcut{Reason: "macOS intercepts this shortcut", Severity: ValidationError}
		}
	}
	return reserved
}

func chordUsesSuper(chord []KeyPattern) bool {
	for _, step := range chord {
		if step.Super {
			return true
		}
	}
	return false
}

func deduplicateIssues(issues []ValidationIssue) []ValidationIssue {
	seen := make(map[string]bool, len(issues))
	result := make([]ValidationIssue, 0, len(issues))
	for _, issue := range issues {
		identity := issue.Type + "\x00" + string(issue.Context) + "\x00" + issue.Key
		if seen[identity] {
			continue
		}
		seen[identity] = true
		result = append(result, issue)
	}
	return result
}
