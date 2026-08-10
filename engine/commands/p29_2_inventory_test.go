package commands

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/provider"
)

type p292CommandInventory struct {
	current  string
	snapshot provider.RuntimeInventorySnapshot
}

func (i p292CommandInventory) GetModelName() string {
	return i.current
}

func (i p292CommandInventory) ModelInventory() provider.RuntimeInventorySnapshot {
	return i.snapshot
}

func TestP292ModelCommandUsesConfiguredSelectorsOnly(t *testing.T) {
	inventory := p292CommandInventory{
		current: "primary",
		snapshot: provider.RuntimeInventorySnapshot{
			Default: "primary",
			Entries: []provider.RuntimeInventoryEntry{
				{
					Selector:    "primary",
					ProfileID:   "primary",
					DisplayName: "Primary profile",
					Provider:    "agenticopenai",
					APIModel:    "gpt-4o",
				},
				{
					Selector:    "legacy:anthropic:custom-model",
					DisplayName: "custom-model",
					Provider:    "agenticclaude",
					APIModel:    "custom-model",
				},
			},
		},
	}
	ctx := &CommandContext{Engine: inventory, Model: inventory.current}
	listed, err := executeModel(ctx, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"primary",
		"legacy:anthropic:custom-model",
	} {
		if !strings.Contains(listed.Output, expected) {
			t.Fatalf("configured selector %q omitted: %s", expected, listed.Output)
		}
	}
	if strings.Contains(listed.Output, "claude-opus-4-6") {
		t.Fatalf("static catalog leaked into configured list: %s", listed.Output)
	}

	selected, err := executeModel(ctx, "legacy:anthropic:custom-model")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Action != ActionChangeModel ||
		selected.Data["model"] != "legacy:anthropic:custom-model" {
		t.Fatalf("model command selection = %#v", selected)
	}
}
