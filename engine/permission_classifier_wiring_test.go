package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestAutoModeClassifierAllowAndDenyLifecycle(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		wantAllowed  bool
		wantDecision string
	}{
		{name: "allow", response: "<allow/>", wantAllowed: true, wantDecision: string(permission.DecisionAllow)},
		{name: "deny", response: "<block/>", wantAllowed: false, wantDecision: string(permission.DecisionDeny)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompts := 0
			eng := NewQueryEngine(QueryEngineConfig{
				CWD:       t.TempDir(),
				ChatModel: &fixedResponseModel{response: tt.response},
				CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
					prompts++
					return false, "prompted"
				},
			})
			t.Cleanup(eng.Close)
			var events []QueryEvent
			ctx := withClassifierStatusEmitter(withToolUseID(context.Background(), "tool-1"), func(event QueryEvent) {
				events = append(events, event)
			})
			allowed, _ := eng.wrappedCanUseTool(
				ctx,
				"TaskCreate",
				map[string]any{
					"subject":     "review state",
					"description": "review state",
				},
				&ToolUseContext{
					Options: &ToolUseOptions{
						PermissionMode: permission.ModeAuto,
					},
				},
			)
			if allowed != tt.wantAllowed || prompts != 0 {
				t.Fatalf("allowed=%v prompts=%d", allowed, prompts)
			}
			if len(events) != 2 || events[0].ClassifierStatus.Phase != ClassifierStatusChecking ||
				events[1].ClassifierStatus.Phase != ClassifierStatusCompleted ||
				events[1].ClassifierStatus.Decision != tt.wantDecision {
				t.Fatalf("classifier events = %#v", events)
			}
			if tt.wantAllowed {
				if denials := eng.GetPermissionDenials(); len(denials) != 0 {
					t.Fatalf("permission denials = %#v", denials)
				}
			} else {
				denials := eng.GetPermissionDenials()
				if len(denials) != 1 || denials[0].ToolUseID != "tool-1" {
					t.Fatalf("permission denials = %#v", denials)
				}
			}
		})
	}
}

func TestAutoModeClassifierErrorClearsAndPrompts(t *testing.T) {
	chatModel := &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return nil, errors.New("classifier unavailable")
	}}
	prompts := 0
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:       t.TempDir(),
		ChatModel: chatModel,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			prompts++
			return true, "user allowed"
		},
	})
	t.Cleanup(eng.Close)
	var events []QueryEvent
	ctx := withClassifierStatusEmitter(withToolUseID(context.Background(), "tool-2"), func(event QueryEvent) {
		events = append(events, event)
	})
	allowed, _ := eng.wrappedCanUseTool(
		ctx,
		"TaskCreate",
		map[string]any{
			"subject":     "review state",
			"description": "review state",
		},
		&ToolUseContext{
			Options: &ToolUseOptions{
				PermissionMode: permission.ModeAuto,
			},
		},
	)
	if !allowed || prompts != 1 {
		t.Fatalf("allowed=%v prompts=%d", allowed, prompts)
	}
	if len(events) != 2 || events[0].ClassifierStatus.Phase != ClassifierStatusChecking ||
		events[1].ClassifierStatus.Phase != ClassifierStatusCleared {
		t.Fatalf("classifier events = %#v", events)
	}
}
