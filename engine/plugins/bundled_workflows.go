package plugins

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/commands"
)

const (
	bundledWorkflowSchemaVersion = 1
	bundledWorkflowLocation      = "embed://engine/plugins/bundled/workflows.json"
)

//go:embed bundled/workflows.json
var defaultBundledWorkflowData []byte

// LoaderOptions controls prompt-command sources. A nil BundledWorkflowData
// selects the versioned embedded pack; non-nil data is an injectable source for
// deterministic validation tests.
type LoaderOptions struct {
	Dirs                    []string
	DisableBundledWorkflows bool
	BundledWorkflowData     []byte
}

type bundledWorkflowPack struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ID            string                `json:"id"`
	Version       string                `json:"version"`
	Commands      []bundledWorkflowSpec `json:"commands"`
}

type bundledWorkflowSpec struct {
	Name           string   `json:"name"`
	Aliases        []string `json:"aliases,omitempty"`
	Description    string   `json:"description"`
	Usage          string   `json:"usage"`
	Prompt         string   `json:"prompt"`
	PromptWithArgs string   `json:"promptWithArgs,omitempty"`
}

func buildBundledWorkflowPack(data []byte) (
	[]*commands.Command,
	commands.PromptCommandSourceSnapshot,
	[]byte,
	error,
) {
	var pack bundledWorkflowPack
	if err := json.Unmarshal(data, &pack); err != nil {
		return nil, commands.PromptCommandSourceSnapshot{}, nil,
			fmt.Errorf("parse bundled workflow pack: %w", err)
	}
	if pack.SchemaVersion != bundledWorkflowSchemaVersion {
		return nil, commands.PromptCommandSourceSnapshot{}, nil, fmt.Errorf(
			"unsupported bundled workflow schema %d",
			pack.SchemaVersion,
		)
	}
	pack.ID = strings.TrimSpace(pack.ID)
	pack.Version = strings.TrimSpace(pack.Version)
	if pack.ID == "" || pack.Version == "" {
		return nil, commands.PromptCommandSourceSnapshot{}, nil,
			fmt.Errorf("bundled workflow pack requires id and version")
	}
	if len(pack.Commands) == 0 {
		return nil, commands.PromptCommandSourceSnapshot{}, nil,
			fmt.Errorf("bundled workflow pack has no commands")
	}

	result := make([]*commands.Command, 0, len(pack.Commands))
	seen := make(map[string]struct{})
	for _, spec := range pack.Commands {
		cmd, err := buildBundledWorkflowCommand(pack, spec)
		if err != nil {
			return nil, commands.PromptCommandSourceSnapshot{}, nil, err
		}
		for _, key := range append([]string{cmd.Name}, cmd.Aliases...) {
			key = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(key), "/"))
			if _, duplicate := seen[key]; duplicate {
				return nil, commands.PromptCommandSourceSnapshot{}, nil,
					fmt.Errorf("bundled workflow pack repeats name or alias %q", key)
			}
			seen[key] = struct{}{}
		}
		result = append(result, cmd)
	}

	source := commands.PromptCommandSourceSnapshot{
		Kind:      commands.CommandSourceBundled,
		Trust:     commands.CommandTrustBundled,
		Name:      pack.ID,
		Version:   pack.Version,
		Directory: bundledWorkflowLocation,
		Commands:  len(result),
		Health:    "healthy",
	}
	return result, source, append([]byte(nil), data...), nil
}

func buildBundledWorkflowCommand(
	pack bundledWorkflowPack,
	spec bundledWorkflowSpec,
) (*commands.Command, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Usage = strings.TrimSpace(spec.Usage)
	if spec.Name == "" || spec.Description == "" || spec.Usage == "" ||
		strings.TrimSpace(spec.Prompt) == "" {
		return nil, fmt.Errorf(
			"bundled workflow command requires name, description, usage, and prompt",
		)
	}
	if err := validateWorkflowTemplate(spec.Name, spec.Prompt); err != nil {
		return nil, err
	}
	if spec.PromptWithArgs != "" {
		if err := validateWorkflowTemplate(spec.Name, spec.PromptWithArgs); err != nil {
			return nil, err
		}
	}

	aliases := append([]string(nil), spec.Aliases...)
	return &commands.Command{
		Name:           spec.Name,
		Aliases:        aliases,
		Description:    spec.Description,
		Usage:          spec.Usage,
		Source:         pack.ID,
		SourceVersion:  pack.Version,
		Trust:          commands.CommandTrustBundled,
		Kind:           commands.CommandKindPromptWorkflow,
		Entrypoints:    commands.EntrypointsTUI | commands.EntrypointsPlain,
		Availability:   commands.AvailabilitySupported,
		SideEffect:     commands.SideEffectNone,
		ResultKind:     commands.ResultKindPrompt,
		ExecutionOwner: commands.ExecutionOwnerEntrypoint,
		Execute: func(_ context.Context, ctx *commands.CommandContext) (*commands.CommandResult, error) {
			args := ""
			cwd := ""
			if ctx != nil {
				args = strings.TrimSpace(strings.Join(ctx.Args, " "))
				cwd = ctx.CWD
			}
			template := spec.Prompt
			if args != "" && spec.PromptWithArgs != "" {
				template = spec.PromptWithArgs
			}
			output := strings.ReplaceAll(template, "{{cwd}}", cwd)
			output = strings.ReplaceAll(output, "{{args}}", args)
			return &commands.CommandResult{
				Output: output,
				Action: commands.ActionPrompt,
				Data: map[string]any{
					"source":  pack.ID,
					"version": pack.Version,
					"trust":   string(commands.CommandTrustBundled),
				},
			}, nil
		},
	}, nil
}

func validateWorkflowTemplate(name, template string) error {
	remainder := strings.ReplaceAll(template, "{{cwd}}", "")
	remainder = strings.ReplaceAll(remainder, "{{args}}", "")
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		return fmt.Errorf("bundled workflow command %q has an unknown template field", name)
	}
	return nil
}
