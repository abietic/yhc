package promptctx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// BuildUserContext mirrors the reference repo's user-facing prompt context:
// current date, current working directory, and platform details.
func BuildUserContext(cwd string, now time.Time) map[string]string {
	ctx := map[string]string{
		"currentDate": fmt.Sprintf("Today's date is %s.", now.Format("2006-01-02")),
		"platform":    fmt.Sprintf("Platform: %s/%s.", runtime.GOOS, runtime.GOARCH),
	}
	if cwd != "" {
		ctx["cwd"] = fmt.Sprintf("Current working directory: %s", cwd)
	}
	return ctx
}

// BuildSystemContext gathers slower system-derived context. Today this is the
// git snapshot used heavily by the reference runtime.
func BuildSystemContext(ctx context.Context, cwd string) map[string]string {
	result := map[string]string{}
	if gitStatus := buildGitStatus(ctx, cwd); gitStatus != "" {
		result["gitStatus"] = gitStatus
	}
	return result
}

// ComposeSystemPrompt folds the configured system prompt together with the
// user/system context blocks in a stable, inspectable format.
func ComposeSystemPrompt(base, appendPrompt string, userContext, systemContext map[string]string) string {
	sections := make([]string, 0, 4)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		sections = append(sections, trimmed)
	}
	if rendered := renderContextBlock("User context", userContext); rendered != "" {
		sections = append(sections, rendered)
	}
	if rendered := renderContextBlock("System context", systemContext); rendered != "" {
		sections = append(sections, rendered)
	}
	if trimmed := strings.TrimSpace(appendPrompt); trimmed != "" {
		sections = append(sections, trimmed)
	}
	return strings.Join(sections, "\n\n")
}

// ComposeBaseSystemPrompt keeps just the configured prompt layers; the query
// loop appends runtime system context later, mirroring the reference runtime.
func ComposeBaseSystemPrompt(base, appendPrompt string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(appendPrompt); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, "\n\n")
}

// AppendSystemContext mirrors query.ts + utils/api.ts by appending the system
// context to the model-facing system prompt right before the model call.
func AppendSystemContext(systemPrompt *schema.Message, context map[string]string) *schema.Message {
	rendered := renderInlineContext(context)
	if systemPrompt == nil && rendered == "" {
		return nil
	}

	result := &schema.Message{Role: schema.System}
	if systemPrompt != nil {
		copied := *systemPrompt
		result = &copied
		if result.Role == "" {
			result.Role = schema.System
		}
	}

	parts := make([]string, 0, 2)
	if result.Content != "" {
		parts = append(parts, result.Content)
	}
	if rendered != "" {
		parts = append(parts, rendered)
	}
	result.Content = strings.Join(parts, "\n\n")
	return result
}

// PrependUserContext mirrors utils/api.ts by prepending a meta user reminder
// message before the real query messages. The reference runtime skips this in
// NODE_ENV=test to keep tests stable.
func PrependUserContext(messages []*schema.Message, context map[string]string) []*schema.Message {
	if os.Getenv("NODE_ENV") == "test" {
		return messages
	}

	rendered := renderUserContextReminder(context)
	if rendered == "" {
		return messages
	}

	out := make([]*schema.Message, 0, len(messages)+1)
	out = append(out, &schema.Message{
		Role:    schema.User,
		Content: rendered,
		Extra:   map[string]any{"is_meta": true},
	})
	out = append(out, messages...)
	return out
}

func renderContextBlock(title string, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := sortedContextKeys(values)
	if len(keys) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(title)
	b.WriteString(":\n")
	for i, k := range keys {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("- ")
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(values[k])
	}
	return b.String()
}

func buildGitStatus(parent context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		absCWD = cwd
	}

	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	if out, err := gitOutput(ctx, absCWD, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return ""
	}

	branch, _ := gitOutput(ctx, absCWD, "branch", "--show-current")
	defaultBranch, _ := gitOutput(ctx, absCWD, "symbolic-ref", "refs/remotes/origin/HEAD")
	if defaultBranch != "" {
		defaultBranch = strings.TrimPrefix(defaultBranch, "refs/remotes/origin/")
	}
	status, _ := gitOutput(ctx, absCWD, "--no-optional-locks", "status", "--short")
	log, _ := gitOutput(ctx, absCWD, "--no-optional-locks", "log", "--oneline", "-n", "5")
	userName, _ := gitOutput(ctx, absCWD, "config", "user.name")

	parts := []string{
		"This is the git status at the start of the conversation. It is a snapshot and will not update during the turn.",
	}
	if branch != "" {
		parts = append(parts, fmt.Sprintf("Current branch: %s", branch))
	}
	if defaultBranch != "" {
		parts = append(parts, fmt.Sprintf("Default branch: %s", defaultBranch))
	}
	if userName != "" {
		parts = append(parts, fmt.Sprintf("Git user: %s", userName))
	}
	if status != "" {
		parts = append(parts, fmt.Sprintf("Status:\n%s", status))
	} else {
		parts = append(parts, "Status:\n(clean)")
	}
	if log != "" {
		parts = append(parts, fmt.Sprintf("Recent commits:\n%s", log))
	}
	return strings.Join(parts, "\n\n")
}

func renderInlineContext(values map[string]string) string {
	keys := sortedContextKeys(values)
	if len(keys) == 0 {
		return ""
	}

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", key, values[key]))
	}
	return strings.Join(lines, "\n")
}

func renderUserContextReminder(values map[string]string) string {
	keys := sortedContextKeys(values)
	if len(keys) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString("As you answer the user's questions, you can use the following context:\n")
	for i, key := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("# ")
		b.WriteString(key)
		b.WriteByte('\n')
		b.WriteString(values[key])
	}
	b.WriteString("\n\nIMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n")
	b.WriteString("</system-reminder>\n")
	return b.String()
}

func sortedContextKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
