package acp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/provider"
)

type p292ACPInventory struct {
	snapshot provider.RuntimeInventorySnapshot
}

func (i *p292ACPInventory) InventorySnapshot() provider.RuntimeInventorySnapshot {
	return i.snapshot
}

func (i *p292ACPInventory) ResolveInventorySelector(
	selector string,
) (provider.RuntimeInventoryEntry, error) {
	for _, entry := range i.snapshot.Entries {
		if strings.EqualFold(strings.TrimSpace(selector), entry.Selector) {
			return entry, nil
		}
	}
	return provider.RuntimeInventoryEntry{}, errors.New("selector unavailable")
}

func (i *p292ACPInventory) ResolveModel(
	selector string,
) (provider.ResolvedConfig, error) {
	entry, err := i.ResolveInventorySelector(selector)
	if err != nil {
		return provider.ResolvedConfig{}, err
	}
	return provider.ResolvedConfig{Config: provider.Config{
		Provider: provider.Provider(entry.Provider),
		Model:    entry.APIModel,
	}}, nil
}

func TestP292ACPOptionsUseConfiguredInventorySelectors(t *testing.T) {
	required := modelcaps.EffectiveModelMetadata{
		Text:         modelcaps.MetadataField[bool]{Value: true, Source: "test"},
		Streaming:    modelcaps.MetadataField[bool]{Value: true, Source: "test"},
		Tools:        modelcaps.MetadataField[bool]{Value: true, Source: "test"},
		SystemPrompt: modelcaps.MetadataField[bool]{Value: true, Source: "test"},
	}
	inventory := &p292ACPInventory{
		snapshot: provider.RuntimeInventorySnapshot{
			Revision: strings.Repeat("a", 64),
			Default:  "primary",
			Entries: []provider.RuntimeInventoryEntry{
				{
					Selector:            "primary",
					ProfileID:           "primary",
					DisplayName:         "Primary profile",
					Provider:            string(provider.ProviderAgenticOpenAI),
					APIModel:            "gpt-4o",
					Metadata:            required,
					RouteIdentityDigest: strings.Repeat("1", 64),
					MetadataDigest:      strings.Repeat("2", 64),
				},
				{
					Selector:            "secondary",
					ProfileID:           "secondary",
					Provider:            string(provider.ProviderAgenticClaude),
					APIModel:            "claude-sonnet-4-6",
					Metadata:            required,
					RouteIdentityDigest: strings.Repeat("3", 64),
					MetadataDigest:      strings.Repeat("4", 64),
				},
			},
		},
	}
	cwd := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "p292-acp-options",
		CWD:           cwd,
		TranscriptDir: filepath.Join(cwd, "transcripts"),
		Model:         "primary",
		ModelResolver: inventory,
	})
	t.Cleanup(eng.Close)

	options := sessionConfigOptions(context.Background(), eng)
	var values []string
	var names []string
	for _, option := range options {
		if option.Select == nil ||
			option.Select.Id != acpsdk.SessionConfigId("model") ||
			option.Select.Options.Ungrouped == nil {
			continue
		}
		for _, candidate := range *option.Select.Options.Ungrouped {
			values = append(values, string(candidate.Value))
			names = append(names, candidate.Name)
		}
	}
	if strings.Join(values, ",") != "primary,secondary" {
		t.Fatalf("ACP model option values = %#v", values)
	}
	if strings.Join(names, ",") !=
		"Primary profile,agenticclaude:claude-sonnet-4-6" {
		t.Fatalf("ACP model option labels = %#v", names)
	}
}
