package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/identity"
)

const sessionAdministrationEnvelopeSchemaVersion = administrationEnvelopeSchemaVersion

type sessionsCommandOptions struct {
	outputFormat string
}

type (
	sessionAdministrationEnvelope = administrationEnvelope
	sessionAdministrationOutput   = administrationOutput
)

type sessionListEntry struct {
	SessionID       string `json:"session_id"`
	Title           string `json:"title"`
	LastModified    string `json:"last_modified,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	BranchName      string `json:"branch_name,omitempty"`
	Model           string `json:"model,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Status          string `json:"status,omitempty"`
	ReadOnly        bool   `json:"read_only,omitempty"`
	NeedsImport     bool   `json:"needs_import,omitempty"`
}

type sessionListOutput struct {
	Sessions   []sessionListEntry `json:"sessions"`
	NextCursor string             `json:"next_cursor,omitempty"`
	HasMore    bool               `json:"has_more"`
	Scanned    int                `json:"scanned"`
}

type sessionResumeOutput struct {
	SessionID     string   `json:"session_id"`
	ThreadID      string   `json:"thread_id,omitempty"`
	CWD           string   `json:"cwd,omitempty"`
	MessageCount  int      `json:"message_count"`
	TokenEstimate int      `json:"token_estimate"`
	Warnings      []string `json:"warnings,omitempty"`
}

type sessionRenameOutput struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

type sessionExportOutput struct {
	SessionID    string `json:"session_id"`
	Path         string `json:"path"`
	MessageCount int    `json:"message_count"`
}

type sessionForkOutput struct {
	ParentSessionID string   `json:"parent_session_id"`
	SessionID       string   `json:"session_id"`
	BranchName      string   `json:"branch_name"`
	MessagesCopied  int      `json:"messages_copied"`
	OperationID     string   `json:"operation_id"`
	Warnings        []string `json:"warnings,omitempty"`
}

type sessionDeleteOutput struct {
	SessionID                 string `json:"session_id"`
	TranscriptRemoved         bool   `json:"transcript_removed"`
	WorkBoardAuthorityRemoved bool   `json:"workboard_authority_removed"`
	WorkBoardShadowRemoved    bool   `json:"workboard_shadow_removed"`
	MediaRemoved              bool   `json:"media_removed"`
	CleanupCompleted          bool   `json:"cleanup_completed"`
}

type sessionWorkBoardRecoveryOutput struct {
	SessionID         string `json:"session_id"`
	PreviousBoardID   string `json:"previous_board_id"`
	PreviousRevision  uint64 `json:"previous_revision"`
	RecoveredBoardID  string `json:"recovered_board_id"`
	RecoveredRevision uint64 `json:"recovered_revision"`
}

func newSessionsCommand() *cobra.Command {
	options := &sessionsCommandOptions{outputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "sessions",
		Short: "Inspect and manage durable sessions",
		Args:  sessionAdministrationArgs("sessions", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return renderSessionAdministrationUsage(
				cmd,
				"sessions",
				usageErrorf("sessions requires one of: list, resume, rename, export, fork, delete, recover-workboard"),
			)
		},
	}
	command.PersistentFlags().StringVar(
		&options.outputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	command.AddCommand(
		newSessionsListCommand(options),
		newSessionsResumeCommand(options),
		newSessionsRenameCommand(options),
		newSessionsExportCommand(options),
		newSessionsForkCommand(options),
		newSessionsDeleteCommand(options),
		newSessionsRecoverWorkBoardCommand(options),
	)
	return command
}

func newSessionsDeleteCommand(
	options *sessionsCommandOptions,
) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete one exact durable session and its owned artifacts",
		Args:  sessionAdministrationArgs("sessions.delete", exactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionAdministration(
				cmd,
				options,
				"sessions.delete",
				func(
					ctx context.Context,
					service *engine.SessionService,
				) (sessionAdministrationOutput, error) {
					deleted, err := service.Delete(ctx, args[0])
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf(
							"delete session: %w",
							err,
						)
					}
					result := sessionDeleteOutput{
						SessionID:                 deleted.SessionID,
						TranscriptRemoved:         deleted.TranscriptRemoved,
						WorkBoardAuthorityRemoved: deleted.WorkBoardAuthorityRemoved,
						WorkBoardShadowRemoved:    deleted.WorkBoardShadowRemoved,
						MediaRemoved:              deleted.MediaRemoved,
						CleanupCompleted:          deleted.CleanupCompleted,
					}
					return sessionAdministrationOutput{
						text: fmt.Sprintf(
							"Deleted session %s and completed owned-artifact cleanup.",
							result.SessionID,
						),
						result: result,
					}, nil
				},
			)
		},
	}
}

func newSessionsRecoverWorkBoardCommand(
	options *sessionsCommandOptions,
) *cobra.Command {
	boardID := ""
	revision := uint64(0)
	acknowledge := false
	command := &cobra.Command{
		Use:   "recover-workboard <session-id>",
		Short: "Destructively restore the immutable WorkBoard baseline",
		Args: sessionAdministrationArgs(
			"sessions.recover-workboard",
			exactArgs(1),
		),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(boardID) == "" || revision == 0 {
				return renderSessionAdministrationUsage(
					cmd,
					"sessions.recover-workboard",
					usageErrorf(
						"exact --board-id and positive --revision are required",
					),
				)
			}
			if !acknowledge {
				return renderSessionAdministrationUsage(
					cmd,
					"sessions.recover-workboard",
					usageErrorf(
						"--acknowledge-data-loss is required",
					),
				)
			}
			return runSessionAdministration(
				cmd,
				options,
				"sessions.recover-workboard",
				func(
					ctx context.Context,
					service *engine.SessionService,
				) (sessionAdministrationOutput, error) {
					recovered, err := service.RecoverWorkBoard(
						ctx,
						engine.SessionWorkBoardRecoveryRequest{
							SessionID:           args[0],
							BoardID:             boardID,
							Revision:            revision,
							AcknowledgeDataLoss: acknowledge,
						},
					)
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf(
							"recover WorkBoard: %w",
							err,
						)
					}
					result := sessionWorkBoardRecoveryOutput{
						SessionID:         recovered.SessionID,
						PreviousBoardID:   recovered.PreviousBoardID,
						PreviousRevision:  recovered.PreviousRevision,
						RecoveredBoardID:  recovered.RecoveredBoardID,
						RecoveredRevision: recovered.RecoveredRevision,
					}
					return sessionAdministrationOutput{
						text: fmt.Sprintf(
							"Recovered WorkBoard for session %s from board %s revision %d into board %s revision %d.",
							result.SessionID,
							result.PreviousBoardID,
							result.PreviousRevision,
							result.RecoveredBoardID,
							result.RecoveredRevision,
						),
						result: result,
					}, nil
				},
			)
		},
	}
	command.Flags().StringVar(
		&boardID,
		"board-id",
		"",
		"Exact current WorkBoard BoardID",
	)
	command.Flags().Uint64Var(
		&revision,
		"revision",
		0,
		"Exact current WorkBoard revision",
	)
	command.Flags().BoolVar(
		&acknowledge,
		"acknowledge-data-loss",
		false,
		"Acknowledge loss of every post-cutover WorkBoard mutation",
	)
	return command
}

func newSessionsListCommand(options *sessionsCommandOptions) *cobra.Command {
	limit := 10
	search := ""
	cursor := ""
	command := &cobra.Command{
		Use:   "list",
		Short: "List saved sessions for the current project",
		Args:  sessionAdministrationArgs("sessions.list", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return renderSessionAdministrationUsage(
					cmd,
					"sessions.list",
					usageErrorf("session limit must be positive"),
				)
			}
			return runSessionAdministration(
				cmd,
				options,
				"sessions.list",
				func(ctx context.Context, service *engine.SessionService) (sessionAdministrationOutput, error) {
					page, err := service.Query(ctx, enginesession.SessionQuery{
						Scope:  enginesession.SessionScopeCWD,
						Limit:  limit,
						Cursor: cursor,
						Filter: enginesession.ListFilter{Search: strings.TrimSpace(search)},
					})
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("list sessions: %w", err)
					}
					result := projectSessionPage(page)
					return sessionAdministrationOutput{
						text:   formatSessionListText(result, strings.TrimSpace(search)),
						result: result,
					}, nil
				},
			)
		},
	}
	command.Flags().IntVar(&limit, "limit", 10, "Maximum sessions to return")
	command.Flags().StringVar(&search, "search", "", "Filter session title or prompt text")
	command.Flags().StringVar(&cursor, "cursor", "", "Continue from an opaque list cursor")
	return command
}

func newSessionsResumeCommand(options *sessionsCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Restore a session without starting a TUI",
		Args:  sessionAdministrationArgs("sessions.resume", exactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			var source enginesession.SessionInfo
			return runSessionAdministrationPrepared(
				cmd,
				options,
				"sessions.resume",
				func(
					ctx context.Context,
					cwd string,
					catalogPath string,
					legacyCatalogPath string,
				) error {
					var err error
					source, err = enginesession.AdmitSessionResume(
						ctx,
						enginesession.ResumeAdmissionRequest{
							SessionID:         args[0],
							CWD:               cwd,
							CatalogPath:       catalogPath,
							LegacyCatalogPath: legacyCatalogPath,
						},
					)
					return err
				},
				func(ctx context.Context, service *engine.SessionService) (sessionAdministrationOutput, error) {
					resumed, err := service.ResumeInfo(ctx, source)
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("resume session: %w", err)
					}
					result := sessionResumeOutput{
						SessionID:     resumed.SessionID,
						ThreadID:      resumed.Metadata.ThreadID,
						CWD:           resumed.Metadata.CWD,
						MessageCount:  len(resumed.Messages),
						TokenEstimate: resumed.TokenEstimate,
						Warnings:      append([]string(nil), resumed.Warnings...),
					}
					return sessionAdministrationOutput{
						text: fmt.Sprintf(
							"Resumed session %s (%d messages, ~%d tokens)",
							result.SessionID,
							result.MessageCount,
							result.TokenEstimate,
						),
						result:   result,
						warnings: result.Warnings,
					}, nil
				},
			)
		},
	}
}

func newSessionsRenameCommand(options *sessionsCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <session-id> <name>",
		Short: "Persist a display name for a saved session",
		Args:  sessionAdministrationArgs("sessions.rename", minimumArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(strings.Join(args[1:], " "))
			if name == "" {
				return renderSessionAdministrationUsage(
					cmd,
					"sessions.rename",
					usageErrorf("session name is required"),
				)
			}
			return runSessionAdministration(
				cmd,
				options,
				"sessions.rename",
				func(ctx context.Context, service *engine.SessionService) (sessionAdministrationOutput, error) {
					renamed, err := service.Rename(ctx, args[0], name)
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("rename session: %w", err)
					}
					result := sessionRenameOutput{SessionID: renamed.SessionID, Name: renamed.Name}
					return sessionAdministrationOutput{
						text:   fmt.Sprintf("Session %s renamed to: %s", result.SessionID, result.Name),
						result: result,
					}, nil
				},
			)
		},
	}
}

func newSessionsExportCommand(options *sessionsCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "export <session-id> [filename]",
		Short: "Export one persisted session as Markdown",
		Args:  sessionAdministrationArgs("sessions.export", rangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			filename := ""
			if len(args) == 2 {
				filename = args[1]
			}
			return runSessionAdministration(
				cmd,
				options,
				"sessions.export",
				func(ctx context.Context, service *engine.SessionService) (sessionAdministrationOutput, error) {
					exported, err := service.Export(ctx, args[0], filename)
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("export session: %w", err)
					}
					result := sessionExportOutput{
						SessionID:    exported.SessionID,
						Path:         exported.Path,
						MessageCount: exported.MessageCount,
					}
					return sessionAdministrationOutput{
						text: fmt.Sprintf(
							"Exported session %s (%d messages) to %s",
							result.SessionID,
							result.MessageCount,
							result.Path,
						),
						result: result,
					}, nil
				},
			)
		},
	}
}

func newSessionsForkCommand(options *sessionsCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "fork <session-id> [name]",
		Short: "Commit and validate a resumable child session",
		Args:  sessionAdministrationArgs("sessions.fork", rangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			branchName := ""
			if len(args) == 2 {
				branchName = args[1]
			}
			return runSessionAdministration(
				cmd,
				options,
				"sessions.fork",
				func(ctx context.Context, service *engine.SessionService) (sessionAdministrationOutput, error) {
					source, err := service.Resolve(ctx, args[0])
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("resolve fork source: %w", err)
					}
					resumed, created, err := service.Fork(ctx, engine.SessionForkRequest{
						Source:     &source,
						BranchName: branchName,
					})
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("fork session: %w", err)
					}
					result := sessionForkOutput{
						ParentSessionID: created.Info.ParentSessionID,
						SessionID:       created.Info.SessionID,
						BranchName:      created.Branch.BranchName,
						MessagesCopied:  created.Branch.MessagesCopied,
						OperationID:     created.OperationID,
						Warnings:        append([]string(nil), resumed.Warnings...),
					}
					return sessionAdministrationOutput{
						text: fmt.Sprintf(
							"Forked session %s to %s from turn %d. Now on branch: %s",
							result.ParentSessionID,
							result.SessionID,
							result.MessagesCopied,
							result.BranchName,
						),
						result:   result,
						warnings: result.Warnings,
					}, nil
				},
			)
		},
	}
}

func sessionAdministrationArgs(
	operation string,
	validate cobra.PositionalArgs,
) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return renderSessionAdministrationUsage(cmd, operation, err)
		}
		return nil
	}
}

func installSessionFlagErrorHandlers(command *cobra.Command) {
	command.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return renderSessionAdministrationUsage(
			cmd,
			sessionAdministrationOperation(cmd),
			usageErrorf("%v", err),
		)
	})
	for _, child := range command.Commands() {
		installSessionFlagErrorHandlers(child)
	}
}

func sessionAdministrationOperation(cmd *cobra.Command) string {
	if cmd == nil || cmd.Name() == "sessions" {
		return "sessions"
	}
	return "sessions." + cmd.Name()
}

func sessionAdministrationOutputFormat(cmd *cobra.Command) string {
	if cmd != nil {
		if flag := cmd.Flag("output-format"); flag != nil {
			return flag.Value.String()
		}
	}
	return string(outputFormatText)
}

func renderSessionAdministrationUsage(
	cmd *cobra.Command,
	operation string,
	err error,
) error {
	return renderSessionAdministrationFailure(
		formatForError(sessionAdministrationOutputFormat(cmd)),
		cmd.OutOrStdout(),
		cmd.ErrOrStderr(),
		operation,
		err,
		"usage_error",
		ExitUsage,
	)
}

func runSessionAdministration(
	cmd *cobra.Command,
	options *sessionsCommandOptions,
	operation string,
	action func(context.Context, *engine.SessionService) (sessionAdministrationOutput, error),
) error {
	return runSessionAdministrationPrepared(
		cmd,
		options,
		operation,
		nil,
		action,
	)
}

type sessionAdministrationPreflight func(
	context.Context,
	string,
	string,
	string,
) error

func runSessionAdministrationPrepared(
	cmd *cobra.Command,
	options *sessionsCommandOptions,
	operation string,
	preflight sessionAdministrationPreflight,
	action func(context.Context, *engine.SessionService) (sessionAdministrationOutput, error),
) error {
	format, err := parseOutputFormat(options.outputFormat)
	if err != nil {
		return renderSessionAdministrationFailure(
			formatForError(options.outputFormat),
			cmd.OutOrStdout(),
			cmd.ErrOrStderr(),
			operation,
			err,
			"usage_error",
			ExitUsage,
		)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return renderSessionAdministrationFailure(
			format,
			cmd.OutOrStdout(),
			cmd.ErrOrStderr(),
			operation,
			fmt.Errorf("resolve current working directory: %w", err),
			"session_error",
			ExitFailure,
		)
	}
	catalogPath, legacyCatalogPath := enginesession.DefaultCatalogPaths()
	if preflight != nil {
		if err := preflight(
			cmd.Context(),
			cwd,
			catalogPath,
			legacyCatalogPath,
		); err != nil {
			code, exitCode := sessionAdministrationFailureCode(err)
			return renderSessionAdministrationFailure(
				format,
				cmd.OutOrStdout(),
				cmd.ErrOrStderr(),
				operation,
				err,
				code,
				exitCode,
			)
		}
	}
	eng := engine.NewSessionAdministrationEngine(engine.SessionAdministrationConfig{
		CWD:                      cwd,
		SessionCatalogPath:       catalogPath,
		LegacySessionCatalogPath: legacyCatalogPath,
	})
	defer eng.Close()

	output, err := action(cmd.Context(), eng.SessionService())
	if err != nil {
		code, exitCode := sessionAdministrationFailureCode(err)
		return renderSessionAdministrationFailure(
			format,
			cmd.OutOrStdout(),
			cmd.ErrOrStderr(),
			operation,
			err,
			code,
			exitCode,
		)
	}
	return renderSessionAdministrationSuccess(
		format,
		cmd.OutOrStdout(),
		cmd.ErrOrStderr(),
		operation,
		output,
	)
}

func sessionAdministrationFailureCode(err error) (string, int) {
	exitCode := ExitCode(err)
	if exitCode == ExitSuccess {
		exitCode = ExitFailure
	}
	if exitCode == ExitCancelled || errors.Is(err, context.Canceled) {
		return "cancelled", ExitCancelled
	}
	if errors.Is(err, enginesession.ErrLegacySessionImportRequired) {
		return "legacy_session_import_required", exitCode
	}
	return "session_error", exitCode
}

func renderSessionAdministrationSuccess(
	format outputFormat,
	stdout io.Writer,
	stderr io.Writer,
	operation string,
	output sessionAdministrationOutput,
) error {
	return renderAdministrationSuccess(
		format,
		stdout,
		stderr,
		operation,
		output,
		"session",
	)
}

func renderSessionAdministrationFailure(
	format outputFormat,
	stdout io.Writer,
	stderr io.Writer,
	operation string,
	err error,
	code string,
	exitCode int,
) error {
	return renderAdministrationFailure(
		format,
		stdout,
		stderr,
		operation,
		err,
		code,
		exitCode,
		"session",
	)
}

func projectSessionPage(page *enginesession.SessionPage) sessionListOutput {
	if page == nil {
		return sessionListOutput{Sessions: []sessionListEntry{}}
	}
	result := sessionListOutput{
		Sessions:   make([]sessionListEntry, 0, len(page.Sessions)),
		NextCursor: page.NextCursor,
		HasMore:    page.HasMore,
		Scanned:    page.Scanned,
	}
	for _, info := range page.Sessions {
		modified := ""
		if !info.LastModified.IsZero() {
			modified = info.LastModified.UTC().Format(time.RFC3339Nano)
		}
		result.Sessions = append(result.Sessions, sessionListEntry{
			SessionID:       info.SessionID,
			Title:           sessionListTitle(info),
			LastModified:    modified,
			CWD:             info.CWD,
			ParentSessionID: info.ParentSessionID,
			BranchName:      info.BranchName,
			Model:           info.Model,
			Provider:        info.Provider,
			Status:          info.Status,
			ReadOnly:        info.ReadOnly,
			NeedsImport:     info.NeedsImport,
		})
	}
	return result
}

func sessionListTitle(info enginesession.SessionInfo) string {
	for _, value := range []string{info.CustomTitle, info.Summary, info.FirstPrompt} {
		if value = strings.Join(strings.Fields(value), " "); value != "" {
			runes := []rune(value)
			if len(runes) > 72 {
				return string(runes[:69]) + "..."
			}
			return value
		}
	}
	return "(untitled)"
}

func formatSessionListText(result sessionListOutput, search string) string {
	if len(result.Sessions) == 0 {
		if search != "" {
			return fmt.Sprintf("No saved sessions matched %q.", search)
		}
		return "No saved sessions found."
	}
	var builder strings.Builder
	if search != "" {
		fmt.Fprintf(&builder, "Sessions matching %q (%d shown):\n", search, len(result.Sessions))
	} else {
		fmt.Fprintf(&builder, "Sessions (%d shown):\n", len(result.Sessions))
	}
	for _, info := range result.Sessions {
		modified := info.LastModified
		if modified == "" {
			modified = "unknown"
		}
		state := ""
		if info.NeedsImport {
			state = "  [import required]"
		}
		fmt.Fprintf(&builder, "  %s  %s  %s%s\n", info.SessionID, modified, info.Title, state)
	}
	if result.HasMore {
		fmt.Fprintf(&builder, "Next cursor: %s\n", result.NextCursor)
	}
	fmt.Fprintf(&builder, "Use %s sessions resume <session-id> to restore a session.", identity.CommandName)
	return builder.String()
}
