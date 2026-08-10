package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetPlanFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plansDir := GetPlansDirPath()

	got := GetPlanFilePath("test-session", "")
	want := filepath.Join(plansDir, "test-session.md")
	if got != want {
		t.Errorf("GetPlanFilePath(%q, %q) = %q, want %q", "test-session", "", got, want)
	}

	gotAgent := GetPlanFilePath("test-session", "agent-1")
	wantAgent := filepath.Join(plansDir, "test-session-agent-agent-1.md")
	if gotAgent != wantAgent {
		t.Errorf("GetPlanFilePath with agent = %q, want %q", gotAgent, wantAgent)
	}
	if _, err := os.Stat(plansDir); !os.IsNotExist(err) {
		t.Fatalf("GetPlanFilePath created plans directory: %v", err)
	}
}

func TestSaveAndGetPlan(t *testing.T) {
	sessionID := fmt.Sprintf("test-%d", os.Getpid())
	content := "## My Plan\n\n1. Step one\n2. Step two"

	err := SavePlan(sessionID, "", content)
	if err != nil {
		t.Fatal(err)
		return
	}
	defer os.Remove(GetPlanFilePath(sessionID, ""))

	got := GetPlan(sessionID, "")
	if got != content {
		t.Errorf("GetPlan = %q, want %q", got, content)
	}
}

func TestGetPlanMissing(t *testing.T) {
	got := GetPlan("nonexistent-session-id", "")
	if got != "" {
		t.Errorf("GetPlan on missing file = %q, want empty", got)
	}
}

func TestSaveAndGetPlanWithAgent(t *testing.T) {
	sessionID := fmt.Sprintf("test-agent-%d", os.Getpid())
	content := "Agent-specific plan"

	err := SavePlan(sessionID, "explore-agent", content)
	if err != nil {
		t.Fatal(err)
		return
	}
	defer os.Remove(GetPlanFilePath(sessionID, "explore-agent"))

	got := GetPlan(sessionID, "explore-agent")
	if got != content {
		t.Errorf("GetPlan with agent = %q, want %q", got, content)
	}

	mainPlan := GetPlan(sessionID, "")
	if mainPlan != "" {
		t.Error("main plan should be empty when only agent plan saved")
	}
}

func TestExitPlanModeToolCreation(t *testing.T) {
	tool := ExitPlanModeTool()
	if tool.Info.Name != "ExitPlanMode" {
		t.Errorf("name = %q", tool.Info.Name)
	}
	if tool.NeedsPermissions != true {
		t.Error("ExitPlanMode should require permissions")
	}
	if tool.IsReadOnly {
		t.Error("ExitPlanMode should not be classified as generally read-only")
	}
	if !tool.IsPlanModeTransition {
		t.Error("ExitPlanMode should be classified as a plan-mode transition")
	}
}

func TestExitPlanModeExecuteEmptyPlan(t *testing.T) {
	ctx := WithSessionID(context.Background(), fmt.Sprintf("empty-plan-%d", os.Getpid()))
	tool := ExitPlanModeTool()
	result, err := tool.ExecuteCtx(ctx, "{}")
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "approved exiting plan mode") {
		t.Errorf("empty plan result = %q", result)
	}
}

func TestExitPlanModeExecuteWithPlan(t *testing.T) {
	sid := fmt.Sprintf("test-exit-%d", os.Getpid())
	_ = SavePlan(sid, "", "## Plan\n\n- Do the thing")
	defer os.Remove(GetPlanFilePath(sid, ""))

	ctx := WithSessionID(context.Background(), sid)
	tool := ExitPlanModeTool()
	result, err := tool.ExecuteCtx(ctx, "{}")
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "Approved Plan") {
		t.Errorf("result should contain approved plan, got %q", result)
	}
	if !strings.Contains(result, "Do the thing") {
		t.Errorf("result should contain plan content")
	}
}

func TestExitPlanModeWithAllowedPrompts(t *testing.T) {
	sid := fmt.Sprintf("test-prompts-%d", os.Getpid())
	_ = SavePlan(sid, "", "## Plan\n\nRun tests")
	defer os.Remove(GetPlanFilePath(sid, ""))

	ctx := WithSessionID(context.Background(), sid)
	tool := ExitPlanModeTool()
	input := `{"allowedPrompts":[{"tool":"Bash","prompt":"run tests"}]}`
	result, err := tool.ExecuteCtx(ctx, input)
	if err != nil {
		t.Fatal(err)
		return
	}
	if strings.Contains(result, "Granted Permissions") {
		t.Errorf("result must not claim semantic prompts were granted: %q", result)
	}
	if !strings.Contains(result, "Model-provided Implementation Notes (not granted)") ||
		!strings.Contains(result, "do not grant runtime permission") {
		t.Errorf("result should explain non-authoritative prompts, got %q", result)
	}
	if !strings.Contains(result, "run tests") {
		t.Errorf("result should contain the allowed prompt")
	}
}

func TestExitPlanModeValidateInput(t *testing.T) {
	tool := ExitPlanModeTool()

	// Valid empty input
	if err := tool.ValidateInput(map[string]any{}); err != nil {
		t.Errorf("empty input should be valid: %v", err)
	}

	// Valid allowedPrompts
	validInput := map[string]any{
		"allowedPrompts": []any{
			map[string]any{"tool": "Bash", "prompt": "run tests"},
		},
	}
	if err := tool.ValidateInput(validInput); err != nil {
		t.Errorf("valid allowedPrompts should pass: %v", err)
	}

	// Invalid: missing tool field
	invalidInput := map[string]any{
		"allowedPrompts": []any{
			map[string]any{"tool": "", "prompt": "run tests"},
		},
	}
	if err := tool.ValidateInput(invalidInput); err == nil {
		t.Error("allowedPrompts with empty tool should fail validation")
	}

	// Invalid: missing prompt field
	invalidInput2 := map[string]any{
		"allowedPrompts": []any{
			map[string]any{"tool": "Bash", "prompt": ""},
		},
	}
	if err := tool.ValidateInput(invalidInput2); err == nil {
		t.Error("allowedPrompts with empty prompt should fail validation")
	}
}

func TestExitPlanModeTeammateFlow(t *testing.T) {
	sid := fmt.Sprintf("test-teammate-%d", os.Getpid())

	_ = SavePlan(sid, "", "## Teammate Plan\n\nDo the work")
	defer os.Remove(GetPlanFilePath(sid, ""))

	// Set mailbox dir to a known location for test verification
	mailboxTestDir := filepath.Join(t.TempDir(), "mailbox")
	SetMailboxDir(mailboxTestDir)
	defer SetMailboxDir("")

	// Set teammate env vars
	t.Setenv("CLAUDE_CODE_AGENT_NAME", "worker-1")
	t.Setenv("CLAUDE_CODE_TEAM_NAME", "my-team")

	tool := ExitPlanModeTool()
	ctx := WithSessionID(context.Background(), sid)
	result, err := tool.ExecuteCtx(ctx, "{}")
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "submitted to the team lead") {
		t.Errorf("teammate flow should mention team lead submission, got %q", result)
	}
	if !strings.Contains(result, "Do NOT proceed until you receive approval") {
		t.Errorf("teammate flow should warn not to proceed")
	}
	if !strings.Contains(result, "Request ID:") {
		t.Errorf("teammate flow should include request ID")
	}

	// Verify mailbox was written
	messages, err := ReadMailbox("team-lead", "my-team")
	if err != nil {
		t.Fatalf("reading mailbox: %v", err)
		return
	}
	if len(messages) == 0 {
		t.Error("mailbox should have at least one message")
	} else {
		if !strings.Contains(messages[0].Text, "plan_approval_request") {
			t.Errorf("mailbox message should be plan_approval_request, got %q", messages[0].Text)
		}
		if messages[0].From != "worker-1" {
			t.Errorf("mailbox message from = %q, want worker-1", messages[0].From)
		}
	}
}

func TestExitPlanModeTeammateNoPlan(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// No plan saved — teammate should get an error
	t.Setenv("CLAUDE_CODE_AGENT_NAME", "worker-1")
	t.Setenv("CLAUDE_CODE_TEAM_NAME", "my-team")

	tool := ExitPlanModeTool()
	_, err := tool.ExecuteCtx(WithSessionID(context.Background(), fmt.Sprintf("noplan-%d", os.Getpid())), "{}")
	if err == nil {
		t.Error("teammate with no plan should get an error")
	}
	if !strings.Contains(err.Error(), "no plan file found") {
		t.Errorf("error should mention missing plan file, got: %v", err)
	}
}

func TestExitPlanModeHasTaskToolHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sessionID := fmt.Sprintf("test-task-hint-%d", os.Getpid())
	_ = SavePlan(sessionID, "", "## Plan\n\nMulti-step work")
	defer os.Remove(GetPlanFilePath(sessionID, ""))

	// Initialize default registry with tools so hasToolInRegistry works
	r := NewRegistry()
	RegisterDefaults(r)

	tool := ExitPlanModeTool()
	result, err := tool.ExecuteCtx(WithSessionID(context.Background(), sessionID), "{}")
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(result, "TeamCreate") {
		t.Errorf("should include team hint when Agent tool is in registry, got %q", result)
	}
}

func TestP172PlanToolsUseEngineOwnedExactFileIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_AGENT_NAME", "")
	t.Setenv("CLAUDE_CODE_TEAM_NAME", "")
	exact := filepath.Join(t.TempDir(), ".claude", "plans", "resumed.md")
	if err := os.MkdirAll(filepath.Dir(exact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exact, []byte("## Restored Plan\n\nKeep identity."), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithPlanFileIdentity(
		WithSessionID(context.Background(), "different-session"),
		exact,
	)

	enter, err := EnterPlanModeTool().ExecuteCtx(ctx, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enter, exact) ||
		strings.Contains(enter, GetPlanFilePath("different-session", "")) {
		t.Fatalf("EnterPlanMode exact path output = %q", enter)
	}
	exit, err := ExitPlanModeTool().ExecuteCtx(ctx, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exit, exact) ||
		!strings.Contains(exit, "Keep identity.") {
		t.Fatalf("ExitPlanMode exact path output = %q", exit)
	}
}

func TestIsTeammateMode(t *testing.T) {
	// Not teammate by default
	t.Setenv("CLAUDE_CODE_AGENT_NAME", "")
	t.Setenv("CLAUDE_CODE_TEAM_NAME", "")
	if IsTeammateMode() {
		t.Error("should not be teammate when env vars are empty")
	}

	// Only agent name
	t.Setenv("CLAUDE_CODE_AGENT_NAME", "worker")
	t.Setenv("CLAUDE_CODE_TEAM_NAME", "")
	if IsTeammateMode() {
		t.Error("should not be teammate with only agent name")
	}

	// Both set
	t.Setenv("CLAUDE_CODE_AGENT_NAME", "worker")
	t.Setenv("CLAUDE_CODE_TEAM_NAME", "team")
	if !IsTeammateMode() {
		t.Error("should be teammate when both env vars set")
	}
}

func TestGeneratePlanRequestID(t *testing.T) {
	id1 := generatePlanRequestID("agent-1")
	id2 := generatePlanRequestID("agent-1")
	if id1 == id2 {
		t.Error("request IDs should be unique")
	}
	if !strings.Contains(id1, "plan_approval_agent-1") {
		t.Errorf("request ID should contain agent name, got %q", id1)
	}
}

func TestEnterPlanModeToolCreation(t *testing.T) {
	tool := EnterPlanModeTool()
	if tool.Info.Name != "EnterPlanMode" {
		t.Errorf("name = %q", tool.Info.Name)
	}
	if !tool.IsReadOnly {
		t.Error("EnterPlanMode should be read-only")
	}
	if !tool.IsPlanModeTransition {
		t.Error("EnterPlanMode should be classified as a plan-mode transition")
	}
}

func TestPlanFilePathCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", ".claude", "PLAN.md")
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatal(err)
		return
	}
	if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
		t.Error("directory should exist after MkdirAll")
	}
}

func TestAllowedPromptParsing(t *testing.T) {
	input := `{"allowedPrompts":[{"tool":"Bash","prompt":"run tests"},{"tool":"Bash","prompt":"install deps"}]}`
	var parsed ExitPlanModeInput
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		t.Fatal(err)
		return
	}
	if len(parsed.AllowedPrompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(parsed.AllowedPrompts))
	}
	if parsed.AllowedPrompts[0].Tool != "Bash" {
		t.Errorf("tool = %q", parsed.AllowedPrompts[0].Tool)
	}
	if parsed.AllowedPrompts[1].Prompt != "install deps" {
		t.Errorf("prompt = %q", parsed.AllowedPrompts[1].Prompt)
	}
}
