package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// AgentDefinition describes a loadable agent with its configuration.
// Agents can be defined via markdown frontmatter (.md files) or JSON files
// in .claude/agents/, project .agents/, or plugin directories.
//
// Reference: src/tools/AgentTool/loadAgentsDir.ts (755 lines)
type AgentDefinition struct {
	Name            string   `json:"name" yaml:"name"`
	AgentType       string   `json:"agentType" yaml:"agentType"`
	Description     string   `json:"description" yaml:"description"`
	WhenToUse       string   `json:"whenToUse" yaml:"whenToUse"`
	Prompt          string   `json:"prompt" yaml:"prompt"`
	Tools           []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	DisallowedTools []string `json:"disallowedTools,omitempty" yaml:"disallowedTools,omitempty"`
	Skills          []string `json:"skills,omitempty" yaml:"skills,omitempty"`
	McpServers      []string `json:"mcpServers,omitempty" yaml:"mcpServers,omitempty"`
	Model           string   `json:"model,omitempty" yaml:"model,omitempty"`
	PermissionMode  string   `json:"permissionMode,omitempty" yaml:"permissionMode,omitempty"`
	MaxTurns        int      `json:"maxTurns,omitempty" yaml:"maxTurns,omitempty"`
	Isolation       string   `json:"isolation,omitempty" yaml:"isolation,omitempty"`
	Background      bool     `json:"background,omitempty" yaml:"background,omitempty"`
	Memory          string   `json:"memory,omitempty" yaml:"memory,omitempty"`
	InitialPrompt   string   `json:"initialPrompt,omitempty" yaml:"initialPrompt,omitempty"`
	Filename        string   `json:"filename,omitempty" yaml:"-"`
	BaseDir         string   `json:"baseDir,omitempty" yaml:"-"`
	Source          string   `json:"source,omitempty" yaml:"-"`
}

// AgentLoader loads agent definitions from filesystem directories.
type AgentLoader struct {
	mu     sync.RWMutex
	agents map[string]*AgentDefinition
	dirs   []string
}

// NewAgentLoader creates a loader that searches the given directories.
func NewAgentLoader(dirs ...string) *AgentLoader {
	return &AgentLoader{
		agents: make(map[string]*AgentDefinition),
		dirs:   dirs,
	}
}

// DefaultAgentDirs returns the standard directories to search for agent definitions.
func DefaultAgentDirs(cwd, configDir string) []string {
	var dirs []string
	claudeAgents := filepath.Join(cwd, ".claude", "agents")
	if info, err := os.Stat(claudeAgents); err == nil && info.IsDir() {
		dirs = append(dirs, claudeAgents)
	}
	projectAgents := filepath.Join(cwd, ".agents")
	if info, err := os.Stat(projectAgents); err == nil && info.IsDir() {
		dirs = append(dirs, projectAgents)
	}
	if configDir != "" {
		globalAgents := filepath.Join(configDir, "agents")
		if info, err := os.Stat(globalAgents); err == nil && info.IsDir() {
			dirs = append(dirs, globalAgents)
		}
	}
	return dirs
}

// Load scans all configured directories and loads agent definitions.
func (l *AgentLoader) Load() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.agents = make(map[string]*AgentDefinition)

	for _, dir := range l.dirs {
		if err := l.loadDir(dir); err != nil {
			continue
		}
	}
	return nil
}

// Get returns an agent definition by name.
func (l *AgentLoader) Get(name string) (*AgentDefinition, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.agents[strings.ToLower(name)]
	return a, ok
}

// List returns all loaded agent definitions.
func (l *AgentLoader) List() []*AgentDefinition {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]*AgentDefinition, 0, len(l.agents))
	for _, a := range l.agents {
		result = append(result, a)
	}
	return result
}

// Names returns all loaded agent names.
func (l *AgentLoader) Names() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, 0, len(l.agents))
	for name := range l.agents {
		names = append(names, name)
	}
	return names
}

func (l *AgentLoader) loadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		ext := strings.ToLower(filepath.Ext(entry.Name()))

		switch ext {
		case ".md":
			if err := l.loadMarkdownAgent(path, dir); err != nil {
				continue
			}
		case ".json":
			if err := l.loadJSONAgents(path, dir); err != nil {
				continue
			}
		}
	}
	return nil
}

func (l *AgentLoader) loadMarkdownAgent(path, baseDir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	frontmatter, body := parseFrontmatter(content)
	if frontmatter == nil && body == "" {
		return fmt.Errorf("empty agent file: %s", path)
	}

	def := &AgentDefinition{
		Name:     name,
		Prompt:   strings.TrimSpace(body),
		Filename: name,
		BaseDir:  baseDir,
		Source:   "filesystem",
	}

	if frontmatter != nil {
		// Reference: frontmatter 'name' takes priority over filename
		if fmName, ok := frontmatter["name"].(string); ok && fmName != "" {
			def.Name = fmName
			name = fmName
		}
		if desc, ok := frontmatter["description"].(string); ok {
			def.Description = desc
		}
		if whenToUse, ok := frontmatter["whenToUse"].(string); ok {
			def.WhenToUse = whenToUse
		}
		if agentType, ok := frontmatter["agentType"].(string); ok {
			def.AgentType = agentType
		}
		if model, ok := frontmatter["model"].(string); ok {
			def.Model = model
		}
		if pm, ok := frontmatter["permissionMode"].(string); ok {
			def.PermissionMode = pm
		}
		if mt, ok := frontmatter["maxTurns"]; ok {
			switch v := mt.(type) {
			case int:
				def.MaxTurns = v
			case float64:
				def.MaxTurns = int(v)
			}
		}
		if iso, ok := frontmatter["isolation"].(string); ok {
			def.Isolation = iso
		}
		if bg, ok := frontmatter["background"].(bool); ok {
			def.Background = bg
		}
		if mem, ok := frontmatter["memory"].(string); ok {
			def.Memory = mem
		}
		if ip, ok := frontmatter["initialPrompt"].(string); ok {
			def.InitialPrompt = ip
		}
		if tools, ok := frontmatter["tools"]; ok {
			def.Tools = toStringSlice(tools)
		}
		if dt, ok := frontmatter["disallowedTools"]; ok {
			def.DisallowedTools = toStringSlice(dt)
		}
		if skills, ok := frontmatter["skills"]; ok {
			def.Skills = toStringSlice(skills)
		}
		if mcpServers, ok := frontmatter["mcpServers"]; ok {
			def.McpServers = toStringSlice(mcpServers)
		}
	}

	if def.Description == "" {
		def.Description = fmt.Sprintf("Agent: %s", name)
	}
	if def.MaxTurns < 0 {
		return fmt.Errorf("agent %s: maxTurns must be zero (unlimited) or positive", name)
	}

	l.agents[strings.ToLower(name)] = def
	return nil
}

func (l *AgentLoader) loadJSONAgents(path, baseDir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var agents map[string]json.RawMessage
	if err := json.Unmarshal(data, &agents); err != nil {
		var single AgentDefinition
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return err
		}
		if single.Name == "" {
			single.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		if single.MaxTurns < 0 {
			return fmt.Errorf("agent %s: maxTurns must be zero (unlimited) or positive", single.Name)
		}
		single.BaseDir = baseDir
		single.Source = "filesystem"
		l.agents[strings.ToLower(single.Name)] = &single
		return nil
	}

	for name, raw := range agents {
		var def AgentDefinition
		if err := json.Unmarshal(raw, &def); err != nil {
			continue
		}
		if def.MaxTurns < 0 {
			continue
		}
		def.Name = name
		def.BaseDir = baseDir
		def.Source = "filesystem"
		l.agents[strings.ToLower(name)] = &def
	}
	return nil
}

// parseFrontmatter extracts YAML frontmatter from a markdown file.
// Returns (frontmatter map, body text).
func parseFrontmatter(content string) (map[string]interface{}, string) {
	if !strings.HasPrefix(content, "---") {
		return nil, content
	}

	end := strings.Index(content[3:], "\n---")
	if end < 0 {
		return nil, content
	}

	fmContent := content[3 : end+3]
	body := content[end+3+4:]

	var fm map[string]interface{}
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return nil, content
	}

	return fm, body
}

func toStringSlice(v interface{}) []string {
	switch val := v.(type) {
	case []interface{}:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return val
	case string:
		parts := strings.Split(val, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	}
	return nil
}

// BuiltInAgents returns the default built-in agent definitions.
// Reference: src/tools/AgentTool/builtInAgents.ts
func BuiltInAgents() []*AgentDefinition {
	return []*AgentDefinition{
		{
			Name:        "general-purpose",
			AgentType:   "general-purpose",
			Description: "General-purpose agent for researching complex questions, searching for code, and executing multi-step tasks.",
			WhenToUse:   "When you are searching for a keyword or file and are not confident that you will find the right match in the first few tries.",
			Source:      "built-in",
			BaseDir:     "built-in",
		},
		{
			Name:        "Explore",
			AgentType:   "Explore",
			Description: "Fast read-only search agent for locating code.",
			WhenToUse:   "Use it to find files by pattern, grep for symbols or keywords, or answer where is X defined / which files reference Y.",
			Source:      "built-in",
			BaseDir:     "built-in",
			Tools:       []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch"},
		},
		{
			Name:        "Plan",
			AgentType:   "Plan",
			Description: "Software architect agent for designing implementation plans.",
			WhenToUse:   "Use this when you need to plan the implementation strategy for a task. Returns step-by-step plans.",
			Source:      "built-in",
			BaseDir:     "built-in",
			Tools:       []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch"},
		},
	}
}

// LoadWithBuiltIns loads agent definitions from directories and prepends built-in agents.
// Later-loaded agents override earlier ones with the same name (last-wins).
func (l *AgentLoader) LoadWithBuiltIns() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.agents = make(map[string]*AgentDefinition)

	// Load built-in agents first (can be overridden by user/project agents)
	for _, agent := range BuiltInAgents() {
		l.agents[strings.ToLower(agent.Name)] = agent
	}

	// Then load from directories (overrides built-ins with same name)
	for _, dir := range l.dirs {
		if err := l.loadDir(dir); err != nil {
			continue
		}
	}
	return nil
}
