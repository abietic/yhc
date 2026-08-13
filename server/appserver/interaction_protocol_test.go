package appserver

import (
	"encoding/json"
	"testing"
)

func TestProtocolV2InteractionSnapshotsMarshal(t *testing.T) {
	tests := []struct {
		name  string
		value InteractionSnapshot
		want  string
	}{
		{
			name: "permission",
			value: InteractionSnapshot{
				RequestID: "request-1", TurnID: "turn-1", Kind: "permission",
				Permission: &PermissionInteractionSnapshot{
					Available: true, ToolLabel: "Write", Summary: "Allow this tool action?",
					Evidence:    []PermissionEvidenceSnapshot{{Label: "Access", Value: "May change data"}},
					GrantScopes: []string{"allow_once", "allow_session"},
				},
			},
			want: `{"request_id":"request-1","turn_id":"turn-1","kind":"permission","permission":{"available":true,"tool_label":"Write","summary":"Allow this tool action?","evidence":[{"label":"Access","value":"May change data"}],"grant_scopes":["allow_once","allow_session"]}}`,
		},
		{
			name: "question",
			value: InteractionSnapshot{
				RequestID: "request-2", TurnID: "turn-2", Kind: "question",
				Question: &QuestionInteractionSnapshot{Questions: []QuestionSnapshot{{
					ID: "q-1", Header: "Choice", Text: "Choose one", MultiSelect: false,
					Options: []QuestionOptionSnapshot{{ID: "q-1-o-1", Label: "One", Description: "First choice"}},
				}}},
			},
			want: `{"request_id":"request-2","turn_id":"turn-2","kind":"question","question":{"questions":[{"id":"q-1","header":"Choice","text":"Choose one","options":[{"id":"q-1-o-1","label":"One","description":"First choice"}],"multi_select":false,"free_text":false}]}}`,
		},
		{
			name: "plan approval",
			value: InteractionSnapshot{
				RequestID: "request-3", TurnID: "turn-3", Kind: "plan_approval",
				PlanApproval: &PlanApprovalInteractionSnapshot{Revision: 4, TargetModes: []string{"default", "bypassPermissions"}, ReviewAvailable: true},
			},
			want: `{"request_id":"request-3","turn_id":"turn-3","kind":"plan_approval","plan_approval":{"revision":4,"target_modes":["default","bypassPermissions"],"review_available":true}}`,
		},
		{
			name: "repeated tool",
			value: InteractionSnapshot{
				RequestID: "request-4", TurnID: "turn-4", Kind: "repeated_tool",
				RepeatedTool: &RepeatedToolInteractionSnapshot{Attempt: 3, Explanation: "This repeated tool call needs your decision.", Outcomes: []string{"continue", "stop"}},
			},
			want: `{"request_id":"request-4","turn_id":"turn-4","kind":"repeated_tool","repeated_tool":{"attempt":3,"explanation":"This repeated tool call needs your decision.","outcomes":["continue","stop"]}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("marshal interaction snapshot: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("JSON = %s, want %s", got, test.want)
			}
		})
	}
}

func TestProtocolV2ResolveInteractionBodiesUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"permission", `{"kind":"permission","permission":{"decision":"allow_once"}}`},
		{"question", `{"kind":"question","question":{"outcome":"submit","answers":[{"question_id":"q-1","option_ids":["q-1-o-1"],"text":"other"}]}}`},
		{"plan approval", `{"kind":"plan_approval","plan_approval":{"outcome":"approve","revision":4,"target_mode":"default","reviewed_digest":"sha256:abc","confirmed":false}}`},
		{"repeated tool", `{"kind":"repeated_tool","repeated_tool":{"outcome":"continue"}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got ResolveInteractionRequest
			if err := json.Unmarshal([]byte(test.body), &got); err != nil {
				t.Fatalf("unmarshal resolve body: %v", err)
			}
			if got.Kind == "" {
				t.Fatalf("resolve body has no explicit kind: %#v", got)
			}
			variants := 0
			for _, present := range []bool{got.Permission != nil, got.Question != nil, got.PlanApproval != nil, got.RepeatedTool != nil} {
				if present {
					variants++
				}
			}
			if variants != 1 {
				t.Fatalf("resolve body did not select exactly one variant: %#v", got)
			}
		})
	}
}

func TestProtocolV2PublicDTOsDoNotSerializeLegacyFields(t *testing.T) {
	snapshot := InteractionSnapshot{
		RequestID: "request-1", TurnID: "turn-1", Kind: "plan_approval",
		PlanApproval: &PlanApprovalInteractionSnapshot{Revision: 1, TargetModes: []string{"default"}, ReviewAvailable: true},
	}
	resolve := ResolveInteractionRequest{
		Kind:         "repeated_tool",
		RepeatedTool: &ResolveRepeatedToolResult{Outcome: "stop"},
	}

	assertNoJSONFields(t, snapshot, "input", "message", "source", "plan_file_identity", "initial_plan_digest", "updated_input")
	assertNoJSONFields(t, resolve, "input", "plan_file_identity", "initial_plan_digest", "updated_input")
}

func assertNoJSONFields(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal public DTO: %v", err)
	}
	for _, field := range forbidden {
		if containsJSONField(encoded, field) {
			t.Fatalf("public DTO leaked %q in %s", field, encoded)
		}
	}
}

func containsJSONField(encoded []byte, field string) bool {
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return false
	}
	return containsField(value, field)
}

func containsField(value any, field string) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == field || containsField(child, field) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsField(child, field) {
				return true
			}
		}
	}
	return false
}
