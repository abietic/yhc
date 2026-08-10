package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/prefetch"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

const (
	projectGraphCheckpointEnvelopeVersion = 1
	projectGraphHITLStateVersion          = 1
	projectGraphHITLRequestVersion        = 1
	projectGraphHITLDecisionVersion       = 1
	projectGraphCheckpointSuffix          = ".project-graph-checkpoint.json"
	maxGraphHITLPresentationBytes         = 32 * 1024
)

func init() {
	schema.RegisterName[projectGraphKernelInput](
		"eino_agent_project_graph_kernel_input_v1",
	)
	schema.RegisterName[projectGraphKernelResult](
		"eino_agent_project_graph_kernel_result_v1",
	)
	schema.RegisterName[projectGraphStep](
		"eino_agent_project_graph_step_v1",
	)
	schema.RegisterName[*projectGraphKernelState](
		"eino_agent_project_graph_kernel_state_v1",
	)
	schema.RegisterName[projectGraphHITLRequest](
		"eino_agent_project_graph_hitl_request_v1",
	)
	schema.RegisterName[RuntimePermissionDecision](
		"eino_agent_runtime_permission_decision_v1",
	)
	schema.RegisterName[*projectGraphHITLInterruptState](
		"eino_agent_project_graph_hitl_interrupt_state_v1",
	)
}

// projectGraphHITLRequest is the sanitized durable identity presented to a
// transport. Exact tool arguments remain inside the 0600 opaque Compose
// checkpoint and are verified through InvocationDigest before a decision can
// be committed.
type projectGraphHITLRequest struct {
	Version          int                  `json:"version"`
	RequestID        string               `json:"request_id"`
	InterruptID      string               `json:"interrupt_id,omitempty"`
	InvocationDigest string               `json:"invocation_digest"`
	PolicyRevision   string               `json:"policy_revision"`
	ToolName         string               `json:"tool_name"`
	Input            map[string]any       `json:"input,omitempty"`
	Message          string               `json:"message,omitempty"`
	SessionScope     string               `json:"session_scope,omitempty"`
	Scope            RuntimeInputScope    `json:"scope"`
	Kind             string               `json:"kind"`
	PlanApproval     *PlanApprovalRequest `json:"plan_approval,omitempty"`
}

type projectGraphHITLInterruptInfo struct {
	Request projectGraphHITLRequest `json:"request"`
}

// projectGraphHITLInterruptState contains only user intent. It never contains
// a callback, coordinator waiter, registry, model, hook executor, or committed
// approval. Every intent is re-evaluated against the live invocation.
type projectGraphHITLInterruptState struct {
	Version   int                         `json:"version"`
	Request   projectGraphHITLRequest     `json:"request"`
	Decisions []RuntimePermissionDecision `json:"decisions,omitempty"`
}

type projectGraphCheckpointEnvelope struct {
	Version       int                      `json:"version"`
	CheckpointID  string                   `json:"checkpoint_id"`
	KernelVersion string                   `json:"kernel_version"`
	Scope         RuntimeInputScope        `json:"scope"`
	Opaque        []byte                   `json:"opaque"`
	Interrupt     *projectGraphHITLRequest `json:"interrupt,omitempty"`
	UpdatedAt     time.Time                `json:"updated_at"`
}

type projectGraphCheckpointStore struct {
	mu           sync.Mutex
	path         string
	checkpointID string
	scope        RuntimeInputScope
	clock        func() time.Time
	envelope     projectGraphCheckpointEnvelope
}

type projectGraphCheckpointStoreContextKey struct{}

type projectGraphCheckpointStoreDelegate struct{}

func projectGraphCheckpointPath(transcriptPath string) string {
	transcriptPath = strings.TrimSpace(transcriptPath)
	if transcriptPath == "" {
		return ""
	}
	return transcriptPath + projectGraphCheckpointSuffix
}

func projectGraphCheckpointID(scope RuntimeInputScope) string {
	encoded, _ := json.Marshal(struct {
		Kernel string            `json:"kernel"`
		Scope  RuntimeInputScope `json:"scope"`
	}{
		Kernel: queryKernelVersionProjectGraph,
		Scope:  scope,
	})
	digest := sha256.Sum256(encoded)
	return "project-graph-" + hex.EncodeToString(digest[:])
}

func projectGraphPermissionDecisionItemID(
	request projectGraphHITLRequest,
) string {
	encoded, _ := json.Marshal(struct {
		RequestID        string `json:"request_id"`
		InterruptID      string `json:"interrupt_id"`
		InvocationDigest string `json:"invocation_digest"`
		PolicyRevision   string `json:"policy_revision"`
	}{
		RequestID:        request.RequestID,
		InterruptID:      request.InterruptID,
		InvocationDigest: request.InvocationDigest,
		PolicyRevision:   request.PolicyRevision,
	})
	digest := sha256.Sum256(encoded)
	return "project-graph-decision-" + hex.EncodeToString(digest[:])
}

func persistedProjectGraphInterruptMatches(
	persisted *session.PersistedGraphInterrupt,
	request projectGraphHITLRequest,
) bool {
	return persisted != nil &&
		persisted.Version == request.Version &&
		persisted.RequestID == request.RequestID &&
		persisted.InterruptID == request.InterruptID &&
		persisted.InvocationDigest == request.InvocationDigest &&
		persisted.PolicyRevision == request.PolicyRevision &&
		persisted.Kind == request.Kind
}

func newProjectGraphCheckpointStore(
	path string,
	scope RuntimeInputScope,
	clock func() time.Time,
) (*projectGraphCheckpointStore, error) {
	if clock == nil {
		clock = time.Now
	}
	scope = RuntimeInputScope{
		SessionID: strings.TrimSpace(scope.SessionID),
		ThreadID:  strings.TrimSpace(scope.ThreadID),
		AgentID:   strings.TrimSpace(scope.AgentID),
	}
	store := &projectGraphCheckpointStore{
		path:         strings.TrimSpace(path),
		checkpointID: projectGraphCheckpointID(scope),
		scope:        scope,
		clock:        clock,
	}
	if store.path == "" {
		return store, nil
	}
	envelope, existed, err := loadProjectGraphCheckpointEnvelope(store.path)
	if err != nil {
		return nil, err
	}
	if !existed {
		return store, nil
	}
	if err := store.validateEnvelope(envelope); err != nil {
		return nil, err
	}
	store.envelope = cloneProjectGraphCheckpointEnvelope(envelope)
	return store, nil
}

func (projectGraphCheckpointStoreDelegate) Get(
	ctx context.Context,
	checkpointID string,
) ([]byte, bool, error) {
	store, err := projectGraphCheckpointStoreFromContext(ctx)
	if err != nil {
		return nil, false, err
	}
	return store.Get(ctx, checkpointID)
}

func (projectGraphCheckpointStoreDelegate) Set(
	ctx context.Context,
	checkpointID string,
	checkpoint []byte,
) error {
	store, err := projectGraphCheckpointStoreFromContext(ctx)
	if err != nil {
		return err
	}
	return store.Set(ctx, checkpointID, checkpoint)
}

func (projectGraphCheckpointStoreDelegate) Delete(
	ctx context.Context,
	checkpointID string,
) error {
	store, err := projectGraphCheckpointStoreFromContext(ctx)
	if err != nil {
		return err
	}
	return store.Delete(ctx, checkpointID)
}

func withProjectGraphCheckpointStore(
	ctx context.Context,
	store *projectGraphCheckpointStore,
) context.Context {
	return context.WithValue(ctx, projectGraphCheckpointStoreContextKey{}, store)
}

func projectGraphCheckpointStoreFromContext(
	ctx context.Context,
) (*projectGraphCheckpointStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("project graph checkpoint context is missing")
	}
	store, ok := ctx.Value(projectGraphCheckpointStoreContextKey{}).(*projectGraphCheckpointStore)
	if !ok || store == nil {
		return nil, fmt.Errorf("project graph checkpoint store is missing")
	}
	return store, nil
}

func (s *projectGraphCheckpointStore) Get(
	ctx context.Context,
	checkpointID string,
) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateCheckpointID(checkpointID); err != nil {
		return nil, false, err
	}
	if len(s.envelope.Opaque) == 0 {
		return nil, false, nil
	}
	return append([]byte(nil), s.envelope.Opaque...), true, nil
}

func (s *projectGraphCheckpointStore) Set(
	ctx context.Context,
	checkpointID string,
	checkpoint []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateCheckpointID(checkpointID); err != nil {
		return err
	}
	if len(checkpoint) == 0 {
		return fmt.Errorf("project graph checkpoint payload is empty")
	}
	next := cloneProjectGraphCheckpointEnvelope(s.envelope)
	next.Version = projectGraphCheckpointEnvelopeVersion
	next.CheckpointID = s.checkpointID
	next.KernelVersion = queryKernelVersionProjectGraph
	next.Scope = s.scope
	next.Opaque = append([]byte(nil), checkpoint...)
	// A newly committed Eino node checkpoint supersedes the prior human
	// decision boundary. Clear that owner before persisting so a crash after a
	// tool node can only fail closed; MarkInterrupt installs a new owner when
	// the current run actually pauses again.
	next.Interrupt = nil
	next.UpdatedAt = s.clock().UTC()
	if err := s.persistLocked(next); err != nil {
		return err
	}
	s.envelope = next
	return nil
}

func (s *projectGraphCheckpointStore) Delete(
	ctx context.Context,
	checkpointID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateCheckpointID(checkpointID); err != nil {
		return err
	}
	if s.path != "" {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove project graph checkpoint: %w", err)
		}
		syncProjectGraphCheckpointDirectory(filepath.Dir(s.path))
	}
	s.envelope = projectGraphCheckpointEnvelope{}
	return nil
}

func (s *projectGraphCheckpointStore) MarkInterrupt(
	ctx context.Context,
	request projectGraphHITLRequest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.envelope.Opaque) == 0 {
		return fmt.Errorf("project graph interrupt has no opaque checkpoint")
	}
	request = cloneProjectGraphHITLRequest(request)
	if err := validateProjectGraphHITLRequest(request, true); err != nil {
		return err
	}
	next := cloneProjectGraphCheckpointEnvelope(s.envelope)
	next.Interrupt = &request
	next.UpdatedAt = s.clock().UTC()
	if err := s.persistLocked(next); err != nil {
		return err
	}
	s.envelope = next
	return nil
}

func (s *projectGraphCheckpointStore) ActiveInterrupt() (
	projectGraphHITLRequest,
	bool,
) {
	if s == nil {
		return projectGraphHITLRequest{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.envelope.Interrupt == nil {
		return projectGraphHITLRequest{}, false
	}
	return cloneProjectGraphHITLRequest(*s.envelope.Interrupt), true
}

func (s *projectGraphCheckpointStore) HasOpaqueCheckpoint() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.envelope.Opaque) > 0
}

func (s *projectGraphCheckpointStore) validateCheckpointID(
	checkpointID string,
) error {
	if s == nil {
		return fmt.Errorf("project graph checkpoint store is unavailable")
	}
	if strings.TrimSpace(checkpointID) == "" ||
		checkpointID != s.checkpointID {
		return fmt.Errorf("project graph checkpoint identity mismatch")
	}
	return nil
}

func (s *projectGraphCheckpointStore) validateEnvelope(
	envelope projectGraphCheckpointEnvelope,
) error {
	if envelope.Version != projectGraphCheckpointEnvelopeVersion {
		return fmt.Errorf(
			"unsupported project graph checkpoint envelope version %d",
			envelope.Version,
		)
	}
	if envelope.CheckpointID != s.checkpointID ||
		envelope.KernelVersion != queryKernelVersionProjectGraph ||
		!runtimeScopesEqual(envelope.Scope, s.scope) {
		return fmt.Errorf("project graph checkpoint envelope identity mismatch")
	}
	if len(envelope.Opaque) == 0 {
		return fmt.Errorf("project graph checkpoint envelope has no opaque payload")
	}
	if envelope.Interrupt == nil {
		return fmt.Errorf(
			"project graph checkpoint has no durable interrupt owner",
		)
	}
	return validateProjectGraphHITLRequest(*envelope.Interrupt, true)
}

func (s *projectGraphCheckpointStore) persistLocked(
	envelope projectGraphCheckpointEnvelope,
) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project graph checkpoint envelope: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create project graph checkpoint directory: %w", err)
	}
	file, err := os.CreateTemp(
		dir,
		"."+filepath.Base(s.path)+".tmp-*",
	)
	if err != nil {
		return fmt.Errorf("create project graph checkpoint temp file: %w", err)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect project graph checkpoint temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write project graph checkpoint temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync project graph checkpoint temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close project graph checkpoint temp file: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace project graph checkpoint: %w", err)
	}
	removeTemp = false
	syncProjectGraphCheckpointDirectory(dir)
	return nil
}

func loadProjectGraphCheckpointEnvelope(
	path string,
) (projectGraphCheckpointEnvelope, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return projectGraphCheckpointEnvelope{}, false, nil
		}
		return projectGraphCheckpointEnvelope{}, false, fmt.Errorf(
			"read project graph checkpoint: %w",
			err,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope projectGraphCheckpointEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return projectGraphCheckpointEnvelope{}, false, fmt.Errorf(
			"decode project graph checkpoint: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return projectGraphCheckpointEnvelope{}, false, fmt.Errorf(
				"decode project graph checkpoint: trailing JSON value",
			)
		}
		return projectGraphCheckpointEnvelope{}, false, fmt.Errorf(
			"decode project graph checkpoint: %w",
			err,
		)
	}
	return envelope, true, nil
}

func syncProjectGraphCheckpointDirectory(dir string) {
	directory, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = directory.Sync()
	_ = directory.Close()
}

func cloneProjectGraphCheckpointEnvelope(
	envelope projectGraphCheckpointEnvelope,
) projectGraphCheckpointEnvelope {
	envelope.Opaque = append([]byte(nil), envelope.Opaque...)
	if envelope.Interrupt != nil {
		request := cloneProjectGraphHITLRequest(*envelope.Interrupt)
		envelope.Interrupt = &request
	}
	return envelope
}

func cloneProjectGraphHITLRequest(
	request projectGraphHITLRequest,
) projectGraphHITLRequest {
	request.Input = cloneInputMap(request.Input)
	if request.PlanApproval != nil {
		approval := *request.PlanApproval
		request.PlanApproval = &approval
	}
	return request
}

func validateProjectGraphHITLRequest(
	request projectGraphHITLRequest,
	requireInterruptID bool,
) error {
	if request.Version != projectGraphHITLRequestVersion ||
		strings.TrimSpace(request.RequestID) == "" ||
		strings.TrimSpace(request.InvocationDigest) == "" ||
		strings.TrimSpace(request.PolicyRevision) == "" ||
		strings.TrimSpace(request.ToolName) == "" ||
		strings.TrimSpace(request.Scope.SessionID) == "" {
		return fmt.Errorf("project graph HITL request is incomplete")
	}
	if requireInterruptID && strings.TrimSpace(request.InterruptID) == "" {
		return fmt.Errorf("project graph HITL request has no interrupt identity")
	}
	if request.Kind == "" {
		return fmt.Errorf("project graph HITL request has no interaction kind")
	}
	if request.PlanApproval != nil &&
		(request.PlanApproval.RequestID != request.RequestID ||
			request.PlanApproval.PlanRevision == 0 ||
			!validPlanDigest(request.PlanApproval.InitialPlanDigest)) {
		return fmt.Errorf("project graph HITL Plan approval identity mismatch")
	}
	return nil
}

func validateProjectGraphResumeDecision(
	request projectGraphHITLRequest,
	decision RuntimePermissionDecision,
) error {
	if err := validateProjectGraphHITLRequest(request, true); err != nil {
		return err
	}
	if decision.Version != projectGraphHITLDecisionVersion ||
		decision.RequestID != request.RequestID ||
		decision.InterruptID != request.InterruptID ||
		decision.InvocationDigest != request.InvocationDigest ||
		decision.PolicyRevision != request.PolicyRevision {
		return fmt.Errorf("project graph resume decision identity mismatch")
	}
	switch decision.Result.Decision {
	case PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways,
		PermissionDeny, PermissionCancelled, PermissionTimedOut:
		return nil
	default:
		return fmt.Errorf("project graph resume decision is invalid")
	}
}

func projectGraphRootInterrupt(
	info *compose.InterruptInfo,
) (projectGraphHITLRequest, error) {
	if info == nil || len(info.InterruptContexts) != 1 {
		return projectGraphHITLRequest{}, fmt.Errorf(
			"project graph requires exactly one root interrupt",
		)
	}
	interrupt := info.InterruptContexts[0]
	if interrupt == nil || !interrupt.IsRootCause ||
		strings.TrimSpace(interrupt.ID) == "" {
		return projectGraphHITLRequest{}, fmt.Errorf(
			"project graph root interrupt identity is unavailable",
		)
	}
	payload, ok := interrupt.Info.(projectGraphHITLInterruptInfo)
	if !ok {
		if pointer, pointerOK := interrupt.Info.(*projectGraphHITLInterruptInfo); pointerOK &&
			pointer != nil {
			payload = *pointer
			ok = true
		}
	}
	if !ok {
		return projectGraphHITLRequest{}, fmt.Errorf(
			"project graph root interrupt payload is unsupported",
		)
	}
	request := cloneProjectGraphHITLRequest(payload.Request)
	request.InterruptID = interrupt.ID
	if err := validateProjectGraphHITLRequest(request, true); err != nil {
		return projectGraphHITLRequest{}, err
	}
	return request, nil
}

func restoreProjectGraphRuntimeForToolResume(
	runtime *canonicalQueryRuntime,
	round projectGraphRound,
) {
	if runtime == nil {
		return
	}
	toolCalls := projectGraphToolCallPointers(round.ToolCalls)
	runtime.state.Messages = append(
		[]*schema.Message(nil),
		runtime.params.Messages...,
	)
	runtime.state.ToolUseContext = runtime.params.ToolUseContext
	runtime.state.TurnCount = round.Number
	runtime.state.NeedsFollowUp = true
	runtime.state.ToolUseBlocks = toolCalls
	skillPrefetch := prefetch.NewSkillPrefetch(runtime.params.SkillRegistry)
	skillPrefetch.Start(runtime.params.Messages)
	runtime.prepared.messagesForQuery = runtime.params.Messages
	runtime.prepared.toolUseContext = runtime.params.ToolUseContext
	runtime.prepared.skillPrefetch = skillPrefetch
	runtime.model = canonicalModelRoundResult{
		messagesForQuery:   runtime.params.Messages,
		toolUseBlocks:      toolCalls,
		needsFollowUp:      true,
		toolCallsCommitted: true,
		toolUseContext:     runtime.params.ToolUseContext,
	}
}

func ensureProjectGraphAfterToolRuntime(runtime *canonicalQueryRuntime) {
	if runtime == nil {
		return
	}
	if runtime.prepared.messagesForQuery == nil {
		runtime.prepared.messagesForQuery = runtime.params.Messages
	}
	if runtime.prepared.toolUseContext == nil {
		runtime.prepared.toolUseContext = runtime.params.ToolUseContext
	}
	if runtime.prepared.skillPrefetch == nil {
		skillPrefetch := prefetch.NewSkillPrefetch(runtime.params.SkillRegistry)
		skillPrefetch.Start(runtime.params.Messages)
		runtime.prepared.skillPrefetch = skillPrefetch
	}
	if runtime.model.messagesForQuery == nil {
		runtime.model.messagesForQuery = runtime.params.Messages
	}
}

type projectGraphHITLProbe struct {
	scope     RuntimeInputScope
	decisions []RuntimePermissionDecision
	captured  *projectGraphHITLRequest
}

type projectGraphHITLExecution struct {
	mu                          sync.Mutex
	decisions                   []RuntimePermissionDecision
	basePolicyRevision          string
	currentPolicyRevision       string
	invalid                     bool
	afterLivePolicyCheckForTest func()
}

type (
	projectGraphHITLProbeContextKey     struct{}
	projectGraphHITLExecutionContextKey struct{}
)

func withProjectGraphHITLProbe(
	ctx context.Context,
	probe *projectGraphHITLProbe,
) context.Context {
	return context.WithValue(ctx, projectGraphHITLProbeContextKey{}, probe)
}

func projectGraphHITLProbeFromContext(
	ctx context.Context,
) *projectGraphHITLProbe {
	if ctx == nil {
		return nil
	}
	probe, _ := ctx.Value(projectGraphHITLProbeContextKey{}).(*projectGraphHITLProbe)
	return probe
}

func withProjectGraphHITLExecution(
	ctx context.Context,
	decisions []RuntimePermissionDecision,
) context.Context {
	cloned := cloneRuntimePermissionDecisions(decisions)
	execution := &projectGraphHITLExecution{decisions: cloned}
	if len(cloned) == 0 {
		execution.invalid = true
	} else {
		execution.basePolicyRevision = cloned[0].PolicyRevision
		execution.currentPolicyRevision = execution.basePolicyRevision
		for _, decision := range cloned {
			if decision.PolicyRevision != execution.basePolicyRevision {
				execution.invalid = true
				break
			}
		}
	}
	return context.WithValue(
		ctx,
		projectGraphHITLExecutionContextKey{},
		execution,
	)
}

func projectGraphHITLExecutionFromContext(
	ctx context.Context,
) *projectGraphHITLExecution {
	if ctx == nil {
		return nil
	}
	execution, _ := ctx.Value(projectGraphHITLExecutionContextKey{}).(*projectGraphHITLExecution)
	return execution
}

func cloneRuntimePermissionDecisions(
	decisions []RuntimePermissionDecision,
) []RuntimePermissionDecision {
	cloned := make([]RuntimePermissionDecision, 0, len(decisions))
	for _, decision := range decisions {
		decision.Result = clonePermissionInteractionResult(decision.Result)
		cloned = append(cloned, decision)
	}
	return cloned
}

func projectGraphHITLDecisionForRequest(
	decisions []RuntimePermissionDecision,
	request projectGraphHITLRequest,
) (RuntimePermissionDecision, bool) {
	for _, decision := range decisions {
		if decision.RequestID == request.RequestID &&
			decision.InvocationDigest == request.InvocationDigest &&
			decision.PolicyRevision == request.PolicyRevision {
			return decision, true
		}
	}
	return RuntimePermissionDecision{}, false
}

// projectGraphHITLExecutionDecisionForRequest deliberately excludes the
// policy revision from lookup. A resumed batch may advance its tracked policy
// revision after an earlier exact allow; the durable decision itself remains
// bound to the single batch base revision below.
func projectGraphHITLExecutionDecisionForRequest(
	decisions []RuntimePermissionDecision,
	request projectGraphHITLRequest,
) (RuntimePermissionDecision, bool) {
	for _, decision := range decisions {
		if decision.RequestID == request.RequestID &&
			decision.InvocationDigest == request.InvocationDigest {
			return decision, true
		}
	}
	return RuntimePermissionDecision{}, false
}

func (e *QueryEngine) resolveProjectGraphHITLPermission(
	ctx context.Context,
	request PermissionPromptRequest,
	emit func(QueryEvent),
) (bool, string) {
	probe := projectGraphHITLProbeFromContext(ctx)
	execution := projectGraphHITLExecutionFromContext(ctx)
	if probe == nil && execution == nil {
		return false, "project graph HITL context is missing"
	}
	var visible []*schema.ToolInfo
	if request.ToolContext != nil && request.ToolContext.Options != nil {
		visible = request.ToolContext.Options.Tools
	}
	toolSchemaDigest, err := projectGraphToolSchemaDigest(
		e.toolRegistry,
		visible,
		request.ToolName,
	)
	if err != nil {
		return false, err.Error()
	}
	scope := RuntimeInputScope{
		SessionID: request.SessionID,
		ThreadID:  request.ThreadID,
		AgentID:   request.AgentID,
	}
	if probe != nil && !runtimeScopesEqual(scope, probe.scope) {
		return false, "project graph HITL request scope mismatch"
	}
	durable := projectGraphHITLRequest{
		Version:   projectGraphHITLRequestVersion,
		RequestID: strings.TrimSpace(request.ToolUseID),
		InvocationDigest: projectGraphInvocationDigest(
			request,
			scope,
			toolSchemaDigest,
		),
		PolicyRevision: e.projectGraphPolicyRevision(request.ToolContext),
		ToolName:       strings.TrimSpace(request.ToolName),
		Input: sanitizeProjectGraphHITLInput(
			request.ToolName,
			request.Input,
		),
		Message:      strings.TrimSpace(request.Message),
		SessionScope: strings.TrimSpace(request.SessionScope),
		Scope:        scope,
		Kind:         permissionInteractionKind(request),
		PlanApproval: request.PlanApproval,
	}
	if err := validateProjectGraphHITLRequest(durable, false); err != nil {
		return false, err.Error()
	}
	if probe != nil {
		if _, ok := projectGraphHITLDecisionForRequest(
			probe.decisions,
			durable,
		); ok {
			return true, ""
		}
		captured := cloneProjectGraphHITLRequest(durable)
		probe.captured = &captured
		return false, "project graph interaction requires resume"
	}

	var decision RuntimePermissionDecision
	var ok bool
	if durable.PlanApproval != nil {
		// Plan approval is not part of the ordinary permission revision chain.
		// Keep its durable identity strictly bound to the original policy
		// revision before its existing settlement and dispatch path runs.
		decision, ok = projectGraphHITLDecisionForRequest(
			execution.decisions,
			durable,
		)
	} else {
		decision, ok = projectGraphHITLExecutionDecisionForRequest(
			execution.decisions,
			durable,
		)
	}
	if !ok {
		emitProjectGraphPermissionResolved(
			emit,
			durable,
			PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  "project graph permission intent expired",
			},
			"expired",
		)
		return false, "project graph permission intent expired"
	}
	result := clonePermissionInteractionResult(decision.Result)
	if durable.PlanApproval != nil {
		result = e.settlePlanApproval(durable.PlanApproval, result, emit)
		result = e.ensurePlanApprovalSettled(
			durable.PlanApproval,
			result,
			emit,
		)
		if result.PlanApproval != nil {
			SetPlanApprovalDecision(ctx, result.PlanApproval)
		}
	} else {
		execution.mu.Lock()
		actualRevision := e.projectGraphPolicyRevision(request.ToolContext)
		if execution.invalid ||
			decision.PolicyRevision != execution.basePolicyRevision ||
			actualRevision != execution.currentPolicyRevision {
			execution.invalid = execution.invalid ||
				actualRevision != execution.currentPolicyRevision
			result = PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  "project graph permission intent expired",
			}
		} else {
			afterLivePolicyCheckForTest := execution.afterLivePolicyCheckForTest
			if afterLivePolicyCheckForTest != nil {
				afterLivePolicyCheckForTest()
			}
			// Rebuild inside the batch critical section. request.action was
			// captured before a sibling settlement and can no longer prove the
			// current policy/action binding.
			initialAction, actionErr := e.buildPermissionActionDescriptor(
				request.ToolName,
				request.Input,
				request.ToolContext,
			)
			if actionErr != nil {
				result = PermissionInteractionResult{
					Decision: PermissionDeny,
					Message:  actionErr.Error(),
				}
			} else if initialAction.PolicySnapshotID != execution.currentPolicyRevision {
				execution.invalid = true
				result = PermissionInteractionResult{
					Decision: PermissionDeny,
					Message:  "project graph permission intent expired",
				}
			} else {
				result = e.settlePermissionInteraction(
					initialAction,
					request.ToolContext,
					result,
				)
			}
			postRevision := e.projectGraphPolicyRevision(request.ToolContext)
			if result.Allowed() && result.settledAction != nil &&
				result.settledAction.PolicySnapshotID == postRevision {
				execution.currentPolicyRevision = postRevision
			} else if postRevision != execution.currentPolicyRevision {
				execution.invalid = true
			}
		}
		execution.mu.Unlock()
	}
	if durable.PlanApproval != nil && result.Allowed() {
		dispatchAction, actionErr := e.buildPermissionActionDescriptor(
			request.ToolName,
			request.Input,
			request.ToolContext,
		)
		if actionErr != nil {
			result = PermissionInteractionResult{
				Decision: PermissionDeny,
				Message: "Plan permission dispatch action rebuild failed: " +
					actionErr.Error(),
			}
		} else {
			result.UpdatedInput = dispatchAction.Input
			result.settledAction = &dispatchAction
		}
	}
	if result.UpdatedInput != nil {
		SetUpdatedInput(ctx, result.UpdatedInput)
	}
	if result.settledAction != nil {
		setSettledPermissionAction(ctx, result.settledAction)
	}
	emitProjectGraphPermissionResolved(
		emit,
		durable,
		result,
		"graph_resume",
	)
	return result.Allowed(), result.Message
}

func emitProjectGraphPermissionResolved(
	emit func(QueryEvent),
	request projectGraphHITLRequest,
	result PermissionInteractionResult,
	reason string,
) {
	if emit == nil {
		return
	}
	emit(QueryEvent{
		Type: EventPermissionResolved,
		PermissionResolved: &PermissionResolvedEvent{
			ToolUseID:    request.RequestID,
			Decision:     string(result.Decision),
			Reason:       reason,
			Message:      result.Message,
			Kind:         request.Kind,
			PlanApproval: clonePlanApprovalDecision(result.PlanApproval),
		},
	})
}

func (e *QueryEngine) reprojectProjectGraphInterrupt(
	request projectGraphHITLRequest,
) {
	if e == nil {
		return
	}
	turnID := "restored-project-graph-" + request.RequestID
	e.decorateRuntimeEvent(turnID, QueryEvent{
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolName:     request.ToolName,
			ToolUseID:    request.RequestID,
			Input:        cloneInputMap(request.Input),
			Message:      request.Message,
			Source:       "project_graph",
			Kind:         request.Kind,
			PlanApproval: request.PlanApproval,
		},
	})
	terminal := Terminal{Reason: TerminalWaitingInput}
	e.decorateRuntimeEvent(turnID, QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &terminal,
	})
}

// PendingProjectGraphPermissionRequest returns the presentation-safe durable
// interrupt currently owned by this session. Callers may render it, but only
// ResolvePermissionInteraction can enqueue a targeted decision.
func (e *QueryEngine) PendingProjectGraphPermissionRequest() (
	PermissionRequestEvent,
	bool,
) {
	if e == nil {
		return PermissionRequestEvent{}, false
	}
	e.mu.Lock()
	checkpoint := e.projectGraphCheckpoint
	e.mu.Unlock()
	if checkpoint == nil {
		return PermissionRequestEvent{}, false
	}
	request, ok := checkpoint.ActiveInterrupt()
	if !ok {
		return PermissionRequestEvent{}, false
	}
	return PermissionRequestEvent{
		ToolName:     request.ToolName,
		ToolUseID:    request.RequestID,
		Input:        cloneInputMap(request.Input),
		Message:      request.Message,
		Source:       "project_graph",
		Kind:         request.Kind,
		PlanApproval: request.PlanApproval,
	}, true
}

func projectGraphInvocationDigest(
	request PermissionPromptRequest,
	scope RuntimeInputScope,
	toolSchemaDigest string,
) string {
	encoded, _ := json.Marshal(struct {
		ToolName         string               `json:"tool_name"`
		ToolUseID        string               `json:"tool_use_id"`
		Input            map[string]any       `json:"input"`
		Scope            RuntimeInputScope    `json:"scope"`
		ToolSchemaDigest string               `json:"tool_schema_digest"`
		PlanApproval     *PlanApprovalRequest `json:"plan_approval,omitempty"`
	}{
		ToolName:         strings.TrimSpace(request.ToolName),
		ToolUseID:        strings.TrimSpace(request.ToolUseID),
		Input:            cloneInputMap(request.Input),
		Scope:            scope,
		ToolSchemaDigest: toolSchemaDigest,
		PlanApproval:     request.PlanApproval,
	})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (e *QueryEngine) projectGraphPolicyRevision(
	toolCtx *ToolUseContext,
) string {
	return e.effectivePolicySnapshot(toolCtx).ID()
}

func projectGraphToolSchemaDigest(
	registry *tools.Registry,
	visible []*schema.ToolInfo,
	toolName string,
) (string, error) {
	var info *schema.ToolInfo
	if registry != nil {
		implementation, ok := registry.Get(toolName)
		if !ok || implementation.Info == nil {
			return "", fmt.Errorf(
				"project graph HITL tool %q is not registered",
				toolName,
			)
		}
		info = implementation.Info
	} else {
		for _, candidate := range visible {
			if candidate != nil && candidate.Name == toolName {
				info = candidate
				break
			}
		}
		if info == nil {
			return "", fmt.Errorf(
				"project graph HITL tool %q has no visible schema",
				toolName,
			)
		}
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf(
			"encode project graph HITL tool schema %q: %w",
			toolName,
			err,
		)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sanitizeProjectGraphHITLInput(
	toolName string,
	input map[string]any,
) map[string]any {
	sanitized := sanitizeProjectGraphHITLValue(
		strings.EqualFold(strings.TrimSpace(toolName), "AskUserQuestion"),
		"",
		input,
		0,
	)
	value, _ := sanitized.(map[string]any)
	encoded, _ := json.Marshal(value)
	if len(encoded) <= maxGraphHITLPresentationBytes {
		return value
	}
	digest := sha256.Sum256(encoded)
	return map[string]any{
		"summary": "permission input omitted from durable presentation",
		"sha256":  hex.EncodeToString(digest[:]),
		"bytes":   len(encoded),
	}
}

func sanitizeProjectGraphHITLValue(
	question bool,
	key string,
	value any,
	depth int,
) any {
	if depth > 8 {
		return "<omitted>"
	}
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	if !question &&
		(strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "password") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "authorization") ||
			strings.Contains(lowerKey, "api_key") ||
			lowerKey == "content") {
		encoded, _ := json.Marshal(value)
		digest := sha256.Sum256(encoded)
		return map[string]any{
			"redacted": true,
			"sha256":   hex.EncodeToString(digest[:]),
			"bytes":    len(encoded),
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, child := range typed {
			out[childKey] = sanitizeProjectGraphHITLValue(
				question,
				childKey,
				child,
				depth+1,
			)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(
				out,
				sanitizeProjectGraphHITLValue(question, key, child, depth+1),
			)
		}
		return out
	default:
		return typed
	}
}

func projectGraphHITLStateForNode(
	ctx context.Context,
) ([]RuntimePermissionDecision, *projectGraphHITLInterruptState, error) {
	wasInterrupted, hasState, state := compose.GetInterruptState[*projectGraphHITLInterruptState](ctx)
	if !wasInterrupted {
		return nil, nil, nil
	}
	if !hasState || state == nil ||
		state.Version != projectGraphHITLStateVersion {
		return nil, nil, fmt.Errorf(
			"project graph HITL interrupt state is unavailable or unsupported",
		)
	}
	if err := validateProjectGraphHITLRequest(state.Request, false); err != nil {
		return nil, nil, err
	}
	isResume, hasData, decision := compose.GetResumeContext[RuntimePermissionDecision](ctx)
	if !isResume {
		return nil, state, nil
	}
	if !hasData {
		return nil, nil, fmt.Errorf(
			"project graph HITL resume has no targeted decision",
		)
	}
	if decision.Version != projectGraphHITLDecisionVersion ||
		decision.RequestID != state.Request.RequestID ||
		decision.InvocationDigest != state.Request.InvocationDigest ||
		decision.PolicyRevision != state.Request.PolicyRevision {
		return nil, nil, fmt.Errorf(
			"project graph HITL resume decision identity mismatch",
		)
	}
	switch decision.Result.Decision {
	case PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways,
		PermissionDeny, PermissionCancelled, PermissionTimedOut:
	default:
		return nil, nil, fmt.Errorf(
			"project graph HITL resume decision is invalid",
		)
	}
	decisions := cloneRuntimePermissionDecisions(state.Decisions)
	for index, existing := range decisions {
		if existing.RequestID != decision.RequestID {
			continue
		}
		if existing.InvocationDigest != decision.InvocationDigest ||
			existing.PolicyRevision != decision.PolicyRevision ||
			!permissionInteractionResultsEqual(
				existing.Result,
				decision.Result,
			) {
			return nil, nil, fmt.Errorf(
				"project graph HITL decision conflicts with prior intent",
			)
		}
		decisions[index] = decision
		return decisions, nil, nil
	}
	decisions = append(decisions, decision)
	return decisions, nil, nil
}

func permissionInteractionResultsEqual(
	left PermissionInteractionResult,
	right PermissionInteractionResult,
) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil &&
		rightErr == nil &&
		bytes.Equal(leftBytes, rightBytes)
}

func probeProjectGraphHITL(
	ctx context.Context,
	runtime *canonicalQueryRuntime,
	toolCalls []schema.ToolCall,
	decisions []RuntimePermissionDecision,
) *projectGraphHITLRequest {
	if runtime == nil ||
		runtime.params.CanUseTool == nil ||
		!runtime.params.ProjectGraphHITLEnabled {
		return nil
	}
	scope := RuntimeInputScope{
		SessionID: runtime.params.SessionID,
	}
	toolCtx := runtime.prepared.toolUseContext
	if toolCtx != nil {
		if strings.TrimSpace(toolCtx.SessionID) != "" {
			scope.SessionID = toolCtx.SessionID
		}
		scope.ThreadID = toolCtx.ThreadID
		scope.AgentID = toolCtx.AgentID
	}
	probe := &projectGraphHITLProbe{
		scope:     scope,
		decisions: cloneRuntimePermissionDecisions(decisions),
	}
	for index := range toolCalls {
		call := &toolCalls[index]
		input, err := projectGraphHITLValidatedInput(runtime, call)
		if err != nil {
			// The canonical tool boundary will return the existing model-visible
			// validation result. Invalid calls must not create an approval.
			continue
		}
		callCtx := withProjectGraphHITLProbe(ctx, probe)
		callCtx = withToolUseID(callCtx, call.ID)
		callCtx = withPermissionPromptEmitter(callCtx, runtime.yield)
		callCtx = withClassifierStatusEmitter(callCtx, runtime.yield)
		_, _ = runtime.params.CanUseTool(
			callCtx,
			call.Function.Name,
			input,
			toolCtx,
		)
		if probe.captured != nil {
			request := cloneProjectGraphHITLRequest(*probe.captured)
			return &request
		}
	}
	return nil
}

func projectGraphHITLValidatedInput(
	runtime *canonicalQueryRuntime,
	call *schema.ToolCall,
) (map[string]any, error) {
	if runtime == nil || call == nil {
		return nil, fmt.Errorf("project graph HITL tool call is missing")
	}
	input, err := parseToolInput(call.Function.Arguments)
	if err != nil {
		return nil, err
	}
	canonicalToolName := strings.TrimSpace(call.Function.Name)
	if runtime.params.ToolRegistry != nil {
		implementation, ok := runtime.params.ToolRegistry.Get(
			call.Function.Name,
		)
		if !ok {
			return nil, fmt.Errorf(
				"project graph HITL tool %q is unavailable",
				call.Function.Name,
			)
		}
		if implementation.Info != nil {
			canonicalToolName = strings.TrimSpace(implementation.Info.Name)
			input = tools.CoerceToolInput(implementation.Info, input)
			if err := tools.ValidateToolInput(
				implementation.Info,
				input,
			); err != nil {
				return nil, err
			}
		}
		if implementation.ValidateInput != nil {
			if err := implementation.ValidateInput(input); err != nil {
				return nil, err
			}
		}
	}
	planDecision := evaluateToolContextPlanPolicy(
		runtime.prepared.toolUseContext,
		runtime.params.ToolRegistry,
		canonicalToolName,
		input,
	)
	if !planDecision.Allowed {
		return nil, fmt.Errorf("%s", planDecision.Reason)
	}
	return cloneInputMap(input), nil
}
