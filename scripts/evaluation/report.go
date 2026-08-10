package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	reportKind     = "eino-agent/evaluation-report"
	reportMaxBytes = 64 << 10
)

type evaluationReport struct {
	Kind          string            `json:"kind"`
	SchemaVersion int               `json:"schema_version"`
	RunnerVersion string            `json:"runner_version"`
	Scenario      reportScenario    `json:"scenario"`
	Harness       harnessResult     `json:"harness"`
	Grade         canonicalGrade    `json:"grade"`
	Diagnostics   reportDiagnostics `json:"diagnostics"`
}

type reportScenario struct {
	ID            string `json:"id"`
	Version       int    `json:"version"`
	FixtureSHA256 string `json:"fixture_sha256"`
	TaskSHA256    string `json:"task_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
}

type harnessResult struct {
	State   string `json:"state"`
	Code    string `json:"code"`
	Replays int    `json:"replays"`
}

type canonicalGrade struct {
	Entrypoints    []entrypointGrade `json:"entrypoints"`
	Outcome        stateCode         `json:"outcome"`
	PublicGrader   stateCode         `json:"public_grader"`
	HiddenGrader   stateCode         `json:"hidden_grader"`
	ExpectedChange expectedChange    `json:"expected_change"`
	Policy         policyGrade       `json:"policy"`
	Budgets        []budgetGrade     `json:"budgets"`
	Usage          usageGrade        `json:"usage"`
	Cost           stateCode         `json:"cost"`
	Recovery       stateCode         `json:"recovery"`
	Residual       residualGrade     `json:"residual"`
	Isolation      []isolationGrade  `json:"isolation"`
	Cleanup        stateCode         `json:"cleanup"`
}

type entrypointGrade struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type stateCode struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

type expectedChange struct {
	State         string `json:"state"`
	Code          string `json:"code"`
	RelativePath  string `json:"relative_path"`
	ContentSHA256 string `json:"content_sha256"`
}

type policyGrade struct {
	Attempts        int `json:"attempts"`
	BlockedAttempts int `json:"blocked_attempts"`
	AllowedAttempts int `json:"allowed_attempts"`
	Violations      int `json:"violations"`
}

type budgetGrade struct {
	Name     string `json:"name"`
	Observed int    `json:"observed"`
	Limit    int    `json:"limit"`
	State    string `json:"state"`
}

type usageGrade struct {
	Coverage     string `json:"coverage"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

type residualGrade struct {
	State                 string   `json:"state"`
	Code                  string   `json:"code"`
	ProductChanges        []string `json:"product_changes"`
	FinalTreeSHA256       string   `json:"final_tree_sha256"`
	RuntimeMetadataState  string   `json:"runtime_metadata_state"`
	RuntimeMetadataReason string   `json:"runtime_metadata_reason"`
}

type isolationGrade struct {
	Axis      string `json:"axis"`
	State     string `json:"state"`
	Mechanism string `json:"mechanism"`
}

type reportDiagnostics struct {
	ReplayDurationMilliseconds []int64 `json:"replay_duration_milliseconds"`
	StdoutTruncated            bool    `json:"stdout_truncated"`
	StderrTruncated            bool    `json:"stderr_truncated"`
}

type publicationOptions struct {
	afterValidation func(parent string) error
	beforeLink      func(parent string) error
}

type reportTarget struct {
	path       string
	parentInfo os.FileInfo
}

func canonicalGradeBytes(grade canonicalGrade) ([]byte, error) {
	data, err := json.Marshal(grade)
	if err != nil {
		return nil, fail("grade_encode_failed", err)
	}
	return data, nil
}

func canonicalReportTarget(path string) (reportTarget, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return reportTarget{}, fail("report_parent_invalid", err)
	}
	parent := filepath.Dir(absolute)
	originalParent, err := os.Lstat(parent)
	if err != nil || !originalParent.IsDir() || originalParent.Mode()&os.ModeSymlink != 0 {
		return reportTarget{}, fail("report_parent_invalid", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return reportTarget{}, fail("report_parent_invalid", err)
	}
	info, err := os.Lstat(canonicalParent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return reportTarget{}, fail("report_parent_invalid", err)
	}
	name := filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return reportTarget{}, fail("report_path_invalid", nil)
	}
	target := filepath.Join(canonicalParent, name)
	if _, err := os.Lstat(target); err == nil {
		return reportTarget{}, fail("report_collision", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return reportTarget{}, fail("report_path_invalid", err)
	}
	return reportTarget{path: target, parentInfo: info}, nil
}

func canonicalReportPath(path string) (string, error) {
	target, err := canonicalReportTarget(path)
	return target.path, err
}

func publishReport(path string, report evaluationReport, forbidden []string, options publicationOptions) error {
	target, err := canonicalReportTarget(path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return fail("report_encode_failed", err)
	}
	if len(data) > reportMaxBytes {
		return fail("report_too_large", nil)
	}
	for _, sentinel := range forbidden {
		if sentinel != "" && bytes.Contains(data, []byte(sentinel)) {
			return fail("report_redaction_failed", nil)
		}
	}
	if options.afterValidation != nil {
		if err := options.afterValidation(filepath.Dir(target.path)); err != nil {
			return fail("report_parent_replaced", err)
		}
	}
	return publishNoReplace(target, data, options)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digestFile(path string, maxBytes int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return "", fail("binary_invalid", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fail("binary_invalid", err)
	}
	defer file.Close()
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil || read > maxBytes {
		return "", fail("binary_invalid", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func redactionSentinels(manifest scenarioManifest, script providerScript, paths ...string) []string {
	sentinels := []string{
		manifest.Task.Prompt,
		manifest.Provider.FakeAPIKey,
		"fixture.local/localized-write-fix",
		"func Greeting(name string)",
		"outside-before",
	}
	for _, step := range script.Steps {
		sentinels = append(sentinels, step.CallID)
		if len(step.Content) >= 16 {
			sentinels = append(sentinels, step.Content)
		}
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		sentinels = append(sentinels, path, filepath.ToSlash(path))
	}
	return sentinels
}

func validateHeadlessEnvelope(data []byte, assistantText string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope struct {
		SchemaVersion  int             `json:"schema_version"`
		Status         string          `json:"status"`
		Output         string          `json:"output"`
		SessionID      string          `json:"session_id"`
		TerminalReason string          `json:"terminal_reason"`
		ExitCode       int             `json:"exit_code"`
		Error          json.RawMessage `json:"error"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		return fail("headless_envelope_invalid", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fail("headless_envelope_invalid", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Status != "completed" || envelope.Output != assistantText ||
		envelope.ExitCode != 0 || len(envelope.Error) != 0 {
		return fail("headless_envelope_invalid", fmt.Errorf("unexpected terminal envelope"))
	}
	return nil
}
