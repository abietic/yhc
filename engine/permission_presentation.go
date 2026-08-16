package engine

import (
	"strings"
	"unicode/utf8"
)

const permissionPresentationVersion = 1

// PermissionPresentation is a bounded, non-authoritative display projection
// of a validated ordinary permission action. It intentionally excludes raw
// inputs, reviewer material, and every other execution authority.
type PermissionPresentation struct {
	Version     int                              `json:"version"`
	ToolLabel   string                           `json:"tool_label,omitempty"`
	Summary     string                           `json:"summary,omitempty"`
	Evidence    []PermissionPresentationEvidence `json:"evidence,omitempty"`
	GrantScopes []PermissionInteractionDecision  `json:"grant_scopes,omitempty"`
	Unavailable bool                             `json:"unavailable,omitempty"`
}

// PermissionPresentationEvidence is one fixed, display-only fact about the
// validated action. Values not in the allowlist are never rendered.
type PermissionPresentationEvidence struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func unavailablePermissionPresentation() *PermissionPresentation {
	return &PermissionPresentation{
		Version:     permissionPresentationVersion,
		Unavailable: true,
		GrantScopes: []PermissionInteractionDecision{PermissionAllowOnce},
	}
}

func clonePermissionPresentation(presentation *PermissionPresentation) *PermissionPresentation {
	if presentation == nil {
		return nil
	}
	clone := *presentation
	clone.Evidence = append([]PermissionPresentationEvidence(nil), presentation.Evidence...)
	clone.GrantScopes = append([]PermissionInteractionDecision(nil), presentation.GrantScopes...)
	return &clone
}

// normalizedPermissionPresentation rejects every untrusted or stale display
// payload. A failed validation deliberately exposes only an allow-once choice.
func normalizedPermissionPresentation(
	kind, toolName string,
	presentation *PermissionPresentation,
	constraint PermissionDecisionConstraint,
) *PermissionPresentation {
	if kind != PermissionInteractionKindPermission {
		return nil
	}
	if !validPermissionPresentationForTool(presentation, toolName, constraint) {
		return unavailablePermissionPresentation()
	}
	return clonePermissionPresentation(presentation)
}

func validPermissionPresentationForTool(
	presentation *PermissionPresentation,
	toolName string,
	constraint PermissionDecisionConstraint,
) bool {
	if !validPermissionPresentation(presentation) {
		return false
	}
	if presentation.Unavailable {
		return presentation.ToolLabel == "" && presentation.Summary == "" &&
			len(presentation.Evidence) == 0 &&
			len(presentation.GrantScopes) == 1 &&
			presentation.GrantScopes[0] == PermissionAllowOnce
	}
	if strings.TrimSpace(presentation.ToolLabel) == "" ||
		presentation.ToolLabel != strings.TrimSpace(toolName) ||
		presentation.Summary != "Allow this tool action?" ||
		!permissionPresentationScopesMatchConstraint(presentation.GrantScopes, constraint) {
		return false
	}

	seenPairs := make(map[string]bool, len(presentation.Evidence))
	seenLabels := make(map[string]bool, len(presentation.Evidence))
	accessCount := 0
	for _, evidence := range presentation.Evidence {
		pair := evidence.Label + "\x00" + evidence.Value
		if seenPairs[pair] || seenLabels[evidence.Label] || !allowedPermissionPresentationEvidence(evidence) {
			return false
		}
		seenPairs[pair] = true
		seenLabels[evidence.Label] = true
		if evidence.Label == "Access" {
			accessCount++
		}
	}
	return accessCount == 1
}

func allowedPermissionPresentationEvidence(evidence PermissionPresentationEvidence) bool {
	switch evidence.Label + "\x00" + evidence.Value {
	case "Access\x00Reads data",
		"Access\x00May make destructive changes",
		"Access\x00May change data",
		"Access\x00May perform an action",
		"Path scope\x00Within workspace",
		"Path scope\x00Outside workspace boundary",
		"Network\x00Uses network access",
		"Process\x00Starts a child process":
		return true
	default:
		return false
	}
}

func validPermissionPresentation(presentation *PermissionPresentation) bool {
	if presentation == nil || presentation.Version != permissionPresentationVersion ||
		len(presentation.Evidence) > 6 || len(presentation.GrantScopes) > 3 ||
		!boundedPresentationString(presentation.ToolLabel, 96) ||
		!boundedPresentationString(presentation.Summary, 256) {
		return false
	}
	for _, evidence := range presentation.Evidence {
		if !boundedPresentationString(evidence.Label, 48) || !boundedPresentationString(evidence.Value, 160) {
			return false
		}
	}
	for _, scope := range presentation.GrantScopes {
		if scope != PermissionAllowOnce && scope != PermissionAllowSession && scope != PermissionAllowAlways {
			return false
		}
	}
	return true
}

func boundedPresentationString(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func permissionPresentationForAction(
	kind string,
	action PermissionActionDescriptor,
	constraint PermissionDecisionConstraint,
) *PermissionPresentation {
	if kind != PermissionInteractionKindPermission {
		return nil
	}
	toolName := strings.TrimSpace(action.CanonicalToolName)
	if toolName == "" || !boundedPresentationString(toolName, 96) {
		return unavailablePermissionPresentation()
	}

	evidence := make([]PermissionPresentationEvidence, 0, 4)
	switch {
	case action.ReadOnly:
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Access", Value: "Reads data"})
	case action.Destructive:
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Access", Value: "May make destructive changes"})
	case action.Write:
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Access", Value: "May change data"})
	default:
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Access", Value: "May perform an action"})
	}
	if action.Path.Paths != nil {
		pathScope := "Within workspace"
		if !action.PathWithinRoots {
			pathScope = "Outside workspace boundary"
		}
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Path scope", Value: pathScope})
	}
	if action.Network {
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Network", Value: "Uses network access"})
	}
	if action.Child {
		evidence = append(evidence, PermissionPresentationEvidence{Label: "Process", Value: "Starts a child process"})
	}

	grantScopes := []PermissionInteractionDecision{PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways}
	if constraint == PermissionAllowOnceOnly {
		grantScopes = []PermissionInteractionDecision{PermissionAllowOnce}
	}
	presentation := &PermissionPresentation{
		Version:     permissionPresentationVersion,
		ToolLabel:   toolName,
		Summary:     "Allow this tool action?",
		Evidence:    evidence,
		GrantScopes: grantScopes,
	}
	if !validPermissionPresentationForTool(presentation, toolName, constraint) {
		return unavailablePermissionPresentation()
	}
	return presentation
}

func permissionPresentationScopesMatchConstraint(
	scopes []PermissionInteractionDecision,
	constraint PermissionDecisionConstraint,
) bool {
	if !constraint.valid() {
		return false
	}
	if constraint == PermissionAllowOnceOnly {
		return len(scopes) == 1 && scopes[0] == PermissionAllowOnce
	}
	return len(scopes) == 3 &&
		scopes[0] == PermissionAllowOnce &&
		scopes[1] == PermissionAllowSession &&
		scopes[2] == PermissionAllowAlways
}
