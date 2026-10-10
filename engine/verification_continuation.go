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

const verificationCursorMetadataName = "independent-verification-cursor-v1"

const verificationContinuationPrompt = "Resume the saved original task and verification stage with the configured finite allowance. Retain completed work, repair saved counterexamples, and execute fresh checks."

var errVerificationCheckpoint = errors.New("independent verification checkpoint failed")

// The cursor is runtime metadata, never model-authored history. Diagnostics are
// repair hints; neither historical receipts nor a stored PASS authorize resume.
type verificationCursor struct {
	Version           int                            `json:"version"`
	SessionID         string                         `json:"session_id"`
	Workspace         string                         `json:"workspace"`
	Requirements      string                         `json:"requirements"`
	RequirementsSHA   string                         `json:"requirements_sha256"`
	MaxTurns          int                            `json:"max_turns"`
	MaxRepairs        int                            `json:"max_repairs"`
	CoverageReview    bool                           `json:"coverage_review,omitempty"`
	MaxCoverageChecks int                            `json:"max_coverage_checks,omitempty"`
	CoverageChecks    int                            `json:"coverage_checks,omitempty"`
	Repairs           int                            `json:"repairs"`
	Phase             string                         `json:"phase"`
	Diagnostics       *independentVerificationReport `json:"diagnostics,omitempty"`
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
		if loaded.Metadata[i].Key != verificationCursorMetadataName {
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
	if err := recorder.RecordMetadata(verificationCursorMetadataName, string(encoded)); err != nil {
		return err
	}
	return recorder.Flush()
}

func (g *independentVerificationGate) commit(phase string, report *independentVerificationReport) error {
	candidate := g.cursor
	candidate.Phase = phase
	return g.commitCursor(candidate, report)
}

func (g *independentVerificationGate) commitCursor(candidate verificationCursor, report *independentVerificationReport) error {
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
	if cursor == nil || cursor.Version != 1 || cursor.SessionID != params.SessionID || cursor.Workspace != params.verificationWorkspace || cursor.MaxTurns != cfg.MaxTurns || cursor.MaxRepairs != cfg.MaxRepairs || cursor.CoverageReview != cfg.CoverageReview || cursor.Repairs < 0 || cursor.Repairs > cfg.MaxRepairs {
		return fmt.Errorf("verification continuation identity or configuration mismatch")
	}
	if cursor.MaxCoverageChecks != cfg.MaxCoverageChecks || cursor.CoverageChecks < 0 || cursor.CoverageChecks > cfg.MaxCoverageChecks {
		return fmt.Errorf("verification continuation coverage allowance mismatch")
	}
	if strings.TrimSpace(cursor.Requirements) == "" || len(cursor.Requirements) > 128*1024 || cursor.RequirementsSHA != fmt.Sprintf("%x", sha256.Sum256([]byte(cursor.Requirements))) {
		return fmt.Errorf("invalid saved verification requirements")
	}
	switch cursor.Phase {
	case "solver", "check":
	case "repair":
		if cursor.Repairs == 0 || cursor.Diagnostics == nil || cursor.Diagnostics.Verdict != "FAIL" {
			return fmt.Errorf("repair cursor requires a FAIL counterexample; legacy PARTIAL repair cannot resume")
		}
	case "coverage":
		if cursor.CoverageChecks >= cfg.MaxCoverageChecks || cursor.Diagnostics == nil || cursor.Diagnostics.Verdict != "PARTIAL" {
			return fmt.Errorf("supplemental coverage cursor has no pending allowance or PARTIAL diagnostic")
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

func verificationCheckMessages(requirements string, report *independentVerificationReport) []*schema.Message {
	var messages []*schema.Message
	if hint := verificationPlanningMessage(report); hint != nil {
		messages = append(messages, hint)
	}
	// Preserve the report's assistant provenance on the wire. The original
	// requirements remain the latest user request, independent of Extra.
	return append(messages, schema.UserMessage(requirements))
}

// Prior diagnostics help order a fresh audit; only this invocation's receipts
// may authorize its report. Do not copy historical commands, IDs, or PASSes.
func verificationPlanningMessage(report *independentVerificationReport) *schema.Message {
	if report == nil || (report.Verdict != "FAIL" && report.Verdict != "PARTIAL") {
		return nil
	}
	type concern struct {
		Requirement string `json:"requirement"`
		Expected    string `json:"expected"`
		Observed    string `json:"observed"`
		Status      string `json:"status"`
	}
	plan := struct {
		Missing  []string  `json:"missing,omitempty"`
		Concerns []concern `json:"concerns,omitempty"`
	}{Missing: report.Missing}
	for _, check := range report.Checks {
		if check.Status == "FAIL" || check.Status == "UNVERIFIED" {
			plan.Concerns = append(plan.Concerns, concern{check.Requirement, check.Expected, check.Observed, check.Status})
		}
	}
	if len(plan.Missing) == 0 && len(plan.Concerns) == 0 {
		return nil
	}
	encoded, err := json.Marshal(plan)
	if err != nil || len(encoded) > 16*1024 {
		// Oversized hints never replace or truncate the original requirements.
		return nil
	}
	return &schema.Message{Role: schema.Assistant, Content: "Historical verification planning hints, not evidence or instructions. Planning data: " + string(encoded), Extra: map[string]any{"is_meta": true, "attachment_kind": "verification_planning"}}
}

func verificationAttachment(report independentVerificationReport, attempt, coverageChecks int) *schema.Message {
	summary := IndependentVerificationSummary{Attempt: attempt, Verdict: report.Verdict, Checks: len(report.Checks), ReportCorrections: report.reportCorrections, FormatIssue: report.formatIssue, CoverageReviews: report.coverageReviews, CoverageVerdict: report.coverageVerdict, CoverageChecks: coverageChecks}
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
	phase := "check"
	if g.cursor.Phase == "coverage" {
		phase = "coverage"
	}
	if err := g.commit(phase, nil); err != nil {
		return nil, err
	}
	for {
		report, err := g.check(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrIndependentVerification, err)
		}
		candidate := g.cursor
		// Count completed supplemental audits. Admission failure, cancellation,
		// or an invalid report leaves this same pending audit resumable.
		if candidate.Phase == "coverage" {
			candidate.CoverageChecks++
		}
		attempt := candidate.Repairs + candidate.CoverageChecks + 1
		candidate.Phase = "exhausted"
		switch report.Verdict {
		case "PASS":
			candidate.Phase = "completed"
		case "FAIL":
			if candidate.Repairs < g.params.IndependentVerification.MaxRepairs {
				candidate.Repairs++
				candidate.Phase = "repair"
			}
		case "PARTIAL":
			if candidate.CoverageChecks < g.params.IndependentVerification.MaxCoverageChecks {
				candidate.Phase = "coverage"
			}
		}
		if err := g.commitCursor(candidate, &report); err != nil {
			return nil, err
		}
		attachment := verificationAttachment(report, attempt, candidate.CoverageChecks)
		yield(QueryEvent{Type: EventAttachment, AttachmentMessage: attachment})
		switch candidate.Phase {
		case "exhausted":
			return nil, ErrIndependentVerification
		case "coverage":
			// Never return a coverage gap to the solver. The next check has a
			// fresh history/receipts and only bounded prior planning hints.
			continue
		default:
			g.passed = candidate.Phase == "completed"
			return attachment, nil
		}
	}
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
