package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func registerPoolTestTool(registry *tools.Registry, name string) {
	registry.Register(tools.ToolImpl{
		Info:    &schema.ToolInfo{Name: name},
		Execute: func(string) (string, error) { return "ok", nil },
	})
}

func poolToolNames(infos []*schema.ToolInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

func TestQueryEngineModelVisibleToolsApplySelectionAndRuntimeDeny(t *testing.T) {
	registry := tools.NewRegistry()
	for _, name := range []string{"Write", "Read", "mcp__github__search"} {
		registerPoolTestTool(registry, name)
	}
	registry.Disable("Write")
	selection := tools.ParseToolSelection([]string{"Read"})
	rules := permission.NewRulesEngine(toolSelectionDenyRules(registry, &selection))
	registry.Enable("Write")
	eng := &QueryEngine{
		config: QueryEngineConfig{
			ToolRegistry:  registry,
			ToolSelection: &selection,
		},
		toolRegistry:    registry,
		permissionRules: rules,
	}

	got := poolToolNames(eng.modelVisibleTools())
	want := []string{"Read", "mcp__github__search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modelVisibleTools() = %v, want %v", got, want)
	}

	canUse := eng.wrapCanUseTool(func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
		return true, ""
	})
	allowed, _ := canUse(context.Background(), "Write", map[string]any{}, nil)
	if allowed {
		t.Fatal("unselected built-in remained executable despite generated runtime deny rule")
	}
}

func TestQueryEngineModelVisibleToolsRefreshFromRegistry(t *testing.T) {
	registry := tools.NewRegistry()
	registerPoolTestTool(registry, "Read")
	eng := &QueryEngine{
		config:          QueryEngineConfig{ToolRegistry: registry},
		toolRegistry:    registry,
		permissionRules: permission.NewRulesEngine(nil),
	}

	if got := poolToolNames(eng.modelVisibleTools()); !reflect.DeepEqual(got, []string{"Read"}) {
		t.Fatalf("initial model tools = %v, want [Read]", got)
	}
	registerPoolTestTool(registry, "mcp__docs__lookup")
	if got := poolToolNames(eng.modelVisibleTools()); !reflect.DeepEqual(got, []string{"Read", "mcp__docs__lookup"}) {
		t.Fatalf("refreshed model tools = %v, want [Read mcp__docs__lookup]", got)
	}
}

func TestQueryEngineExplicitEmptyToolsStillAllowsMCP(t *testing.T) {
	registry := tools.NewRegistry()
	registerPoolTestTool(registry, "Read")
	registerPoolTestTool(registry, "mcp__docs__lookup")
	selection := tools.ParseToolSelection([]string{""})
	eng := &QueryEngine{
		config: QueryEngineConfig{
			ToolRegistry:  registry,
			ToolSelection: &selection,
		},
		toolRegistry:    registry,
		permissionRules: permission.NewRulesEngine(toolSelectionDenyRules(registry, &selection)),
	}

	got := poolToolNames(eng.modelVisibleTools())
	want := []string{"mcp__docs__lookup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modelVisibleTools() = %v, want %v", got, want)
	}
}

func TestQueryEngineRegistryFreeToolsExcludeUnavailableBuiltins(t *testing.T) {
	eng := &QueryEngine{
		config: QueryEngineConfig{
			Tools: []*schema.ToolInfo{
				{Name: "Read"},
				{Name: "EnterWorktree"},
				{Name: "ExitWorktree"},
			},
		},
	}

	got := poolToolNames(eng.modelVisibleTools())
	want := []string{"Read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry-free model tools = %v, want %v", got, want)
	}
}
