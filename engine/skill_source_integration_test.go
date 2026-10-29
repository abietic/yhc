package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestQueryEngineSkillSourceResolvesSupportOutsideCWD(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle with spaces")
	if err := os.MkdirAll(filepath.Join(bundle, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "SKILL.md"), []byte("---\nname: project-lens\n---\nRead references/contract.txt.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const support = "independent fixture contract"
	if err := os.WriteFile(filepath.Join(bundle, "references", "contract.txt"), []byte(support), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := skills.NewSkillRegistry()
	if err := registry.LoadFromDirectory(bundle); err != nil {
		t.Fatal(err)
	}
	toolRegistry := tools.NewRegistry()
	tools.RegisterDefaults(toolRegistry)
	mdl := &skillSourceFixtureModel{expectedSource: filepath.Join(bundle, "SKILL.md"), expectedSupport: support}
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: mdl,
		CustomSystemPrompt: "Use the fixture tools.", ToolRegistry: toolRegistry,
		SkillRegistry: registry, PermissionMode: permission.ModeBypassPermissions,
	})
	t.Cleanup(eng.Close)
	events, admission := eng.SubmitMessage(t.Context(), "Inspect the fixture.")
	if admission.Err != nil {
		t.Fatal(admission.Err)
	}
	assertAutoCompactRestartTerminal(t, events)
	if mdl.calls != 3 || !mdl.supportRead {
		t.Fatalf("calls=%d supportRead=%v; expected Skill, Read, final", mdl.calls, mdl.supportRead)
	}
}

// This scripted model uses the actual returned locator to choose its Read;
// neither the test's CWD nor a canned Read argument supplies that location.
type skillSourceFixtureModel struct {
	calls           int
	expectedSource  string
	expectedSupport string
	supportRead     bool
}

func (m *skillSourceFixtureModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("fixture requires streaming")
}

func (m *skillSourceFixtureModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	response := &schema.Message{Role: schema.Assistant}
	switch m.calls {
	case 1:
		response.ToolCalls = []schema.ToolCall{{ID: "load-skill", Type: "function", Function: schema.FunctionCall{Name: "Skill", Arguments: `{"skill":"project-lens"}`}}}
	case 2:
		var body string
		for _, message := range input {
			if message.Role == schema.Tool && message.ToolCallID == "load-skill" {
				body = message.Content
			}
		}
		base := ""
		for _, line := range strings.Split(body, "\n") {
			if value, found := strings.CutPrefix(line, "Base directory: "); found {
				base = value
			}
		}
		if base == "" || !strings.Contains(body, "Source: "+m.expectedSource) {
			return nil, fmt.Errorf("skill result is missing its source context")
		}
		args, err := json.Marshal(map[string]string{"file_path": filepath.Join(base, "references", "contract.txt")})
		if err != nil {
			return nil, err
		}
		response.ToolCalls = []schema.ToolCall{{ID: "read-support", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: string(args)}}}
	case 3:
		for _, message := range input {
			if message.Role == schema.Tool && message.ToolCallID == "read-support" && strings.Contains(message.Content, m.expectedSupport) {
				m.supportRead = true
			}
		}
		if !m.supportRead {
			return nil, fmt.Errorf("support file was not read through the engine")
		}
		response.Content = "Fixture complete."
	default:
		return nil, fmt.Errorf("unexpected extra model call: %d", m.calls)
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}
