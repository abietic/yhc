package appserver

import (
	"context"
	"encoding/json"
	"time"
)

const (
	// ProtocolVersion is the public local app-server protocol version.
	ProtocolVersion = 2

	defaultEventBuffer = 1024
	defaultMaxSessions = 32
	maxRequestBytes    = 1 << 20
	maxPromptBytes     = 256 << 10
)

// Bootstrap is the single machine-readable line printed by `serve app`.
type Bootstrap struct {
	ProtocolVersion int    `json:"protocol_version"`
	URL             string `json:"url"`
	Token           string `json:"token"`
	PID             int    `json:"pid"`
	WebURL          string `json:"web_url,omitempty"`
}

// ErrorEnvelope is the stable error response shape.
type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

// APIError is safe to render directly in a desktop client.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ExecutionModelOption is the display-safe identity of one selectable model.
type ExecutionModelOption struct {
	Selector    string `json:"selector"`
	DisplayName string `json:"display_name"`
	Provider    string `json:"provider"`
	APIModel    string `json:"api_model"`
}

// ExecutionDispatchBlock is safe model-binding remediation state.
type ExecutionDispatchBlock struct {
	Code        string `json:"code"`
	Selector    string `json:"selector,omitempty"`
	Remediation string `json:"remediation"`
	ContextOnly bool   `json:"context_only"`
}

// ExecutionSettingsResponse is the bounded execution-control projection.
type ExecutionSettingsResponse struct {
	Model                    string                  `json:"model"`
	Models                   []ExecutionModelOption  `json:"models"`
	ReasoningEffort          string                  `json:"reasoning_effort"`
	ReasoningEffortSupported bool                    `json:"reasoning_effort_supported"`
	ReasoningEffortOptions   []string                `json:"reasoning_effort_options"`
	PermissionMode           string                  `json:"permission_mode"`
	PermissionModeOptions    []string                `json:"permission_mode_options"`
	DispatchBlock            *ExecutionDispatchBlock `json:"dispatch_block,omitempty"`
}

// UpdateExecutionSettingsRequest changes exactly one execution setting.
type UpdateExecutionSettingsRequest struct {
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	PermissionMode  *string `json:"permission_mode,omitempty"`
}

// HealthResponse describes the running protocol server.
type HealthResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	ServerID        string    `json:"server_id"`
	StartedAt       time.Time `json:"started_at"`
}

// ResumeValidator checks durable session state before any lease or engine is
// created. A failed validation must remain a read-only operation.
type ResumeValidator func(context.Context, EngineOptions) error

// CreateSessionRequest creates or resumes one engine-owned durable session.
type CreateSessionRequest struct {
	WorkspaceHandle string `json:"workspace_handle"`

	// Deprecated: in-process construction fields. They are deliberately not part
	// of the HTTP JSON contract; durable attach builds this value internally.
	SessionID string `json:"-"`
	CWD       string `json:"-"`
	Title     string `json:"-"`
	Resume    bool   `json:"-"`
}

// RegisterWorkspaceRequest is accepted only from the trusted Desktop process.
// It is the sole transport boundary that can name a local filesystem path.
type RegisterWorkspaceRequest struct {
	CWD string `json:"cwd"`
}

// RegisterWorkspaceResponse returns a short-lived opaque capability for one
// validated workspace. The renderer must pass the handle, never the path.
type RegisterWorkspaceResponse struct {
	WorkspaceHandle string    `json:"workspace_handle"`
	WorkspaceLabel  string    `json:"workspace_label"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// StartTurnRequest starts one prompt in an idle session.
type StartTurnRequest struct {
	Prompt       string `json:"prompt"`
	ClientTurnID string `json:"client_turn_id,omitempty"`
}

// StartTurnResponse acknowledges admission before model work completes.
type StartTurnResponse struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Accepted  bool   `json:"accepted"`
}

// AttachTurnRequest atomically resumes one durable session and admits its first turn.
type AttachTurnRequest struct {
	Prompt       string `json:"prompt"`
	ClientTurnID string `json:"client_turn_id"`
}

// AttachTurnResponse is the flat, tagged result of durable-session attachment.
type AttachTurnResponse struct {
	Status       string               `json:"status"`
	Session      SessionSummary       `json:"session"`
	ClientTurnID string               `json:"client_turn_id"`
	TurnID       string               `json:"turn_id,omitempty"`
	Interaction  *InteractionSnapshot `json:"interaction,omitempty"`
}

// CancelTurnRequest targets the currently active turn.
type CancelTurnRequest struct {
	TurnID string `json:"turn_id,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ResolveInteractionRequest is one typed interaction result. The handler
// accepts exactly one variant matching the frozen pending interaction.
type ResolveInteractionRequest struct {
	Kind         string                     `json:"kind"`
	Permission   *ResolvePermissionResult   `json:"permission,omitempty"`
	Question     *ResolveQuestionResult     `json:"question,omitempty"`
	PlanApproval *ResolvePlanApprovalResult `json:"plan_approval,omitempty"`
	RepeatedTool *ResolveRepeatedToolResult `json:"repeated_tool,omitempty"`
}

// ResolvePermissionResult selects one server-advertised ordinary decision.
type ResolvePermissionResult struct {
	Decision string `json:"decision"`
	Message  string `json:"message,omitempty"`
}

// ResolveQuestionResult submits answers, returns to discussion, or cancels.
type ResolveQuestionResult struct {
	Outcome string                 `json:"outcome"`
	Answers []QuestionAnswerResult `json:"answers,omitempty"`
}

// QuestionAnswerResult carries request-scoped presentation IDs only.
type QuestionAnswerResult struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	Text       string   `json:"text,omitempty"`
}

// ResolvePlanApprovalResult is one decision over an exact reviewed revision.
type ResolvePlanApprovalResult struct {
	Outcome        string `json:"outcome"`
	Revision       uint64 `json:"revision"`
	TargetMode     string `json:"target_mode"`
	ReviewedDigest string `json:"reviewed_digest"`
	Confirmed      bool   `json:"confirmed"`
	Feedback       string `json:"feedback,omitempty"`
}

// ResolveRepeatedToolResult chooses one of the two repeated-call outcomes.
type ResolveRepeatedToolResult struct {
	Outcome string `json:"outcome"`
}

// ResolveInteractionResponse reports whether this request won settlement.
type ResolveInteractionResponse struct {
	Accepted bool `json:"accepted"`
}

// SessionSummary is the bounded desktop session projection.
type SessionSummary struct {
	ID             string    `json:"id"`
	ThreadID       string    `json:"thread_id"`
	WorkspaceLabel string    `json:"workspace_label"`
	Status         string    `json:"status"`
	ActiveTurnID   string    `json:"active_turn_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastError      string    `json:"last_error,omitempty"`

	// Deprecated: in-process compatibility fields. They are never serialized.
	CWD   string `json:"-"`
	Title string `json:"-"`
}

// SessionListResponse lists only sessions owned by this app-server process.
type SessionListResponse struct {
	Sessions []SessionSummary `json:"sessions"`
}

// DurableSessionSummary is a bounded read-only catalog projection. Opening one
// still goes through normal session admission and resume validation.
type DurableSessionSummary struct {
	ID              string    `json:"id"`
	WorkspaceLabel  string    `json:"workspace_label"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
	GitBranch       string    `json:"git_branch,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	Resumable       bool      `json:"resumable"`

	// Deprecated: in-process compatibility fields. They are never serialized.
	CWD   string `json:"-"`
	Title string `json:"-"`
}

// DurableSessionListResponse is one opaque-cursor page across registered
// project-local transcript roots.
type DurableSessionListResponse struct {
	Sessions   []DurableSessionSummary `json:"sessions"`
	NextCursor string                  `json:"next_cursor,omitempty"`
	HasMore    bool                    `json:"has_more"`
	Scanned    int                     `json:"scanned"`
}

// ImportDurableSessionRequest is the explicit, stopped-producer attestation
// required before a server-discovered legacy transcript can be promoted. It
// intentionally accepts no client path, workspace, catalog, or runtime data.
type ImportDurableSessionRequest struct {
	ConfirmLegacyStopped bool `json:"confirm_legacy_stopped"`
}

// ImportDurableSessionResponse reports only the durable identity after an
// idempotent canonical promotion. Clients must discover again before attach.
type ImportDurableSessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

// ReviewDiffResponse is a point-in-time, read-only review projection for one
// live session's owned workspace.
type ReviewDiffResponse struct {
	WorkspaceLabel string             `json:"workspace_label"`
	GeneratedAt    time.Time          `json:"generated_at"`
	Sources        []ReviewDiffSource `json:"sources"`

	// Deprecated: in-process compatibility field. It is never serialized.
	CWD string `json:"-"`
}

// ReviewDiffSource is one bounded VCS diff. DiffHash and TotalBytes cover the
// returned prefix; Truncated reports that Git was stopped at the output cap.
type ReviewDiffSource struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	WorkspaceLabel string `json:"workspace_label"`
	BaseRef        string `json:"base_ref"`
	HeadRef        string `json:"head_ref"`
	Diff           string `json:"diff"`
	DiffHash       string `json:"diff_hash"`
	TotalBytes     int64  `json:"total_bytes"`
	Truncated      bool   `json:"truncated"`

	// Deprecated: in-process compatibility field. It is never serialized.
	RepositoryRoot string `json:"-"`
}

// SessionSnapshot is the stable Desktop recovery projection. It deliberately
// avoids exposing engine reducer structs as a public transport contract.
type SessionSnapshot struct {
	Session      SessionSummary        `json:"session"`
	EventCursor  uint64                `json:"event_cursor"`
	Messages     []SnapshotMessage     `json:"messages"`
	Interactions []InteractionSnapshot `json:"interactions"`
	Activity     []ActivityEntry       `json:"activity"`
}

// ActivityEntry is a bounded, display-safe operational projection. It is not
// conversation truth and deliberately contains no model, prompt, tool input,
// tool output, command, path, or free-form error text.
type ActivityEntry struct {
	ID        string    `json:"id"`
	TurnID    string    `json:"turn_id"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	Category  string    `json:"category,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// TranscriptPageResponse is one frozen, reverse-paginated durable conversation
// page. Messages are returned in chronological order within each page; clients
// prepend follow-up pages while NextCursor is non-empty.
type TranscriptPageResponse struct {
	Messages     []SnapshotMessage `json:"messages"`
	NextCursor   string            `json:"next_cursor,omitempty"`
	HasMore      bool              `json:"has_more"`
	SnapshotSize int64             `json:"snapshot_size,omitempty"`
	BytesRead    int64             `json:"bytes_read,omitempty"`
	Corruptions  int               `json:"corruptions,omitempty"`
}

// SnapshotMessage is the bounded message representation used after an event
// replay gap.
type SnapshotMessage struct {
	ID               string             `json:"id,omitempty"`
	TurnID           string             `json:"turn_id,omitempty"`
	Sequence         uint64             `json:"sequence,omitempty"`
	Role             string             `json:"role"`
	Content          string             `json:"content"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCallID       string             `json:"tool_call_id,omitempty"`
	ToolName         string             `json:"tool_name,omitempty"`
	ToolCalls        []SnapshotToolCall `json:"tool_calls,omitempty"`
	Completed        bool               `json:"completed"`
	Timestamp        time.Time          `json:"timestamp,omitempty"`
	Kind             string             `json:"kind,omitempty"`
	Source           string             `json:"source,omitempty"`
}

// SnapshotToolCall is a display-safe tool invocation summary.
type SnapshotToolCall struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name"`
	InputPreview string `json:"input_preview,omitempty"`
	Input        string `json:"input,omitempty"`
}

// BrowserPairRequest exchanges one short-lived, single-use URL-fragment token
// for an HttpOnly browser session cookie.
type BrowserPairRequest struct {
	PairingToken string `json:"pairing_token"`
}

// BrowserPairingResponse contains a newly minted one-time Web launch URL. It is
// returned only to the trusted bearer-authenticated Desktop host.
type BrowserPairingResponse struct {
	WebURL    string    `json:"web_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// BrowserSessionResponse exposes only the non-cookie CSRF proof needed by the
// same-origin Web client.
type BrowserSessionResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	CSRFToken       string    `json:"csrf_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// InteractionSnapshot is one safe unresolved interaction. A valid snapshot
// has a nonempty turn ID and exactly one variant matching Kind.
type InteractionSnapshot struct {
	RequestID    string                           `json:"request_id"`
	TurnID       string                           `json:"turn_id"`
	Kind         string                           `json:"kind"`
	Permission   *PermissionInteractionSnapshot   `json:"permission,omitempty"`
	Question     *QuestionInteractionSnapshot     `json:"question,omitempty"`
	PlanApproval *PlanApprovalInteractionSnapshot `json:"plan_approval,omitempty"`
	RepeatedTool *RepeatedToolInteractionSnapshot `json:"repeated_tool,omitempty"`
}

// PermissionInteractionSnapshot is the bounded engine-owned permission view.
type PermissionInteractionSnapshot struct {
	Available   bool                         `json:"available"`
	ToolLabel   string                       `json:"tool_label"`
	Summary     string                       `json:"summary"`
	Evidence    []PermissionEvidenceSnapshot `json:"evidence"`
	GrantScopes []string                     `json:"grant_scopes"`
}

// PermissionEvidenceSnapshot is one display-safe evidence item.
type PermissionEvidenceSnapshot struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// QuestionInteractionSnapshot contains only renderer presentation data.
type QuestionInteractionSnapshot struct {
	Questions []QuestionSnapshot `json:"questions"`
}

// QuestionSnapshot is one question with request-scoped wire IDs.
type QuestionSnapshot struct {
	ID          string                   `json:"id"`
	Header      string                   `json:"header"`
	Text        string                   `json:"text"`
	Options     []QuestionOptionSnapshot `json:"options"`
	MultiSelect bool                     `json:"multi_select"`
	FreeText    bool                     `json:"free_text"`
}

// QuestionOptionSnapshot is one option with a request-scoped wire ID.
type QuestionOptionSnapshot struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// PlanApprovalInteractionSnapshot omits Plan path and digest identity.
type PlanApprovalInteractionSnapshot struct {
	Revision        uint64   `json:"revision"`
	TargetModes     []string `json:"target_modes"`
	ReviewAvailable bool     `json:"review_available"`
}

// RepeatedToolInteractionSnapshot exposes no original tool input.
type RepeatedToolInteractionSnapshot struct {
	Attempt     int      `json:"attempt"`
	Explanation string   `json:"explanation"`
	Outcomes    []string `json:"outcomes"`
}

// PlanReviewResponse is one bounded request-owned Plan review result.
type PlanReviewResponse struct {
	Content  string `json:"content"`
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}

// WireEvent is the append-only event envelope consumed by Desktop.
type WireEvent struct {
	ProtocolVersion int             `json:"protocol_version"`
	ID              uint64          `json:"id"`
	Type            string          `json:"type"`
	SessionID       string          `json:"session_id"`
	ThreadID        string          `json:"thread_id,omitempty"`
	TurnID          string          `json:"turn_id,omitempty"`
	AgentID         string          `json:"agent_id,omitempty"`
	Sequence        uint64          `json:"engine_sequence,omitempty"`
	Timestamp       time.Time       `json:"timestamp"`
	CausationID     string          `json:"causation_id,omitempty"`
	Data            json.RawMessage `json:"data"`
}

// ReplayGap reports that a requested event cursor is no longer buffered.
type ReplayGap struct {
	Earliest uint64 `json:"earliest"`
	Latest   uint64 `json:"latest"`
}
