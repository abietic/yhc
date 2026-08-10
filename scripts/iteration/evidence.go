package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

type GateEvidence struct {
	Target           string     `json:"target"`
	Level            string     `json:"level"`
	Status           GateStatus `json:"status"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	DurationMillis   int64      `json:"duration_ms"`
	FailureLogPath   string     `json:"failure_log_path,omitempty"`
	FirstFailingSeed string     `json:"first_failing_seed,omitempty"`
}

type Evidence struct {
	SchemaVersion int            `json:"schema_version"`
	Plan          Plan           `json:"plan"`
	State         string         `json:"state"`
	Gates         []GateEvidence `json:"gates"`
}

func initialEvidence(plan Plan) Evidence {
	notApplicable := make(map[string]struct{}, len(plan.NotApplicable))
	addStrings(notApplicable, plan.NotApplicable...)
	var gates []GateEvidence
	if len(plan.RequiredTargets) > 0 {
		targets := append([]string(nil), plan.RequiredTargets...)
		slices.Sort(targets)
		gates = make([]GateEvidence, 0, len(targets))
		for _, target := range targets {
			status := GateBlocked
			if _, ok := notApplicable[target]; ok {
				status = GateNotApplicable
			}
			gates = append(gates, GateEvidence{
				Target: target,
				Level:  "merge",
				Status: status,
			})
		}
	}
	return Evidence{
		SchemaVersion: 1,
		Plan:          plan,
		State:         stateForPlan(plan),
		Gates:         gates,
	}
}

func stateForPlan(plan Plan) string {
	if len(plan.Changed) == 0 {
		return "planned"
	}
	return "changed"
}

func decodeEvidence(reader io.Reader) (Evidence, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var evidence Evidence
	if err := decoder.Decode(&evidence); err != nil {
		return Evidence{}, fmt.Errorf("decode evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return Evidence{}, fmt.Errorf("decode trailing evidence: %w", err)
		}
		return Evidence{}, errors.New("decode evidence: multiple JSON values are not allowed")
	}
	if err := validateEvidence(evidence); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func validateEvidence(evidence Evidence) error {
	if evidence.SchemaVersion != 1 || evidence.Plan.SchemaVersion != 1 {
		return errors.New("evidence schema_version must be 1")
	}
	if !oneOf(evidence.State, "planned", "changed", "focused_verified", "merge_verified", "evidence_ready") {
		return fmt.Errorf("invalid evidence state %q", evidence.State)
	}
	seen := make(map[string]struct{}, len(evidence.Gates))
	for _, gate := range evidence.Gates {
		if strings.TrimSpace(gate.Target) == "" {
			return errors.New("evidence gate target must not be empty")
		}
		key := gate.Level + "\x00" + gate.Target
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate evidence target %q at level %q", gate.Target, gate.Level)
		}
		seen[key] = struct{}{}
		if !oneOf(gate.Level, "focused", "merge", "deep", "remote", "live") {
			return fmt.Errorf("invalid evidence level %q", gate.Level)
		}
		if !oneOf(
			string(gate.Status),
			string(GatePass),
			string(GateFail),
			string(GateBlocked),
			string(GateNotApplicable),
		) {
			return fmt.Errorf("invalid evidence status %q", gate.Status)
		}
		if gate.DurationMillis < 0 {
			return fmt.Errorf("negative duration for evidence target %q", gate.Target)
		}
	}
	return nil
}

func renderJSON(value any, writer io.Writer) error {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("render JSON: %w", err)
	}
	if _, err := writer.Write(output.Bytes()); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

func renderPlanMarkdown(plan Plan, writer io.Writer) error {
	return renderMarkdown("Change Plan", initialEvidence(plan), writer)
}

func renderEvidenceMarkdown(evidence Evidence, writer io.Writer) error {
	if err := validateEvidence(evidence); err != nil {
		return err
	}
	return renderMarkdown("Change Evidence", evidence, writer)
}

func renderMarkdown(title string, evidence Evidence, writer io.Writer) error {
	plan := evidence.Plan
	var output strings.Builder
	fmt.Fprintf(&output, "# %s\n\n", title)
	fmt.Fprintf(&output, "- Base: `%s`\n", plan.Base)
	fmt.Fprintf(&output, "- Head: `%s`\n", plan.Head)
	fmt.Fprintf(&output, "- Diff digest: `%s`\n", plan.DiffDigest)
	fmt.Fprintf(&output, "- State: `%s`\n", evidence.State)
	fmt.Fprintf(&output, "- Outside-scope untracked count: `%d`\n", plan.OutsideUntracked)
	if plan.Slice != nil {
		fmt.Fprintf(
			&output,
			"- Accepted slice: `%s` (`%s`; contract `%s`)\n",
			escapeMarkdownCell(plan.Slice.ID),
			escapeMarkdownCell(plan.Slice.State),
			escapeMarkdownCell(plan.Slice.Contract),
		)
	}

	output.WriteString("\n## Changed owners\n\n")
	output.WriteString("| Path | Status | Owner | Kind |\n")
	output.WriteString("|---|---|---|---|\n")
	if len(plan.Changed) == 0 {
		output.WriteString("| _None_ |  |  |  |\n")
	} else {
		for _, change := range plan.Changed {
			fmt.Fprintf(
				&output,
				"| %s | %s | %s | %s |\n",
				escapeMarkdownCell(change.Path),
				escapeMarkdownCell(change.Status),
				escapeMarkdownCell(change.Owner),
				escapeMarkdownCell(string(change.Kind)),
			)
		}
	}

	output.WriteString("\n## Required checks\n\n")
	output.WriteString("| Target | Status |\n")
	output.WriteString("|---|---|\n")
	if len(evidence.Gates) == 0 {
		output.WriteString("| _None_ |  |\n")
	} else {
		for _, gate := range evidence.Gates {
			fmt.Fprintf(
				&output,
				"| %s | %s |\n",
				escapeMarkdownCell(gate.Target),
				escapeMarkdownCell(string(gate.Status)),
			)
		}
	}
	if _, err := io.WriteString(writer, output.String()); err != nil {
		return fmt.Errorf("write Markdown: %w", err)
	}
	return nil
}

func escapeMarkdownCell(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "\r\n", "<br>")
	value = strings.ReplaceAll(value, "\r", "<br>")
	return strings.ReplaceAll(value, "\n", "<br>")
}
