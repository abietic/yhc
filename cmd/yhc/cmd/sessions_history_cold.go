package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/spf13/cobra"
)

func newSessionsRecoverHistoryCommand(options *sessionsCommandOptions) *cobra.Command {
	var sourceSHA, migrationID, bindingSHA string
	command := &cobra.Command{
		Use: "recover-history <session-id>", Short: "Explicitly import verified history into a closed restored workspace",
		Args: sessionAdministrationArgs("sessions.recover-history", exactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			var proof filehistory.ColdHistoryProof
			raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
			if err != nil || len(raw) > 65536 {
				return renderSessionAdministrationUsage(cmd, "sessions.recover-history", usageErrorf("bounded source proof JSON is required on stdin"))
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&proof) != nil || decoder.Decode(new(any)) != io.EOF || filehistory.ValidateColdHistoryProof(proof) != nil || proof.SHA256 != sourceSHA || proof.SessionID != args[0] {
				return renderSessionAdministrationUsage(cmd, "sessions.recover-history", usageErrorf("source proof identity or external digest is invalid"))
			}
			request := filehistory.ColdHistoryImport{Version: 1, Source: proof, SourceSHA256: sourceSHA, MigrationID: migrationID, BindingSHA256: bindingSHA}
			if filehistory.ValidateColdHistoryImport(request) != nil {
				return renderSessionAdministrationUsage(cmd, "sessions.recover-history", usageErrorf("migration UUID and anchored target binding are required"))
			}
			var source enginesession.SessionInfo
			return runSessionAdministrationPrepared(cmd, options, "sessions.recover-history",
				func(ctx context.Context, cwd, catalog, legacy string) error {
					if proof.Root != cwd {
						return usageErrorf("source and target Session must use the same logical root")
					}
					var err error
					source, err = enginesession.AdmitSessionResume(ctx, enginesession.ResumeAdmissionRequest{SessionID: args[0], CWD: cwd, CatalogPath: catalog, LegacyCatalogPath: legacy})
					return err
				}, func(ctx context.Context, service *engine.SessionService) (sessionAdministrationOutput, error) {
					receipt, err := service.ImportWorkspaceHistory(ctx, source, request)
					if err != nil {
						return sessionAdministrationOutput{}, fmt.Errorf("recover history: %w", err)
					}
					return sessionAdministrationOutput{text: "Workspace history imported; no agent was started", result: receipt}, nil
				})
		},
	}
	command.Flags().StringVar(&sourceSHA, "source-sha256", "", "Externally retained canonical source proof digest")
	command.Flags().StringVar(&migrationID, "migration-id", "", "Fresh operator migration UUID, retained for exact recovery")
	command.Flags().StringVar(&bindingSHA, "binding-sha256", "", "Externally anchored admitted target generation digest")
	return command
}
