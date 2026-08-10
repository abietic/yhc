package tools

import (
	"os"
	"strings"
	"testing"
)

func TestReadToolCreation(t *testing.T) {
	tool := ReadTool()
	if tool.Info.Name != "Read" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestWriteToolCreation(t *testing.T) {
	tool := WriteTool()
	if tool.Info.Name != "Write" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestEditToolCreation(t *testing.T) {
	tool := EditTool()
	if tool.Info.Name != "Edit" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestBashToolCreation(t *testing.T) {
	tool := BashTool()
	if tool.Info.Name != "Bash" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestBriefToolCreation(t *testing.T) {
	tool := BriefTool()
	if tool.Info.Name != "Brief" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestMonitorToolCreation(t *testing.T) {
	tool := MonitorTool()
	if tool.Info.Name != "Monitor" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestAskUserQuestionToolCreation(t *testing.T) {
	tool := AskUserQuestionTool()
	if tool.Info.Name != "AskUserQuestion" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestToolSearchToolCreation(t *testing.T) {
	tool := ToolSearchTool()
	if tool.Info.Name != "ToolSearch" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestP244ToolSearchExcludesQueryEngineOwnedGoalDeclarations(t *testing.T) {
	previous := DefaultRegistry
	t.Cleanup(func() { DefaultRegistry = previous })
	registry := NewRegistry()
	RegisterDefaults(registry)

	result, err := executeToolSearch(`{"query":"goal"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, GetGoalToolName) ||
		strings.Contains(result, UpdateGoalToolName) ||
		!strings.Contains(result, "No tools found") {
		t.Fatalf("ToolSearch leaked Goal declarations: %q", result)
	}
}

func TestTodoWriteToolCreation(t *testing.T) {
	tool := TodoWriteTool()
	if tool.Info.Name != "TodoWrite" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestNotebookEditToolCreation(t *testing.T) {
	tool := NotebookEditTool()
	if tool.Info.Name != "NotebookEdit" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestWebFetchToolCreation(t *testing.T) {
	tool := WebFetchTool()
	if tool.Info.Name != "WebFetch" {
		t.Errorf("name = %q", tool.Info.Name)
	}
	if webFetchUserAgent != "Mozilla/5.0 (compatible; YHC/1.0)" {
		t.Errorf("User-Agent = %q", webFetchUserAgent)
	}
}

func TestWebSearchToolCreation(t *testing.T) {
	tool := WebSearchTool()
	if tool.Info.Name != "WebSearch" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestScheduleCronToolCreation(t *testing.T) {
	tool := ScheduleCronTool()
	if tool.Info.Name != "ScheduleCron" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestLSPToolCreation(t *testing.T) {
	tool := LSPTool()
	if tool.Info.Name != "LSP" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestMCPToolCreation(t *testing.T) {
	tool := MCPTool()
	if tool.Info.Name != "mcp_tool" {
		t.Errorf("name = %q, want mcp_tool", tool.Info.Name)
	}
}

func TestTeamCreateToolCreation(t *testing.T) {
	tool := TeamCreateTool()
	if tool.Info.Name != "TeamCreate" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestTeamDeleteToolCreation(t *testing.T) {
	tool := TeamDeleteTool()
	if tool.Info.Name != "TeamDelete" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}

func TestWorktreeCompatibilityStubsFailClosedWithoutChangingProcessCWD(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []ToolImpl{EnterWorktreeTool(), ExitWorktreeTool()} {
		if tool.Execute == nil {
			t.Fatalf("%s compatibility stub has no executor", tool.Info.Name)
		}
		_, err := tool.Execute(`{"branch":"must-not-be-created","remove":true}`)
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s error = %v, want explicit unavailable", tool.Info.Name, err)
		}
		after, cwdErr := os.Getwd()
		if cwdErr != nil {
			t.Fatal(cwdErr)
		}
		if after != before {
			t.Fatalf("%s changed process cwd from %q to %q", tool.Info.Name, before, after)
		}
	}
}

func TestTaskToolsCreation(t *testing.T) {
	tools := []struct {
		name string
		fn   func() ToolImpl
	}{
		{"TaskCreate", TaskCreateTool},
		{"TaskGet", TaskGetTool},
		{"TaskList", TaskListTool},
		{"TaskOutput", TaskOutputTool},
		{"TaskStop", TaskStopTool},
		{"TaskUpdate", TaskUpdateTool},
		{"Task", TaskTool},
	}
	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.fn()
			if tool.Info.Name != tc.name {
				t.Errorf("name = %q, want %q", tool.Info.Name, tc.name)
			}
		})
	}
}

func TestMailboxWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	SetMailboxDir(dir)
	defer SetMailboxDir("")

	msg := MailboxMessage{
		From:      "agent-1",
		Text:      "hello team lead",
		Timestamp: "2024-01-01T00:00:00Z",
	}
	if err := WriteToMailbox("team-lead", msg, "my-team"); err != nil {
		t.Fatal(err)
		return
	}

	messages, err := ReadMailbox("team-lead", "my-team")
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if messages[0].From != "agent-1" {
		t.Errorf("from = %q", messages[0].From)
	}
	if messages[0].Text != "hello team lead" {
		t.Errorf("text = %q", messages[0].Text)
	}

	// Read again should be empty (consumed)
	messages2, _ := ReadMailbox("team-lead", "my-team")
	if len(messages2) != 0 {
		t.Errorf("second read should be empty, got %d", len(messages2))
	}
}

func TestSyntheticOutputToolCreation(t *testing.T) {
	tool := SyntheticOutputTool()
	if tool.Info.Name != "SyntheticOutput" {
		t.Errorf("name = %q", tool.Info.Name)
	}
}
