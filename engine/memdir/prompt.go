package memdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildUnifiedMemoryPrompt assembles the model-visible private/team policy and
// independently truncated MEMORY.md indexes for one project.
func BuildUnifiedMemoryPrompt(projectRoot string) (string, error) {
	if !IsAutoMemoryEnabled() {
		return "", nil
	}

	privateDir := GetAutoMemPathForProject(projectRoot)
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		return "", fmt.Errorf("creating private memory directory: %w", err)
	}

	teamEnabled := IsTeamMemoryEnabled()
	teamDir := GetTeamMemPath()
	if teamEnabled {
		if err := os.MkdirAll(teamDir, 0o755); err != nil {
			return "", fmt.Errorf("creating team memory directory: %w", err)
		}
	}

	sections := []string{buildMemoryPolicy(privateDir, teamDir, teamEnabled)}
	sections = append(sections, formatMemoryIndex(
		"Private memory index",
		filepath.Join(privateDir, EntrypointName),
		"user's private auto-memory, persists across conversations",
		false,
	))
	if teamEnabled {
		sections = append(sections, formatMemoryIndex(
			"Team memory index",
			filepath.Join(teamDir, EntrypointName),
			"shared team memory from the configured shared directory",
			true,
		))
	}
	return strings.Join(sections, "\n\n"), nil
}

// BuildAgentMemoryPrompt assembles one custom agent's scoped policy and
// independently bounded MEMORY.md index.
func BuildAgentMemoryPrompt(agentType string, scope AgentMemoryScope, projectRoot string) (string, error) {
	if !IsAutoMemoryEnabled() || ParseAgentMemoryScope(string(scope)) == "" {
		return "", nil
	}
	dir := GetAgentMemoryDirForProject(agentType, scope, projectRoot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating agent memory directory: %w", err)
	}
	scopeGuidance := map[AgentMemoryScope]string{
		ScopeUser:    "Keep learnings general because user-scope memory applies across projects.",
		ScopeProject: "Tailor project-scope memory to this project; it may be shared through version control.",
		ScopeLocal:   "Tailor local-scope memory to this project and machine; it is not intended for version control.",
	}[scope]
	policy := fmt.Sprintf(`# Persistent Agent Memory

Agent %q has %s-scope persistent memory at %s. The directory already exists.

%s Use one topic Markdown file per durable learning and maintain concise one-line pointers in MEMORY.md. Verify stale facts against current project state before acting on them.`, agentType, scope, dir, scopeGuidance)
	return policy + "\n\n" + formatMemoryIndex(
		"Agent memory index",
		filepath.Join(dir, EntrypointName),
		fmt.Sprintf("persistent %s-scope memory for agent %s", scope, agentType),
		false,
	), nil
}

func buildMemoryPolicy(privateDir, teamDir string, teamEnabled bool) string {
	var b strings.Builder
	b.WriteString("# Persistent Memory\n\n")
	if teamEnabled {
		fmt.Fprintf(&b, "You have two persistent memory scopes: private at `%s` and shared team memory at `%s`. Both directories already exist.\n\n", privateDir, teamDir)
		b.WriteString("- private: facts and preferences that must remain between the current user and this agent.\n")
		b.WriteString("- team: project knowledge safe to share with every collaborator using the configured shared directory. Never place credentials or user-private information here.\n")
	} else {
		fmt.Fprintf(&b, "You have persistent private memory at `%s`. The directory already exists.\n", privateDir)
	}
	b.WriteString("\nUse the closed memory types `user`, `feedback`, `project`, and `reference`. ")
	b.WriteString("Do not save facts derivable from current code, git history, temporary task state, or material already documented in project instructions.\n\n")
	b.WriteString("Saving memory is a two-step operation: write or update one topic markdown file with `name`, `description`, and `type` frontmatter, then add a concise one-line pointer to the `MEMORY.md` index in the same scope. Keep each index under 200 lines; never put full memory bodies directly in an index.\n\n")
	b.WriteString("When memory seems relevant, read the indexed topic file before relying on it and verify time-sensitive claims against current sources. If the user asks to ignore memory, act as though both indexes were empty.")
	return b.String()
}

func formatMemoryIndex(title, path, description string, shared bool) string {
	content := ""
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		content = TruncateEntrypointContent(string(data)).Content
	}
	if content == "" {
		content = "(empty)"
	}
	body := fmt.Sprintf("Contents of %s (%s):\n\n%s", path, description, content)
	if shared {
		body = fmt.Sprintf("Contents of %s (%s):\n\n<team-memory-content source=\"shared\">\n%s\n</team-memory-content>", path, description, content)
	}
	return "## " + title + "\n\n" + body
}
