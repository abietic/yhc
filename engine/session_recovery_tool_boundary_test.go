package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestRecoveryProofObservesCommittedToolsWithoutAuthorizingPausedTurn(t *testing.T) {
	root := t.TempDir()
	dir := root + "/transcripts"
	recorder := transcript.NewRecorder("paused-tools", dir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "private prompt"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "one", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{}`}},
			{ID: "two", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}},
		}, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}}},
		{Role: schema.Tool, ToolCallID: "two", Content: "private result"},
		{Role: schema.Tool, ToolCallID: "one", Content: "saved once"},
	}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{SessionID: "paused-tools", ThreadID: "paused-tools", CWD: root, QueryKernelVersion: queryKernelVersionProjectGraph, QueryKernelStage: string(queryKernelStageFull)}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	eng := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
	t.Cleanup(eng.Close)
	_, proof, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: "paused-tools", CWD: root, TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		ToolBoundary struct {
			Version int      `json:"version"`
			Kind    string   `json:"kind"`
			Count   int      `json:"call_count"`
			Names   []string `json:"tool_names"`
			SHA256  string   `json:"sha256"`
		} `json:"tool_boundary"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.ToolBoundary.Version != 1 || raw.ToolBoundary.Kind != "committed_tool_results" || raw.ToolBoundary.Count != 2 || len(raw.ToolBoundary.SHA256) != 64 || len(raw.ToolBoundary.Names) != 2 {
		t.Fatalf("missing exact committed-tool observation: %s", encoded)
	}
	if strings.Contains(string(encoded), "private prompt") || strings.Contains(string(encoded), "private result") || strings.Contains(string(encoded), "saved once") {
		t.Fatal("tool proof leaked conversation content")
	}
	if proof.ValidationComplete || proof.CanResumeAgent || len(proof.BlockingReasons) != 1 || proof.BlockingReasons[0] != "native_turn_incomplete" {
		t.Fatalf("tool observation authorized an unfinished turn: %+v", proof)
	}
}

func TestRecoveryProofLocalFileCallsFailClosedOutsideCapturedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		local      bool
	}{
		{"Write", `{"file_path":"file.txt"}`, true},
		{"Read", `{"file_path":"/workspace/file.txt"}`, true},
		{"Edit", `{"file_path":"sub/../file.txt"}`, true},
		{"Write", `{"file_path":"../file.txt"}`, false},
		{"Write", `{"file_path":"/workspace-other/file.txt"}`, false},
		{"Read", `{"file_path":"/tmp/file.txt"}`, false},
		{"Read", `{}`, false},
		{"Bash", `{"command":"pwd"}`, false},
		{"mcp__external", `{"file_path":"file.txt"}`, false},
	} {
		call := schema.ToolCall{Function: schema.FunctionCall{Name: tc.name, Arguments: tc.args}}
		if got := recoveryProofLocalFileCall(call, "/workspace"); got != tc.local {
			t.Fatalf("%s %s: local=%v want=%v", tc.name, tc.args, got, tc.local)
		}
	}
	// A later local round does not erase an earlier external tool dependency.
	makeCall := func(id, path string) *schema.Message {
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{"file_path":"` + path + `"}`}}}}
	}
	state := recoveryProofToolBoundary([]*schema.Message{
		{Role: schema.User},
		makeCall("outside", "/tmp/external"),
		{Role: schema.Tool, ToolCallID: "outside"},
		{Role: schema.Assistant},
		{Role: schema.User},
		makeCall("inside", "local"),
		{Role: schema.Tool, ToolCallID: "inside"},
	}, "/workspace")
	if state == nil || state.WorkspaceLocal {
		t.Fatalf("external historical dependency was forgotten: %+v", state)
	}
}

func TestRecoveryProofToolBoundaryRejectsAmbiguousOrUnfinishedTools(t *testing.T) {
	call := func(id string) *schema.Message {
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{}`}}}}
	}
	result := func(id string) *schema.Message {
		return &schema.Message{Role: schema.Tool, ToolCallID: id, Content: "saved"}
	}
	for _, tc := range []struct {
		name     string
		messages []*schema.Message
		valid    bool
	}{
		{name: "committed", messages: []*schema.Message{{Role: schema.User}, call("a"), result("a")}, valid: true},
		{name: "metadata", messages: []*schema.Message{{Role: schema.User}, call("a"), {Role: schema.System}, result("a"), {Role: schema.Assistant, Extra: map[string]any{"api_error": true}}}, valid: true},
		{name: "in flight", messages: []*schema.Message{{Role: schema.User}, call("a")}},
		{name: "foreign result", messages: []*schema.Message{{Role: schema.User}, call("a"), result("b")}},
		{name: "duplicate result", messages: []*schema.Message{{Role: schema.User}, call("a"), result("a"), result("a")}},
		{name: "reused id", messages: []*schema.Message{{Role: schema.User}, call("a"), result("a"), call("a"), result("a")}},
		{name: "unresolved earlier round", messages: []*schema.Message{{Role: schema.User}, call("a"), call("b"), result("b")}},
		{name: "new user", messages: []*schema.Message{{Role: schema.User}, call("a"), result("a"), {Role: schema.User}}},
		{name: "completed turn", messages: []*schema.Message{{Role: schema.User}, call("a"), result("a"), {Role: schema.Assistant}}},
		{name: "empty id", messages: []*schema.Message{{Role: schema.User}, call(""), result("")}},
		{name: "unknown role", messages: []*schema.Message{call("a"), result("a"), {Role: schema.RoleType("future")}}},
		{name: "hidden pending", messages: []*schema.Message{call("a"), result("a"), {Role: schema.Assistant, ToolCalls: call("b").ToolCalls, Extra: map[string]any{"is_meta": true}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoveryProofToolBoundary(tc.messages, "/workspace"); (got != nil) != tc.valid {
				t.Fatalf("boundary = %+v, want valid=%v", got, tc.valid)
			}
		})
	}
	first := recoveryProofToolBoundary([]*schema.Message{call("a"), result("a")}, "/workspace")
	changed := result("a")
	changed.Content = "different committed output"
	second := recoveryProofToolBoundary([]*schema.Message{call("a"), changed}, "/workspace")
	if first == nil || second == nil || first.SHA256 == second.SHA256 {
		t.Fatal("tool digest did not bind the actual result")
	}
}
