package permission

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func reviewAuditReportRecord(
	event int,
	kind ReviewAuditKind,
) ReviewAuditRecord {
	record := validReviewAuditRecord(kind)
	record.EventID = reviewAuditReportEventID(event)
	return record
}

func TestBuildReviewAuditReportDispatcherDiagnostics(t *testing.T) {
	diagnostic := func(
		code ReviewAuditDispatcherDiagnostic,
		count uint64,
	) ReviewAuditRecord {
		record := reviewAuditReportRecord(1, ReviewAuditKindDispatcherDiagnostic)
		record.DispatcherDiagnostic = code
		record.DiagnosticCount = count
		return record
	}
	report := BuildReviewAuditReport(
		ReviewAuditLoadResult{Records: []ReviewAuditRecord{
			diagnostic(ReviewAuditDiagnosticEnqueueDrop, 2),
			diagnostic(ReviewAuditDiagnosticEnqueueDrop, 3),
			diagnostic(ReviewAuditDiagnosticSinkFailure, math.MaxUint64),
			diagnostic(ReviewAuditDiagnosticSinkFailure, 1),
			diagnostic(ReviewAuditDiagnosticFlushExpiry, 4),
			diagnostic(ReviewAuditDiagnosticAfterClose, 5),
		}},
		ReviewAuditRetentionReport{Basis: "size_window"},
	)
	if report.Status != ReviewAuditReportPartial {
		t.Fatalf("status = %s, want partial", report.Status)
	}
	if report.Diagnostics.EnqueueDrops != 5 ||
		report.Diagnostics.SinkFailures != math.MaxUint64 ||
		report.Diagnostics.ShutdownFlushExpiry != 4 ||
		report.Diagnostics.EnqueueAfterClose != 5 {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
	}
	if report.Outcomes.EligibleActions != 0 ||
		report.Outcomes.ReviewerAttempts != 0 ||
		report.Outcomes.TerminalResults != 0 ||
		report.Diagnostics.OrphanRecords != 0 ||
		report.Diagnostics.UnmatchedComparisons != 0 {
		t.Fatalf("dispatcher diagnostics affected lifecycle aggregates: %+v", report)
	}
}

func reviewAuditReportEventID(event int) string {
	const hex = "0123456789abcdef"
	result := make([]byte, 32)
	for i := range result {
		result[i] = '0'
	}
	result[30] = hex[(event/16)%16]
	result[31] = hex[event%16]
	return string(result)
}

func TestBuildReviewAuditReportDecisionMetrics(t *testing.T) {
	records := make([]ReviewAuditRecord, 0, 20)
	add := func(event int, kind ReviewAuditKind) *ReviewAuditRecord {
		records = append(records, reviewAuditReportRecord(event, kind))
		return &records[len(records)-1]
	}

	// Event 1: approve. Human and corpus expect deny (two false allows);
	// legacy classifier expects allow.
	add(1, ReviewAuditKindEligible)
	add(1, ReviewAuditKindAttempt)
	terminal := add(1, ReviewAuditKindTerminal)
	terminal.LatencyMS = 10
	legacy := add(1, ReviewAuditKindComparison)
	legacy.ComparisonSource = "legacy_classifier"
	legacy.ExpectedDecision = "allow"
	human := add(1, ReviewAuditKindComparison)
	human.ComparisonSource = "human"
	human.ExpectedDecision = "deny"
	corpus := add(1, ReviewAuditKindCorpusGroundTruth)
	corpus.ExpectedDecision = "deny"

	// Event 2: deny agrees with a human denial.
	add(2, ReviewAuditKindEligible)
	add(2, ReviewAuditKindAttempt)
	terminal = add(2, ReviewAuditKindTerminal)
	terminal.ReviewerDecision = ReviewDecisionDeny
	terminal.ReasonCode = ReviewReasonUnexpectedRisk
	terminal.LatencyMS = 20
	human = add(2, ReviewAuditKindComparison)
	human.ComparisonSource = "human"
	human.ExpectedDecision = "deny"

	// Event 3: escalation is an abstention/disagreement against legacy allow.
	add(3, ReviewAuditKindEligible)
	add(3, ReviewAuditKindAttempt)
	terminal = add(3, ReviewAuditKindTerminal)
	terminal.ReviewerDecision = ReviewDecisionEscalate
	terminal.ReasonCode = ReviewReasonInsufficientContext
	terminal.LatencyMS = 30
	legacy = add(3, ReviewAuditKindComparison)
	legacy.ComparisonSource = "legacy_classifier"
	legacy.ExpectedDecision = "allow"

	// Event 4: reviewer unavailable before an attempt was launched.
	add(4, ReviewAuditKindEligible)
	terminal = add(4, ReviewAuditKindTerminal)
	terminal.ReviewerStatus = "unavailable"
	terminal.ReviewerDecision = ""
	terminal.ReasonCode = "projection_unavailable"
	terminal.LatencyMS = 40

	report := BuildReviewAuditReport(
		ReviewAuditLoadResult{Records: records},
		ReviewAuditRetentionReport{
			Basis:           "size_window",
			SegmentMaxBytes: 1 << 20,
			MaxSegments:     8,
			MaxWindowBytes:  8 << 20,
		},
	)

	if report.Status != ReviewAuditReportRetainedWindow {
		t.Fatalf("Status = %s, want retained_window", report.Status)
	}
	wantOutcomes := ReviewAuditOutcomeReport{
		EligibleActions:  4,
		ReviewerAttempts: 3,
		TerminalResults:  4,
		Completed:        3,
		Unavailable:      1,
		Escalations:      1,
		UnavailableBy: []ReviewAuditReasonCount{{
			Reason: "projection_unavailable",
			Count:  1,
		}},
	}
	if !reflect.DeepEqual(report.Outcomes, wantOutcomes) {
		t.Fatalf("Outcomes = %+v, want %+v", report.Outcomes, wantOutcomes)
	}
	if report.Latency.Status != ReviewAuditEvidenceAvailable ||
		report.Latency.Samples != 3 ||
		report.Latency.P50MS != 20 ||
		report.Latency.P95MS != 30 {
		t.Fatalf("Latency = %+v, want 3 attempt-terminal samples p50=20 p95=30", report.Latency)
	}
	assertReviewAuditComparison(
		t,
		report.LegacyClassifier,
		2,
		1,
		0,
	)
	assertReviewAuditComparison(t, report.Human, 2, 1, 1)
	assertReviewAuditComparison(t, report.VersionedCorpus, 1, 1, 1)
	if got := report.Human.FalseAllows[0]; got.EventID != reviewAuditReportEventID(1) ||
		got.ComparisonSource != "human" ||
		got.CanonicalTool != "Bash" ||
		got.ActionKind != "filesystem_read" {
		t.Fatalf("human false allow = %+v", got)
	}
	if got := report.VersionedCorpus.FalseAllows[0]; got.ComparisonSource != "versioned_corpus" {
		t.Fatalf("corpus false allow = %+v", got)
	}
}

func TestBuildReviewAuditReportLatencyRequiresAttemptTerminalPair(t *testing.T) {
	retention := ReviewAuditRetentionReport{Basis: "size_window"}

	t.Run("attempt-terminal pairs include every terminal outcome", func(t *testing.T) {
		records := make([]ReviewAuditRecord, 0, 18)
		add := func(event int, kind ReviewAuditKind) *ReviewAuditRecord {
			records = append(records, reviewAuditReportRecord(event, kind))
			return &records[len(records)-1]
		}
		for _, test := range []struct {
			event   int
			reason  string
			latency int64
		}{
			{event: 1, latency: 90},
			{event: 2, reason: "timeout", latency: 10},
			{event: 3, reason: "reviewer_unavailable", latency: 40},
			{event: 4, reason: "invalid_result", latency: 20},
		} {
			add(test.event, ReviewAuditKindEligible)
			add(test.event, ReviewAuditKindAttempt)
			terminal := add(test.event, ReviewAuditKindTerminal)
			terminal.LatencyMS = test.latency
			if test.reason != "" {
				terminal.ReviewerStatus = "unavailable"
				terminal.ReviewerDecision = ""
				terminal.ReasonCode = test.reason
			}
		}

		report := BuildReviewAuditReport(
			ReviewAuditLoadResult{Records: records}, retention,
		)
		if report.Outcomes.TerminalResults != 4 ||
			report.Latency.Status != ReviewAuditEvidenceAvailable ||
			report.Latency.Samples != 4 ||
			report.Latency.P50MS != 20 ||
			report.Latency.P95MS != 90 {
			t.Fatalf("report = %+v, want four terminal outcomes and four samples p50=20 p95=90", report)
		}
	})

	for _, test := range []struct {
		name    string
		records []ReviewAuditRecord
	}{
		{
			name: "terminal-only projection unavailable",
			records: func() []ReviewAuditRecord {
				eligible := reviewAuditReportRecord(1, ReviewAuditKindEligible)
				terminal := reviewAuditReportRecord(1, ReviewAuditKindTerminal)
				terminal.ReviewerStatus = "unavailable"
				terminal.ReviewerDecision = ""
				terminal.ReasonCode = "projection_unavailable"
				return []ReviewAuditRecord{eligible, terminal}
			}(),
		},
		{
			name: "attempt without terminal",
			records: []ReviewAuditRecord{
				reviewAuditReportRecord(1, ReviewAuditKindEligible),
				reviewAuditReportRecord(1, ReviewAuditKindAttempt),
			},
		},
		{
			name: "eligible only",
			records: []ReviewAuditRecord{
				reviewAuditReportRecord(1, ReviewAuditKindEligible),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := BuildReviewAuditReport(
				ReviewAuditLoadResult{Records: test.records}, retention,
			)
			if report.Latency.Status != ReviewAuditEvidenceUnavailable ||
				report.Latency.Samples != 0 {
				t.Fatalf("Latency = %+v, want unavailable zero samples", report.Latency)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"p50_ms"`) ||
				strings.Contains(string(encoded), `"p95_ms"`) {
				t.Fatalf("unavailable latency exposed zero percentiles: %s", encoded)
			}
		})
	}
}

func TestBuildReviewAuditReportEvidenceStatus(t *testing.T) {
	retention := ReviewAuditRetentionReport{
		Basis:           "size_window",
		SegmentMaxBytes: 1024,
		MaxSegments:     3,
		MaxWindowBytes:  3072,
	}
	t.Run("no data is unavailable rather than zero evidence", func(t *testing.T) {
		report := BuildReviewAuditReport(ReviewAuditLoadResult{}, retention)
		if report.Status != ReviewAuditReportNoData {
			t.Fatalf("Status = %s, want no_data", report.Status)
		}
		for name, evidence := range map[string]ReviewAuditComparisonReport{
			"legacy": report.LegacyClassifier,
			"human":  report.Human,
			"corpus": report.VersionedCorpus,
		} {
			if evidence.Status != ReviewAuditEvidenceUnavailable ||
				evidence.Denominator != 0 {
				t.Fatalf("%s evidence = %+v, want unavailable", name, evidence)
			}
		}
		if report.Latency.Status != ReviewAuditEvidenceUnavailable {
			t.Fatalf("Latency status = %s, want unavailable", report.Latency.Status)
		}
	})

	t.Run("corruption without records is partial", func(t *testing.T) {
		report := BuildReviewAuditReport(ReviewAuditLoadResult{
			MalformedRecords:   2,
			PartialTailRecords: 1,
		}, retention)
		if report.Status != ReviewAuditReportPartial {
			t.Fatalf("Status = %s, want partial", report.Status)
		}
		if report.Diagnostics.MalformedRecords != 2 ||
			report.Diagnostics.PartialTailRecords != 1 {
			t.Fatalf("Diagnostics = %+v", report.Diagnostics)
		}
	})
}

func TestBuildReviewAuditReportLifecycleDiagnostics(t *testing.T) {
	records := []ReviewAuditRecord{
		reviewAuditReportRecord(1, ReviewAuditKindEligible),
		reviewAuditReportRecord(1, ReviewAuditKindEligible), // duplicate
		reviewAuditReportRecord(1, ReviewAuditKindAttempt),  // no terminal
		reviewAuditReportRecord(2, ReviewAuditKindTerminal), // orphan
		reviewAuditReportRecord(2, ReviewAuditKindComparison),
	}
	report := BuildReviewAuditReport(
		ReviewAuditLoadResult{Records: records},
		ReviewAuditRetentionReport{Basis: "size_window"},
	)
	if report.Status != ReviewAuditReportPartial {
		t.Fatalf("Status = %s, want partial", report.Status)
	}
	if report.Diagnostics.DuplicateRecords != 1 ||
		report.Diagnostics.OrphanRecords != 2 ||
		report.Diagnostics.IncompleteActions != 1 ||
		report.Diagnostics.UnmatchedComparisons != 1 {
		t.Fatalf("Diagnostics = %+v", report.Diagnostics)
	}
	if report.Human.Status != ReviewAuditEvidenceUnavailable ||
		report.LegacyClassifier.Status != ReviewAuditEvidenceUnavailable {
		t.Fatal("orphan/incomplete comparisons were presented as available evidence")
	}
}

func TestBuildReviewAuditReportMissingRequiredAttemptIsPartial(t *testing.T) {
	eligible := reviewAuditReportRecord(1, ReviewAuditKindEligible)
	terminal := reviewAuditReportRecord(1, ReviewAuditKindTerminal)
	terminal.ReviewerStatus = "unavailable"
	terminal.ReviewerDecision = ""
	terminal.ReasonCode = "timeout"
	report := BuildReviewAuditReport(
		ReviewAuditLoadResult{
			Records: []ReviewAuditRecord{eligible, terminal},
		},
		ReviewAuditRetentionReport{Basis: "size_window"},
	)
	if report.Status != ReviewAuditReportPartial ||
		report.Diagnostics.IncompleteActions != 1 ||
		report.Outcomes.Unavailable != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReviewAuditStoreReportAndJSONMinimization(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	eventID := reviewAuditReportEventID(1)
	for _, kind := range []ReviewAuditKind{
		ReviewAuditKindEligible,
		ReviewAuditKindAttempt,
		ReviewAuditKindTerminal,
		ReviewAuditKindComparison,
	} {
		record := validReviewAuditRecord(kind)
		record.EventID = eventID
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record %s: %v", kind, err)
		}
	}
	report, err := store.Report()
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Status != ReviewAuditReportRetainedWindow {
		t.Fatalf("Status = %s, want retained_window", report.Status)
	}
	if report.Retention.Basis != "size_window" ||
		report.Retention.SegmentMaxBytes != defaultReviewAuditSegmentMaxBytes ||
		report.Retention.MaxSegments != defaultReviewAuditMaxSegments {
		t.Fatalf("Retention = %+v", report.Retention)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		store.dir,
		"request_id",
		"tool_call_id",
		"input",
		"digest",
		"nonce",
		"rationale",
		"credential",
		"secret",
		"transcript",
		"session",
		"agent_id",
		"cwd",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report JSON contains forbidden value/token %q", forbidden)
		}
	}
}

func assertReviewAuditComparison(
	t *testing.T,
	report ReviewAuditComparisonReport,
	denominator int,
	disagreements int,
	falseAllows int,
) {
	t.Helper()
	if report.Status != ReviewAuditEvidenceAvailable ||
		report.Denominator != denominator ||
		report.Disagreements != disagreements ||
		report.FalseAllowCount != falseAllows ||
		len(report.FalseAllows) != falseAllows {
		t.Fatalf(
			"comparison = %+v, want denominator=%d disagreements=%d falseAllows=%d",
			report,
			denominator,
			disagreements,
			falseAllows,
		)
	}
}
