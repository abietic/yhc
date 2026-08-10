package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestP474CtrlTProjectsMixedRowsInStableGroupOrder(t *testing.T) {
	snapshot := p474MixedSnapshot()
	panel := p474OpenMixedPanel(t, &snapshot)

	if len(panel.rows) != 4 {
		t.Fatalf("Ctrl+T rows = %d, want 4 mixed rows", len(panel.rows))
	}
	assertP474RowIdentity(t, panel.rows[0], "work", "work-2", 0)
	assertP474RowIdentity(t, panel.rows[1], "work", "work-1", 0)
	assertP474RowIdentity(t, panel.rows[2], "agent", "agent-a", 2)
	assertP474RowIdentity(t, panel.rows[3], "agent", "agent-a", 1)

	plain := stripANSIForTest(panel.Render(80, 24))
	workIndex := strings.Index(plain, "WorkItem")
	executionIndex := strings.Index(plain, "Execution")
	if workIndex < 0 || executionIndex < 0 || workIndex >= executionIndex {
		t.Fatalf("mixed textual groups missing or reordered:\n%s", plain)
	}
}

func TestP474SelectionIdentityRequiresOneExactCompositeKey(t *testing.T) {
	tests := []struct {
		name      string
		selection taskExplorerSelection
		want      bool
	}{
		{
			name: "exact WorkItem",
			selection: taskExplorerSelection{
				boardID: "board", workID: "work",
			},
			want: true,
		},
		{
			name: "exact execution",
			selection: taskExplorerSelection{
				agentID: "agent", generation: 1,
			},
			want: true,
		},
		{
			name: "partial WorkItem",
			selection: taskExplorerSelection{
				boardID: "board",
			},
		},
		{
			name: "execution without generation",
			selection: taskExplorerSelection{
				agentID: "agent",
			},
		},
		{
			name: "execution with negative generation",
			selection: taskExplorerSelection{
				agentID: "agent", generation: -1,
			},
		},
		{
			name: "mixed kinds",
			selection: taskExplorerSelection{
				boardID: "board", workID: "work",
				agentID: "agent", generation: 1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.selection.valid(); got != test.want {
				t.Fatalf("valid() = %v, want %v for %#v", got, test.want, test.selection)
			}
		})
	}
}

func TestP474RefreshPreservesExactIdentityAndFallsBackByPriorCursor(t *testing.T) {
	snapshot := p474MixedSnapshot()
	panel := p474OpenMixedPanel(t, &snapshot)
	if len(panel.rows) != 4 {
		t.Fatalf("initial rows = %d, want 4", len(panel.rows))
	}
	panel.move(3, 24)
	if panel.selection.agentID != "agent-a" || panel.selection.generation != 1 {
		t.Fatalf("initial selection = %#v", panel.selection)
	}

	snapshot.WorkItems = []engine.TaskExplorerWorkItem{
		{BoardID: "board", WorkItemID: "work-1", Title: "same label"},
		{BoardID: "board", WorkItemID: "work-2", Title: "same label"},
		{BoardID: "board", WorkItemID: "inserted", Title: "same label"},
	}
	snapshot.Executions = []engine.TaskExplorerExecution{
		p474Execution("other", 1),
		p474Execution("agent-a", 1),
		p474Execution("agent-a", 2),
	}
	panel.Refresh()
	if panel.cursor != 4 ||
		panel.selection.agentID != "agent-a" ||
		panel.selection.generation != 1 {
		t.Fatalf("reordered exact selection = cursor %d, %#v", panel.cursor, panel.selection)
	}

	snapshot.Executions = []engine.TaskExplorerExecution{
		p474Execution("other", 1),
		p474Execution("agent-a", 2),
	}
	panel.Refresh()
	if panel.cursor != 4 ||
		panel.selection.agentID != "agent-a" ||
		panel.selection.generation != 2 {
		t.Fatalf("removed identity fallback = cursor %d, %#v", panel.cursor, panel.selection)
	}

	panel.detail = true
	panel.offset = 3
	snapshot.WorkItems = nil
	snapshot.Executions = nil
	panel.Refresh()
	if panel.cursor != -1 || panel.selection.valid() || panel.detail || panel.offset != 0 {
		t.Fatalf(
			"empty reset = cursor %d selection %#v detail=%v offset=%d",
			panel.cursor,
			panel.selection,
			panel.detail,
			panel.offset,
		)
	}
}

func TestP474RefreshUsesWorkItemIdentityInsteadOfSameLabel(t *testing.T) {
	snapshot := p474MixedSnapshot()
	panel := p474OpenMixedPanel(t, &snapshot)
	panel.move(1, 24)
	if panel.selection.workID != "work-1" {
		t.Fatalf("initial WorkItem selection = %#v", panel.selection)
	}

	snapshot.WorkItems[0], snapshot.WorkItems[1] = snapshot.WorkItems[1], snapshot.WorkItems[0]
	panel.Refresh()
	if panel.cursor != 0 || panel.selection.workID != "work-1" {
		t.Fatalf("same-label WorkItem selection = cursor %d, %#v", panel.cursor, panel.selection)
	}
}

func TestP474ResponsiveNoColorRowsKeepKindAndSelection(t *testing.T) {
	capabilities := interactiveTerminalCaps()
	capabilities.Color = terminalcap.ColorNone
	app := New(Config{
		Resumed: true, ReducedMotion: true, TerminalCaps: &capabilities,
	})
	snapshot := p474MixedSnapshot()
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})
	app.enterTaskPanel()
	panel := app.taskExplorer
	if len(panel.rows) != 4 {
		t.Fatalf("initial rows = %d, want 4", len(panel.rows))
	}
	panel.move(3, 30)
	wantSelection := panel.selection

	sizes := []struct {
		width  int
		height int
	}{
		{width: 40, height: 20},
		{width: 80, height: 24},
		{width: 120, height: 30},
		{width: 180, height: 30},
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			app.width, app.height = size.width, size.height
			app.updateLayout()
			frame := app.renderView()
			if strings.Contains(frame, "\x1b") {
				t.Fatalf("no-color frame contains ANSI: %q", frame)
			}
			assertP313ExplorerFrame(t, frame, size.width, size.height, "WorkItem")
			if !strings.Contains(frame, "Execution") {
				t.Fatalf("frame omitted execution kind:\n%s", frame)
			}
			if panel.selection != wantSelection || panel.cursor != 3 {
				t.Fatalf(
					"resize changed selection: cursor %d selection %#v",
					panel.cursor,
					panel.selection,
				)
			}
		})
	}
}

func TestP474MixedRowsPreserveExactExecutionActions(t *testing.T) {
	snapshot := p474MixedSnapshot()
	snapshot.WorkItems = snapshot.WorkItems[:1]
	snapshot.Executions = snapshot.Executions[:1]
	panel := p474OpenMixedPanel(t, &snapshot)
	requests := make([]engine.TaskExplorerActionRequest, 0, 1)
	panel.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		requests = append(requests, request)
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Conflict:        "test_unavailable",
		}
	})

	for _, key := range []rune{'x', 's', 'p', 'c', 'n'} {
		panel.HandleKey(tea.KeyPressMsg{Code: key, Text: string(key)}, 24)
	}
	if len(requests) != 0 {
		t.Fatalf("WorkItem dispatched execution actions: %+v", requests)
	}

	panel.move(1, 24)
	panel.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}, 24)
	if len(requests) != 1 ||
		requests[0].AgentID != "agent-a" ||
		requests[0].Generation != 2 ||
		requests[0].Action != engine.TaskExplorerActionSwitch {
		t.Fatalf("exact execution request = %+v", requests)
	}
}

func p474OpenMixedPanel(
	t *testing.T,
	snapshot *engine.TaskExplorerSnapshot,
) *TaskExplorerPanel {
	t.Helper()
	app := New(Config{Resumed: true})
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return *snapshot
	})
	app.enterTaskPanel()
	return app.taskExplorer
}

func p474MixedSnapshot() engine.TaskExplorerSnapshot {
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "work-2", Title: "same label",
			Status: "pending",
		},
		engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "work-1", Title: "same label",
			Status: "pending",
		},
	)
	snapshot.Revision = engine.TaskExplorerRevision{Board: 4, Runtime: 7}
	snapshot.Executions = []engine.TaskExplorerExecution{
		p474Execution("agent-a", 2),
		p474Execution("agent-a", 1),
	}
	return snapshot
}

func p474Execution(agentID string, generation int64) engine.TaskExplorerExecution {
	execution := p313Execution(
		agentID,
		generation,
		engine.TaskExplorerExecutionRunning,
	)
	execution.Name = "same label"
	execution.ThreadID = "shared-thread"
	execution.AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
		engine.TaskExplorerActionSwitch,
		engine.TaskExplorerActionSend,
		engine.TaskExplorerActionPause,
		engine.TaskExplorerActionCancel,
		engine.TaskExplorerActionContinue,
	}
	return execution
}

func assertP474RowIdentity(
	t *testing.T,
	row taskExplorerRow,
	kind string,
	id string,
	generation int64,
) {
	t.Helper()
	switch kind {
	case "work":
		if row.work == nil || row.execution != nil || row.selection.workID != id {
			t.Fatalf("WorkItem row = %#v", row)
		}
	case "agent":
		if row.execution == nil || row.work != nil ||
			row.selection.agentID != id ||
			row.selection.generation != generation {
			t.Fatalf("execution row = %#v", row)
		}
	default:
		t.Fatalf("unknown row kind %q", kind)
	}
}
