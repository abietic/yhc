package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/skills"
	"github.com/cloudwego/eino/schema"
)

// DefaultSkillRegistry is the global skill registry that tools can reference.
// Set by InitSkills during initialization.
var DefaultSkillRegistry *skills.SkillRegistry

type skillRegistryCtxKey struct{}

// WithSkillRegistry returns a context carrying the given SkillRegistry.
func WithSkillRegistry(ctx context.Context, r *skills.SkillRegistry) context.Context {
	return context.WithValue(ctx, skillRegistryCtxKey{}, r)
}

// SkillRegistryFromCtx returns the per-engine SkillRegistry from context,
// falling back to DefaultSkillRegistry if not set.
func SkillRegistryFromCtx(ctx context.Context) *skills.SkillRegistry {
	if r, ok := ctx.Value(skillRegistryCtxKey{}).(*skills.SkillRegistry); ok && r != nil {
		return r
	}
	return DefaultSkillRegistry
}

// InitSkills initializes the default skill registry by loading skills from disk.
// It loads from standard locations: <projectDir>/.claude/skills/ and ~/.claude/skills/.
func InitSkills(projectDir string) error {
	registry, err := skills.LoadDefaultSkills(projectDir)
	if err != nil {
		return fmt.Errorf("init skills: %w", err)
	}
	DefaultSkillRegistry = registry
	return nil
}

// SkillTool returns a tool implementation that allows the agent to invoke
// registered skills by name with optional argument substitution.
func SkillTool() ToolImpl {
	return SkillToolForRegistry(DefaultSkillRegistry)
}

// SkillToolForRegistry builds the model-visible skill description for one
// engine while execution still resolves the current registry from context.
func SkillToolForRegistry(registry *skills.SkillRegistry) ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Skill",
			Desc: skillToolDescription(registry),
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"skill":     {Type: schema.String, Desc: "The name of the skill to invoke", Required: true},
				"arguments": {Type: schema.Object, Desc: "Optional positional arguments for runtime placeholder substitution in skill content"},
			}),
		},
		Execute: executeSkill,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return executeSkillWithRegistry(input, SkillRegistryFromCtx(ctx))
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
	}
}

func skillToolDescription(registry *skills.SkillRegistry) string {
	const base = "Execute a skill within the main conversation. Skills provide specialized capabilities and domain knowledge. Use when the user's request matches an available skill."
	if registry == nil {
		return base
	}
	available := registry.List()
	sort.Slice(available, func(i, j int) bool { return available[i].Name < available[j].Name })
	entries := make([]string, 0, len(available))
	for _, skill := range available {
		if skill == nil || skill.Name == "" {
			continue
		}
		entry := skill.Name
		if skill.Description != "" {
			entry += ": " + skill.Description
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return base
	}
	return base + " Available skills: " + strings.Join(entries, "; ")
}

func executeSkill(input string) (string, error) {
	return executeSkillWithRegistry(input, DefaultSkillRegistry)
}

func executeSkillWithRegistry(input string, registry *skills.SkillRegistry) (string, error) {
	var params struct {
		Skill     string         `json:"skill"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("skill: invalid params: %w", err)
	}
	if params.Skill == "" {
		return "", fmt.Errorf("skill: skill parameter is required")
	}

	if registry == nil {
		return "", fmt.Errorf("skill: skill registry not initialized")
	}

	// Convert arguments from map[string]any to map[string]string for the registry.
	args := make(map[string]string, len(params.Arguments))
	for k, v := range params.Arguments {
		switch val := v.(type) {
		case string:
			args[k] = val
		default:
			// Marshal non-string values to their JSON representation.
			b, err := json.Marshal(val)
			if err != nil {
				args[k] = fmt.Sprintf("%v", val)
			} else {
				args[k] = string(b)
			}
		}
	}

	content, err := registry.Invoke(params.Skill, args)
	if err != nil {
		return "", fmt.Errorf("skill: %w", err)
	}

	return content, nil
}
