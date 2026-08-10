//go:build unix

package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

const (
	tuiWorkflowHelperEnv = "YHC_TUI_WORKFLOW_HELPER"

	ptyBackgroundOpenMarker   = "OPENBG"
	ptyBackgroundClosedMarker = "CLOSEB"
	ptyTeamsOpenMarker        = "OPENTM"
	ptyTeamsClosedMarker      = "CLOSET"
	ptyExplorerOpenMarker     = "OPENTX"
	ptyExplorerClosedMarker   = "CLOSETX"
)

func TestTUIWorkflowPTY(t *testing.T) {
	if os.Getenv(tuiWorkflowHelperEnv) == "1" {
		runTUIWorkflowHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestTUIWorkflowPTY$")
	command.Env = append(os.Environ(), tuiWorkflowHelperEnv+"=1")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start workflow PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck

	output := newLockedPTYOutput(80, 24)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 8192)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				output.append(buffer[:count])
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitPTYContains(t, command, output, "PTY workflow target line 290")
	writePTY(t, terminal, "\x1b[200~PTY_PASTE\nSECOND_LINE\x1b[201~")
	waitPTYContains(t, command, output, "PTY_PASTE")
	waitPTYContains(t, command, output, "SECOND_LINE")

	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 100, Rows: 28}); err != nil {
		t.Fatalf("resize workflow PTY: %v", err)
	}
	output.setSize(100, 28)
	waitPTYContains(t, command, output, "100x28")

	mark := output.size()
	writePTY(t, terminal, "\x1b[<0;5;5M")
	writePTY(t, terminal, "\x1b[<32;16;5M")
	waitPTYContainsAfter(t, command, output, mark, "100x28 SEL DRAFT")
	writePTY(t, terminal, "\x1b[<0;16;5m")

	// Escape must be sent as separate terminal keys; adjacent ESC bytes are an
	// ambiguous Alt sequence in a real parser.
	mark = output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYTranscriptContainsAfter(
		t,
		command,
		output,
		mark,
		"NOSEL DRAFT",
	)
	mark = output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYTranscriptContainsAfter(
		t,
		command,
		output,
		mark,
		"EMPTY",
	)
	attentionStarted := time.Now()
	writePTY(t, terminal, "/agent\r")
	waitPTYContains(t, command, output, "Agent threads")
	waitPTYContains(t, command, output, "beta builder")
	writePTY(t, terminal, "alpha scout\r")
	waitPTYContains(t, command, output, "inspect runtime")
	waitPTYContains(t, command, output, "Bash command")
	waitPTYContains(t, command, output, "make test")
	t.Logf("attention locate and first transcript: actions=3 elapsed=%s", time.Since(attentionStarted))
	writePTY(t, terminal, "a")
	waitPTYContains(t, command, output, "thread:@alpha scout")
	mark = output.size()
	writePTY(t, terminal, "/agent\rmain\r")
	waitPTYContainsAfter(t, command, output, mark, "thread:main")

	evictedStarted := time.Now()
	writePTY(t, terminal, "/agent\r")
	waitPTYContains(t, command, output, "Agent threads")
	writePTY(t, terminal, "gamma reviewer")
	waitPTYContains(t, command, output, "failed")
	waitPTYContains(t, command, output, "disk")
	writePTY(t, terminal, "\r")
	waitPTYContains(t, command, output, "failure explanation preserved in TUI")
	waitPTYContains(t, command, output, "thread:@gamma reviewer")
	t.Logf("failed evicted transcript retrieval: actions=3 elapsed=%s", time.Since(evictedStarted))
	mark = output.size()
	writePTY(t, terminal, "/agent\rmain\r")
	waitPTYContainsAfter(t, command, output, mark, "thread:main")

	explorerStarted := time.Now()
	mark = output.size()
	writePTY(t, terminal, "\x14")
	waitPTYContainsAfter(t, command, output, mark, "Task Explorer")
	waitPTYContainsAfter(t, command, output, mark, "PTY logical work")
	writePTY(t, terminal, "f")
	waitPTYContainsAfter(t, command, output, mark, "[active]")
	writePTY(t, terminal, "/")
	// Incremental terminal repaint writes only the changed "editing" suffix on
	// the control row, while the stable footer is emitted as a complete line.
	waitPTYContainsAfter(t, command, output, mark, "Search · Enter apply")
	writePTY(t, terminal, "logical\r")
	waitPTYContainsAfter(t, command, output, mark, "Focus: list")
	waitPTYContainsAfter(t, command, output, mark, "PTY logical work")
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(t, command, output, mark, "Focus: detail")
	writePTY(t, terminal, "l")
	waitPTYContainsAfter(t, command, output, mark, "Tabs: overview [activity]")
	writePTY(t, terminal, "G")
	waitPTYContainsAfter(t, command, output, mark, "PTY exact diagnostic 11")
	// Clear the WorkItem-only search and select the exact gamma execution. The
	// deep tabs must never be inferred from WorkItem capability or dispatch.
	writePTY(t, terminal, "\x1b[Z")
	waitPTYContainsAfter(t, command, output, mark, "Focus: list")
	writePTY(t, terminal, "/")
	writePTY(t, terminal, "\x15")
	writePTY(t, terminal, "\r")
	// The earlier active filter excludes the terminal gamma row; return to all
	// rows before selecting its exact execution generation.
	writePTY(t, terminal, "f")
	writePTY(t, terminal, "f")
	writePTY(t, terminal, "f")
	waitPTYContainsAfter(t, command, output, mark, "Filter: [all]")
	writePTY(t, terminal, "g")
	writePTY(t, terminal, "\x1b[B")
	waitPTYContainsAfter(t, command, output, mark, "Execution agent-alpha@g1")
	writePTY(t, terminal, "\x1b[B")
	waitPTYContainsAfter(t, command, output, mark, "Execution agent-beta@g1")
	writePTY(t, terminal, "\x1b[B")
	waitPTYContainsAfter(t, command, output, mark, "gamma reviewer")
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(t, command, output, mark, "Detail · Execution")
	writePTY(t, terminal, "l")
	waitPTYContainsAfter(t, command, output, mark, "Tabs: overview [activity]")
	writePTY(t, terminal, "\x1b[C")
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		"Tabs: overview activity [transcript] output lineage",
	)
	waitPTYContainsAfter(t, command, output, mark, "failure explanation preserved in TUI")
	writePTY(t, terminal, "l")
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		"Tabs: overview activity transcript [output] lineage",
	)
	waitPTYContainsAfter(t, command, output, mark, "bounded output tail remains available")
	writePTY(t, terminal, "\x1b[C")
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		"Tabs: overview activity transcript output [lineage]",
	)
	waitPTYContainsAfter(t, command, output, mark, "Thread: child-gamma")
	explorerResizeMark := output.size()
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 64, Rows: 22}); err != nil {
		t.Fatalf("resize Task Explorer PTY: %v", err)
	}
	output.setSize(64, 22)
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Task Explorer")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "[all]")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "[lineage]")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Thread: child-gamma")
	writePTY(t, terminal, "hhh")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Tabs: overview [activity]")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Tab focus")
	writePTY(t, terminal, "g")
	writePTY(t, terminal, "h")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Tabs: [overview] activity")
	writePTY(t, terminal, "\x1b[Z")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Focus: list")
	writePTY(t, terminal, "f")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "[active]")
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Focus: list")
	explorerResizeMark = output.size()
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 100, Rows: 28}); err != nil {
		t.Fatalf("restore Task Explorer PTY size: %v", err)
	}
	output.setSize(100, 28)
	waitPTYContainsAfter(t, command, output, explorerResizeMark, "Task Explorer")
	closeMark := output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYTranscriptContainsAfter(t, command, output, closeMark, ptyExplorerClosedMarker)
	backgroundMark := output.size()
	writePTY(t, terminal, "\x02")
	waitPTYTranscriptContainsAfter(t, command, output, backgroundMark, ptyBackgroundOpenMarker)
	waitPTYContainsAfter(t, command, output, backgroundMark, "Background Tasks")
	waitPTYContainsAfter(t, command, output, backgroundMark, "build feature")
	backgroundCloseMark := output.size()
	// Ctrl+B may open either the list or the latest Agent detail. Ctrl+C closes
	// this passive overlay from either depth without interrupting the live turn.
	writePTY(t, terminal, "\x03")
	waitPTYTranscriptContainsAfter(t, command, output, backgroundCloseMark, ptyBackgroundClosedMarker)
	t.Logf(
		"explorer open/filter/search/focus/resize/Ctrl+B/close: elapsed=%s",
		time.Since(explorerStarted),
	)

	monitorStarted := time.Now()
	mark = output.size()
	writePTY(t, terminal, "/team\r")
	waitPTYTranscriptContainsAfter(t, command, output, mark, ptyTeamsOpenMarker)
	waitPTYContainsAfter(t, command, output, mark, "Multi-Agent monitor")
	waitPTYContainsAfter(t, command, output, mark, "beta builder")
	writePTY(t, terminal, "\x1b[B")
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(t, command, output, mark, "Read-only peek")
	waitPTYContainsAfter(t, command, output, mark, "build feature preview")
	switchMark := output.size()
	writePTY(t, terminal, "\r")
	waitPTYTranscriptContainsAfter(t, command, output, switchMark, ptyTeamsClosedMarker)
	waitPTYContainsAfter(t, command, output, mark, "thread:@beta builder")
	mark = output.size()
	writePTY(t, terminal, "/agent\rmain\r")
	waitPTYContainsAfter(t, command, output, mark, "thread:main")

	mark = output.size()
	writePTY(t, terminal, "/team\r")
	waitPTYTranscriptContainsAfter(t, command, output, mark, ptyTeamsOpenMarker)
	waitPTYContainsAfter(t, command, output, mark, "Multi-Agent monitor")
	waitPTYContainsAfter(t, command, output, mark, "completed")
	resizeMark := output.size()
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 64, Rows: 22}); err != nil {
		t.Fatalf("resize Agent monitor PTY: %v", err)
	}
	output.setSize(64, 22)
	waitPTYContainsAfter(t, command, output, resizeMark, "Multi-Agent monitor")
	waitPTYContainsAfter(t, command, output, resizeMark, "Tab peek · Enter switch · Esc close")
	closeMark = output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYTranscriptContainsAfter(t, command, output, closeMark, ptyTeamsClosedMarker)
	resizeMark = output.size()
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 100, Rows: 28}); err != nil {
		t.Fatalf("restore workflow PTY size: %v", err)
	}
	output.setSize(100, 28)
	waitPTYContainsAfter(
		t,
		command,
		output,
		resizeMark,
		"100x28 NOSEL EMPTY "+ptyTeamsClosedMarker+" "+ptyBackgroundClosedMarker,
	)
	t.Logf("monitor open/move/peek/switch/return/resize/completion: elapsed=%s", time.Since(monitorStarted))

	writePTY(t, terminal, "\x03")
	waitPTYContains(t, command, output, "Request interrupted")
	waitPTYContains(t, command, output, "CANCEL")
	writePTY(t, terminal, "\x04")

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("workflow helper failed: %v\n%s", waitErr, output.raw())
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		t.Fatalf("workflow helper timed out\n%s", output.raw())
	}
	_ = terminal.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("workflow PTY reader did not finish")
	}

	raw := output.raw()
	for _, sequence := range []string{
		"\x1b[?1049h", "\x1b[?1049l",
		"\x1b[?2004h", "\x1b[?2004l",
		"\x1b[?1002h", "\x1b[?1002l",
		"\x1b[?1006h", "\x1b[?1006l",
		"\x1b[?25h",
	} {
		if !strings.Contains(raw, sequence) {
			t.Fatalf("workflow terminal output missing cleanup %q\n%q", sequence, raw)
		}
	}
	cleanupIndex := strings.LastIndex(raw, "\x1b[?1049l")
	restoredIndex := strings.LastIndex(raw, "PTY_HELPER_RESTORED")
	if cleanupIndex < 0 || restoredIndex <= cleanupIndex {
		t.Fatalf("restore marker did not follow terminal cleanup: cleanup=%d marker=%d\n%q", cleanupIndex, restoredIndex, raw)
	}
}

func runTUIWorkflowHelper(t *testing.T) {
	t.Helper()
	app, catalog, details := newThreadNavigationTestApp(t)
	for index := range catalog.Threads {
		if catalog.Threads[index].ThreadID == "child-alpha" {
			catalog.Threads[index].Mode = engine.ThreadModeLiveAttach
			catalog.Threads[index].Status = engine.RuntimeThreadWaitingInput
			catalog.Threads[index].PermissionCount = 1
		}
	}
	alpha := details["agent-alpha"]
	alpha.Agent.Status = "waiting_input"
	alpha.Thread.Status = engine.RuntimeThreadWaitingInput
	details["agent-alpha"] = alpha
	startedAt := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	catalog.Threads = append(catalog.Threads, engine.RuntimeThreadCatalogEntry{
		ThreadID: "child-gamma", AgentID: "agent-gamma", Name: "gamma reviewer",
		Status: engine.RuntimeThreadFailed, Mode: engine.ThreadModeEvictedTranscript,
		StartedAt: startedAt,
	})
	details["agent-gamma"] = engine.AgentDetailSnapshot{
		Revision: 1,
		Agent: engine.RuntimeAgentSnapshot{
			AgentID: "agent-gamma", Generation: 1,
			SessionID: "session-gamma", ThreadID: "child-gamma",
			Name: "gamma reviewer", Status: "failed",
			Error: "model request failed", TranscriptPath: "/tmp/agent-gamma.jsonl", OutputFile: "/tmp/agent-gamma.output",
		},
		Thread: engine.RuntimeThreadSnapshot{
			ThreadID: "child-gamma", AgentID: "agent-gamma", Status: engine.RuntimeThreadFailed,
			LastTerminal: &engine.RuntimeTerminalSnapshot{Reason: engine.TerminalModelError, Error: "model request failed"},
		},
		Messages: []engine.AgentDetailMessage{{
			ID: "gamma-system", Role: "system", Content: "failure explanation preserved in TUI", Completed: true,
		}},
		Output: "bounded output tail remains available", Storage: "evicted",
	}
	var monitorFirstSeen time.Time
	monitorComplete := func() bool {
		return !monitorFirstSeen.IsZero() && time.Since(monitorFirstSeen) >= 1200*time.Millisecond
	}
	explorerProvider := func() engine.TaskExplorerSnapshot {
		if app.teamsPanel.Visible() && monitorFirstSeen.IsZero() {
			monitorFirstSeen = time.Now()
		}
		betaPhase, betaStatus, betaActivity := engine.TaskExplorerExecutionRunning, "running", "building feature"
		if monitorComplete() {
			betaPhase, betaStatus, betaActivity = engine.TaskExplorerExecutionCompleted, "completed", "build completed"
		}
		workItem := engine.TaskExplorerWorkItem{
			BoardID: "pty-board", WorkItemID: "pty-work",
			Title: "PTY logical work", ActiveForm: "Inspect PTY explorer",
			Status: "in_progress", LinkedLive: true,
			ExecutionKeys: []engine.RuntimeExecutionKey{
				{AgentID: "agent-alpha", Generation: 1},
				{AgentID: "agent-beta", Generation: 1},
				{AgentID: "agent-gamma", Generation: 1},
			},
		}
		snapshot := p313ExplorerSnapshot(workItem)
		snapshot.Executions = []engine.TaskExplorerExecution{
			{Key: engine.RuntimeExecutionKey{AgentID: "agent-alpha", Generation: 1}, SessionID: "session-alpha", ThreadID: "child-alpha", Name: "alpha scout", Task: "inspect runtime", Status: "waiting_input", Phase: engine.TaskExplorerExecutionWaitingInput, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}},
			{Key: engine.RuntimeExecutionKey{AgentID: "agent-beta", Generation: 1}, SessionID: "session-beta", ThreadID: "child-beta", Name: "beta builder", Task: "build feature", Activity: betaActivity, Status: betaStatus, Phase: betaPhase, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}},
			{Key: engine.RuntimeExecutionKey{AgentID: "agent-gamma", Generation: 1}, SessionID: "session-gamma", ThreadID: "child-gamma", Name: "gamma reviewer", Task: "review failure", Status: "failed", Phase: engine.TaskExplorerExecutionFailed, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}},
		}
		for index, execution := range snapshot.Executions {
			snapshot.Links = append(snapshot.Links, engine.TaskExplorerLink{
				WorkExecutionLink: engine.WorkExecutionLink{
					BoardID: "pty-board", WorkItemID: "pty-work",
					AgentID: execution.Key.AgentID, Generation: execution.Key.Generation,
				},
				State:             engine.TaskExplorerLinkValid,
				UnavailableReason: fmt.Sprintf("PTY exact link %02d", index),
			})
		}
		for index := 0; index < 12; index++ {
			snapshot.Diagnostics = append(snapshot.Diagnostics, engine.TaskExplorerDiagnostic{
				Kind: "pty", ItemID: "pty-work",
				Message: fmt.Sprintf("PTY exact diagnostic %02d", index),
			})
		}
		return snapshot
	}
	baseCatalogProvider := app.threadCatalogProvider
	app.taskExplorerSnapshotSource = explorerProvider
	app.threadCatalogProvider = func() engine.RuntimeThreadCatalogSnapshot {
		snapshot := baseCatalogProvider()
		if monitorComplete() {
			for index := range snapshot.Threads {
				if snapshot.Threads[index].AgentID == "agent-beta" {
					snapshot.Threads[index].Status = engine.RuntimeThreadCompleted
					snapshot.Threads[index].CompletedAt = time.Now()
				}
			}
		}
		return snapshot
	}
	app.taskExplorer.SetSnapshotProvider(explorerProvider)
	app.taskExplorer.SetTranscriptProvider(func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		execution, ok := taskExplorerExecutionByAgent(
			explorerProvider(),
			request.AgentID,
		)
		if !ok || execution.Key.Generation != request.Generation {
			return engine.AgentTranscriptPage{}, false, nil
		}
		detail, ok := details[request.AgentID]
		if !ok {
			return engine.AgentTranscriptPage{}, false, nil
		}
		messages := make(
			[]engine.AgentTranscriptMessage,
			0,
			len(detail.Messages),
		)
		for _, message := range detail.Messages {
			messages = append(messages, engine.AgentTranscriptMessage{
				ID: message.ID, TranscriptEntryID: message.ID,
				Role: message.Role, Content: message.Content,
				Completed: message.Completed,
			})
		}
		return engine.AgentTranscriptPage{
			Revision: 1, AgentID: request.AgentID,
			SessionID: execution.SessionID, ThreadID: execution.ThreadID,
			Generation: execution.Key.Generation,
			AttachMode: engine.ThreadModeReplayOnly,
			Storage:    "durable",
			Messages:   messages,
		}, true, nil
	})
	app.taskExplorer.SetExecutionDetailProvider(func(
		request engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		execution, ok := taskExplorerExecutionByAgent(
			explorerProvider(),
			request.AgentID,
		)
		if !ok || execution.Key.Generation != request.Generation ||
			execution.SessionID != request.SessionID ||
			execution.ThreadID != request.ThreadID {
			return engine.AgentExecutionDetail{}, false, nil
		}
		detail, ok := details[request.AgentID]
		if !ok || detail.Agent.AgentID != request.AgentID ||
			detail.Agent.Generation != request.Generation ||
			detail.Agent.SessionID != request.SessionID ||
			detail.Agent.ThreadID != request.ThreadID {
			return engine.AgentExecutionDetail{}, false, nil
		}
		result := engine.AgentExecutionDetail{
			Revision: detail.Revision,
			Agent:    detail.Agent,
		}
		if request.IncludeOutput {
			result.Output = detail.Output
			result.OutputTruncated = detail.OutputTruncated
			result.LoadError = detail.LoadError
		}
		return result, true, nil
	})
	app.backgroundTasks.SetExplorerSnapshotProvider(explorerProvider)
	app.teamsPanel.SetExplorerSnapshotProvider(explorerProvider)
	app.teamsPanel.SetDetailProvider(app.agentDetailProvider)
	app.teamsPanel.SetTranscriptSelectionProvider(func(agentID string) (agentTranscriptSelection, bool) {
		snapshot := explorerProvider()
		for _, execution := range snapshot.Executions {
			if execution.Key.AgentID == agentID {
				mode := engine.ThreadModeLiveAttach
				for _, thread := range app.threadCatalogProvider().Threads {
					if thread.AgentID == agentID {
						mode = thread.Mode
						break
					}
				}
				return agentTranscriptSelectionFromExecution(execution, mode), true
			}
		}
		return agentTranscriptSelection{}, false
	})
	app.teamsPanel.SetTranscriptProvider(func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		execution, ok := taskExplorerExecutionByAgent(explorerProvider(), request.AgentID)
		if !ok || execution.Key.Generation != request.Generation {
			return engine.AgentTranscriptPage{}, false, nil
		}
		content := execution.Task + " preview"
		return engine.AgentTranscriptPage{
			Revision: 1, AgentID: request.AgentID, SessionID: execution.SessionID, ThreadID: execution.ThreadID,
			Generation: execution.Key.Generation, AttachMode: engine.ThreadModeLiveAttach, Storage: "durable",
			Messages: []engine.AgentTranscriptMessage{{
				ID: request.AgentID + "-preview", TranscriptEntryID: request.AgentID + "-preview",
				Role: "assistant", Content: content, Completed: true,
			}},
		}, true, nil
	})
	for index := 0; index < 300; index++ {
		app.chat.AppendSystem(fmt.Sprintf("PTY workflow target line %02d", index))
	}
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "pty-permission", ThreadID: "child-alpha", AgentID: "agent-alpha",
		Kind: threadAttentionPermission, Tool: "Bash", Input: `{"command":"make test"}`, Source: "prompter",
	})
	cancelled := false
	app.running = true
	app.cancelFn = func() { cancelled = true }
	app.sessionStart = time.Now()
	app.reducedMotion = true
	app.fullscreen = true
	app.mouseEnabled = true
	app.terminalCaps = terminalcap.Capabilities{
		Platform: "linux", Terminal: "wezterm", Interactive: true,
		FocusReporting: true, Mouse: true, BracketedPaste: true,
	}
	app.statusLineHook = func(left, _ string) (string, string) {
		marker := fmt.Sprintf(" %dx%d", app.width, app.height)
		if app.selection.HasSelection() {
			marker += " SEL"
		} else {
			marker += " NOSEL"
		}
		if app.textarea.Value() == "" {
			marker += " EMPTY"
		} else {
			marker += " DRAFT"
		}
		if app.hasDialog(StateTeams) {
			marker += " " + ptyTeamsOpenMarker
		} else {
			marker += " " + ptyTeamsClosedMarker
		}
		if app.hasDialog(StateBackgroundTasks) {
			marker += " " + ptyBackgroundOpenMarker
		} else {
			marker += " " + ptyBackgroundClosedMarker
		}
		if app.state == StateTaskPanel {
			marker += " " + ptyExplorerOpenMarker
		} else {
			marker += " " + ptyExplorerClosedMarker
		}
		if cancelled {
			marker += " CANCEL"
		}
		// Keep synchronization markers at the start so narrow PTY frames cannot
		// truncate the exact dialog-stack state needed by the parent process. Each
		// open/closed pair differs at every byte so incremental repaint emits the
		// complete new marker instead of one changed cell.
		return marker + " " + left, "pty"
	}

	program := tea.NewProgram(app)
	app.SetProgram(program)
	if _, err := program.Run(); err != nil {
		t.Fatalf("run workflow TUI: %v", err)
	}
	fmt.Fprint(os.Stdout, "PTY_HELPER_RESTORED")
}

func taskExplorerExecutionByAgent(snapshot engine.TaskExplorerSnapshot, agentID string) (engine.TaskExplorerExecution, bool) {
	for _, execution := range snapshot.Executions {
		if execution.Key.AgentID == agentID {
			return execution, true
		}
	}
	return engine.TaskExplorerExecution{}, false
}

type lockedPTYOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	emulator *vt.Emulator
}

func newLockedPTYOutput(width, height int) *lockedPTYOutput {
	emulator := vt.NewEmulator(width, height)
	go func() {
		_, _ = io.Copy(io.Discard, emulator)
	}()
	return &lockedPTYOutput{emulator: emulator}
}

func (o *lockedPTYOutput) append(value []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.buffer.Write(value)
	if o.emulator != nil {
		_, _ = o.emulator.Write(value)
	}
}

func (o *lockedPTYOutput) raw() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func (o *lockedPTYOutput) size() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Len()
}

func (o *lockedPTYOutput) setSize(width, height int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.emulator != nil {
		o.emulator.Resize(width, height)
	}
}

func (o *lockedPTYOutput) screenPlain() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.emulator == nil {
		return ""
	}
	return o.emulator.String()
}

func (o *lockedPTYOutput) plainAfter(offset int) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	raw := o.buffer.String()
	if offset < 0 || offset > len(raw) {
		offset = 0
	}
	return xansi.Strip(raw[offset:])
}

func (o *lockedPTYOutput) plain() string {
	return xansi.Strip(o.raw())
}

func waitPTYContains(t *testing.T, command *exec.Cmd, output *lockedPTYOutput, needle string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.plain(), needle) ||
			strings.Contains(output.screenPlain(), needle) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("workflow helper exited before %q\n%s", needle, output.raw())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait for workflow output %q timed out\n%s", needle, output.raw())
}

func waitPTYContainsAfter(t *testing.T, command *exec.Cmd, output *lockedPTYOutput, offset int, needle string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if output.size() > offset &&
			(strings.Contains(output.plainAfter(offset), needle) ||
				strings.Contains(output.screenPlain(), needle)) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("workflow helper exited before new output %q\n%s", needle, output.raw())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wait for new workflow output %q timed out\n%s", needle, output.raw())
}

func waitPTYTranscriptContainsAfter(
	t *testing.T,
	command *exec.Cmd,
	output *lockedPTYOutput,
	offset int,
	needle string,
) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.plainAfter(offset), needle) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf(
				"workflow helper exited before transcript output %q\n%s",
				needle,
				output.raw(),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(
		"wait for new workflow transcript output %q timed out\n%s",
		needle,
		output.raw(),
	)
}

func writePTY(t *testing.T, terminal *os.File, value string) {
	t.Helper()
	if _, err := terminal.Write([]byte(value)); err != nil {
		t.Fatalf("write workflow PTY input %q: %v", value, err)
	}
}
