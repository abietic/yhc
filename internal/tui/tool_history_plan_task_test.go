package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlanHistoryRendererTransitionsAndApproval(t *testing.T) {
	enter := &ToolMessage{
		name: "EnterPlanMode",
		output: `Plan mode entered. You are now in planning mode.

## Plan File Info:
You should create or edit your plan at: /tmp/.claude/plans/session-1.md
Use the Write tool to create the file or Edit tool to modify it.

## Instructions:
Explore the codebase.`,
		status: ToolSuccess, version: 1,
	}
	enterPlain := stripANSIForTest(enter.Render(72, defaultStyles()))
	for _, want := range []string{"Plan", "entered", "session-1.md", "Exploring and designing"} {
		if !strings.Contains(enterPlain, want) {
			t.Fatalf("enter plan missing %q: %q", want, enterPlain)
		}
	}
	if strings.Contains(enterPlain, "## Instructions") {
		t.Fatalf("enter plan leaked execution boilerplate: %q", enterPlain)
	}

	planLines := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		planLines = append(planLines, fmt.Sprintf("%d. Step %d", i, i))
	}
	output := "User has approved your plan. You can now start coding.\n\n" +
		"Your plan has been saved to: /tmp/.claude/plans/session-1.md\n" +
		"You can refer back to it if needed during implementation.\n\n" +
		"## Approved Plan:\n" + strings.Join(planLines, "\n") +
		"\n\n## Granted Permissions:\n- Bash: run tests"
	exit := &ToolMessage{
		name:   "ExitPlanMode",
		input:  `{"allowedPrompts":[{"tool":"Bash","prompt":"run tests"},{"tool":"Write","prompt":"edit implementation"}]}`,
		output: output, status: ToolSuccess, version: 2,
	}
	rendered := exit.Render(76, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{"Plan", "approved", "2 permissions", "1. Step 1", "+4 lines", "10. Step 10"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("approved plan missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "Granted Permissions") || strings.Contains(plain, "You can now start coding") {
		t.Fatalf("approved plan leaked boilerplate: %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 76)

	expanded := stripANSIForTest(exit.RenderExpanded(HistoryRenderContext{Width: 62, Styles: defaultStyles()}))
	for _, want := range []string{"Input:", "edit implementation", "Result:", "5. Step 5", "Granted Permissions"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded plan missing %q: %q", want, expanded)
		}
	}
	assertHistoryLinesFit(t, exit.RenderExpanded(HistoryRenderContext{Width: 62, Styles: defaultStyles()}), 62)
	transcript := exit.RenderTranscript(HistoryRenderContext{})
	if !strings.Contains(transcript, output) || !strings.Contains(transcript, "Status: completed") {
		t.Fatalf("plan transcript = %q", transcript)
	}
}

func TestPlanHistoryRendererTeamSubmissionAndFailure(t *testing.T) {
	submitted := &ToolMessage{
		name: "ExitPlanMode",
		output: `Your plan has been submitted to the team lead for approval.

Plan file: /tmp/.claude/plans/child.md
Waiting for review.

Request ID: plan_approval_child_1`,
		status: ToolSuccess, version: 1,
	}
	plain := stripANSIForTest(submitted.Render(68, defaultStyles()))
	for _, want := range []string{"submitted", "child.md", "Waiting for team lead approval", "plan_approval_child_1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("submitted plan missing %q: %q", want, plain)
		}
	}

	failed := &ToolMessage{
		name: "ExitPlanMode", input: `{bad`, output: "User rejected the plan: add rollback detail",
		status: ToolError, version: 1,
	}
	rendered := failed.Render(30, defaultStyles())
	failedPlain := stripANSIForTest(rendered)
	if !strings.Contains(failedPlain, "failed") || !strings.Contains(failedPlain, "User rejected") ||
		!strings.Contains(failedPlain, "content clipped") {
		t.Fatalf("failed plan = %q", failedPlain)
	}
	assertHistoryLinesFit(t, rendered, 30)
}

func TestTodoHistoryRendererProgressAndFullProjection(t *testing.T) {
	items := make([]string, 0, 11)
	for i := 1; i <= 11; i++ {
		status := "pending"
		active := fmt.Sprintf("Working item %d", i)
		if i <= 3 {
			status = "completed"
		}
		if i == 4 {
			status = "in_progress"
		}
		items = append(items, fmt.Sprintf(`{"content":"Item %d","status":"%s","activeForm":"%s"}`, i, status, active))
	}
	input := `{"todos":[` + strings.Join(items, ",") + `]}`
	tool := &ToolMessage{
		name: "TodoWrite", input: input,
		output: "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress.",
		status: ToolSuccess, version: 1,
	}
	rendered := tool.Render(70, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{"To-Do", "updated", "3/11", "Working item 4", "[x] Item 1", "[~] Working item 4", "+3 lines", "[ ] Item 11"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("todo rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "Todos have been modified") || strings.Contains(plain, "Item 6") {
		t.Fatalf("todo rich was not semantic/bounded: %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 70)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 66, Styles: defaultStyles()}))
	for _, want := range []string{"Input:", "Item 6", "Result:", "Todos have been modified"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("todo expanded missing %q: %q", want, expanded)
		}
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{})
	if !strings.Contains(transcript, "Working item 4") || !strings.Contains(transcript, "Item 11") ||
		!strings.Contains(transcript, "Todos have been modified") {
		t.Fatalf("todo transcript = %q", transcript)
	}
}

func TestTaskHistoryRendererCRUDAndList(t *testing.T) {
	created := &ToolMessage{
		name:   "TaskCreate",
		input:  `{"subject":"Implement renderer","description":"Add semantic task history","activeForm":"Implementing renderer"}`,
		output: "Task #12 created successfully: Implement renderer",
		status: ToolSuccess, version: 1,
	}
	createPlain := stripANSIForTest(created.Render(72, defaultStyles()))
	for _, want := range []string{"Task", "created", "#12", "Implement renderer", "Add semantic task history", "Active: Implementing renderer"} {
		if !strings.Contains(createPlain, want) {
			t.Fatalf("task create missing %q: %q", want, createPlain)
		}
	}

	updated := &ToolMessage{
		name:   "Task",
		input:  `{"action":"update","task_id":"12","status":"in_progress","owner":"agent-a"}`,
		output: "Task #12 updated: status, owner", status: ToolSuccess, version: 1,
	}
	updatePlain := stripANSIForTest(updated.Render(60, defaultStyles()))
	if !strings.Contains(updatePlain, "in progress") || !strings.Contains(updatePlain, "#12") || !strings.Contains(updatePlain, "status, owner") {
		t.Fatalf("task update = %q", updatePlain)
	}

	listOutput := strings.Join([]string{
		"#1 [completed] Inspect runtime",
		"#2 [in_progress] Implement renderer (agent-a) — Implementing renderer",
		"#3 [pending] Add tests [blocked by #2]",
		"#4 [pending] Update docs",
		"#5 [pending] Run lint",
		"#6 [pending] Run tests",
		"#7 [pending] Build binaries",
		"#8 [pending] Review diff",
		"#9 [pending] Close milestone",
	}, "\n")
	listed := &ToolMessage{name: "TaskList", input: `{}`, output: listOutput, status: ToolSuccess, version: 1}
	listRendered := listed.Render(82, defaultStyles())
	listPlain := stripANSIForTest(listRendered)
	for _, want := range []string{"9 tasks", "1 active", "1 done", "1 blocked", "[x] #1", "[~] #2", "+2 lines", "[ ] #9"} {
		if !strings.Contains(listPlain, want) {
			t.Fatalf("task list missing %q: %q", want, listPlain)
		}
	}
	if strings.Contains(listPlain, "#5") {
		t.Fatalf("task list was not bounded: %q", listPlain)
	}
	assertHistoryLinesFit(t, listRendered, 82)
}

func TestTaskHistoryRendererGetOutputStopAndLegacyAgent(t *testing.T) {
	get := &ToolMessage{
		name: "TaskGet", input: `{"task_id":"7"}`,
		output: "Task #7: Verify release\nStatus: in_progress\nDescription: Run all release checks\nBlocked by: #6\nOwner: agent-b",
		status: ToolSuccess, version: 1,
	}
	getPlain := stripANSIForTest(get.Render(70, defaultStyles()))
	for _, want := range []string{"in progress", "#7", "Verify release", "Run all release checks", "Blocked by: #6", "Owner: agent-b"} {
		if !strings.Contains(getPlain, want) {
			t.Fatalf("task get missing %q: %q", want, getPlain)
		}
	}

	runningOutput := &ToolMessage{
		name: "TaskOutput", input: `{"task_id":"7","block":false}`,
		output: "<retrieval_status>not_ready</retrieval_status>\n\n<task_id>7</task_id>\n\n<task_type>local_task</task_type>\n\n<status>in_progress</status>",
		status: ToolSuccess, version: 1,
	}
	runningPlain := stripANSIForTest(runningOutput.Render(64, defaultStyles()))
	if !strings.Contains(runningPlain, "still running") || !strings.Contains(runningPlain, "#7") || !strings.Contains(runningPlain, "Task is still running") {
		t.Fatalf("task output running = %q", runningPlain)
	}

	agentOutput := &ToolMessage{
		name: "TaskOutput", input: `{"task_id":"agent-7"}`,
		output: "<retrieval_status>success</retrieval_status>\n\n<task_id>agent-7</task_id>\n\n<task_type>local_agent</task_type>\n\n<status>completed</status>\n\n<output>full child answer</output>",
		status: ToolSuccess, version: 1,
	}
	agentPlain := stripANSIForTest(agentOutput.Render(70, defaultStyles()))
	if !strings.Contains(agentPlain, "Agent output available") || strings.Contains(agentPlain, "full child answer") {
		t.Fatalf("agent task output rich = %q", agentPlain)
	}
	agentExpanded := stripANSIForTest(agentOutput.RenderExpanded(HistoryRenderContext{Width: 60, Styles: defaultStyles()}))
	if !strings.Contains(agentExpanded, "full child answer") {
		t.Fatalf("agent task output expanded = %q", agentExpanded)
	}

	stopped := &ToolMessage{
		name: "TaskStop", input: `{"task_id":"7"}`,
		output: "Successfully stopped task: 7 (Verify release)\ntask_id: 7\ntask_type: local_task",
		status: ToolSuccess, version: 1,
	}
	stopPlain := stripANSIForTest(stopped.Render(54, defaultStyles()))
	if !strings.Contains(stopPlain, "stopped") || !strings.Contains(stopPlain, "#7") || strings.Contains(stopPlain, "Successfully stopped") {
		t.Fatalf("task stop = %q", stopPlain)
	}

	legacy := &ToolMessage{
		name:   "Task",
		input:  `{"description":"Inspect history","prompt":"Find rendering gaps","agent_type":"Explore"}`,
		output: "Found the gap", status: ToolSuccess, version: 1,
	}
	legacyPlain := stripANSIForTest(legacy.Render(70, defaultStyles()))
	for _, want := range []string{"Agent", "completed", "Explore", "Inspect history", "Found the gap"} {
		if !strings.Contains(legacyPlain, want) {
			t.Fatalf("legacy Task Agent missing %q: %q", want, legacyPlain)
		}
	}
	if strings.Contains(legacyPlain, "Task done") {
		t.Fatalf("legacy Task rendered as local task: %q", legacyPlain)
	}
}

func TestPlanTaskTodoRendererRegistrationAndNarrowWidth(t *testing.T) {
	for _, name := range []string{"EnterPlanMode", "ExitPlanMode", "Task", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskStop", "TaskOutput", "TodoWrite"} {
		if _, ok := toolHistoryRendererFor(name).(planTaskTodoToolHistoryRenderer); !ok {
			t.Fatalf("%s renderer = %T", name, toolHistoryRendererFor(name))
		}
	}
	tool := &ToolMessage{
		name: "TaskUpdate", input: `{bad`, output: "task update: invalid params",
		status: ToolError, version: 1,
	}
	rendered := tool.Render(24, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "Task") || !strings.Contains(plain, "failed") ||
		!strings.Contains(plain, "task update") || !strings.Contains(plain, "content clipped") {
		t.Fatalf("narrow malformed task = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 24)
}
