package services

import (
	"context"
	"errors"
)

// PromptSuggestionStatus classifies an ephemeral suggestion without provider
// error text, conversation content, or an inferred successful generation.
type PromptSuggestionStatus string

const (
	PromptSuggestionAvailable  PromptSuggestionStatus = "available"
	PromptSuggestionSuppressed PromptSuggestionStatus = "suppressed"
	PromptSuggestionEmpty      PromptSuggestionStatus = "empty"
	PromptSuggestionFiltered   PromptSuggestionStatus = "filtered"
	PromptSuggestionTimedOut   PromptSuggestionStatus = "timed_out"
	PromptSuggestionCancelled  PromptSuggestionStatus = "cancelled"
	PromptSuggestionFailed     PromptSuggestionStatus = "failed"
	PromptSuggestionStale      PromptSuggestionStatus = "stale"
)

// PromptSuggestionResult is content-free unless Status is available. Reason is
// a bounded project-owned code, never a provider error or rejected model output.
type PromptSuggestionResult struct {
	Suggestion string                 `json:"suggestion"`
	Status     PromptSuggestionStatus `json:"status"`
	Reason     string                 `json:"reason,omitempty"`
}

// PromptSuggestionErrorResult exposes only a finite error category to clients.
func PromptSuggestionErrorResult(err error) PromptSuggestionResult {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return PromptSuggestionResult{Status: PromptSuggestionTimedOut}
	case errors.Is(err, context.Canceled):
		return PromptSuggestionResult{Status: PromptSuggestionCancelled}
	default:
		return PromptSuggestionResult{Status: PromptSuggestionFailed, Reason: "provider_error"}
	}
}
