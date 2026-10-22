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

func TestQueryEngineSkillPrefetchReachesFollowingRound(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "fixture.txt")
	const readContent = "independent read fixture"
	if err := os.WriteFile(path, []byte(readContent), 0o644); err != nil {
		t.Fatal(err)
	}
	const skillContent = "independent prefetched verification fixture"
	const manualContent = "manual-only fixture must not be prefetched"
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "trace-lens", Content: skillContent})
	registry.Register(&skills.Skill{Name: "manual-lens", Content: manualContent, DisableModelInvocation: true})
	toolRegistry := tools.NewRegistry()
	tools.RegisterDefaults(toolRegistry)
	mdl := &skillPrefetchFixtureModel{path: path, readContent: readContent, skillContent: skillContent, manualContent: manualContent}
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: cwd, TranscriptDir: t.TempDir(), ChatModel: mdl,
		CustomSystemPrompt: "Use the fixture tools.", ToolRegistry: toolRegistry,
		SkillRegistry: registry, PermissionMode: permission.ModeBypassPermissions,
	})
	t.Cleanup(eng.Close)
	events, admission := eng.SubmitMessage(t.Context(), "Inspect the fixture with trace-lens and manual-lens.")
	if admission.Err != nil {
		t.Fatal(admission.Err)
	}
	assertAutoCompactRestartTerminal(t, events)
	if mdl.calls != 2 || !mdl.prefetchSeen {
		t.Fatalf("calls=%d prefetchSeen=%v; expected Read then final with prefetched context", mdl.calls, mdl.prefetchSeen)
	}
}

// This model never calls Skill. The second request can see its body only if
// the shared kernel collects and projects the asynchronous prefetch result.
type skillPrefetchFixtureModel struct {
	path          string
	readContent   string
	skillContent  string
	manualContent string
	calls         int
	prefetchSeen  bool
}

func (m *skillPrefetchFixtureModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("fixture requires streaming")
}

func (m *skillPrefetchFixtureModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	response := &schema.Message{Role: schema.Assistant}
	switch m.calls {
	case 1:
		args, err := json.Marshal(map[string]string{"file_path": m.path})
		if err != nil {
			return nil, err
		}
		response.ToolCalls = []schema.ToolCall{{ID: "prefetch-probe-read", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: string(args)}}}
	case 2:
		readSeen := false
		for _, message := range input {
			if strings.Contains(message.Content, m.manualContent) {
				return nil, fmt.Errorf("manual-only skill was prefetched")
			}
			m.prefetchSeen = m.prefetchSeen || strings.Contains(message.Content, m.skillContent)
			readSeen = readSeen || (message.Role == schema.Tool && message.ToolCallID == "prefetch-probe-read" && strings.Contains(message.Content, m.readContent))
		}
		if !m.prefetchSeen || !readSeen {
			return nil, fmt.Errorf("following round missing skill=%v read=%v", m.prefetchSeen, readSeen)
		}
		response.Content = "Fixture complete."
	default:
		return nil, fmt.Errorf("unexpected extra model round: %d", m.calls)
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}
