package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

func TestBubbleTeaThreadSwitchPreservesDraftAndPresentsOwnerAttention(t *testing.T) {
	app, catalog, _ := newThreadNavigationTestApp(t)
	for i := range catalog.Threads {
		if catalog.Threads[i].ThreadID == "child-alpha" {
			catalog.Threads[i].Mode = engine.ThreadModeLiveAttach
		}
	}
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"alt+left":  keybindings.ActionChatPreviousAgent,
			"alt+right": keybindings.ActionChatNextAgent,
		},
	}})
	app.textarea.SetValue("leader draft")
	responseCh := make(chan PermissionResponse, 1)

	updateApp(t, app, permissionRequestMsg{
		requestID: "child-permission", threadID: "child-alpha", agentID: "agent-alpha",
		tool: "Bash", input: `{"command":"make test"}`, responseCh: responseCh,
	})
	if app.state != StateChat || app.activeThreadViewID() != "leader-thread" || app.hasDialog(StatePermission) {
		t.Fatalf("inactive attention stole focus: state=%v thread=%q dialogs=%d", app.state, app.activeThreadViewID(), app.dialogs.Len())
	}

	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	if app.activeThreadViewID() != "child-alpha" || app.state != StatePermission || !app.hasDialog(StatePermission) {
		t.Fatalf("owner switch did not present permission: state=%v thread=%q", app.state, app.activeThreadViewID())
	}
	app.textarea.SetValue("child draft")
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt})
	if app.activeThreadViewID() != "child-alpha" {
		t.Fatal("permission modal leaked navigation key to the underlying thread")
	}

	updateApp(t, app, threadAttentionAnsweredMsg{requestID: "child-permission", response: PermissionAllow})
	select {
	case response := <-responseCh:
		if response != PermissionAllow {
			t.Fatalf("permission response = %v", response)
		}
	default:
		t.Fatal("permission callback was not released")
	}
	if app.state != StateChat || app.hasDialog(StatePermission) {
		t.Fatalf("resolved owner permission remained: state=%v dialogs=%d", app.state, app.dialogs.Len())
	}

	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt})
	if app.activeThreadViewID() != "leader-thread" || app.textarea.Value() != "leader draft" {
		t.Fatalf("leader restore = thread:%q draft:%q", app.activeThreadViewID(), app.textarea.Value())
	}
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	if app.activeThreadViewID() != "child-alpha" || app.textarea.Value() != "child draft" {
		t.Fatalf("child restore = thread:%q draft:%q", app.activeThreadViewID(), app.textarea.Value())
	}
}

func TestBubbleTeaCoveredAttentionResolutionMatrix(t *testing.T) {
	tests := []struct {
		name        string
		dialogState AppState
		request     func(chan PermissionResponse) tea.Msg
	}{
		{
			name: "permission", dialogState: StatePermission,
			request: func(response chan PermissionResponse) tea.Msg {
				return permissionRequestMsg{
					requestID: "request-1", threadID: fallbackLeaderThreadID,
					tool: "Write", input: `{"file_path":"README.md"}`, responseCh: response,
				}
			},
		},
		{
			name: "question", dialogState: StateAskUser,
			request: func(response chan PermissionResponse) tea.Msg {
				return askUserQuestionMsg{
					requestID: "request-1", threadID: fallbackLeaderThreadID,
					input: `{"questions":[]}`, responseCh: response,
				}
			},
		},
		{
			name: "plan", dialogState: StatePlanApproval,
			request: func(response chan PermissionResponse) tea.Msg {
				return planApprovalMsg{
					requestID: "request-1", threadID: fallbackLeaderThreadID,
					sessionID: "session-1", responseCh: response,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newTestApp(80, 24)
			responseCh := make(chan PermissionResponse, 1)
			updateApp(t, app, test.request(responseCh))
			if app.state != test.dialogState || !app.hasDialog(test.dialogState) {
				t.Fatalf("request did not open dialog %v: state=%v", test.dialogState, app.state)
			}

			app.openHelpOverlay()
			if app.state != StateHelp || app.dialogs.Len() != 2 {
				t.Fatalf("covered stack = state:%v depth:%d", app.state, app.dialogs.Len())
			}
			updateApp(t, app, threadAttentionAnsweredMsg{requestID: "request-1", response: PermissionAllow})
			if app.state != StateHelp || app.dialogs.Len() != 1 || app.hasDialog(test.dialogState) {
				t.Fatalf("async resolution corrupted covered stack: state=%v depth=%d", app.state, app.dialogs.Len())
			}
			select {
			case response := <-responseCh:
				if response != PermissionAllow {
					t.Fatalf("response = %v", response)
				}
			default:
				t.Fatal("callback was not released")
			}

			updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyEsc})
			if app.state != StateChat || app.dialogs.Len() != 0 {
				t.Fatalf("help did not return to base state: state=%v depth=%d", app.state, app.dialogs.Len())
			}
		})
	}
}
