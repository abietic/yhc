package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestResumeInfoWithRecoveryProofUsesLoadedAdministrationState(t *testing.T) {
	root := t.TempDir()
	transcriptDir := root + "/transcripts"
	recorder := transcript.NewRecorder("proof-session", transcriptDir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "proof secret prompt"},
		{Role: schema.Assistant, Content: "proof answer", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}}},
	}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: "proof-session", ThreadID: "proof-thread", CWD: root,
		QueryKernelVersion: queryKernelVersionProjectGraph, QueryKernelStage: string(queryKernelStageFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	eng := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: transcriptDir})
	t.Cleanup(eng.Close)
	resumed, proof, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: "proof-session", CWD: root, TranscriptDir: transcriptDir})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != proof.SessionID || proof.ThreadID != "proof-thread" || proof.MessageCount != 2 || proof.NativeUsage.TotalTokens != 10 || !proof.ValidationComplete || proof.CanResumeAgent {
		t.Fatalf("proof = %#v", proof)
	}
	if _, err := os.Lstat(filepath.Join(transcriptDir, "proof-session.workspace-history")); !os.IsNotExist(err) {
		t.Fatalf("proof created an absent history journal: %v", err)
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || bytes.Contains(encoded, []byte("proof secret prompt")) || bytes.Contains(encoded, []byte("proof answer")) {
		t.Fatalf("proof leaked message content: %s", encoded)
	}
}

func TestResumeInfoWithRecoveryProofRejectsNonAdministrationEngine(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir()})
	t.Cleanup(eng.Close)
	_, _, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: "nope"})
	if err == nil || !strings.Contains(err.Error(), "administration") {
		t.Fatalf("error = %v", err)
	}
}

func TestResumeInfoWithRecoveryProofRejectsUnverifiedWorkspaceHistory(t *testing.T) {
	for _, scenario := range []string{"compatible", "replaced_root", "invalid_restore"} {
		t.Run(scenario, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, dir := filepath.Join(parent, "workspace"), filepath.Join(parent, "transcripts")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			const id = "history-proof"
			recorder := transcript.NewRecorder(id, dir)
			if err := recorder.RecordMessages([]*schema.Message{
				{Role: schema.User, Content: "owned recovery fixture"},
				{Role: schema.Assistant, Content: "completed", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}}},
			}); err != nil {
				t.Fatal(err)
			}
			if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{SessionID: id, ThreadID: id, CWD: root, QueryKernelVersion: queryKernelVersionProjectGraph, QueryKernelStage: string(queryKernelStageFull)}); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			historyDir := filepath.Join(dir, id+".workspace-history")
			journal, err := filehistory.OpenRevisionJournal(root, historyDir, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.RecordTurnStart("source-turn", id, time.Unix(1, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(historyDir, "revisions.jsonl")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			switch scenario {
			case "replaced_root":
				if err := os.Rename(root, root+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				want = "workspace_history_unavailable"
			case "invalid_restore":
				if err := os.WriteFile(filepath.Join(historyDir, "restore.json"), []byte("{invalid fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "workspace_restore_state_unverified"
			}
			eng := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
			t.Cleanup(eng.Close)
			_, proof, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: id, CWD: root, TranscriptDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if !proof.ValidationComplete {
					t.Fatalf("compatible observation history rejected: %+v", proof)
				}
			} else if proof.ValidationComplete || !slices.Contains(proof.BlockingReasons, want) || proof.CanResumeAgent {
				t.Fatalf("unverified history accepted: %+v", proof)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("proof changed the source journal: %v", err)
			}
		})
	}
}

func TestRecoveryProofExcludesCompletedWorkspaceRestore(t *testing.T) {
	eng, _, turn, calls, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	if blocker := eng.recoveryProofWorkspaceHistoryBlocker(); blocker != "" {
		t.Fatalf("compatible capture blocked: %s", blocker)
	}
	preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	status, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true)
	if err != nil || status.State != "completed" || status.Finished != 1 {
		t.Fatalf("restore status=%+v err=%v", status, err)
	}
	before := calls.Load()
	if blocker := eng.recoveryProofWorkspaceHistoryBlocker(); blocker != "workspace_restore_state_unverified" {
		t.Fatalf("completed restore accepted without a cold-state proof: %s", blocker)
	}
	if calls.Load() != before {
		t.Fatal("recovery inspection dispatched model work")
	}
}

func TestResumeInfoWithRecoveryProofRejectsInterruptedTurnWithKnownUsage(t *testing.T) {
	for _, stage := range []string{"new_prompt", "tool_results_without_final_response"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			dir := root + "/transcripts"
			messages := []*schema.Message{
				{Role: schema.User, Content: "earlier completed request"},
				{Role: schema.Assistant, Content: "earlier answer", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}}},
				{Role: schema.User, Content: "interrupted request"},
			}
			if stage == "tool_results_without_final_response" {
				messages = append(messages,
					&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "completed-effect", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{"file_path":"effect.txt","content":"once"}`}}}, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}},
					&schema.Message{Role: schema.Tool, ToolCallID: "completed-effect", Content: "effect completed"},
				)
			}
			if warnings := session.ValidateMessageHistory(messages); len(warnings) != 0 {
				t.Fatalf("valid but unfinished history rejected by ordinary validator: %v", warnings)
			}
			recorder := transcript.NewRecorder("interrupted", dir)
			if err := recorder.RecordMessages(messages); err != nil {
				t.Fatal(err)
			}
			if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{SessionID: "interrupted", ThreadID: "interrupted", CWD: root, QueryKernelVersion: queryKernelVersionProjectGraph, QueryKernelStage: string(queryKernelStageFull)}); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			eng := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
			t.Cleanup(eng.Close)
			resumed, proof, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: "interrupted", CWD: root, TranscriptDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if len(resumed.Warnings) != 0 || !proof.NativeUsage.CurrentContextUsageKnown || !proof.NativeUsage.LastResponseHadUsageMetadata {
				t.Fatalf("fixture has a different blocker: warnings=%v usage=%+v", resumed.Warnings, proof.NativeUsage)
			}
			if proof.ValidationComplete || !slices.Contains(proof.BlockingReasons, "native_turn_incomplete") || proof.CanResumeAgent {
				t.Fatalf("unfinished turn accepted as a complete cold boundary: %+v", proof)
			}
		})
	}
}

func TestRecoveryProofClosedTurnIgnoresMetadataButNotPendingWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []*schema.Message
		closed   bool
	}{
		{name: "empty"},
		{name: "final", messages: []*schema.Message{{Role: schema.User}, {Role: schema.Assistant}}, closed: true},
		{name: "trailing metadata", messages: []*schema.Message{{Role: schema.User}, {Role: schema.Assistant}, nil, {Role: schema.System}, {Role: schema.RoleType("developer")}, {Role: schema.User, Extra: map[string]any{"is_meta": true}}}, closed: true},
		{name: "pending tool", messages: []*schema.Message{{Role: schema.User}, {Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "pending"}}}}},
		{name: "diagnostic after user", messages: []*schema.Message{{Role: schema.User}, {Role: schema.Assistant, Extra: map[string]any{"api_error": true}}}},
		{name: "metadata after tool", messages: []*schema.Message{{Role: schema.Tool}, {Role: schema.Assistant, Extra: map[string]any{"is_meta": true}}}},
		{name: "user error marker", messages: []*schema.Message{{Role: schema.Assistant}, {Role: schema.User, Extra: map[string]any{"api_error": true}}}},
		{name: "unknown role", messages: []*schema.Message{{Role: schema.Assistant}, {Role: schema.RoleType("future")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoveryProofHasClosedTurn(tc.messages); got != tc.closed {
				t.Fatalf("closed=%v want=%v", got, tc.closed)
			}
		})
	}
}

func TestRecoveryProofBlockersRejectIncompleteUsage(t *testing.T) {
	resumed := &session.ResumedSession{Metadata: session.SessionMetadata{GoalState: &session.PersistedGoalState{}}}
	blockers := recoveryProofBlockers(resumed, []RuntimeItem{{}}, transcript.UsageSummary{Version: transcript.UsageSummaryVersion, UnsupportedSnapshotVersion: 99, ResponsesWithoutMetadata: 1, LegacyBoundariesWithoutUsage: 1})
	for _, want := range []string{"goal_state", "runtime_input_items", "native_usage_incomplete"} {
		if !slices.Contains(blockers, want) {
			t.Fatalf("blockers = %v, missing %q", blockers, want)
		}
	}
}

func TestRecoveryProofRejectsNegativeAndInconsistentNativeCounters(t *testing.T) {
	for _, usage := range []transcript.UsageSummary{
		{Version: 1, PromptTokens: -1},
		{Version: 1, PromptTokens: 7, CompletionTokens: 4, TotalTokens: 10},
		{Version: 1, LastPromptTokens: 1},
	} {
		if !slices.Contains(recoveryProofBlockers(&session.ResumedSession{}, nil, usage), "native_usage_invalid") {
			t.Fatalf("accepted invalid native counters: %#v", usage)
		}
	}
}

func TestResumeInfoWithRecoveryProofMarksUnsupportedUsageIncomplete(t *testing.T) {
	root := t.TempDir()
	transcriptDir := root + "/transcripts"
	recorder := transcript.NewRecorder("future-usage", transcriptDir)
	messages := []*schema.Message{{Role: schema.User, Content: "question"}, {Role: schema.Assistant, Content: "answer"}}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: "future-usage", ThreadID: "future-usage", CWD: root,
		QueryKernelVersion: queryKernelVersionProjectGraph, QueryKernelStage: string(queryKernelStageFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(struct {
		Kind     string                   `json:"kind"`
		Messages []*schema.Message        `json:"messages"`
		Usage    *transcript.UsageSummary `json:"usage"`
	}{Kind: string(transcript.LifecycleCompact), Messages: messages, Usage: &transcript.UsageSummary{Version: 99}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(recorder.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	eng := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: transcriptDir})
	t.Cleanup(eng.Close)
	_, proof, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: "future-usage", CWD: root, TranscriptDir: transcriptDir})
	if err != nil {
		t.Fatal(err)
	}
	if proof.ValidationComplete || proof.NativeUsage.UnsupportedSnapshotVersion != 99 || !slices.Contains(proof.BlockingReasons, "native_usage_incomplete") {
		t.Fatalf("proof = %#v", proof)
	}
}

func TestResumeInfoWithRecoveryProofRejectsUnknownContextOrLastResponseUsage(t *testing.T) {
	for _, unknown := range []string{"context", "last_response"} {
		t.Run(unknown, func(t *testing.T) {
			root := t.TempDir()
			dir := root + "/transcripts"
			recorder := transcript.NewRecorder("unknown-usage", dir)
			messages := []*schema.Message{{Role: schema.User, Content: "owned fixture"}, {
				Role: schema.Assistant, Content: "answer", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}},
			}}
			if err := recorder.RecordMessages(messages); err != nil {
				t.Fatal(err)
			}
			if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{SessionID: "unknown-usage", ThreadID: "unknown-usage", CWD: root, QueryKernelVersion: queryKernelVersionProjectGraph, QueryKernelStage: string(queryKernelStageFull)}); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			usage := transcript.UsageSummary{}
			usage.ObserveMessage(messages[1])
			if unknown == "context" {
				usage.CurrentContextUsageKnown = false
			} else {
				usage.LastResponseHadUsageMetadata = false
			}
			line, err := json.Marshal(struct {
				Kind     string                  `json:"kind"`
				Messages []*schema.Message       `json:"messages"`
				Usage    transcript.UsageSummary `json:"usage"`
			}{string(transcript.LifecycleCompact), messages, usage})
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(recorder.Path(), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.Write(append(line, '\n'))
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("fixture append: %v %v", writeErr, closeErr)
			}
			eng := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
			t.Cleanup(eng.Close)
			_, proof, err := eng.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: "unknown-usage", CWD: root, TranscriptDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if proof.ValidationComplete || !slices.Contains(proof.BlockingReasons, "native_usage_incomplete") {
				t.Fatalf("unknown usage incorrectly accepted: %#v", proof)
			}
		})
	}
}
