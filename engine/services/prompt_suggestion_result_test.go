package services

import (
	"context"
	"errors"
	"testing"
)

func TestPromptSuggestionResultClassifiesOutcomes(t *testing.T) {
	for _, test := range []struct {
		name   string
		text   string
		err    error
		turns  int
		status PromptSuggestionStatus
		reason string
	}{
		{"available", "继续运行测试", nil, 2, PromptSuggestionAvailable, ""},
		{"early", "", nil, 1, PromptSuggestionSuppressed, "early_conversation"},
		{"empty", "  ", nil, 2, PromptSuggestionEmpty, ""},
		{"filtered", "done", nil, 2, PromptSuggestionFiltered, "done"},
		{"invalid text", "run\x1b tests", nil, 2, PromptSuggestionFiltered, "invalid_text"},
		{"provider", "", errors.New("private provider detail"), 2, PromptSuggestionFailed, "provider_error"},
		{"timeout", "", context.DeadlineExceeded, 2, PromptSuggestionTimedOut, ""},
		{"cancelled", "", context.Canceled, 2, PromptSuggestionCancelled, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			svc := NewPromptSuggestionService(func(context.Context, []string) (string, error) {
				calls++
				return test.text, test.err
			})
			result, err := svc.GenerateSuggestionResult(context.Background(), []string{"conversation"}, test.turns)
			if result.Status != test.status || result.Reason != test.reason {
				t.Fatalf("result = %#v", result)
			}
			if (err != nil) != (test.err != nil) {
				t.Fatalf("error = %v", err)
			}
			if test.status != PromptSuggestionAvailable && result.Suggestion != "" {
				t.Fatal("unsuccessful result leaked candidate")
			}
			if test.turns < 2 && calls != 0 {
				t.Fatal("suppressed request reached provider")
			}
		})
	}
}

func TestPromptSuggestionResultRetainsSingleFlightUntilProviderReturns(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	svc := NewPromptSuggestionService(func(context.Context, []string) (string, error) {
		close(entered)
		<-release
		return "run the tests", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan PromptSuggestionResult, 1)
	go func() { value, _ := svc.GenerateSuggestionResult(ctx, []string{"conversation"}, 2); result <- value }()
	<-entered
	cancel()
	duplicate, err := svc.GenerateSuggestionResult(context.Background(), []string{"conversation"}, 2)
	if err != nil || duplicate.Status != PromptSuggestionSuppressed || duplicate.Reason != "in_flight" {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
	close(release)
	if got := <-result; got.Status != PromptSuggestionCancelled || got.Suggestion != "" {
		t.Fatalf("late result = %#v", got)
	}
}
