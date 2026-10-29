package engine

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestDeterministicAutoCompactRestartPreservesRequests(t *testing.T) {
	const sessionID = "deterministic-requirement-restart"
	const requirement = "Complete all requirements exactly:\n    preserve four spaces\n    keep the public interface\n"
	const correction = "Also retain the existing output format."
	const firstPrompt = "Continue the implementation."
	const resumedPrompt = "Continue after restart."
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "1")
	root, transcriptDir := t.TempDir(), t.TempDir()
	firstModel := &autoCompactRestartCaptureModel{}
	first := NewQueryEngine(QueryEngineConfig{
		SessionID: sessionID, ThreadID: sessionID, CWD: root,
		TranscriptDir: transcriptDir, ChatModel: firstModel,
	})
	t.Cleanup(first.Close)
	first.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: requirement},
		{Role: schema.Assistant, Content: strings.Repeat("discardable investigation detail ", 600)},
		{Role: schema.User, Content: correction},
		{Role: schema.Assistant, Content: "recent action"},
		{Role: schema.Assistant, Content: "recent observation"},
	})
	events, admission := first.SubmitMessage(t.Context(), firstPrompt)
	if admission.Err != nil {
		t.Fatal(admission.Err)
	}
	assertAutoCompactRestartTerminal(t, events)
	loaded, err := first.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	boundaryFound := false
	for _, message := range loaded.Messages {
		boundaryFound = boundaryFound || message.Extra["subtype"] == "compact_boundary"
	}
	if !boundaryFound {
		t.Fatal("the fixture did not trigger a durable compaction")
	}
	assertExactCompactedRequests(t, loaded.Messages, requirement, correction, firstPrompt)
	firstInputs := firstModel.inputsSnapshot()
	if len(firstInputs) != 1 {
		t.Fatalf("first provider calls = %d", len(firstInputs))
	}
	assertExactCompactedRequests(t, firstInputs[0], requirement, correction, firstPrompt)
	first.Close()

	resumedModel := &autoCompactRestartCaptureModel{}
	resumed := NewQueryEngine(QueryEngineConfig{
		SessionID: sessionID, ThreadID: sessionID, CWD: root,
		TranscriptDir: transcriptDir, ChatModel: resumedModel,
	})
	t.Cleanup(resumed.Close)
	if _, err := resumed.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatal(err)
	}
	events, admission = resumed.SubmitMessage(t.Context(), resumedPrompt)
	if admission.Err != nil {
		t.Fatal(admission.Err)
	}
	assertAutoCompactRestartTerminal(t, events)
	inputs := resumedModel.inputsSnapshot()
	if len(inputs) != 1 {
		t.Fatalf("resumed provider calls = %d", len(inputs))
	}
	assertExactCompactedRequests(t, inputs[0], requirement, correction, firstPrompt, resumedPrompt)
}

func assertExactCompactedRequests(t *testing.T, messages []*schema.Message, requests ...string) {
	t.Helper()
	var text strings.Builder
	for _, message := range messages {
		if message == nil || message.Role != schema.User {
			continue
		}
		if message.Content != "" {
			text.WriteString(message.Content)
		} else {
			for _, part := range message.UserInputMultiContent {
				text.WriteString(part.Text)
			}
		}
		text.WriteByte('\n')
	}
	content, position := text.String(), -1
	for index, request := range requests {
		// Compatibility projection may quote a short request in the summary.
		// The later retained copy must still be complete and correctly ordered.
		next := strings.LastIndex(content, request)
		if next <= position {
			t.Fatalf("user request %d was lost, changed, or reordered", index)
		}
		position = next
	}
}
