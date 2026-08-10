package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const maxCopyLookback = 20

func resolveCopyAvailability(_ context.Context, ctx *CommandContext) (AvailabilityState, string) {
	if ctx == nil || ctx.Extra == nil {
		return AvailabilityUnavailable, "interactive clipboard capability was not provided"
	}
	available, _ := ctx.Extra["terminal_clipboard"].(bool)
	if !available {
		return AvailabilityUnavailable, "the active terminal cannot safely receive clipboard output"
	}
	return AvailabilitySupported, ""
}

func executeCopy(ctx *CommandContext, args string) (*CommandResult, error) {
	idx := 1
	if args != "" {
		n, err := strconv.Atoi(strings.TrimSpace(args))
		if err != nil || n < 1 {
			return &CommandResult{Output: "Usage: /copy [N] — where N is 1 (latest), 2 (second latest), etc."}, nil
		}
		idx = n
	}

	text := nthAssistantText(ctx.Messages, idx)
	if text == "" {
		if idx == 1 {
			return &CommandResult{Output: "No committed assistant response to copy."}, nil
		}
		return &CommandResult{Output: fmt.Sprintf("Only %d committed assistant response(s) available.", countAssistantTexts(ctx.Messages))}, nil
	}

	return &CommandResult{
		Action: ActionCopy,
		Data: map[string]any{
			"text":  text,
			"chars": len(text),
			"lines": strings.Count(text, "\n") + 1,
		},
	}, nil
}

// nthAssistantText selects only committed message snapshots. Entrypoints must
// not dispatch /copy while a streaming response is still in progress.
func nthAssistantText(messages []*schema.Message, n int) string {
	count := 0
	for i := len(messages) - 1; i >= 0 && count < maxCopyLookback; i-- {
		msg := messages[i]
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		count++
		if count == n {
			return text
		}
	}
	return ""
}

func countAssistantTexts(messages []*schema.Message) int {
	count := 0
	for i := len(messages) - 1; i >= 0 && count < maxCopyLookback; i-- {
		if messages[i] != nil && messages[i].Role == schema.Assistant &&
			strings.TrimSpace(messages[i].Content) != "" {
			count++
		}
	}
	return count
}
