package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestQueryEngineProjectsRepeatedSkillMaterial(t *testing.T) {
	for _, fileBacked := range []bool{false, true} {
		t.Run(fmt.Sprintf("file_backed_%t", fileBacked), func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, "fixture.txt")
			if err := os.WriteFile(path, []byte("independent read fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			body := strings.Repeat("Preserve the independent fixture contract and check its behavior.\n", 16)
			skill := &skills.Skill{Name: "trace-lens", Content: body}
			if fileBacked {
				skill.FilePath = filepath.Join(cwd, "SKILL.md")
			}
			registry := skills.NewSkillRegistry()
			registry.Register(skill)
			toolRegistry := tools.NewRegistry()
			tools.RegisterDefaults(toolRegistry)
			mdl := &skillProjectionModel{path: path, body: body}
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: cwd, TranscriptDir: t.TempDir(), ChatModel: mdl,
				CustomSystemPrompt: "Use the fixture tools.", ToolRegistry: toolRegistry,
				SkillRegistry: registry, PermissionMode: permission.ModeBypassPermissions,
			})
			t.Cleanup(eng.Close)
			events, admission := eng.SubmitMessage(t.Context(), "Inspect the fixture with trace-lens.")
			if admission.Err != nil {
				t.Fatal(admission.Err)
			}
			var terminal *Terminal
			var rawSkillResult string
			for event := range events {
				if event.Type == EventToolResult && event.ToolResultMessage != nil && event.ToolResultMessage.ToolName == "Skill" {
					rawSkillResult = event.ToolResultMessage.Content
				}
				if event.Type == EventTerminal {
					terminal = event.TerminalInfo
				}
			}
			if terminal == nil || terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal=%+v", terminal)
			}
			if !reflect.DeepEqual(mdl.copies, []int{0, 1, 1, 1}) {
				t.Fatalf("skill body copies in model requests=%v, want [0 1 1 1]", mdl.copies)
			}
			wantResult := body
			if fileBacked {
				wantResult = skill.InvocationPrompt(body)
			}
			if rawSkillResult != wantResult {
				t.Fatal("canonical tool result lost its full instructions or source")
			}
			for i := 1; i < len(mdl.requests); i++ {
				before, after := mdl.requests[i-1], mdl.requests[i]
				if len(after) < len(before) || !reflect.DeepEqual(before, after[:len(before)]) {
					t.Fatalf("request %d rewrote the previous model-visible prefix", i+1)
				}
			}
		})
	}
}

type skillProjectionModel struct {
	path     string
	body     string
	copies   []int
	requests [][]*schema.Message
}

func (*skillProjectionModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("fixture requires streaming")
}

func (m *skillProjectionModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	copies := 0
	for _, message := range input {
		copies += strings.Count(message.Content, m.body)
	}
	m.copies = append(m.copies, copies)
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var snapshot []*schema.Message
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	m.requests = append(m.requests, snapshot)
	response := &schema.Message{Role: schema.Assistant}
	call := len(m.copies)
	if call == 2 {
		response.ToolCalls = []schema.ToolCall{{ID: "invoke-skill", Type: "function", Function: schema.FunctionCall{Name: "Skill", Arguments: `{"skill":"trace-lens"}`}}}
	} else if call == 1 || call == 3 {
		args, _ := json.Marshal(map[string]string{"file_path": m.path})
		response.ToolCalls = []schema.ToolCall{{ID: fmt.Sprintf("read-%d", call), Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: string(args)}}}
	} else {
		response.Content = "Fixture complete."
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}

func TestQueryEngineRestoresSkillResultAfterCompactBoundary(t *testing.T) {
	body := strings.Repeat("Retain complete guidance when its prefetch is removed.\n", 16)
	mdl := &skillProjectionResumeModel{body: body}
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: mdl,
		CustomSystemPrompt: "Use the fixture tools.", SkillRegistry: skills.NewSkillRegistry(),
		PermissionMode: permission.ModeBypassPermissions,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "Earlier trace-lens request."},
		{Role: schema.User, Content: body, Extra: map[string]any{"is_meta": true, "attachment_kind": "skill_prefetch", "skill_name": "trace-lens"}},
		{Role: schema.System, Content: "retained compact summary", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "saved-skill", Type: "function", Function: schema.FunctionCall{Name: "Skill", Arguments: `{"skill":"trace-lens"}`}}}},
		{Role: schema.Tool, ToolCallID: "saved-skill", ToolName: "Skill", Content: body},
	})
	events, admission := eng.SubmitMessage(t.Context(), "Continue.")
	if admission.Err != nil {
		t.Fatal(admission.Err)
	}
	assertAutoCompactRestartTerminal(t, events)
	if !mdl.fullResultSeen || mdl.copies != 1 {
		t.Fatalf("restored result visible=%t body copies=%d", mdl.fullResultSeen, mdl.copies)
	}
}

type skillProjectionResumeModel struct {
	body           string
	copies         int
	fullResultSeen bool
}

func (*skillProjectionResumeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("fixture requires streaming")
}

func (m *skillProjectionResumeModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	for _, message := range input {
		m.copies += strings.Count(message.Content, m.body)
		m.fullResultSeen = m.fullResultSeen || message.Role == schema.Tool && message.ToolCallID == "saved-skill" && message.Content == m.body
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "Restored fixture complete."}}), nil
}
