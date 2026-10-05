package tools

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestAgentToolReportsFailedExecutionWithPartialFindings(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executionErr := errors.New("verification stopped before requirement C")
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(context.Context, AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "Requirement B failed: expected 2, got 1", Messages: []*schema.Message{{Role: schema.Assistant, Content: "Requirement B failed: expected 2, got 1"}}}, executionErr
	}})
	result, err := executeAgentTool(WithAgentRunner(context.Background(), runner), `{"description":"verify implementation","prompt":"Verify A, B and C independently","subagent_type":"verification"}`)
	if !errors.Is(err, executionErr) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "Partial output") || !strings.Contains(err.Error(), "Requirement B failed") {
		t.Fatalf("partial findings not delivered in tool failure: %v", err)
	}
	if strings.Contains(result, "completed task") {
		t.Fatalf("failed execution reported completion: %q", result)
	}
	agent := onlyTrackedAgent(t, runner)
	if agent.Status != "failed" || !strings.Contains(agent.Result, "Requirement B failed") {
		t.Fatalf("failed child result = %#v", agent)
	}
}

func TestAgentRunnerRetainsPartialFindingsWhenOutputWriteFails(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executionErr := errors.New("verification incomplete")
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(_ context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		snapshot, ok := runner.GetAgentSnapshot(opts.AgentID)
		if !ok {
			return nil, errors.New("missing admitted agent")
		}
		if err := os.Remove(snapshot.OutputFile); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Mkdir(snapshot.OutputFile, 0o700); err != nil {
			return nil, err
		}
		return &AgentExecResult{Result: "partial finding survived output failure"}, executionErr
	}})
	result, err := RunAgent(context.Background(), runner, AgentExecOptions{Task: "verify"})
	if !errors.Is(err, executionErr) || !strings.Contains(err.Error(), "write output file") {
		t.Fatalf("combined execution/output error = %v", err)
	}
	if result == nil || result.Result != "partial finding survived output failure" {
		t.Fatalf("foreground partial result = %#v", result)
	}
	agent := onlyTrackedAgent(t, runner)
	if agent.Status != "failed" || agent.Result != result.Result {
		t.Fatalf("live failed result = %q, status = %q", agent.Result, agent.Status)
	}
	notifications := runner.PollAgentNotifications()
	if len(notifications) != 1 || notifications[0].Status != "failed" || !strings.Contains(notifications[0].Message, result.Result) {
		t.Fatalf("durable partial notification = %#v", notifications)
	}
}
