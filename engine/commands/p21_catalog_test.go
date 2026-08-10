package commands

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestP21CatalogSnapshotAndTombstones(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	if got := len(r.List()); got != 40 {
		t.Fatalf("active commands = %d, want 40", got)
	}
	removed := r.ListRemoved()
	if got := len(removed); got != 27 {
		t.Fatalf("removed commands = %d, want 27", got)
	}
	var snapshot []string
	keys := 0
	for _, removed := range removed {
		keys += 1 + len(removed.Aliases)
		entry := removed.Name
		if len(removed.Aliases) > 0 {
			entry += "=" + strings.Join(removed.Aliases, "|")
		}
		snapshot = append(snapshot, entry)
		if removed.RemovedIn != "P21.0" && removed.RemovedIn != "P21.2" {
			t.Fatalf("removed command %#v has wrong boundary", removed)
		}
		if strings.TrimSpace(removed.Reason) == "" || strings.TrimSpace(removed.Replacement) == "" {
			t.Fatalf("removed command %#v has incomplete guidance", removed)
		}
		if r.Get(removed.Name) != nil || r.GetFor(EntrypointTUI, removed.Name) != nil {
			t.Fatalf("removed command %q leaked into active discovery", removed.Name)
		}
		for _, key := range append([]string{removed.Name}, removed.Aliases...) {
			result, err := r.Dispatch(context.Background(), EntrypointTUI, &CommandContext{}, "/"+key+" ignored")
			if err != nil ||
				result.Action != ActionNone ||
				result.Availability != AvailabilityUnavailable ||
				result.Removed == nil ||
				result.Removed.Name != removed.Name ||
				!strings.Contains(result.Output, "/"+key+" was removed in "+removed.RemovedIn) {
				t.Fatalf("tombstone %q = %#v, %v", key, result, err)
			}
		}
	}
	wantSnapshot := []string{
		"plugin",
		"bug=feedback",
		"undo",
		"rewrite=retry",
		"branch",
		"rewind",
		"color",
		"fast",
		"tag",
		"share",
		"release-notes",
		"mode",
		"bypass=yolo",
		"logout",
		"login",
		"env",
		"output-style",
		"session=remote",
		"stats=cost",
		"settings",
		"allowed-tools",
		"bashes",
		"summary",
		"onboarding",
		"pr-comments",
		"issue",
		"commit-push-pr=cpr",
	}
	if !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("removed snapshot = %#v, want %#v", snapshot, wantSnapshot)
	}
	if keys != 33 {
		t.Fatalf("removed lookup keys = %d, want 33", keys)
	}
	for _, cmd := range r.List() {
		if cmd.Availability != AvailabilitySupported {
			t.Fatalf("active command %q has static availability %q", cmd.Name, cmd.Availability)
		}
	}
}

func TestP21TombstonesReserveOnlyUnqualifiedDynamicNames(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	baseline := r.PromptCommandGeneration()
	if err := r.ReplacePluginCommands([]*Command{testPluginCommand("bug")}); err == nil {
		t.Fatal("unqualified tombstone collision was accepted")
	}
	if got := r.PromptCommandGeneration(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("failed collision changed generation: %#v", got)
	}
	if err := r.ReplacePluginCommands([]*Command{testPluginCommand("plugin:summary")}); err != nil {
		t.Fatalf("source-qualified command rejected: %v", err)
	}
	if restored := r.Get("plugin:summary"); restored == nil || restored.Name != "plugin:summary" {
		t.Fatalf("qualified replacement missing: %#v", restored)
	}
}

func TestP21BundledWorkflowTombstoneGuidance(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	want := map[string]string{
		"summary":        "ask for a summary normally; /compact only when context compaction is desired",
		"onboarding":     "use project-native /init",
		"pr-comments":    "request normally or define/use a qualified configured-plugin command",
		"issue":          "request normally or define/use a qualified configured-plugin command",
		"commit-push-pr": "use /commit, then explicitly request push/PR creation under ordinary tools/permissions, or define a qualified configured workflow",
		"cpr":            "use /commit, then explicitly request push/PR creation under ordinary tools/permissions, or define a qualified configured workflow",
	}
	for name, replacement := range want {
		removed := r.GetRemoved(name)
		if removed == nil || removed.RemovedIn != "P21.2" || removed.Replacement != replacement {
			t.Fatalf("tombstone %q = %#v", name, removed)
		}
		for _, entrypoint := range []Entrypoint{EntrypointTUI, EntrypointPlain, EntrypointHeadless, EntrypointACP} {
			if r.GetFor(entrypoint, name) != nil {
				t.Fatalf("removed workflow %q leaked into %s discovery", name, entrypoint)
			}
		}
	}
}

func TestP21AliasLifecycle(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	stableContext := &CommandContext{
		Engine: fixedDiagnosticsEngine{snapshot: diagnosticCommandFixture()},
	}
	for _, alias := range []string{"reset", "exit", "ctx", "teams"} {
		stable, err := r.Dispatch(context.Background(), EntrypointTUI, stableContext, "/"+alias)
		if err != nil || stable.CompatibilityWarning != nil {
			t.Fatalf("stable alias /%s = %#v, %v", alias, stable, err)
		}
	}

	invalid := []struct {
		name       string
		aliases    []string
		deprecated []string
		boundary   string
	}{
		{name: "missing boundary", aliases: []string{"old"}, deprecated: []string{"old"}},
		{name: "boundary without alias", aliases: []string{"old"}, boundary: "P22"},
		{name: "not an alias", aliases: []string{"old"}, deprecated: []string{"older"}, boundary: "P22"},
		{name: "duplicate deprecated alias", aliases: []string{"old"}, deprecated: []string{"old", "/OLD"}, boundary: "P22"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			err := NewRegistry().Register(&Command{
				Name:    "bad",
				Aliases: tt.aliases,
				Compatibility: CommandCompatibility{
					DeprecatedAliases: tt.deprecated,
					RemovalBoundary:   tt.boundary,
				},
				Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
					return &CommandResult{}, nil
				},
			})
			if err == nil {
				t.Fatal("invalid alias lifecycle was accepted")
			}
		})
	}

	invocations := 0
	if err := r.Register(&Command{
		Name:    "good",
		Aliases: []string{"old"},
		Compatibility: CommandCompatibility{
			DeprecatedAliases: []string{"/OLD"},
			RemovalBoundary:   "P22",
		},
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			invocations++
			return &CommandResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := r.Dispatch(context.Background(), EntrypointTUI, &CommandContext{}, "/old")
	if err != nil ||
		invocations != 1 ||
		result.CompatibilityWarning == nil ||
		result.CompatibilityWarning.Alias != "old" ||
		result.CompatibilityWarning.Replacement != "good" ||
		result.CompatibilityWarning.RemovalBoundary != "P22" ||
		!strings.HasPrefix(result.Output, "Warning: /old is deprecated") {
		t.Fatalf("deprecated alias = %#v, %v", result, err)
	}
}

func TestP21RemovedRegistrationIsClonedAndAtomic(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Command{
		Name: "active",
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			return &CommandResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	original := &RemovedCommand{
		Name:        "/Gone",
		Aliases:     []string{"/OLD"},
		Reason:      "retired",
		Replacement: "/active",
		RemovedIn:   "P21.0",
	}
	if err := r.RegisterRemoved(original); err != nil {
		t.Fatal(err)
	}
	original.Name = "mutated"
	original.Aliases[0] = "mutated"
	got := r.GetRemoved("old")
	if got == nil || got.Name != "gone" || !reflect.DeepEqual(got.Aliases, []string{"old"}) {
		t.Fatalf("stored tombstone followed caller mutation: %#v", got)
	}
	got.Aliases[0] = "mutated"
	if again := r.GetRemoved("old"); again == nil || again.Aliases[0] != "old" {
		t.Fatalf("lookup exposed registry-owned aliases: %#v", again)
	}

	before := r.ListRemoved()
	err := r.RegisterRemoved(&RemovedCommand{
		Name:        "candidate",
		Aliases:     []string{"free", "active"},
		Reason:      "retired",
		Replacement: "/active",
		RemovedIn:   "P21.0",
	})
	if err == nil {
		t.Fatal("removed alias collision with active command was accepted")
	}
	if r.GetRemoved("candidate") != nil || r.GetRemoved("free") != nil ||
		!reflect.DeepEqual(r.ListRemoved(), before) {
		t.Fatal("failed removed registration partially mutated the catalog")
	}
	if err := r.Register(&Command{
		Name:    "new-active",
		Aliases: []string{"gone"},
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			return &CommandResult{}, nil
		},
	}); err == nil {
		t.Fatal("active alias collision with tombstone was accepted")
	}
}
