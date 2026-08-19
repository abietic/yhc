// Package acp implements an ACP (Agent Client Protocol) server surface
// that wraps the YHC engine for IDE integration.
package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/hooks"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/buildinfo"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
)

// PermissionTimeout is the default timeout for permission requests to the
// ACP client. If the client does not respond within this duration, the
// permission is denied to avoid blocking the engine indefinitely.
const PermissionTimeout = 60 * time.Second

const codeUnsupportedInput = -32006

func acpSessionTranscriptDir(projectRoot string) string {
	return filepath.Join(projectRoot, identity.ProjectDirName, "transcripts")
}

// engineInitializationMu serializes the compatibility package globals wired by
// tool registration and QueryEngine construction. Query execution remains
// concurrent after each engine has captured its session-scoped dependencies.
var engineInitializationMu sync.Mutex

// Config holds the configuration for the ACP agent.
type Config struct {
	ProviderFlag           string
	ModelFlag              string
	ModelProfileFlag       string
	APIKeyFlag             string
	BaseURLFlag            string
	FallbackModelFlag      string
	ProviderPreflight      bool
	PermissionModeFlag     string
	SandboxProfileFlag     string
	SandboxProfileFlagSet  bool
	ApprovalReviewShadow   bool
	ApprovalReviewProvider string
	ApprovalReviewModel    string
	ApprovalReviewAPIKey   string
	ApprovalReviewBaseURL  string
	ApprovalReviewTimeout  time.Duration
	ApprovalReviewAudit    bool
	ApprovalReviewAuditDir string
	YoloMode               bool
	MaxTurns               int
	CWD                    string
	ToolsFlag              []string
	ToolsFlagSet           bool
	SimpleTools            bool
	// DisableACPAssistantMessageIDs omits the pinned SDK's optional unstable
	// messageId extension without changing canonical assistant content.
	DisableACPAssistantMessageIDs bool
	// DisableACPCommandUpdates disables available_commands_update delivery
	// without changing the shared command registry or dispatch behavior.
	DisableACPCommandUpdates bool
}

// Agent implements the acp.Agent interface, bridging ACP protocol
// to the YHC engine.
type Agent struct {
	// sessionLifecycleMu serializes session creation, restore, fork,
	// close, and durable deletion so an in-flight transition cannot escape the
	// active registry check.
	sessionLifecycleMu    sync.Mutex
	mu                    sync.Mutex
	sessions              map[acpsdk.SessionId]*Session
	sessionRoots          *acpSessionRootLocator
	conn                  *acpsdk.AgentSideConnection
	config                Config
	callID                atomic.Int64
	permissionRegistry    *engine.PermissionCoordinatorRegistry
	approvalReviewAudit   *permission.ReviewAuditStore
	sandboxDiagnosticOnce sync.Once
	sandboxDiagnosticOut  io.Writer
	initialized           bool
	goalNamespace         *acpGoalNamespace

	// permissionTimeout overrides the default PermissionTimeout for testing.
	permissionTimeout       time.Duration
	planPermissionRequestFn func(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error)

	// mockModel is used in tests to inject a mock chat model instead of
	// calling the real provider. Only set in test code.
	mockModel model.BaseChatModel

	// restoreForkEngineFn injects a post-commit fork restore failure in tests.
	restoreForkEngineFn func(
		context.Context,
		acpsdk.SessionId,
		string,
	) (*engine.QueryEngine, *session.ResumedSession, error)
	// createRestoreStagingEngineFn observes or replaces restore-staging engine
	// construction in lifecycle failure tests.
	createRestoreStagingEngineFn func(
		string,
		string,
	) (*engine.QueryEngine, error)
}

// Session holds per-session state for an ACP session.
type Session struct {
	ID        acpsdk.SessionId
	Engine    *engine.QueryEngine
	CreatedAt time.Time
	CWD       string

	mu                     sync.Mutex
	CancelFn               context.CancelFunc
	promptActive           bool
	goalContinuationActive bool
	closed                 bool
	promptWG               sync.WaitGroup
	hookWG                 sync.WaitGroup
	hookCancel             context.CancelFunc
	toolLedger             *acpToolLifecycleLedger
	stateNotify            chan struct{}
	closeOnce              sync.Once

	commandProjectionMu         sync.Mutex
	commandDigest               string
	commandSnapshotWasDelivered bool
	mcpSetupFingerprint         [32]byte
	hasMCPSetup                 bool
}

func newSession(id acpsdk.SessionId, engine *engine.QueryEngine, cwd string) *Session {
	return &Session{
		ID: id, Engine: engine, CreatedAt: time.Now(), CWD: cwd,
		toolLedger:  newACPToolLifecycleLedger(),
		stateNotify: make(chan struct{}, 1),
	}
}

func (s *Session) ensureSignalsLocked() {
	if s.stateNotify == nil {
		s.stateNotify = make(chan struct{}, 1)
	}
}

func signalSession(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Session) beginExclusive(
	cancel context.CancelFunc,
	resetToolLedger bool,
	goalContinuation bool,
) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	s.ensureSignalsLocked()
	if s.closed || s.promptActive {
		s.mu.Unlock()
		return false
	}
	s.promptActive = true
	s.goalContinuationActive = goalContinuation
	s.CancelFn = cancel
	if resetToolLedger {
		s.toolLedger = newACPToolLifecycleLedger()
	}
	s.promptWG.Add(1)
	stateNotify := s.stateNotify
	s.mu.Unlock()
	signalSession(stateNotify)
	return true
}

func (s *Session) beginPrompt(cancel context.CancelFunc) bool {
	return s.beginExclusive(cancel, true, false)
}

func (s *Session) beginGoalContinuation(cancel context.CancelFunc) bool {
	return s.beginExclusive(cancel, true, true)
}

func (s *Session) beginGoalControl() bool {
	return s.beginExclusive(nil, false, false)
}

func (s *Session) beginRead() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.promptWG.Add(1)
	s.mu.Unlock()
	return true
}

func (s *Session) endRead() {
	if s != nil {
		s.promptWG.Done()
	}
}

func (s *Session) endPrompt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ensureSignalsLocked()
	s.CancelFn = nil
	s.promptActive = false
	s.goalContinuationActive = false
	stateNotify := s.stateNotify
	s.mu.Unlock()
	s.promptWG.Done()
	signalSession(stateNotify)
}

func (s *Session) cancelPrompt() (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	if !s.promptActive {
		s.mu.Unlock()
		return false, nil
	}
	cancel := s.CancelFn
	stopErr := s.stopGoalContinuationLocked(
		"ACP Goal continuation cancelled",
	)
	s.CancelFn = nil
	s.mu.Unlock()
	if cancel == nil {
		return false, stopErr
	}
	cancel()
	return true, stopErr
}

func (s *Session) stopGoalContinuationLocked(reason string) error {
	if !s.goalContinuationActive || s.Engine == nil {
		return nil
	}
	// Clearing the marker under Session ownership makes repeated Cancel,
	// request-context, and close paths converge on one durable stop.
	s.goalContinuationActive = false
	return s.Engine.RequestStop(engine.RuntimeStopImmediate, reason)
}

func (s *Session) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.ensureSignalsLocked()
		s.closed = true
		cancel := s.CancelFn
		s.CancelFn = nil
		_ = s.stopGoalContinuationLocked(
			"ACP Session closed during Goal continuation",
		)
		hookCancel := s.hookCancel
		stateNotify := s.stateNotify
		s.mu.Unlock()
		signalSession(stateNotify)
		if cancel != nil {
			cancel()
		}
		if hookCancel != nil {
			hookCancel()
		}
		s.promptWG.Wait()
		s.hookWG.Wait()
		if s.Engine != nil {
			s.Engine.Close()
		}
	})
}

func (a *Agent) startSessionHookRuntime(session *Session) {
	if a == nil || session == nil || session.Engine == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.ensureSignalsLocked()
	if session.closed || session.hookCancel != nil {
		session.mu.Unlock()
		cancel()
		return
	}
	session.hookCancel = cancel
	session.hookWG.Add(1)
	session.mu.Unlock()

	hookEvents := session.Engine.SubscribeAsyncHookEvents()
	go func() {
		defer session.hookWG.Done()
		for {
			select {
			case event, open := <-hookEvents:
				if !open {
					return
				}
				_ = a.streamEvent(ctx, session.ID, event)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// NewAgent creates a new ACP agent with the given configuration.
func NewAgent(cfg Config) (*Agent, error) {
	if cfg.MaxTurns < 0 {
		return nil, fmt.Errorf("max turns must be zero (unlimited) or positive")
	}
	if cfg.ApprovalReviewShadow {
		if strings.TrimSpace(cfg.ApprovalReviewProvider) == "" ||
			strings.TrimSpace(cfg.ApprovalReviewModel) == "" {
			return nil, fmt.Errorf(
				"permission review shadow requires an explicit provider and model",
			)
		}
		if cfg.ApprovalReviewTimeout < 0 {
			return nil, fmt.Errorf("permission review timeout must be positive")
		}
	}
	if !cfg.ApprovalReviewAudit &&
		strings.TrimSpace(cfg.ApprovalReviewAuditDir) != "" {
		return nil, fmt.Errorf(
			"permission review audit directory requires audit opt-in",
		)
	}
	if cfg.ApprovalReviewAudit && !cfg.ApprovalReviewShadow {
		return nil, fmt.Errorf(
			"permission review audit requires permission review shadow",
		)
	}
	var approvalReviewAudit *permission.ReviewAuditStore
	if cfg.ApprovalReviewAudit {
		store, err := permission.NewReviewAuditStore(
			permission.ReviewAuditStoreOptions{
				Dir: cfg.ApprovalReviewAuditDir,
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"initialize permission review audit store: local store unavailable",
			)
		}
		approvalReviewAudit = store
	}
	return &Agent{
		sessions:             make(map[acpsdk.SessionId]*Session),
		sessionRoots:         newACPSessionRootLocator(),
		config:               cfg,
		permissionRegistry:   engine.NewPermissionCoordinatorRegistry(),
		approvalReviewAudit:  approvalReviewAudit,
		sandboxDiagnosticOut: os.Stderr,
	}, nil
}

func (a *Agent) emitExecutionContainmentStartupDiagnostic(eng *engine.QueryEngine) {
	if a == nil || eng == nil {
		return
	}
	a.sandboxDiagnosticOnce.Do(func() {
		code, message := eng.ExecutionContainmentStartupDiagnostic()
		if code == "" || message == "" || a.sandboxDiagnosticOut == nil {
			return
		}
		fmt.Fprintf(a.sandboxDiagnosticOut, "Warning [%s]: %s\n", code, message)
	})
}

func (a *Agent) resolveSandboxSelection(appConfig *config.Config) (*engine.SandboxSelection, error) {
	var userConfig *config.SandboxConfig
	if appConfig != nil {
		userConfig = appConfig.Sandbox
	}
	selection, err := config.ResolveSandbox(config.SandboxSelectionInput{
		Config:        userConfig,
		CLIProfile:    a.config.SandboxProfileFlag,
		CLIProfileSet: a.config.SandboxProfileFlagSet,
	})
	if err != nil {
		return nil, err
	}
	return engine.NewSandboxSelection(
		containment.Profile(selection.GuestProfile),
		containment.SelectionSource(selection.Source),
		selection.ExtraReadRoots,
	)
}

// SetConnection stores the ACP connection for sending notifications to the client.
func (a *Agent) SetConnection(conn *acpsdk.AgentSideConnection) {
	a.conn = conn
}

// Close cancels and joins every active session before closing its engine.
func (a *Agent) Close() {
	if a == nil {
		return
	}
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	a.mu.Lock()
	sessions := make([]*Session, 0, len(a.sessions))
	for id, active := range a.sessions {
		sessions = append(sessions, active)
		delete(a.sessions, id)
	}
	a.mu.Unlock()
	for _, active := range sessions {
		active.close()
	}
}

// Initialize handles the ACP initialize handshake.
func (a *Agent) Initialize(ctx context.Context, p acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	a.mu.Lock()
	if !a.initialized {
		negotiation, err := negotiateACPGoalNamespace(
			p.ClientCapabilities.Meta,
		)
		if err != nil {
			a.mu.Unlock()
			return acpsdk.InitializeResponse{}, err
		}
		a.goalNamespace = negotiation.namespace
		a.initialized = true
	}
	goalNamespace := a.goalNamespace
	a.mu.Unlock()

	title := identity.ProductLongName
	capabilities := acpsdk.AgentCapabilities{
		LoadSession: true,
		PromptCapabilities: acpsdk.PromptCapabilities{
			Image:           true,
			EmbeddedContext: true,
			Audio:           false,
		},
		SessionCapabilities: acpsdk.SessionCapabilities{
			Close:  &acpsdk.SessionCloseCapabilities{},
			List:   &acpsdk.SessionListCapabilities{},
			Resume: &acpsdk.SessionResumeCapabilities{},
			Delete: &acpsdk.SessionDeleteCapabilities{},
		},
	}
	if goalNamespace != nil {
		capabilities.Meta = map[string]any{
			goalNamespace.capabilityKey: map[string]any{
				"version":       goalNamespace.version,
				"notifications": true,
			},
		}
	}
	return acpsdk.InitializeResponse{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		AgentInfo: &acpsdk.Implementation{
			Name:    identity.CommandName,
			Title:   &title,
			Version: buildinfo.Current().Version,
		},
		AgentCapabilities: capabilities,
	}, nil
}

// Authenticate handles authentication (no-op for local agent).
func (a *Agent) Authenticate(ctx context.Context, p acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}

// Logout terminates the authenticated session (no-op for local agent).
func (a *Agent) Logout(ctx context.Context, p acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, nil
}

// CloseSession closes an active session and frees its resources.
func (a *Agent) CloseSession(ctx context.Context, p acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	a.mu.Lock()
	sess, ok := a.sessions[p.SessionId]
	if ok {
		delete(a.sessions, p.SessionId)
	}
	a.mu.Unlock()

	if ok {
		sess.close()
	}
	return acpsdk.CloseSessionResponse{}, nil
}

// ResumeSession resumes an existing session by loading its transcript.
func (a *Agent) ResumeSession(ctx context.Context, p acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	if err := rejectUnsupportedSessionSetup(p.AdditionalDirectories); err != nil {
		return acpsdk.ResumeSessionResponse{}, err
	}
	mcpSetup, err := validateACPSessionMCPSetup(p.McpServers)
	if err != nil {
		return acpsdk.ResumeSessionResponse{}, err
	}
	setupCtx, cancelSetup := acpSessionMCPContext(ctx)
	defer cancelSetup()

	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	sessionID := p.SessionId
	cwd := p.Cwd
	if cwd == "" {
		cwd = a.config.CWD
	}

	// Check if already active in memory.
	a.mu.Lock()
	active, ok := a.sessions[sessionID]
	if ok {
		a.mu.Unlock()
		if len(mcpSetup.servers) > 0 {
			if !active.hasMCPSetup ||
				active.mcpSetupFingerprint != mcpSetup.fingerprint {
				return acpsdk.ResumeSessionResponse{},
					acpSessionMCPConflictRequestError(sessionID)
			}
			manager := active.Engine.GetMCPManager()
			if manager == nil {
				return acpsdk.ResumeSessionResponse{},
					invalidACPMCPSetup("manager_unavailable", 0)
			}
			if err := manager.EnsureSessionServers(setupCtx); err != nil {
				return acpsdk.ResumeSessionResponse{},
					acpSessionMCPRequestError(err)
			}
		}
		if err := a.publishCommandSnapshot(ctx, active, true); err != nil {
			return acpsdk.ResumeSessionResponse{}, err
		}
		a.sessionRoots.remember(sessionID, active.Engine.GetCWD())
		return acpsdk.ResumeSessionResponse{
			ConfigOptions: sessionConfigOptions(ctx, active.Engine),
			Modes:         sessionModeState(active.Engine),
		}, nil
	}
	a.mu.Unlock()

	resumeSource, err := admitACPSessionResume(ctx, sessionID, cwd)
	if err != nil {
		return acpsdk.ResumeSessionResponse{}, err
	}
	eng, resumed, err := a.restoreStagingEngineForSession(ctx, resumeSource)
	if err != nil {
		return acpsdk.ResumeSessionResponse{}, fmt.Errorf(
			"failed to resume session: %w",
			err,
		)
	}
	cwd = eng.GetCWD()
	committed := false
	defer func() {
		if !committed {
			// A prepared commit is retry-only once any monotonic durable owner
			// may have advanced. Close handles both still-staged and incomplete
			// committing engines without adding another persistence write.
			eng.Close()
		}
	}()
	if len(mcpSetup.servers) > 0 {
		if err := eng.PrepareRestoreSessionMCP(
			setupCtx,
			mcpSetup.forCWD(cwd),
		); err != nil {
			return acpsdk.ResumeSessionResponse{},
				acpSessionMCPRequestError(err)
		}
	}

	sess := newSession(sessionID, eng, cwd)
	if len(mcpSetup.servers) > 0 {
		sess.hasMCPSetup = true
		sess.mcpSetupFingerprint = mcpSetup.fingerprint
	}
	if err := a.publishCommandSnapshot(ctx, sess, true); err != nil {
		return acpsdk.ResumeSessionResponse{}, fmt.Errorf(
			"deliver resumed session commands: %w",
			err,
		)
	}
	if err := eng.CommitRestoreStaging(); err != nil {
		return acpsdk.ResumeSessionResponse{}, fmt.Errorf(
			"commit resumed session: %w",
			err,
		)
	}
	committed = true

	a.mu.Lock()
	a.sessions[sessionID] = sess
	a.mu.Unlock()
	a.sessionRoots.remember(sessionID, cwd)
	a.startSessionHookRuntime(sess)
	a.notifyRestoredSession(ctx, sessionID, eng.PlanState(), resumed.Warnings)

	return acpsdk.ResumeSessionResponse{
		ConfigOptions: sessionConfigOptions(ctx, eng),
		Modes:         sessionModeState(eng),
	}, nil
}

// NewSession creates a new agent session with its own QueryEngine.
func (a *Agent) NewSession(ctx context.Context, p acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	if err := rejectUnsupportedSessionSetup(p.AdditionalDirectories); err != nil {
		return acpsdk.NewSessionResponse{}, err
	}
	mcpSetup, err := validateACPSessionMCPSetup(p.McpServers)
	if err != nil {
		return acpsdk.NewSessionResponse{}, err
	}

	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	sessionID := acpsdk.SessionId(uuid.New().String())

	cwd := p.Cwd
	if cwd == "" {
		cwd = a.config.CWD
	}

	setupCtx, cancelSetup := acpSessionMCPContext(ctx)
	defer cancelSetup()
	eng, err := a.createEngineWithSessionMCP(
		setupCtx,
		sessionID,
		mcpSetup,
		cwd,
	)
	if err != nil {
		var setupErr *tools.SessionMCPSetupError
		if errors.As(err, &setupErr) {
			return acpsdk.NewSessionResponse{}, acpSessionMCPRequestError(err)
		}
		return acpsdk.NewSessionResponse{}, fmt.Errorf("failed to create engine: %w", err)
	}

	sess := newSession(sessionID, eng, cwd)
	if len(mcpSetup.servers) > 0 {
		sess.hasMCPSetup = true
		sess.mcpSetupFingerprint = mcpSetup.fingerprint
	}

	a.mu.Lock()
	a.sessions[sessionID] = sess
	a.mu.Unlock()
	a.startSessionHookRuntime(sess)
	if err := a.publishCommandSnapshot(ctx, sess, true); err != nil {
		cleanupErr := a.cleanupFailedNewSession(sess)
		return acpsdk.NewSessionResponse{}, errors.Join(
			fmt.Errorf("deliver new session commands: %w", err),
			cleanupErr,
		)
	}
	a.sessionRoots.remember(sessionID, eng.GetCWD())

	return acpsdk.NewSessionResponse{
		SessionId:     sessionID,
		ConfigOptions: sessionConfigOptions(ctx, eng),
		Modes:         sessionModeState(eng),
	}, nil
}

// Prompt handles an incoming user prompt, driving the query loop and
// streaming results back to the client as SessionUpdate notifications.
func (a *Agent) Prompt(ctx context.Context, p acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	promptInput, err := promptInputFromACP(p.Prompt)
	if err != nil {
		return acpsdk.PromptResponse{}, err
	}
	promptText, err := promptInput.Render()
	if err != nil {
		var validationErr *engine.PromptInputValidationError
		if errors.As(err, &validationErr) {
			return acpsdk.PromptResponse{}, invalidACPInput(
				"prompt",
				validationErr.ReasonCode,
				validationErr.BlockIndex,
			)
		}
		return acpsdk.PromptResponse{}, invalidACPInput(
			"prompt",
			"render_failed",
			-1,
		)
	}

	a.mu.Lock()
	sess, ok := a.sessions[p.SessionId]
	a.mu.Unlock()
	if !ok {
		return acpsdk.PromptResponse{}, fmt.Errorf("session not found: %s", p.SessionId)
	}
	if promptInput.Empty(promptText) {
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
	}

	// Create cancellable context for this prompt
	promptCtx, cancel := context.WithCancel(ctx)
	if !sess.beginPrompt(cancel) {
		cancel()
		return acpsdk.PromptResponse{}, fmt.Errorf("session %s is closed or already processing a prompt", p.SessionId)
	}
	defer sess.endPrompt()
	defer cancel()

	// Emit processing status event
	_ = a.streamStatusEvent(ctx, p.SessionId, "processing", "Processing prompt...")

	if pending, ok := sess.Engine.PendingProjectGraphPermissionRequest(); ok {
		if err := a.resolveProjectGraphPermission(
			promptCtx,
			p.SessionId,
			sess.Engine,
			pending,
		); err != nil {
			return acpsdk.PromptResponse{}, err
		}
		item, claimed, err := sess.Engine.ClaimNextRuntimeItem()
		if err != nil {
			return acpsdk.PromptResponse{}, err
		}
		if !claimed || item.Kind != engine.RuntimeItemPermissionDecision {
			return acpsdk.PromptResponse{}, fmt.Errorf(
				"project graph permission decision was not claimable",
			)
		}
		resumeEvents, _ := sess.Engine.SubmitRuntimeItem(promptCtx, item)
		if _, _, err := a.driveSessionEvents(
			ctx,
			promptCtx,
			sess,
			resumeEvents,
			cancel,
		); err != nil {
			return acpsdk.PromptResponse{}, err
		}
	}

	var events <-chan engine.QueryEvent
	if promptInput.Rich != nil {
		var terminal engine.Terminal
		events, terminal = sess.Engine.SubmitPromptInput(
			promptCtx,
			*promptInput.Rich,
		)
		if terminal.Err != nil {
			if mapped := mapACPPromptAdmissionError(terminal.Err); mapped != nil {
				return acpsdk.PromptResponse{}, mapped
			}
		}
	} else {
		events, _ = sess.Engine.SubmitMessage(promptCtx, promptText)
	}
	terminalReason, _, driveErr := a.driveSessionEvents(
		ctx,
		promptCtx,
		sess,
		events,
		cancel,
	)
	if driveErr != nil {
		if mapped := mapACPPromptAdmissionError(driveErr); mapped != nil {
			return acpsdk.PromptResponse{}, mapped
		}
		return acpsdk.PromptResponse{}, driveErr
	}
	if err := a.publishCommandSnapshot(ctx, sess, false); err != nil {
		return acpsdk.PromptResponse{}, fmt.Errorf(
			"deliver prompt command snapshot: %w",
			err,
		)
	}

	// If the prompt context was canceled (via Cancel notification), report as cancelled
	// regardless of what the engine's terminal reason was, since context cancellation
	// causes model errors that mask the real cause.
	if promptCtx.Err() != nil {
		_ = a.streamStatusEvent(ctx, p.SessionId, "cancelled", "Prompt cancelled")
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonCancelled}, nil
	}

	// Emit completion status
	_ = a.streamStatusEvent(ctx, p.SessionId, "completed", "Turn complete")

	// Build response with session metadata
	resp := acpsdk.PromptResponse{StopReason: mapStopReason(terminalReason)}

	// Attach token usage via the unstable Usage field
	promptTokens, completionTokens := sess.Engine.GetTotalUsage()
	resp.Usage = &acpsdk.Usage{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
		TotalTokens:  promptTokens + completionTokens,
	}

	// Attach additional session metadata via Meta field
	resp.Meta = map[string]any{
		"model":      sess.Engine.GetModelName(),
		"session_id": string(sess.ID),
	}

	return resp, nil
}

// driveSessionEvents is the single ACP turn adapter for ordinary prompts and
// explicit Goal continuations. It retains event-stream ownership through
// durable commit even after client delivery fails.
func (a *Agent) driveSessionEvents(
	deliveryCtx context.Context,
	turnCtx context.Context,
	sess *Session,
	events <-chan engine.QueryEvent,
	cancel context.CancelFunc,
) (engine.TerminalReason, *engine.GoalLifecycleEvent, error) {
	if sess == nil || sess.Engine == nil {
		return "", nil, fmt.Errorf("ACP session engine is unavailable")
	}
	var (
		eventErr           error
		promptAdmissionErr error
		lastGoalEvent      *engine.GoalLifecycleEvent
	)
	captureGoal := func(evt engine.QueryEvent) {
		if evt.Type != engine.EventGoalLifecycle || evt.GoalLifecycle == nil {
			return
		}
		captured := *evt.GoalLifecycle
		lastGoalEvent = &captured
	}
	for {
		terminalReason, currentErr := consumeACPQueryEvents(
			events,
			eventErr,
			func(evt engine.QueryEvent) error {
				captureGoal(evt)
				if evt.Type == engine.EventTerminal &&
					evt.TerminalInfo != nil &&
					evt.TerminalInfo.Err != nil {
					var admissionErr *engine.PromptInputAdmissionError
					if errors.As(evt.TerminalInfo.Err, &admissionErr) {
						promptAdmissionErr = evt.TerminalInfo.Err
					}
				}
				if evt.Type == engine.EventPermissionRequest &&
					evt.PermissionRequest != nil &&
					evt.PermissionRequest.Source == "project_graph" {
					if err := a.resolveProjectGraphPermission(
						turnCtx,
						sess.ID,
						sess.Engine,
						*evt.PermissionRequest,
					); err != nil {
						cancel()
						return err
					}
				}
				if err := a.streamEvent(deliveryCtx, sess.ID, evt); err != nil {
					cancel()
					return err
				}
				return nil
			},
			func(evt engine.QueryEvent, cause error) {
				captureGoal(evt)
				a.settleACPToolLifecycleAfterDeliveryFailure(
					sess,
					evt,
					cause,
				)
				settleACPProjectGraphPermissionAfterDeliveryFailure(
					sess.Engine,
					evt,
					cause,
				)
			},
		)
		eventErr = currentErr
		if terminalReason != engine.TerminalWaitingInput {
			if eventErr == nil && promptAdmissionErr != nil {
				eventErr = promptAdmissionErr
			}
			return terminalReason, lastGoalEvent, eventErr
		}
		item, ok, err := sess.Engine.ClaimNextRuntimeItem()
		if err != nil {
			if eventErr != nil {
				return terminalReason, lastGoalEvent, eventErr
			}
			return terminalReason, lastGoalEvent, err
		}
		if !ok || item.Kind != engine.RuntimeItemPermissionDecision {
			return terminalReason, lastGoalEvent, eventErr
		}
		events, _ = sess.Engine.SubmitRuntimeItem(turnCtx, item)
	}
}

// consumeACPQueryEvents keeps ownership of the engine event stream until the
// producer closes it. A client delivery failure may cancel the turn, but it
// must not orphan the producer while it is still committing transcript or
// runtime-input state.
func consumeACPQueryEvents(
	events <-chan engine.QueryEvent,
	firstErr error,
	handle func(engine.QueryEvent) error,
	afterError func(engine.QueryEvent, error),
) (engine.TerminalReason, error) {
	var terminalReason engine.TerminalReason
	for evt := range events {
		if evt.Type == engine.EventTerminal && evt.TerminalInfo != nil {
			terminalReason = evt.TerminalInfo.Reason
		}
		if firstErr != nil {
			if afterError != nil {
				afterError(evt, firstErr)
			}
			continue
		}
		if handle == nil {
			continue
		}
		if err := handle(evt); err != nil {
			firstErr = err
			if afterError != nil {
				afterError(evt, firstErr)
			}
		}
	}
	return terminalReason, firstErr
}

type acpPermissionInteractionResolver interface {
	ResolvePermissionInteraction(
		string,
		engine.PermissionInteractionResult,
	) bool
}

func settleACPProjectGraphPermissionAfterDeliveryFailure(
	resolver acpPermissionInteractionResolver,
	evt engine.QueryEvent,
	_ error,
) {
	if resolver == nil ||
		evt.Type != engine.EventPermissionRequest ||
		evt.PermissionRequest == nil ||
		evt.PermissionRequest.Source != "project_graph" {
		return
	}
	resolver.ResolvePermissionInteraction(
		evt.PermissionRequest.ToolUseID,
		acpPermissionTerminalResult(
			engine.PermissionPromptRequest{
				PlanApproval: evt.PermissionRequest.PlanApproval,
			},
			engine.PermissionCancelled,
			"ACP client event delivery failed",
		),
	)
}

func (a *Agent) resolveProjectGraphPermission(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	queryEngine *engine.QueryEngine,
	request engine.PermissionRequestEvent,
) error {
	if queryEngine == nil {
		return fmt.Errorf("project graph permission engine is unavailable")
	}
	result := a.makeACPPermissionPrompt(sessionID)(
		ctx,
		engine.PermissionPromptRequest{
			Kind:               request.Kind,
			Attempt:            request.Attempt,
			Source:             request.Source,
			ToolName:           request.ToolName,
			CanonicalToolName:  request.CanonicalToolName,
			ToolUseID:          request.ToolUseID,
			Input:              request.Input,
			Message:            request.Message,
			SessionID:          string(sessionID),
			ThreadID:           queryEngine.ThreadID(),
			AgentID:            queryEngine.AgentID(),
			PlanApproval:       request.PlanApproval,
			DecisionConstraint: request.DecisionConstraint,
		},
	)
	if !queryEngine.ResolvePermissionInteraction(
		request.ToolUseID,
		result,
	) {
		return fmt.Errorf(
			"project graph permission request %q is no longer active",
			request.ToolUseID,
		)
	}
	return nil
}

// Cancel interrupts a running prompt.
func (a *Agent) Cancel(ctx context.Context, p acpsdk.CancelNotification) error {
	a.mu.Lock()
	sess, ok := a.sessions[p.SessionId]
	a.mu.Unlock()
	if !ok {
		return nil
	}
	cancelled, cancelErr := sess.cancelPrompt()
	if !cancelled {
		return nil
	}

	// Ordinary prompts stop through their captured context only. A Goal
	// continuation additionally persists its Goal pause/stop while Session
	// ownership is still held, so a late cancellation cannot target the next
	// turn.
	_ = a.streamStatusEvent(ctx, p.SessionId, "cancelling", "Cancellation requested")
	return cancelErr
}

// ListSessions returns one bounded, cursor-based durable and active page.
func (a *Agent) ListSessions(ctx context.Context, p acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	a.mu.Lock()
	active := make([]session.SessionInfo, 0, len(a.sessions))
	for id, acpSession := range a.sessions {
		if acpSession == nil {
			continue
		}
		sessionCWD := acpSession.CWD
		transcriptDir := acpSessionTranscriptDir(sessionCWD)
		active = append(active, session.SessionInfo{
			SessionID:      string(id),
			CWD:            sessionCWD,
			CreatedAt:      acpSession.CreatedAt,
			LastModified:   acpSession.CreatedAt,
			TranscriptDir:  transcriptDir,
			TranscriptPath: filepath.Join(transcriptDir, string(id)+".jsonl"),
		})
	}
	a.mu.Unlock()

	cwd := a.config.CWD
	if p.Cwd != nil && *p.Cwd != "" {
		cwd = *p.Cwd
	}
	cursor := ""
	if p.Cursor != nil {
		cursor = *p.Cursor
	}
	page, err := session.QuerySessions(session.SessionQuery{
		Scope:                    session.SessionScopeCWD,
		CWD:                      cwd,
		TranscriptDir:            acpSessionTranscriptDir(cwd),
		Cursor:                   cursor,
		Sort:                     session.SortNewestFirst,
		ActiveOverlay:            active,
		BindCandidateGenerations: true,
	})
	if err != nil {
		if errors.Is(err, session.ErrSessionCursorInvalid) {
			return acpsdk.ListSessionsResponse{}, invalidACPInput(
				"session.list.cursor",
				"cursor is malformed, mismatched, or stale",
				-1,
			)
		}
		return acpsdk.ListSessionsResponse{}, err
	}

	sessions := make([]acpsdk.SessionInfo, 0, len(page.Sessions))
	for _, info := range page.Sessions {
		title := info.Summary
		updatedAt := info.LastModified.UTC().Format(time.RFC3339)
		sessionCWD := info.CWD
		if sessionCWD == "" {
			sessionCWD = cwd
		}
		sessions = append(sessions, acpsdk.SessionInfo{
			SessionId: acpsdk.SessionId(info.SessionID),
			Cwd:       sessionCWD,
			Title:     &title,
			UpdatedAt: &updatedAt,
			Meta:      map[string]any{"summary": info.Summary, "gitBranch": info.GitBranch, "tag": info.Tag},
		})
	}
	response := acpsdk.ListSessionsResponse{Sessions: sessions}
	for _, info := range sessions {
		a.sessionRoots.remember(info.SessionId, info.Cwd)
	}
	if page.NextCursor != "" {
		response.NextCursor = stringPointer(page.NextCursor)
	}
	return response, nil
}

// SetSessionConfigOption handles config changes from the ACP client.
func (a *Agent) SetSessionConfigOption(ctx context.Context, p acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil || p.Boolean != nil {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf(
			"session configuration requires exactly one select value",
		)
	}
	sessionID := p.ValueId.SessionId
	configID := string(p.ValueId.ConfigId)

	a.mu.Lock()
	sess, ok := a.sessions[sessionID]
	a.mu.Unlock()
	if !ok {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("session not found: %s", sessionID)
	}
	switch configID {
	case "model":
		if _, err := sess.Engine.ChangeModel(ctx, string(p.ValueId.Value)); err != nil {
			return acpsdk.SetSessionConfigOptionResponse{}, err
		}
	case "effort":
		if _, err := sess.Engine.ChangeReasoningEffort(ctx, string(p.ValueId.Value)); err != nil {
			return acpsdk.SetSessionConfigOptionResponse{}, err
		}
	default:
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("unknown session configuration option %q", configID)
	}
	if err := a.publishCommandSnapshot(ctx, sess, false); err != nil {
		return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf(
			"deliver session command snapshot: %w",
			err,
		)
	}

	return acpsdk.SetSessionConfigOptionResponse{
		ConfigOptions: sessionConfigOptions(ctx, sess.Engine),
	}, nil
}

// SetSessionMode handles permission mode changes from the ACP client.
func (a *Agent) SetSessionMode(ctx context.Context, p acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	a.mu.Lock()
	sess, ok := a.sessions[p.SessionId]
	a.mu.Unlock()
	if !ok {
		return acpsdk.SetSessionModeResponse{}, fmt.Errorf("session not found: %s", p.SessionId)
	}

	if err := sess.Engine.SetPermissionModeConfirmed(
		permission.Mode(p.ModeId),
		false,
	); err != nil {
		return acpsdk.SetSessionModeResponse{}, err
	}
	if a.conn != nil {
		if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
			SessionId: p.SessionId,
			Update: acpsdk.SessionUpdate{CurrentModeUpdate: &acpsdk.SessionCurrentModeUpdate{
				CurrentModeId: acpsdk.SessionModeId(sess.Engine.PermissionMode()),
			}},
		}); err != nil {
			return acpsdk.SetSessionModeResponse{}, err
		}
	}
	if err := a.publishCommandSnapshot(ctx, sess, false); err != nil {
		return acpsdk.SetSessionModeResponse{}, fmt.Errorf(
			"deliver session command snapshot: %w",
			err,
		)
	}
	return acpsdk.SetSessionModeResponse{}, nil
}

func sessionConfigOptions(
	ctx context.Context,
	eng *engine.QueryEngine,
) []acpsdk.SessionConfigOption {
	if eng == nil {
		return nil
	}
	currentModel := eng.GetModelName()
	modelOptions := make(acpsdk.SessionConfigSelectOptionsUngrouped, 0)
	seenModels := make(map[string]struct{})
	appendModel := func(value, name string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seenModels[key]; exists {
			return
		}
		seenModels[key] = struct{}{}
		modelOptions = append(modelOptions, acpsdk.SessionConfigSelectOption{
			Name:  name,
			Value: acpsdk.SessionConfigValueId(value),
		})
	}
	inventory := eng.ModelInventory()
	for _, candidate := range inventory.Entries {
		name := candidate.DisplayName
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("%s:%s", candidate.Provider, candidate.APIModel)
		}
		appendModel(candidate.Selector, name)
	}
	modelCategory := acpsdk.SessionConfigOptionCategoryModel
	modelOption := acpsdk.NewSessionConfigOptionSelect(
		acpsdk.SessionConfigValueId(currentModel),
		acpsdk.SessionConfigSelectOptions{Ungrouped: &modelOptions},
	)
	modelOption.Select.Id = acpsdk.SessionConfigId("model")
	modelOption.Select.Name = "Model"
	modelOption.Select.Category = &modelCategory
	options := []acpsdk.SessionConfigOption{modelOption}

	if supported, _, err := eng.ReasoningEffortCapability(ctx); err == nil && supported {
		effortValues := acpsdk.SessionConfigSelectOptionsUngrouped{
			{Name: "Provider default", Value: "default"},
			{Name: "Low", Value: "low"},
			{Name: "Medium", Value: "medium"},
			{Name: "High", Value: "high"},
			{Name: "Max", Value: "max"},
		}
		currentEffort := eng.ReasoningEffort()
		if currentEffort == "" {
			currentEffort = "default"
		}
		effortCategory := acpsdk.SessionConfigOptionCategoryThoughtLevel
		effortOption := acpsdk.NewSessionConfigOptionSelect(
			acpsdk.SessionConfigValueId(currentEffort),
			acpsdk.SessionConfigSelectOptions{Ungrouped: &effortValues},
		)
		effortOption.Select.Id = acpsdk.SessionConfigId("effort")
		effortOption.Select.Name = "Reasoning effort"
		effortOption.Select.Category = &effortCategory
		options = append(options, effortOption)
	}
	return options
}

func sessionModeState(eng *engine.QueryEngine) *acpsdk.SessionModeState {
	if eng == nil {
		return nil
	}
	descriptions := []struct {
		mode permission.Mode
		name string
		desc string
	}{
		{permission.ModeDefault, "Default", "Prompt when no safe permission path applies."},
		{permission.ModePlan, "Plan", "Restrict tools to the active Plan capability."},
		{permission.ModeAcceptEdits, "Accept edits", "Allow matching edits in configured roots."},
		{permission.ModeDontAsk, "Don't ask", "Deny instead of presenting permission prompts."},
		{permission.ModeAuto, "Auto", "Use the permission classifier before prompting."},
	}
	modes := make([]acpsdk.SessionMode, 0, len(descriptions))
	for _, item := range descriptions {
		description := item.desc
		modes = append(modes, acpsdk.SessionMode{
			Id:          acpsdk.SessionModeId(item.mode),
			Name:        item.name,
			Description: &description,
		})
	}
	return &acpsdk.SessionModeState{
		AvailableModes: modes,
		CurrentModeId:  acpsdk.SessionModeId(eng.PermissionMode()),
	}
}

// --- Internal helpers ---

func (a *Agent) resolveModelRuntime(
	ctx context.Context,
	configSources *config.ConfigSources,
) (model.BaseChatModel, string, string, engine.ModelResolver, error) {
	appConfig := configSources.Effective
	fallbackModel := strings.TrimSpace(a.config.FallbackModelFlag)
	if fallbackModel == "" {
		fallbackModel = strings.TrimSpace(os.Getenv("PROV_FALLBACK_MODEL"))
	}
	if fallbackModel == "" && appConfig != nil {
		fallbackModel = strings.TrimSpace(appConfig.FallbackModel)
	}
	if a.mockModel != nil {
		modelName := strings.TrimSpace(a.config.ModelFlag)
		if modelName == "" && appConfig != nil {
			modelName = appConfig.Model
		}
		return a.mockModel, modelName, fallbackModel, mockACPModelResolver(modelName), nil
	}

	resolution := provider.ResolveInput{
		Explicit: provider.Config{
			Provider: provider.Provider(a.config.ProviderFlag),
			Model:    a.config.ModelFlag,
			APIKey:   a.config.APIKeyFlag,
			BaseURL:  a.config.BaseURLFlag,
		},
	}
	if appConfig != nil {
		resolution.Configured = provider.Config{
			Provider:     provider.Provider(appConfig.Provider),
			Model:        appConfig.Model,
			BaseURL:      appConfig.APIBaseURL,
			ModelAliases: appConfig.ModelAliases,
		}
	}
	runtime, err := provider.NewConfiguredRuntime(ctx, provider.ConfiguredRuntimeOptions{
		Sources:              configSources,
		ExplicitModelProfile: a.config.ModelProfileFlag,
		ExplicitLegacyFields: a.explicitLegacyRuntimeFields(),
		LegacyFallbackModel:  fallbackModel,
		Resolution:           resolution,
		Preflight:            a.config.ProviderPreflight,
	})
	if err != nil {
		return nil, "", "", nil, err
	}
	for _, diagnostic := range runtime.PortfolioDiagnostics() {
		fmt.Fprintf(os.Stderr, "Warning [%s]: %s", diagnostic.Code, diagnostic.Message)
		if len(diagnostic.Keys) > 0 {
			fmt.Fprintf(os.Stderr, " (keys: %s)", strings.Join(diagnostic.Keys, ", "))
		}
		if diagnostic.Path != "" {
			fmt.Fprintf(os.Stderr, " (path: %q)", diagnostic.Path)
		}
		fmt.Fprintln(os.Stderr)
	}
	if fallbackModel != "" && !runtime.UsesNamedPortfolio() {
		fallbackConfig, prepareErr := runtime.PrepareModel(ctx, fallbackModel)
		if prepareErr != nil {
			return nil, "", "", nil, fmt.Errorf("fallback model %q: %w", fallbackModel, prepareErr)
		}
		if fallbackConfig.Provider == runtime.Main.Provider && fallbackConfig.Model == runtime.Main.Model {
			return nil, "", "", nil, fmt.Errorf("fallback model cannot resolve to the same provider and model as the main model")
		}
	}
	return runtime.ChatModel, runtime.Main.Model, fallbackModel, runtime, nil
}

func (a *Agent) explicitLegacyRuntimeFields() []string {
	fields := make([]string, 0, 5)
	if strings.TrimSpace(a.config.ProviderFlag) != "" {
		fields = append(fields, "--provider")
	}
	if strings.TrimSpace(a.config.ModelFlag) != "" {
		fields = append(fields, "--model")
	}
	if strings.TrimSpace(a.config.APIKeyFlag) != "" {
		fields = append(fields, "--api-key")
	}
	if strings.TrimSpace(a.config.BaseURLFlag) != "" {
		fields = append(fields, "--base-url")
	}
	if strings.TrimSpace(a.config.FallbackModelFlag) != "" {
		fields = append(fields, "--fallback-model")
	}
	return fields
}

func (a *Agent) resolveApprovalReviewer(
	ctx context.Context,
) (*provider.ApprovalReviewerRuntime, error) {
	if !a.config.ApprovalReviewShadow {
		return nil, nil
	}
	if strings.TrimSpace(a.config.ApprovalReviewProvider) == "" ||
		strings.TrimSpace(a.config.ApprovalReviewModel) == "" {
		return nil, fmt.Errorf(
			"permission review shadow requires an explicit provider and model",
		)
	}
	timeout := a.config.ApprovalReviewTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	runtime, err := provider.NewApprovalReviewer(ctx, provider.ApprovalReviewerOptions{
		Provider: provider.Provider(a.config.ApprovalReviewProvider),
		Model:    a.config.ApprovalReviewModel,
		APIKey:   a.config.ApprovalReviewAPIKey,
		BaseURL:  a.config.ApprovalReviewBaseURL,
		Timeout:  timeout,
	})
	if err != nil {
		safeError := err.Error()
		if secret := strings.TrimSpace(a.config.ApprovalReviewAPIKey); secret != "" {
			safeError = strings.ReplaceAll(safeError, secret, "[REDACTED]")
		}
		return nil, fmt.Errorf(
			"initialize separate permission reviewer: %s",
			safeError,
		)
	}
	fmt.Fprintf(
		os.Stderr,
		"Permission reviewer shadow enabled (non-authoritative): provider=%s model=%s data_boundary=%s timeout=%s\n",
		runtime.Route.Provider,
		runtime.Route.Model,
		runtime.Route.DataBoundary,
		timeout,
	)
	return runtime, nil
}

func mockACPModelResolver(defaultModel string) engine.ModelResolver {
	return engine.ModelResolverFunc(func(modelSpec string) (provider.ResolvedConfig, error) {
		resolved := strings.TrimSpace(modelSpec)
		if resolved == "" {
			resolved = strings.TrimSpace(defaultModel)
		}
		entry := modelcaps.DefaultRegistry().Lookup(resolved)
		if entry == nil {
			return provider.ResolvedConfig{}, fmt.Errorf("model %q is not in the mock provider inventory", resolved)
		}
		providerID, err := provider.NormalizeProvider(provider.Provider(modelcaps.DetectProvider(entry.ModelID)))
		if err != nil {
			return provider.ResolvedConfig{}, err
		}
		return provider.ResolvedConfig{Config: provider.Config{
			Provider: providerID,
			Model:    entry.ModelID,
		}}, nil
	})
}

func (a *Agent) createEngine(
	sessionID acpsdk.SessionId,
	requestedCWD ...string,
) (*engine.QueryEngine, error) {
	return a.createEngineWithSessionMCP(
		context.Background(),
		sessionID,
		nil,
		requestedCWD...,
	)
}

func (a *Agent) createEngineWithSessionMCP(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	setup *acpSessionMCPSetup,
	requestedCWD ...string,
) (*engine.QueryEngine, error) {
	cwd := a.config.CWD
	if len(requestedCWD) > 0 && strings.TrimSpace(requestedCWD[0]) != "" {
		cwd = requestedCWD[0]
	}

	configSources, err := config.LoadConfigSources(cwd)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	appConfig := configSources.Effective
	sandboxSelection, err := a.resolveSandboxSelection(appConfig)
	if err != nil {
		return nil, fmt.Errorf("sandbox selection: %w", err)
	}

	chatModel, modelName, fallbackModel, modelResolver, err := a.resolveModelRuntime(ctx, configSources)
	if err != nil {
		return nil, fmt.Errorf("model init: %w", err)
	}
	approvalReviewer, err := a.resolveApprovalReviewer(ctx)
	if err != nil {
		return nil, err
	}

	engineInitializationMu.Lock()
	defer engineInitializationMu.Unlock()

	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	executionBindings, err := engine.ResolveExecutionBindings(ctx, cwd, commands.EntrypointACP, sandboxSelection)
	if err != nil {
		return nil, fmt.Errorf("resolve execution bindings: %w", err)
	}
	_ = tools.InitSkills(cwd)
	tools.WebFetchSideModel = chatModel
	var mcpManager *tools.MCPToolManager
	if setup != nil && len(setup.servers) > 0 {
		mcpManager, err = tools.PrepareSessionMCPManagerWithBinding(
			ctx,
			cwd,
			reg,
			setup.forCWD(cwd),
			executionBindings.StdioMCP(),
		)
		if err != nil {
			return nil, err
		}
	}

	permMode := a.resolvePermissionMode(appConfig)
	maxTurns := a.config.MaxTurns
	toolSelection := a.toolSelection()

	systemPrompt := "You are a helpful AI assistant with access to tools. Use the available tools to accomplish tasks. When a tool would help answer a question or complete a task, call it directly rather than describing what you would do."
	if appConfig.CustomSystemPrompt != "" {
		systemPrompt = appConfig.CustomSystemPrompt
	}

	engineCfg := engine.QueryEngineConfig{
		SessionID:                 string(sessionID),
		ThreadID:                  string(sessionID),
		CWD:                       cwd,
		CustomSystemPrompt:        systemPrompt,
		MaxTurns:                  maxTurns,
		ChatModel:                 chatModel,
		ToolRegistry:              reg,
		ToolSelection:             toolSelection,
		SimpleTools:               a.config.SimpleTools,
		MemoryProjectRoot:         cwd,
		EnablePersistentMemory:    !a.config.SimpleTools,
		Model:                     modelName,
		FallbackModel:             fallbackModel,
		ModelResolver:             modelResolver,
		PromptCapabilityResolver:  engine.DefaultPromptCapabilityResolver(),
		PermissionMode:            permMode,
		SandboxSelection:          sandboxSelection,
		ExecutionBindings:         executionBindings,
		PermissionRegistry:        a.permissionRegistry,
		PermissionProjectRoot:     cwd,
		RootSessionID:             string(sessionID),
		HookExecutor:              hooks.NewExecutor(),
		EnableLongSessionServices: true,
		CommandEntrypoint:         commands.EntrypointACP,
		GoalCapability:            a.acpGoalCapabilityConfig(appConfig),
	}
	if mcpManager != nil {
		engineCfg.MCPManager = mcpManager
		engineCfg.OwnsMCPManager = true
	}
	if approvalReviewer != nil {
		engineCfg.ApprovalReviewShadow = true
		engineCfg.ApprovalReviewer = approvalReviewer.Reviewer
		engineCfg.ApprovalReviewerRoute = approvalReviewer.Route
		engineCfg.ApprovalReviewTimeout = a.config.ApprovalReviewTimeout
		if engineCfg.ApprovalReviewTimeout <= 0 {
			engineCfg.ApprovalReviewTimeout = 8 * time.Second
		}
	}
	if a.approvalReviewAudit != nil {
		engineCfg.ApprovalReviewAudit = a.approvalReviewAudit
	}

	// Keep the prompt adapter installed even when the initial mode is bypass;
	// a later typed mode transition must not leave default/plan mode without an
	// interaction owner.
	engineCfg.PermissionPrompt = a.makeACPPermissionPrompt(sessionID)
	engineCfg.RepeatedToolCallPrompt = a.makeACPRepeatedToolCallPrompt(sessionID)

	eng := engine.NewQueryEngine(engineCfg)
	a.emitExecutionContainmentStartupDiagnostic(eng)
	return eng, nil
}

func (a *Agent) resolvePermissionMode(appConfig *config.Config) permission.Mode {
	if a.config.YoloMode {
		return permission.ModeBypassPermissions
	}
	if a.config.PermissionModeFlag != "" {
		return permission.Mode(a.config.PermissionModeFlag)
	}
	if appConfig != nil && appConfig.PermissionMode != "" {
		return permission.Mode(appConfig.PermissionMode)
	}
	return permission.ModeDefault
}

func (a *Agent) acpGoalCapabilityConfig(
	appConfig *config.Config,
) *engine.GoalCapabilityConfig {
	capability := &engine.GoalCapabilityConfig{
		Enabled:       true,
		ACPNegotiated: a.acpGoalCapabilityNegotiated(),
	}
	if appConfig == nil || appConfig.Goal == nil {
		return capability
	}
	if appConfig.Goal.Enabled != nil {
		capability.Enabled = *appConfig.Goal.Enabled
	}
	if appConfig.Goal.DefaultTokenBudget != nil {
		budget := *appConfig.Goal.DefaultTokenBudget
		capability.DefaultTokenBudget = &budget
	}
	return capability
}

func (a *Agent) toolSelection() *tools.ToolSelection {
	if !a.config.ToolsFlagSet {
		return nil
	}
	selection := tools.ParseToolSelection(a.config.ToolsFlag)
	return &selection
}

func (a *Agent) makeACPPermissionPrompt(sessionID acpsdk.SessionId) engine.PermissionPromptFn {
	return func(ctx context.Context, request engine.PermissionPromptRequest) engine.PermissionInteractionResult {
		if a.conn == nil {
			return acpPermissionTerminalResult(
				request,
				engine.PermissionDeny,
				"no ACP connection",
			)
		}
		if err := a.ensureACPToolStartBeforePermission(
			ctx,
			sessionID,
			request.ToolUseID,
			request.ToolName,
		); err != nil {
			return acpPermissionTerminalResult(
				request,
				engine.PermissionDeny,
				err.Error(),
			)
		}
		if request.PlanApproval != nil {
			return a.requestACPPlanApproval(ctx, sessionID, request)
		}

		callID := acpsdk.ToolCallId(request.ToolUseID)
		if strings.TrimSpace(request.ToolUseID) == "" {
			callID = acpsdk.ToolCallId(fmt.Sprintf("call_%d", a.callID.Add(1)))
		}
		title := fmt.Sprintf("Execute: %s", request.ToolName)
		var toolContent []acpsdk.ToolCallContent
		options := acpPermissionOptions(request)

		// Apply permission timeout to avoid indefinite blocking.
		timeout := a.permissionTimeout
		if timeout == 0 {
			timeout = PermissionTimeout
		}
		permCtx, permCancel := context.WithTimeout(ctx, timeout)
		defer permCancel()

		resp, err := a.conn.RequestPermission(permCtx, acpsdk.RequestPermissionRequest{
			SessionId: sessionID,
			ToolCall: acpsdk.ToolCallUpdate{
				ToolCallId: callID,
				Title:      acpsdk.Ptr(title),
				Content:    toolContent,
			},
			Options: options,
		})
		if err != nil {
			if ctx.Err() != nil {
				return acpPermissionTerminalResult(
					request,
					engine.PermissionCancelled,
					"permission request cancelled",
				)
			}
			if permCtx.Err() == context.DeadlineExceeded {
				return acpPermissionTerminalResult(
					request,
					engine.PermissionTimedOut,
					"permission request timed out",
				)
			}
			return acpPermissionTerminalResult(
				request,
				engine.PermissionDeny,
				fmt.Sprintf("permission request failed: %v", err),
			)
		}

		if resp.Outcome.Selected != nil {
			optID := string(resp.Outcome.Selected.OptionId)
			switch optID {
			case "allow":
				return engine.PermissionInteractionResult{Decision: engine.PermissionAllowOnce}
			case "allow_always":
				if request.DecisionConstraint == engine.PermissionAllowOnceOnly {
					return acpPermissionTerminalResult(
						request,
						engine.PermissionDeny,
						"permission decision is not allowed by request constraint",
					)
				}
				return engine.PermissionInteractionResult{Decision: engine.PermissionAllowAlways}
			}
		}
		if resp.Outcome.Cancelled != nil {
			return acpPermissionTerminalResult(
				request,
				engine.PermissionCancelled,
				"permission request cancelled",
			)
		}
		return acpPermissionTerminalResult(
			request,
			engine.PermissionDeny,
			"user denied permission",
		)
	}
}

func acpPermissionOptions(request engine.PermissionPromptRequest) []acpsdk.PermissionOption {
	options := []acpsdk.PermissionOption{{Kind: acpsdk.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"}}
	if request.DecisionConstraint != engine.PermissionAllowOnceOnly {
		options = append(options, acpsdk.PermissionOption{Kind: acpsdk.PermissionOptionKindAllowAlways, Name: "Always Allow", OptionId: "allow_always"})
	}
	return append(options, acpsdk.PermissionOption{Kind: acpsdk.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"})
}

func acpPermissionTerminalResult(
	request engine.PermissionPromptRequest,
	decision engine.PermissionInteractionDecision,
	message string,
) engine.PermissionInteractionResult {
	result := engine.PermissionInteractionResult{
		Decision: decision,
		Message:  message,
	}
	if request.PlanApproval != nil {
		result.PlanApproval = &engine.PlanApprovalDecision{
			RequestID:    request.PlanApproval.RequestID,
			PlanRevision: request.PlanApproval.PlanRevision,
			Outcome:      engine.PlanApprovalCancel,
			TargetMode:   permission.ModePlan,
		}
	}
	return result
}

func (a *Agent) requestACPPlanApproval(ctx context.Context, sessionID acpsdk.SessionId, request engine.PermissionPromptRequest) engine.PermissionInteractionResult {
	if strings.TrimSpace(request.ToolUseID) == "" {
		return acpPermissionTerminalResult(
			request,
			engine.PermissionDeny,
			"ACP Plan permission tool identity is incomplete",
		)
	}
	callID := acpsdk.ToolCallId(request.ToolUseID)
	plan := request.PlanApproval
	planBytes, digest, err := engine.ReadPlanReviewSnapshot(plan.PlanFileIdentity)
	if err != nil {
		return acpPermissionTerminalResult(request, engine.PermissionDeny, err.Error())
	}
	timeout := a.permissionTimeout
	if timeout == 0 {
		timeout = PermissionTimeout
	}
	deadlineCtx, cancel := context.WithDeadline(ctx, time.Now().Add(timeout))
	defer cancel()
	title := fmt.Sprintf("Approve plan revision %d: %s", plan.PlanRevision, plan.PlanFileIdentity)
	content := []acpsdk.ToolCallContent{acpsdk.ToolContent(acpsdk.TextBlock(string(planBytes)))}
	for {
		resp, err := a.requestPlanPermission(deadlineCtx, acpsdk.RequestPermissionRequest{SessionId: sessionID, ToolCall: acpsdk.ToolCallUpdate{ToolCallId: callID, Title: acpsdk.Ptr(title), Content: content}, Options: acpPlanApprovalOptions(plan)})
		if err != nil {
			return acpPlanTerminalResult(ctx, request, err)
		}
		if resp.Outcome.Selected == nil {
			return acpPermissionTerminalResult(request, engine.PermissionCancelled, "plan approval cancelled")
		}
		optionID := string(resp.Outcome.Selected.OptionId)
		if optionID != "plan_bypass" &&
			(optionID != "plan_manual" || plan.ReturnMode != permission.ModeBypassPermissions) {
			return acpPlanApprovalResult(plan, optionID, digest)
		}
		confirmed, back, terminal := a.acpPlanBypassConfirmation(
			deadlineCtx,
			sessionID,
			callID,
			request,
		)
		if back {
			continue
		}
		if !confirmed {
			return terminal
		}
		return engine.PermissionInteractionResult{Decision: engine.PermissionAllowOnce, PlanApproval: &engine.PlanApprovalDecision{RequestID: plan.RequestID, PlanRevision: plan.PlanRevision, Outcome: engine.PlanApprovalApprove, ReviewedPlanDigest: digest, TargetMode: permission.ModeBypassPermissions, Confirmed: true}}
	}
}

func (a *Agent) requestPlanPermission(ctx context.Context, request acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	if a.planPermissionRequestFn != nil {
		return a.planPermissionRequestFn(ctx, request)
	}
	return a.conn.RequestPermission(ctx, request)
}

func acpPlanTerminalResult(parent context.Context, request engine.PermissionPromptRequest, err error) engine.PermissionInteractionResult {
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		return acpPermissionTerminalResult(request, engine.PermissionTimedOut, "permission request timed out")
	}
	if parent.Err() != nil || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), context.Canceled.Error()) {
		return acpPermissionTerminalResult(request, engine.PermissionCancelled, "permission request cancelled")
	}
	return acpPermissionTerminalResult(request, engine.PermissionDeny, fmt.Sprintf("permission request failed: %v", err))
}

func acpPlanApprovalOptions(
	request *engine.PlanApprovalRequest,
) []acpsdk.PermissionOption {
	returnMode := permission.ModeDefault
	if request != nil && request.ReturnMode != "" {
		returnMode = request.ReturnMode
	}
	targets := engine.PlanApprovalTargetModes(returnMode)
	options := make([]acpsdk.PermissionOption, 0, len(targets)+1)
	for _, target := range targets {
		option := acpsdk.PermissionOption{
			Kind: acpsdk.PermissionOptionKindAllowOnce,
			Name: fmt.Sprintf("Approve with previous permissions (%s)", target), OptionId: "plan_manual",
		}
		if target == permission.ModeAcceptEdits {
			option.Name, option.OptionId = "Approve and auto-accept edits", "plan_accept_edits"
		}
		if target == permission.ModeBypassPermissions {
			option.Name, option.OptionId = "Approve and bypass permissions", "plan_bypass"
		}
		options = append(options, option)
	}
	return append(options, acpsdk.PermissionOption{
		Kind: acpsdk.PermissionOptionKindRejectOnce,
		Name: "Reject and keep planning", OptionId: "plan_reject",
	})
}

func acpPlanApprovalResult(
	request *engine.PlanApprovalRequest,
	optionID string,
	reviewedPlanDigest string,
) engine.PermissionInteractionResult {
	decision := &engine.PlanApprovalDecision{
		RequestID:    request.RequestID,
		PlanRevision: request.PlanRevision,
		Outcome:      engine.PlanApprovalCancel,
		TargetMode:   permission.ModePlan,
	}
	result := engine.PermissionInteractionResult{
		Decision:     engine.PermissionDeny,
		Message:      "User rejected the plan.",
		PlanApproval: decision,
	}
	switch optionID {
	case "plan_previous", "plan_manual":
		decision.Outcome = engine.PlanApprovalApprove
		decision.Confirmed = false
		decision.TargetMode = request.ReturnMode
		if decision.TargetMode == "" {
			decision.TargetMode = permission.ModeDefault
		}
		decision.ReviewedPlanDigest = reviewedPlanDigest
		result.Decision = engine.PermissionAllowOnce
		result.Message = ""
	case "plan_accept_edits":
		decision.Outcome = engine.PlanApprovalApprove
		decision.Confirmed = false
		decision.TargetMode = permission.ModeAcceptEdits
		decision.ReviewedPlanDigest = reviewedPlanDigest
		result.Decision = engine.PermissionAllowOnce
		result.Message = ""
	case "plan_bypass":
		result.Message = "bypass requires a second confirmation"
	}
	return result
}

func (a *Agent) acpPlanBypassConfirmation(ctx context.Context, sessionID acpsdk.SessionId, callID acpsdk.ToolCallId, request engine.PermissionPromptRequest) (bool, bool, engine.PermissionInteractionResult) {
	resp, err := a.requestPlanPermission(ctx, acpsdk.RequestPermissionRequest{SessionId: sessionID, ToolCall: acpsdk.ToolCallUpdate{ToolCallId: callID, Title: acpsdk.Ptr("Confirm bypass permissions")}, Options: []acpsdk.PermissionOption{{Kind: acpsdk.PermissionOptionKindAllowOnce, Name: "Confirm", OptionId: "plan_bypass_confirm"}, {Kind: acpsdk.PermissionOptionKindRejectOnce, Name: "Back", OptionId: "plan_bypass_back"}}})
	if err != nil {
		return false, false, acpPlanTerminalResult(ctx, request, err)
	}
	if resp.Outcome.Selected == nil {
		return false, false, acpPermissionTerminalResult(request, engine.PermissionCancelled, "bypass confirmation cancelled")
	}
	switch string(resp.Outcome.Selected.OptionId) {
	case "plan_bypass_confirm":
		return true, false, engine.PermissionInteractionResult{}
	case "plan_bypass_back":
		return false, true, engine.PermissionInteractionResult{}
	default:
		return false, false, acpPermissionTerminalResult(request, engine.PermissionCancelled, "bypass confirmation cancelled")
	}
}

func (a *Agent) makeACPRepeatedToolCallPrompt(sessionID acpsdk.SessionId) engine.RepeatedToolCallPromptFn {
	return func(ctx context.Context, toolName, toolUseID string, attempt int, toolCtx *engine.ToolUseContext) (bool, string) {
		if a.conn == nil {
			return false, "no ACP connection"
		}
		targetSessionID := sessionID
		if toolCtx != nil && strings.TrimSpace(toolCtx.SessionID) != "" {
			targetSessionID = acpsdk.SessionId(toolCtx.SessionID)
		}
		if err := a.ensureACPToolStartBeforePermission(
			ctx,
			targetSessionID,
			toolUseID,
			toolName,
		); err != nil {
			return false, err.Error()
		}
		callID := acpsdk.ToolCallId(toolUseID)
		if strings.TrimSpace(toolUseID) == "" {
			callID = acpsdk.ToolCallId(fmt.Sprintf("call_%d", a.callID.Add(1)))
		}
		timeout := a.permissionTimeout
		if timeout == 0 {
			timeout = PermissionTimeout
		}
		promptCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		response, err := a.conn.RequestPermission(promptCtx, acpsdk.RequestPermissionRequest{
			SessionId: targetSessionID,
			ToolCall: acpsdk.ToolCallUpdate{
				ToolCallId: callID,
				Title:      acpsdk.Ptr(fmt.Sprintf("Repeated tool call (%d): %s", attempt, toolName)),
			},
			Options: repeatedToolPermissionOptions(),
		})
		if err != nil {
			if promptCtx.Err() != nil {
				return false, "repeated tool override request timed out"
			}
			return false, fmt.Sprintf("repeated tool override request failed: %v", err)
		}
		if response.Outcome.Selected != nil && string(response.Outcome.Selected.OptionId) == "run_once" {
			return true, "one-call override granted"
		}
		return false, "user chose to stop and change strategy"
	}
}

func repeatedToolPermissionOptions() []acpsdk.PermissionOption {
	return []acpsdk.PermissionOption{
		{Kind: acpsdk.PermissionOptionKindAllowOnce, Name: "Run Once", OptionId: "run_once"},
		{Kind: acpsdk.PermissionOptionKindRejectOnce, Name: "Stop and Change Strategy", OptionId: "stop"},
	}
}

func (a *Agent) streamEvent(ctx context.Context, sessionID acpsdk.SessionId, evt engine.QueryEvent) error {
	if a.conn == nil {
		return nil
	}
	if evt.Type == engine.EventCanonicalProjection {
		return a.projectCanonicalProjection(
			ctx,
			sessionID,
			evt.CanonicalProjection,
		)
	}

	switch evt.Type {
	case engine.EventAssistant:
		// Canonical assistant deltas are the only ACP assistant text producer.
		return nil

	case engine.EventCommandResult:
		if evt.CommandResult != nil {
			if evt.CommandResult.Status == engine.CommandResultSucceeded &&
				(evt.CommandResult.Action == commands.ActionChangeModel ||
					evt.CommandResult.Action == commands.ActionSetEffort) {
				a.mu.Lock()
				sess := a.sessions[sessionID]
				a.mu.Unlock()
				if sess != nil {
					if err := a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
						SessionId: sessionID,
						Update: acpsdk.SessionUpdate{ConfigOptionUpdate: &acpsdk.SessionConfigOptionUpdate{
							ConfigOptions: sessionConfigOptions(ctx, sess.Engine),
						}},
					}); err != nil {
						return err
					}
				}
			}
			if evt.CommandResult.Output == "" {
				return nil
			}
			return a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
				SessionId: sessionID,
				Update:    acpsdk.UpdateAgentMessageText(evt.CommandResult.Output),
			})
		}

	case engine.EventPlanStateTransition:
		if evt.PlanStateTransition != nil {
			return a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
				SessionId: sessionID,
				Update: acpsdk.SessionUpdate{CurrentModeUpdate: &acpsdk.SessionCurrentModeUpdate{
					CurrentModeId: acpsdk.SessionModeId(evt.PlanStateTransition.PermissionMode),
				}},
			})
		}

	case engine.EventPermissionRequest:
		// Permission requests are handled in the CanUseTool callback.
		// Emit a status event so the client knows a permission is in progress.
		if evt.PermissionRequest != nil {
			if evt.PermissionRequest.Kind == "repeated_tool" {
				return a.streamStatusEvent(ctx, sessionID, "waiting_for_repeated_tool_override",
					fmt.Sprintf("Repeated tool call (%d): %s", evt.PermissionRequest.Attempt, evt.PermissionRequest.ToolName))
			}
			return a.streamStatusEvent(ctx, sessionID, "waiting_for_permission",
				fmt.Sprintf("Permission needed: %s", evt.PermissionRequest.ToolName))
		}

	case engine.EventPermissionResolved:
		if evt.PermissionResolved != nil {
			resolved := evt.PermissionResolved
			decision := strings.TrimSpace(resolved.Decision)
			if resolved.Kind == "repeated_tool" {
				status := "repeated_tool_" + decision
				if decision == "" {
					status = "repeated_tool_resolved"
				}
				message := strings.TrimSpace(resolved.Message)
				if message == "" {
					message = "Repeated tool call resolved"
				}
				return a.streamStatusEvent(ctx, sessionID, status, message)
			}
			status := "permission_resolved"
			if decision != "" {
				status = "permission_" + decision
			}
			message := strings.TrimSpace(resolved.Message)
			if message == "" {
				message = strings.TrimSpace(resolved.Reason)
			}
			if message == "" {
				message = "Permission resolved"
			}
			return a.streamStatusEvent(ctx, sessionID, status, message)
		}

	case engine.EventHookStatus:
		if evt.HookStatus != nil {
			return a.streamStatusEvent(ctx, sessionID, "hook_"+evt.HookStatus.Phase,
				evt.HookStatus.StatusMessage)
		}

	case engine.EventHookResponse:
		if evt.HookResponse != nil {
			response := evt.HookResponse
			message := response.StatusMessage
			if message == "" {
				message = fmt.Sprintf("Hook %s: %s", response.HookName, response.Outcome)
			}
			status := response.Outcome
			if response.Phase == "running" {
				status = "running"
			}
			return a.streamStatusEvent(ctx, sessionID, "hook_"+status, message)
		}

	case engine.EventMaxTurnsReached:
		if evt.MaxTurnsInfo != nil {
			return a.streamStatusEvent(ctx, sessionID, "max_turns_reached",
				fmt.Sprintf("Max turns reached: %d/%d", evt.MaxTurnsInfo.TurnCount, evt.MaxTurnsInfo.MaxTurns))
		}

	case engine.EventCompactBoundary:
		return a.streamStatusEvent(ctx, sessionID, "compaction", "Context compacted")

	case engine.EventUserInterruption:
		return a.streamStatusEvent(ctx, sessionID, "interrupted", "User interrupted")

	case engine.EventTaskProgress:
		if evt.TaskProgress != nil {
			return a.streamStatusEvent(ctx, sessionID, "task_progress",
				fmt.Sprintf("Agent %s: %s", evt.TaskProgress.TaskID, evt.TaskProgress.Description))
		}

	case engine.EventTaskLifecycle:
		if evt.TaskLifecycle != nil {
			return a.streamStatusEvent(ctx, sessionID, "task_lifecycle",
				fmt.Sprintf("Task %s: %s (%s)", evt.TaskLifecycle.TaskID, evt.TaskLifecycle.Subject, evt.TaskLifecycle.Status))
		}

	case engine.EventClassifierStatus:
		if evt.ClassifierStatus != nil && evt.ClassifierStatus.Phase == engine.ClassifierStatusChecking {
			return a.streamStatusEvent(ctx, sessionID, "classifier_checking",
				fmt.Sprintf("Checking permission for: %s", evt.ClassifierStatus.ToolName))
		}

	case engine.EventPermissionReview:
		if evt.PermissionReview != nil {
			status, message := permissionReviewACPStatus(evt.PermissionReview)
			if status != "" {
				return a.streamStatusEvent(ctx, sessionID, status, message)
			}
		}

	case engine.EventModelAttempt:
		if notice := modelFallbackNotice(evt.ModelAttempt); notice != "" {
			return a.streamStatusEvent(
				ctx,
				sessionID,
				"model_fallback",
				notice,
			)
		}

	case engine.EventAttachment:
		if evt.AttachmentMessage != nil && evt.AttachmentMessage.Content != "" {
			content := evt.AttachmentMessage.Content
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			return a.streamStatusEvent(ctx, sessionID, "attachment", content)
		}
	case engine.EventTerminal:
		// Terminal events are captured in the Prompt handler for the response.
		// Emit an error-class status if the terminal reason indicates failure.
		if evt.TerminalInfo != nil && evt.TerminalInfo.Err != nil {
			return a.streamStatusEvent(ctx, sessionID, "error",
				fmt.Sprintf("Error: %v", evt.TerminalInfo.Err))
		}
	}

	return nil
}

func permissionReviewACPStatus(review *engine.PermissionReviewEvent) (string, string) {
	if review == nil {
		return "", ""
	}
	tool := permissionReviewACPToken(review.CanonicalTool, "tool")
	switch review.Phase {
	case engine.PermissionReviewChecking:
		return "permission_review_checking",
			fmt.Sprintf("Advisory permission review checking: %s", tool)
	case engine.PermissionReviewCompleted:
		decision := permissionReviewACPToken(review.Decision, "unknown")
		reason := permissionReviewACPToken(review.ReasonCode, "unknown")
		return "permission_review_completed", fmt.Sprintf(
			"Advisory permission review completed: %s %s (%s)",
			tool,
			decision,
			reason,
		)
	case engine.PermissionReviewUnavailable:
		reason := permissionReviewACPToken(review.ReasonCode, "unavailable")
		return "permission_review_unavailable", fmt.Sprintf(
			"Advisory permission review unavailable: %s (%s)",
			tool,
			reason,
		)
	default:
		return "", ""
	}
}

func permissionReviewACPToken(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return fallback
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '.' {
			continue
		}
		return fallback
	}
	return value
}

func modelFallbackNotice(attempt *engine.ModelAttemptEvent) string {
	if attempt == nil ||
		attempt.Phase != engine.ModelAttemptStarted ||
		attempt.AttemptIndex <= 0 ||
		attempt.SwitchCount <= 0 ||
		!isSafeModelProfileID(attempt.Profile) {
		return ""
	}
	return fmt.Sprintf(
		"Model fallback: profile %s after overload (switch %d)",
		attempt.Profile,
		attempt.SwitchCount,
	)
}

func isSafeModelProfileID(profile string) bool {
	if len(profile) == 0 || len(profile) > 64 ||
		profile[0] < 'a' || profile[0] > 'z' {
		return false
	}
	for index := 1; index < len(profile); index++ {
		character := profile[index]
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// mapStopReason converts engine terminal reasons to ACP stop reasons.
func mapStopReason(reason engine.TerminalReason) acpsdk.StopReason {
	switch reason {
	case engine.TerminalMaxTurns:
		return acpsdk.StopReasonMaxTurnRequests
	case engine.TerminalAbortedStreaming, engine.TerminalAbortedTools:
		return acpsdk.StopReasonCancelled
	default:
		return acpsdk.StopReasonEndTurn
	}
}

type acpPromptInput struct {
	Legacy engine.PromptInput
	Rich   *engine.UntrustedPromptInput
}

func (input acpPromptInput) Render() (string, error) {
	return input.Legacy.Render()
}

func (input acpPromptInput) Empty(rendered string) bool {
	return input.Rich == nil && rendered == ""
}

func promptInputFromACP(blocks []acpsdk.ContentBlock) (acpPromptInput, error) {
	legacy := engine.PromptInput{
		Blocks: make([]engine.PromptInputBlock, 0, len(blocks)),
	}
	richParts := make([]engine.UntrustedPromptPart, 0, len(blocks))
	hasRich := false
	for index, block := range blocks {
		if acpContentBlockVariants(block) != 1 {
			return acpPromptInput{}, invalidACPInput(
				"prompt",
				"invalid_block_union",
				index,
			)
		}
	}

	for index, block := range blocks {
		switch {
		case block.Text != nil:
			text := block.Text.Text
			legacy.Blocks = append(legacy.Blocks, engine.PromptInputBlock{
				Text: &text,
			})
			richParts = append(richParts, engine.NewPromptTextPart(text))
		case block.ResourceLink != nil:
			resource := promptResourceLinkFromACP(*block.ResourceLink)
			legacy.Blocks = append(legacy.Blocks, engine.PromptInputBlock{
				ResourceLink: promptResourceLinkFromACP(
					*block.ResourceLink,
				),
			})
			richParts = append(
				richParts,
				engine.NewPromptResourceLinkPart(*resource),
			)
			hasRich = true
		case block.Image != nil:
			richParts = append(
				richParts,
				engine.NewPromptImagePartWithAnnotations(
					block.Image.Data,
					block.Image.MimeType,
					engine.PromptImageDetailAuto,
					promptAnnotationsFromACP(block.Image.Annotations),
				),
			)
			hasRich = true
		case block.Audio != nil:
			return acpPromptInput{}, unsupportedACPPromptInput(
				"audio",
				"capability_unsupported",
				index,
				"",
				"",
			)
		case block.Resource != nil:
			resource := block.Resource.Resource
			switch {
			case resource.TextResourceContents != nil &&
				resource.BlobResourceContents == nil:
				richParts = append(
					richParts,
					engine.NewPromptEmbeddedTextPart(
						engine.PromptEmbeddedTextResource{
							URI:      resource.TextResourceContents.Uri,
							MIMEType: resource.TextResourceContents.MimeType,
							Text:     resource.TextResourceContents.Text,
							Annotations: promptAnnotationsFromACP(
								block.Resource.Annotations,
							),
						},
					),
				)
			case resource.TextResourceContents == nil &&
				resource.BlobResourceContents != nil:
				mimeType := ""
				if resource.BlobResourceContents.MimeType != nil {
					mimeType = *resource.BlobResourceContents.MimeType
				}
				richParts = append(
					richParts,
					engine.NewPromptEmbeddedBlobPart(
						engine.PromptEmbeddedBlobResource{
							URI:        resource.BlobResourceContents.Uri,
							MIMEType:   mimeType,
							Base64Data: resource.BlobResourceContents.Blob,
							Detail:     engine.PromptImageDetailAuto,
							Annotations: promptAnnotationsFromACP(
								block.Resource.Annotations,
							),
						},
					),
				)
			default:
				return acpPromptInput{}, invalidACPInput(
					"prompt",
					"invalid_embedded_resource_union",
					index,
				)
			}
			hasRich = true
		}
	}
	if !hasRich {
		return acpPromptInput{Legacy: legacy}, nil
	}
	rich := engine.NewUntrustedPromptInput(richParts...)
	if err := engine.ValidateUntrustedPromptInputMetadata(rich); err != nil {
		if mapped := mapACPPromptAdmissionError(err); mapped != nil {
			return acpPromptInput{}, mapped
		}
		return acpPromptInput{}, invalidACPInput(
			"prompt",
			"metadata_invalid",
			-1,
		)
	}
	return acpPromptInput{Legacy: legacy, Rich: &rich}, nil
}

func acpContentBlockVariants(block acpsdk.ContentBlock) int {
	variants := 0
	for _, present := range []bool{
		block.Text != nil,
		block.Image != nil,
		block.Audio != nil,
		block.ResourceLink != nil,
		block.Resource != nil,
	} {
		if present {
			variants++
		}
	}
	return variants
}

func promptResourceLinkFromACP(
	resource acpsdk.ContentBlockResourceLink,
) *engine.PromptResourceLink {
	result := &engine.PromptResourceLink{
		URI:         resource.Uri,
		Name:        resource.Name,
		Title:       resource.Title,
		Description: resource.Description,
		MIMEType:    resource.MimeType,
		Size:        resource.Size,
	}
	if resource.Annotations == nil ||
		len(resource.Annotations.Audience) == 0 &&
			resource.Annotations.LastModified == nil &&
			resource.Annotations.Priority == nil {
		return result
	}
	audience := make([]string, len(resource.Annotations.Audience))
	for index, role := range resource.Annotations.Audience {
		audience[index] = string(role)
	}
	result.Annotations = &engine.PromptResourceAnnotations{
		Audience:     audience,
		LastModified: resource.Annotations.LastModified,
		Priority:     resource.Annotations.Priority,
	}
	return result
}

func promptAnnotationsFromACP(
	annotations *acpsdk.Annotations,
) *engine.PromptResourceAnnotations {
	if annotations == nil ||
		len(annotations.Audience) == 0 &&
			annotations.LastModified == nil &&
			annotations.Priority == nil {
		return nil
	}
	audience := make([]string, len(annotations.Audience))
	for index, role := range annotations.Audience {
		audience[index] = string(role)
	}
	return &engine.PromptResourceAnnotations{
		Audience:     audience,
		LastModified: annotations.LastModified,
		Priority:     annotations.Priority,
	}
}

func rejectUnsupportedSessionSetup(
	additionalDirectories []string,
) error {
	if len(additionalDirectories) > 0 {
		return unsupportedACPInput("session.additionalDirectories")
	}
	return nil
}

func unsupportedACPInput(input string) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    codeUnsupportedInput,
		Message: "Unsupported input",
		Data:    map[string]any{"input": input},
	}
}

func unsupportedACPPromptInput(
	kind string,
	reason string,
	blockIndex int,
	providerName string,
	modelName string,
) *acpsdk.RequestError {
	data := map[string]any{
		"input":  acpPromptInputName(kind),
		"reason": reason,
	}
	if blockIndex >= 0 {
		data["block"] = blockIndex
	}
	if providerName != "" {
		data["provider"] = providerName
	}
	if modelName != "" {
		data["model"] = modelName
	}
	return &acpsdk.RequestError{
		Code:    codeUnsupportedInput,
		Message: "Unsupported input",
		Data:    data,
	}
}

func mapACPPromptAdmissionError(err error) error {
	var admissionErr *engine.PromptInputAdmissionError
	if !errors.As(err, &admissionErr) {
		return nil
	}
	switch admissionErr.ReasonCode {
	case "capability_unknown", "capability_unsupported", "route_unknown":
		return unsupportedACPPromptInput(
			admissionErr.PartKind,
			admissionErr.ReasonCode,
			admissionErr.PartIndex,
			admissionErr.Provider,
			admissionErr.Model,
		)
	case "media_persistence_failed", "engine_closed", "canceled":
		return nil
	default:
		return invalidACPInput(
			acpPromptInputName(admissionErr.PartKind),
			admissionErr.ReasonCode,
			admissionErr.PartIndex,
		)
	}
}

func acpPromptInputName(kind string) string {
	switch kind {
	case "image":
		return "prompt.image"
	case "embedded_blob", "embedded_text":
		return "prompt.embeddedResource"
	case "resource_link":
		return "prompt.resourceLink"
	case "audio":
		return "prompt.audio"
	default:
		return "prompt"
	}
}

func invalidACPInput(
	input string,
	reason string,
	blockIndex int,
) *acpsdk.RequestError {
	data := map[string]any{
		"input":  input,
		"reason": reason,
	}
	if blockIndex >= 0 {
		data["block"] = blockIndex
	}
	return acpsdk.NewInvalidParams(data)
}

func acpSessionConflictRequestError(
	sessionID acpsdk.SessionId,
) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeSessionConflict,
		Message: "Session already active",
		Data:    map[string]any{"sessionId": string(sessionID)},
	}
}

func acpSessionRootConflictRequestError(
	sessionID acpsdk.SessionId,
) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeSessionConflict,
		Message: "Session project root conflict",
		Data:    map[string]any{"sessionId": string(sessionID)},
	}
}

func acpSessionMCPConflictRequestError(
	sessionID acpsdk.SessionId,
) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeSessionConflict,
		Message: "Session MCP setup conflict",
		Data: map[string]any{
			"sessionId": string(sessionID),
			"reason":    "mcp_setup_mismatch",
		},
	}
}

func acpSessionNotFoundRequestError(
	sessionID acpsdk.SessionId,
) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeSessionNotFound,
		Message: "Session not found",
		Data:    map[string]any{"sessionId": string(sessionID)},
	}
}

func acpLegacySessionImportRequiredRequestError(
	sessionID acpsdk.SessionId,
) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    CodeLegacySessionImportRequired,
		Message: "legacy_session_import_required",
		Data:    map[string]any{"sessionId": string(sessionID)},
	}
}

func admitACPSessionResume(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	cwd string,
) (session.SessionInfo, error) {
	info, err := session.AdmitDefaultSessionResume(ctx, cwd, string(sessionID))
	if errors.Is(err, session.ErrLegacySessionImportRequired) {
		return session.SessionInfo{},
			acpLegacySessionImportRequiredRequestError(sessionID)
	}
	if errors.Is(err, os.ErrNotExist) {
		return session.SessionInfo{}, acpSessionNotFoundRequestError(sessionID)
	}
	return info, err
}

// Ensure Agent satisfies the acp.Agent interface at compile time.
var _ acpsdk.Agent = (*Agent)(nil)

// Ensure Agent satisfies the optional AgentLoader interface.
var _ acpsdk.AgentLoader = (*Agent)(nil)

// LoadSession loads an existing session by ID and registers it in memory.
// Implements the acpsdk.AgentLoader interface.
func (a *Agent) LoadSession(ctx context.Context, p acpsdk.LoadSessionRequest) (acpsdk.LoadSessionResponse, error) {
	if err := rejectUnsupportedSessionSetup(p.AdditionalDirectories); err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	mcpSetup, err := validateACPSessionMCPSetup(p.McpServers)
	if err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	setupCtx, cancelSetup := acpSessionMCPContext(ctx)
	defer cancelSetup()

	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	sessionID := p.SessionId
	cwd := p.Cwd
	if cwd == "" {
		cwd = a.config.CWD
	}

	a.mu.Lock()
	_, active := a.sessions[sessionID]
	if active {
		a.mu.Unlock()
		return acpsdk.LoadSessionResponse{},
			acpSessionConflictRequestError(sessionID)
	}
	a.mu.Unlock()

	resumeSource, err := admitACPSessionResume(ctx, sessionID, cwd)
	if err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	snapshot, err := session.LoadSessionReplaySnapshot(
		ctx,
		session.ResumeOptions{
			SessionID:  resumeSource.SessionID,
			SessionDir: resumeSource.TranscriptDir,
			ProjectDir: resumeSource.CWD,
		},
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return acpsdk.LoadSessionResponse{},
				acpSessionNotFoundRequestError(sessionID)
		}
		return acpsdk.LoadSessionResponse{}, fmt.Errorf(
			"validate load replay: %w",
			err,
		)
	}
	replay, err := buildACPReplayProjection(
		snapshot,
		!a.config.DisableACPAssistantMessageIDs,
	)
	if err != nil {
		var requestErr *acpsdk.RequestError
		if errors.As(err, &requestErr) {
			return acpsdk.LoadSessionResponse{}, requestErr
		}
		return acpsdk.LoadSessionResponse{}, fmt.Errorf(
			"project load replay: %w",
			err,
		)
	}

	eng, _, err := a.restoreStagingEngineForSession(ctx, resumeSource)
	if err != nil {
		return acpsdk.LoadSessionResponse{}, fmt.Errorf(
			"restore staged session: %w",
			err,
		)
	}
	cwd = eng.GetCWD()
	committed := false
	defer func() {
		if !committed {
			// See ResumeSession: Close is the truthful cleanup for both a
			// write-free staged failure and a partially advanced commit.
			eng.Close()
		}
	}()
	if len(mcpSetup.servers) > 0 {
		if err := eng.PrepareRestoreSessionMCP(
			setupCtx,
			mcpSetup.forCWD(cwd),
		); err != nil {
			return acpsdk.LoadSessionResponse{},
				acpSessionMCPRequestError(err)
		}
	}

	sess := newSession(sessionID, eng, cwd)
	if len(mcpSetup.servers) > 0 {
		sess.hasMCPSetup = true
		sess.mcpSetupFingerprint = mcpSetup.fingerprint
	}

	if err := a.deliverACPReplay(ctx, sessionID, replay); err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	if err := a.publishRestoredSessionState(ctx, sess); err != nil {
		return acpsdk.LoadSessionResponse{}, err
	}
	if err := a.publishCommandSnapshot(ctx, sess, true); err != nil {
		return acpsdk.LoadSessionResponse{}, fmt.Errorf(
			"deliver loaded session commands: %w",
			err,
		)
	}
	if err := eng.CommitRestoreStaging(); err != nil {
		return acpsdk.LoadSessionResponse{}, fmt.Errorf(
			"commit loaded session: %w",
			err,
		)
	}
	committed = true

	a.mu.Lock()
	a.sessions[sessionID] = sess
	a.mu.Unlock()
	a.sessionRoots.remember(sessionID, cwd)
	a.startSessionHookRuntime(sess)

	return acpsdk.LoadSessionResponse{
		ConfigOptions: sessionConfigOptions(ctx, eng),
		Modes:         sessionModeState(eng),
	}, nil
}

// UnstableDeleteSession deletes an inactive session through the shared session
// service. Active sessions must be closed explicitly before their durable
// state can be deleted.
func (a *Agent) UnstableDeleteSession(ctx context.Context, p acpsdk.UnstableDeleteSessionRequest) (acpsdk.UnstableDeleteSessionResponse, error) {
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	sessionID := p.SessionId

	// Hold the session registry lock across the active check and deletion so a
	// concurrent ACP load/resume cannot register the same session between them.
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, active := a.sessions[sessionID]; active {
		return acpsdk.UnstableDeleteSessionResponse{}, fmt.Errorf(
			"cannot delete active session %q",
			sessionID,
		)
	}

	root, _, ambiguous := a.sessionRoots.resolve(sessionID, a.config.CWD)
	if ambiguous {
		return acpsdk.UnstableDeleteSessionResponse{},
			acpSessionRootConflictRequestError(sessionID)
	}
	transcriptDir := acpSessionTranscriptDir(root)
	if _, err := session.DeleteSession(session.DeleteOptions{
		SessionID: string(sessionID),
		Dir:       transcriptDir,
	}); err != nil {
		// Preserve the existing idempotent ACP delete behavior for a missing
		// durable session. Other validation and filesystem errors remain
		// visible to the client.
		if errors.Is(err, os.ErrNotExist) {
			a.sessionRoots.forget(sessionID, root)
			return acpsdk.UnstableDeleteSessionResponse{}, nil
		}
		return acpsdk.UnstableDeleteSessionResponse{}, fmt.Errorf(
			"failed to delete session: %w",
			err,
		)
	}
	a.sessionRoots.forget(sessionID, root)

	return acpsdk.UnstableDeleteSessionResponse{}, nil
}

// streamStatusEvent sends a status notification to the client as an
// agent message chunk containing a status annotation. This uses the
// standard agent_message_chunk update with Meta to carry status info
// since the ACP protocol does not have a dedicated status event type.
func (a *Agent) streamStatusEvent(ctx context.Context, sessionID acpsdk.SessionId, status, message string) error {
	if a.conn == nil {
		return nil
	}
	// Use extension notification for status events. This keeps status events
	// separate from content and is gracefully ignored by clients that don't
	// support the extension.
	return a.conn.NotifyExtension(ctx, "_session/status", map[string]any{
		"sessionId": string(sessionID),
		"status":    status,
		"message":   message,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// createEngineForSession creates a QueryEngine pre-configured to load an
// existing session's transcript from disk. This is used by both ResumeSession
// and LoadSession.
func (a *Agent) createEngineForSession(sessionID, cwd string) (*engine.QueryEngine, error) {
	return a.createEngineForSessionWithConstructor(
		sessionID,
		cwd,
		engine.NewQueryEngine,
	)
}

func (a *Agent) createRestoreStagingEngineForSession(
	sessionID string,
	cwd string,
) (*engine.QueryEngine, error) {
	if a.createRestoreStagingEngineFn != nil {
		return a.createRestoreStagingEngineFn(sessionID, cwd)
	}
	return a.createEngineForSessionWithConstructor(
		sessionID,
		cwd,
		engine.NewRestoreStagingQueryEngine,
	)
}

func (a *Agent) createEngineForSessionWithConstructor(
	sessionID string,
	cwd string,
	construct func(engine.QueryEngineConfig) *engine.QueryEngine,
) (*engine.QueryEngine, error) {
	if construct == nil {
		return nil, errors.New("query engine constructor is unavailable")
	}
	configSources, err := config.LoadConfigSources(cwd)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	appConfig := configSources.Effective
	sandboxSelection, err := a.resolveSandboxSelection(appConfig)
	if err != nil {
		return nil, fmt.Errorf("sandbox selection: %w", err)
	}

	ctx := context.Background()
	chatModel, modelName, fallbackModel, modelResolver, err := a.resolveModelRuntime(ctx, configSources)
	if err != nil {
		return nil, fmt.Errorf("model init: %w", err)
	}
	approvalReviewer, err := a.resolveApprovalReviewer(ctx)
	if err != nil {
		return nil, err
	}

	engineInitializationMu.Lock()
	defer engineInitializationMu.Unlock()

	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	executionBindings, err := engine.ResolveExecutionBindings(ctx, cwd, commands.EntrypointACP, sandboxSelection)
	if err != nil {
		return nil, fmt.Errorf("resolve execution bindings: %w", err)
	}
	_ = tools.InitSkills(cwd)
	tools.WebFetchSideModel = chatModel

	permMode := a.resolvePermissionMode(appConfig)
	maxTurns := a.config.MaxTurns
	toolSelection := a.toolSelection()

	systemPrompt := "You are a helpful AI assistant with access to tools. Use the available tools to accomplish tasks. When a tool would help answer a question or complete a task, call it directly rather than describing what you would do."
	if appConfig.CustomSystemPrompt != "" {
		systemPrompt = appConfig.CustomSystemPrompt
	}

	transcriptDir := acpSessionTranscriptDir(cwd)

	engineCfg := engine.QueryEngineConfig{
		SessionID:                 sessionID,
		TranscriptDir:             transcriptDir,
		CWD:                       cwd,
		CustomSystemPrompt:        systemPrompt,
		MaxTurns:                  maxTurns,
		ChatModel:                 chatModel,
		ToolRegistry:              reg,
		ToolSelection:             toolSelection,
		SimpleTools:               a.config.SimpleTools,
		MemoryProjectRoot:         cwd,
		EnablePersistentMemory:    !a.config.SimpleTools,
		Model:                     modelName,
		FallbackModel:             fallbackModel,
		ModelResolver:             modelResolver,
		PromptCapabilityResolver:  engine.DefaultPromptCapabilityResolver(),
		PermissionMode:            permMode,
		SandboxSelection:          sandboxSelection,
		ExecutionBindings:         executionBindings,
		PermissionRegistry:        a.permissionRegistry,
		PermissionProjectRoot:     cwd,
		RootSessionID:             sessionID,
		HookExecutor:              hooks.NewExecutor(),
		EnableLongSessionServices: true,
		CommandEntrypoint:         commands.EntrypointACP,
		GoalCapability:            a.acpGoalCapabilityConfig(appConfig),
	}
	if approvalReviewer != nil {
		engineCfg.ApprovalReviewShadow = true
		engineCfg.ApprovalReviewer = approvalReviewer.Reviewer
		engineCfg.ApprovalReviewerRoute = approvalReviewer.Route
		engineCfg.ApprovalReviewTimeout = a.config.ApprovalReviewTimeout
		if engineCfg.ApprovalReviewTimeout <= 0 {
			engineCfg.ApprovalReviewTimeout = 8 * time.Second
		}
	}
	if a.approvalReviewAudit != nil {
		engineCfg.ApprovalReviewAudit = a.approvalReviewAudit
	}

	engineCfg.PermissionPrompt = a.makeACPPermissionPrompt(acpsdk.SessionId(sessionID))
	engineCfg.RepeatedToolCallPrompt = a.makeACPRepeatedToolCallPrompt(acpsdk.SessionId(sessionID))

	eng := construct(engineCfg)
	a.emitExecutionContainmentStartupDiagnostic(eng)
	return eng, nil
}

func (a *Agent) restoreEngineForSession(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	cwd string,
) (*engine.QueryEngine, *session.ResumedSession, error) {
	source, err := admitACPSessionResume(ctx, sessionID, cwd)
	if err != nil {
		return nil, nil, err
	}
	// Resume is a read boundary. Validate the stored session before constructing
	// an engine because QueryEngine.Close persists a checkpoint; closing an
	// engine after a failed restore must not create or rewrite the requested
	// transcript.
	if _, err := session.ResumeSession(ctx, session.ResumeOptions{
		SessionID:        source.SessionID,
		SessionDir:       source.TranscriptDir,
		ProjectDir:       source.CWD,
		ValidateMessages: true,
	}); err != nil {
		return nil, nil, err
	}

	eng, err := a.createEngineForSession(source.SessionID, source.CWD)
	if err != nil {
		return nil, nil, err
	}
	resumed, err := eng.SessionService().ResumeInfo(ctx, source)
	if err != nil {
		eng.Close()
		return nil, nil, err
	}
	return eng, resumed, nil
}

func (a *Agent) restoreStagingEngineForSession(
	ctx context.Context,
	source session.SessionInfo,
) (*engine.QueryEngine, *session.ResumedSession, error) {
	eng, err := a.createRestoreStagingEngineForSession(
		source.SessionID,
		source.CWD,
	)
	if err != nil {
		return nil, nil, err
	}
	resumed, err := eng.SessionService().ResumeInfo(ctx, source)
	if err != nil {
		abortErr := eng.AbortRestoreStaging()
		return nil, nil, errors.Join(err, abortErr)
	}
	return eng, resumed, nil
}

func (a *Agent) notifyRestoredSession(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	planState engine.PlanState,
	warnings []string,
) {
	message := "session restored; plan_phase=" + string(planState.Phase)
	if len(warnings) > 0 {
		message += fmt.Sprintf("; recovery_warnings=%d", len(warnings))
	}
	_ = a.streamStatusEvent(ctx, sessionID, "restored", message)
}
