package commands

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestP21DiscoveryMetadataSnapshot(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	var got []string
	for _, cmd := range r.List() {
		got = append(got, fmt.Sprintf(
			"%s=%s/%s/%d/%s",
			cmd.Name,
			cmd.Category,
			cmd.DiscoveryTier,
			cmd.DisplayOrder,
			cmd.PhaseScope,
		))
	}
	want := []string{
		"help=UI/secondary/7010/idle-only",
		"new=Session/primary/10/idle-only",
		"clear=Session/secondary/1010/idle-only",
		"compact=Session/primary/20/idle-only",
		"status=Runtime/primary/70/idle-only",
		"model=Runtime/primary/40/idle-only",
		"sessions=Session/primary/30/idle-only",
		"resume=Session/secondary/1020/idle-only",
		"terminal=Runtime/secondary/2010/idle-only",
		"suspend=Runtime/secondary/2020/idle-only",
		"quit=Runtime/secondary/2030/idle-only",
		"version=Runtime/secondary/2040/idle-only",
		"usage=Runtime/secondary/2050/idle-only",
		"context=Runtime/secondary/2060/idle-only",
		"config=Runtime/secondary/2070/idle-only",
		"diff=Workspace/primary/80/idle-only",
		"copy=UI/secondary/7020/idle-only",
		"init=Workspace/secondary/4010/idle-only",
		"permissions=Safety/primary/60/idle-only",
		"hooks=Safety/secondary/3010/idle-only",
		"doctor=Safety/secondary/3020/idle-only",
		"plan=Safety/primary/50/idle-only",
		"goal=Runtime/secondary/9000/idle-only",
		"memory=Workspace/secondary/4020/idle-only",
		"add-dir=Workspace/secondary/4030/idle-only",
		"tasks=Agents/secondary/5010/idle-only",
		"effort=Runtime/secondary/2080/idle-only",
		"skills=Extensions/secondary/6010/idle-only",
		"agents=Agents/primary/100/idle-only",
		"agent=Agents/secondary/5020/any",
		"mcp=Extensions/secondary/6020/idle-only",
		"team=Agents/secondary/5030/any",
		"queue=Agents/secondary/5040/any",
		"files=Workspace/primary/90/idle-only",
		"vim=UI/secondary/7030/idle-only",
		"theme=UI/secondary/7040/idle-only",
		"fork=Session/secondary/1030/idle-only",
		"key" + "bindings=UI/secondary/7050/any",
		"search=UI/secondary/7060/idle-only",
		"reload-plugins=Extensions/secondary/6030/idle-only",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discovery metadata:\n got %v\nwant %v", got, want)
	}

	for _, invalid := range []*Command{
		{Name: "bad-category", Category: "Unknown"},
		{Name: "bad-tier", DiscoveryTier: "featured"},
		{Name: "bad-order", DisplayOrder: -1},
		{Name: "bad-phase", PhaseScope: "sometimes"},
	} {
		invalid.Execute = func(context.Context, *CommandContext) (*CommandResult, error) {
			return &CommandResult{}, nil
		}
		if err := NewRegistry().Register(invalid); err == nil {
			t.Fatalf("invalid discovery metadata accepted: %#v", invalid)
		}
	}
}

func TestP21DynamicDiscoveryDefaults(t *testing.T) {
	r := NewRegistry()
	for _, cmd := range []*Command{
		{
			Name:  "review",
			Kind:  CommandKindPromptWorkflow,
			Trust: CommandTrustBundled,
		},
		{
			Name:  "commit",
			Kind:  CommandKindPromptWorkflow,
			Trust: CommandTrustBundled,
		},
		{
			Name:   "plugin:inspect",
			Kind:   CommandKindPromptWorkflow,
			Source: "plugin:example",
			Trust:  CommandTrustConfigured,
		},
		{
			Name: "custom-query",
		},
		{
			Name:  "configured-query",
			Kind:  CommandKindQuery,
			Trust: CommandTrustConfigured,
		},
	} {
		cmd.Execute = func(context.Context, *CommandContext) (*CommandResult, error) {
			return &CommandResult{}, nil
		}
		if err := r.Register(cmd); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		category CommandCategory
		tier     DiscoveryTier
		order    int
	}{
		{name: "review", category: CommandCategoryWorkflow, tier: DiscoveryTierPrimary, order: 110},
		{name: "commit", category: CommandCategoryWorkflow, tier: DiscoveryTierPrimary, order: 120},
		{name: "plugin:inspect", category: CommandCategoryWorkflow, tier: DiscoveryTierSecondary, order: 8000},
		{name: "custom-query", category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 9000},
		{name: "configured-query", category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 9000},
	}
	for _, tt := range tests {
		cmd := r.Get(tt.name)
		if cmd == nil ||
			cmd.Category != tt.category ||
			cmd.DiscoveryTier != tt.tier ||
			cmd.DisplayOrder != tt.order ||
			cmd.PhaseScope != PhaseScopeIdleOnly {
			t.Fatalf("%s discovery metadata = %#v", tt.name, cmd)
		}
	}
}

func TestP21PrimaryDiscoveryOrderAndPhaseProjection(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	got := r.ListForContext(context.Background(), EntrypointTUI, &CommandContext{})
	var names []string
	for _, cmd := range got {
		if cmd.DiscoveryTier == DiscoveryTierPrimary {
			names = append(names, cmd.Name)
		}
	}
	want := []string{
		"new", "compact", "sessions", "model", "plan",
		"permissions", "status", "diff", "files", "agents",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("primary order = %v, want %v", names, want)
	}

	active := &CommandContext{
		Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn},
	}
	got = r.ListForContext(context.Background(), EntrypointTUI, active)
	names = names[:0]
	for _, cmd := range got {
		names = append(names, cmd.Name)
	}
	if want := []string{"agent", "team", "queue", "keybindings"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("active TUI = %v, want %v", names, want)
	}
	if active.Entrypoint != "" || active.Environment.Entrypoint != "" {
		t.Fatalf("discovery mutated detached caller environment: %#v", active)
	}
	for _, entrypoint := range []Entrypoint{
		EntrypointPlain,
		EntrypointHeadless,
		EntrypointACP,
	} {
		if got := r.ListForContext(
			context.Background(),
			entrypoint,
			active,
		); len(got) != 0 {
			t.Fatalf("active %s leaked %d commands", entrypoint, len(got))
		}
	}
}

func TestP21DispatchRechecksPhaseBeforeExecute(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(&Command{
		Name:       "idle",
		PhaseScope: PhaseScopeIdleOnly,
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			called = true
			return &CommandResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if r.GetForContext(
		context.Background(),
		EntrypointTUI,
		&CommandContext{Environment: CommandEnvironment{Phase: CommandPhaseIdle}},
		"idle",
	) == nil {
		t.Fatal("idle command was not discoverable while idle")
	}

	result, err := r.Dispatch(
		context.Background(),
		EntrypointTUI,
		&CommandContext{
			Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn},
		},
		"/idle",
	)
	if err != nil {
		t.Fatal(err)
	}
	if called ||
		result.Action != ActionNone ||
		result.Availability != AvailabilityUnavailable ||
		result.Output != "/idle is unavailable: command is available only while no request is running." {
		t.Fatalf("result=%+v called=%v", result, called)
	}
}

func TestP21ActiveTurnSubcommandsRemainSerialized(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	for _, input := range []string{
		"/agent create helper",
		"/team create dev",
		"/keybindings verbose",
	} {
		result, err := r.Dispatch(
			context.Background(),
			EntrypointTUI,
			&CommandContext{
				Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn},
			},
			input,
		)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if result.Action != ActionNone ||
			result.Availability != AvailabilityUnavailable ||
			!strings.Contains(result.Output, "without arguments") {
			t.Fatalf("%s result = %#v", input, result)
		}
	}

	result, err := r.Dispatch(
		context.Background(),
		EntrypointTUI,
		&CommandContext{
			Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn},
		},
		"/queue list",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ActionNone ||
		result.Output != "Queue controls are available in the interactive TUI." {
		t.Fatalf("/queue list result = %#v", result)
	}
}

func TestP21HelpGroupsReachablePrimaryAndSecondaryCommands(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	result, err := r.Dispatch(
		context.Background(),
		EntrypointTUI,
		&CommandContext{},
		"/help",
	)
	if err != nil {
		t.Fatal(err)
	}
	headings := []string{
		"Session:",
		"Runtime:",
		"Safety:",
		"Workspace:",
		"Agents:",
		"Extensions:",
		"UI:",
	}
	last := -1
	for _, heading := range headings {
		index := strings.Index(result.Output, heading)
		if index <= last {
			t.Fatalf("help category %q missing or out of order:\n%s", heading, result.Output)
		}
		last = index
	}
	for _, command := range []string{"/new", "/clear", "/status", "/hooks", "/add-dir", "/tasks", "/reload-plugins", "/theme"} {
		if !strings.Contains(result.Output, command) {
			t.Fatalf("help hid secondary or reachable command %q:\n%s", command, result.Output)
		}
	}
}
