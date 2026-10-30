package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionsResumeRecoveryProofIsOptInAndRedacted(t *testing.T) {
	_, transcriptDir := prepareSessionCLIProject(t)
	writeSessionCLITranscript(t, transcriptDir, "proof-session", "do not expose this prompt")
	stdout, stderr, err := executeSessionCLI(context.Background(), t, "sessions", "resume", "proof-session", "--output-format", "json")
	if err != nil || stderr != "" || strings.Contains(stdout, "recovery_proof") {
		t.Fatalf("default resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = executeSessionCLI(context.Background(), t, "sessions", "resume", "proof-session", "--recovery-proof", "--output-format", "json")
	if err != nil || stderr != "" || strings.Contains(stdout, "do not expose this prompt") {
		t.Fatalf("proof resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope struct {
		Result struct {
			RecoveryProof *struct {
				Scope          string `json:"scope"`
				SessionID      string `json:"session_id"`
				MessageCount   int    `json:"message_count"`
				MessagesSHA256 string `json:"messages_sha256"`
				CanResumeAgent bool   `json:"can_resume_agent"`
			} `json:"recovery_proof"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	proof := envelope.Result.RecoveryProof
	if proof == nil || proof.Scope != "canonical_administration_restore" || proof.SessionID != "proof-session" || proof.MessageCount != 2 || proof.MessagesSHA256 == "" || proof.CanResumeAgent {
		t.Fatalf("proof = %#v", proof)
	}
}
