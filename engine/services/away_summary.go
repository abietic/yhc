package services

import (
	"context"
	"strings"
	"time"
)

const recentMessageWindow = 30

func buildAwaySummaryPrompt(memory string) string {
	var memoryBlock string
	if memory != "" {
		memoryBlock = "Session memory (broader context):\n" + memory + "\n\n"
	}
	return memoryBlock + "The user stepped away and is coming back. Write exactly 1-3 short sentences. Start by stating the high-level task — what they are building or debugging, not implementation details. Next: the concrete next step. Skip status reports and commit recaps."
}

// AwaySummaryModelFn calls a fast model for away summary generation.
type AwaySummaryModelFn func(ctx context.Context, messages []string, prompt string) (string, error)

// GenerateAwaySummary produces a short "while you were away" recap for
// the user returning to a session. Uses the last 30 messages as context.
// Returns empty string on error or empty transcript.
//
// Reference: src/services/awaySummary.ts (74 lines)
func GenerateAwaySummary(
	ctx context.Context,
	messages []string,
	sessionMemory string,
	modelFn AwaySummaryModelFn,
) string {
	if len(messages) == 0 || modelFn == nil {
		return ""
	}

	recent := messages
	if len(recent) > recentMessageWindow {
		recent = recent[len(recent)-recentMessageWindow:]
	}

	prompt := buildAwaySummaryPrompt(sessionMemory)

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := modelFn(callCtx, recent, prompt)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(result)
}
