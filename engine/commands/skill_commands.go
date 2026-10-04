package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/abietic/yhc/engine/skills"
)

// SetSkillRegistry binds engine-owned skills. Discovery reads only in-memory
// snapshots; it never walks the filesystem or loads content on a keypress.
func (r *Registry) SetSkillRegistry(registry *skills.SkillRegistry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.skillRegistry = registry
	r.mu.Unlock()
}

type commandRegistrySnapshot struct {
	commands map[string]*Command
	order    []string
	removed  map[string]*RemovedCommand
}

// commandSnapshot seals static commands before projecting one skill snapshot.
// Neither registry lock is held while acquiring the other. Each handler retains
// the same immutable skill bytes that supplied its metadata.
func (r *Registry) commandSnapshot() commandRegistrySnapshot {
	r.mu.RLock()
	snapshot := commandRegistrySnapshot{commands: make(map[string]*Command, len(r.commands)), order: append([]string(nil), r.order...), removed: make(map[string]*RemovedCommand, len(r.removed))}
	for key, cmd := range r.commands {
		snapshot.commands[key] = cmd
	}
	for key, cmd := range r.removed {
		snapshot.removed[key] = cmd
	}
	source := r.skillRegistry
	r.mu.RUnlock()
	if source == nil {
		return snapshot
	}
	available := source.List()
	sort.Slice(available, func(i, j int) bool { return available[i].Name < available[j].Name })
	var projected []*Command
	for _, skill := range available {
		if skill.UserInvocable != nil && !*skill.UserInvocable {
			continue
		}
		name := normalizeCommandKey(skill.Name)
		if name == "" || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' }) {
			continue
		}
		cmd := skillCommand(skill, name)
		if snapshot.commands[cmd.Name] != nil || snapshot.removed[cmd.Name] != nil {
			continue
		}
		snapshot.commands[cmd.Name] = cmd
		snapshot.order = append(snapshot.order, cmd.Name)
		projected = append(projected, cmd)
	}
	// Reserve every qualified name before assigning convenience aliases.
	for _, cmd := range projected {
		alias := strings.TrimPrefix(cmd.Name, "skill:")
		if IsCommand("/"+alias) && snapshot.commands[alias] == nil && snapshot.removed[alias] == nil {
			cmd.Aliases = []string{alias}
			snapshot.commands[alias] = cmd
		}
	}
	return snapshot
}

func skillCommand(skill *skills.Skill, name string) *Command {
	commandName := "skill:" + name
	args := make([]ArgDef, 0, len(skill.Args))
	for _, arg := range skill.Args {
		args = append(args, ArgDef{Name: arg.Name, Type: "string", Required: arg.Required && arg.Default == "", Default: arg.Default, Description: arg.Description})
	}
	hint := strings.TrimSpace(skill.ArgumentHint)
	if hint == "" {
		hint = commandArgDefsHint(args)
	}
	if hint == "" {
		hint = "[arguments]"
	}
	return &Command{
		Name: commandName, Description: skill.Description, Usage: "/" + commandName + " " + hint,
		Source: "skill:" + skill.Source, Trust: CommandTrustConfigured,
		Category: CommandCategoryExtensions, DiscoveryTier: DiscoveryTierSecondary, DisplayOrder: 8100, PhaseScope: PhaseScopeIdleOnly,
		Kind: CommandKindPromptWorkflow, Entrypoints: EntrypointsTUI | EntrypointsPlain,
		Availability: AvailabilitySupported, SideEffect: SideEffectNone, ResultKind: ResultKindPrompt, ExecutionOwner: ExecutionOwnerEntrypoint,
		// Skill.Render owns required/default validation by declared position. The
		// command's free-form Usage is a hint, not a second validation schema.
		Execute: func(ctx context.Context, cmdCtx *CommandContext) (*CommandResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			values := make(map[string]string, len(skill.Args))
			for i, arg := range skill.Args {
				if i < len(cmdCtx.Args) {
					values[arg.Name] = cmdCtx.Args[i]
				}
			}
			// Replace raw-argument placeholders before named substitution so values
			// containing a literal $ARGUMENTS are not interpreted a second time.
			copied := *skill
			arguments := strings.Join(cmdCtx.Args, " ")
			hasArguments := strings.Contains(copied.Content, "$ARGUMENTS")
			copied.Content = strings.ReplaceAll(copied.Content, "$ARGUMENTS", arguments)
			output, err := copied.Render(values)
			if err != nil {
				return nil, fmt.Errorf("/%s: %w", commandName, err)
			}
			if arguments != "" && !hasArguments && (len(skill.Args) == 0 || len(cmdCtx.Args) > len(skill.Args)) {
				output += "\n\nArguments: " + arguments
			}
			// Keep the expansion ordinary prompt text even for injected skills
			// without a file path whose body starts with a slash command.
			heading := "Skill: " + skill.Name
			if skill.FilePath != "" {
				heading += "\nSource: " + skill.FilePath
			}
			output = heading + "\n\n" + output
			return &CommandResult{Action: ActionPrompt, Output: output, Data: map[string]any{"skill": skill.Name}}, nil
		},
	}
}
