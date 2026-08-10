package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/memdir"
	"gopkg.in/yaml.v3"
)

var errNotAgentDefinition = errors.New("not an agent definition")

const (
	agentSourceBuiltIn = "built-in"
	agentSourceUser    = "user"
	agentSourceProject = "project"
)

type agentStringList []string

func (l *agentStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var values []string
		if err := node.Decode(&values); err != nil {
			return err
		}
		*l = values
		return nil
	case yaml.ScalarNode:
		if strings.TrimSpace(node.Value) == "" {
			*l = nil
			return nil
		}
		parts := strings.Split(node.Value, ",")
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			if value := strings.TrimSpace(part); value != "" {
				values = append(values, value)
			}
		}
		*l = values
		return nil
	default:
		return fmt.Errorf("expected a string or string list")
	}
}

type customAgentFrontmatter struct {
	Name            string          `yaml:"name"`
	Description     string          `yaml:"description"`
	Tools           agentStringList `yaml:"tools"`
	DisallowedTools agentStringList `yaml:"disallowedTools"`
	Model           string          `yaml:"model"`
	PermissionMode  string          `yaml:"permissionMode"`
	MaxTurns        int             `yaml:"maxTurns"`
	OmitClaudeMd    *bool           `yaml:"omitClaudeMd"`
	ReadOnly        *bool           `yaml:"readOnly"`
	Memory          string          `yaml:"memory"`
}

// LoadAgentDefinitions returns the active agent definitions for cwd. Definitions
// are merged in the same broad precedence order as the reference implementation:
// built-ins, user definitions, then project definitions. Later definitions with
// the same name replace earlier ones.
func LoadAgentDefinitions(cwd string) (map[string]BuiltInAgentDef, []error) {
	defs := GetBuiltInAgentDefs()
	var errs []error

	if home, err := os.UserHomeDir(); err == nil {
		errs = append(errs, loadAgentDefinitionsDir(filepath.Join(home, ".claude", "agents"), agentSourceUser, defs)...)
	}
	for _, dir := range projectAgentDirs(cwd) {
		errs = append(errs, loadAgentDefinitionsDir(dir, agentSourceProject, defs)...)
	}
	return defs, errs
}

func loadAgentDefinitionsDir(dir, source string, defs map[string]BuiltInAgentDef) []error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{fmt.Errorf("load agents from %s: %w", dir, err)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		def, err := parseCustomAgentDefinition(path, source)
		if err != nil {
			if errors.Is(err, errNotAgentDefinition) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		if def.Name != "" {
			defs[def.Name] = def
		}
	}
	return errs
}

func parseCustomAgentDefinition(path, source string) (BuiltInAgentDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BuiltInAgentDef{}, fmt.Errorf("read agent %s: %w", path, err)
	}
	frontmatter, body, err := splitAgentFrontmatter(string(data))
	if err != nil {
		if errors.Is(err, errNotAgentDefinition) {
			return BuiltInAgentDef{}, err
		}
		return BuiltInAgentDef{}, fmt.Errorf("parse agent %s: %w", path, err)
	}
	var meta customAgentFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return BuiltInAgentDef{}, fmt.Errorf("parse agent frontmatter %s: %w", path, err)
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	body = strings.TrimSpace(body)
	if meta.Name == "" {
		return BuiltInAgentDef{}, errNotAgentDefinition
	}
	if meta.Description == "" {
		return BuiltInAgentDef{}, fmt.Errorf("parse agent %s: missing required description", path)
	}
	if body == "" {
		return BuiltInAgentDef{}, fmt.Errorf("parse agent %s: prompt body is empty", path)
	}
	if meta.MaxTurns < 0 {
		return BuiltInAgentDef{}, fmt.Errorf("parse agent %s: maxTurns must be zero (unlimited) or positive", path)
	}

	memoryScope := memdir.ParseAgentMemoryScope(meta.Memory)
	if strings.TrimSpace(meta.Memory) != "" && memoryScope == "" {
		return BuiltInAgentDef{}, fmt.Errorf("parse agent %s: unknown memory scope %q", path, meta.Memory)
	}
	tools := append([]string(nil), meta.Tools...)
	if memoryScope != "" && len(tools) > 0 {
		tools = appendMissingAgentMemoryTools(tools)
	}
	readOnly := inferReadOnlyAgent(tools, meta.DisallowedTools)
	if meta.ReadOnly != nil {
		readOnly = *meta.ReadOnly
	}
	omitClaudeMd := false
	if meta.OmitClaudeMd != nil {
		omitClaudeMd = *meta.OmitClaudeMd
	}
	modelName := strings.TrimSpace(meta.Model)
	if strings.EqualFold(modelName, "inherit") {
		modelName = ""
	}

	return BuiltInAgentDef{
		Name:            meta.Name,
		WhenToUse:       meta.Description,
		Tools:           tools,
		DisallowedTools: append([]string(nil), meta.DisallowedTools...),
		Model:           modelName,
		PermissionMode:  strings.TrimSpace(meta.PermissionMode),
		OmitClaudeMd:    omitClaudeMd,
		MaxTurns:        meta.MaxTurns,
		ReadOnly:        readOnly,
		SystemPrompt:    body,
		Source:          source,
		FilePath:        path,
		Memory:          memoryScope,
	}, nil
}

func appendMissingAgentMemoryTools(tools []string) []string {
	seen := make(map[string]struct{}, len(tools)+3)
	for _, tool := range tools {
		seen[strings.ToLower(strings.TrimSpace(tool))] = struct{}{}
	}
	for _, tool := range []string{"Read", "Edit", "Write"} {
		if _, ok := seen[strings.ToLower(tool)]; !ok {
			tools = append(tools, tool)
		}
	}
	return tools
}

func initializeAgentMemorySnapshots(defs map[string]BuiltInAgentDef, projectRoot string) []error {
	if !memdir.IsAutoMemoryEnabled() {
		return nil
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		def := defs[name]
		if def.Source == agentSourceBuiltIn || def.Memory != memdir.ScopeUser {
			continue
		}
		status := memdir.CheckAgentMemorySnapshot(def.Name, def.Memory, projectRoot)
		switch status.Action {
		case memdir.AgentSnapshotInitialize:
			if err := memdir.InitializeAgentMemoryFromSnapshot(def.Name, def.Memory, projectRoot, status.SnapshotTimestamp); err != nil {
				errs = append(errs, fmt.Errorf("initialize memory snapshot for agent %s: %w", def.Name, err))
			}
		case memdir.AgentSnapshotPromptUpdate:
			def.PendingSnapshotUpdate = &AgentSnapshotUpdate{SnapshotTimestamp: status.SnapshotTimestamp}
			defs[name] = def
		}
	}
	return errs
}

func splitAgentFrontmatter(content string) (string, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", "", errNotAgentDefinition
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", "", fmt.Errorf("unterminated YAML frontmatter")
	}
	return rest[:end], rest[end+len("\n---\n"):], nil
}

func projectAgentDirs(cwd string) []string {
	if cwd == "" {
		return nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	var dirs []string
	for current := abs; ; current = filepath.Dir(current) {
		dirs = append(dirs, filepath.Join(current, ".claude", "agents"))
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	// Load outer directories first so definitions nearest cwd win.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

func inferReadOnlyAgent(tools, _ []string) bool {
	if len(tools) == 0 {
		return false
	}
	mutating := map[string]bool{
		"edit": true, "write": true, "notebookedit": true, "bash": true,
		"taskcreate": true, "taskupdate": true, "taskstop": true,
	}
	for _, tool := range tools {
		if mutating[strings.ToLower(strings.TrimSpace(tool))] {
			return false
		}
	}
	return true
}
