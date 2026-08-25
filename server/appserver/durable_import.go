package appserver

import (
	"errors"
	"net/http"
	"strings"

	enginesession "github.com/abietic/yhc/engine/session"
)

// handleImportDurableSession is the only Desktop transport boundary that may
// promote a discovered legacy transcript. The request carries an attestation,
// never physical source selection; discovery and provenance remain server-side.
func (s *Server) handleImportDurableSession(w http.ResponseWriter, r *http.Request) {
	var input ImportDurableSessionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !input.ConfirmLegacyStopped {
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"legacy_stopped_attestation_required",
			"confirm_legacy_stopped must be true before importing a legacy session",
		)
		return
	}

	s.mu.RLock()
	closing := s.closing
	s.mu.RUnlock()
	if closing {
		writeError(w, http.StatusServiceUnavailable, "server_closing", "app-server is shutting down")
		return
	}

	info, err := s.resolveDurableSession(r.PathValue("session_id"))
	if err != nil {
		s.writeDurableResolveError(w, err)
		return
	}
	if err := enginesession.RequireCanonicalSession(*info); err == nil {
		writeError(
			w,
			http.StatusConflict,
			"legacy_import_not_required",
			"session is already canonical and cannot be imported again",
		)
		return
	} else if !errors.Is(err, enginesession.ErrLegacySessionImportRequired) {
		writeError(w, http.StatusConflict, "legacy_import_rejected", "session cannot be imported")
		return
	}

	userRoots, err := enginesession.DefaultSessionImportUserRoots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "legacy_import_failed", "could not prepare session import")
		return
	}
	admitted, err := enginesession.ImportDiscoveredLegacySession(
		r.Context(),
		enginesession.ImportDiscoveredLegacySessionRequest{
			Info:                 *info,
			CatalogPath:          s.sessionCatalogPath,
			LegacyCatalogPath:    s.legacySessionCatalogPath(),
			UserRoots:            userRoots,
			ConfirmLegacyStopped: true,
			Now:                  s.now().UTC(),
		},
	)
	if err != nil {
		switch {
		case errors.Is(err, enginesession.ErrSessionImportAttestationRequired):
			writeError(w, http.StatusUnprocessableEntity, "legacy_stopped_attestation_required", "legacy producer stopped attestation is required")
		case errors.Is(err, enginesession.ErrSessionImportUnsafe):
			writeError(w, http.StatusConflict, "legacy_import_rejected", "legacy session import was rejected")
		default:
			writeError(w, http.StatusInternalServerError, "legacy_import_failed", "could not import legacy session")
		}
		return
	}
	if strings.TrimSpace(admitted.SessionID) == "" {
		writeError(w, http.StatusInternalServerError, "legacy_import_failed", "import returned no session identity")
		return
	}
	writeJSON(w, http.StatusOK, ImportDurableSessionResponse{
		SessionID: admitted.SessionID,
		Status:    "imported",
	})
}

func (s *Server) writeDurableResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDurableSessionNotFound):
		writeError(w, http.StatusNotFound, "durable_session_not_found", "durable session not found")
	case errors.Is(err, errDurableSessionAmbiguous):
		writeError(w, http.StatusConflict, "durable_session_ambiguous", "durable session is ambiguous")
	case errors.Is(err, errDurableServerClosing):
		writeError(w, http.StatusServiceUnavailable, "server_closing", "app-server is shutting down")
	default:
		writeError(w, http.StatusInternalServerError, "durable_sessions_failed", "could not inspect durable sessions")
	}
}
