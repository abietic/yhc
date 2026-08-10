package services

import (
	"context"
	"testing"
)

func TestFilterSuggestionAcceptsGood(t *testing.T) {
	good := []string{
		"run the tests",
		"commit this",
		"push it",
		"try it out",
		"/compact",
		"yes",
		"no",
		"fix the linter errors",
	}
	for _, s := range good {
		if reason := FilterSuggestion(s); reason != "" {
			t.Errorf("FilterSuggestion(%q) = %q, want empty", s, reason)
		}
	}
}

func TestFilterSuggestionRejectsBad(t *testing.T) {
	cases := []struct {
		input  string
		reason string
	}{
		{"", "empty"},
		{"done", "done"},
		{"nothing found", "meta_text"},
		{"no suggestion needed", "meta_text"},
		{"(silence — the user needs to think)", "meta_wrapped"},
		{"[no suggestion]", "meta_wrapped"},
		{"api error: something broke", "error_message"},
		{"Note: this is important", "prefixed_label"},
		{"hmm", "too_few_words"},
		{"This is a very long suggestion that has way too many words for the user to reasonably type in one go", "too_many_words"},
		{"This is one sentence. And this is another sentence.", "multiple_sentences"},
		{"looks good", "evaluative"},
		{"thanks for that help", "evaluative"},
		{"Let me check that for you", "claude_voice"},
		{"I'll handle this now please", "claude_voice"},
	}
	for _, tc := range cases {
		reason := FilterSuggestion(tc.input)
		if reason != tc.reason {
			t.Errorf("FilterSuggestion(%q) = %q, want %q", tc.input, reason, tc.reason)
		}
	}
}

func TestSuggestionSuppressReason(t *testing.T) {
	if r := SuggestionSuppressReason(false, false, false); r != "disabled" {
		t.Errorf("disabled case: got %q", r)
	}
	if r := SuggestionSuppressReason(true, false, true); r != "pending_permission" {
		t.Errorf("pending permission case: got %q", r)
	}
	if r := SuggestionSuppressReason(true, true, false); r != "plan_mode" {
		t.Errorf("plan mode case: got %q", r)
	}
	if r := SuggestionSuppressReason(true, false, false); r != "" {
		t.Errorf("allowed case: got %q", r)
	}
}

func TestPromptSuggestionServiceGenerate(t *testing.T) {
	modelFn := func(ctx context.Context, conv []string) (string, error) {
		return "run the tests", nil
	}
	svc := NewPromptSuggestionService(modelFn)
	result := svc.GenerateSuggestion(context.Background(), []string{"fix the bug"}, 3)
	if result != "run the tests" {
		t.Errorf("got %q, want 'run the tests'", result)
	}
}

func TestPromptSuggestionServiceFiltersResult(t *testing.T) {
	modelFn := func(ctx context.Context, conv []string) (string, error) {
		return "thanks", nil
	}
	svc := NewPromptSuggestionService(modelFn)
	result := svc.GenerateSuggestion(context.Background(), []string{"done"}, 3)
	if result != "" {
		t.Errorf("expected filtered (empty), got %q", result)
	}
}

func TestPromptSuggestionServiceMinTurns(t *testing.T) {
	modelFn := func(ctx context.Context, conv []string) (string, error) {
		return "commit this", nil
	}
	svc := NewPromptSuggestionService(modelFn)
	result := svc.GenerateSuggestion(context.Background(), []string{"hello"}, 1)
	if result != "" {
		t.Errorf("expected empty for early conversation, got %q", result)
	}
}

func TestGetSuggestionPrompt(t *testing.T) {
	prompt := GetSuggestionPrompt()
	if !contains(prompt, "SUGGESTION MODE") {
		t.Error("prompt should contain SUGGESTION MODE header")
	}
	if !contains(prompt, "NEVER SUGGEST") {
		t.Error("prompt should contain NEVER SUGGEST section")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
