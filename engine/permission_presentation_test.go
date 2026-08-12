package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/permission"
)

func TestPermissionPresentationIsBoundedAndDoesNotProjectInput(t *testing.T) {
	secret := "secret-command-/absolute/path"
	presentation := permissionPresentationForAction("permission", PermissionActionDescriptor{
		CanonicalToolName: "Write",
		Write:             true,
		PathWithinRoots:   false,
		Input:             map[string]any{"command": secret},
		CanonicalInput:    secret,
		CWD:               secret,
		WorkingRoots:      []string{secret},
	})
	encoded, err := json.Marshal(presentation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || presentation.Version != 1 || len(presentation.GrantScopes) != 3 {
		t.Fatalf("unsafe presentation: %#v", presentation)
	}
	copy := clonePermissionPresentation(presentation)
	copy.Evidence[0].Value = "changed"
	if presentation.Evidence[0].Value == "changed" {
		t.Fatal("presentation was not cloned")
	}
}

func TestPermissionPresentationFailClosedForInvalidOrForgedPayload(t *testing.T) {
	valid := permissionPresentationForAction("permission", PermissionActionDescriptor{CanonicalToolName: "Write", Write: true})
	if normalized := normalizedPermissionPresentation("permission", "Write", valid); normalized == nil || normalized.Unavailable {
		t.Fatalf("valid presentation = %#v", normalized)
	}

	for name, forged := range map[string]*PermissionPresentation{
		"mismatched tool": valid,
		"secret text": {
			Version:     1,
			ToolLabel:   "Write",
			Summary:     "secret /tmp/private",
			GrantScopes: []PermissionInteractionDecision{PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways},
		},
		"invalid utf8": {
			Version:     1,
			ToolLabel:   string([]byte{0xff}),
			GrantScopes: []PermissionInteractionDecision{PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways},
		},
	} {
		t.Run(name, func(t *testing.T) {
			toolName := "Write"
			if name == "mismatched tool" {
				toolName = "legacy-write"
			}
			normalized := normalizedPermissionPresentation("permission", toolName, forged)
			if normalized == nil || !normalized.Unavailable || normalized.ToolLabel != "" || normalized.Summary != "" || len(normalized.Evidence) != 0 || len(normalized.GrantScopes) != 1 || normalized.GrantScopes[0] != PermissionAllowOnce {
				t.Fatalf("forged presentation = %#v", normalized)
			}
		})
	}

	if normalizedPermissionPresentation("question", "Write", valid) != nil {
		t.Fatal("non-permission interaction received a presentation")
	}
}

func TestPermissionPresentationBoundsAndFixedEvidenceAllowlist(t *testing.T) {
	if !boundedPresentationString(strings.Repeat("界", 96), 96) || boundedPresentationString(strings.Repeat("界", 97), 96) || boundedPresentationString(string([]byte{0xff}), 96) {
		t.Fatal("rune or UTF-8 bounds not enforced")
	}

	valid := permissionPresentationForAction("permission", PermissionActionDescriptor{
		CanonicalToolName: "Bash",
		Destructive:       true,
		Path:              permission.PathResolution{Paths: []string{"workspace/file"}},
		PathWithinRoots:   false,
		Network:           true,
		Child:             true,
	})
	if valid == nil || valid.Unavailable || len(valid.Evidence) != 4 {
		t.Fatalf("allowlisted presentation = %#v", valid)
	}
	valid.Evidence = append(valid.Evidence, PermissionPresentationEvidence{Label: "Arbitrary", Value: "untrusted"})
	if normalized := normalizedPermissionPresentation("permission", "Bash", valid); normalized == nil || !normalized.Unavailable {
		t.Fatalf("unallowlisted evidence was accepted: %#v", normalized)
	}
}

func TestPermissionPresentationCoordinatorPreservesCanonicalAliasAndClones(t *testing.T) {
	coordinator := newPermissionCoordinator(PermissionProjectIdentity{})
	coordinator.registerEngine("engine-1")
	presentation := permissionPresentationForAction(
		PermissionInteractionKindPermission,
		PermissionActionDescriptor{CanonicalToolName: "Write", Write: true},
	)
	var emitted *PermissionPresentation
	var adapterPresentation *PermissionPresentation
	result := coordinator.request(
		context.Background(),
		"engine-1",
		PermissionPromptRequest{
			Kind:              PermissionInteractionKindPermission,
			Source:            "coordinator",
			ToolName:          "write_alias",
			CanonicalToolName: "Write",
			ToolUseID:         "alias-call",
			Presentation:      presentation,
		},
		func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
			adapterPresentation = clonePermissionPresentation(request.Presentation)
			if request.Presentation != nil && len(request.Presentation.Evidence) > 0 {
				request.Presentation.Evidence[0].Value = "adapter mutation"
			}
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
		func(event QueryEvent) {
			if event.PermissionRequest != nil {
				emitted = event.PermissionRequest.Presentation
				if event.PermissionRequest.CanonicalToolName != "Write" {
					t.Fatalf("event canonical tool = %q", event.PermissionRequest.CanonicalToolName)
				}
			}
		},
		nil,
		nil,
	)
	if result.Decision != PermissionDeny {
		t.Fatalf("result = %#v", result)
	}
	if adapterPresentation == nil || adapterPresentation.Unavailable || adapterPresentation.ToolLabel != "Write" {
		t.Fatalf("adapter presentation = %#v", adapterPresentation)
	}
	if presentation.Evidence[0].Value != "May change data" {
		t.Fatalf("source presentation was mutated: %#v", presentation)
	}
	if emitted == nil || emitted.Evidence[0].Value != "May change data" {
		t.Fatalf("event presentation was mutated: %#v", emitted)
	}
}

func TestProjectGraphPermissionPresentationPersistsAndReprojects(t *testing.T) {
	presentation := permissionPresentationForAction(
		PermissionInteractionKindPermission,
		PermissionActionDescriptor{CanonicalToolName: "Write", Write: true},
	)
	request := projectGraphHITLRequest{
		Version:           projectGraphHITLRequestVersion,
		RequestID:         "graph-presentation",
		ToolName:          "write_alias",
		CanonicalToolName: "Write",
		Kind:              PermissionInteractionKindPermission,
		Scope:             RuntimeInputScope{SessionID: "session-1"},
		Presentation:      presentation,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var restored projectGraphHITLRequest
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	store := &projectGraphCheckpointStore{
		envelope: projectGraphCheckpointEnvelope{Interrupt: &restored},
	}
	query := &QueryEngine{projectGraphCheckpoint: store}

	first, ok := query.PendingProjectGraphPermissionRequest()
	if !ok || first.CanonicalToolName != "Write" || first.Presentation == nil || first.Presentation.Unavailable || first.Presentation.ToolLabel != "Write" {
		t.Fatalf("first projection = %#v ok=%v", first, ok)
	}
	first.Presentation.Evidence[0].Value = "caller mutation"
	second, ok := query.PendingProjectGraphPermissionRequest()
	if !ok || second.Presentation == nil || second.Presentation.Evidence[0].Value != "May change data" {
		t.Fatalf("second projection = %#v ok=%v", second, ok)
	}

	legacy := restored
	legacy.CanonicalToolName = ""
	legacy.Presentation = nil
	store.envelope.Interrupt = &legacy
	recovered, ok := query.PendingProjectGraphPermissionRequest()
	if !ok || recovered.Presentation == nil || !recovered.Presentation.Unavailable || len(recovered.Presentation.GrantScopes) != 1 || recovered.Presentation.GrantScopes[0] != PermissionAllowOnce {
		t.Fatalf("legacy projection = %#v ok=%v", recovered, ok)
	}
}
