package engine

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestSubmitMetadataDoesNotReplaceUserTask(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "legacy-checker-metadata"}[legacy], func(t *testing.T) {
			mdl := &canonicalScriptModel{}
			eng := NewQueryEngine(QueryEngineConfig{
				SessionID: "metadata-task", CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: mdl,
			})
			t.Cleanup(eng.Close)
			const task = "Implement the requested change and run its relevant tests."
			events, _ := eng.SubmitMessageWithMetadata(t.Context(), task, map[string]any{
				"verification_continuation": legacy, "command_uuid": "task-command",
			})
			var terminal *Terminal
			for event := range events {
				if event.TerminalInfo != nil {
					terminal = event.TerminalInfo
				}
			}
			if terminal == nil || terminal.Reason != TerminalCompleted || terminal.Err != nil || mdl.callCount != 1 {
				t.Fatalf("normal completion = %+v, model calls = %d", terminal, mdl.callCount)
			}
			var user *schema.Message
			for _, message := range eng.GetMessages() {
				if message.Role == schema.User {
					user = message
				}
			}
			if user == nil || user.Content != task || user.Extra["command_uuid"] != "task-command" {
				t.Fatalf("metadata changed the user task: %+v", user)
			}
		})
	}
}

func TestLegacyCheckerCursorIsInertOnOrdinaryResume(t *testing.T) {
	mdl := &canonicalScriptModel{}
	cfg := QueryEngineConfig{
		SessionID: "legacy-cursor", CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: mdl,
	}
	eng := NewQueryEngine(cfg)
	events, _ := eng.SubmitMessage(t.Context(), "Complete the original task.")
	for range events {
	}
	if err := eng.transcript.RecordMetadata("independent-verification-cursor-v1", "retired-format"); err != nil {
		t.Fatal(err)
	}
	eng.Close()

	resumed := NewQueryEngine(cfg)
	t.Cleanup(resumed.Close)
	if len(resumed.GetMessages()) != 2 {
		t.Fatal("ordinary resume lost existing solver history")
	}
	events, _ = resumed.SubmitMessage(t.Context(), "Continue the original task from saved progress.")
	var terminal *Terminal
	for event := range events {
		if event.TerminalInfo != nil {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil || terminal.Reason != TerminalCompleted || terminal.Err != nil || mdl.callCount != 2 {
		t.Fatalf("historical checker metadata affected ordinary resume: terminal=%+v calls=%d", terminal, mdl.callCount)
	}
}
