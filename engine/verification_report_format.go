package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/cloudwego/eino/schema"
)

// Keep diagnostics categorical: decoder errors may contain model-supplied field
// names or values. Neither those errors nor the raw report leave the checker.
type verificationReportFormatError struct {
	category    string
	description string
}

func (e *verificationReportFormatError) Error() string {
	return e.description + " (" + e.category + ")"
}

func verificationJSONFormatError(err error) error {
	category := "json_schema"
	var syntax *json.SyntaxError
	var valueType *json.UnmarshalTypeError
	if errors.As(err, &syntax) {
		category = "json_syntax"
	}
	if errors.As(err, &valueType) {
		category = "json_type"
	}
	return &verificationReportFormatError{category, "verification report does not match required JSON schema"}
}

// Only a completely decoded report can be corrected without inventing claims
// lost to a JSON syntax/type/schema error. There is no enum alias coercion.
func (e *verificationReportFormatError) correctable() bool {
	return e.category == "verdict_enum" || e.category == "check_fields" ||
		e.category == "check_status_enum" || e.category == "check_status_missing"
}

// Semantic failures take precedence over formatting errors. In particular an
// invalid status cannot erase a known contradiction or supply a FAIL witness.
func verificationReportSemantics(report independentVerificationReport) error {
	if report.Evidence != nil {
		return fmt.Errorf("tool evidence is runtime-owned")
	}
	if len(report.Checks) > 128 || len(report.Missing) > 128 {
		return fmt.Errorf("too many verification checks")
	}
	if report.Verdict == "PASS" && (!report.CoverageComplete || len(report.Checks) == 0 || len(report.Missing) != 0) {
		return fmt.Errorf("PASS requires complete executable coverage")
	}
	failed := false
	for _, check := range report.Checks {
		failed = failed || check.Status == "FAIL"
		if report.Verdict == "PASS" && (check.Status == "FAIL" || check.Status == "UNVERIFIED") {
			return fmt.Errorf("PASS contradicts a check")
		}
	}
	if report.Verdict == "FAIL" && !failed {
		return fmt.Errorf("FAIL requires a counterexample")
	}
	return nil
}

func verificationCorrectionPreservesReport(before, after independentVerificationReport) error {
	if ((before.Verdict == "PASS" || before.Verdict == "FAIL" || before.Verdict == "PARTIAL") && before.Verdict != after.Verdict) ||
		before.CoverageComplete != after.CoverageComplete || !slices.Equal(before.Missing, after.Missing) || len(before.Checks) != len(after.Checks) {
		return fmt.Errorf("report correction changed existing verification claims")
	}
	for i, check := range before.Checks {
		corrected := after.Checks[i]
		if (check.Status == "PASS" || check.Status == "FAIL" || check.Status == "UNVERIFIED") && check.Status != corrected.Status {
			return fmt.Errorf("report correction changed an existing check verdict")
		}
		for _, pair := range [][2]string{{check.Requirement, corrected.Requirement}, {check.ToolCallID, corrected.ToolCallID}, {check.Command, corrected.Command}, {check.Expected, corrected.Expected}, {check.Observed, corrected.Observed}} {
			if pair[0] != "" && pair[0] != pair[1] {
				return fmt.Errorf("report correction changed existing check evidence")
			}
		}
	}
	return nil
}

// Reject supplied hard evidence errors before offering a format-only correction.
// The report is already completely decoded; incomplete JSON is never retried.
func verificationCorrectionReferences(report independentVerificationReport, commands map[string]string, results map[string]*schema.Message) error {
	for _, check := range report.Checks {
		if check.Status == "UNVERIFIED" {
			continue
		}
		if check.ToolCallID == "" {
			return fmt.Errorf("executable check missing tool identity")
		}
		command, exists := commands[check.ToolCallID]
		result := results[check.ToolCallID]
		if !exists || result == nil || result.ToolName != "Bash" {
			return fmt.Errorf("check references an unknown or incomplete Bash receipt")
		}
		if isError, _ := result.Extra["is_error"].(bool); isError {
			return fmt.Errorf("check references a failed tool invocation")
		}
		if check.Command != "" && check.Command != command {
			return fmt.Errorf("check command does not match its Bash receipt")
		}
	}
	return nil
}

// Bounded runtime evidence replaces a replay of the potentially large inspection
// history. The maps remain local to one g.check; no solver/old receipt is loaded.
func verificationCorrectionEvidence(commands map[string]string, results map[string]*schema.Message) string {
	type receipt struct {
		ID      string `json:"tool_call_id"`
		Preview string `json:"command_preview"`
		Output  string `json:"output"`
	}
	ids := make([]string, 0, len(commands))
	for id := range commands {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]receipt, 0, len(ids))
	encoded := []byte("[]")
	for _, id := range ids {
		result := results[id]
		if result == nil || result.ToolName != "Bash" {
			continue
		}
		if isError, _ := result.Extra["is_error"].(bool); isError {
			continue
		}
		preview := []rune(commands[id])
		if len(preview) > 256 {
			preview = append(preview[:230], []rune(" [preview truncated]")...)
		}
		output := result.Content
		if len(output) > 16384 {
			output = output[:16384] + " [truncated]"
		}
		items = append(items, receipt{id, string(preview), output})
		value, _ := json.Marshal(items)
		if len(value) > 64*1024 || len(items) > 128 {
			break
		}
		encoded = value
	}
	return string(encoded)
}
