package commands

import (
	"context"
	"strings"
	"testing"
)

func TestReplacePromptCommandGenerationCommitsMetadataWithCommands(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	candidate := PromptCommandGenerationCandidate{
		Digest: "digest-v1",
		Commands: []*Command{{
			Name:        "plugin:inspect",
			Aliases:     []string{"plugin:i"},
			Description: "Inspect plugin",
			Usage:       "/plugin:inspect",
			Source:      "plugin:plugin",
			Trust:       CommandTrustConfigured,
			legacyExecute: func(
				_ *CommandContext,
				_ string,
			) (*CommandResult, error) {
				return &CommandResult{Output: "generation-v1"}, nil
			},
		}},
		Sources: []PromptCommandSourceSnapshot{{
			Kind:      CommandSourcePlugin,
			Trust:     CommandTrustConfigured,
			Name:      "plugin",
			Version:   "1.0.0",
			Directory: "/plugins/plugin",
			Commands:  1,
			Health:    "healthy",
		}},
	}

	generation, err := registry.ReplacePromptCommandGeneration(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if generation.Revision != 1 ||
		generation.Digest != "digest-v1" ||
		generation.Commands != 1 {
		t.Fatalf("generation = %#v", generation)
	}
	result, err := registry.Dispatch(
		context.Background(),
		EntrypointPlain,
		&CommandContext{},
		"/plugin:i",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "generation-v1" {
		t.Fatalf("dispatch output = %q", result.Output)
	}

	generation.Sources[0].Name = "mutated"
	again := registry.PromptCommandGeneration()
	if again.Sources[0].Name != "plugin" {
		t.Fatalf("live generation mutated through snapshot: %#v", again)
	}
}

func TestReplacePromptCommandGenerationRejectsCollisionsAndRetainsPriorGeneration(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	_, err := registry.ReplacePromptCommandGeneration(PromptCommandGenerationCandidate{
		Digest: "stable",
		Commands: []*Command{{
			Name:   "plugin:stable",
			Source: "plugin:plugin",
			Trust:  CommandTrustConfigured,
			legacyExecute: func(
				_ *CommandContext,
				_ string,
			) (*CommandResult, error) {
				return &CommandResult{Output: "stable"}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = registry.ReplacePromptCommandGeneration(PromptCommandGenerationCandidate{
		Digest: "conflict",
		Commands: []*Command{{
			Name:   "help",
			Source: "plugin:plugin",
			Trust:  CommandTrustConfigured,
			legacyExecute: func(
				_ *CommandContext,
				_ string,
			) (*CommandResult, error) {
				return &CommandResult{}, nil
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("built-in collision error = %v", err)
	}
	if generation := registry.PromptCommandGeneration(); generation.Revision != 1 ||
		generation.Digest != "stable" {
		t.Fatalf("failed replacement changed generation: %#v", generation)
	}
	if registry.Get("plugin:stable") == nil {
		t.Fatal("failed replacement removed prior command")
	}

	_, err = registry.ReplacePromptCommandGeneration(PromptCommandGenerationCandidate{
		Commands: []*Command{
			{
				Name:    "plugin:one",
				Aliases: []string{"plugin:shared"},
				Source:  "plugin:plugin",
				Trust:   CommandTrustConfigured,
				legacyExecute: func(
					_ *CommandContext,
					_ string,
				) (*CommandResult, error) {
					return &CommandResult{}, nil
				},
			},
			{
				Name:    "plugin:two",
				Aliases: []string{"plugin:shared"},
				Source:  "plugin:plugin",
				Trust:   CommandTrustConfigured,
				legacyExecute: func(
					_ *CommandContext,
					_ string,
				) (*CommandResult, error) {
					return &CommandResult{}, nil
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("alias collision error = %v", err)
	}
	if generation := registry.PromptCommandGeneration(); generation.Revision != 1 {
		t.Fatalf("alias collision changed generation: %#v", generation)
	}
}

func TestValidatePromptCommandGenerationUsesReplacementChecksWithoutMutation(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	stable := PromptCommandGenerationCandidate{
		Digest: "stable",
		Commands: []*Command{{
			Name:   "plugin:stable",
			Source: "plugin:stable",
			Trust:  CommandTrustConfigured,
			legacyExecute: func(
				_ *CommandContext,
				_ string,
			) (*CommandResult, error) {
				return &CommandResult{Output: "stable"}, nil
			},
		}},
	}
	if _, err := registry.ReplacePromptCommandGeneration(stable); err != nil {
		t.Fatal(err)
	}
	before := registry.PromptCommandGeneration()
	beforeList := registry.List()

	candidate := PromptCommandGenerationCandidate{
		Digest: "candidate",
		Commands: []*Command{{
			Name:   "plugin:candidate",
			Source: "plugin:candidate",
			Trust:  CommandTrustConfigured,
			legacyExecute: func(
				_ *CommandContext,
				_ string,
			) (*CommandResult, error) {
				return &CommandResult{Output: "candidate"}, nil
			},
		}},
	}
	live, err := registry.ValidatePromptCommandGeneration(candidate)
	if err != nil {
		t.Fatalf("validate candidate: %v", err)
	}
	if live.Revision != before.Revision || live.Digest != before.Digest {
		t.Fatalf("validated live generation = %#v, want %#v", live, before)
	}
	if after := registry.PromptCommandGeneration(); after.Revision != before.Revision ||
		after.Digest != before.Digest || after.Commands != before.Commands {
		t.Fatalf("validation changed generation: before=%#v after=%#v", before, after)
	}
	if registry.Get("plugin:candidate") != nil || len(registry.List()) != len(beforeList) {
		t.Fatal("validation changed the live command registry")
	}

	candidate.Commands[0].Name = "help"
	live, err = registry.ValidatePromptCommandGeneration(candidate)
	if err == nil ||
		!strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("collision validation error = %v", err)
	}
	if live.Revision != before.Revision || live.Digest != before.Digest {
		t.Fatalf("rejected live generation = %#v, want %#v", live, before)
	}
	if after := registry.PromptCommandGeneration(); after.Revision != before.Revision ||
		after.Digest != before.Digest || registry.Get("plugin:stable") == nil {
		t.Fatalf("failed validation changed live state: %#v", after)
	}
}
