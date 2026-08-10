package tui

import (
	"strings"
	"testing"
	"time"
)

func TestAgentHistoryRendererWaitingAndTranscriptLineage(t *testing.T) {
	now := time.Now()
	trace := agentToolTrace{
		AgentID: "agent-1234567890", Status: "waiting_input",
		Summary: "Inspecting the runtime", LastToolName: "Grep",
		TranscriptPath: "/tmp/transcripts/agent.jsonl", ToolUses: 2, TotalTokens: 1200,
		UnresolvedCount: 1, StartedAt: now.Add(-time.Second), UpdatedAt: now,
		RecentActivities: []agentToolTraceActivity{
			{ToolName: "Read", Description: "engine/query.go"},
			{ToolName: "Grep", Description: "ThreadID"},
		},
	}
	tool := &ToolMessage{
		toolCallID: "spawn-agent", name: "Agent",
		input:  `{"description":"inspect runtime","prompt":"Find lifecycle gaps","subagent_type":"Explore","model":"sonnet","run_in_background":true,"isolation":"worktree"}`,
		output: "full child narrative must stay out of parent history",
		status: ToolSuccess, version: 3, agentTrace: &trace,
	}
	rendered := tool.Render(80, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{
		"Agent", "needs input", "@agent-1234567890", "Explore", "inspect runtime",
		"Inspecting the runtime", "Read: engine/query.go", "1 request needs attention", "Open Agent details",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("agent rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "full child narrative") {
		t.Fatalf("agent rich embedded child output: %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 80)

	compact := stripANSIForTest(tool.RenderCompact(HistoryRenderContext{Width: 80, Styles: defaultStyles()}))
	if !strings.Contains(compact, "needs input") || strings.Contains(compact, "Inspecting the runtime") {
		t.Fatalf("agent compact = %q", compact)
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 80, Styles: defaultStyles()})
	for _, want := range []string{
		"Agent agent-1234567890", "Status: needs input", "Prompt: Find lifecycle gaps",
		"Model: sonnet", "Isolation: worktree", "Activity: Grep: ThreadID",
		"Attention: 1 unresolved", "Transcript: /tmp/transcripts/agent.jsonl",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("agent transcript missing %q: %q", want, transcript)
		}
	}
}

func TestAgentHistoryRendererTerminalAndLaunchFallback(t *testing.T) {
	launch := &ToolMessage{
		name: "Agent", input: `{"description":"review code","prompt":"Review it"}`,
		status: ToolRunning, version: 1,
	}
	launchPlain := stripANSIForTest(launch.Render(48, defaultStyles()))
	if !strings.Contains(launchPlain, "launching") || !strings.Contains(launchPlain, "review code") {
		t.Fatalf("agent launch = %q", launchPlain)
	}

	trace := agentToolTrace{
		AgentID: "agent-terminal", Status: "failed", TerminalReason: "model_error",
		Error: "upstream failed", TranscriptPath: "/tmp/agent-terminal.jsonl",
	}
	terminal := &ToolMessage{
		name: "Agent", input: `{"description":"review code"}`,
		status: ToolSuccess, version: 4, agentTrace: &trace,
	}
	terminalPlain := stripANSIForTest(terminal.Render(56, defaultStyles()))
	for _, want := range []string{"failed", "model_error", "upstream failed", "Open Agent details"} {
		if !strings.Contains(terminalPlain, want) {
			t.Fatalf("terminal Agent missing %q: %q", want, terminalPlain)
		}
	}
	if !terminal.Finished() || terminal.HistoryAnimationVersion(9) != terminal.Version() {
		t.Fatalf("terminal Agent finished/version = %v/%d", terminal.Finished(), terminal.HistoryAnimationVersion(9))
	}
}

func TestAgentHistoryNestedItemsHaveStableIdentity(t *testing.T) {
	trace := agentToolTrace{
		AgentID: "agent-nested", Status: "running", Summary: "working",
		RecentActivities: []agentToolTraceActivity{
			{ToolName: "Read", Description: "a.go"},
			{ToolName: "Read", Description: "a.go"},
			{ToolName: "Bash", Description: "go test ./..."},
		},
	}
	tool := &ToolMessage{
		toolCallID: "spawn-nested", name: "Agent", status: ToolSuccess, version: 7, agentTrace: &trace,
	}
	first := tool.NestedHistoryItems()
	second := tool.NestedHistoryItems()
	if len(first) != 1 || len(second) != 1 || first[0].ID() != "agent:agent-nested" || first[0].ID() != second[0].ID() {
		t.Fatalf("nested trace IDs = %#v / %#v", first, second)
	}
	traceItem, ok := first[0].(HistoryNestedItem)
	if !ok {
		t.Fatalf("trace item capability = %T", first[0])
	}
	activities := traceItem.NestedHistoryItems()
	activitiesAgain := second[0].(HistoryNestedItem).NestedHistoryItems()
	if len(activities) != 3 || len(activitiesAgain) != 3 {
		t.Fatalf("activity counts = %d/%d", len(activities), len(activitiesAgain))
	}
	seen := make(map[string]bool)
	for i, activity := range activities {
		if seen[activity.ID()] || activity.ID() != activitiesAgain[i].ID() {
			t.Fatalf("activity ID %d = %q / %q", i, activity.ID(), activitiesAgain[i].ID())
		}
		seen[activity.ID()] = true
	}
	if raw := activities[2].Raw(HistoryRenderContext{}); raw != "Bash: go test ./..." {
		t.Fatalf("activity raw = %q", raw)
	}

	trace.Summary = "mutated after snapshot"
	if raw := first[0].Raw(HistoryRenderContext{}); strings.Contains(raw, "mutated after snapshot") {
		t.Fatalf("nested snapshot was not defensive: %q", raw)
	}
	if tool.HistoryAnimationVersion(5) != tool.Version()+5 {
		t.Fatalf("active child animation version = %d", tool.HistoryAnimationVersion(5))
	}
	adapted := adaptChatItem("agent-parent", tool)
	nested, ok := adapted.(HistoryNestedItem)
	if !ok || len(nested.NestedHistoryItems()) != 1 {
		t.Fatalf("adapted Agent nested capability = %T %#v", adapted, nested)
	}
}

func TestAgentHistoryRendererMalformedAndNarrow(t *testing.T) {
	tool := &ToolMessage{name: "Agent", input: `{bad`, output: "launch failed", status: ToolError, version: 1}
	rendered := tool.Render(24, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "Agent") || !strings.Contains(plain, "failed") {
		t.Fatalf("malformed Agent = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 24)
	if _, ok := toolHistoryRendererFor("Agent").(agentToolHistoryRenderer); !ok {
		t.Fatalf("Agent renderer = %T", toolHistoryRendererFor("Agent"))
	}
}
