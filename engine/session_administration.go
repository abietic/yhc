package engine

import (
	"time"

	"github.com/abietic/yhc/engine/commands"
)

// SessionAdministrationConfig identifies the local stores used by one
// provider-free CLI administration process.
type SessionAdministrationConfig struct {
	CWD                      string
	TranscriptDir            string
	SessionCatalogPath       string
	LegacySessionCatalogPath string
	Clock                    func() time.Time
}

// NewSessionAdministrationEngine constructs a lightweight QueryEngine host for
// the canonical SessionService. It initializes no model, MCP connection,
// plugin generation, shell hook generation, or long-session service. Closing
// the host also skips the ordinary active-session checkpoint so a read-only
// administration command cannot create a synthetic transcript.
func NewSessionAdministrationEngine(config SessionAdministrationConfig) *QueryEngine {
	return newQueryEngineWithOptions(QueryEngineConfig{
		CWD:                      config.CWD,
		TranscriptDir:            config.TranscriptDir,
		SessionCatalogPath:       config.SessionCatalogPath,
		LegacySessionCatalogPath: config.LegacySessionCatalogPath,
		Clock:                    config.Clock,
		CommandEntrypoint:        commands.EntrypointAdministration,
	}, queryEngineConstructionOptions{administration: true})
}
