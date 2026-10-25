package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

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
