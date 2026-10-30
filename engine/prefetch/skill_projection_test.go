package prefetch

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

func projectionFixture(content string) []*schema.Message {
	return []*schema.Message{
		{Role: schema.User, Content: content, Extra: map[string]any{"is_meta": true, "attachment_kind": "skill_prefetch", "skill_name": "trace-lens"}},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "load-skill", Type: "function", Function: schema.FunctionCall{Name: "Skill", Arguments: `{"skill":"trace-lens"}`}}}},
		{Role: schema.Tool, ToolCallID: "load-skill", ToolName: "Skill", Content: content},
	}
}

func TestProjectSkillToolResultsRequiresCompleteVisibleMaterial(t *testing.T) {
	body := strings.Repeat("Apply the complete independent guidance, mode=default.\n", 16)
	const header = "Skill: trace-lens\nSource: /skills/lens/SKILL.md\nBase directory: /skills/lens\n\n"
	cases := []struct {
		name      string
		change    func([]*schema.Message) []*schema.Message
		wantShort bool
	}{
		{"complete", nil, true},
		{"file source retained", func(m []*schema.Message) []*schema.Message {
			m[0].Content = header + body
			m[2].Content = header + body
			return m
		}, true},
		{"explicit non-error", func(m []*schema.Message) []*schema.Message { m[2].Extra = map[string]any{"is_error": false}; return m }, true},
		{"truncated prefetch", func(m []*schema.Message) []*schema.Message { m[0].Content = body[:80] + "\n...(truncated)"; return m }, false},
		{"matching truncated result", func(m []*schema.Message) []*schema.Message {
			m[0].Content = body[:80] + "\n...(truncated)"
			m[2].Content = m[0].Content
			return m
		}, false},
		{"rendered argument differs", func(m []*schema.Message) []*schema.Message {
			m[1].ToolCalls[0].Function.Arguments = `{"skill":"trace-lens","arguments":{"mode":"custom"}}`
			m[2].Content = strings.ReplaceAll(body, "mode=default", "mode=custom")
			return m
		}, false},
		{"changed body", func(m []*schema.Message) []*schema.Message { m[2].Content += "new generation"; return m }, false},
		{"changed source", func(m []*schema.Message) []*schema.Message {
			m[0].Content = header + body
			m[2].Content = strings.ReplaceAll(header, "/skills/lens", "/skills/new") + body
			return m
		}, false},
		{"ordinary quotation", func(m []*schema.Message) []*schema.Message { m[0].Extra = nil; return m }, false},
		{"assistant quotation", func(m []*schema.Message) []*schema.Message { m[0].Role = schema.Assistant; return m }, false},
		{"wrong attachment", func(m []*schema.Message) []*schema.Message { m[0].Extra["attachment_kind"] = "file"; return m }, false},
		{"wrong skill name", func(m []*schema.Message) []*schema.Message { m[0].Extra["skill_name"] = "other"; return m }, false},
		{"foreign tool", func(m []*schema.Message) []*schema.Message { m[1].ToolCalls[0].Function.Name = "Other"; return m }, false},
		{"foreign result", func(m []*schema.Message) []*schema.Message { m[2].ToolName = "Other"; return m }, false},
		{"malformed call", func(m []*schema.Message) []*schema.Message { m[1].ToolCalls[0].Function.Arguments = "{"; return m }, false},
		{"unmatched result", func(m []*schema.Message) []*schema.Message { m[2].ToolCallID = "orphan"; return m }, false},
		{"failed tool", func(m []*schema.Message) []*schema.Message { m[2].Extra = map[string]any{"is_error": true}; return m }, false},
		{"unknown error marker", func(m []*schema.Message) []*schema.Message {
			m[2].Extra = map[string]any{"is_error": "unknown"}
			return m
		}, false},
		{"multimodal attachment", func(m []*schema.Message) []*schema.Message {
			m[0].UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: "other visible input"}}
			return m
		}, false},
		{"multimodal result", func(m []*schema.Message) []*schema.Message {
			m[2].AssistantGenMultiContent = []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeText, Text: body}}
			return m
		}, false},
		{"reasoning result", func(m []*schema.Message) []*schema.Message { m[2].ReasoningContent = "retained reasoning"; return m }, false},
		{"duplicate call identity", func(m []*schema.Message) []*schema.Message {
			m[1].ToolCalls = append(m[1].ToolCalls, m[1].ToolCalls[0])
			return m
		}, false},
		{"small body", func(m []*schema.Message) []*schema.Message { m[0].Content = "small"; m[2].Content = "small"; return m }, false},
		{"unsupported source locator", func(m []*schema.Message) []*schema.Message {
			m[0].Content = strings.Replace(header, "Source: /skills/lens/SKILL.md", "Source: /skills/lens\n/SKILL.md", 1) + body
			m[2].Content = m[0].Content
			return m
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			messages := projectionFixture(body)
			if tc.change != nil {
				messages = tc.change(messages)
			}
			before, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			projected := ProjectSkillToolResults(messages)
			after, err := json.Marshal(messages)
			if err != nil || string(before) != string(after) {
				t.Fatal("projection mutated canonical input")
			}
			if len(projected) != len(messages) {
				t.Fatal("message identity or ordering changed")
			}
			short := len(projected[2].Content) < len(messages[2].Content)
			if short != tc.wantShort {
				t.Fatalf("shortened=%t, want %t", short, tc.wantShort)
			}
			if !short && !reflect.DeepEqual(projected, messages) {
				t.Fatal("ineligible material changed")
			}
			if short {
				if !reflect.DeepEqual(projected[:2], messages[:2]) || projected[2].ToolCallID != messages[2].ToolCallID || projected[2].ToolName != "Skill" {
					t.Fatal("preceding history or tool identity changed")
				}
				if strings.Contains(projected[2].Content, body) {
					t.Fatal("duplicate body still present")
				}
				if strings.HasPrefix(messages[2].Content, header) && !strings.HasPrefix(projected[2].Content, header) {
					t.Fatal("source locator was removed")
				}
			}
		})
	}
}

func TestProjectSkillToolResultsRestoresAfterCompaction(t *testing.T) {
	body := strings.Repeat("Keep complete instructions through context loss.\n", 16)
	history := projectionFixture(body)
	if ProjectSkillToolResults(history)[2].Content == body {
		t.Fatal("fixture did not shorten duplicate")
	}
	boundary := &schema.Message{Role: schema.System, Content: "retained summary", Extra: map[string]any{"subtype": "compact_boundary"}}
	compacted := append([]*schema.Message{history[0], boundary}, history[1:]...)
	active := compact.GetMessagesAfterCompactBoundary(compacted)
	projected := ProjectSkillToolResults(active)
	if len(projected) != 3 || projected[2].Content != body {
		t.Fatal("compacted-away prefetch must restore canonical full result")
	}
	data, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var restored []*schema.Message
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ProjectSkillToolResults(history), ProjectSkillToolResults(restored)) {
		t.Fatal("projection depends on process-local delivery state")
	}
}

func TestProjectSkillToolResultsDoesNotRewriteForLaterAttachments(t *testing.T) {
	body := strings.Repeat("Keep the model request prefix immutable.\n", 16)
	fixture := projectionFixture(body)
	early := fixture[1:]
	before := ProjectSkillToolResults(early)
	after := ProjectSkillToolResults(append(append([]*schema.Message(nil), early...), fixture[0]))
	if !reflect.DeepEqual(before, after[:len(before)]) || after[1].Content != body {
		t.Fatal("later prefetch rewrote an earlier result")
	}
	before = ProjectSkillToolResults(fixture)
	changed := projectionFixture(body + "changed generation")
	changed[1].ToolCalls[0].ID = "load-new-skill"
	changed[2].ToolCallID = "load-new-skill"
	longer := append(append([]*schema.Message(nil), fixture...), changed...)
	after = ProjectSkillToolResults(longer)
	if !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatal("later generation rewrote an already sent prefix")
	}
	if after[5].Content == changed[2].Content {
		t.Fatal("current complete generation was not eligible")
	}
}

func TestProjectSkillToolResultsNilInput(t *testing.T) {
	if ProjectSkillToolResults(nil) != nil {
		t.Fatal("nil input changed")
	}
	input := []*schema.Message{nil}
	if got := ProjectSkillToolResults(input); len(got) != 1 || got[0] != nil {
		t.Fatal("nil message changed")
	}
}

func TestProjectSkillToolResultsKeepsLegacyMultimodalHistory(t *testing.T) {
	body := strings.Repeat("Retain legacy serialized multimodal context.\n", 16)
	for _, index := range []int{0, 2} {
		t.Run(string(rune('0'+index)), func(t *testing.T) {
			messages := projectionFixture(body)
			// Exercise persisted legacy JSON without constructing deprecated SDK types.
			if err := json.Unmarshal([]byte(`{"multi_content":[{"type":"text","text":"legacy visible input"}]}`), messages[index]); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ProjectSkillToolResults(messages), messages) {
				t.Fatal("legacy multimodal material was shortened")
			}
		})
	}
}
