package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
)

const (
	acpGoalProtocolVersion     = 1
	acpGoalMaxProtocolVersion  = 65535
	acpGoalMaxOfferedVersions  = 8
	acpGoalMaxParamsBytes      = 32 * 1024
	acpGoalMaxSessionIDBytes   = 256
	acpGoalMaxRequestIDBytes   = 128
	acpGoalMaxGoalIDBytes      = 256
	acpGoalMaxObjectiveScalars = 4000
)

type acpGoalNamespace struct {
	capabilityKey  string
	getMethod      string
	controlMethod  string
	continueMethod string
	updatedMethod  string
	version        int
}

var canonicalACPGoalNamespace = acpGoalNamespace{
	capabilityKey:  "yhc.goal",
	getMethod:      "_yhc/goal/get",
	controlMethod:  "_yhc/goal/control",
	continueMethod: "_yhc/goal/continue",
	updatedMethod:  "_yhc/goal/updated",
	version:        acpGoalProtocolVersion,
}

var legacyACPGoalNamespace = acpGoalNamespace{
	capabilityKey:  "eino-agent.goal",
	getMethod:      "_eino/goal/get",
	controlMethod:  "_eino/goal/control",
	continueMethod: "_eino/goal/continue",
	updatedMethod:  "_eino/goal/updated",
	version:        acpGoalProtocolVersion,
}

type acpGoalNegotiation struct {
	namespace *acpGoalNamespace
	offered   bool
}

type parsedACPGoalOffer struct {
	offered   bool
	valid     bool
	supported bool
	versions  []int
}

type acpGoalEnvelope struct {
	SchemaVersion int    `json:"schemaVersion"`
	SessionID     string `json:"sessionId"`
	RequestID     string `json:"requestId"`
}

type acpGoalControlParams struct {
	SchemaVersion    int     `json:"schemaVersion"`
	SessionID        string  `json:"sessionId"`
	RequestID        string  `json:"requestId"`
	Operation        string  `json:"operation"`
	ExpectedGoalID   string  `json:"expectedGoalId,omitempty"`
	ExpectedRevision uint64  `json:"expectedRevision"`
	Objective        *string `json:"objective,omitempty"`
	TokenBudget      *uint64 `json:"tokenBudget,omitempty"`
}

type acpGoalContinueParams struct {
	SchemaVersion               int    `json:"schemaVersion"`
	SessionID                   string `json:"sessionId"`
	RequestID                   string `json:"requestId"`
	ExpectedGoalID              string `json:"expectedGoalId"`
	ExpectedRevision            uint64 `json:"expectedRevision"`
	ExpectedObjectiveRevision   uint64 `json:"expectedObjectiveRevision"`
	ExpectedContinuationOrdinal uint64 `json:"expectedContinuationOrdinal"`
}

type acpGoalSnapshot struct {
	GoalID                           string   `json:"goalId"`
	Objective                        string   `json:"objective"`
	ObjectiveRevision                uint64   `json:"objectiveRevision"`
	Status                           string   `json:"status"`
	StatusReasonCode                 string   `json:"statusReasonCode,omitempty"`
	StatusReason                     string   `json:"statusReason,omitempty"`
	Revision                         uint64   `json:"revision"`
	TokenBudget                      *uint64  `json:"tokenBudget"`
	TokensUsed                       uint64   `json:"tokensUsed"`
	TokensRemaining                  *uint64  `json:"tokensRemaining"`
	UsageLedgerRevision              uint64   `json:"usageLedgerRevision"`
	UsageCoverage                    string   `json:"usageCoverage"`
	RootActiveTimeMillis             int64    `json:"rootActiveTimeMillis"`
	ContinuationOrdinal              uint64   `json:"continuationOrdinal"`
	LastGoalTurnID                   string   `json:"lastGoalTurnId,omitempty"`
	LastTerminalSequence             uint64   `json:"lastTerminalSequence"`
	PendingCompleteTurnID            string   `json:"pendingCompleteTurnId,omitempty"`
	PendingCompleteObjectiveRevision uint64   `json:"pendingCompleteObjectiveRevision"`
	BlockerKey                       string   `json:"blockerKey,omitempty"`
	BlockerTurnIDs                   []string `json:"blockerTurnIds"`
	CreatedAt                        string   `json:"createdAt"`
	UpdatedAt                        string   `json:"updatedAt"`
	Available                        bool     `json:"available"`
}

type acpGoalResponse struct {
	SchemaVersion  int              `json:"schemaVersion"`
	SessionID      string           `json:"sessionId"`
	RequestID      string           `json:"requestId"`
	EventID        string           `json:"eventId"`
	Phase          string           `json:"phase"`
	GoalTurnID     string           `json:"goalTurnId,omitempty"`
	Goal           *acpGoalSnapshot `json:"goal"`
	Cleared        bool             `json:"cleared"`
	RequiresPrompt bool             `json:"requiresPrompt,omitempty"`
	TerminalReason string           `json:"terminalReason,omitempty"`
}

func negotiateACPGoalNamespace(meta map[string]any) (acpGoalNegotiation, error) {
	canonical := parseACPGoalOffer(meta, &canonicalACPGoalNamespace)
	legacy := parseACPGoalOffer(meta, &legacyACPGoalNamespace)

	if canonical.offered && legacy.offered {
		negotiation := acpGoalNegotiation{offered: true}
		if !canonical.valid || !legacy.valid {
			return negotiation, invalidACPGoalNegotiation()
		}
		if canonical.supported && legacy.supported {
			negotiation.namespace = &canonicalACPGoalNamespace
			return negotiation, nil
		}
		if canonical.supported != legacy.supported ||
			!slices.Equal(canonical.versions, legacy.versions) {
			return negotiation, invalidACPGoalNegotiation()
		}
		return negotiation, nil
	}

	if canonical.offered {
		negotiation := acpGoalNegotiation{offered: true}
		if canonical.valid && canonical.supported {
			negotiation.namespace = &canonicalACPGoalNamespace
		}
		return negotiation, nil
	}
	if legacy.offered {
		negotiation := acpGoalNegotiation{offered: true}
		if legacy.valid && legacy.supported {
			negotiation.namespace = &legacyACPGoalNamespace
		}
		return negotiation, nil
	}
	return acpGoalNegotiation{}, nil
}

func parseACPGoalOffer(
	meta map[string]any,
	namespace *acpGoalNamespace,
) parsedACPGoalOffer {
	if namespace == nil {
		return parsedACPGoalOffer{}
	}
	raw, ok := meta[namespace.capabilityKey]
	if !ok {
		return parsedACPGoalOffer{}
	}
	parsed := parsedACPGoalOffer{offered: true}
	offer, ok := raw.(map[string]any)
	if !ok || len(offer) != 2 {
		return parsed
	}
	notifications, ok := offer["notifications"].(bool)
	if !ok || !notifications {
		return parsed
	}
	versions, ok := acpGoalOfferedVersions(offer["versions"])
	if !ok || len(versions) == 0 || len(versions) > acpGoalMaxOfferedVersions {
		return parsed
	}
	slices.Sort(versions)
	parsed.versions = slices.Compact(versions)
	parsed.valid = true
	parsed.supported = slices.Contains(parsed.versions, namespace.version)
	return parsed
}

func invalidACPGoalNegotiation() *acpsdk.RequestError {
	return acpsdk.NewInvalidParams(map[string]any{
		"detail": "conflicting ACP Goal capability offers",
	})
}

func acpGoalOfferedVersions(raw any) ([]int, bool) {
	switch values := raw.(type) {
	case []int:
		if len(values) == 0 || len(values) > acpGoalMaxOfferedVersions {
			return nil, false
		}
		return append([]int(nil), values...), true
	case []any:
		if len(values) == 0 || len(values) > acpGoalMaxOfferedVersions {
			return nil, false
		}
		versions := make([]int, 0, len(values))
		for _, value := range values {
			version, ok := acpGoalVersion(value)
			if !ok {
				return nil, false
			}
			versions = append(versions, version)
		}
		return versions, true
	default:
		return nil, false
	}
}

func acpGoalVersion(value any) (int, bool) {
	switch version := value.(type) {
	case int:
		return version, version > 0 && version <= acpGoalMaxProtocolVersion
	case int32:
		return int(version), version > 0 && version <= acpGoalMaxProtocolVersion
	case int64:
		if version <= 0 || version > acpGoalMaxProtocolVersion {
			return 0, false
		}
		return int(version), true
	case uint:
		if version == 0 || version > acpGoalMaxProtocolVersion {
			return 0, false
		}
		return int(version), true
	case uint64:
		if version == 0 || version > acpGoalMaxProtocolVersion {
			return 0, false
		}
		return int(version), true
	case float64:
		if version <= 0 || version > acpGoalMaxProtocolVersion ||
			version != math.Trunc(version) {
			return 0, false
		}
		return int(version), true
	default:
		return 0, false
	}
}

func (a *Agent) acpGoalCapabilityNegotiated() bool {
	return a.selectedACPGoalNamespace() != nil
}

func (a *Agent) selectedACPGoalNamespace() *acpGoalNamespace {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.initialized {
		return nil
	}
	return a.goalNamespace
}

func (a *Agent) handleGoalExtension(
	ctx context.Context,
	method string,
	params json.RawMessage,
) (any, error) {
	namespace := a.selectedACPGoalNamespace()
	if namespace == nil {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	switch method {
	case namespace.getMethod:
		return a.handleGoalGet(params)
	case namespace.controlMethod:
		return a.handleGoalControl(ctx, params)
	case namespace.continueMethod:
		return a.handleGoalContinue(ctx, params)
	default:
		return nil, acpsdk.NewMethodNotFound(method)
	}
}

func (a *Agent) handleGoalGet(params json.RawMessage) (any, error) {
	var request acpGoalEnvelope
	if err := decodeACPGoalParams(params, &request); err != nil {
		return nil, err
	}
	if err := validateACPGoalEnvelope(request); err != nil {
		return nil, err
	}
	sess, err := a.acquireGoalSessionRead(request.SessionID)
	if err != nil {
		return nil, err
	}
	defer sess.endRead()
	if available, reason := sess.Engine.GoalCommandAvailability(); !available {
		return nil, newACPGoalConflict(
			"unavailable",
			reason,
			sess.Engine,
		)
	}
	snapshot, _ := sess.Engine.GoalSnapshot()
	return newACPGoalResponse(
		request.SessionID,
		request.RequestID,
		"current",
		snapshot,
		false,
		false,
		"",
		"",
	), nil
}

func (a *Agent) handleGoalControl(
	ctx context.Context,
	params json.RawMessage,
) (any, error) {
	var request acpGoalControlParams
	if err := decodeACPGoalParams(params, &request); err != nil {
		return nil, err
	}
	if err := validateACPGoalControl(request); err != nil {
		return nil, err
	}
	sess, err := a.lookupGoalSession(request.SessionID)
	if err != nil {
		return nil, err
	}
	if !sess.beginGoalControl() {
		return nil, newACPGoalConflict(
			"session_busy",
			"session is closed or already processing a prompt or Goal request",
			sess.Engine,
		)
	}
	defer sess.endPrompt()

	result, err := sess.Engine.ApplyGoalControl(engine.GoalControlRequest{
		Operation:        engine.GoalControlOperation(request.Operation),
		ExpectedGoalID:   request.ExpectedGoalID,
		ExpectedRevision: request.ExpectedRevision,
		Objective:        optionalString(request.Objective),
		TokenBudget:      request.TokenBudget,
	})
	if err != nil {
		return nil, mapACPGoalControlError(err, sess.Engine)
	}
	response := newACPGoalResponse(
		request.SessionID,
		request.RequestID,
		string(result.Phase),
		result.Goal,
		result.Cleared,
		result.RequiresPrompt,
		"",
		"",
	)
	if err := a.notifyACPGoalUpdated(ctx, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (a *Agent) handleGoalContinue(
	ctx context.Context,
	params json.RawMessage,
) (any, error) {
	var request acpGoalContinueParams
	if err := decodeACPGoalParams(params, &request); err != nil {
		return nil, err
	}
	if err := validateACPGoalContinue(request); err != nil {
		return nil, err
	}
	sess, err := a.lookupGoalSession(request.SessionID)
	if err != nil {
		return nil, err
	}
	turnCtx, cancel := context.WithCancel(ctx)
	if !sess.beginGoalContinuation(cancel) {
		cancel()
		return nil, newACPGoalConflict(
			"session_busy",
			"session is closed or already processing a prompt or Goal request",
			sess.Engine,
		)
	}
	defer sess.endPrompt()
	cancelWatchDone := make(chan struct{})
	cancelWatchFinished := make(chan struct{})
	go func() {
		defer close(cancelWatchFinished)
		select {
		case <-turnCtx.Done():
			_, _ = sess.cancelPrompt()
		case <-cancelWatchDone:
		}
	}()
	defer func() {
		if turnCtx.Err() != nil {
			_, _ = sess.cancelPrompt()
		}
		close(cancelWatchDone)
		<-cancelWatchFinished
		cancel()
	}()

	current, ok := sess.Engine.GoalSnapshot()
	if !ok || !matchesACPGoalContinuationExpectation(current, request) {
		return nil, newACPGoalConflict(
			"stale_continuation",
			"Goal identity, revision, objective revision, or continuation ordinal changed",
			sess.Engine,
		)
	}
	item, claimed, err := sess.Engine.ClaimNextGoalContinuation()
	if err != nil {
		return nil, newACPGoalConflict(
			"continuation_unavailable",
			err.Error(),
			sess.Engine,
		)
	}
	if !claimed || item.GoalContinuation == nil {
		return nil, newACPGoalConflict(
			"continuation_unavailable",
			"no exact durable Goal continuation is pending",
			sess.Engine,
		)
	}
	events, _ := sess.Engine.SubmitGoalContinuation(turnCtx, item)
	terminalReason, lastGoal, driveErr := a.driveSessionEvents(
		ctx,
		turnCtx,
		sess,
		events,
		cancel,
	)
	current, _ = sess.Engine.GoalSnapshot()
	if driveErr != nil {
		return nil, driveErr
	}
	phase := "settled"
	goalTurnID := ""
	if lastGoal != nil {
		phase = string(lastGoal.Phase)
		goalTurnID = lastGoal.Goal.LastGoalTurnID
	}
	if goalTurnID == "" && current != nil {
		goalTurnID = current.LastGoalTurnID
	}
	response := newACPGoalResponse(
		request.SessionID,
		request.RequestID,
		phase,
		current,
		false,
		false,
		string(terminalReason),
		goalTurnID,
	)
	if err := a.notifyACPGoalUpdated(ctx, response); err != nil {
		return nil, err
	}
	return response, nil
}

func decodeACPGoalParams(params json.RawMessage, target any) error {
	if len(params) == 0 || len(params) > acpGoalMaxParamsBytes {
		return newACPGoalInvalidParams("Goal params are empty or exceed the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return newACPGoalInvalidParams("Goal params do not match the version 1 schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return newACPGoalInvalidParams("Goal params contain trailing JSON")
	}
	return nil
}

func validateACPGoalEnvelope(request acpGoalEnvelope) error {
	if request.SchemaVersion != acpGoalProtocolVersion {
		return newACPGoalInvalidParams("schemaVersion must be 1")
	}
	if err := validateACPGoalToken(
		"sessionId",
		request.SessionID,
		acpGoalMaxSessionIDBytes,
	); err != nil {
		return err
	}
	return validateACPGoalToken(
		"requestId",
		request.RequestID,
		acpGoalMaxRequestIDBytes,
	)
}

func validateACPGoalControl(request acpGoalControlParams) error {
	if err := validateACPGoalEnvelope(acpGoalEnvelope{
		SchemaVersion: request.SchemaVersion,
		SessionID:     request.SessionID,
		RequestID:     request.RequestID,
	}); err != nil {
		return err
	}
	operation := engine.GoalControlOperation(request.Operation)
	switch operation {
	case engine.GoalControlCreate:
		if request.ExpectedRevision != 0 || request.ExpectedGoalID != "" {
			return newACPGoalInvalidParams(
				"create requires expectedRevision 0 and no expectedGoalId",
			)
		}
		if request.Objective == nil {
			return newACPGoalInvalidParams("create requires objective")
		}
	case engine.GoalControlEdit:
		if err := validateACPGoalMutationExpectation(
			request.ExpectedGoalID,
			request.ExpectedRevision,
		); err != nil {
			return err
		}
		if request.Objective == nil || request.TokenBudget != nil {
			return newACPGoalInvalidParams(
				"edit requires objective and forbids tokenBudget",
			)
		}
	case engine.GoalControlBudget:
		if err := validateACPGoalMutationExpectation(
			request.ExpectedGoalID,
			request.ExpectedRevision,
		); err != nil {
			return err
		}
		if request.Objective != nil ||
			request.TokenBudget == nil ||
			*request.TokenBudget == 0 {
			return newACPGoalInvalidParams(
				"budget requires one positive tokenBudget and no objective",
			)
		}
	case engine.GoalControlPause,
		engine.GoalControlResume,
		engine.GoalControlClear:
		if err := validateACPGoalMutationExpectation(
			request.ExpectedGoalID,
			request.ExpectedRevision,
		); err != nil {
			return err
		}
		if request.Objective != nil || request.TokenBudget != nil {
			return newACPGoalInvalidParams(
				"this operation accepts no objective or tokenBudget",
			)
		}
	default:
		return newACPGoalInvalidParams("operation is unsupported")
	}
	if request.Objective != nil {
		if !utf8.ValidString(*request.Objective) ||
			strings.ContainsRune(*request.Objective, '\x00') ||
			len([]rune(*request.Objective)) > acpGoalMaxObjectiveScalars ||
			strings.TrimSpace(*request.Objective) == "" {
			return newACPGoalInvalidParams(
				"objective must be non-empty valid UTF-8 without NUL and at most 4000 characters",
			)
		}
	}
	return nil
}

func validateACPGoalContinue(request acpGoalContinueParams) error {
	if err := validateACPGoalEnvelope(acpGoalEnvelope{
		SchemaVersion: request.SchemaVersion,
		SessionID:     request.SessionID,
		RequestID:     request.RequestID,
	}); err != nil {
		return err
	}
	if err := validateACPGoalMutationExpectation(
		request.ExpectedGoalID,
		request.ExpectedRevision,
	); err != nil {
		return err
	}
	if request.ExpectedObjectiveRevision == 0 ||
		request.ExpectedContinuationOrdinal == 0 {
		return newACPGoalInvalidParams(
			"continue requires positive objective revision and continuation ordinal",
		)
	}
	return nil
}

func validateACPGoalMutationExpectation(goalID string, revision uint64) error {
	if err := validateACPGoalToken(
		"expectedGoalId",
		goalID,
		acpGoalMaxGoalIDBytes,
	); err != nil {
		return err
	}
	if revision == 0 {
		return newACPGoalInvalidParams("expectedRevision must be positive")
	}
	return nil
}

func validateACPGoalToken(name, value string, maxBytes int) error {
	if !utf8.ValidString(value) ||
		strings.TrimSpace(value) == "" ||
		value != strings.TrimSpace(value) ||
		strings.ContainsRune(value, '\x00') ||
		len(value) > maxBytes {
		return newACPGoalInvalidParams(
			fmt.Sprintf("%s is invalid or exceeds its size limit", name),
		)
	}
	return nil
}

func (a *Agent) lookupGoalSession(sessionID string) (*Session, error) {
	a.mu.Lock()
	sess := a.sessions[acpsdk.SessionId(sessionID)]
	a.mu.Unlock()
	if sess == nil {
		return nil, newACPGoalSessionNotFound(sessionID)
	}
	return sess, nil
}

func (a *Agent) acquireGoalSessionRead(sessionID string) (*Session, error) {
	sess, err := a.lookupGoalSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !sess.beginRead() {
		return nil, newACPGoalSessionNotFound(sessionID)
	}
	return sess, nil
}

func matchesACPGoalContinuationExpectation(
	current *engine.GoalSnapshot,
	request acpGoalContinueParams,
) bool {
	return current != nil &&
		current.GoalID == request.ExpectedGoalID &&
		current.Revision == request.ExpectedRevision &&
		current.ObjectiveRevision == request.ExpectedObjectiveRevision &&
		current.ContinuationOrdinal == request.ExpectedContinuationOrdinal
}

func mapACPGoalControlError(
	err error,
	queryEngine *engine.QueryEngine,
) error {
	var conflict *engine.GoalControlConflictError
	if errors.As(err, &conflict) {
		return newACPGoalConflict(
			"stale_goal",
			conflict.Reason,
			queryEngine,
		)
	}
	return newACPGoalConflict("control_rejected", err.Error(), queryEngine)
}

func newACPGoalConflict(
	reason string,
	detail string,
	queryEngine *engine.QueryEngine,
) *acpsdk.RequestError {
	data := map[string]any{
		"reason": reason,
		"detail": boundedACPGoalDetail(detail),
	}
	if queryEngine != nil {
		if current, ok := queryEngine.GoalSnapshot(); ok {
			data["currentGoal"] = projectACPGoal(current)
		}
	}
	return &acpsdk.RequestError{
		Code:    CodeGoalConflict,
		Message: "Goal request conflict",
		Data:    data,
	}
}

func newACPGoalInvalidParams(detail string) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeInvalidParams,
		Message: "Invalid params",
		Data:    map[string]any{"detail": detail},
	}
}

func newACPGoalSessionNotFound(sessionID string) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeSessionNotFound,
		Message: "Session not found",
		Data:    map[string]any{"sessionId": sessionID},
	}
}

func boundedACPGoalDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) <= 512 {
		return detail
	}
	for len(detail) > 512 {
		_, size := utf8.DecodeLastRuneInString(detail)
		detail = detail[:len(detail)-size]
	}
	return detail
}

func newACPGoalResponse(
	sessionID string,
	requestID string,
	phase string,
	goal *engine.GoalSnapshot,
	cleared bool,
	requiresPrompt bool,
	terminalReason string,
	goalTurnID string,
) *acpGoalResponse {
	return &acpGoalResponse{
		SchemaVersion:  acpGoalProtocolVersion,
		SessionID:      sessionID,
		RequestID:      requestID,
		EventID:        uuid.New().String(),
		Phase:          phase,
		GoalTurnID:     goalTurnID,
		Goal:           projectACPGoal(goal),
		Cleared:        cleared,
		RequiresPrompt: requiresPrompt,
		TerminalReason: terminalReason,
	}
}

func projectACPGoal(snapshot *engine.GoalSnapshot) *acpGoalSnapshot {
	if snapshot == nil {
		return nil
	}
	var tokenBudget *uint64
	var tokensRemaining *uint64
	if snapshot.TokenBudget != nil {
		budget := *snapshot.TokenBudget
		tokenBudget = &budget
		remaining := uint64(0)
		if budget > snapshot.TokensUsed {
			remaining = budget - snapshot.TokensUsed
		}
		tokensRemaining = &remaining
	}
	return &acpGoalSnapshot{
		GoalID:                           snapshot.GoalID,
		Objective:                        snapshot.Objective,
		ObjectiveRevision:                snapshot.ObjectiveRevision,
		Status:                           snapshot.Status,
		StatusReasonCode:                 snapshot.StatusReasonCode,
		StatusReason:                     snapshot.StatusReason,
		Revision:                         snapshot.Revision,
		TokenBudget:                      tokenBudget,
		TokensUsed:                       snapshot.TokensUsed,
		TokensRemaining:                  tokensRemaining,
		UsageLedgerRevision:              snapshot.UsageLedgerRevision,
		UsageCoverage:                    snapshot.UsageCoverage,
		RootActiveTimeMillis:             snapshot.RootActiveTimeMillis,
		ContinuationOrdinal:              snapshot.ContinuationOrdinal,
		LastGoalTurnID:                   snapshot.LastGoalTurnID,
		LastTerminalSequence:             snapshot.LastTerminalSequence,
		PendingCompleteTurnID:            snapshot.PendingCompleteTurnID,
		PendingCompleteObjectiveRevision: snapshot.PendingCompleteObjectiveRevision,
		BlockerKey:                       snapshot.BlockerKey,
		BlockerTurnIDs:                   append([]string{}, snapshot.BlockerTurnIDs...),
		CreatedAt:                        snapshot.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:                        snapshot.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Available:                        snapshot.Available,
	}
}

func (a *Agent) notifyACPGoalUpdated(
	ctx context.Context,
	response *acpGoalResponse,
) error {
	if a == nil || a.conn == nil || response == nil {
		return nil
	}
	namespace := a.selectedACPGoalNamespace()
	if namespace == nil {
		return nil
	}
	return a.conn.NotifyExtension(ctx, namespace.updatedMethod, response)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
