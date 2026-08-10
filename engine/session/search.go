package session

import (
	"strings"
)

// TranscriptSearchResult represents a match found in the transcript.
type TranscriptSearchResult struct {
	MessageIndex int
	Content      string
	Role         string
}

// SearchTranscript searches through message content for a query string.
// Returns matching messages with their indices. Case-insensitive.
//
// Reference: src/utils/transcriptSearch.ts (202 lines)
// Go port focuses on engine-side search; UI highlighting is in the TUI layer.
func SearchTranscript(messages []TranscriptMessage, query string) []TranscriptSearchResult {
	if query == "" {
		return nil
	}

	lowerQuery := strings.ToLower(query)
	var results []TranscriptSearchResult

	for i, msg := range messages {
		text := strings.ToLower(msg.Content)
		if strings.Contains(text, lowerQuery) {
			results = append(results, TranscriptSearchResult{
				MessageIndex: i,
				Content:      msg.Content,
				Role:         msg.Role,
			})
		}
	}

	return results
}

// TranscriptMessage is a simplified message for search purposes.
type TranscriptMessage struct {
	Role    string
	Content string
}
