package permission

import (
	"strings"
	"testing"
)

func TestPermissionReviewStrictBindingAndReason(t *testing.T) {
	request := validPermissionReviewRequest()
	valid := PermissionReviewResult{
		SchemaVersion: PermissionReviewSchemaVersion,
		RequestID:     request.RequestID,
		ToolCallID:    request.ToolCallID,
		BindingNonce:  request.BindingNonce,
		Decision:      ReviewDecisionApprove,
		ReasonCode:    ReviewReasonExpectedSafe,
		Rationale:     "bounded action",
	}
	if err := ValidatePermissionReviewResult(request, valid); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*PermissionReviewResult)
	}{
		{
			name: "schema",
			mutate: func(result *PermissionReviewResult) {
				result.SchemaVersion++
			},
		},
		{
			name: "request",
			mutate: func(result *PermissionReviewResult) {
				result.RequestID = strings.Repeat("3", 32)
			},
		},
		{
			name: "tool call",
			mutate: func(result *PermissionReviewResult) {
				result.ToolCallID = "other"
			},
		},
		{
			name: "nonce",
			mutate: func(result *PermissionReviewResult) {
				result.BindingNonce = strings.Repeat("4", 64)
			},
		},
		{
			name: "decision",
			mutate: func(result *PermissionReviewResult) {
				result.Decision = "maybe"
			},
		},
		{
			name: "reason",
			mutate: func(result *PermissionReviewResult) {
				result.ReasonCode = ReviewReasonUnexpectedRisk
			},
		},
		{
			name: "empty rationale",
			mutate: func(result *PermissionReviewResult) {
				result.Rationale = ""
			},
		},
		{
			name: "oversized rationale",
			mutate: func(result *PermissionReviewResult) {
				result.Rationale = strings.Repeat(
					"x",
					MaxPermissionReviewRationaleBytes+1,
				)
			},
		},
		{
			name: "invalid UTF-8 rationale",
			mutate: func(result *PermissionReviewResult) {
				result.Rationale = string([]byte{0xff})
			},
		},
		{
			name: "control character rationale",
			mutate: func(result *PermissionReviewResult) {
				result.Rationale = "safe\x00unsafe"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			test.mutate(&result)
			if err := ValidatePermissionReviewResult(
				request,
				result,
			); err == nil {
				t.Fatal("invalid result was accepted")
			}
		})
	}
}

func TestPermissionReviewRequestAndRouteValidation(t *testing.T) {
	request := validPermissionReviewRequest()
	if err := ValidatePermissionReviewRequest(request); err != nil {
		t.Fatal(err)
	}
	request.Projection.RedactedArgs = []byte(`{"unterminated"`)
	if err := ValidatePermissionReviewRequest(request); err == nil {
		t.Fatal("invalid projection JSON was accepted")
	}
	for _, mutate := range []func(*PermissionReviewRequest){
		func(request *PermissionReviewRequest) {
			request.ToolCallID = "tool\x00unsafe"
		},
		func(request *PermissionReviewRequest) {
			request.Projection.CanonicalTool = "Read/injected"
		},
		func(request *PermissionReviewRequest) {
			request.Projection.RedactedArgs = []byte(
				`{"password":"raw-secret"}`,
			)
		},
		func(request *PermissionReviewRequest) {
			request.Projection.RedactedArgs = []byte(
				`{"password":{"kind":"redacted_secret","value":"raw-secret"}}`,
			)
		},
		func(request *PermissionReviewRequest) {
			request.Projection.RootFacts = []RootFact{{
				Kind:     "cwd",
				Label:    "/absolute/host/path",
				Boundary: "inside",
			}}
		},
		func(request *PermissionReviewRequest) {
			request.Projection.TrustedIntent = []IntentRecord{{
				Kind:    "direct_user",
				Content: "unsafe\x00intent",
			}}
		},
	} {
		candidate := validPermissionReviewRequest()
		mutate(&candidate)
		if err := ValidatePermissionReviewRequest(candidate); err == nil {
			t.Fatalf("unsafe request was accepted: %#v", candidate)
		}
	}
	if IsSafeReviewRoute(ApprovalReviewerRoute{
		Provider:     "openai",
		Model:        "review-model",
		DataBoundary: "actor_context",
	}) {
		t.Fatal("unsafe data boundary was accepted")
	}
	if !IsSafeReviewRoute(ApprovalReviewerRoute{
		Provider:     "openai",
		Model:        "account/review-model@2026-07-27",
		DataBoundary: PermissionReviewDataBoundary,
	}) {
		t.Fatal("explicit model-safe route was rejected")
	}
	if IsSafeReviewRoute(ApprovalReviewerRoute{
		Provider:     "openai",
		Model:        "review-model\ninjected",
		DataBoundary: PermissionReviewDataBoundary,
	}) {
		t.Fatal("unsafe diagnostic route was accepted")
	}
}

func validPermissionReviewRequest() PermissionReviewRequest {
	return PermissionReviewRequest{
		SchemaVersion: PermissionReviewSchemaVersion,
		RequestID:     strings.Repeat("1", 32),
		ToolCallID:    "tool-1",
		BindingNonce:  strings.Repeat("2", 64),
		Projection: PermissionReviewProjection{
			CanonicalTool: "TaskCreate",
			ActionKind:    "runtime_state",
			RedactedArgs:  []byte(`{}`),
		},
	}
}
