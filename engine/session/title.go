package session

import (
	"context"
	"encoding/json"
	"strings"
)

const maxConversationText = 1000

const sessionTitlePrompt = `Generate a concise, sentence-case title (3-7 words) that captures the main topic or goal of this coding session. The title should be clear enough that the user recognizes the session in a list. Use sentence case: capitalize only the first word and proper nouns.

Return JSON with a single "title" field.

Good examples:
{"title": "Fix login button on mobile"}
{"title": "Add OAuth authentication"}
{"title": "Debug failing CI tests"}
{"title": "Refactor API client error handling"}

Bad (too vague): {"title": "Code changes"}
Bad (too long): {"title": "Investigate and fix the issue where the login button does not respond on mobile devices"}
Bad (wrong case): {"title": "Fix Login Button On Mobile"}`

// TitleModelFn calls a fast model (e.g., Haiku) to generate a title.
// Takes system prompt, user prompt, returns response text.
type TitleModelFn func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// ExtractConversationText flattens messages into a single text string for
// title generation. Tail-slices to the last 1000 chars so recent context wins.
//
// Reference: src/utils/sessionTitle.ts extractConversationText
func ExtractConversationText(messages []string) string {
	text := strings.Join(messages, "\n")
	if len(text) > maxConversationText {
		text = text[len(text)-maxConversationText:]
	}
	return text
}

// GenerateSessionTitle generates a sentence-case session title from conversation text.
// Returns empty string on error.
//
// Reference: src/utils/sessionTitle.ts generateSessionTitle (129 lines)
func GenerateSessionTitle(ctx context.Context, description string, modelFn TitleModelFn) string {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" || modelFn == nil {
		return ""
	}

	result, err := modelFn(ctx, sessionTitlePrompt, trimmed)
	if err != nil {
		return ""
	}

	var parsed struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return ""
	}

	return strings.TrimSpace(parsed.Title)
}
