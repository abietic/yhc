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

func TestQueryEngineSkillPrefetchIsNotRepeatedAcrossToolRounds(t *testing.T) {
	for _, withBoundary := range []bool{false, true} {
		name := "fresh"
		if withBoundary {
			name = "after_compact_boundary"
		}
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, "fixture.txt")
			const readContent = "independent multi-round fixture"
			if err := os.WriteFile(path, []byte(readContent), 0o644); err != nil {
				t.Fatal(err)
			}
			const skillContent = "independent skill body that should remain visible once"
			skillPath := filepath.Join(cwd, "SKILL.md")
			if err := os.WriteFile(skillPath, []byte(skillContent), 0o644); err != nil {
				t.Fatal(err)
			}
			registry := skills.NewSkillRegistry()
			registry.Register(&skills.Skill{Name: "trace-lens", FilePath: skillPath, Content: skillContent})
			toolRegistry := tools.NewRegistry()
			tools.RegisterDefaults(toolRegistry)
			mdl := &skillPrefetchFixtureModel{path: path, readContent: readContent, skillContent: skillContent, toolRounds: 3}
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: cwd, TranscriptDir: t.TempDir(), ChatModel: mdl,
				CustomSystemPrompt: "Use the fixture tools.", ToolRegistry: toolRegistry,
				SkillRegistry: registry, PermissionMode: permission.ModeBypassPermissions,
			})
			t.Cleanup(eng.Close)
			if withBoundary {
				eng.SetResumedMessages([]*schema.Message{
					{Role: schema.User, Content: "Earlier trace-lens request."},
					{
						Role: schema.User, Content: "Skill: trace-lens\nSource: " + skillPath + "\nBase directory: " + cwd + "\n\n" + skillContent,
						Extra: map[string]any{"is_meta": true, "attachment_kind": "skill_prefetch", "skill_name": "trace-lens"},
					},
					{Role: schema.System, Content: "retained compact summary", Extra: map[string]any{"subtype": "compact_boundary"}},
				})
			}
			events, admission := eng.SubmitMessage(t.Context(), "Inspect the fixture with trace-lens.")
			if admission.Err != nil {
				t.Fatal(admission.Err)
			}
			assertAutoCompactRestartTerminal(t, events)
			if mdl.calls != 4 {
				t.Fatalf("calls=%d; expected three Read rounds then a final response", mdl.calls)
			}
			for round, count := range mdl.skillCopies {
				want := 1
				if round == 0 {
					want = 0
				}
				if count != want {
					t.Fatalf("model request %d saw %d copies of the skill body, want %d; all counts=%v", round+1, count, want, mdl.skillCopies)
				}
			}
		})
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
	toolRounds    int
	skillCopies   []int
}

func (m *skillPrefetchFixtureModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("fixture requires streaming")
}

func (m *skillPrefetchFixtureModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	response := &schema.Message{Role: schema.Assistant}
	copies := 0
	for _, message := range input {
		if strings.Contains(message.Content, m.skillContent) {
			copies++
		}
		if m.manualContent != "" && strings.Contains(message.Content, m.manualContent) {
			return nil, fmt.Errorf("manual-only skill was prefetched")
		}
	}
	m.skillCopies = append(m.skillCopies, copies)
	if m.calls > 1 {
		readSeen := false
		for _, message := range input {
			readSeen = readSeen || (message.Role == schema.Tool && strings.HasPrefix(message.ToolCallID, "prefetch-probe-read-") && strings.Contains(message.Content, m.readContent))
		}
		m.prefetchSeen = copies > 0
		if !m.prefetchSeen || !readSeen {
			return nil, fmt.Errorf("following round missing skill=%v read=%v", m.prefetchSeen, readSeen)
		}
	}
	toolRounds := m.toolRounds
	if toolRounds == 0 {
		toolRounds = 1
	}
	switch {
	case m.calls <= toolRounds:
		args, err := json.Marshal(map[string]string{"file_path": m.path})
		if err != nil {
			return nil, err
		}
		response.ToolCalls = []schema.ToolCall{{ID: fmt.Sprintf("prefetch-probe-read-%d", m.calls), Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: string(args)}}}
	case m.calls == toolRounds+1:
		response.Content = "Fixture complete."
	default:
		return nil, fmt.Errorf("unexpected extra model round: %d", m.calls)
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}
