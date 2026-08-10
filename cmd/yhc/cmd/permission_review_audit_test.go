package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/permission"
)

const permissionReviewAuditCommandEventID = "0123456789abcdef0123456789abcdef"

func TestPermissionReviewAuditReportCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	store, err := permission.NewReviewAuditStore(
		permission.ReviewAuditStoreOptions{Dir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	records := []permission.ReviewAuditRecord{
		{
			EventID:            permissionReviewAuditCommandEventID,
			Kind:               permission.ReviewAuditKindEligible,
			CanonicalTool:      "TaskCreate",
			ActionKind:         "runtime_state",
			DeterministicClass: "review",
		},
		{
			EventID:      permissionReviewAuditCommandEventID,
			Kind:         permission.ReviewAuditKindAttempt,
			Provider:     "openai",
			Model:        "openai/gpt-5.2@2026-07-01",
			DataBoundary: permission.PermissionReviewDataBoundary,
		},
		{
			EventID:          permissionReviewAuditCommandEventID,
			Kind:             permission.ReviewAuditKindTerminal,
			ReviewerStatus:   "completed",
			ReviewerDecision: permission.ReviewDecisionApprove,
			ReasonCode:       permission.ReviewReasonExpectedSafe,
			LatencyMS:        17,
		},
		{
			EventID:          permissionReviewAuditCommandEventID,
			Kind:             permission.ReviewAuditKindComparison,
			ComparisonSource: "human",
			ExpectedDecision: "deny",
		},
		{
			EventID:            "fedcba9876543210fedcba9876543210",
			Kind:               permission.ReviewAuditKindEligible,
			CanonicalTool:      "TaskCreate",
			ActionKind:         "runtime_state",
			DeterministicClass: "review",
		},
		{
			EventID:        "fedcba9876543210fedcba9876543210",
			Kind:           permission.ReviewAuditKindTerminal,
			ReviewerStatus: "unavailable",
			ReasonCode:     "projection_unavailable",
			LatencyMS:      23,
		},
	}
	for _, record := range records {
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record %s: %v", record.Kind, err)
		}
	}

	stdout, stderr, err := executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"report",
		"--dir",
		dir,
		"--output-format",
		"json",
	)
	if err != nil {
		t.Fatalf("report json: %v, stderr=%s", err, stderr)
	}
	var report permission.ReviewAuditReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v; output=%s", err, stdout.String())
	}
	if report.Status != permission.ReviewAuditReportRetainedWindow ||
		report.Human.Denominator != 1 ||
		report.Human.FalseAllowCount != 1 ||
		report.Outcomes.TerminalResults != 2 ||
		report.Latency.Samples != 1 {
		t.Fatalf("report = %+v", report)
	}
	if strings.Contains(stdout.String(), dir) ||
		strings.Contains(stderr.String(), dir) {
		t.Fatal("JSON report leaked the absolute store path")
	}

	stdout, stderr, err = executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"report",
		"--dir",
		dir,
	)
	if err != nil {
		t.Fatalf("report text: %v, stderr=%s", err, stderr)
	}
	for _, required := range []string{
		"Permission review audit: retained_window",
		"Human: denominator=1 disagreements=1 false_allows=1",
		"false_allow event=" + permissionReviewAuditCommandEventID,
	} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("text report missing %q:\n%s", required, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), dir) {
		t.Fatal("text report leaked the absolute store path")
	}
}

func TestPermissionReviewAuditReportTerminalOnlyOmitsLatencyPercentiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	store, err := permission.NewReviewAuditStore(
		permission.ReviewAuditStoreOptions{Dir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []permission.ReviewAuditRecord{
		{
			EventID:            permissionReviewAuditCommandEventID,
			Kind:               permission.ReviewAuditKindEligible,
			CanonicalTool:      "TaskCreate",
			ActionKind:         "runtime_state",
			DeterministicClass: "review",
		},
		{
			EventID:        permissionReviewAuditCommandEventID,
			Kind:           permission.ReviewAuditKindTerminal,
			ReviewerStatus: "unavailable",
			ReasonCode:     "projection_unavailable",
			LatencyMS:      23,
		},
	} {
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record %s: %v", record.Kind, err)
		}
	}

	stdout, stderr, err := executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"report",
		"--dir",
		dir,
		"--output-format",
		"json",
	)
	if err != nil {
		t.Fatalf("report json: %v, stderr=%s", err, stderr)
	}
	var report permission.ReviewAuditReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v; output=%s", err, stdout.String())
	}
	if report.Outcomes.TerminalResults != 1 ||
		report.Latency.Status != permission.ReviewAuditEvidenceUnavailable ||
		report.Latency.Samples != 0 {
		t.Fatalf("report = %+v", report)
	}
	if strings.Contains(stdout.String(), `"p50_ms"`) ||
		strings.Contains(stdout.String(), `"p95_ms"`) {
		t.Fatalf("unavailable latency exposed zero percentiles: %s", stdout.String())
	}
}

func TestPermissionReviewAuditReportDispatcherDiagnostics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	store, err := permission.NewReviewAuditStore(
		permission.ReviewAuditStoreOptions{Dir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []permission.ReviewAuditRecord{
		{
			EventID:              permissionReviewAuditCommandEventID,
			Kind:                 permission.ReviewAuditKindDispatcherDiagnostic,
			DispatcherDiagnostic: permission.ReviewAuditDiagnosticEnqueueDrop,
			DiagnosticCount:      2,
		},
		{
			EventID:              "fedcba9876543210fedcba9876543210",
			Kind:                 permission.ReviewAuditKindDispatcherDiagnostic,
			DispatcherDiagnostic: permission.ReviewAuditDiagnosticSinkFailure,
			DiagnosticCount:      1,
		},
	} {
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record %s: %v", record.DispatcherDiagnostic, err)
		}
	}

	stdout, stderr, err := executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"report",
		"--dir",
		dir,
		"--output-format",
		"json",
	)
	if err != nil {
		t.Fatalf("report json: %v, stderr=%s", err, stderr)
	}
	var report permission.ReviewAuditReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v; output=%s", err, stdout.String())
	}
	if report.Status != permission.ReviewAuditReportPartial ||
		report.Diagnostics.EnqueueDrops != 2 ||
		report.Diagnostics.SinkFailures != 1 ||
		report.Outcomes.EligibleActions != 0 ||
		report.Outcomes.ReviewerAttempts != 0 ||
		report.Outcomes.TerminalResults != 0 {
		t.Fatalf("diagnostic report = %+v", report)
	}

	stdout, stderr, err = executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"report",
		"--dir",
		dir,
	)
	if err != nil {
		t.Fatalf("report text: %v, stderr=%s", err, stderr)
	}
	for _, required := range []string{
		"Permission review audit: partial",
		"enqueue_drop=2",
		"sink_failure=1",
		"shutdown_flush_expiry=0",
		"enqueue_after_close=0",
	} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("text report missing %q:\n%s", required, stdout.String())
		}
	}
}

func TestPermissionReviewAuditReportNoDataIsUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	stdout, stderr, err := executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"report",
		"--dir",
		dir,
	)
	if err != nil {
		t.Fatalf("report: %v, stderr=%s", err, stderr)
	}
	for _, required := range []string{
		"Permission review audit: no_data",
		"Evidence: unavailable",
		"Human: unavailable",
		"Versioned corpus: unavailable",
	} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("no-data report missing %q:\n%s", required, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), dir) ||
		strings.Contains(stderr.String(), dir) {
		t.Fatal("no-data report leaked the absolute store path")
	}
}

func TestPermissionReviewAuditDeleteCommandRequiresConfirmation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	store, err := permission.NewReviewAuditStore(
		permission.ReviewAuditStoreOptions{Dir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), permission.ReviewAuditRecord{
		EventID:            permissionReviewAuditCommandEventID,
		Kind:               permission.ReviewAuditKindEligible,
		CanonicalTool:      "TaskCreate",
		ActionKind:         "runtime_state",
		DeterministicClass: "review",
	}); err != nil {
		t.Fatal(err)
	}
	neighbor := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(neighbor, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err = executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"delete",
		"--dir",
		dir,
	)
	if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("unconfirmed delete error = %v, exit=%d", err, ExitCode(err))
	}
	report, err := store.Report()
	if err != nil || report.Diagnostics.ValidRecords != 1 {
		t.Fatalf("unconfirmed delete changed store: report=%+v err=%v", report, err)
	}

	stdout, stderr, err := executePermissionReviewAuditCommand(
		t,
		"permission-review-audit",
		"delete",
		"--dir",
		dir,
		"--confirm",
		"--output-format",
		"json",
	)
	if err != nil {
		t.Fatalf("confirmed delete: %v, stderr=%s", err, stderr)
	}
	var deleted permissionReviewAuditDeleteOutput
	if err := json.Unmarshal(stdout.Bytes(), &deleted); err != nil {
		t.Fatalf("decode delete result: %v; output=%s", err, stdout.String())
	}
	if deleted.Status != "deleted" ||
		deleted.SegmentsRemoved != 1 ||
		deleted.BytesRemoved <= 0 {
		t.Fatalf("delete result = %+v", deleted)
	}
	if _, err := os.Stat(neighbor); err != nil {
		t.Fatal("delete removed unknown neighboring file")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("delete removed audit directory")
	}
	if strings.Contains(stdout.String(), dir) ||
		strings.Contains(stderr.String(), dir) {
		t.Fatal("delete output leaked the absolute store path")
	}
}

func executePermissionReviewAuditCommand(
	t *testing.T,
	args ...string,
) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := newRootCommand()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	return stdout, stderr, root.Execute()
}
