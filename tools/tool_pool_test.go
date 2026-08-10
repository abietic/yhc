package tools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func toolInfo(name string) *schema.ToolInfo {
	return &schema.ToolInfo{Name: name}
}

// newTestRegistry returns a registry with a mix of built-in and MCP tools in
// non-alphabetical registration order. It includes hidden and disabled tools so
// callers can exercise filtering behavior.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()

	// Built-ins registered out of order; simple-mode eligible names are Bash,
	// Edit, and Read.
	r.Register(ToolImpl{Info: toolInfo("Write")})
	r.Register(ToolImpl{Info: toolInfo("Alpha")})
	r.Register(ToolImpl{Info: toolInfo("Bash")})
	r.Register(ToolImpl{Info: toolInfo("HiddenTool"), IsHidden: true})
	r.Register(ToolImpl{Info: toolInfo("Edit")})
	r.Register(ToolImpl{Info: toolInfo("DisabledTool")})
	r.Disable("DisabledTool")
	r.Register(ToolImpl{Info: toolInfo("Read")})
	r.Register(ToolImpl{Info: toolInfo("Zeta")})

	// MCP tools registered out of order across two logical servers.
	r.Register(ToolImpl{Info: toolInfo("mcp__server1__alpha")})
	r.Register(ToolImpl{Info: toolInfo("mcp__server2__bravo")})
	r.Register(ToolImpl{Info: toolInfo("mcp__server1__denied")})

	return r
}

func toolNames(infos []*schema.ToolInfo) []string {
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	return names
}

func TestAssembleToolPool_DefaultPartitionedSorting(t *testing.T) {
	r := newTestRegistry(t)

	got := toolNames(AssembleToolPool(r, ToolPoolOptions{}))
	want := []string{
		"Alpha", "Bash", "Edit", "Read", "Write", "Zeta",
		"mcp__server1__alpha", "mcp__server1__denied", "mcp__server2__bravo",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAssembleToolPool_HiddenAndDisabledFiltering(t *testing.T) {
	r := newTestRegistry(t)

	got := toolNames(AssembleToolPool(r, ToolPoolOptions{}))
	for _, excluded := range []string{"HiddenTool", "DisabledTool"} {
		for _, name := range got {
			if name == excluded {
				t.Errorf("expected %q to be filtered out", excluded)
			}
		}
	}
}

func TestAssembleToolPool_SimpleMode(t *testing.T) {
	r := newTestRegistry(t)

	got := toolNames(AssembleToolPool(r, ToolPoolOptions{Simple: true}))
	want := []string{
		"Bash", "Edit", "Read",
		"mcp__server1__alpha", "mcp__server1__denied", "mcp__server2__bravo",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAssembleToolPool_BlanketDenied(t *testing.T) {
	r := newTestRegistry(t)

	opts := ToolPoolOptions{
		BlanketDeniedFn: func(name string) bool {
			// Deny a specific built-in and every tool served by server1.
			return name == "Write" || strings.HasPrefix(name, "mcp__server1__")
		},
	}

	got := toolNames(AssembleToolPool(r, opts))
	want := []string{
		"Alpha", "Bash", "Edit", "Read", "Zeta",
		"mcp__server2__bravo",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAssembleToolPool_AllowedNames(t *testing.T) {
	r := newTestRegistry(t)

	tests := []struct {
		name string
		opts ToolPoolOptions
		want []string
	}{
		{
			name: "explicit empty scope exposes no tools",
			opts: ToolPoolOptions{AllowedNames: []string{}},
			want: []string{},
		},
		{
			name: "named scope restricts to allowed names and keeps partitions sorted",
			opts: ToolPoolOptions{AllowedNames: []string{"Bash", "Read", "mcp__server2__bravo", "Write"}},
			want: []string{"Bash", "Read", "Write", "mcp__server2__bravo"},
		},
		{
			name: "built-in selection preserves independently governed MCP tools",
			opts: ToolPoolOptions{BuiltInNames: []string{"Read"}},
			want: []string{"Read", "mcp__server1__alpha", "mcp__server1__denied", "mcp__server2__bravo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolNames(AssembleToolPool(r, tt.opts))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestAssembleToolPool_BuiltInsContiguousPrefix(t *testing.T) {
	r := newTestRegistry(t)

	cases := []ToolPoolOptions{
		{},
		{Simple: true},
		{AllowedNames: []string{"Bash", "Read", "mcp__server2__bravo"}},
		{
			BlanketDeniedFn: func(name string) bool {
				return strings.HasPrefix(name, "mcp__server1__")
			},
		},
	}

	for i, opts := range cases {
		infos := AssembleToolPool(r, opts)

		// Locate the first MCP tool.
		firstMCP := 0
		for firstMCP < len(infos) && !IsMCPToolName(infos[firstMCP].Name) {
			firstMCP++
		}

		// After the first MCP tool, no built-in may appear.
		for j := firstMCP; j < len(infos); j++ {
			if !IsMCPToolName(infos[j].Name) {
				t.Errorf("case %d: built-in %q appears after MCP tools", i, infos[j].Name)
			}
		}
	}
}

func TestParseToolSelection(t *testing.T) {
	if got := ParseToolPreset("minimal"); got != "" {
		t.Fatalf("ParseToolPreset(minimal) = %q, want empty invalid result", got)
	}

	tests := []struct {
		name   string
		values []string
		preset ToolPreset
		names  []string
	}{
		{name: "default preset case insensitive", values: []string{" DEFAULT "}, preset: PresetDefault},
		{name: "comma and whitespace list", values: []string{"Bash,Edit", "Read"}, names: []string{"Bash", "Edit", "Read"}},
		{name: "explicit empty", values: []string{""}, names: []string{}},
		{name: "unknown preset is a tool name", values: []string{"minimal"}, names: []string{"minimal"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseToolSelection(tt.values)
			if got.Preset != tt.preset || !reflect.DeepEqual(got.Names, tt.names) {
				t.Fatalf("ParseToolSelection(%q) = %#v, want preset=%q names=%v", tt.values, got, tt.preset, tt.names)
			}
		})
	}
}

func TestDefaultPresetExcludesMCPTools(t *testing.T) {
	r := NewRegistry()
	r.Register(ToolImpl{Info: toolInfo("Read")})
	r.Register(ToolImpl{Info: toolInfo("mcp__server__read")})

	got := GetToolsForPreset(r, PresetDefault)
	want := []string{"Read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetToolsForPreset(default) = %v, want %v", got, want)
	}
}
