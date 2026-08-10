package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine/permission"
)

type permissionReviewAuditCommandOptions struct {
	dir          string
	outputFormat string
}

type permissionReviewAuditDeleteOutput struct {
	Status          string `json:"status"`
	SegmentsRemoved int    `json:"segments_removed"`
	BytesRemoved    int64  `json:"bytes_removed"`
}

func newPermissionReviewAuditCommand() *cobra.Command {
	options := &permissionReviewAuditCommandOptions{
		outputFormat: string(outputFormatText),
	}
	command := &cobra.Command{
		Use:   "permission-review-audit",
		Short: "Report or delete local redacted permission reviewer measurements",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			return usageErrorf(
				"permission-review-audit requires one of: report, delete",
			)
		},
	}
	command.PersistentFlags().StringVar(
		&options.dir,
		"dir",
		"",
		"Override the local permission reviewer audit directory",
	)
	command.PersistentFlags().StringVar(
		&options.outputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	command.AddCommand(
		newPermissionReviewAuditReportCommand(options),
		newPermissionReviewAuditDeleteCommand(options),
	)
	return command
}

func newPermissionReviewAuditReportCommand(
	options *permissionReviewAuditCommandOptions,
) *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Aggregate the retained local measurement window",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := parseOutputFormat(options.outputFormat)
			if err != nil {
				return err
			}
			store, err := openPermissionReviewAuditStore(options.dir)
			if err != nil {
				return err
			}
			report, err := store.Report()
			if err != nil {
				return fmt.Errorf(
					"read permission review audit: local store unavailable",
				)
			}
			if format == outputFormatJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetEscapeHTML(false)
				if err := encoder.Encode(report); err != nil {
					return fmt.Errorf(
						"write permission review audit report: %w",
						err,
					)
				}
				return nil
			}
			return renderPermissionReviewAuditReport(
				cmd.OutOrStdout(),
				report,
			)
		},
	}
}

func newPermissionReviewAuditDeleteCommand(
	options *permissionReviewAuditCommandOptions,
) *cobra.Command {
	confirm := false
	command := &cobra.Command{
		Use:   "delete",
		Short: "Delete exactly the owned retained measurement segments",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirm {
				return usageErrorf(
					"permission-review-audit delete requires --confirm",
				)
			}
			format, err := parseOutputFormat(options.outputFormat)
			if err != nil {
				return err
			}
			store, err := openPermissionReviewAuditStore(options.dir)
			if err != nil {
				return err
			}
			deleted, err := store.Delete()
			if err != nil {
				return fmt.Errorf(
					"delete permission review audit: local store unavailable",
				)
			}
			output := permissionReviewAuditDeleteOutput{
				Status:          "deleted",
				SegmentsRemoved: deleted.SegmentsRemoved,
				BytesRemoved:    deleted.BytesRemoved,
			}
			if format == outputFormatJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetEscapeHTML(false)
				if err := encoder.Encode(output); err != nil {
					return fmt.Errorf(
						"write permission review audit delete result: %w",
						err,
					)
				}
				return nil
			}
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"Deleted %d owned permission review audit segment(s), %d byte(s). Unknown neighboring files and the directory were preserved.\n",
				output.SegmentsRemoved,
				output.BytesRemoved,
			)
			return err
		},
	}
	command.Flags().BoolVar(
		&confirm,
		"confirm",
		false,
		"Confirm deletion of the owned retained audit segments",
	)
	return command
}

func openPermissionReviewAuditStore(
	dir string,
) (*permission.ReviewAuditStore, error) {
	store, err := permission.NewReviewAuditStore(
		permission.ReviewAuditStoreOptions{Dir: dir},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"open permission review audit: local store unavailable",
		)
	}
	return store, nil
}

func renderPermissionReviewAuditReport(
	writer io.Writer,
	report permission.ReviewAuditReport,
) error {
	var output strings.Builder
	fmt.Fprintf(&output, "Permission review audit: %s\n", report.Status)
	fmt.Fprintf(
		&output,
		"Retention: %s, %d segment(s), %d bytes/segment, %d bytes maximum window\n",
		report.Retention.Basis,
		report.Retention.MaxSegments,
		report.Retention.SegmentMaxBytes,
		report.Retention.MaxWindowBytes,
	)
	if report.Status == permission.ReviewAuditReportNoData {
		output.WriteString(
			"Evidence: unavailable; numeric zeros are not observed-zero rates.\n",
		)
	}
	fmt.Fprintf(
		&output,
		"Outcomes: eligible=%d attempts=%d terminal=%d completed=%d unavailable=%d escalations=%d\n",
		report.Outcomes.EligibleActions,
		report.Outcomes.ReviewerAttempts,
		report.Outcomes.TerminalResults,
		report.Outcomes.Completed,
		report.Outcomes.Unavailable,
		report.Outcomes.Escalations,
	)
	if report.Latency.Status == permission.ReviewAuditEvidenceAvailable {
		fmt.Fprintf(
			&output,
			"Latency: samples=%d p50=%dms p95=%dms\n",
			report.Latency.Samples,
			report.Latency.P50MS,
			report.Latency.P95MS,
		)
	} else {
		output.WriteString("Latency: unavailable\n")
	}
	renderPermissionReviewAuditComparison(
		&output,
		"Legacy classifier",
		report.LegacyClassifier,
	)
	renderPermissionReviewAuditComparison(&output, "Human", report.Human)
	renderPermissionReviewAuditComparison(
		&output,
		"Versioned corpus",
		report.VersionedCorpus,
	)
	fmt.Fprintf(
		&output,
		"Diagnostics: valid=%d malformed=%d partial_tail=%d tail_repairs=%d duplicates=%d orphans=%d incomplete=%d unmatched_comparisons=%d enqueue_drop=%d sink_failure=%d shutdown_flush_expiry=%d enqueue_after_close=%d\n",
		report.Diagnostics.ValidRecords,
		report.Diagnostics.MalformedRecords,
		report.Diagnostics.PartialTailRecords,
		report.Diagnostics.TailRepairs,
		report.Diagnostics.DuplicateRecords,
		report.Diagnostics.OrphanRecords,
		report.Diagnostics.IncompleteActions,
		report.Diagnostics.UnmatchedComparisons,
		report.Diagnostics.EnqueueDrops,
		report.Diagnostics.SinkFailures,
		report.Diagnostics.ShutdownFlushExpiry,
		report.Diagnostics.EnqueueAfterClose,
	)
	_, err := io.WriteString(writer, output.String())
	return err
}

func renderPermissionReviewAuditComparison(
	output *strings.Builder,
	label string,
	report permission.ReviewAuditComparisonReport,
) {
	if report.Status == permission.ReviewAuditEvidenceUnavailable {
		fmt.Fprintf(output, "%s: unavailable\n", label)
		return
	}
	fmt.Fprintf(
		output,
		"%s: denominator=%d disagreements=%d false_allows=%d\n",
		label,
		report.Denominator,
		report.Disagreements,
		report.FalseAllowCount,
	)
	for _, falseAllow := range report.FalseAllows {
		fmt.Fprintf(
			output,
			"  false_allow event=%s source=%s tool=%s action=%s\n",
			falseAllow.EventID,
			falseAllow.ComparisonSource,
			falseAllow.CanonicalTool,
			falseAllow.ActionKind,
		)
	}
}
