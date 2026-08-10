package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

func TestPermissionInteractionResultPreservesStructuredDecisions(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		response PermissionResponse
		data     threadAttentionResponseData
		want     engine.PermissionInteractionResult
	}{
		{name: "once", response: PermissionAllow, want: engine.PermissionInteractionResult{Decision: engine.PermissionAllowOnce}},
		{name: "session", response: PermissionAllowSession, want: engine.PermissionInteractionResult{Decision: engine.PermissionAllowSession}},
		{name: "always", response: PermissionAllowAlways, want: engine.PermissionInteractionResult{Decision: engine.PermissionAllowAlways}},
		{name: "deny", response: PermissionDeny, want: engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: "user denied permission"}},
		{
			name: "question answer", toolName: "AskUserQuestion", response: PermissionAllow,
			data: threadAttentionResponseData{answerJSON: `{"answers":{"Choose":"A"}}`},
			want: engine.PermissionInteractionResult{
				Decision:     engine.PermissionAllowOnce,
				UpdatedInput: map[string]any{"answers": map[string]any{"Choose": "A"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := engine.PermissionPromptRequest{ToolName: test.toolName}
			if got := permissionInteractionResult(request, test.response, test.data); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPermissionInteractionResultPreservesPlanFeedback(t *testing.T) {
	request := engine.PermissionPromptRequest{
		ToolName: "ExitPlanMode",
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: "plan-1", PlanRevision: 3,
		},
	}
	got := permissionInteractionResult(request, PermissionDeny, threadAttentionResponseData{planResult: &engine.PlanApprovalDecision{RequestID: "plan-1", PlanRevision: 3, Outcome: engine.PlanApprovalRevise, TargetMode: permission.ModePlan, Feedback: "cover rollback"}})
	if got.Decision != engine.PermissionDeny || !strings.Contains(got.Message, "cover rollback") ||
		got.PlanApproval == nil || got.PlanApproval.RequestID != "plan-1" ||
		got.PlanApproval.PlanRevision != 3 ||
		got.PlanApproval.Outcome != engine.PlanApprovalRevise ||
		got.PlanApproval.Feedback != "cover rollback" ||
		got.PlanApproval.Approved {
		t.Fatalf("plan denial = %#v", got)
	}
}

func TestPermissionInteractionResultPreservesExplicitPlanTarget(t *testing.T) {
	request := engine.PermissionPromptRequest{
		ToolName: "ExitPlanMode",
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: "plan-1", PlanRevision: 3,
		},
	}
	got := permissionInteractionResult(request, PermissionAllow, threadAttentionResponseData{
		planResult: &engine.PlanApprovalDecision{RequestID: "plan-1", PlanRevision: 3, Outcome: engine.PlanApprovalApprove, ReviewedPlanDigest: engine.PlanBytesDigest([]byte("# Reviewed")), TargetMode: permission.ModeBypassPermissions, Confirmed: true},
	})
	if got.Decision != engine.PermissionAllowOnce || got.PlanApproval == nil ||
		got.PlanApproval.Approved || !got.PlanApproval.Confirmed ||
		got.PlanApproval.Outcome != engine.PlanApprovalApprove ||
		got.PlanApproval.ReviewedPlanDigest !=
			engine.PlanBytesDigest([]byte("# Reviewed")) ||
		got.PlanApproval.TargetMode != permission.ModeBypassPermissions ||
		got.PlanApproval.RequestID != "plan-1" || got.PlanApproval.PlanRevision != 3 {
		t.Fatalf("plan approval = %#v", got)
	}
}

func TestPermissionInteractionResultFailsClosedWithoutPlanTerminalResult(t *testing.T) {
	request := engine.PermissionPromptRequest{ToolName: "ExitPlanMode", PlanApproval: &engine.PlanApprovalRequest{RequestID: "plan-1", PlanRevision: 3}}
	for _, response := range []PermissionResponse{PermissionAllow, PermissionDeny} {
		got := permissionInteractionResult(request, response, threadAttentionResponseData{feedback: "retained draft", planTarget: permission.ModeBypassPermissions, planConfirmed: true})
		if got.Decision != engine.PermissionDeny || got.PlanApproval == nil || got.PlanApproval.Outcome != engine.PlanApprovalCancel || got.PlanApproval.Feedback != "" || got.PlanApproval.Confirmed {
			t.Fatalf("generic response %v escaped fail-closed result %#v", response, got)
		}
	}
}

func TestPermissionTerminalResultEmitsTypedPlanCancel(t *testing.T) {
	request := engine.PermissionPromptRequest{
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: "plan-terminal", PlanRevision: 9,
		},
	}
	for _, decision := range []engine.PermissionInteractionDecision{
		engine.PermissionCancelled,
		engine.PermissionTimedOut,
		engine.PermissionDeny,
	} {
		got := permissionTerminalResult(request, decision, "terminal")
		if got.Decision != decision ||
			got.PlanApproval == nil ||
			got.PlanApproval.RequestID != "plan-terminal" ||
			got.PlanApproval.PlanRevision != 9 ||
			got.PlanApproval.Outcome != engine.PlanApprovalCancel ||
			got.PlanApproval.Approved ||
			got.PlanApproval.TargetMode != permission.ModePlan {
			t.Fatalf("decision %q result = %#v", decision, got)
		}
	}
}

func TestCoordinatorPermissionEventDoesNotDuplicateAdapterDialog(t *testing.T) {
	app := newTestApp(80, 24)
	cmd := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPermissionRequest,
		PermissionRequest: &engine.PermissionRequestEvent{
			ToolName: "Bash", ToolUseID: "permission-1", Source: "coordinator",
		},
	})
	if cmd != nil || app.hasDialog(StatePermission) || len(app.threadAttention.requests) != 0 {
		t.Fatalf("coordinator event duplicated adapter presentation: cmd=%v dialogs=%d attention=%d", cmd != nil, app.dialogs.Len(), len(app.threadAttention.requests))
	}
}

func TestCoalescedPermissionResolutionClearsOnlyMatchingOwnerAttention(t *testing.T) {
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "leader-permission", ThreadID: "leader-thread", Kind: threadAttentionPermission,
		Tool: "Bash", Source: "callback", responseCh: make(chan PermissionResponse, 1),
	})
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "child-permission", ThreadID: "child-thread", AgentID: "child-agent", Kind: threadAttentionPermission,
		Tool: "Bash", Source: "callback", responseCh: make(chan PermissionResponse, 1),
	})

	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPermissionResolved,
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			ThreadID: "child-thread",
			AgentID:  "child-agent",
		},
		PermissionResolved: &engine.PermissionResolvedEvent{
			ToolUseID: "child-permission", Decision: string(engine.PermissionAllowSession), Reason: "coalesced",
		},
	})

	if _, exists := app.threadAttention.get("child-permission"); exists {
		t.Fatal("coalesced child attention remained visible")
	}
	if _, exists := app.threadAttention.get("leader-permission"); !exists {
		t.Fatal("coalesced child resolution removed unrelated leader attention")
	}
	if len(app.threadAttention.requests) != 1 || app.state != StatePermission {
		t.Fatalf("attention state = requests:%d state:%v", len(app.threadAttention.requests), app.state)
	}
}
