package promptctx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// Attachment represents a piece of contextual information injected between
// conversation turns to enrich the model's awareness of the project state.
// This mirrors the reference implementation's attachment system from
// attachments.ts, adapted for the Go runtime.
type Attachment struct {
	// Type classifies the attachment (e.g. "git_status", "memory",
	// "file_state", "mcp_instructions", "custom").
	Type string
	// Content is the textual payload of the attachment.
	Content string
	// Priority controls inclusion order; higher values are more important
	// and appear first in the rendered output.
	Priority int
	// Source describes where this attachment originated (e.g. "git",
	// "memory_file", "file_watcher").
	Source string
	// Dedupe is the deduplication key. If non-empty, only the first
	// attachment with this key will be kept.
	Dedupe string
}

// AttachmentCollector accumulates context attachments with deduplication,
// then renders them into schema.Message slices suitable for injection into
// the conversation. It mirrors the reference's getAttachments() flow:
// collect various signals, deduplicate, sort by priority, and produce
// messages positioned between the system prompt and user messages.
type AttachmentCollector struct {
	attachments []Attachment
	seen        map[string]bool
}

// NewAttachmentCollector creates an empty collector ready for use.
func NewAttachmentCollector() *AttachmentCollector {
	return &AttachmentCollector{
		seen: make(map[string]bool),
	}
}

// Add appends an attachment if its Dedupe key has not been seen before.
// Attachments with an empty Dedupe key are always added (no deduplication).
func (c *AttachmentCollector) Add(a Attachment) {
	if a.Dedupe != "" {
		if c.seen[a.Dedupe] {
			return
		}
		c.seen[a.Dedupe] = true
	}
	c.attachments = append(c.attachments, a)
}

// AddGitStatus runs `git status --short` in projectDir and adds the result
// as a "git_status" attachment. If the directory is not a git repository or
// the command fails, an error is returned and nothing is added.
func (c *AttachmentCollector) AddGitStatus(projectDir string) error {
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolving project dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Verify this is a git repo.
	check := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	check.Dir = absDir
	if out, err := check.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("not a git repository: %s", absDir)
	}

	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "status", "--short")
	cmd.Dir = absDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}

	status := strings.TrimSpace(string(out))
	if status == "" {
		status = "(clean)"
	}

	// Truncate excessively long status output (mirrors MAX_STATUS_CHARS = 2000
	// from the reference implementation).
	const maxStatusChars = 2000
	if len(status) > maxStatusChars {
		status = status[:maxStatusChars] + "\n... (truncated, run `git status` for full output)"
	}

	c.Add(Attachment{
		Type:     "git_status",
		Content:  fmt.Sprintf("Git status:\n%s", status),
		Priority: 80,
		Source:   "git",
		Dedupe:   "git_status",
	})
	return nil
}

// AddGitBranch runs `git branch --show-current` in projectDir and adds the
// result as a "git_status" attachment with the branch name. Returns an error
// if the directory is not a git repository or the command fails.
func (c *AttachmentCollector) AddGitBranch(projectDir string) error {
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolving project dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = absDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch: %w", err)
	}

	branch := strings.TrimSpace(string(out))
	if branch == "" {
		// Detached HEAD or other unusual state.
		branch = "(detached HEAD)"
	}

	c.Add(Attachment{
		Type:     "git_status",
		Content:  fmt.Sprintf("Current branch: %s", branch),
		Priority: 85,
		Source:   "git",
		Dedupe:   "git_branch",
	})
	return nil
}

// AddMemoryFiles reads markdown files from memoryDir (typically .claude/memory
// or a similar path) and adds each as a "memory" attachment. Files that cannot
// be read are silently skipped. Only .md files are included.
func (c *AttachmentCollector) AddMemoryFiles(memoryDir string) error {
	absDir, err := filepath.Abs(memoryDir)
	if err != nil {
		return fmt.Errorf("resolving memory dir: %w", err)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No memory directory is not an error.
		}
		return fmt.Errorf("reading memory dir: %w", err)
	}

	const maxMemoryLines = 200
	const maxMemoryBytes = 4096

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		fullPath := filepath.Join(absDir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		content := string(data)

		// Enforce byte limit (mirrors MAX_MEMORY_BYTES = 4096 from the reference).
		if len(content) > maxMemoryBytes {
			content = content[:maxMemoryBytes] + "\n... (truncated)"
		}

		// Enforce line limit (mirrors MAX_MEMORY_LINES = 200 from the reference).
		lines := strings.Split(content, "\n")
		if len(lines) > maxMemoryLines {
			content = strings.Join(lines[:maxMemoryLines], "\n") + "\n... (truncated)"
		}

		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}

		c.Add(Attachment{
			Type:     "memory",
			Content:  fmt.Sprintf("Memory file %s:\n%s", entry.Name(), content),
			Priority: 70,
			Source:   "memory_file",
			Dedupe:   "memory:" + fullPath,
		})
	}

	return nil
}

// AddFileState summarizes a list of recently touched files for context. Each
// file's existence and modification time is checked; missing files are noted.
func (c *AttachmentCollector) AddFileState(files []string) error {
	if len(files) == 0 {
		return nil
	}

	var summaryLines []string
	for _, file := range files {
		absPath, err := filepath.Abs(file)
		if err != nil {
			absPath = file
		}

		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				summaryLines = append(summaryLines, fmt.Sprintf("  %s (deleted/missing)", filepath.Base(absPath)))
			}
			continue
		}

		relPath := filepath.Base(absPath)
		modAge := time.Since(info.ModTime())
		var ageStr string
		switch {
		case modAge < time.Minute:
			ageStr = "just now"
		case modAge < time.Hour:
			ageStr = fmt.Sprintf("%dm ago", int(modAge.Minutes()))
		case modAge < 24*time.Hour:
			ageStr = fmt.Sprintf("%dh ago", int(modAge.Hours()))
		default:
			ageStr = fmt.Sprintf("%dd ago", int(modAge.Hours()/24))
		}

		summaryLines = append(summaryLines, fmt.Sprintf("  %s (modified %s, %d bytes)", relPath, ageStr, info.Size()))
	}

	if len(summaryLines) == 0 {
		return nil
	}

	c.Add(Attachment{
		Type:     "file_state",
		Content:  fmt.Sprintf("Recently touched files:\n%s", strings.Join(summaryLines, "\n")),
		Priority: 50,
		Source:   "file_watcher",
		Dedupe:   "file_state",
	})
	return nil
}

// Build returns the collected attachments as schema.Message slices, sorted by
// priority (highest first). Each attachment becomes a User message with
// system-reminder wrapping, matching the reference's attachment rendering.
func (c *AttachmentCollector) Build() []*schema.Message {
	if len(c.attachments) == 0 {
		return nil
	}

	// Sort by priority descending (higher priority first).
	sorted := make([]Attachment, len(c.attachments))
	copy(sorted, c.attachments)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	msgs := make([]*schema.Message, 0, len(sorted))
	for _, a := range sorted {
		if strings.TrimSpace(a.Content) == "" {
			continue
		}
		msgs = append(msgs, &schema.Message{
			Role:    schema.User,
			Content: fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", a.Content),
			Extra: map[string]any{
				"is_meta":         true,
				"attachment_type": a.Type,
				"source":          a.Source,
			},
		})
	}
	return msgs
}

// BuildString concatenates all attachment contents into a single context
// string separated by newlines, sorted by priority (highest first). Useful
// for injecting into a system prompt directly rather than as separate messages.
func (c *AttachmentCollector) BuildString() string {
	if len(c.attachments) == 0 {
		return ""
	}

	sorted := make([]Attachment, len(c.attachments))
	copy(sorted, c.attachments)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	parts := make([]string, 0, len(sorted))
	for _, a := range sorted {
		if trimmed := strings.TrimSpace(a.Content); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

// CollectDefaultAttachments is a convenience function that creates an
// AttachmentCollector and populates it with the standard set of context
// attachments: git branch, git status, and memory files. This mirrors the
// reference's getSystemContext + getAttachments default path.
func CollectDefaultAttachments(projectDir string) (*AttachmentCollector, error) {
	c := NewAttachmentCollector()

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return c, fmt.Errorf("resolving project dir: %w", err)
	}

	// Collect git context (errors are non-fatal; the project may not be a
	// git repo, or git may not be installed).
	_ = c.AddGitBranch(absDir)
	_ = c.AddGitStatus(absDir)

	// Collect memory files from .claude/memory if it exists.
	memoryDir := filepath.Join(absDir, ".claude", "memory")
	_ = c.AddMemoryFiles(memoryDir)

	return c, nil
}

// InjectAttachments inserts attachment messages at the correct position in
// the conversation: after the system prompt (if any), before user messages.
// This preserves the reference implementation's positioning where attachments
// appear as meta user messages between the system context and the actual
// conversation history.
func InjectAttachments(messages, attachments []*schema.Message) []*schema.Message {
	if len(attachments) == 0 {
		return messages
	}
	if len(messages) == 0 {
		return attachments
	}

	// Find the insertion point: after system messages, before the first
	// non-system message.
	insertIdx := 0
	for i, msg := range messages {
		if msg.Role == schema.System {
			insertIdx = i + 1
		} else {
			break
		}
	}

	// Build the result: [system...] + [attachments] + [rest...]
	result := make([]*schema.Message, 0, len(messages)+len(attachments))
	result = append(result, messages[:insertIdx]...)
	result = append(result, attachments...)
	result = append(result, messages[insertIdx:]...)
	return result
}
