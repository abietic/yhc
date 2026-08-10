package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
)

func TestBackgroundTasksAgentDetailProvidesAllViewsAndRevisionRefresh(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	detail := agentDetailFixture(now)
	snapshot := agentListFixture(detail)
	providerCalls := 0
	panel := NewBackgroundTasksPanel(defaultStyles())
	panel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetDetailProvider(func(agentID string) (engine.AgentDetailSnapshot, bool) {
		providerCalls++
		return detail, agentID == detail.Agent.AgentID
	})
	panel.Show()
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 30)

	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "Overview", "Status: failed", "Pending input: 2", "Attention: 1 unresolved", "Terminal: model_error")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "Activity", "Task: Inspect the runtime", "Recent activity", "Read: Reading runtime_state.go")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "Transcript", "USER", "Inspect the runtime", "ASSISTANT", "Tool: Read")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "Output", "bounded final output", "agent-detail.out")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "Lineage", "Parent thread: leader-thread", "Spawn tool: spawn-detail", "Transcript:")

	panel.Refresh()
	if providerCalls != 1 {
		t.Fatalf("unchanged terminal revision reloaded detail %d times, want 1", providerCalls)
	}
	detail.Revision = 8
	detail.Agent.Status = "completed"
	detail.Messages = append(detail.Messages, engine.AgentDetailMessage{ID: "message-4", Role: "assistant", Content: "new live tail", Completed: true})
	snapshot.Revision.Runtime = 8
	panel.Refresh()
	panel.detailTab = agentDetailTranscript
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "new live tail")
	if providerCalls != 2 {
		t.Fatalf("changed revision detail calls = %d, want 2", providerCalls)
	}
}

func TestTeamsAndBackgroundPanelsShareAgentTranscriptDetail(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	detail := agentDetailFixture(now)
	snapshot := agentListFixture(detail)
	provider := func(agentID string) (engine.AgentDetailSnapshot, bool) {
		return detail, agentID == detail.Agent.AgentID
	}

	background := NewBackgroundTasksPanel(defaultStyles())
	background.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	background.SetDetailProvider(provider)
	background.Show()
	background.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 30)
	background.detailTab = agentDetailTranscript
	backgroundView := stripANSIForTest(background.renderOutputView(72, 30))

	teams := NewTeamsPanel(defaultStyles())
	teams.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	teams.SetDetailProvider(provider)
	teams.Show()
	teams.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	teamsView := stripANSIForTest(teams.renderDetailView(72, 30))

	for _, expected := range []string{"USER", "Inspect the runtime", "ASSISTANT", "Tool: Read"} {
		if !strings.Contains(backgroundView, expected) || !strings.Contains(teamsView, expected) {
			t.Fatalf("shared transcript %q missing:\nbackground=%q\nteams=%q", expected, backgroundView, teamsView)
		}
	}
}

func TestAgentDetailLinesAndTabsFitNarrowContentWidth(t *testing.T) {
	detail := agentDetailFixture(time.Now())
	detail.Agent.Task = strings.Repeat("很长的任务说明", 20)
	detail.Agent.TranscriptPath = "/very/long/unbroken/transcript/path/that/must/not/overflow/agent.jsonl"
	for tab := agentDetailOverview; tab < agentDetailTabCount; tab++ {
		lines := buildAgentDetailLines(detail, tab, 24, time.Now())
		for i, line := range lines {
			if width := xansi.StringWidth(line); width > 24 {
				t.Fatalf("tab %d line %d width = %d, want <= 24: %q", tab, i, width, line)
			}
		}
	}
	if width := xansi.StringWidth(renderAgentDetailTabs(defaultStyles(), agentDetailTranscript, 24)); width > 24 {
		t.Fatalf("narrow tab bar width = %d, want <= 24", width)
	}
}

func TestBackgroundAgentDetailKeyboardControlsSendResumeAndAbort(t *testing.T) {
	detail := agentDetailFixture(time.Now())
	detail.Agent.Status = "running"
	detail.Thread.Status = engine.RuntimeThreadRunning
	detail.Thread.LastTerminal = nil
	detail.Agent.Error = ""
	snapshot := agentListFixture(detail)
	snapshot.Executions[0].Phase = engine.TaskExplorerExecutionRunning
	snapshot.Executions[0].Status = "running"
	snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionSend, engine.TaskExplorerActionPause,
		engine.TaskExplorerActionCancel,
	}
	var sentAgent, sentContent string
	abortCalls := 0
	pauseCalls := 0
	resumeCalls := 0
	panel := NewBackgroundTasksPanel(defaultStyles())
	panel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetDetailProvider(func(agentID string) (engine.AgentDetailSnapshot, bool) {
		return detail, agentID == detail.Agent.AgentID
	})
	panel.SetActionProvider(func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		switch request.Action {
		case engine.TaskExplorerActionSend,
			engine.TaskExplorerActionContinue:
			sentAgent, sentContent = request.AgentID, request.Payload
			detail.PendingMessageCount++
			detail.Messages = append(
				detail.Messages,
				engine.AgentDetailMessage{
					ID:      "optimistic-1",
					Role:    "user",
					Content: request.Payload,
				},
			)
		case engine.TaskExplorerActionCancel:
			abortCalls++
			detail.Agent.Status = "aborted"
			detail.Thread.Status = engine.RuntimeThreadAborted
		case engine.TaskExplorerActionPause:
			pauseCalls++
			detail.SteeringState = "paused"
			snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
				engine.TaskExplorerActionSend,
				engine.TaskExplorerActionResume,
				engine.TaskExplorerActionCancel,
			}
		case engine.TaskExplorerActionResume:
			resumeCalls++
			detail.SteeringState = "running"
			snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
				engine.TaskExplorerActionSend,
				engine.TaskExplorerActionPause,
				engine.TaskExplorerActionCancel,
			}
		}
		detail.Revision++
		snapshot.Revision.Runtime = detail.Revision
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, Action: request.Action, Outcome: "accepted"}
	})
	panel.Show()
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 30)
	panel.detailTab = agentDetailTranscript

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'i'})}, 30)
	if !panel.control.active {
		t.Fatal("i did not enter Agent message input")
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("follow up now"))}, 30)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 30)
	if sentAgent != detail.Agent.AgentID || sentContent != "follow up now" || panel.control.notice != "Message queued" {
		t.Fatalf("queued detail input: agent=%q content=%q control=%#v", sentAgent, sentContent, panel.control)
	}
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "follow up now", "Message queued")

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'i'})}, 30)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 30)
	if panel.control.active || panel.subView != bgTaskViewOutput {
		t.Fatalf("Esc input cancellation changed detail view: control=%#v subview=%v", panel.control, panel.subView)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'p'})}, 30)
	if pauseCalls != 1 || panel.detail.SteeringState != "paused" || panel.control.notice != "Pause requested" {
		t.Fatalf("pause detail action: calls=%d detail=%#v control=%#v", pauseCalls, panel.detail, panel.control)
	}
	panel.detailTab = agentDetailOverview
	assertAgentDetailViewContains(t, panel.renderOutputView(72, 30), "Steering: paused", "p resume", "Pause requested")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'p'})}, 30)
	if resumeCalls != 1 || panel.detail.SteeringState != "running" || panel.control.notice != "Agent resumed" {
		t.Fatalf("resume detail action: calls=%d detail=%#v control=%#v", resumeCalls, panel.detail, panel.control)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'x'})}, 30)
	if abortCalls != 1 || panel.detail.Agent.Status != "aborted" || panel.control.notice != "Abort requested" {
		t.Fatalf("abort detail action: calls=%d detail=%#v control=%#v", abortCalls, panel.detail.Agent, panel.control)
	}

	detail.Agent.Status = "completed"
	detail.Thread.Status = engine.RuntimeThreadCompleted
	detail.Revision++
	snapshot.Revision.Runtime = detail.Revision
	snapshot.Executions[0].Status = "completed"
	snapshot.Executions[0].Phase = engine.TaskExplorerExecutionCompleted
	snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionContinue,
	}
	panel.Refresh()
	panel.SetActionProvider(func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		sentAgent, sentContent = request.AgentID, request.Payload
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, Action: request.Action, Outcome: "accepted"}
	})
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'i'})}, 30)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("resume work"))}, 30)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 30)
	if sentContent != "resume work" || panel.control.notice != "Agent resumed" {
		t.Fatalf("terminal resume control: content=%q control=%#v", sentContent, panel.control)
	}
}

func TestTeamsAgentPeekIsReadOnlyAndSelectsExistingThread(t *testing.T) {
	detail := agentDetailFixture(time.Now())
	detail.Agent.Status = "running"
	snapshot := agentListFixture(detail)
	snapshot.Executions[0].Phase = engine.TaskExplorerExecutionRunning
	snapshot.Executions[0].Status = "running"
	panel := NewTeamsPanel(defaultStyles())
	panel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetDetailProvider(func(agentID string) (engine.AgentDetailSnapshot, bool) {
		return detail, agentID == detail.Agent.AgentID
	})
	panel.Show()
	for _, key := range []string{"i", "x", "p", "s"} {
		panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune(key))}, 30)
	}
	if !panel.visible || panel.subView != teamsViewList || panel.switchThread != "" {
		t.Fatalf("mutation-like keys changed monitor: visible=%v subview=%v switch=%q", panel.visible, panel.subView, panel.switchThread)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	if panel.subView != teamsViewDetail {
		t.Fatal("Tab did not open the read-only peek")
	}
	for _, key := range []string{"i", "x", "p", "s"} {
		panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune(key))}, 30)
	}
	if panel.detail.SteeringState != detail.SteeringState || panel.switchThread != "" {
		t.Fatalf("mutation-like keys changed peek: detail=%#v switch=%q", panel.detail, panel.switchThread)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 30)
	if panel.visible || panel.takeSwitchThread() != detail.Agent.ThreadID {
		t.Fatalf("Enter selection did not request existing thread: visible=%v", panel.visible)
	}
}

func agentDetailFixture(now time.Time) engine.AgentDetailSnapshot {
	terminal := &engine.RuntimeTerminalSnapshot{
		TurnID:    "turn-detail",
		Reason:    engine.TerminalModelError,
		Error:     "upstream unavailable",
		Sequence:  12,
		Timestamp: now.Add(4 * time.Second),
	}
	return engine.AgentDetailSnapshot{
		Revision: 7,
		Agent: engine.RuntimeAgentSnapshot{
			AgentID:         "agent-detail",
			SessionID:       "agent-session",
			ThreadID:        "agent-thread",
			ParentSessionID: "leader-session",
			ParentThreadID:  "leader-thread",
			ParentAgentID:   "leader-agent",
			ParentToolUseID: "spawn-detail",
			Name:            "detail-worker",
			Task:            "Inspect the runtime",
			Description:     "Inspecting runtime details",
			AgentType:       "Explore",
			Model:           "small-model",
			PermissionMode:  "default",
			Isolation:       "worktree",
			CWD:             "/repo",
			WorktreePath:    "/repo/.worktrees/agent-detail",
			WorktreeBranch:  "agent/detail",
			TranscriptPath:  "/state/transcripts/agent-detail.jsonl",
			OutputFile:      "/state/agent-detail.out",
			Status:          "failed",
			Error:           "upstream unavailable",
			Generation:      2,
			StartedAt:       now,
			CompletedAt:     now.Add(4 * time.Second),
			Progress: engine.RuntimeAgentProgressSnapshot{
				ToolUses:     2,
				TotalTokens:  512,
				DurationMS:   4000,
				LastToolName: "Read",
				Summary:      "Inspected runtime state",
				RecentActivities: []engine.RuntimeAgentActivitySnapshot{{
					ToolName: "Read", Description: "Reading runtime_state.go", IsRead: true,
				}},
			},
		},
		Thread: engine.RuntimeThreadSnapshot{
			SessionID:       "agent-session",
			ThreadID:        "agent-thread",
			AgentID:         "agent-detail",
			ParentSessionID: "leader-session",
			ParentThreadID:  "leader-thread",
			ParentAgentID:   "leader-agent",
			ParentToolUseID: "spawn-detail",
			Status:          engine.RuntimeThreadFailed,
			LastTerminal:    terminal,
		},
		Messages: []engine.AgentDetailMessage{
			{ID: "message-1", Role: "user", Content: "Inspect the runtime", Completed: true},
			{ID: "message-2", Role: "assistant", Content: "I will inspect it.", ToolCalls: []engine.RuntimeToolCallSnapshot{{ID: "read-1", Name: "Read", InputPreview: `{"file_path":"runtime_state.go"}`}}, Completed: true},
			{ID: "message-3", Role: "tool", ToolName: "Read", Content: "file contents", Completed: true},
		},
		Output:              "bounded final output",
		OutputTruncated:     true,
		PendingMessageCount: 2,
		UnresolvedCount:     1,
		Storage:             "evicted",
	}
}

func agentListFixture(detail engine.AgentDetailSnapshot) engine.TaskExplorerSnapshot {
	phase := engine.TaskExplorerExecutionFailed
	if detail.Agent.Status == "running" {
		phase = engine.TaskExplorerExecutionRunning
	}
	return engine.TaskExplorerSnapshot{Available: true, BoardID: "detail-board", Revision: engine.TaskExplorerRevision{Board: detail.Revision, Runtime: detail.Revision}, Executions: []engine.TaskExplorerExecution{{
		Key:       engine.RuntimeExecutionKey{AgentID: detail.Agent.AgentID, Generation: detail.Agent.Generation},
		SessionID: detail.Agent.SessionID, ThreadID: detail.Agent.ThreadID, ParentSessionID: detail.Agent.ParentSessionID, ParentThreadID: detail.Agent.ParentThreadID, ParentAgentID: detail.Agent.ParentAgentID, ParentToolUseID: detail.Agent.ParentToolUseID,
		Name: detail.Agent.Name, Task: detail.Agent.Task, Description: detail.Agent.Description, Activity: detail.Agent.Progress.LastToolName, Status: detail.Agent.Status, Phase: phase,
		AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch},
	}}}
}

func assertAgentDetailViewContains(t *testing.T, view string, expected ...string) {
	t.Helper()
	plain := stripANSIForTest(view)
	for _, value := range expected {
		if !strings.Contains(plain, value) {
			t.Fatalf("Agent detail missing %q: %q", value, plain)
		}
	}
}
