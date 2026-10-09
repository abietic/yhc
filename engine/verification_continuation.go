package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const verificationCursorKey = "independent-verification-cursor-v1"

const verificationContinuationPrompt = "Resume the saved original task and verification stage with the configured finite allowance. Retain completed work, repair saved counterexamples, and execute fresh checks."

var errVerificationCheckpoint = errors.New("independent verification checkpoint failed")

// The cursor is runtime metadata, never model-authored history. Diagnostics are
// repair hints; neither historical receipts nor a stored PASS authorize resume.
type verificationCursor struct {
	Version         int                            `json:"version"`
	SessionID       string                         `json:"session_id"`
	Workspace       string                         `json:"workspace"`
	Requirements    string                         `json:"requirements"`
	RequirementsSHA string                         `json:"requirements_sha256"`
	MaxTurns        int                            `json:"max_turns"`
	MaxRepairs      int                            `json:"max_repairs"`
	Repairs         int                            `json:"repairs"`
	Phase           string                         `json:"phase"`
	Diagnostics     *independentVerificationReport `json:"diagnostics,omitempty"`
}

func (e *QueryEngine) loadVerificationCursor() (*verificationCursor, error) {
	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil {
		return nil, fmt.Errorf("verification continuation requires a transcript")
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		return nil, err
	}
	if len(loaded.Corruptions) > 0 {
		return nil, fmt.Errorf("verification continuation rejects a corrupt transcript")
	}
	for i := len(loaded.Metadata) - 1; i >= 0; i-- {
		if loaded.Metadata[i].Key != verificationCursorKey {
			continue
		}
		value := loaded.Metadata[i].Value
		if len(value) > 512*1024 {
			return nil, fmt.Errorf("verification cursor exceeds 512 KiB")
		}
		var cursor verificationCursor
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cursor); err != nil {
			return nil, err
		}
		// Metadata was encoded by the runtime as one JSON value.
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("verification cursor must contain exactly one JSON object")
		}
		return &cursor, nil
	}
	return nil, fmt.Errorf("no saved independent verification cursor")
}

func (e *QueryEngine) commitVerificationCursor(cursor verificationCursor) error {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	if len(encoded) > 512*1024 {
		return fmt.Errorf("verification cursor exceeds 512 KiB")
	}
	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil {
		return fmt.Errorf("verification requires a transcript")
	}
	if err := recorder.RecordMetadata(verificationCursorKey, string(encoded)); err != nil {
		return err
	}
	return recorder.Flush()
}

func (g *independentVerificationGate) commit(phase string, report *independentVerificationReport) error {
	candidate := g.cursor
	candidate.Phase = phase
	candidate.Repairs = g.repairs
	if report != nil {
		diagnostics := *report
		diagnostics.Evidence = nil // Invocation receipts are deliberately not restored.
		candidate.Diagnostics = &diagnostics
	}
	if g.params.commitVerificationCursor != nil {
		if err := g.params.commitVerificationCursor(candidate); err != nil {
			return fmt.Errorf("%w: %w", errVerificationCheckpoint, err)
		}
	}
	g.cursor = candidate
	return nil
}

func (cursor *verificationCursor) validate(params *QueryParams) error {
	cfg := params.IndependentVerification
	if cursor == nil || cursor.Version != 1 || cursor.SessionID != params.SessionID || cursor.Workspace != params.verificationWorkspace || cursor.MaxTurns != cfg.MaxTurns || cursor.MaxRepairs != cfg.MaxRepairs || cursor.Repairs < 0 || cursor.Repairs > cfg.MaxRepairs {
		return fmt.Errorf("verification continuation identity or configuration mismatch")
	}
	if strings.TrimSpace(cursor.Requirements) == "" || len(cursor.Requirements) > 128*1024 || cursor.RequirementsSHA != fmt.Sprintf("%x", sha256.Sum256([]byte(cursor.Requirements))) {
		return fmt.Errorf("invalid saved verification requirements")
	}
	switch cursor.Phase {
	case "solver", "check":
	case "repair":
		if cursor.Repairs == 0 || cursor.Diagnostics == nil {
			return fmt.Errorf("repair cursor has no repair diagnostic")
		}
	default:
		return fmt.Errorf("verification cursor is not resumable: %s", cursor.Phase)
	}
	if cursor.Diagnostics != nil {
		diagnostics := *cursor.Diagnostics
		diagnostics.Checks = append([]independentVerificationCheck(nil), diagnostics.Checks...)
		for i := range diagnostics.Checks {
			diagnostics.Checks[i].Command = ""
		}
		value, err := json.Marshal(diagnostics)
		if err != nil {
			return err
		}
		report, err := parseIndependentVerificationReport(string(value))
		if err != nil || report.Verdict == "PASS" {
			return fmt.Errorf("invalid historical verification diagnostics")
		}
	}
	return nil
}

func verificationAttachment(report independentVerificationReport, attempt int) *schema.Message {
	summary := IndependentVerificationSummary{Attempt: attempt, Verdict: report.Verdict, Checks: len(report.Checks)}
	for _, check := range report.Checks {
		if check.Status == "FAIL" {
			summary.Failed++
		}
		if check.Status == "UNVERIFIED" {
			summary.Unverified++
		}
	}
	encoded, _ := json.Marshal(report)
	return &schema.Message{Role: schema.User, Content: "<independent-verification>" + string(encoded) + "</independent-verification>", Extra: map[string]any{"is_meta": true, "attachment_kind": "independent_verification", "verdict": report.Verdict, "verification_summary": summary}}
}

func (g *independentVerificationGate) verify(ctx context.Context, yield func(QueryEvent)) (*schema.Message, error) {
	if err := g.commit("check", nil); err != nil {
		return nil, err
	}
	report, err := g.check(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndependentVerification, err)
	}
	attempt := g.repairs + 1
	phase := "completed"
	if report.Verdict != "PASS" {
		phase = "exhausted"
		if g.repairs < g.params.IndependentVerification.MaxRepairs {
			g.repairs++
			phase = "repair"
		}
	}
	if err := g.commit(phase, &report); err != nil {
		return nil, err
	}
	attachment := verificationAttachment(report, attempt)
	yield(QueryEvent{Type: EventAttachment, AttachmentMessage: attachment})
	if phase == "exhausted" {
		return nil, ErrIndependentVerification
	}
	g.passed = phase == "completed"
	return attachment, nil
}

func verificationErrorTerminal(err error) Terminal {
	reason := TerminalModelError
	if errors.Is(err, errVerificationCheckpoint) {
		reason = TerminalPersistenceError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		reason = TerminalAbortedStreaming
	}
	return Terminal{Reason: reason, Err: err}
}
