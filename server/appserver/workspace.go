package appserver

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const defaultWorkspaceHandleTTL = 5 * time.Minute

var (
	errWorkspaceHandleMissing = errors.New("workspace_handle is required")
	errWorkspaceHandleInvalid = errors.New("workspace_handle is invalid or already used")
	errWorkspaceHandleExpired = errors.New("workspace_handle has expired")
)

type workspaceCapability struct {
	cwd       string
	label     string
	expiresAt time.Time
	reserved  bool
}

func (s *Server) handleRegisterWorkspace(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipalFromContext(r.Context())
	if !ok || !principal.bearer {
		writeError(w, http.StatusForbidden, "bearer_required", "trusted Desktop bearer authentication required")
		return
	}
	var input RegisterWorkspaceRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cwd, err := validateCWD(input.CWD)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "workspace_invalid", err.Error())
		return
	}
	now := s.now().UTC()
	capability := workspaceCapability{cwd: cwd, label: workspaceLabel(cwd), expiresAt: now.Add(s.workspaceHandleTTL)}
	handle := uuid.NewString()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "server_closing", "app-server is shutting down")
		return
	}
	s.pruneWorkspaceHandlesLocked(now)
	s.workspaceHandles[handle] = capability
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, RegisterWorkspaceResponse{WorkspaceHandle: handle, WorkspaceLabel: capability.label, ExpiresAt: capability.expiresAt})
}

func (s *Server) reserveWorkspaceHandle(value string) (string, workspaceCapability, error) {
	handle := strings.TrimSpace(value)
	if handle == "" {
		return "", workspaceCapability{}, errWorkspaceHandleMissing
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneWorkspaceHandlesLocked(now)
	capability, ok := s.workspaceHandles[handle]
	if !ok || capability.reserved {
		return "", workspaceCapability{}, errWorkspaceHandleInvalid
	}
	if !capability.expiresAt.After(now) {
		delete(s.workspaceHandles, handle)
		return "", workspaceCapability{}, errWorkspaceHandleExpired
	}
	capability.reserved = true
	s.workspaceHandles[handle] = capability
	return handle, capability, nil
}

func (s *Server) settleWorkspaceHandle(handle string, consume bool) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	capability, ok := s.workspaceHandles[handle]
	if !ok || consume || !capability.expiresAt.After(now) {
		delete(s.workspaceHandles, handle)
		return
	}
	capability.reserved = false
	s.workspaceHandles[handle] = capability
}

func (s *Server) pruneWorkspaceHandlesLocked(now time.Time) {
	for handle, capability := range s.workspaceHandles {
		if !capability.expiresAt.After(now) {
			delete(s.workspaceHandles, handle)
		}
	}
}

func workspaceLabel(cwd string) string { return baseName(cwd) }
