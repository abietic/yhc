package permission

import (
	"math"
	"sort"
)

// ReviewAuditReportStatus describes how much retained evidence the report can
// safely claim. Counts always describe the retained local window only.
type ReviewAuditReportStatus string

const (
	ReviewAuditReportNoData         ReviewAuditReportStatus = "no_data"
	ReviewAuditReportRetainedWindow ReviewAuditReportStatus = "retained_window"
	ReviewAuditReportPartial        ReviewAuditReportStatus = "partial"
)

// ReviewAuditEvidenceStatus distinguishes measured evidence from an absent
// denominator. An unavailable source is never presented as zero disagreement.
type ReviewAuditEvidenceStatus string

const (
	ReviewAuditEvidenceAvailable   ReviewAuditEvidenceStatus = "available"
	ReviewAuditEvidenceUnavailable ReviewAuditEvidenceStatus = "unavailable"
)

// ReviewAuditRetentionReport describes the fixed size-window policy. It makes
// no age-retention claim.
type ReviewAuditRetentionReport struct {
	Basis           string `json:"basis"`
	SegmentMaxBytes int64  `json:"segment_max_bytes"`
	MaxSegments     int    `json:"max_segments"`
	MaxWindowBytes  int64  `json:"max_window_bytes"`
}

// ReviewAuditOutcomeReport separates reviewer attempts from terminal results.
type ReviewAuditOutcomeReport struct {
	EligibleActions  int                      `json:"eligible_actions"`
	ReviewerAttempts int                      `json:"reviewer_attempts"`
	TerminalResults  int                      `json:"terminal_results"`
	Completed        int                      `json:"completed"`
	Unavailable      int                      `json:"unavailable"`
	Escalations      int                      `json:"escalations"`
	UnavailableBy    []ReviewAuditReasonCount `json:"unavailable_by_reason"`
}

// ReviewAuditReasonCount is one deterministic unavailable-reason aggregate.
type ReviewAuditReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// ReviewAuditLatencyReport is computed from unique retained attempt-terminal
// pairs using nearest-rank percentiles.
type ReviewAuditLatencyReport struct {
	Status  ReviewAuditEvidenceStatus `json:"status"`
	Samples int                       `json:"samples"`
	P50MS   int64                     `json:"p50_ms,omitempty"`
	P95MS   int64                     `json:"p95_ms,omitempty"`
}

// ReviewAuditFalseAllow identifies every retained reviewer approval whose
// typed comparable label expected denial. It contains no request identity,
// raw input, path, digest, nonce, or rationale.
type ReviewAuditFalseAllow struct {
	EventID          string `json:"event_id"`
	ComparisonSource string `json:"comparison_source"`
	CanonicalTool    string `json:"canonical_tool"`
	ActionKind       string `json:"action_kind"`
}

// ReviewAuditComparisonReport keeps each ground-truth source on its own
// denominator.
type ReviewAuditComparisonReport struct {
	Status          ReviewAuditEvidenceStatus `json:"status"`
	Denominator     int                       `json:"denominator"`
	Disagreements   int                       `json:"disagreements"`
	FalseAllowCount int                       `json:"false_allow_count"`
	FalseAllows     []ReviewAuditFalseAllow   `json:"false_allows"`
}

// ReviewAuditDiagnostics describes evidence loss or lifecycle ambiguity. Any
// nonzero value makes the overall report partial.
type ReviewAuditDiagnostics struct {
	ValidRecords         int    `json:"valid_records"`
	MalformedRecords     int    `json:"malformed_records"`
	PartialTailRecords   int    `json:"partial_tail_records"`
	TailRepairs          int    `json:"tail_repairs"`
	DuplicateRecords     int    `json:"duplicate_records"`
	OrphanRecords        int    `json:"orphan_records"`
	IncompleteActions    int    `json:"incomplete_actions"`
	UnmatchedComparisons int    `json:"unmatched_comparisons"`
	EnqueueDrops         uint64 `json:"enqueue_drops"`
	SinkFailures         uint64 `json:"sink_failures"`
	ShutdownFlushExpiry  uint64 `json:"shutdown_flush_expiry"`
	EnqueueAfterClose    uint64 `json:"enqueue_after_close"`
}

// ReviewAuditReport is the deterministic aggregate for the retained local
// window. Human, legacy-classifier, and versioned-corpus evidence never share
// a denominator.
type ReviewAuditReport struct {
	SchemaVersion    int                         `json:"schema_version"`
	Status           ReviewAuditReportStatus     `json:"status"`
	Retention        ReviewAuditRetentionReport  `json:"retention"`
	Outcomes         ReviewAuditOutcomeReport    `json:"outcomes"`
	Latency          ReviewAuditLatencyReport    `json:"latency"`
	LegacyClassifier ReviewAuditComparisonReport `json:"legacy_classifier"`
	Human            ReviewAuditComparisonReport `json:"human"`
	VersionedCorpus  ReviewAuditComparisonReport `json:"versioned_corpus"`
	Diagnostics      ReviewAuditDiagnostics      `json:"diagnostics"`
}

type reviewAuditEventGroup struct {
	eligible    *ReviewAuditRecord
	attempt     *ReviewAuditRecord
	terminal    *ReviewAuditRecord
	comparisons map[string]*ReviewAuditRecord
	corpus      *ReviewAuditRecord
	recordCount int
}

// Report loads and aggregates the current retained window under the same
// bounded cross-process lock used by Record and Delete.
func (s *ReviewAuditStore) Report() (ReviewAuditReport, error) {
	load, err := s.Load()
	if err != nil {
		return ReviewAuditReport{}, err
	}
	return BuildReviewAuditReport(load, ReviewAuditRetentionReport{
		Basis:           "size_window",
		SegmentMaxBytes: s.segmentMaxBytes,
		MaxSegments:     s.maxSegments,
		MaxWindowBytes:  s.segmentMaxBytes * int64(s.maxSegments),
	}), nil
}

// BuildReviewAuditReport aggregates already validated retained records. It is
// exported for deterministic offline fixtures and contains no storage access.
func BuildReviewAuditReport(
	load ReviewAuditLoadResult,
	retention ReviewAuditRetentionReport,
) ReviewAuditReport {
	report := ReviewAuditReport{
		SchemaVersion: ReviewAuditSchemaVersion,
		Status:        ReviewAuditReportNoData,
		Retention:     retention,
		Latency: ReviewAuditLatencyReport{
			Status: ReviewAuditEvidenceUnavailable,
		},
		LegacyClassifier: newReviewAuditComparisonReport(),
		Human:            newReviewAuditComparisonReport(),
		VersionedCorpus:  newReviewAuditComparisonReport(),
		Diagnostics: ReviewAuditDiagnostics{
			ValidRecords:       len(load.Records),
			MalformedRecords:   load.MalformedRecords,
			PartialTailRecords: load.PartialTailRecords,
			TailRepairs:        load.TailRepairs,
		},
	}

	groups := make(map[string]*reviewAuditEventGroup)
	hasActionData := false
	for i := range load.Records {
		record := &load.Records[i]
		if record.Kind == ReviewAuditKindDispatcherDiagnostic {
			addReviewAuditDispatcherDiagnostic(&report.Diagnostics, record)
			continue
		}
		if record.Kind == ReviewAuditKindStorageRecovery {
			continue
		}
		hasActionData = true
		group := groups[record.EventID]
		if group == nil {
			group = &reviewAuditEventGroup{
				comparisons: make(map[string]*ReviewAuditRecord),
			}
			groups[record.EventID] = group
		}
		group.recordCount++
		switch record.Kind {
		case ReviewAuditKindEligible:
			if group.eligible != nil {
				report.Diagnostics.DuplicateRecords++
				continue
			}
			group.eligible = record
		case ReviewAuditKindAttempt:
			if group.attempt != nil {
				report.Diagnostics.DuplicateRecords++
				continue
			}
			group.attempt = record
		case ReviewAuditKindTerminal:
			if group.terminal != nil {
				report.Diagnostics.DuplicateRecords++
				continue
			}
			group.terminal = record
		case ReviewAuditKindComparison:
			if group.comparisons[record.ComparisonSource] != nil {
				report.Diagnostics.DuplicateRecords++
				continue
			}
			group.comparisons[record.ComparisonSource] = record
		case ReviewAuditKindCorpusGroundTruth:
			if group.corpus != nil {
				report.Diagnostics.DuplicateRecords++
				continue
			}
			group.corpus = record
		}
	}

	eventIDs := make([]string, 0, len(groups))
	for eventID := range groups {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Strings(eventIDs)

	latencies := make([]int64, 0, len(groups))
	unavailable := make(map[string]int)
	for _, eventID := range eventIDs {
		group := groups[eventID]
		if group.eligible == nil {
			report.Diagnostics.OrphanRecords += group.recordCount
		} else {
			report.Outcomes.EligibleActions++
		}
		if group.attempt != nil {
			report.Outcomes.ReviewerAttempts++
		}
		if group.terminal == nil {
			if group.eligible != nil {
				report.Diagnostics.IncompleteActions++
			}
			report.Diagnostics.UnmatchedComparisons += len(group.comparisons)
			if group.corpus != nil {
				report.Diagnostics.UnmatchedComparisons++
			}
			continue
		}

		report.Outcomes.TerminalResults++
		if group.attempt != nil {
			latencies = append(latencies, group.terminal.LatencyMS)
		}
		switch group.terminal.ReviewerStatus {
		case "completed":
			report.Outcomes.Completed++
			if group.eligible != nil && group.attempt == nil {
				report.Diagnostics.IncompleteActions++
			}
			if group.terminal.ReviewerDecision == ReviewDecisionEscalate {
				report.Outcomes.Escalations++
			}
		case "unavailable":
			report.Outcomes.Unavailable++
			unavailable[group.terminal.ReasonCode]++
			if group.eligible != nil &&
				group.attempt == nil &&
				reviewAuditUnavailableRequiresAttempt(
					group.terminal.ReasonCode,
				) {
				report.Diagnostics.IncompleteActions++
			}
		}

		if group.eligible == nil {
			report.Diagnostics.UnmatchedComparisons += len(group.comparisons)
			if group.corpus != nil {
				report.Diagnostics.UnmatchedComparisons++
			}
			continue
		}
		if group.terminal.ReviewerStatus != "completed" {
			// A typed label may still be retained when the reviewer was
			// unavailable. It has no comparison denominator, but it is not a
			// corrupt or missing lifecycle.
			continue
		}
		for source, comparison := range group.comparisons {
			switch source {
			case "legacy_classifier":
				addReviewAuditComparison(
					&report.LegacyClassifier,
					eventID,
					group.eligible,
					group.terminal,
					comparison,
				)
			case "human":
				addReviewAuditComparison(
					&report.Human,
					eventID,
					group.eligible,
					group.terminal,
					comparison,
				)
			}
		}
		if group.corpus != nil {
			addReviewAuditComparison(
				&report.VersionedCorpus,
				eventID,
				group.eligible,
				group.terminal,
				group.corpus,
			)
		}
	}

	report.Outcomes.UnavailableBy = sortedReviewAuditReasonCounts(unavailable)
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		report.Latency.Status = ReviewAuditEvidenceAvailable
		report.Latency.Samples = len(latencies)
		report.Latency.P50MS = nearestRankReviewAuditPercentile(latencies, 50)
		report.Latency.P95MS = nearestRankReviewAuditPercentile(latencies, 95)
	}
	finalizeReviewAuditComparison(&report.LegacyClassifier)
	finalizeReviewAuditComparison(&report.Human)
	finalizeReviewAuditComparison(&report.VersionedCorpus)

	switch {
	case !hasActionData &&
		report.Diagnostics.MalformedRecords == 0 &&
		report.Diagnostics.PartialTailRecords == 0 &&
		report.Diagnostics.TailRepairs == 0 &&
		report.Diagnostics.EnqueueDrops == 0 &&
		report.Diagnostics.SinkFailures == 0 &&
		report.Diagnostics.ShutdownFlushExpiry == 0 &&
		report.Diagnostics.EnqueueAfterClose == 0:
		report.Status = ReviewAuditReportNoData
	case report.Diagnostics.MalformedRecords > 0 ||
		report.Diagnostics.PartialTailRecords > 0 ||
		report.Diagnostics.TailRepairs > 0 ||
		report.Diagnostics.DuplicateRecords > 0 ||
		report.Diagnostics.OrphanRecords > 0 ||
		report.Diagnostics.IncompleteActions > 0 ||
		report.Diagnostics.UnmatchedComparisons > 0 ||
		report.Diagnostics.EnqueueDrops > 0 ||
		report.Diagnostics.SinkFailures > 0 ||
		report.Diagnostics.ShutdownFlushExpiry > 0 ||
		report.Diagnostics.EnqueueAfterClose > 0:
		report.Status = ReviewAuditReportPartial
	default:
		report.Status = ReviewAuditReportRetainedWindow
	}
	return report
}

func addReviewAuditDispatcherDiagnostic(
	diagnostics *ReviewAuditDiagnostics,
	record *ReviewAuditRecord,
) {
	switch record.DispatcherDiagnostic {
	case ReviewAuditDiagnosticEnqueueDrop:
		diagnostics.EnqueueDrops = saturatingReviewAuditAdd(
			diagnostics.EnqueueDrops, record.DiagnosticCount,
		)
	case ReviewAuditDiagnosticSinkFailure:
		diagnostics.SinkFailures = saturatingReviewAuditAdd(
			diagnostics.SinkFailures, record.DiagnosticCount,
		)
	case ReviewAuditDiagnosticFlushExpiry:
		diagnostics.ShutdownFlushExpiry = saturatingReviewAuditAdd(
			diagnostics.ShutdownFlushExpiry, record.DiagnosticCount,
		)
	case ReviewAuditDiagnosticAfterClose:
		diagnostics.EnqueueAfterClose = saturatingReviewAuditAdd(
			diagnostics.EnqueueAfterClose, record.DiagnosticCount,
		)
	}
}

func saturatingReviewAuditAdd(current, delta uint64) uint64 {
	if math.MaxUint64-current < delta {
		return math.MaxUint64
	}
	return current + delta
}

func newReviewAuditComparisonReport() ReviewAuditComparisonReport {
	return ReviewAuditComparisonReport{
		Status:      ReviewAuditEvidenceUnavailable,
		FalseAllows: make([]ReviewAuditFalseAllow, 0),
	}
}

func addReviewAuditComparison(
	report *ReviewAuditComparisonReport,
	eventID string,
	eligible *ReviewAuditRecord,
	terminal *ReviewAuditRecord,
	comparison *ReviewAuditRecord,
) {
	report.Denominator++
	actual := terminal.ReviewerDecision
	expected := comparison.ExpectedDecision
	if actual == ReviewDecisionApprove {
		actual = "allow"
	}
	if actual != expected {
		report.Disagreements++
	}
	if terminal.ReviewerDecision == ReviewDecisionApprove && expected == "deny" {
		report.FalseAllows = append(report.FalseAllows, ReviewAuditFalseAllow{
			EventID:          eventID,
			ComparisonSource: comparison.ComparisonSource,
			CanonicalTool:    eligible.CanonicalTool,
			ActionKind:       eligible.ActionKind,
		})
	}
}

func finalizeReviewAuditComparison(report *ReviewAuditComparisonReport) {
	if report.Denominator > 0 {
		report.Status = ReviewAuditEvidenceAvailable
	}
	sort.Slice(report.FalseAllows, func(i, j int) bool {
		if report.FalseAllows[i].EventID != report.FalseAllows[j].EventID {
			return report.FalseAllows[i].EventID < report.FalseAllows[j].EventID
		}
		return report.FalseAllows[i].ComparisonSource <
			report.FalseAllows[j].ComparisonSource
	})
	report.FalseAllowCount = len(report.FalseAllows)
}

func sortedReviewAuditReasonCounts(counts map[string]int) []ReviewAuditReasonCount {
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	result := make([]ReviewAuditReasonCount, 0, len(reasons))
	for _, reason := range reasons {
		result = append(result, ReviewAuditReasonCount{
			Reason: reason,
			Count:  counts[reason],
		})
	}
	return result
}

func nearestRankReviewAuditPercentile(sortedValues []int64, percentile int) int64 {
	rank := (percentile*len(sortedValues) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sortedValues[rank-1]
}

func reviewAuditUnavailableRequiresAttempt(reason string) bool {
	switch reason {
	case "timeout", "reviewer_unavailable", "invalid_result", "binding_changed":
		return true
	default:
		return false
	}
}
