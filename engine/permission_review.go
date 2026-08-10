package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/permission"
)

const (
	defaultApprovalReviewTimeout = 8 * time.Second
	maxPermissionReviewSeen      = 1024
	maxPermissionReviewPending   = 64
	maxReviewProjectionDepth     = 8
	maxReviewProjectionItems     = 128
	maxReviewProjectionBytes     = 32 * 1024
	maxReviewIntentRecords       = 3
	reviewUserIntentMarker       = "direct_user_submission"
)

type PermissionReviewPhase string

const (
	PermissionReviewChecking    PermissionReviewPhase = "checking"
	PermissionReviewCompleted   PermissionReviewPhase = "completed"
	PermissionReviewUnavailable PermissionReviewPhase = "unavailable"
)

// PermissionReviewEvent is an intentionally opaque, non-authoritative
// lifecycle projection. It never contains action arguments, host paths,
// policy bytes, a binding nonce, an action digest, or reviewer rationale.
type PermissionReviewEvent struct {
	Phase         PermissionReviewPhase
	RequestID     string
	ToolUseID     string
	CanonicalTool string
	ActionKind    string
	Decision      string
	ReasonCode    string
	Provider      string
	Model         string
	DataBoundary  string
	LatencyMS     int64
	UpdatedAt     time.Time
}

type permissionReviewEmitterKey struct{}

func withPermissionReviewEmitter(
	ctx context.Context,
	emit func(QueryEvent),
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, permissionReviewEmitterKey{}, emit)
}

func emitPermissionReview(ctx context.Context, event PermissionReviewEvent) {
	if ctx == nil {
		return
	}
	emit, _ := ctx.Value(permissionReviewEmitterKey{}).(func(QueryEvent))
	if emit != nil {
		emit(QueryEvent{
			Type:             EventPermissionReview,
			PermissionReview: &event,
		})
	}
}

type permissionReviewAuditRefKey struct{}

// permissionReviewAuditRef is process-local correlation only. Its EventID is
// the sole persisted identity; the action remains host-local for exact
// same-action comparison checks.
type permissionReviewAuditRef struct {
	EventID     string
	Action      PermissionActionDescriptor
	ToolContext *ToolUseContext
}

func withPermissionReviewAuditRef(
	ctx context.Context,
	ref *permissionReviewAuditRef,
) context.Context {
	if ref == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, permissionReviewAuditRefKey{}, ref)
}

func permissionReviewAuditRefFromContext(
	ctx context.Context,
) *permissionReviewAuditRef {
	if ctx == nil {
		return nil
	}
	ref, _ := ctx.Value(permissionReviewAuditRefKey{}).(*permissionReviewAuditRef)
	return ref
}

type pendingPermissionReview struct {
	request      permission.PermissionReviewRequest
	action       PermissionActionDescriptor
	actionDigest [sha256.Size]byte
	toolContext  *ToolUseContext
	route        permission.ApprovalReviewerRoute
	auditRef     *permissionReviewAuditRef
	cancel       context.CancelFunc
	startedAt    time.Time
}

func (e *QueryEngine) launchPermissionReview(
	ctx context.Context,
	action PermissionActionDescriptor,
	toolCtx *ToolUseContext,
) *permissionReviewAuditRef {
	if e == nil ||
		!e.config.ApprovalReviewShadow ||
		e.config.ApprovalReviewer == nil ||
		!permission.IsSafeReviewRoute(e.config.ApprovalReviewerRoute) ||
		e.goalProviderUsageRequired() ||
		action.Mode != permission.ModeAuto ||
		action.AgentID != "" ||
		projectGraphHITLProbeFromContext(ctx) != nil {
		return nil
	}
	toolUseID := strings.TrimSpace(currentToolUseID(ctx))
	if toolUseID == "" {
		return nil
	}
	action = clonePermissionActionDescriptor(action)
	actionDigest, err := permissionReviewActionDigest(action)
	if err != nil {
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			"",
			"projection_unavailable",
			nil,
		)
		return nil
	}
	seenKey := permissionReviewSeenKey(toolUseID, actionDigest)
	e.permissionReviewMu.Lock()
	if e.permissionReviewClosed {
		e.permissionReviewMu.Unlock()
		return nil
	}
	if _, seen := e.permissionReviewSeen[seenKey]; seen {
		e.permissionReviewMu.Unlock()
		return nil
	}
	e.rememberPermissionReviewLocked(seenKey)
	e.permissionReviewMu.Unlock()

	var auditRef *permissionReviewAuditRef
	if e.permissionReviewAudit != nil {
		eventID, auditErr := randomReviewOpaqueID(16)
		if auditErr == nil {
			auditRef = &permissionReviewAuditRef{
				EventID:     eventID,
				Action:      clonePermissionActionDescriptor(action),
				ToolContext: clonePermissionReviewToolContext(toolCtx),
			}
			e.recordPermissionReviewAudit(ctx, permission.ReviewAuditRecord{
				EventID:            eventID,
				Kind:               permission.ReviewAuditKindEligible,
				CanonicalTool:      action.CanonicalToolName,
				ActionKind:         string(action.ActionKind),
				DeterministicClass: "review",
			})
		}
	}

	requestID, err := randomReviewOpaqueID(16)
	if err != nil {
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			"",
			"identity_unavailable",
			auditRef,
		)
		return auditRef
	}
	nonce, err := randomReviewOpaqueID(32)
	if err != nil {
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			requestID,
			"identity_unavailable",
			auditRef,
		)
		return auditRef
	}
	projection, err := buildPermissionReviewProjection(
		action,
		e.permissionReviewUserIntentSnapshot(),
	)
	if err != nil {
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			requestID,
			"projection_unavailable",
			auditRef,
		)
		return auditRef
	}
	request := permission.PermissionReviewRequest{
		SchemaVersion: permission.PermissionReviewSchemaVersion,
		RequestID:     requestID,
		ToolCallID:    toolUseID,
		BindingNonce:  nonce,
		Projection:    projection,
	}
	if err := permission.ValidatePermissionReviewRequest(request); err != nil {
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			requestID,
			"projection_unavailable",
			auditRef,
		)
		return auditRef
	}
	timeout := e.config.ApprovalReviewTimeout
	if timeout <= 0 {
		timeout = defaultApprovalReviewTimeout
	}
	reviewCtx, cancel := context.WithTimeout(ctx, timeout)
	startedAt := e.permissionReviewNow()
	pending := pendingPermissionReview{
		request:      request,
		action:       action,
		actionDigest: actionDigest,
		toolContext:  clonePermissionReviewToolContext(toolCtx),
		route:        e.config.ApprovalReviewerRoute,
		auditRef:     auditRef,
		cancel:       cancel,
		startedAt:    startedAt,
	}
	e.permissionReviewMu.Lock()
	if e.permissionReviewClosed {
		e.permissionReviewMu.Unlock()
		cancel()
		e.recordPermissionReviewTerminal(
			ctx,
			auditRef,
			PermissionReviewUnavailable,
			"",
			"cancelled",
			0,
		)
		return auditRef
	}
	if len(e.permissionReviewPending) >= maxPermissionReviewPending {
		e.permissionReviewMu.Unlock()
		cancel()
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			requestID,
			"capacity_exceeded",
			auditRef,
		)
		return auditRef
	}
	if _, collision := e.permissionReviewPending[request.RequestID]; collision {
		e.permissionReviewMu.Unlock()
		cancel()
		e.emitPermissionReviewUnavailable(
			ctx,
			action,
			toolUseID,
			requestID,
			"identity_unavailable",
			auditRef,
		)
		return auditRef
	}
	e.permissionReviewPending[request.RequestID] = pending
	e.permissionReviewWG.Add(1)
	e.permissionReviewMu.Unlock()
	emitPermissionReview(ctx, pending.permissionReviewEvent(
		PermissionReviewChecking,
		"",
		"",
		startedAt,
	))
	go e.runPermissionReview(ctx, reviewCtx, pending)
	return auditRef
}

func (e *QueryEngine) runPermissionReview(
	eventCtx context.Context,
	reviewCtx context.Context,
	pending pendingPermissionReview,
) {
	defer e.permissionReviewWG.Done()
	if pending.auditRef != nil {
		e.recordPermissionReviewAudit(eventCtx, permission.ReviewAuditRecord{
			EventID:      pending.auditRef.EventID,
			Kind:         permission.ReviewAuditKindAttempt,
			Provider:     pending.route.Provider,
			Model:        pending.route.Model,
			DataBoundary: pending.route.DataBoundary,
		})
	}
	result, reviewErr := e.config.ApprovalReviewer.Review(
		reviewCtx,
		pending.request,
	)
	event := e.settlePermissionReview(reviewCtx, pending.request.RequestID, result, reviewErr)
	if event != nil {
		emitPermissionReview(eventCtx, *event)
	}
}

func (e *QueryEngine) settlePermissionReview(
	reviewCtx context.Context,
	requestID string,
	result permission.PermissionReviewResult,
	reviewErr error,
) *PermissionReviewEvent {
	pending, active := e.claimPendingPermissionReview(requestID)
	if !active {
		return nil
	}
	defer pending.cancel()
	finishedAt := e.permissionReviewNow()
	latency := finishedAt.Sub(pending.startedAt)
	if latency < 0 {
		latency = 0
	}
	unavailable := func(reason string) *PermissionReviewEvent {
		event := pending.permissionReviewEvent(
			PermissionReviewUnavailable,
			"",
			reason,
			finishedAt,
		)
		event.LatencyMS = latency.Milliseconds()
		e.recordPermissionReviewTerminal(
			reviewCtx,
			pending.auditRef,
			PermissionReviewUnavailable,
			"",
			reason,
			event.LatencyMS,
		)
		return &event
	}
	if reviewCtx == nil {
		return unavailable("cancelled")
	}
	if ctxErr := reviewCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return unavailable("timeout")
		}
		return unavailable("cancelled")
	}
	if reviewErr != nil {
		return unavailable("reviewer_unavailable")
	}
	if err := permission.ValidatePermissionReviewResult(
		pending.request,
		result,
	); err != nil {
		return unavailable("invalid_result")
	}
	current, err := e.buildPermissionActionDescriptor(
		pending.action.RequestedToolName,
		pending.action.Input,
		pending.toolContext,
	)
	if err != nil || !samePermissionActionBinding(pending.action, current) {
		return unavailable("binding_changed")
	}
	currentDigest, err := permissionReviewActionDigest(current)
	if err != nil || currentDigest != pending.actionDigest {
		return unavailable("binding_changed")
	}
	event := pending.permissionReviewEvent(
		PermissionReviewCompleted,
		result.Decision,
		result.ReasonCode,
		finishedAt,
	)
	event.LatencyMS = latency.Milliseconds()
	e.recordPermissionReviewTerminal(
		reviewCtx,
		pending.auditRef,
		PermissionReviewCompleted,
		result.Decision,
		result.ReasonCode,
		event.LatencyMS,
	)
	return &event
}

func (e *QueryEngine) claimPendingPermissionReview(
	requestID string,
) (pendingPermissionReview, bool) {
	e.permissionReviewMu.Lock()
	defer e.permissionReviewMu.Unlock()
	pending, active := e.permissionReviewPending[requestID]
	if active {
		delete(e.permissionReviewPending, requestID)
	}
	return pending, active
}

func (e *QueryEngine) cancelPermissionReviews() {
	e.permissionReviewMu.Lock()
	e.permissionReviewClosed = true
	cancels := make([]context.CancelFunc, 0, len(e.permissionReviewPending))
	for _, pending := range e.permissionReviewPending {
		cancels = append(cancels, pending.cancel)
	}
	e.permissionReviewMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	e.permissionReviewWG.Wait()
	e.permissionReviewMu.Lock()
	clear(e.permissionReviewPending)
	e.permissionReviewMu.Unlock()
}

func (e *QueryEngine) rememberPermissionReviewLocked(key string) {
	if e.permissionReviewSeen == nil {
		e.permissionReviewSeen = make(map[string]struct{})
	}
	if len(e.permissionReviewSeenOrder) >= maxPermissionReviewSeen {
		oldest := e.permissionReviewSeenOrder[0]
		e.permissionReviewSeenOrder = e.permissionReviewSeenOrder[1:]
		delete(e.permissionReviewSeen, oldest)
	}
	e.permissionReviewSeen[key] = struct{}{}
	e.permissionReviewSeenOrder = append(e.permissionReviewSeenOrder, key)
}

func (e *QueryEngine) emitPermissionReviewUnavailable(
	ctx context.Context,
	action PermissionActionDescriptor,
	toolUseID string,
	requestID string,
	reason string,
	auditRef *permissionReviewAuditRef,
) {
	e.recordPermissionReviewTerminal(
		ctx,
		auditRef,
		PermissionReviewUnavailable,
		"",
		reason,
		0,
	)
	emitPermissionReview(ctx, PermissionReviewEvent{
		Phase:         PermissionReviewUnavailable,
		RequestID:     requestID,
		ToolUseID:     toolUseID,
		CanonicalTool: action.CanonicalToolName,
		ActionKind:    string(action.ActionKind),
		ReasonCode:    reason,
		Provider:      e.config.ApprovalReviewerRoute.Provider,
		Model:         e.config.ApprovalReviewerRoute.Model,
		DataBoundary:  e.config.ApprovalReviewerRoute.DataBoundary,
		LatencyMS:     0,
		UpdatedAt:     e.permissionReviewNow(),
	})
}

func (p pendingPermissionReview) permissionReviewEvent(
	phase PermissionReviewPhase,
	decision string,
	reason string,
	updatedAt time.Time,
) PermissionReviewEvent {
	return PermissionReviewEvent{
		Phase:         phase,
		RequestID:     p.request.RequestID,
		ToolUseID:     p.request.ToolCallID,
		CanonicalTool: p.action.CanonicalToolName,
		ActionKind:    string(p.action.ActionKind),
		Decision:      decision,
		ReasonCode:    reason,
		Provider:      p.route.Provider,
		Model:         p.route.Model,
		DataBoundary:  p.route.DataBoundary,
		UpdatedAt:     updatedAt,
	}
}

// recordPermissionReviewAudit is deliberately non-authoritative. Queue
// pressure, sink latency, errors, and panics can never change a permission
// decision, prompt, grant, or reviewer lifecycle.
func (e *QueryEngine) recordPermissionReviewAudit(
	_ context.Context,
	record permission.ReviewAuditRecord,
) {
	if e == nil || e.permissionReviewAudit == nil {
		return
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = permission.ReviewAuditSchemaVersion
	}
	record.OccurredAt = e.permissionReviewNow()
	e.permissionReviewAudit.Enqueue(record)
}

func (e *QueryEngine) recordPermissionReviewTerminal(
	ctx context.Context,
	ref *permissionReviewAuditRef,
	phase PermissionReviewPhase,
	decision string,
	reason string,
	latencyMS int64,
) {
	if ref == nil {
		return
	}
	e.recordPermissionReviewAudit(ctx, permission.ReviewAuditRecord{
		EventID:          ref.EventID,
		Kind:             permission.ReviewAuditKindTerminal,
		ReviewerStatus:   string(phase),
		ReviewerDecision: decision,
		ReasonCode:       reason,
		LatencyMS:        latencyMS,
	})
}

func (e *QueryEngine) recordPermissionReviewComparison(
	ctx context.Context,
	ref *permissionReviewAuditRef,
	source string,
	expected string,
) {
	if ref == nil {
		return
	}
	e.recordPermissionReviewAudit(ctx, permission.ReviewAuditRecord{
		EventID:          ref.EventID,
		Kind:             permission.ReviewAuditKindComparison,
		ComparisonSource: source,
		ExpectedDecision: expected,
	})
}

func (e *QueryEngine) recordPermissionReviewHumanComparison(
	ctx context.Context,
	action PermissionActionDescriptor,
	result PermissionInteractionResult,
) {
	ref := permissionReviewAuditRefFromContext(ctx)
	if ref == nil ||
		result.settlementSource != "adapter" ||
		!result.submittedDecisionCaptured ||
		!samePermissionActionBinding(ref.Action, action) {
		return
	}

	var expected string
	switch result.submittedDecision {
	case PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways:
		if !result.Allowed() || result.settledAction == nil ||
			!samePermissionReviewAuditAction(ref.Action, *result.settledAction) {
			return
		}
		expected = "allow"
	case PermissionDeny:
		if result.Decision != PermissionDeny || result.UpdatedInput != nil {
			return
		}
		current, err := e.buildPermissionActionDescriptor(
			action.RequestedToolName,
			action.Input,
			ref.ToolContext,
		)
		if err != nil || !samePermissionActionBinding(ref.Action, current) {
			return
		}
		expected = "deny"
	default:
		return
	}
	e.recordPermissionReviewComparison(ctx, ref, "human", expected)
}

func samePermissionReviewAuditAction(
	reviewed PermissionActionDescriptor,
	settled PermissionActionDescriptor,
) bool {
	// A session/always grant can legitimately advance only the effective
	// policy snapshot during settlement. Every action/input/capability field
	// must still match the reviewer-bound action.
	settled.PolicySnapshotID = reviewed.PolicySnapshotID
	return samePermissionActionBinding(reviewed, settled)
}

func (e *QueryEngine) permissionReviewNow() time.Time {
	if e != nil && e.config.Clock != nil {
		return e.config.Clock().UTC()
	}
	return time.Now().UTC()
}

func permissionReviewActionDigest(
	action PermissionActionDescriptor,
) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(struct {
		SchemaVersion uint16
		Action        PermissionActionDescriptor
	}{
		SchemaVersion: permission.PermissionReviewSchemaVersion,
		Action:        action,
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func permissionReviewSeenKey(
	toolUseID string,
	digest [sha256.Size]byte,
) string {
	return toolUseID + ":" + hex.EncodeToString(digest[:])
}

func randomReviewOpaqueID(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func clonePermissionReviewToolContext(toolCtx *ToolUseContext) *ToolUseContext {
	if toolCtx == nil {
		return nil
	}
	cloned := &ToolUseContext{
		AgentID:   toolCtx.AgentID,
		SessionID: toolCtx.SessionID,
		ThreadID:  toolCtx.ThreadID,
		PlanMode:  toolCtx.PlanMode,
	}
	if toolCtx.Options != nil {
		options := *toolCtx.Options
		cloned.Options = &options
	}
	return cloned
}

func buildPermissionReviewProjection(
	action PermissionActionDescriptor,
	userIntent []string,
) (permission.PermissionReviewProjection, error) {
	state := reviewProjectionState{
		roots: action.WorkingRoots,
		cwd:   action.CWD,
	}
	redacted, err := state.redact(action.Input, "", 0)
	if err != nil {
		return permission.PermissionReviewProjection{}, err
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return permission.PermissionReviewProjection{}, err
	}
	if len(encoded) > maxReviewProjectionBytes {
		return permission.PermissionReviewProjection{}, fmt.Errorf(
			"permission review projection exceeds %d bytes",
			maxReviewProjectionBytes,
		)
	}
	projection := permission.PermissionReviewProjection{
		CanonicalTool: action.CanonicalToolName,
		ActionKind:    string(action.ActionKind),
		RedactedArgs:  encoded,
		RootFacts:     permissionReviewRootFacts(action),
		RiskFacts:     permissionReviewRiskFacts(action),
		TrustedIntent: permissionReviewTrustedIntent(userIntent),
	}
	return projection, nil
}

type reviewProjectionState struct {
	items int
	roots []string
	cwd   string
}

func (s *reviewProjectionState) redact(
	value any,
	key string,
	depth int,
) (any, error) {
	if depth > maxReviewProjectionDepth {
		return nil, fmt.Errorf("permission review projection is too deep")
	}
	s.items++
	if s.items > maxReviewProjectionItems {
		return nil, fmt.Errorf("permission review projection has too many items")
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(keys))
		for index, childKey := range keys {
			projectedKey := safeReviewFieldName(childKey, index)
			if isReviewSecretKey(childKey) {
				result[projectedKey] = map[string]any{
					"kind": "redacted_secret",
				}
				continue
			}
			child, err := s.redact(typed[childKey], childKey, depth+1)
			if err != nil {
				return nil, err
			}
			result[projectedKey] = child
		}
		return result, nil
	case []any:
		if len(typed) > maxReviewProjectionItems {
			return nil, fmt.Errorf("permission review projection array is too large")
		}
		result := make([]any, len(typed))
		for index := range typed {
			child, err := s.redact(typed[index], key, depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = child
		}
		return result, nil
	case string:
		if !utf8.ValidString(typed) {
			return nil, fmt.Errorf("permission review string is not UTF-8")
		}
		if isReviewPathKey(key) || filepath.IsAbs(typed) {
			label, boundary := permissionReviewPathLabel(
				typed,
				s.cwd,
				s.roots,
			)
			return map[string]any{
				"kind":     "path",
				"label":    label,
				"boundary": boundary,
			}, nil
		}
		return map[string]any{
			"kind":  "text",
			"bytes": len(typed),
		}, nil
	case bool, float64, nil:
		return typed, nil
	case json.Number:
		return typed, nil
	default:
		return nil, fmt.Errorf(
			"permission review projection contains unsupported %T",
			value,
		)
	}
}

func permissionReviewRootFacts(
	action PermissionActionDescriptor,
) []permission.RootFact {
	facts := make([]permission.RootFact, 0, len(action.WorkingRoots)+2)
	for index := range action.WorkingRoots {
		facts = append(facts, permission.RootFact{
			Kind:     "working_root",
			Label:    "root-" + strconv.Itoa(index),
			Boundary: "trusted_host_root",
		})
	}
	cwdLabel, cwdBoundary := permissionReviewPathLabel(
		action.CWD,
		action.CWD,
		action.WorkingRoots,
	)
	facts = append(facts, permission.RootFact{
		Kind:     "cwd",
		Label:    cwdLabel,
		Boundary: cwdBoundary,
	})
	if action.Path.Logical != "" {
		pathLabel, pathBoundary := permissionReviewPathLabel(
			action.Path.Logical,
			action.CWD,
			action.WorkingRoots,
		)
		facts = append(facts, permission.RootFact{
			Kind:     "requested_path",
			Label:    pathLabel,
			Boundary: pathBoundary,
		})
	}
	return facts
}

func permissionReviewRiskFacts(
	action PermissionActionDescriptor,
) []permission.RiskFact {
	values := map[string]string{
		"capabilities_declared":      strconv.FormatBool(action.CapabilitiesDeclared),
		"child":                      strconv.FormatBool(action.Child),
		"custom_validation_complete": strconv.FormatBool(action.CustomValidationComplete),
		"destructive":                strconv.FormatBool(action.Destructive),
		"dynamic":                    strconv.FormatBool(action.Dynamic),
		"enabled":                    strconv.FormatBool(action.Enabled),
		"network":                    strconv.FormatBool(action.Network),
		"path_within_roots":          strconv.FormatBool(action.PathWithinRoots),
		"read_only":                  strconv.FormatBool(action.ReadOnly),
		"registered":                 strconv.FormatBool(action.Registered),
		"requires_user_interaction":  strconv.FormatBool(action.RequiresUserInteraction),
		"schema_validated":           strconv.FormatBool(action.SchemaValidated),
		"selected":                   strconv.FormatBool(action.Selected),
		"shell_complete":             strconv.FormatBool(action.ShellComplete),
		"write":                      strconv.FormatBool(action.Write),
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	facts := make([]permission.RiskFact, 0, len(names))
	for _, name := range names {
		facts = append(facts, permission.RiskFact{
			Name:  name,
			Value: values[name],
		})
	}
	return facts
}

func permissionReviewTrustedIntent(
	userIntent []string,
) []permission.IntentRecord {
	retained := make([]string, 0, len(userIntent))
	for _, raw := range userIntent {
		if strings.TrimSpace(raw) != "" {
			retained = append(retained, raw)
		}
	}
	if len(retained) > maxReviewIntentRecords {
		retained = retained[len(retained)-maxReviewIntentRecords:]
	}
	records := make([]permission.IntentRecord, 0, len(retained))
	for range retained {
		records = append(records, permission.IntentRecord{
			Kind:    "direct_user",
			Content: reviewUserIntentMarker,
		})
	}
	return records
}

func (e *QueryEngine) recordPermissionReviewUserIntent(content string) {
	if e == nil || !e.config.ApprovalReviewShadow || e.config.AgentID != "" {
		return
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	e.permissionReviewMu.Lock()
	defer e.permissionReviewMu.Unlock()
	e.permissionReviewUserIntent = append(
		e.permissionReviewUserIntent,
		reviewUserIntentMarker,
	)
	if len(e.permissionReviewUserIntent) > maxReviewIntentRecords {
		e.permissionReviewUserIntent = append(
			[]string(nil),
			e.permissionReviewUserIntent[len(e.permissionReviewUserIntent)-maxReviewIntentRecords:]...,
		)
	}
}

func (e *QueryEngine) permissionReviewUserIntentSnapshot() []string {
	if e == nil {
		return nil
	}
	e.permissionReviewMu.Lock()
	defer e.permissionReviewMu.Unlock()
	return append([]string(nil), e.permissionReviewUserIntent...)
}

func permissionReviewPathLabel(
	value string,
	cwd string,
	roots []string,
) (string, string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "none", "absent"
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cwd, candidate)
	}
	candidate = filepath.Clean(candidate)
	for index, root := range roots {
		root = filepath.Clean(root)
		relative, err := filepath.Rel(root, candidate)
		if err != nil || permissionReviewRelativeEscapesRoot(relative) {
			continue
		}
		label := "root-" + strconv.Itoa(index)
		if relative != "." {
			label += "/" + safeReviewRelativePath(relative)
		}
		return label, "inside"
	}
	return "outside-root", "outside"
}

func permissionReviewRelativeEscapesRoot(relative string) bool {
	return relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative)
}

func safeReviewRelativePath(value string) string {
	parts := strings.Split(filepath.ToSlash(value), "/")
	for index, part := range parts {
		if part == ".." {
			parts[index] = "parent-boundary"
			continue
		}
		if !isSafeReviewToken(part) || len(part) > 64 {
			parts[index] = "segment"
		}
	}
	return strings.Join(parts, "/")
}

func safeReviewFieldName(value string, index int) string {
	if isSafeReviewToken(value) && len(value) <= 64 {
		return value
	}
	return "field_" + strconv.Itoa(index)
}

func isSafeReviewToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func isReviewSecretKey(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"secret",
		"token",
		"password",
		"credential",
		"authorization",
		"cookie",
		"api_key",
		"apikey",
		"private_key",
		"access_key",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return lower == "key"
}

func isReviewPathKey(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "path") ||
		strings.Contains(lower, "file") ||
		strings.Contains(lower, "directory") ||
		strings.Contains(lower, "dir") ||
		lower == "cwd" ||
		strings.Contains(lower, "root")
}
