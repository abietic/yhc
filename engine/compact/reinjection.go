package compact

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	// PostCompactMaxFilesToRestore is the maximum number of recently-read files
	// to re-inject after compaction.
	PostCompactMaxFilesToRestore = 5

	// PostCompactTokenBudget limits total tokens for re-injected file content.
	PostCompactTokenBudget = 50_000

	// PostCompactMaxTokensPerFile limits tokens per re-injected file.
	PostCompactMaxTokensPerFile = 5_000

	// PostCompactMaxTokensPerSkill limits tokens per re-injected skill.
	PostCompactMaxTokensPerSkill = 5_000

	// PostCompactSkillsTokenBudget limits total tokens for re-injected skills.
	PostCompactSkillsTokenBudget = 25_000

	// skillTruncationMarker is appended when a skill is truncated.
	skillTruncationMarker = "\n\n[... content truncated for context budget ...]"
)

// FileStateEntry represents a recently-read file tracked by the engine.
type FileStateEntry struct {
	Filename  string
	Content   string
	Timestamp int64 // unix millis, higher = more recent
}

// SkillEntry represents an invoked skill for post-compact re-injection.
type SkillEntry struct {
	SkillName string
	SkillPath string
	Content   string
	InvokedAt int64 // unix millis
}

// CreatePostCompactFileAttachments builds attachment messages for recently-read
// files that should be re-injected after compaction so the model retains awareness.
// Mirrors reference compact/compact.ts createPostCompactFileAttachments.
func CreatePostCompactFileAttachments(
	readFileState []FileStateEntry,
	maxFiles int,
	excludePaths map[string]bool,
) []*schema.Message {
	if len(readFileState) == 0 || maxFiles <= 0 {
		return nil
	}

	// Sort by timestamp descending (most recent first)
	sorted := make([]FileStateEntry, len(readFileState))
	copy(sorted, readFileState)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp > sorted[j].Timestamp
	})

	// Filter excluded paths and limit
	var candidates []FileStateEntry
	for _, entry := range sorted {
		if len(candidates) >= maxFiles {
			break
		}
		normalized := normalizePath(entry.Filename)
		if excludePaths != nil && excludePaths[normalized] {
			continue
		}
		candidates = append(candidates, entry)
	}

	// Build attachment messages within token budget
	var attachments []*schema.Message
	usedTokens := 0
	for _, file := range candidates {
		content := truncateToTokens(file.Content, PostCompactMaxTokensPerFile)
		tokens := roughTokenEstimate(content)
		if usedTokens+tokens > PostCompactTokenBudget {
			break
		}
		usedTokens += tokens

		attachments = append(attachments, &schema.Message{
			Role:    schema.User,
			Content: fmt.Sprintf("[File attachment from before compaction — recently read file:]\n\nFile: %s\n```\n%s\n```", file.Filename, content),
			Extra: map[string]any{
				"subtype":  "attachment",
				"type":     "post_compact_file_restore",
				"filename": file.Filename,
			},
		})
	}
	return attachments
}

// CreatePlanAttachmentIfNeeded creates a plan re-injection attachment if a plan
// file exists at the given path.
// Mirrors reference compact/compact.ts createPlanAttachmentIfNeeded.
func CreatePlanAttachmentIfNeeded(planFilePath string) *schema.Message {
	if planFilePath == "" {
		return nil
	}

	content, err := os.ReadFile(planFilePath)
	if err != nil || len(strings.TrimSpace(string(content))) == 0 {
		return nil
	}

	return &schema.Message{
		Role:    schema.User,
		Content: fmt.Sprintf("[Plan file reference — preserved across compaction:]\n\nFile: %s\n```\n%s\n```", planFilePath, strings.TrimSpace(string(content))),
		Extra: map[string]any{
			"subtype":      "attachment",
			"type":         "plan_file_reference",
			"planFilePath": planFilePath,
		},
	}
}

// CreateSkillAttachmentIfNeeded creates a skill re-injection attachment from
// invoked skills, sorted by recency and truncated within budget.
// Mirrors reference compact/compact.ts createSkillAttachmentIfNeeded.
func CreateSkillAttachmentIfNeeded(invokedSkills []SkillEntry) *schema.Message {
	if len(invokedSkills) == 0 {
		return nil
	}

	// Sort most-recent-first
	sorted := make([]SkillEntry, len(invokedSkills))
	copy(sorted, invokedSkills)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].InvokedAt > sorted[j].InvokedAt
	})

	var parts []string
	usedTokens := 0
	for _, skill := range sorted {
		content := truncateToTokens(skill.Content, PostCompactMaxTokensPerSkill)
		tokens := roughTokenEstimate(content)
		if usedTokens+tokens > PostCompactSkillsTokenBudget {
			break
		}
		usedTokens += tokens
		parts = append(parts, fmt.Sprintf("### Skill: %s\nPath: %s\n\n%s", skill.SkillName, skill.SkillPath, content))
	}

	if len(parts) == 0 {
		return nil
	}

	return &schema.Message{
		Role:    schema.User,
		Content: "[Invoked skills — preserved across compaction:]\n\n" + strings.Join(parts, "\n\n---\n\n"),
		Extra: map[string]any{
			"subtype": "attachment",
			"type":    "invoked_skills",
		},
	}
}

// truncateToTokens truncates content to fit within a token budget.
func truncateToTokens(content string, maxTokens int) string {
	if roughTokenEstimate(content) <= maxTokens {
		return content
	}
	charBudget := maxTokens*4 - len(skillTruncationMarker)
	if charBudget < 0 {
		charBudget = 0
	}
	if charBudget >= len(content) {
		return content
	}
	return content[:charBudget] + skillTruncationMarker
}

// roughTokenEstimate approximates token count from character length.
func roughTokenEstimate(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// normalizePath expands ~ and resolves to absolute path.
func normalizePath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
