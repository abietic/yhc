package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/provider"
)

func TestP292ModelPickerUsesOnlyConfiguredInventorySelectors(t *testing.T) {
	picker := NewModelPicker(defaultStyles())
	inventory := provider.RuntimeInventorySnapshot{
		Revision: strings.Repeat("a", 64),
		Default:  "primary",
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
	}

	picker.Show(inventory, "legacy:anthropic:custom-model")
	var selectors []string
	for _, item := range picker.items {
		if !item.isHeader {
			selectors = append(selectors, item.entry.Selector)
		}
	}
	if !slices.Equal(selectors, []string{
		"legacy:anthropic:custom-model",
		"primary",
	}) {
		t.Fatalf("picker selectors = %#v", selectors)
	}
	selected, dismissed := picker.HandleKey(
		tea.KeyPressMsg{Code: tea.KeyEnter},
	)
	if !dismissed || selected != "legacy:anthropic:custom-model" {
		t.Fatalf("picker selection = %q, dismissed=%t", selected, dismissed)
	}
}
