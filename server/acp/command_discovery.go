package acp

import (
	"context"
	"errors"
	"fmt"
	"os"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/session"
)

func (a *Agent) publishCommandSnapshot(
	ctx context.Context,
	acpSession *Session,
	force bool,
) error {
	if a == nil || acpSession == nil || acpSession.Engine == nil {
		return errors.New("ACP command snapshot session is unavailable")
	}
	if a.config.DisableACPCommandUpdates {
		return nil
	}

	acpSession.commandProjectionMu.Lock()
	defer acpSession.commandProjectionMu.Unlock()

	snapshot := acpSession.Engine.GetCommandRegistry().DiscoverySnapshotForContext(
		ctx,
		commands.EntrypointACP,
		acpSession.Engine.CommandContext(),
	)
	if !force &&
		acpSession.commandSnapshotWasDelivered &&
		acpSession.commandDigest == snapshot.Digest {
		return nil
	}
	if a.conn == nil {
		// Direct in-process callers have no notification transport. Do not
		// advance delivered state so the next real protocol boundary retries.
		return nil
	}

	rows := make([]acpsdk.AvailableCommand, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		projected := acpsdk.AvailableCommand{
			Name:        row.Name,
			Description: row.Description,
		}
		if row.Input != nil {
			projected.Input = &acpsdk.AvailableCommandInput{
				Unstructured: &acpsdk.UnstructuredCommandInput{
					Hint: row.Input.Hint,
				},
			}
		}
		rows = append(rows, projected)
	}
	if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
		SessionId: acpSession.ID,
		Update: acpsdk.SessionUpdate{
			AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
				AvailableCommands: rows,
			},
		},
	}); err != nil {
		return fmt.Errorf("ACP available commands delivery failed: %w", err)
	}
	acpSession.commandDigest = snapshot.Digest
	acpSession.commandSnapshotWasDelivered = true
	return nil
}

func (a *Agent) unregisterAndCloseSession(acpSession *Session) {
	if a == nil || acpSession == nil {
		return
	}
	a.mu.Lock()
	if a.sessions[acpSession.ID] == acpSession {
		delete(a.sessions, acpSession.ID)
	}
	a.mu.Unlock()
	acpSession.close()
}

func (a *Agent) cleanupFailedNewSession(acpSession *Session) error {
	if acpSession == nil {
		return nil
	}
	a.unregisterAndCloseSession(acpSession)
	transcriptDir := acpSessionTranscriptDir(acpSession.CWD)
	if _, err := session.DeleteSession(session.DeleteOptions{
		SessionID: string(acpSession.ID),
		Dir:       transcriptDir,
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove failed new session artifacts: %w", err)
	}
	return nil
}
