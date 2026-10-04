// Package skills implements a markdown-based skill system that loads reusable
// prompts/capabilities from .md files with YAML frontmatter. Skills can be
// organized in directories and invoked with argument substitution.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// SkillArg defines an argument placeholder that can be substituted in skill content.
type SkillArg struct {
	// Name is the argument identifier used in {{name}} placeholders.
	Name string `yaml:"name"`
	// Description explains what this argument is for.
	Description string `yaml:"description"`
	// Required indicates whether the argument must be provided on invocation.
	Required bool `yaml:"required"`
	// Default is the fallback value when the argument is not provided.
	Default string `yaml:"default"`
}

// Skill represents a loaded markdown skill with its metadata and content.
type Skill struct {
	// Name is the unique identifier for this skill.
	Name string
	// Description is a short summary of what the skill does.
	Description string
	// Content is the markdown body/prompt with optional {{arg}} placeholders.
	Content string
	// FilePath is the absolute path where this skill was loaded from.
	FilePath string
	// Source identifies the runtime source that supplied this skill.
	Source string
	// Health reports whether the loaded skill is available to the Skill tool.
	Health string
	// Tags are optional labels for categorization.
	Tags []string
	// Args defines the argument placeholders this skill accepts.
	Args []SkillArg
	// ArgumentHint is the ghost hint shown when invoking this skill as a command.
	ArgumentHint string
	// UserInvocable controls whether the skill is exposed as a user command. A nil value defaults to true.
	UserInvocable *bool
	// DisableModelInvocation prevents the model-visible Skill tool from invoking it.
	DisableModelInvocation bool
}

// Diagnostic records one skill source that could not enter the live registry.
type Diagnostic struct {
	Source   string
	FilePath string
	Message  string
}

// Snapshot is a stable read-only view of the live skill generation and its
// source diagnostics.
type Snapshot struct {
	Skills      []*Skill
	Diagnostics []Diagnostic
}

// skillFrontmatter is the internal struct for parsing YAML frontmatter.
type skillFrontmatter struct {
	Name                   string     `yaml:"name"`
	Description            string     `yaml:"description"`
	Tags                   []string   `yaml:"tags"`
	Args                   []SkillArg `yaml:"args"`
	ArgumentHint           string     `yaml:"argument-hint"`
	UserInvocable          *bool      `yaml:"user-invocable"`
	DisableModelInvocation bool       `yaml:"disable-model-invocation"`
}

// SkillRegistry manages a collection of loaded skills with thread-safe access.
type SkillRegistry struct {
	skills      map[string]*Skill
	diagnostics []Diagnostic
	mu          sync.RWMutex
}

// NewSkillRegistry creates a new empty skill registry.
func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{
		skills: make(map[string]*Skill),
	}
}

// Register adds or replaces a single skill in the registry.
func (r *SkillRegistry) Register(skill *Skill) {
	if r == nil || skill == nil || skill.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := cloneSkill(skill)
	if cloned.Source == "" {
		cloned.Source = "runtime"
	}
	if cloned.Health == "" {
		cloned.Health = "available"
	}
	r.skills[cloned.Name] = cloned
}

// LoadFromDirectory scans a directory (recursively) for .md files,
// parses their frontmatter and content, and registers them as skills.
func (r *SkillRegistry) LoadFromDirectory(dir string) error {
	return r.LoadFromDirectoryWithSource(dir, "directory")
}

// LoadFromDirectoryWithSource loads a directory while retaining source
// attribution and malformed-file diagnostics.
func (r *SkillRegistry) LoadFromDirectoryWithSource(
	dir string,
	source string,
) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory doesn't exist, nothing to load.
		}
		return fmt.Errorf("skills: stat directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("skills: %s is not a directory", dir)
	}

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			bundle := filepath.Join(path, "SKILL.md")
			if bundleInfo, bundleErr := os.Stat(bundle); bundleErr == nil && !bundleInfo.IsDir() {
				skill, parseErr := ParseSkillFile(bundle)
				if parseErr != nil {
					r.recordDiagnostic(Diagnostic{Source: source, FilePath: bundle, Message: parseErr.Error()})
				} else {
					skill.Source, skill.Health = source, "available"
					r.Register(skill)
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		skill, parseErr := ParseSkillFile(path)
		if parseErr != nil {
			r.recordDiagnostic(Diagnostic{
				Source:   source,
				FilePath: path,
				Message:  parseErr.Error(),
			})
			return nil
		}

		skill.Source = source
		skill.Health = "available"
		r.Register(skill)
		return nil
	})
}

// Get retrieves a skill by name. Returns the skill and true if found,
// or nil and false if not registered.
func (r *SkillRegistry) Get(name string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	return cloneSkill(s), ok
}

// List returns all registered skills in no guaranteed order.
func (r *SkillRegistry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		result = append(result, cloneSkill(s))
	}
	return result
}

// Snapshot returns cloned skill records and diagnostics under one read lock.
func (r *SkillRegistry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := Snapshot{
		Skills:      make([]*Skill, 0, len(r.skills)),
		Diagnostics: append([]Diagnostic(nil), r.diagnostics...),
	}
	for _, skill := range r.skills {
		snapshot.Skills = append(snapshot.Skills, cloneSkill(skill))
	}
	return snapshot
}

// MergeSnapshot atomically adds the supplied skills and diagnostics to the
// registry. Later skills in the snapshot replace earlier skills with the same
// name, matching sequential Register calls without exposing a partial batch.
func (r *SkillRegistry) MergeSnapshot(snapshot Snapshot) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, skill := range snapshot.Skills {
		if skill == nil || skill.Name == "" {
			continue
		}
		cloned := cloneSkill(skill)
		if cloned.Source == "" {
			cloned.Source = "runtime"
		}
		if cloned.Health == "" {
			cloned.Health = "available"
		}
		r.skills[cloned.Name] = cloned
	}
	r.diagnostics = append(r.diagnostics, snapshot.Diagnostics...)
}

// ForProjectDirectory returns an isolated registry generation that preserves
// non-project skills and reloads project-scoped skills from projectDir. This is
// used by worktree-isolated child engines so uncommitted parent skill content
// cannot leak into a child created from committed HEAD.
func (r *SkillRegistry) ForProjectDirectory(projectDir string) (*SkillRegistry, error) {
	next := NewSkillRegistry()
	if r != nil {
		snapshot := r.Snapshot()
		for _, skill := range snapshot.Skills {
			if skill != nil && skill.Source != "project" {
				next.Register(skill)
			}
		}
		for _, diagnostic := range snapshot.Diagnostics {
			if diagnostic.Source != "project" {
				next.recordDiagnostic(diagnostic)
			}
		}
	}
	for _, dir := range []string{
		filepath.Join(projectDir, ".claude", "skills"),
		filepath.Join(projectDir, ".agents", "skills"),
	} {
		if err := next.LoadFromDirectoryWithSource(dir, "project"); err != nil {
			return nil, err
		}
	}
	return next, nil
}

// Search returns skills whose name or description contains the query string
// (case-insensitive).
func (r *SkillRegistry) Search(query string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lower := strings.ToLower(query)
	var result []*Skill
	for _, s := range r.skills {
		if strings.Contains(strings.ToLower(s.Name), lower) ||
			strings.Contains(strings.ToLower(s.Description), lower) {
			result = append(result, cloneSkill(s))
		}
	}
	return result
}

func (r *SkillRegistry) recordDiagnostic(diagnostic Diagnostic) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.diagnostics = append(r.diagnostics, diagnostic)
	r.mu.Unlock()
}

func cloneSkill(skill *Skill) *Skill {
	if skill == nil {
		return nil
	}
	cloned := *skill
	cloned.Tags = append([]string(nil), skill.Tags...)
	cloned.Args = append([]SkillArg(nil), skill.Args...)
	if skill.UserInvocable != nil {
		value := *skill.UserInvocable
		cloned.UserInvocable = &value
	}
	return &cloned
}

// Invoke retrieves a skill by name, substitutes argument placeholders in its
// content using the provided args map, and returns the expanded content ready
// for injection into a prompt. Placeholders use the {{arg_name}} syntax.
func (r *SkillRegistry) Invoke(name string, args map[string]string) (string, error) {
	skill, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("skills: skill %q not found", name)
	}
	return skill.Render(args)
}

// Render substitutes argument placeholders in a stable skill snapshot.
func (skill *Skill) Render(args map[string]string) (string, error) {
	if skill == nil {
		return "", fmt.Errorf("skills: skill is nil")
	}

	// Validate required arguments.
	for _, arg := range skill.Args {
		if arg.Required {
			if _, provided := args[arg.Name]; !provided {
				if arg.Default == "" {
					return "", fmt.Errorf("skills: required argument %q not provided for skill %q", arg.Name, skill.Name)
				}
			}
		}
	}

	// Substitute placeholders.
	content := skill.Content
	for _, arg := range skill.Args {
		placeholder := "{{" + arg.Name + "}}"
		value, provided := args[arg.Name]
		if !provided {
			value = arg.Default
		}
		content = strings.ReplaceAll(content, placeholder, value)
	}

	// Also substitute any ad-hoc placeholders from the args map that may not
	// be declared in the skill's Args list (flexibility for dynamic usage).
	for key, value := range args {
		placeholder := "{{" + key + "}}"
		content = strings.ReplaceAll(content, placeholder, value)
	}

	return content, nil
}

// ParseSkillFile reads a markdown file, splits the YAML frontmatter from the
// body content, parses the frontmatter into skill metadata, and returns the
// assembled Skill struct.
func ParseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skills: read file %s: %w", path, err)
	}
	return ParseSkillData(path, data)
}

// ParseSkillData parses already-authorized skill bytes while retaining path
// attribution. Callers that own a filesystem authority can use this helper
// without reopening the ambient path after validation.
func ParseSkillData(path string, data []byte) (*Skill, error) {
	content := string(data)
	frontmatter, body := splitFrontmatter(content)

	var meta skillFrontmatter
	if frontmatter != "" {
		if yamlErr := yaml.Unmarshal([]byte(frontmatter), &meta); yamlErr != nil {
			return nil, fmt.Errorf("skills: unmarshal frontmatter in %s: %w", path, yamlErr)
		}
	}

	// Derive skill name from frontmatter or filename. A bundle's SKILL.md uses
	// its containing directory so support files cannot become skills.
	name := meta.Name
	if name == "" {
		if strings.EqualFold(filepath.Base(path), "SKILL.md") {
			name = filepath.Base(filepath.Dir(path))
		} else {
			base := filepath.Base(path)
			name = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}

	absPath, _ := filepath.Abs(path)

	return &Skill{
		Name:                   name,
		Description:            meta.Description,
		Content:                body,
		FilePath:               absPath,
		Health:                 "available",
		Tags:                   meta.Tags,
		Args:                   meta.Args,
		ArgumentHint:           meta.ArgumentHint,
		UserInvocable:          meta.UserInvocable,
		DisableModelInvocation: meta.DisableModelInvocation,
	}, nil
}

// LoadDefaultSkills creates a SkillRegistry pre-loaded with skills from the
// standard locations:
//   - ~/.claude/skills/ and ~/.agents/skills/ (user-level skills)
//   - <projectDir>/.claude/skills/ and <projectDir>/.agents/skills/ (project-level skills)
//
// All directories are optional; missing directories are silently skipped.
// Project-level skills take precedence over user-level skills with the same name.
func LoadDefaultSkills(projectDir string) (*SkillRegistry, error) {
	registry := NewSkillRegistry()

	// Load user-level skills first (lower precedence).
	homeDir, err := os.UserHomeDir()
	if err == nil {
		for _, userSkillsDir := range []string{
			filepath.Join(homeDir, ".claude", "skills"),
			filepath.Join(homeDir, ".agents", "skills"),
		} {
			if loadErr := registry.LoadFromDirectoryWithSource(userSkillsDir, "user"); loadErr != nil {
				return nil, fmt.Errorf("skills: load user skills: %w", loadErr)
			}
		}
	}

	// Load project-level skills second (higher precedence, overwrites user-level).
	for _, projectSkillsDir := range []string{
		filepath.Join(projectDir, ".claude", "skills"),
		filepath.Join(projectDir, ".agents", "skills"),
	} {
		if loadErr := registry.LoadFromDirectoryWithSource(projectSkillsDir, "project"); loadErr != nil {
			return nil, fmt.Errorf("skills: load project skills: %w", loadErr)
		}
	}

	return registry, nil
}

// splitFrontmatter separates YAML frontmatter (delimited by --- markers) from
// the markdown body. Returns the raw frontmatter YAML string, the body content,
// and any parsing error.
func splitFrontmatter(content string) (frontmatter, body string) {
	content = strings.TrimLeft(content, "\n\r")

	if !strings.HasPrefix(content, "---") {
		// No frontmatter present; entire content is body.
		return "", content
	}

	// Find the closing --- marker.
	rest := content[3:] // skip opening "---"
	// Skip the rest of the opening line (e.g. "---\n")
	nlIdx := strings.IndexByte(rest, '\n')
	if nlIdx < 0 {
		// Only frontmatter delimiter with no closing — treat as no frontmatter.
		return "", content
	}
	rest = rest[nlIdx+1:]

	closeIdx := strings.Index(rest, "---")
	if closeIdx < 0 {
		// No closing delimiter found.
		return "", content
	}

	// Verify the closing --- is at the start of its line.
	if closeIdx > 0 && rest[closeIdx-1] != '\n' {
		return "", content
	}

	frontmatter = strings.TrimSpace(rest[:closeIdx])
	body = strings.TrimLeft(rest[closeIdx+3:], "\n\r")

	return frontmatter, body
}
