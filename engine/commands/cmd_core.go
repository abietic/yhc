package commands

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/provider"
)

// statusEngine is the legacy model-control subset used by /model.
type statusEngine interface {
	GetModelName() string
}

type modelInventoryEngine interface {
	statusEngine
	ModelInventory() provider.RuntimeInventorySnapshot
}

// executeClear implements /clear — clear conversation history and show what was cleared.
// The actual clearing is performed by the engine when it sees ActionClear.
// Mirrors reference commands/clear/clear.ts behavior.
func executeClear(ctx *CommandContext, args string) (*CommandResult, error) {
	// Gather stats about what will be cleared.
	msgCount := 0
	tokenEstimate := 0
	if ctx.Messages != nil {
		msgCount = len(ctx.Messages)
		tokenEstimate = compact.EstimateTokenCount(ctx.Messages)
	}

	var sb strings.Builder
	if msgCount == 0 {
		sb.WriteString("Conversation already empty.")
	} else {
		fmt.Fprintf(&sb, "Cleared %d message(s) (~%d tokens).", msgCount, tokenEstimate)
	}

	return &CommandResult{
		Output: sb.String(),
		Action: ActionClear,
	}, nil
}

// executeCompact implements /compact — trigger manual compaction with optional instructions.
// The actual compaction is performed by the engine when it sees ActionCompact.
// Mirrors reference commands/compact/compact.ts behavior (supporting custom instructions).
func executeCompact(ctx *CommandContext, args string) (*CommandResult, error) {
	msgCount := 0
	tokenEstimate := 0
	if ctx.Messages != nil {
		msgCount = len(ctx.Messages)
		tokenEstimate = compact.EstimateTokenCount(ctx.Messages)
	}

	if msgCount == 0 {
		return &CommandResult{Output: "Nothing to compact (no messages in session)."}, nil
	}

	var output string
	if args != "" {
		output = fmt.Sprintf("Compacting %d messages (~%d tokens) with instructions: %s", msgCount, tokenEstimate, args)
	} else {
		output = fmt.Sprintf("Compacting %d messages (~%d tokens)...", msgCount, tokenEstimate)
	}

	data := map[string]any{}
	if strings.TrimSpace(args) != "" {
		data["custom_instructions"] = strings.TrimSpace(args)
	}

	return &CommandResult{
		Output: output,
		Action: ActionCompact,
		Data:   data,
	}, nil
}

// executeModel implements /model — show current model and request a validated
// engine-owned switch. Presentation uses only the configured runtime inventory.
func executeModel(ctx *CommandContext, args string) (*CommandResult, error) {
	args = strings.TrimSpace(args)

	if args == "" {
		return modelShowCurrent(ctx)
	}

	// Handle subcommands.
	parts := strings.Fields(args)
	switch parts[0] {
	case "list", "ls":
		return modelList(ctx)
	default:
		return modelSwitch(ctx, args)
	}
}

func modelShowCurrent(ctx *CommandContext) (*CommandResult, error) {
	model := ctx.Model
	if model == "" {
		if eng, ok := ctx.Engine.(statusEngine); ok {
			model = eng.GetModelName()
		}
	}

	var sb strings.Builder
	sb.WriteString("Current Model\n")
	sb.WriteString("=============\n\n")

	if model == "" {
		sb.WriteString("  No model configured.\n")
		sb.WriteString("\n  Set via: PROV_MODEL environment variable or /model <name>\n")
	} else {
		fmt.Fprintf(&sb, "  Model: %s\n", model)

		if entry, ok := commandInventoryEntry(ctx, model); ok {
			if metadataSourceKnown(
				entry.Metadata.ContextWindowTokens.Source,
			) {
				fmt.Fprintf(
					&sb,
					"  Context window: %d tokens\n",
					entry.Metadata.ContextWindowTokens.Value,
				)
			}
			if metadataSourceKnown(entry.Metadata.MaxOutputTokens.Source) {
				fmt.Fprintf(
					&sb,
					"  Max output: %d tokens\n",
					entry.Metadata.MaxOutputTokens.Value,
				)
			}
			features := []string{}
			if entry.Metadata.Thinking.Value {
				features = append(features, "thinking")
			}
			if entry.Metadata.Images.Value {
				features = append(features, "images")
			}
			if entry.Metadata.PDFs.Value {
				features = append(features, "PDFs")
			}
			if len(features) > 0 {
				fmt.Fprintf(&sb, "  Features: %s\n", strings.Join(features, ", "))
			}
		}
	}

	sb.WriteString("\n  Use /model list to see available models.")
	sb.WriteString("\n  Use /model <name> to switch models.")

	return &CommandResult{Output: sb.String()}, nil
}

func modelList(ctx *CommandContext) (*CommandResult, error) {
	var sb strings.Builder
	sb.WriteString("Available Models\n")
	sb.WriteString("================\n\n")

	currentModel := ctx.Model
	if currentModel == "" {
		if eng, ok := ctx.Engine.(statusEngine); ok {
			currentModel = eng.GetModelName()
		}
	}

	inventory := commandInventory(ctx)
	currentProvider := ""
	for _, entry := range inventory.Entries {
		if entry.Provider != currentProvider {
			if currentProvider != "" {
				sb.WriteString("\n")
			}
			currentProvider = entry.Provider
			fmt.Fprintf(&sb, "  %s:\n", entry.Provider)
		}
		marker := "  "
		if strings.EqualFold(entry.Selector, currentModel) {
			marker = "> "
		}
		features := []string{}
		if entry.Metadata.Tools.Value {
			features = append(features, "tools")
		}
		if entry.Metadata.Thinking.Value {
			features = append(features, "thinking")
		}
		if entry.Metadata.Images.Value || entry.Metadata.PDFs.Value {
			features = append(features, "media")
		}
		featureStr := ""
		if len(features) > 0 {
			featureStr = " [" + strings.Join(features, ", ") + "]"
		}
		contextLabel := "unknown ctx"
		if metadataSourceKnown(entry.Metadata.ContextWindowTokens.Source) {
			contextLabel = fmt.Sprintf(
				"%dk ctx",
				entry.Metadata.ContextWindowTokens.Value/1000,
			)
		}
		fmt.Fprintf(
			&sb,
			"    %s%-40s %s%s\n",
			marker,
			entry.Selector,
			contextLabel,
			featureStr,
		)
	}
	if len(inventory.Entries) == 0 {
		sb.WriteString("  Configured model inventory is unavailable.\n")
	} else {
		sb.WriteString("\n")
	}

	sb.WriteString("  Use /model <name> to switch to a model.")

	return &CommandResult{Output: sb.String()}, nil
}

func metadataSourceKnown(source string) bool {
	source = strings.TrimSpace(source)
	return source != "" && source != "unknown"
}

func modelSwitch(ctx *CommandContext, modelName string) (*CommandResult, error) {
	modelName = strings.TrimSpace(modelName)
	var sb strings.Builder
	if entry, ok := commandInventoryEntry(ctx, modelName); ok {
		fmt.Fprintf(&sb, "Switching to: %s", entry.Selector)
		if entry.DisplayName != "" &&
			entry.DisplayName != entry.Selector {
			fmt.Fprintf(&sb, " (%s)", entry.DisplayName)
		}
		features := []string{}
		if entry.Metadata.Tools.Value {
			features = append(features, "tools")
		}
		if entry.Metadata.Thinking.Value {
			features = append(features, "thinking")
		}
		if len(features) > 0 {
			fmt.Fprintf(&sb, " [%s]", strings.Join(features, ", "))
		}
		modelName = entry.Selector
	} else {
		fmt.Fprintf(&sb, "Selecting model: %s", modelName)
	}

	return &CommandResult{
		Output: sb.String(),
		Action: ActionChangeModel,
		Data:   map[string]any{"model": modelName},
	}, nil
}

func commandInventory(ctx *CommandContext) provider.RuntimeInventorySnapshot {
	if ctx == nil {
		return provider.RuntimeInventorySnapshot{}
	}
	if engine, ok := ctx.Engine.(modelInventoryEngine); ok {
		return engine.ModelInventory()
	}
	return provider.RuntimeInventorySnapshot{}
}

func commandInventoryEntry(
	ctx *CommandContext,
	selector string,
) (provider.RuntimeInventoryEntry, bool) {
	selector = strings.TrimSpace(selector)
	for _, entry := range commandInventory(ctx).Entries {
		if strings.EqualFold(entry.Selector, selector) {
			return entry, true
		}
	}
	return provider.RuntimeInventoryEntry{}, false
}
