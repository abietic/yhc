package tui

import (
	contextPkg "context"
	jsonPkg "encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	enginemcp "github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/engine/onboarding"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/tui/attachments"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/abietic/yhc/internal/tui/terminalcap"
	"github.com/abietic/yhc/internal/tui/vim"
	"github.com/abietic/yhc/tools"
)

const streamBatchWindow = time.Second / 30

// Focus is retained for API compatibility but always FocusEditor in the
// reference layout (no split focus — scroll via hotkeys while typing).
type Focus int

const (
	FocusEditor Focus = iota
)

// AppState tracks the top-level UI state.
type AppState int

const (
	StateWelcome AppState = iota // initial welcome/logo screen (fresh sessions only)
	StateChat
	StatePermission
	StateResume
	StateExpand          // dedicated scrollable expand view
	StateTaskPanel       // scrollable canonical TaskExplorer panel
	StateHelp            // help overlay showing keybindings and commands
	StateSearch          // search overlay for finding text in conversation
	StateBypassConfirm   // bypass permissions confirmation dialog
	StateMCPApproval     // MCP server approval dialog
	StateMessageSelect   // message selection mode for rewrite/branch
	StateModelPicker     // model picker overlay for browsing/selecting models
	StateBackgroundTasks // background tasks management panel (Ctrl+B)
	StateMCPSettings     // MCP servers settings/browser panel (/mcp)
	StateAgentWizard     // agent creation/edit wizard overlay
	StateTeams           // team members panel (/team)
	StateCommandPalette  // command palette overlay (Ctrl+K)
	StatePlanApproval    // plan approval dialog (ExitPlanMode)
	StateAskUser         // question picker dialog (AskUserQuestion)
	StateAgentPicker     // searchable leader/sub-agent thread picker
)

type InputMode int

const (
	InputNormal InputMode = iota
	InputCommand
	InputShell
)

type commandPaletteSubmission struct {
	id                 uint64
	command            string
	queryID            uint64
	clipboardRequestID uint64
}

// App is the main Bubble Tea model for the TUI.
type App struct {
	// Dimensions
	width, height int
	layout        layout

	// State
	focus             Focus
	state             AppState
	dialogs           *appDialogStack
	inputMode         InputMode
	permMode          permission.Mode
	vimModel          vim.Model
	commandHints      []*commands.Command
	commandHintIdx    int      // -1 = no selection, 0..len-1 = selected hint
	fileHints         []string // file path completions
	fileHintIdx       int      // -1 = no selection
	mentionHints      []composerMentionHint
	mentionHintIdx    int
	mentionIndex      composerMentionIndex
	commandRegistry   *commands.Registry
	styles            Styles
	renderEnvironment RenderEnvironment

	// Components
	textarea        textarea.Model
	chat            *ChatView
	dialog          *PermissionDialog
	resume          *ResumeDialog
	help            *HelpOverlay
	search          *SearchOverlay
	msgSelector     *MessageSelector
	mcpApproval     *MCPApprovalDialog
	modelPicker     *ModelPicker
	backgroundTasks *BackgroundTasksPanel
	agentWizard     *AgentWizard
	mcpSettings     *MCPSettingsPanel
	teamsPanel      *TeamsPanel
	taskExplorer    *TaskExplorerPanel
	commandPalette  *CommandPalette
	planDialog      *PlanDialog
	questionDialog  *QuestionDialog
	agentPicker     *AgentThreadPicker

	// Streaming & Progress integration
	streamingCtx    *ChatStreamingContext
	permQueue       *PermissionQueue
	threadAttention *threadAttentionStore
	thinkingInd     *ThinkingIndicator
	toolProgress    *ToolProgressDisplay

	// Keybinding resolver for user-customizable shortcuts
	keybindResolver *keybindings.Resolver

	// Engine
	engine    *engine.QueryEngine
	model     string
	program   *tea.Program
	eventChan <-chan engine.QueryEvent
	running   bool // whether a query is in progress
	cancelFn  contextPkg.CancelFunc
	queryID   uint64 // monotonically increasing query identifier
	// commandPaletteSubmission is live presentation provenance only. An
	// unbound record is claimed synchronously by sendSlashCommand; an engine
	// record is then rebound to the exact queryID until its typed result
	// settles. Durable command events and non-TUI entrypoints remain unchanged.
	commandPaletteSubmission       *commandPaletteSubmission
	commandPaletteSubmissionSerial uint64
	// pendingCommandPrompt is a presentation-level continuation requested by a
	// completed engine-owned command (for example /plan <description>).
	pendingCommandPrompt string

	// Spinner
	spinnerCount       int
	spinnerState       SpinnerState
	hookStatus         string // non-empty when a hook is running (shown as spinner text)
	asyncHookStatuses  map[string]string
	asyncHookOrder     []string
	classifierChecking string // non-empty when auto-classifier is evaluating (tool name)
	permissionReview   string // non-empty while the advisory permission reviewer is checking

	// Task tree (sub-agent progress)
	activeTasks map[string]*taskEntry // taskID → entry

	// Inline tool call tree (active tool executions shown below spinner)
	activeTools      map[string]*inlineToolEntry // toolCallID → entry
	activeToolsOrder []string                    // insertion-order list of toolCallIDs

	// History
	history              []string
	historyIdx           int
	draft                string
	draftElements        []threadComposerElement
	historySetText       string // text placed by the last history recall ("" when browsing draft)
	richHistoryElements  map[int][]threadComposerElement
	historySearch        composerHistorySearch
	composerUndo         []composerUndoEntry
	externalEditorActive bool

	// View-only state keyed by the canonical runtime ThreadID. Engine facts are
	// never copied into this store.
	threadViews               *threadViewStore
	composerElements          []threadComposerElement
	nextComposerElementID     uint64
	draftMedia                map[string]*composerDraftImage
	composerRevision          uint64
	composerImageLoadPending  *composerImageLoadRequest
	composerImageLoadSerial   uint64
	composerAdmissionPending  *composerAdmissionRequest
	composerAdmissionSerial   uint64
	queuedInputPreview        []threadQueuedInput
	threadDetailTab           agentDetailTab
	threadAttentionResponses  sync.Map
	sessionViewSaveGeneration uint64
	sessionViewSaveError      string
	sessionRestorePending     bool

	// Quit
	quitting  bool
	lastCtrlC time.Time // for double-press exit

	// Status line hook
	statusLineHook StatusLineFunc

	// Notification presentation is mutated only by Update. Tea deadline
	// messages are generation-fenced because their commands are not cancellable.
	notifications                 *NotificationStack
	notificationNow               func() time.Time
	notificationAfter             NotificationAfterFunc
	notificationExpiryScheduled   bool
	notificationExpiryGeneration  uint64
	notificationScheduledDeadline time.Time
	startupThemeDiagnostics       []startupThemeDiagnostic

	// Idle tracking (welcome-back notification on return from idle)
	idle *idleTracker
	// Session start time (for elapsed display in status bar)
	sessionStart time.Time

	// Mascot animation
	mascotAnim            *MascotAnimator
	mascotIdleScheduled   bool
	mascotIdleGeneration  uint64
	mascotIdleRand        func() float64
	mascotIdleAfter       mascotIdleAfterFunc
	fullscreen            bool
	mouseEnabled          bool
	selection             *Selection
	selectionEdgeScroll   selectionEdgeScrollState
	clipboard             *ClipboardService
	clipboardImageReader  func(contextPkg.Context) attachments.ImagePasteResult
	clipboardPending      *clipboardPendingRequest
	clipboardRequestID    uint64
	reducedMotion         bool
	terminalCaps          terminalcap.Capabilities
	focusState            *terminalcap.FocusState
	welcomeGreeting       string
	welcomeTip            string
	welcomeTipHistoryPath string
	welcomeTipPinned      bool

	// Bypass permissions confirmation dialog
	bypassConfirmIdx int // 0 = "Yes, I accept", 1 = "No, go back"

	// Expand view state
	expandContent      string               // full expanded content
	expandLines        []string             // pre-split lines
	expandOffset       int                  // scroll offset (line index)
	expandSearch       *ExpandSearchOverlay // search within expand view
	expandReturnDialog AppState
	expandConversation bool
	expandRaw          bool

	// Task panel state
	taskPanelLines  []string // pre-rendered task panel lines
	taskPanelOffset int      // scroll offset

	agentTraceProvider         func() []engine.AgentParentTraceSnapshot
	taskExplorerSnapshotSource func() engine.TaskExplorerSnapshot
	taskExplorerActionProvider func(engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult
	threadCatalogProvider      func() engine.RuntimeThreadCatalogSnapshot
	threadAttentionProvider    func() []engine.RuntimeThreadAttentionSnapshot
	agentDetailProvider        func(string) (engine.AgentDetailSnapshot, bool)
	agentTranscriptProvider    agentTranscriptPageProvider
	spinnerTickScheduled       bool
}

// StatusLineFunc is a hook that can customize the status line content.
// It receives the default left and right segments and returns replacements.
// Return empty strings to use the defaults.
type StatusLineFunc func(defaultLeft, defaultRight string) (left, right string)

// NotificationAfterFunc creates one Bubble Tea deadline command. Tests can
// inject a deterministic command without sleeping.
type NotificationAfterFunc func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

// Config holds initialization parameters for the App.
type Config struct {
	Engine         *engine.QueryEngine // optional; can be set later via SetEngine
	Model          string
	Theme          string         // startup theme name; empty selects terminal-capability fallback
	StatusLineHook StatusLineFunc // optional hook to customize status bar content
	Resumed        bool           // true if resuming a session (skip welcome screen)
	Fullscreen     bool           // alternate-screen/fullscreen rendering is enabled
	MouseEnabled   bool           // Bubble Tea mouse tracking is enabled
	ReducedMotion  bool           // disable non-essential animation
	Chooser        ChoiceFunc     // optional stable chooser for session content/tests
	TerminalCaps   *terminalcap.Capabilities
	FocusState     *terminalcap.FocusState
	// ClipboardImageReader injects the bounded, typed clipboard-image boundary.
	// Nil selects the platform adapter in internal/tui/attachments.
	ClipboardImageReader func(contextPkg.Context) attachments.ImagePasteResult
	// DisplayCellProfile injects one immutable session profile. Nil or an
	// invalid zero value selects DefaultDisplayCellProfile.
	DisplayCellProfile *DisplayCellProfile
	// NotificationNow and NotificationAfter are deterministic notification
	// lifecycle seams. Nil selects time.Now and tea.Tick.
	NotificationNow   func() time.Time
	NotificationAfter NotificationAfterFunc
	// KeybindingsConfigDir overrides only the resolved keybindings directory.
	// Empty selects the canonical user config root or an explicit env override.
	KeybindingsConfigDir string
}

// New creates a new App with the given configuration.
func New(cfg Config) *App {
	caps := terminalcap.Current(cfg.MouseEnabled)
	if cfg.TerminalCaps != nil {
		caps = *cfg.TerminalCaps
	}
	focusState := cfg.FocusState
	if focusState == nil {
		focusState = terminalcap.NewFocusState(caps.FocusReporting)
	}
	themeResolution := resolveStartupThemeForCapabilities(cfg.Theme, caps)
	styles := StylesForTheme(themeResolution.theme)
	resolver := keybindings.NewResolver()
	displayCells := DefaultDisplayCellProfile()
	if cfg.DisplayCellProfile != nil && cfg.DisplayCellProfile.valid() {
		displayCells = *cfg.DisplayCellProfile
	}
	renderEnvironment := newRenderEnvironment(styles, displayCells)

	ta := newBoundedTextarea(
		"Ask anything...",
		80,
		1,
		2,
		func(lineIdx int) string {
			if lineIdx == 0 {
				return "❯ "
			}
			return "  "
		},
		cfg.ReducedMotion,
	)

	cmdReg := commands.NewRegistry()
	commands.RegisterDefaults(cmdReg)
	if cfg.Engine != nil {
		cmdReg = cfg.Engine.GetCommandRegistry()
	}

	// Determine initial state: show welcome on fresh sessions only
	initialState := StateWelcome
	if cfg.Resumed {
		initialState = StateChat
	}

	chooser := cfg.Chooser
	if chooser == nil {
		chooser = randomChoice()
	}
	clipboardImageReader := cfg.ClipboardImageReader
	if clipboardImageReader == nil {
		clipboardImageReader = attachments.ReadClipboardImage
	}
	notificationNow := cfg.NotificationNow
	if notificationNow == nil {
		notificationNow = time.Now
	}
	notificationAfter := cfg.NotificationAfter
	if notificationAfter == nil {
		notificationAfter = defaultNotificationAfter
	}
	app := &App{
		focus:             FocusEditor,
		state:             initialState,
		dialogs:           &appDialogStack{},
		inputMode:         InputNormal,
		permMode:          permission.ModeDefault,
		commandHintIdx:    -1,
		fileHintIdx:       -1,
		mentionHintIdx:    -1,
		commandRegistry:   cmdReg,
		styles:            styles,
		renderEnvironment: renderEnvironment,
		statusLineHook:    cfg.StatusLineHook,
		textarea:          ta,
		chat:              newChatViewWithRenderEnvironment(renderEnvironment),
		dialog:            NewPermissionDialog(styles),
		resume:            NewResumeDialog(styles),
		help:              NewHelpOverlay(styles, resolver),
		search:            NewSearchOverlay(styles),
		msgSelector:       NewMessageSelector(styles),
		mcpApproval:       NewMCPApprovalDialog(styles),
		modelPicker:       NewModelPicker(styles),
		backgroundTasks:   NewBackgroundTasksPanel(styles),
		mcpSettings:       NewMCPSettingsPanel(styles),
		agentWizard:       NewAgentWizard(styles),
		teamsPanel:        NewTeamsPanel(styles),
		taskExplorer:      NewTaskExplorerPanel(styles),
		commandPalette:    NewCommandPalette(styles),
		planDialog: newPlanDialog(
			styles,
			resolver,
			cfg.ReducedMotion,
			caps.Color == terminalcap.ColorNone,
		),
		questionDialog:    NewQuestionDialog(styles),
		agentPicker:       NewAgentThreadPicker(styles),
		expandSearch:      NewExpandSearchOverlay(styles),
		engine:            cfg.Engine,
		model:             cfg.Model,
		activeTasks:       make(map[string]*taskEntry),
		activeTools:       make(map[string]*inlineToolEntry),
		idle:              newIdleTracker(),
		sessionStart:      time.Now(),
		notifications:     NewNotificationStack(),
		notificationNow:   notificationNow,
		notificationAfter: notificationAfter,
		startupThemeDiagnostics: append(
			[]startupThemeDiagnostic(nil),
			themeResolution.diagnostics...,
		),
		streamingCtx:         NewChatStreamingContext(),
		permQueue:            NewPermissionQueue(styles),
		threadAttention:      newThreadAttentionStore(defaultThreadAttentionLimit),
		thinkingInd:          NewThinkingIndicator(),
		toolProgress:         NewToolProgressDisplay(),
		keybindResolver:      resolver,
		vimModel:             vim.New(),
		fullscreen:           cfg.Fullscreen,
		mouseEnabled:         cfg.MouseEnabled && (cfg.TerminalCaps == nil || caps.Mouse),
		selection:            &Selection{},
		draftMedia:           make(map[string]*composerDraftImage),
		clipboardImageReader: clipboardImageReader,
		reducedMotion:        cfg.ReducedMotion,
		terminalCaps:         caps,
		focusState:           focusState,
		welcomeGreeting:      welcomeGreetings[chooseIndex(chooser, len(welcomeGreetings))],
		welcomeTip:           welcomeTips[chooseIndex(chooser, len(welcomeTips))],
	}
	app.projectModalRenderEnvironment()

	app.mascotAnim = NewMascotAnimator(chooser)
	app.bindTaskExplorerProviders()

	// Show first-run guidance if this is a new installation.
	if state := onboarding.CheckOnboardingNeeded(); state.IsFirstRun {
		app.welcomeTip = "First run! Key commands: /help, /model, /mode. Configure API key in env or config."
		app.welcomeTipPinned = true
	} else {
		app.rotateWelcomeTip()
	}

	if cfg.Engine != nil {
		app.permMode = cfg.Engine.PermissionMode()
	}

	// Load only canonical user keybindings unless the caller or environment
	// selects one exact config directory.
	keybindingsDir := cfg.KeybindingsConfigDir
	var keybindingsDirErr error
	if keybindingsDir == "" {
		var home string
		home, keybindingsDirErr = os.UserHomeDir()
		if keybindingsDirErr == nil {
			keybindingsDir, keybindingsDirErr = keybindings.ResolveUserConfigDir(home)
		}
	}
	if keybindingsDirErr != nil {
		app.chat.AppendSystem("Keybinding config ignored: config root is unavailable")
	} else {
		issues, loadErr := app.keybindResolver.LoadUserBindings(keybindingsDir)
		switch {
		case loadErr != nil:
			app.chat.AppendSystem("Keybinding config ignored: " + loadErr.Error())
		case len(issues) > 0:
			app.chat.AppendSystem("Keybinding config diagnostics:\n" + keybindings.FormatValidationIssues(issues))
		}
	}

	// Load persisted history
	if hist := loadHistory(); len(hist) > 0 {
		app.history = hist
		app.historyIdx = len(hist)
	}
	app.initializeThreadViews()
	if cfg.Resumed && cfg.Engine != nil {
		if err := app.resetAndRestoreSessionViews(); err != nil {
			app.chat.AppendSystem("Session view state was not restored: " + err.Error())
		}
	}
	app.hydrateQueuedInputPreview()

	return app
}

// SetProgram sets the tea.Program reference for async message sending.
func (a *App) SetProgram(p *tea.Program) {
	a.program = p
}

// SetEngine sets the query engine after creation.
func (a *App) SetEngine(eng *engine.QueryEngine) {
	a.engine = eng
	if eng != nil {
		a.commandRegistry = eng.GetCommandRegistry()
		a.rebindLeaderThreadView(eng.ThreadID())
	}
	a.bindTaskExplorerProviders()
	if eng != nil {
		a.permMode = eng.PermissionMode()
	}
	a.hydrateQueuedInputPreview()
	a.rotateWelcomeTip()
}

func (a *App) bindTaskExplorerProviders() {
	var explorerProvider func() engine.TaskExplorerSnapshot
	var explorerAction func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult
	var detailProvider func(string) (engine.AgentDetailSnapshot, bool)
	var transcriptProvider agentTranscriptPageProvider
	var executionDetailProvider taskExplorerExecutionDetailProvider
	var traceProvider func() []engine.AgentParentTraceSnapshot
	var mcpManager *tools.MCPToolManager
	if a != nil && a.engine != nil {
		explorerProvider = a.engine.TaskExplorerSnapshot
		explorerAction = a.engine.ApplyTaskExplorerAction
		detailProvider = a.engine.AgentDetailSnapshot
		transcriptProvider = a.engine.AgentTranscriptPage
		executionDetailProvider = a.engine.AgentExecutionDetail
		traceProvider = a.engine.AgentParentTraceSnapshots
		mcpManager = a.engine.GetMCPManager()
	}
	if a != nil {
		a.agentTraceProvider = traceProvider
		a.taskExplorerSnapshotSource = explorerProvider
		a.taskExplorerActionProvider = explorerAction
		if a.taskExplorer != nil {
			a.taskExplorer.SetSnapshotProvider(explorerProvider)
			a.taskExplorer.SetActionProvider(explorerAction)
			a.taskExplorer.SetTranscriptProvider(transcriptProvider)
			a.taskExplorer.SetExecutionDetailProvider(executionDetailProvider)
		}
		a.threadCatalogProvider = nil
		a.threadAttentionProvider = nil
		a.agentDetailProvider = detailProvider
		a.agentTranscriptProvider = transcriptProvider
		if a.engine != nil {
			a.threadCatalogProvider = a.engine.ThreadCatalogSnapshot
			a.threadAttentionProvider = a.engine.ThreadAttentionSnapshots
		}
	}
	if a != nil && a.backgroundTasks != nil {
		a.backgroundTasks.SetExplorerSnapshotProvider(explorerProvider)
		a.backgroundTasks.SetActionProvider(explorerAction)
		a.backgroundTasks.SetDetailProvider(detailProvider)
		a.backgroundTasks.SetTranscriptProvider(transcriptProvider)
		a.backgroundTasks.SetTranscriptSelectionProvider(a.agentTranscriptSelectionByAgent)
	}
	if a != nil && a.teamsPanel != nil {
		a.teamsPanel.SetExplorerSnapshotProvider(explorerProvider)
		a.teamsPanel.SetDetailProvider(detailProvider)
		a.teamsPanel.SetTranscriptProvider(transcriptProvider)
		a.teamsPanel.SetTranscriptSelectionProvider(a.agentTranscriptSelectionByAgent)
	}
	if a != nil && a.mcpSettings != nil {
		a.mcpSettings.SetManager(mcpManager)
	}
	if a != nil {
		a.mentionIndex.listMCP = nil
		a.mentionIndex.readMCP = nil
		a.mentionIndex.loaded = false
		if mcpManager != nil {
			a.mentionIndex.listMCP = func(ctx contextPkg.Context) ([]enginemcp.MCPResource, error) {
				return mcpManager.ListResources(ctx, "")
			}
			a.mentionIndex.readMCP = mcpManager.ReadResource
		}
	}
}

// MakePermissionPromptFn returns a presentation-only permission adapter. The
// engine owns persistence and the canonical request/resolved lifecycle.
func (a *App) MakePermissionPromptFn() engine.PermissionPromptFn {
	return func(ctx contextPkg.Context, request engine.PermissionPromptRequest) engine.PermissionInteractionResult {
		if a.program == nil {
			return permissionTerminalResult(
				request,
				engine.PermissionCancelled,
				"TUI permission prompt unavailable",
			)
		}

		inputJSON := "{}"
		if request.Input != nil {
			if b, err := jsonPkg.Marshal(request.Input); err == nil {
				inputJSON = string(b)
			}
		}
		sessionScope := request.SessionScope
		if sessionScope == "" {
			sessionScope = "this exact tool input"
		}
		requestID := request.ToolUseID
		if requestID == "" {
			requestID = engine.ToolUseIDFromContext(ctx)
		}
		threadID := a.leaderThreadViewID()
		if request.ThreadID != "" {
			threadID = normalizeThreadViewID(request.ThreadID)
		}

		responseCh := make(chan PermissionResponse, 1)
		if request.PlanApproval != nil {
			planApproval := cloneTUIPlanApprovalRequest(request.PlanApproval)
			a.program.Send(planApprovalMsg{
				requestID: requestID, threadID: threadID, sessionID: request.SessionID,
				agentID: request.AgentID, planApproval: planApproval, responseCh: responseCh,
			})
		} else if request.Kind == engine.PermissionInteractionKindQuestion {
			a.program.Send(askUserQuestionMsg{
				requestID: requestID, threadID: threadID, agentID: request.AgentID,
				input: inputJSON, responseCh: responseCh,
			})
		} else {
			a.program.Send(permissionRequestMsg{
				requestID: requestID, threadID: threadID, agentID: request.AgentID,
				tool: request.ToolName, input: inputJSON, sessionScope: sessionScope, responseCh: responseCh,
				decisionConstraint: request.DecisionConstraint,
			})
		}

		select {
		case resp := <-responseCh:
			responseData := a.takeThreadAttentionResponse(requestID)
			return permissionInteractionResult(request, resp, responseData)
		case <-ctx.Done():
			a.program.Send(threadAttentionCanceledMsg{requestID: requestID})
			return permissionTerminalResult(
				request,
				engine.PermissionCancelled,
				"context canceled",
			)
		case <-time.After(5 * time.Minute):
			a.program.Send(threadAttentionCanceledMsg{requestID: requestID})
			return permissionTerminalResult(
				request,
				engine.PermissionTimedOut,
				"permission request timed out",
			)
		}
	}
}

func permissionTerminalResult(
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

func cloneTUIPlanApprovalRequest(request *engine.PlanApprovalRequest) *engine.PlanApprovalRequest {
	if request == nil {
		return nil
	}
	cloned := *request
	return &cloned
}

func permissionInteractionResult(
	request engine.PermissionPromptRequest,
	response PermissionResponse,
	responseData threadAttentionResponseData,
) engine.PermissionInteractionResult {
	if request.PlanApproval != nil {
		if responseData.planResult != nil {
			approval := *responseData.planResult
			result := engine.PermissionInteractionResult{Decision: mapPlanOutcomeDecision(approval.Outcome), PlanApproval: &approval}
			switch approval.Outcome {
			case engine.PlanApprovalRevise:
				result.Message = "User rejected the plan with feedback: " + approval.Feedback
			case engine.PlanApprovalCancel:
				result.Message = "User rejected the plan."
			}
			return result
		}
		approval := &engine.PlanApprovalDecision{RequestID: request.PlanApproval.RequestID, PlanRevision: request.PlanApproval.PlanRevision, Outcome: engine.PlanApprovalCancel, TargetMode: permission.ModePlan}
		return engine.PermissionInteractionResult{
			Decision:     engine.PermissionDeny,
			Message:      "Plan interaction ended without an explicit terminal result.",
			PlanApproval: approval,
		}
	}
	if request.DecisionConstraint == engine.PermissionAllowOnceOnly &&
		(response == PermissionAllowSession || response == PermissionAllowAlways) {
		return engine.PermissionInteractionResult{
			Decision: engine.PermissionDeny,
			Message:  "permission decision is not allowed by request constraint",
		}
	}
	switch response {
	case PermissionAllow:
		result := engine.PermissionInteractionResult{Decision: engine.PermissionAllowOnce}
		if request.Kind == engine.PermissionInteractionKindQuestion && responseData.answerJSON != "" {
			_ = jsonPkg.Unmarshal([]byte(responseData.answerJSON), &result.UpdatedInput)
		}
		return result
	case PermissionAllowSession:
		return engine.PermissionInteractionResult{Decision: engine.PermissionAllowSession}
	case PermissionAllowAlways:
		return engine.PermissionInteractionResult{Decision: engine.PermissionAllowAlways}
	default:
		return engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: "user denied permission"}
	}
}

func mapPlanOutcomeDecision(outcome engine.PlanApprovalOutcome) engine.PermissionInteractionDecision {
	if outcome == engine.PlanApprovalApprove {
		return engine.PermissionAllowOnce
	}
	return engine.PermissionDeny
}

// MakeCanUseToolFn preserves the legacy callback API for embedders that have
// not adopted PermissionPromptFn yet.
func (a *App) MakeCanUseToolFn() func(ctx contextPkg.Context, toolName string, input map[string]any, toolCtx *engine.ToolUseContext) (bool, string) {
	prompt := a.MakePermissionPromptFn()
	return func(ctx contextPkg.Context, toolName string, input map[string]any, toolCtx *engine.ToolUseContext) (allowed bool, reason string) {
		request := engine.PermissionPromptRequest{
			Kind:     permissionPromptKindForTUI(toolName),
			ToolName: toolName, ToolUseID: engine.ToolUseIDFromContext(ctx), Input: input,
			SessionScope: "this exact tool input", ToolContext: toolCtx,
		}
		if a.engine != nil {
			request.SessionScope = a.engine.SessionApprovalDescription(toolName, input)
		}
		if toolCtx != nil {
			request.SessionID = toolCtx.SessionID
			request.ThreadID = toolCtx.ThreadID
			request.AgentID = toolCtx.AgentID
		}

		engine.ReportPermissionPromptRequested(ctx, toolName, input, request.SessionScope)
		result := prompt(ctx, request)
		defer func() { engine.ReportPermissionPromptResolved(ctx, allowed, reason) }()

		switch result.Decision {
		case engine.PermissionAllowOnce:
			if len(result.UpdatedInput) > 0 {
				engine.SetUpdatedInput(ctx, result.UpdatedInput)
			}
			return true, result.Message
		case engine.PermissionAllowSession:
			if a.engine == nil {
				return false, "session approval unavailable"
			}
			if err := a.engine.ApproveForSession(toolName, input); err != nil {
				return false, err.Error()
			}
			return true, result.Message
		case engine.PermissionAllowAlways:
			if a.engine == nil {
				return false, "always-allow unavailable"
			}
			if err := a.engine.PersistPermissionRule(toolName, input); err != nil {
				return false, err.Error()
			}
			return true, result.Message
		default:
			return false, result.Message
		}
	}
}

func permissionPromptKindForTUI(toolName string) string {
	if strings.EqualFold(strings.TrimSpace(toolName), "AskUserQuestion") {
		return engine.PermissionInteractionKindQuestion
	}
	return engine.PermissionInteractionKindPermission
}

// MakeRepeatedToolCallPromptFn returns a one-call-only repeated-tool override
// prompt. Engine owns its corresponding request/resolved events, so this
// callback must not report generic permission prompt events itself.
func (a *App) MakeRepeatedToolCallPromptFn() engine.RepeatedToolCallPromptFn {
	return func(ctx contextPkg.Context, toolName, toolUseID string, attempt int, toolCtx *engine.ToolUseContext) (bool, string) {
		if a.program == nil {
			return false, "interactive one-call override unavailable"
		}

		responseCh := make(chan PermissionResponse, 1)
		threadID := a.leaderThreadViewID()
		agentID := ""
		if toolCtx != nil {
			threadID = normalizeThreadViewID(toolCtx.ThreadID)
			agentID = toolCtx.AgentID
		}
		a.program.Send(permissionRequestMsg{
			requestID: toolUseID, threadID: threadID, agentID: agentID, tool: toolName,
			sessionScope: "This is the third consecutive identical tool call. Run this call once, or stop and change strategy.", kind: threadAttentionRepeatedTool,
			attempt: attempt, responseCh: responseCh,
		})

		select {
		case response := <-responseCh:
			if response == PermissionAllow {
				return true, "one-call override granted"
			}
			return false, "user chose to stop and change strategy"
		case <-ctx.Done():
			a.program.Send(threadAttentionCanceledMsg{requestID: toolUseID})
			return false, "context canceled"
		case <-time.After(5 * time.Minute):
			a.program.Send(threadAttentionCanceledMsg{requestID: toolUseID})
			return false, "repeated tool-call prompt timed out"
		}
	}
}

type startupThemeDiagnosticsMsg struct {
	effective   ThemeName
	diagnostics []startupThemeDiagnostic
}

func (a *App) takeStartupThemeDiagnosticsCmd() tea.Cmd {
	if a == nil || len(a.startupThemeDiagnostics) == 0 {
		return nil
	}
	message := startupThemeDiagnosticsMsg{
		effective: a.styles.theme,
		diagnostics: append(
			[]startupThemeDiagnostic(nil),
			a.startupThemeDiagnostics...,
		),
	}
	a.startupThemeDiagnostics = nil
	return func() tea.Msg { return message }
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	var cmds []tea.Cmd
	if diagnostics := a.takeStartupThemeDiagnosticsCmd(); diagnostics != nil {
		cmds = append(cmds, diagnostics)
	}
	if wait := a.waitForAsyncHookEvent(); wait != nil {
		cmds = append(cmds, wait)
	}
	if wait := a.waitForRuntimeInput(); wait != nil {
		cmds = append(cmds, wait)
	}
	if wait := a.waitForGoalContinuation(); wait != nil {
		cmds = append(cmds, wait)
	}
	if !a.reducedMotion {
		cmds = append(cmds, textarea.Blink)
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (model tea.Model, resultCmd tea.Cmd) {
	defer func() {
		resultCmd = batchNotificationCmd(
			resultCmd,
			a.reconcileNotificationExpiry(),
		)
	}()

	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case startupThemeDiagnosticsMsg:
		for _, diagnostic := range msg.diagnostics {
			a.showNotification(diagnostic.message(msg.effective), NotifyWarning)
		}
		return a, nil

	case NotificationDeliveryMsg:
		a.showNotification(msg.Message, msg.Severity)
		return a, nil

	case notificationExpiryTickMsg:
		if !a.acceptNotificationExpiryTick(msg.generation) {
			return a, nil
		}
		a.notifications.PruneAt(a.notificationNow())
		return a, nil

	case tea.FocusMsg:
		a.focusState.SetFocused(true)
		if a.reducedMotion {
			return a, nil
		}
		return a, textarea.Blink

	case tea.BlurMsg:
		a.focusState.SetFocused(false)
		return a, nil

	case tea.ResumeMsg:
		restore := a.restoreTerminalCapabilitiesCmd()
		return a, restore

	case tea.WindowSizeMsg:
		a.stopSelectionEdgeScroll()
		// Bubble Tea ticks cannot be cancelled. Invalidate the old delay before
		// every resize so a delayed message cannot animate against new geometry.
		// An active idle sequence is also reset; its already-pending frame tick
		// will then be inert. Visible resizes deliberately preserve click
		// sequences, whose immediate interaction contract is independent.
		idleActive := a.mascotAnim.IdleActive()
		a.invalidateMascotIdle()
		if a.width != msg.Width || a.height != msg.Height {
			a.renderEnvironment = a.renderEnvironment.withGeometry()
			if a.threadViews != nil {
				a.threadViews.SetRenderEnvironment(a.renderEnvironment)
			} else {
				a.chat.SetRenderEnvironment(a.renderEnvironment)
			}
			a.projectModalRenderEnvironment()
		}
		a.width = msg.Width
		a.height = msg.Height
		a.updateLayout()
		if !a.mascotVisible() || idleActive {
			a.mascotAnim.Stop()
		}
		cmd := a.ensureMascotIdleTick()
		return a, cmd

	case tea.KeyPressMsg:
		a.stopSelectionEdgeScroll()
		cmd := a.handleKey(msg)
		if msg.String() == "ctrl+c" &&
			a.running &&
			a.cancelFn == nil &&
			a.hookStatus == "Cancelling..." {
			// handleInterrupt has accepted cancellation of the live query.
			// Do not wait for a terminal event to invalidate palette truth.
			a.clearCommandPaletteEngineSubmission(a.queryID)
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if a.quitting {
			a.stopMascotIdle()
			a.invalidateSessionViewSave()
			_ = a.persistSessionViewState()
			return a, tea.Quit
		}
		if mascotCmd := a.reconcileMascotIdle(); mascotCmd != nil {
			cmds = append(cmds, mascotCmd)
		}
		a.updateLayout() // recalc after editor keys may change content
		if save := a.scheduleSessionViewSave(); save != nil {
			cmds = append(cmds, save)
		}
		return a, tea.Batch(cmds...)

	case tea.PasteMsg:
		// Modal editors consume paste through their own update paths below.
		if _, modalActive := a.activeDialogState(); modalActive {
			break
		}
		cmd := a.handleComposerPaste(msg)
		a.updateLayout()
		if save := a.scheduleSessionViewSave(); save != nil {
			return a, tea.Batch(cmd, a.ensureMentionIndex(), save)
		}
		return a, tea.Batch(cmd, a.ensureMentionIndex())

	case sessionViewSaveDueMsg:
		if msg.generation == a.sessionViewSaveGeneration && !a.sessionRestorePending {
			if err := a.persistSessionViewState(); err != nil {
				if err.Error() != a.sessionViewSaveError {
					a.showNotification("Session view state was not saved: "+err.Error(), NotifyWarning)
					a.sessionViewSaveError = err.Error()
				}
			} else {
				a.sessionViewSaveError = ""
			}
		}
		return a, nil

	case clipboardResultMsg:
		liveResult := a.clipboardPending != nil &&
			msg.requestID == a.clipboardPending.id &&
			msg.caller == a.clipboardPending.caller
		a.handleClipboardResult(msg)
		a.settleCommandPaletteClipboardResult(msg, liveResult)
		return a, nil

	case tea.MouseMsg:
		{
			msg := normalizeMouseMsg(msg)
			if msg.Shift {
				a.stopSelectionEdgeScroll()
				if a.selection.IsDragging() {
					a.selection.Clear()
				}
				return a, nil
			}
			if msg.Button == tea.MouseLeft &&
				(msg.Action == mouseActionPress ||
					msg.Action == mouseActionRelease) {
				a.stopSelectionEdgeScroll()
			}
			if cmd, handled := a.handleMascotMouse(msg); handled {
				return a, cmd
			}
			// Modal layers own pointer input; never leak clicks to the chat below.
			if dialogState, ok := a.activeDialogState(); ok {
				if dialogState == StatePlanApproval {
					a.planDialog.HandleMouse(msg)
				}
				return a, nil
			}
			// The dedicated Task Explorer owns its full overlay while open. Route
			// render-derived local coordinates to it before sidebar/chat handling,
			// and consume every pointer event so none can mutate chat selection.
			if a.state == StateTaskPanel {
				if a.taskExplorer != nil && a.taskExplorer.provider != nil &&
					msg.X >= a.layout.overlayRect.X &&
					msg.X < a.layout.overlayRect.X+a.layout.overlayRect.Width &&
					msg.Y >= a.layout.overlayRect.Y &&
					msg.Y < a.layout.overlayRect.Y+a.layout.overlayRect.Height {
					local := msg
					local.X -= a.layout.overlayRect.X
					local.Y -= a.layout.overlayRect.Y
					a.taskExplorer.HandleMouse(local)
				}
				return a, nil
			}
			if a.layout.sidebarRect.Width > 0 && msg.X >= a.layout.sidebarRect.X {
				return a, nil
			}

			// Expand view: handle selection and scroll
			if a.state == StateExpand {
				switch msg.Button {
				case tea.MouseWheelUp:
					a.expandOffset -= 3
					if a.expandOffset < 0 {
						a.expandOffset = 0
					}
				case tea.MouseWheelDown:
					a.expandOffset += 3
					max := len(a.expandLines) - a.height + 4
					if max < 0 {
						max = 0
					}
					if a.expandOffset > max {
						a.expandOffset = max
					}
				default:
					// Selection in expand view
					contentRow, selectable := a.expandMouseSelectionRow(msg)
					if selectable && a.selection.HandleExpandMouse(msg, msg.X, contentRow, a.expandLines) {
						if !a.selection.isDragging && a.selection.HasExpandSelection() {
							text := a.selection.ExtractExpandText(
								a.expandLines,
								a.renderEnvironment.normalized().profile,
							)
							if text != "" {
								cmd := a.requestClipboardCopy(
									ClipboardCallerExpandSelection,
									text,
								)
								return a, cmd
							}
						}
					}
				}
				return a, nil
			}

			// Adjust mouse coordinates to be relative to chat viewport.
			chatX := msg.X
			chatY := msg.Y - a.layout.chatRect.Y

			// Click on the new-messages / jump-to-bottom pill: snap to bottom.
			if msg.Button == tea.MouseLeft && msg.Action == mouseActionPress {
				if a.pillClickHits(chatX, chatY) {
					a.chat.ResetFollow()
					return a, nil
				}
			}

			// Edge-scroll during drag: auto-scroll when dragging near/beyond edges.
			if a.selection.IsDragging() && msg.Action == mouseActionMotion {
				cmd := a.handleSelectionEdgeMotion(chatX, chatY)
				return a, cmd
			}

			// Text selection: handle drag/multi-click events for copy-on-select.
			if a.selection.HandleMouseForChat(msg, chatX, chatY, a.chat) {
				// On drag finish or multi-click selection, copy selected text.
				if !a.selection.isDragging && a.selection.HasSelection() {
					text := a.selection.ExtractTextFromChat(a.chat)
					if text != "" {
						cmd := a.requestClipboardCopy(
							ClipboardCallerChatSelection,
							text,
						)
						return a, cmd
					}
				} else if msg.Button == tea.MouseLeft && msg.Action == mouseActionRelease {
					if agentID, ok := a.chat.AgentTraceTargetAtViewportRow(chatY); ok {
						cmd := a.openAgentDetail(agentID)
						return a, cmd
					}
				}
				return a, nil
			}
			if msg.Button == tea.MouseLeft &&
				msg.Action == mouseActionRelease &&
				!a.selection.HasSelection() {
				if agentID, ok := a.chat.AgentTraceTargetAtViewportRow(chatY); ok {
					cmd := a.openAgentDetail(agentID)
					return a, cmd
				}
			}
			switch msg.Button {
			case tea.MouseWheelUp:
				a.stopSelectionEdgeScroll()
				a.selection.Clear()
				a.chat.ScrollUp(3)
				cmd := a.loadOlderActiveAgentTranscript()
				return a, cmd
			case tea.MouseWheelDown:
				a.stopSelectionEdgeScroll()
				a.selection.Clear()
				a.chat.ScrollDown(3)
			}
			return a, nil
		}

	case selectionEdgeScrollTickMsg:
		cmd := a.handleSelectionEdgeScrollTick(msg)
		return a, cmd

	case mascotTickMsg:
		if a.reducedMotion || !a.mascotVisible() {
			a.stopMascotIdle()
			return a, nil
		}
		cmd := a.mascotAnim.Tick()
		if cmd != nil {
			return a, cmd
		}
		cmd = a.ensureMascotIdleTick()
		return a, cmd

	case mascotIdleTickMsg:
		if !a.acceptMascotIdleTick(msg.generation) {
			return a, nil
		}
		return a, a.mascotAnim.TriggerIdle(a.mascotIdleSequence())

	case engineEventMsg:
		if msg.queryID != a.queryID {
			return a, nil // stale event from old query
		}
		cmd := a.handleEngineEvent(msg.event)
		a.settleCommandPaletteEngineEvent(msg.queryID, msg.event)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		// If handleEngineEvent didn't terminate the run, continue consuming
		if a.running {
			cmds = append(cmds, a.waitForEvent())
		}
		a.updateLayout()
		return a, tea.Batch(cmds...)

	case asyncHookEventMsg:
		if msg.open {
			if cmd := a.handleEngineEvent(msg.event); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if wait := a.waitForAsyncHookEvent(); wait != nil {
				cmds = append(cmds, wait)
			}
		}
		a.updateLayout()
		return a, tea.Batch(cmds...)

	case runtimeInputReadyMsg:
		if msg.engine != a.engine {
			wait := a.waitForRuntimeInput()
			return a, wait
		}
		if err := a.engine.SyncRuntimeItems(contextPkg.Background()); err != nil {
			a.showNotification("Runtime input synchronization failed: "+err.Error(), NotifyError)
		}
		if !a.running {
			if next := a.scheduleNextQueuedInput(); next != nil {
				cmds = append(cmds, next)
			}
		}
		if wait := a.waitForRuntimeInput(); wait != nil {
			cmds = append(cmds, wait)
		}
		return a, tea.Batch(cmds...)

	case goalContinuationReadyMsg:
		if msg.engine != a.engine {
			wait := a.waitForGoalContinuation()
			return a, wait
		}
		if !a.running {
			if next := a.scheduleNextRuntimeWork(); next != nil {
				cmds = append(cmds, next)
			}
		}
		if wait := a.waitForGoalContinuation(); wait != nil {
			cmds = append(cmds, wait)
		}
		return a, tea.Batch(cmds...)

	case engineBatchMsg:
		if msg.queryID != a.queryID {
			return a, nil
		}
		for _, evt := range msg.events {
			cmd := a.handleEngineEvent(evt)
			a.settleCommandPaletteEngineEvent(msg.queryID, evt)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if msg.done {
			a.clearCommandPaletteEngineSubmission(msg.queryID)
			a.running = false
			a.eventChan = nil
			a.cancelFn = nil
			a.chat.FinishAssistant()
			a.updateLayout()
			if next := a.scheduleNextRuntimeWork(); next != nil {
				cmds = append(cmds, next)
			}
			if save := a.scheduleSessionViewSave(); save != nil {
				cmds = append(cmds, save)
			}
			return a, tea.Batch(cmds...)
		}
		a.updateLayout()
		cmds = append(cmds, a.waitForEvent())
		return a, tea.Batch(cmds...)

	case engineStartMsg:
		if msg.queryID != a.queryID {
			return a, nil // stale
		}
		a.eventChan = msg.events
		return a, tea.Batch(a.waitForEvent(), a.ensureSpinnerTick())

	case eventsDoneMsg:
		if msg.queryID != a.queryID {
			return a, nil // stale done from old query
		}
		a.clearCommandPaletteEngineSubmission(msg.queryID)
		a.running = false
		a.eventChan = nil
		a.cancelFn = nil
		a.chat.FinishAssistant()
		a.streamingCtx.EndStreaming()
		a.toolProgress.Reset()
		a.updateLayout()
		next := a.scheduleNextRuntimeWork()
		return a, tea.Batch(next, a.scheduleSessionViewSave())

	case permissionRequestMsg:
		kind := msg.kind
		if kind == "" {
			kind = threadAttentionPermission
		}
		cmd := a.enqueueThreadAttention(threadAttentionRequest{
			ID: msg.requestID, ThreadID: msg.threadID, AgentID: msg.agentID,
			Kind: kind, Tool: msg.tool, Input: msg.input,
			SessionScope: msg.sessionScope, Attempt: msg.attempt, Source: "callback", responseCh: msg.responseCh,
			decisionConstraint: msg.decisionConstraint,
		})
		return a, cmd

	case planApprovalMsg:
		cmd := a.enqueueThreadAttention(threadAttentionRequest{
			ID: msg.requestID, ThreadID: msg.threadID, AgentID: msg.agentID,
			Kind: threadAttentionPlan, Tool: "ExitPlanMode", SessionID: msg.sessionID,
			Source: "callback", PlanApproval: msg.planApproval, responseCh: msg.responseCh,
		})
		return a, cmd

	case planEditorFinishedMsg:
		restore := a.applyPlanEditorResult(msg)
		return a, restore

	case composerEditorFinishedMsg:
		a.applyComposerEditorResult(msg)
		if msg.terminalReleased {
			restore := a.restoreTerminalCapabilitiesCmd()
			return a, restore
		}
		return a, nil

	case askUserQuestionMsg:
		cmd := a.enqueueThreadAttention(threadAttentionRequest{
			ID: msg.requestID, ThreadID: msg.threadID, AgentID: msg.agentID,
			Kind: threadAttentionQuestion, Tool: "AskUserQuestion", Input: msg.input,
			Source: "callback", responseCh: msg.responseCh,
		})
		return a, cmd

	case threadAttentionCanceledMsg:
		cmd := a.cancelThreadAttention(msg.requestID)
		return a, cmd

	case threadAttentionAnsweredMsg:
		cmd := a.resolveThreadAttention(msg.requestID, msg.response)
		return a, cmd

	case mcpApprovalRequestMsg:
		a.pushDialog(StateMCPApproval)
		a.mcpApproval.Show(msg.request, msg.responseCh)
		return a, nil

	case resumeSessionsLoadedMsg:
		if a.hasDialog(StateResume) {
			a.resume.SetPage(msg.page, msg.generation, msg.reset, msg.err)
			var commands []tea.Cmd
			if msg.err == nil && a.resume.hasMore && len(a.resume.filtered) == 0 {
				query, generation := a.resume.beginPage(false)
				if generation != 0 {
					commands = append(commands, a.loadResumePage(query, generation, false))
				}
			}
			commands = append(commands, a.resume.previewRequest())
			if len(commands) == 1 {
				return a, commands[0]
			}
			return a, tea.Batch(commands...)
		}
		return a, nil

	case resumeSessionPageRequestMsg:
		if a.hasDialog(StateResume) && msg.generation == a.resume.generation {
			command := a.loadResumePage(msg.query, msg.generation, msg.reset)
			return a, command
		}
		return a, nil

	case resumeSessionPreviewRequestMsg:
		if !a.hasDialog(StateResume) || msg.generation != a.resume.generation {
			return a, nil
		}
		return a, func() tea.Msg {
			result, err := session.InspectRecent(msg.info, 4)
			return resumeSessionPreviewLoadedMsg{key: msg.key, generation: msg.generation, result: result, err: err}
		}

	case resumeSessionPreviewLoadedMsg:
		if a.hasDialog(StateResume) {
			a.resume.SetPreview(msg.key, msg.generation, msg.result, msg.err)
		}
		return a, nil

	case resumeSessionTranscriptRequestMsg:
		if !a.hasDialog(StateResume) || msg.generation != a.resume.generation {
			return a, nil
		}
		return a, func() tea.Msg {
			result, err := session.InspectFull(msg.info)
			return resumeSessionTranscriptLoadedMsg{info: msg.info, key: msg.key, generation: msg.generation, result: result, err: err}
		}

	case resumeSessionTranscriptLoadedMsg:
		if !a.hasDialog(StateResume) || msg.generation != a.resume.generation {
			return a, nil
		}
		a.resume.transcriptLoading = false
		if msg.err != nil {
			a.resume.err = msg.err.Error()
			return a, nil
		}
		a.popDialog(StateResume)
		a.enterExpandContent(renderSessionTranscript(msg.info, msg.result), StateResume)
		return a, nil

	case resumeSessionActionFinishedMsg:
		a.sessionRestorePending = false
		if msg.err != nil {
			a.resume.err = msg.err.Error()
			a.resume.visible = true
			if !a.hasDialog(StateResume) {
				a.pushDialog(StateResume)
			}
			return a, nil
		}
		if a.engine != nil {
			if err := a.resetAndRestoreSessionViews(); err != nil {
				a.showNotification("Session view state was not restored: "+err.Error(), NotifyWarning)
			}
			a.hydrateQueuedInputPreview()
			a.chat.AppendCompactSummary(msg.count)
		}
		if len(msg.warnings) > 0 {
			a.showNotification(fmt.Sprintf("Session restored with %d warning(s)", len(msg.warnings)), NotifyWarning)
		}
		if msg.selection.Mode == sessionPickerFork {
			a.showNotification("Forked session "+msg.forkedID, NotifySuccess)
		} else {
			a.showNotification("Resumed session "+msg.resumedID, NotifySuccess)
		}
		return a, tea.Batch(
			a.scheduleSessionViewSave(),
			a.scheduleNextRuntimeWork(),
			a.waitForRuntimeInput(),
			a.waitForGoalContinuation(),
		)

	case composerImageLoadedMsg:
		a.handleComposerImageLoaded(msg)
		a.updateLayout()
		return a, nil

	case composerAdmissionSettledMsg:
		cmd := a.handleComposerAdmissionSettled(msg)
		a.updateLayout()
		return a, cmd

	case composerMentionIndexLoadedMsg:
		a.handleMentionIndexLoaded(msg)
		a.updateLayout()
		return a, nil

	case composerMentionPayloadMsg:
		a.handleMentionPayload(msg)
		a.updateLayout()
		return a, nil

	case startQueuedInputMsg:
		start := a.startNextQueuedInput()
		return a, start

	case startGoalContinuationMsg:
		start := a.startNextGoalContinuation()
		return a, start

	case spinnerTickMsg:
		a.spinnerTickScheduled = false
		if a.engine != nil {
			if err := a.engine.SyncRuntimeItems(contextPkg.Background()); err != nil {
				a.showNotification("Runtime input synchronization failed: "+err.Error(), NotifyError)
			}
		}
		attentionCmd := a.syncRuntimeThreadAttention()
		agentRunning := a.syncAgentToolTraces()
		threadRunning, transcriptCmd := a.refreshActiveThreadProjectionWithCmd()
		if a.running || agentRunning || threadRunning {
			if !a.reducedMotion {
				a.spinnerCount++
				a.chat.SetSpinnerCount(a.spinnerCount)
				a.streamingCtx.Tick()
				a.thinkingInd.Tick()
				a.chat.AnimateStep()
			} else {
				a.chat.FinishScrollAnimation()
			}
			// Permission timeout and runtime polling remain functional.
			if a.permQueue.IsActive() {
				if a.permQueue.Prompt().Tick() {
					// Timeout expired — advance queue
					if !a.permQueue.AdvanceQueue() {
						a.popDialog(StatePermission)
					}
				}
			}
			tick := a.ensureSpinnerTick()
			return a, tea.Batch(tick, attentionCmd, transcriptCmd)
		}
		return a, tea.Batch(attentionCmd, transcriptCmd)

	case scrollAnimTickMsg:
		if a.reducedMotion {
			a.chat.FinishScrollAnimation()
			return a, nil
		}
		if a.chat.AnimateStep() {
			return a, scrollAnimTick()
		}
		return a, nil

	case taskExplorerTickMsg:
		if a.state == StateTaskPanel && a.taskExplorer != nil {
			a.taskExplorer.Refresh()
			return a, tea.Batch(
				taskExplorerTickCmd(),
				a.taskExplorer.ensureLazyDetail(false),
			)
		}
		return a, nil

	case bgTaskTickMsg:
		agentRunning := a.syncAgentToolTraces()
		threadRunning, transcriptCmd := a.refreshActiveThreadProjectionWithCmd()
		if a.hasDialog(StateBackgroundTasks) {
			a.backgroundTasks.Refresh()
			panelTranscriptCmd := a.backgroundTasks.ensureTranscriptPage()
			if agentRunning || threadRunning {
				return a, tea.Batch(bgTaskTickCmd(), a.ensureSpinnerTick(), transcriptCmd, panelTranscriptCmd)
			}
			return a, tea.Batch(bgTaskTickCmd(), transcriptCmd, panelTranscriptCmd)
		}
		if agentRunning || threadRunning {
			tick := a.ensureSpinnerTick()
			return a, tea.Batch(tick, transcriptCmd)
		}
		return a, transcriptCmd

	case teamsTickMsg:
		agentRunning := a.syncAgentToolTraces()
		threadRunning, transcriptCmd := a.refreshActiveThreadProjectionWithCmd()
		if a.hasDialog(StateTeams) {
			a.teamsPanel.Refresh()
			panelTranscriptCmd := a.teamsPanel.ensureTranscriptPage()
			if agentRunning || threadRunning {
				return a, tea.Batch(teamsTickCmd(), a.ensureSpinnerTick(), transcriptCmd, panelTranscriptCmd)
			}
			return a, tea.Batch(teamsTickCmd(), transcriptCmd, panelTranscriptCmd)
		}
		if agentRunning || threadRunning {
			tick := a.ensureSpinnerTick()
			return a, tea.Batch(tick, transcriptCmd)
		}
		return a, transcriptCmd

	case agentTranscriptPageLoadedMsg:
		a.applyAgentTranscriptPage(msg)
		a.updateLayout()
		return a, nil
	case taskExplorerExecutionDetailLoadedMsg:
		if a.state == StateTaskPanel && a.taskExplorer != nil {
			a.taskExplorer.applyExecutionDetail(msg)
			a.updateLayout()
		}
		return a, nil
	case shellResultMsg:
		if msg.isError {
			a.chat.UpdateToolError(msg.toolID, "Bash", msg.result)
		} else {
			a.chat.UpdateToolResult(msg.toolID, "Bash", msg.result)
		}
		a.updateLayout()
		return a, nil

	case permissionTimeoutTickMsg:
		if a.hasDialog(StatePermission) {
			return a, permissionTimeoutTick()
		}
		return a, nil
	}

	if a.hasDialog(StatePlanApproval) &&
		a.planDialog.feedbackFocused() {
		cmd := a.planDialog.Update(msg)
		a.updateLayout()
		return a, cmd
	}

	// Update textarea
	var cmd tea.Cmd
	a.textarea, cmd = a.textarea.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	a.updateLayout() // recalc if textarea height changed

	return a, tea.Batch(cmds...)
}

// View implements tea.Model. Terminal capabilities are declared with the
// rendered frame so Bubble Tea v2 can restore them after suspend and external
// process handoff without imperative re-enable commands.
func (a *App) View() tea.View {
	view := tea.NewView(a.renderView())
	view.WindowTitle = identity.CommandName
	view.AltScreen = a.fullscreen
	view.ReportFocus = a.terminalCaps.FocusReporting
	if a.mouseEnabled {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view
}

// renderView builds the terminal content without changing presentation state.
func (a *App) renderView() string {
	if a.quitting {
		return a.finalizeView(a.renderGoodbye())
	}
	if a.width == 0 {
		return a.finalizeView("Initializing...")
	}

	// Terminal too small — show a styled message instead of the normal view.
	if a.width < minTermWidth || a.height < minTermHeight {
		return a.finalizeView(a.renderWindowTooSmall())
	}

	baseState := a.baseState()
	var view string
	if baseState == StateWelcome {
		view = a.renderWelcomeView()
	} else if baseState == StateExpand { //nolint:staticcheck
		// Dedicated expand view — full screen scrollable content
		expandRendered := a.renderExpandView()
		if a.selection.HasExpandSelection() {
			expandRendered = a.applyExpandHighlight(expandRendered)
		}
		view = renderLayoutBands(a.renderEnvironment.profile, a.width, a.height, layoutBand{
			rect: a.layout.overlayRect, content: expandRendered,
		})
	} else if baseState == StateTaskPanel {
		// Dedicated task panel — full screen scrollable task list
		view = renderLayoutBands(a.renderEnvironment.profile, a.width, a.height, layoutBand{
			rect: a.layout.overlayRect, content: a.renderTaskPanel(),
		})
	} else {
		contextBar := ""
		if baseState == StateSearch {
			contextBar = a.search.Render(a.layout.headerRect.Width)
		}
		if baseState == StateMessageSelect {
			contextBar = a.msgSelector.RenderHintBar(a.layout.headerRect.Width)
		}

		// Chat area with selection highlight overlay.
		chatRendered := a.chat.Render(a.layout.chatRect.Width, a.layout.chatRect.Height)
		if a.selection.HasSelection() {
			chatRendered = a.applyViewportHighlight(chatRendered)
		}

		var activity []string
		if spinnerLine := a.renderSpinner(); spinnerLine != "" {
			activity = append(activity, spinnerLine)
		}
		if taskTree := a.renderTaskTree(); taskTree != "" && a.layout.mode != layoutModeWide {
			activity = append(activity, taskTree)
		}

		hints := ""
		editor := ""
		if a.chat.Following() {
			hints = a.renderHintSection()
			editor = a.renderEditor()
		}

		main := renderLayoutBands(a.renderEnvironment.profile, a.layout.width, a.height,
			layoutBand{rect: a.layout.headerRect, content: contextBar},
			layoutBand{rect: a.layout.chatRect, content: chatRendered, alignBottom: a.chat.Following()},
			layoutBand{rect: a.layout.activityRect, content: strings.Join(activity, "\n")},
			layoutBand{rect: a.layout.hintRect, content: hints},
			layoutBand{rect: a.layout.editorRect, content: editor},
			layoutBand{rect: a.layout.statusRect, content: a.renderStatus()},
		)
		view = joinLayoutColumns(
			a.renderEnvironment.profile, main, a.renderWideSidebar(), a.layout.width,
			a.layout.sidebarRect.Width, a.height,
		)
	}

	view = a.renderActiveDialog(view)

	return a.finalizeView(renderLayoutBands(a.renderEnvironment.profile, a.width, a.height, layoutBand{
		rect: a.layout.overlayRect, content: view,
	}))
}

func (a *App) finalizeView(view string) string {
	profile := DefaultDisplayCellProfile()
	width := 0
	if a != nil {
		profile = a.renderEnvironment.profile
		width = a.width
		if a.terminalCaps.Color == terminalcap.ColorNone {
			view = xansi.Strip(view)
		}
	}
	bounded, _ := finalizeFrameGeometry(view, width, profile)
	return bounded
}

// frameGeometryDiagnostic records the first physical row that exceeded the
// selected App grid before final-frame truncation. It is intentionally
// package-private so development and focused tests can inspect the boundary
// without adding visible chrome or View side effects.
type frameGeometryDiagnostic struct {
	Profile          string
	FirstOverflowRow int
	MeasuredWidth    int
	Limit            int
}

func (d frameGeometryDiagnostic) diagnosticSummary() string {
	return d.Profile + fmt.Sprintf("\nFirst overflow: row=%d width=%d limit=%d", d.FirstOverflowRow, d.MeasuredWidth, d.Limit)
}

// finalizeFrameGeometry closes controls per physical row before applying the
// immutable App-selected profile's cell boundary. It does not allocate layout
// rectangles or mutate App state.
func finalizeFrameGeometry(view string, width int, profile DisplayCellProfile) (string, *frameGeometryDiagnostic) {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	lines := profile.balanceControlLines(strings.Split(view, "\n"))
	var diagnostic *frameGeometryDiagnostic
	for row, line := range lines {
		measured := profile.measure(line, 0)
		if width > 0 && measured > width && diagnostic == nil {
			diagnostic = &frameGeometryDiagnostic{
				Profile:          profile.diagnosticSummary(),
				FirstOverflowRow: row,
				MeasuredWidth:    measured,
				Limit:            width,
			}
		}
		if width > 0 {
			lines[row] = profile.truncate(line, width)
		}
	}
	return strings.Join(lines, "\n"), diagnostic
}

func (a *App) renderHintSection() string {
	var hints string
	if hints = a.renderHistorySearch(); hints == "" {
		hints = a.renderMentionHints()
	}
	if hints == "" {
		hints = a.renderCommandHints()
	}
	if hints == "" {
		hints = a.renderFileHints()
	}
	queued := a.renderQueuedInputRows()
	if a.layout.mode == layoutModeCompact && hints != "" {
		// Autocomplete owns the compact hint window while it is active. Keep
		// queued previews below it so clipping cannot hide the selected item.
		content := hints
		if queued != "" {
			content += "\n" + queued
		}
		return a.renderHintBox(content)
	}
	content := queued
	if hints != "" {
		if content != "" {
			content += "\n"
		}
		content += hints
	}
	if content == "" {
		return ""
	}
	return a.renderHintBox(content)
}

func (a *App) renderHintBox(content string) string {
	boxStyle := a.styles.HintBorder
	ownerWidth := a.layout.width
	if ownerWidth <= 0 {
		ownerWidth = a.width
	}
	boxWidth := ownerWidth - 2
	if boxWidth < 20 {
		boxWidth = 20
	}
	profile := a.renderEnvironment.normalized().profile
	lines := contentProjectRows(
		profile,
		strings.Split(content, "\n"),
		max(ownerWidth-4, 1),
		2,
	)
	rendered := contentRenderStyleWidth(
		profile,
		boxStyle,
		boxWidth,
		strings.Join(lines, "\n"),
	)
	return strings.Join(
		contentProjectRows(profile, strings.Split(rendered, "\n"), ownerWidth, 0),
		"\n",
	)
}

// renderWindowTooSmall renders a styled message when the terminal dimensions
// are below the minimum required for the normal layout.
func (a *App) renderWindowTooSmall() string {
	title := a.styles.Warning.Bold(true).Render("Terminal too small")
	current := fmt.Sprintf("Current: %d x %d", a.width, a.height)
	minimum := fmt.Sprintf("Minimum: %d x %d", minTermWidth, minTermHeight)
	hint := a.styles.Subtle.Render("Please resize your terminal window.")

	// Build the content block
	lines := []string{
		"",
		title,
		"",
		current,
		minimum,
		"",
		hint,
		"",
	}
	// Preserve Lip Gloss Place's per-line horizontal centering and block-level
	// vertical centering while selecting cells only through the App profile.
	profile := a.renderEnvironment.normalized().profile
	startY := max(0, (a.height-len(lines))/2)
	projected := make([]string, startY, startY+len(lines))
	for _, line := range lines {
		projected = append(
			projected,
			centerLineWithProfile(profile, line, a.width),
		)
	}
	view, _ := modalTopProjectedFrame(profile, projected, a.height)
	return view
}

func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	// Track idle state: show welcome-back toast when returning from idle
	if idleDur, wasIdle := a.idle.recordInput(); wasIdle {
		a.showToast(idleReturnMessage(idleDur))
	}

	// Ctrl+C remains non-rebindable. History search consumes it as local
	// cancellation before the active-turn interrupt path.
	if msg.String() == "ctrl+c" {
		if a.historySearch.Active {
			a.cancelHistorySearch()
			return nil
		}
		return a.handleInterrupt()
	}
	if a.historySearch.Active {
		return a.handleHistorySearchKey(msg)
	}

	if handled, cmd := a.handleActiveDialogKey(msg); handled {
		return cmd
	}

	// Search overlay handles its own keys
	if a.state == StateSearch {
		if handled, cmd := a.resolveKeyAction(msg); handled {
			return cmd
		}
		return a.handleSearchKey(msg)
	}

	// Message selector handles its own keys
	if a.state == StateMessageSelect {
		return a.handleMessageSelectKey(msg)
	}

	// Expand view handles its own keys
	if a.state == StateExpand {
		return a.handleExpandKey(msg)
	}

	// Task panel handles its own keys
	if a.state == StateTaskPanel {
		return a.handleTaskPanelKey(msg)
	}

	// Always route to editor — no focus split (reference pattern)
	return a.handleEditorKey(msg)
}

func (a *App) handleEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	if a.composerInputBlocked() {
		a.showNotification("Wait for the current composer submission to finish", NotifyWarning)
		return nil
	}
	if handled, cmd := a.handleVimEditorKey(msg); handled {
		return tea.Batch(cmd, a.ensureMentionIndex())
	}
	if a.textarea.Value() == "" {
		switch msg.String() {
		case "/":
			a.inputMode = InputCommand
			a.textarea.SetValue("/")
			a.composerElements = nil
			a.updateCommandHints()
			return nil
		case "!", "$":
			a.inputMode = InputShell
			a.textarea.SetValue("")
			a.composerElements = nil
			a.setEditorPrompt()
			a.updateCommandHints()
			return nil
		case "?":
			a.openHelpOverlay()
			return nil
		}
	}
	if msg.String() == "backspace" && a.inputMode == InputShell && a.textarea.Value() == "" {
		a.textarea.Reset()
		a.composerElements = nil
		a.setEditorPrompt()
		a.inputMode = InputNormal
		return nil
	}
	if handled, cmd := a.resolveEditorKeyAction(msg); handled {
		return cmd
	}

	switch {
	case msg.String() == "shift+up":
		if a.selection.HasSelection() {
			a.selection.ExtendUp(a.chat)
			return nil
		}

	case msg.String() == "shift+down":
		if a.selection.HasSelection() {
			a.selection.ExtendDown(a.chat)
			return nil
		}

	}

	// Default: let textarea handle it
	before := a.captureComposerUndoEntry()
	var cmd tea.Cmd
	a.textarea, cmd = a.textarea.Update(msg)
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	if before.Text != a.textarea.Value() {
		a.markComposerChanged()
	}
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	return tea.Batch(cmd, a.ensureMentionIndex())
}

func (a *App) sendMessage() tea.Cmd {
	if a.composerAdmissionPending != nil {
		a.showNotification("A composer submission is already being accepted", NotifyWarning)
		return nil
	}
	if a.composerImageLoadPending != nil {
		a.showNotification("Wait for the image attachment to finish loading", NotifyWarning)
		return nil
	}
	snapshot, composerErr := a.captureComposerSubmission()
	if composerErr != nil {
		a.showNotification(composerErr.Error(), NotifyWarning)
		return nil
	}
	value := snapshot.Text
	if len(snapshot.Input.Parts) == 0 {
		return nil
	}
	if !a.commandPaletteAdmissionMatches(value) {
		// A newer manual submission supersedes any unmatched palette result,
		// even when that input is queued behind the active query.
		a.commandPaletteSubmission = nil
	}
	if a.inputMode == InputShell {
		if snapshot.HasImages || snapshot.HasContext {
			a.showNotification("Attachments cannot be submitted in shell mode", NotifyWarning)
			return nil
		}
		return a.sendShellCommand(value)
	}
	if a.inputMode == InputCommand || commands.IsCommand(value) {
		if snapshot.HasImages || snapshot.HasContext {
			a.showNotification("Attachments cannot be submitted with slash commands", NotifyWarning)
			return nil
		}
		return a.sendSlashCommand(value)
	}
	if value == "/exit" || value == "/quit" || value == "exit" || value == "quit" {
		a.quitting = true
		return nil
	}
	if !a.isLeaderThreadView() {
		if snapshot.HasImages {
			a.showNotification("Image input is available only in the leader thread", NotifyWarning)
			return nil
		}
		return a.sendActiveAgentThreadMessage(value)
	}

	// Busy Enter queues/steers. Ctrl+C remains the explicit cancellation path.
	if a.running {
		return a.beginComposerAdmission(snapshot, true)
	}
	return a.beginComposerAdmission(snapshot, false)
}

func (a *App) handleEngineEvent(evt engine.QueryEvent) tea.Cmd { //nolint:unparam
	attemptID := ""
	if evt.ModelAttempt != nil {
		attemptID = evt.ModelAttempt.AttemptID
	}
	switch evt.Type {
	case engine.EventAssistant:
		if evt.AssistantMessage != nil {
			// Complete assistant message (non-streaming path)
			a.spinnerState.RecordEvent()
			if evt.AssistantMessage.ReasoningContent != "" {
				a.chat.StreamThinkingDeltaAttempt(
					evt.AssistantMessage.ReasoningContent,
					attemptID,
				)
				if !a.thinkingInd.IsActive() {
					a.thinkingInd.Start()
				}
				a.chat.FinishThinking()
				a.thinkingInd.Stop()
			}
			if evt.AssistantMessage.Content != "" {
				a.spinnerState.Mode = SpinnerResponding
				a.chat.AppendOrUpdateAssistantAttempt(
					evt.AssistantMessage.Content,
					attemptID,
				)
			}
			if len(evt.AssistantMessage.ToolCalls) > 0 {
				a.chat.FinishThinking()
				a.thinkingInd.Stop()
				for _, tc := range evt.AssistantMessage.ToolCalls {
					a.spinnerState.Mode = SpinnerToolUse
					a.spinnerState.ToolName = tc.Function.Name
					a.chat.AppendOrUpdateToolAttempt(
						tc.ID,
						tc.Function.Name,
						tc.Function.Arguments,
						attemptID,
					)
					a.trackToolStart(
						tc.ID,
						tc.Function.Name,
						tc.Function.Arguments,
						attemptID,
					)
				}
			}
		} else if evt.Message != nil {
			// Streaming delta — accumulate, don't replace
			a.spinnerState.RecordEvent()
			if evt.Message.ReasoningContent != "" {
				a.chat.StreamThinkingDeltaAttempt(
					evt.Message.ReasoningContent,
					attemptID,
				)
				if !a.thinkingInd.IsActive() {
					a.thinkingInd.Start()
				}
			}
			if evt.Message.Content != "" {
				a.spinnerState.Mode = SpinnerResponding
				// Wire streaming context
				if !a.streamingCtx.IsStreaming() {
					a.streamingCtx.BeginStreaming()
				}
				a.streamingCtx.OnStreamDelta(evt.Message.Content)
				a.chat.StreamAssistantDeltaAttempt(
					evt.Message.Content,
					attemptID,
				)
			}
			if len(evt.Message.ToolCalls) > 0 {
				a.chat.FinishThinking()
				a.thinkingInd.Stop()
				for _, tc := range evt.Message.ToolCalls {
					a.spinnerState.Mode = SpinnerToolUse
					a.spinnerState.ToolName = tc.Function.Name
					a.chat.AppendOrUpdateToolAttempt(
						tc.ID,
						tc.Function.Name,
						tc.Function.Arguments,
						attemptID,
					)
					a.trackToolStart(
						tc.ID,
						tc.Function.Name,
						tc.Function.Arguments,
						attemptID,
					)
				}
			}
		}

	case engine.EventStream:
		if evt.StreamEvent != nil {
			a.spinnerState.RecordEvent()
			if evt.StreamEvent.ReasoningContent != "" {
				a.chat.StreamThinkingDeltaAttempt(
					evt.StreamEvent.ReasoningContent,
					attemptID,
				)
				if !a.thinkingInd.IsActive() {
					a.thinkingInd.Start()
				}
			}
			if evt.StreamEvent.Content != "" {
				a.spinnerState.Mode = SpinnerResponding
				a.chat.StreamAssistantDeltaAttempt(
					evt.StreamEvent.Content,
					attemptID,
				)
				if !a.streamingCtx.IsStreaming() {
					a.streamingCtx.BeginStreaming()
				}
				a.streamingCtx.OnStreamDelta(evt.StreamEvent.Content)
			}
		}

	case engine.EventToolResult:
		a.spinnerState.RecordEvent()
		toolCallID := ""
		name := "tool"
		content := ""
		isError := false
		if evt.ToolResultMessage != nil {
			toolCallID = evt.ToolResultMessage.ToolCallID
			if evt.ToolResultMessage.ToolName != "" {
				name = evt.ToolResultMessage.ToolName
			}
			content = evt.ToolResultMessage.Content
			if evt.ToolResultMessage.Extra != nil {
				if _, ok := evt.ToolResultMessage.Extra["is_error"]; ok {
					isError = true
				}
			}
		} else if evt.Message != nil {
			toolCallID = evt.Message.ToolCallID
			content = evt.Message.Content
			if evt.Message.ToolName != "" {
				name = evt.Message.ToolName
			}
		}
		if isError {
			a.chat.UpdateToolError(toolCallID, name, content)
			a.trackToolComplete(toolCallID, ToolError)
		} else {
			a.chat.UpdateToolResult(toolCallID, name, content)
			a.trackToolComplete(toolCallID, ToolSuccess)
		}

	case engine.EventPlanStateTransition:
		if evt.PlanStateTransition != nil {
			a.permMode = evt.PlanStateTransition.PermissionMode
		}

	case engine.EventToolProgress:
		// Show streaming progress on running tools (e.g., Bash stdout)
		if evt.ToolProgress != nil && !evt.ToolProgress.IsFinal {
			a.chat.UpdateToolProgress(evt.ToolProgress.ToolUseID, evt.ToolProgress.ToolName, evt.ToolProgress.Content)
			a.toolProgress.UpdateProgress(evt.ToolProgress.ToolUseID, evt.ToolProgress.Content)
		}

	case engine.EventTaskProgress:
		if a.engine != nil {
			break
		}
		if evt.TaskProgress != nil {
			tp := evt.TaskProgress
			if tp.Subtype == "done" || tp.Type == "done" {
				// Task completed — remove from active tree
				delete(a.activeTasks, tp.TaskID)
			} else {
				// Create or update entry
				entry, ok := a.activeTasks[tp.TaskID]
				if !ok {
					entry = &taskEntry{taskID: tp.TaskID}
					a.activeTasks[tp.TaskID] = entry
				}
				if tp.Description != "" {
					entry.description = tp.Description
				}
				if tp.ToolUseID != "" {
					entry.toolUseID = tp.ToolUseID
				}
				if tp.LastToolName != "" {
					entry.lastTool = tp.LastToolName
				}
				if tp.Summary != "" {
					entry.summary = tp.Summary
				}
				entry.toolUses = tp.Usage.ToolUses
				entry.totalTokens = tp.Usage.TotalTokens
				entry.durationMS = tp.Usage.DurationMS
			}
		}
		a.updateLayout()

	case engine.EventTaskLifecycle:
		if a.engine != nil {
			break
		}
		if evt.TaskLifecycle != nil {
			task := evt.TaskLifecycle
			if task.TaskID == "" {
				return nil
			}
			entry, ok := a.activeTasks[task.TaskID]
			if !ok {
				entry = &taskEntry{taskID: task.TaskID, local: true}
				a.activeTasks[task.TaskID] = entry
			}
			entry.local = true
			entry.description = task.Subject
			entry.activeForm = task.ActiveForm
			entry.status = task.Status
			entry.phase = task.Phase
			entry.owner = task.Owner
		}
		a.updateLayout()

	case engine.EventHookStatus:
		if evt.HookStatus != nil {
			if evt.HookStatus.Phase == "completed" {
				a.refreshAsyncHookStatus()
			} else {
				a.hookStatus = evt.HookStatus.StatusMessage
			}
		}

	case engine.EventHookResponse:
		if evt.HookResponse != nil {
			response := evt.HookResponse
			label := strings.TrimSpace(response.StatusMessage)
			if label == "" {
				label = "Background hook"
			}
			if response.Phase == "running" {
				if response.StatusMessage != "" {
					a.setAsyncHookStatus(response.HookID, response.StatusMessage, true)
				}
				break
			}
			a.setAsyncHookStatus(response.HookID, "", false)
			switch response.Outcome {
			case "failed", "timed_out", "cancelled":
				a.showNotification(fmt.Sprintf("%s %s (exit %d)", label, strings.ReplaceAll(response.Outcome, "_", " "), response.ExitCode), NotifyWarning)
			default:
				if response.StatusMessage != "" {
					a.showToast(label + " completed")
				}
			}
		}

	case engine.EventClassifierStatus:
		if evt.ClassifierStatus != nil {
			switch evt.ClassifierStatus.Phase {
			case engine.ClassifierStatusChecking:
				a.classifierChecking = evt.ClassifierStatus.ToolName
			case engine.ClassifierStatusCompleted, engine.ClassifierStatusCleared:
				a.classifierChecking = ""
			}
		}

	case engine.EventPermissionReview:
		if evt.PermissionReview != nil {
			review := evt.PermissionReview
			tool := permissionReviewDisplayToken(review.CanonicalTool, "tool")
			switch review.Phase {
			case engine.PermissionReviewChecking:
				a.permissionReview = tool
			case engine.PermissionReviewCompleted:
				a.permissionReview = ""
				decision := permissionReviewDisplayToken(review.Decision, "unknown")
				reason := permissionReviewDisplayToken(review.ReasonCode, "unknown")
				a.showToast(fmt.Sprintf(
					"Advisory permission review completed for %s: %s (%s)",
					tool,
					decision,
					reason,
				))
			case engine.PermissionReviewUnavailable:
				a.permissionReview = ""
				reason := permissionReviewDisplayToken(review.ReasonCode, "unavailable")
				a.showNotification(fmt.Sprintf(
					"Advisory permission review unavailable for %s: %s",
					tool,
					reason,
				), NotifyWarning)
			}
		}

	case engine.EventCompactBoundary:
		// Only render the visual boundary for the first "compact_boundary" subtype
		// event. The engine emits multiple EventCompactBoundary events per compaction
		// batch (boundary marker, summary, preserved tail); we show a single rich
		// summary marker and suppress duplicates.
		msg := evt.CompactBoundaryMessage
		if msg == nil {
			break
		}
		subtype, _ := msg.Extra["subtype"].(string)
		if subtype == "snip_boundary" {
			stats := "History snipped"
			if tokensFreed, ok := msg.Extra["tokens_freed"].(int); ok && tokensFreed > 0 {
				stats = fmt.Sprintf("History snipped, ~%s tokens freed", formatTokensShort(tokensFreed))
			}
			a.chat.AppendCompactBoundary(stats)
			break
		}
		if subtype == "recovery_boundary" {
			stats := "Recovering context"
			switch reason, _ := msg.Extra["recovery_reason"].(string); reason {
			case "collapse_drain_retry":
				stats = "Context overflow, retrying staged collapse"
			case "reactive_compact_retry":
				stats = "Context overflow, compacting history"
			}
			a.chat.AppendCompactBoundary(stats)
			break
		}
		if subtype != "compact_boundary" {
			// Skip summary and preserved-tail boundary events.
			break
		}

		// The compaction model call reports the input used to build the summary,
		// not the active post-compact context. Until the next main-loop response
		// there is no provider-reported post-compact token fact to render.
		a.chat.AppendCompactBoundaryWithStats(0, 0, 0, 0)
		a.showNotification("Context compacted", NotifySuccess)

	case engine.EventUserInterruption:
		a.chat.FinishAssistant()
		a.appendInterruptionOnce()
		a.activeTasks = make(map[string]*taskEntry)
		a.activeTools = make(map[string]*inlineToolEntry)
		a.activeToolsOrder = nil
		a.hookStatus = "Cancelling..."
		a.classifierChecking = ""
		a.permissionReview = ""
		return nil

	case engine.EventCommandResult:
		result := evt.CommandResult
		if result == nil {
			break
		}
		output := result.Output
		if result.Status == engine.CommandResultSucceeded {
			switch result.Action {
			case commands.ActionNew:
				a.chat.Reset()
				if a.engine != nil {
					a.rebindLeaderThreadView(a.engine.ThreadID())
				}
			case commands.ActionClear:
				a.chat.Reset()
			case commands.ActionCompact:
				if output != "" {
					a.chat.AppendCompactBoundary(output)
					output = ""
				}
			case commands.ActionChangeModel:
				if a.engine != nil {
					a.model = a.engine.GetModelName()
					a.showNotification("Model switched to: "+a.model, NotifySuccess)
				}
			case commands.ActionChangeMode, commands.ActionPlanMode:
				if a.engine != nil {
					a.permMode = a.engine.PermissionMode()
				}
			case commands.ActionAddDir:
				a.showNotification("Working directory added", NotifySuccess)
			case commands.ActionFork:
				a.showNotification("Forked and resumed new session", NotifySuccess)
			}
			a.pendingCommandPrompt = result.FollowUpPrompt
		} else {
			a.showNotification(
				fmt.Sprintf("/%s %s", result.Command, result.Status),
				NotifyWarning,
			)
		}
		if output != "" {
			a.chat.AppendSystem(output)
		}

	case engine.EventTerminal:
		a.chat.FinishAssistant()
		a.running = false
		a.cancelFn = nil
		a.activeTasks = make(map[string]*taskEntry)
		a.activeTools = make(map[string]*inlineToolEntry)
		a.activeToolsOrder = nil
		a.hookStatus = ""
		a.classifierChecking = ""
		a.permissionReview = ""
		if evt.Message != nil && evt.Message.Content != "" {
			a.chat.AppendSystem(evt.Message.Content)
		}
		if evt.TerminalInfo != nil && evt.TerminalInfo.Err != nil && !strings.HasPrefix(evt.TerminalInfo.Err.Error(), "command:") {
			errMsg := evt.TerminalInfo.Err.Error()
			entry := ClassifyError(errMsg)
			var displayMsg string
			if entry.Title != "" && entry.Title != errMsg {
				displayMsg = fmt.Sprintf("[%s] %s: %s", entry.Category, entry.Title, errMsg)
			} else {
				displayMsg = fmt.Sprintf("[%s] %s", entry.Category, errMsg)
			}
			if len(entry.Suggestions) > 0 {
				displayMsg += "\n  Suggestion: " + entry.Suggestions[0].Label
			}
			a.chat.AppendSystem(displayMsg)
		}
		followUpPrompt := a.pendingCommandPrompt
		a.pendingCommandPrompt = ""
		if followUpPrompt != "" {
			return a.startEngineRequest(followUpPrompt)
		}
		return a.scheduleNextRuntimeWork()

	case engine.EventMaxTurnsReached:
		a.chat.FinishAssistant()
		info := "max turns reached"
		if evt.MaxTurnsInfo != nil {
			info = fmt.Sprintf("max turns reached (%d/%d)", evt.MaxTurnsInfo.TurnCount, evt.MaxTurnsInfo.MaxTurns)
		}
		a.chat.AppendSystem(info)
		a.activeTasks = make(map[string]*taskEntry)
		a.activeTools = make(map[string]*inlineToolEntry)
		a.activeToolsOrder = nil
		return nil

	case engine.EventToolUseSummary:
		// Informational - no-op

	case engine.EventCommandLifecycle:
		if evt.CommandLifecycle != nil {
			if evt.CommandLifecycle.Phase == "started" {
				a.hookStatus = "Processing queued command..."
				threadID := evt.ThreadID
				if threadID == "" {
					threadID = a.leaderThreadViewID()
				}
				a.handleQueuedCommandStarted(threadID, evt.CommandLifecycle.CommandUUID)
			} else if evt.CommandLifecycle.Phase == "completed" {
				a.hookStatus = ""
			}
		}

	case engine.EventStreamRequestStart:
		// Model call starting - no-op

	case engine.EventAttachment:
		if evt.AttachmentMessage != nil && evt.AttachmentMessage.Extra != nil {
			kind, _ := evt.AttachmentMessage.Extra["attachment_kind"].(string)
			switch kind {
			case "session_started":
				if a.engine != nil {
					a.rebindLeaderThreadView(a.engine.ThreadID())
				}
			case "session_resumed":
				if a.engine != nil {
					a.rebindLeaderThreadView(a.engine.ThreadID())
				}
				msgCount := len(a.engine.GetMessages())
				a.reloadChatFromEngine()
				a.chat.AppendCompactSummary(msgCount)
			case "image", "file", "pdf":
				name, _ := evt.AttachmentMessage.Extra["filename"].(string)
				if name == "" {
					name = kind
				}
				a.chat.AppendSystem(fmt.Sprintf("[Attached: %s]", name))
			case "skill_prefetch", "memory", "context":
				src, _ := evt.AttachmentMessage.Extra["source"].(string)
				if src == "" {
					src = kind
				}
				a.chat.AppendSystem(fmt.Sprintf("[Context loaded: %s]", src))
			case "queued_command":
				// The command lifecycle promotes the matching queued row into a
				// user message. Avoid a duplicate generic attachment marker.
			default:
				if kind != "" {
					a.chat.AppendSystem(fmt.Sprintf("[Attached: %s]", kind))
				}
			}
		}

	case engine.EventTombstone:
		if attemptID == "" {
			attemptID = evt.TombstoneUUID
		}
		if removed, toolIDs := a.chat.RemoveModelAttempt(attemptID); removed {
			for _, toolCallID := range toolIDs {
				a.trackToolCompleteAttempt(
					toolCallID,
					ToolError,
					attemptID,
				)
			}
			a.streamingCtx.Reset()
			a.thinkingInd.Stop()
		}

	case engine.EventModelAttempt:
		// Runtime state owns bounded attempt facts. The App projects only the
		// safe fallback start and otherwise renders assistant deltas and exact
		// tombstones.
		if notice := modelFallbackNotice(evt.ModelAttempt); notice != "" {
			a.showNotification(notice, NotifyWarning)
		}

	case engine.EventPermissionRequest:
		if evt.PermissionRequest != nil {
			req := evt.PermissionRequest
			// Structured PermissionPromptFn owns presentation. The matching
			// coordinator event still updates runtime state and transcripts, but
			// must not enqueue a second dialog for the same request.
			if req.Source == "coordinator" {
				return nil
			}
			inputJSON, _ := jsonPkg.Marshal(req.Input)
			kind := threadAttentionPermission
			switch req.Kind {
			case engine.PermissionInteractionKindRepeatedTool:
				kind = threadAttentionRepeatedTool
			case engine.PermissionInteractionKindQuestion:
				kind = threadAttentionQuestion
			case engine.PermissionInteractionKindPlanApproval:
				kind = threadAttentionPlan
			}
			var responseCh chan PermissionResponse
			source := req.Source
			if source == "" {
				source = "prompter"
			}
			if source != "callback" {
				responseCh = make(chan PermissionResponse, 1)
				go func(request engine.PermissionPromptRequest) {
					resp := <-responseCh
					if a.engine != nil {
						if source == "project_graph" {
							responseData := a.takeThreadAttentionResponse(
								request.ToolUseID,
							)
							a.engine.ResolvePermissionInteraction(
								request.ToolUseID,
								permissionInteractionResult(
									request,
									resp,
									responseData,
								),
							)
							return
						}
						decision := permission.DecisionDeny
						if resp == PermissionAllow ||
							resp == PermissionAllowSession ||
							resp == PermissionAllowAlways {
							decision = permission.DecisionAllow
						}
						a.engine.ResolvePermission(
							request.ToolUseID,
							decision,
							"",
						)
					}
				}(engine.PermissionPromptRequest{
					Kind:               req.Kind,
					Attempt:            req.Attempt,
					Source:             req.Source,
					ToolName:           req.ToolName,
					CanonicalToolName:  req.CanonicalToolName,
					ToolUseID:          req.ToolUseID,
					Input:              req.Input,
					Message:            req.Message,
					SessionScope:       req.Message,
					SessionID:          evt.SessionID,
					ThreadID:           evt.ThreadID,
					AgentID:            evt.AgentID,
					PlanApproval:       req.PlanApproval,
					Presentation:       cloneTUIPermissionPresentation(req.Presentation),
					DecisionConstraint: req.DecisionConstraint,
				})
			}
			return a.enqueueThreadAttention(threadAttentionRequest{
				ID: req.ToolUseID, ThreadID: evt.ThreadID, AgentID: evt.AgentID,
				Kind: kind, Tool: req.ToolName, Input: string(inputJSON), SessionScope: req.Message, Attempt: req.Attempt,
				SessionID: evt.SessionID, Source: source,
				PlanApproval: req.PlanApproval, responseCh: responseCh,
				decisionConstraint: req.DecisionConstraint,
			})
		}

	case engine.EventPermissionResolved:
		if evt.PermissionResolved != nil {
			return a.cancelThreadAttention(evt.PermissionResolved.ToolUseID)
		}

	default:
		// Unknown event type - continue consuming
	}

	return nil
}

func cloneTUIPermissionPresentation(
	presentation *engine.PermissionPresentation,
) *engine.PermissionPresentation {
	if presentation == nil {
		return nil
	}
	cloned := *presentation
	cloned.Evidence = append([]engine.PermissionPresentationEvidence(nil), presentation.Evidence...)
	cloned.GrantScopes = append([]engine.PermissionInteractionDecision(nil), presentation.GrantScopes...)
	return &cloned
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

func (a *App) waitForEvent() tea.Cmd {
	if a.eventChan == nil {
		return nil
	}
	qid := a.queryID
	ch := a.eventChan
	return func() tea.Msg {
		// Block for the first event
		evt, ok := <-ch
		if !ok {
			return eventsDoneMsg{queryID: qid}
		}
		// Batch at an exact 30fps ceiling to accumulate fast stream deltas.
		// This prevents >30 Update→View cycles per second during fast streaming.
		batch := []engine.QueryEvent{evt}
		timer := time.NewTimer(streamBatchWindow)
		defer timer.Stop()
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return engineBatchMsg{events: batch, queryID: qid, done: true}
				}
				batch = append(batch, e)
				if len(batch) >= 64 {
					return engineBatchMsg{events: batch, queryID: qid}
				}
			case <-timer.C:
				// Timer expired — deliver what we have
				if len(batch) == 1 {
					return engineEventMsg{event: batch[0], queryID: qid}
				}
				return engineBatchMsg{events: batch, queryID: qid}
			}
		}
	}
}

func (a *App) setAsyncHookStatus(hookID, status string, running bool) {
	if a == nil || hookID == "" {
		return
	}
	if a.asyncHookStatuses == nil {
		a.asyncHookStatuses = make(map[string]string)
	}
	if running {
		if _, exists := a.asyncHookStatuses[hookID]; !exists {
			a.asyncHookOrder = append(a.asyncHookOrder, hookID)
		}
		a.asyncHookStatuses[hookID] = status
	} else {
		delete(a.asyncHookStatuses, hookID)
		for index, id := range a.asyncHookOrder {
			if id == hookID {
				a.asyncHookOrder = append(a.asyncHookOrder[:index], a.asyncHookOrder[index+1:]...)
				break
			}
		}
	}
	a.refreshAsyncHookStatus()
}

func (a *App) refreshAsyncHookStatus() {
	a.hookStatus = ""
	for index := len(a.asyncHookOrder) - 1; index >= 0; index-- {
		if current := a.asyncHookStatuses[a.asyncHookOrder[index]]; current != "" {
			a.hookStatus = current
			break
		}
	}
}

func (a *App) waitForAsyncHookEvent() tea.Cmd {
	if a == nil || a.engine == nil {
		return nil
	}
	ch := a.engine.SubscribeAsyncHookEvents()
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		event, open := <-ch
		return asyncHookEventMsg{event: event, open: open}
	}
}

func (a *App) navigateHistory(direction int) {
	if len(a.history) == 0 {
		return
	}
	before := a.captureComposerUndoEntry()
	if a.historyIdx == len(a.history) {
		a.draft = a.textarea.Value()
		a.draftElements = cloneThreadComposerElements(a.composerElements)
	}

	a.historyIdx += direction
	if a.historyIdx < 0 {
		a.historyIdx = 0
	}
	if a.historyIdx > len(a.history) {
		a.historyIdx = len(a.history)
	}

	if a.historyIdx == len(a.history) {
		a.restoreComposerHistoryEntry(a.draft, a.draftElements)
		a.historySetText = ""
	} else {
		a.restoreComposerHistoryEntry(a.history[a.historyIdx], a.richHistoryElements[a.historyIdx])
		// Mark the recalled text: autocomplete hints stay suppressed while the
		// user is still traversing history, so up/down keep navigating instead
		// of moving inside a hint list the recall itself triggered.
		a.historySetText = a.textarea.Value()
	}
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
}

// setEditorPrompt updates the textarea prompt character, showing it only
// on the first display line with blank indent on continuation lines.
func (a *App) setEditorPrompt() {
	a.textarea.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
}

func (a *App) updateLayout() {
	sidebarVisible := a.wideSidebarVisible()
	mode, mainWidth, _ := responsiveLayoutDimensions(a.width, a.height, sidebarVisible)
	// Keep the mode current before measuring hint content: the compact
	// candidate window renders fewer rows than standard/wide.
	a.layout.mode = mode
	// Compute the editor width (border takes 2 chars on each side).
	editorWidth := mainWidth - 4
	if editorWidth < 20 {
		editorWidth = 20
	}
	a.textarea.SetWidth(editorWidth)
	// Count visual lines using word-wrap simulation that matches the
	// textarea's internal wrapping. The effective content width is
	// editorWidth minus the prompt ("❯ " = 2 chars).
	contentWidth := editorWidth - 2
	if contentWidth < 10 {
		contentWidth = 10
	}
	lines := countVisualLines(a.textarea.Value(), contentWidth)
	editorVisible := a.chat.Following()
	hintHeight := 0
	if editorVisible {
		if hints := a.renderHintSection(); hints != "" {
			hintHeight = lipgloss.Height(hints)
		}
	}
	taskTreeHeight := 0
	if tree := a.renderTaskTree(); tree != "" && mode != layoutModeWide {
		taskTreeHeight = lipgloss.Height(tree)
	}
	contextHeight := 0
	baseState := a.baseState()
	if baseState == StateSearch || baseState == StateMessageSelect {
		contextHeight = 1
	}
	a.layout = calculateLayout(layoutRequest{
		totalWidth: a.width, totalHeight: a.height,
		editorContentRows: lines, hintHeight: hintHeight,
		taskTreeHeight: taskTreeHeight, contextHeight: contextHeight,
		spinnerVisible: a.running, editorVisible: editorVisible,
		sidebarVisible: sidebarVisible,
	})

	// Resize components
	// Border takes 2 chars on each side (border + padding)
	editorWidth = a.layout.width - 4
	if editorWidth < 20 {
		editorWidth = 20
	}
	a.textarea.SetWidth(editorWidth)
	newHeight := max(1, a.layout.editorHeight-2) // -2 for border top/bottom
	oldHeight := a.textarea.Height()
	a.textarea.SetHeight(newHeight)
	if newHeight > oldHeight {
		// Height increased. Reset viewport to show all content from top.
		// SetValue resets internal viewport offset to 0 and places cursor
		// at the end, then repositionView ensures cursor stays visible.
		val := a.textarea.Value()
		a.textarea.SetValue(val)
	}
	a.textarea, _ = a.textarea.Update(nil)
	a.chat.SetSize(a.layout.width, a.layout.chatHeight)
}

// renderHeader renders a condensed logo line: product name · model · cwd.
// Reference: CondensedLogo.tsx — shows product, model, billing, cwd.
// Version is set at build time via -ldflags. Falls back to "dev".
var Version = "dev"

// applyViewportHighlight applies visual reverse-video highlighting to the
// rendered chat output for the portion of the selection that is visible
// in the current viewport. Uses item-based coordinates from Selection.
func (a *App) applyViewportHighlight(rendered string) string {
	if !a.selection.HasSelection() {
		return rendered
	}
	startRow, startCell, endRow, endCell, ok := a.selection.GetViewportHighlightRange(a.chat)
	if !ok {
		return rendered
	}
	projection := a.chat.currentViewportProjection()
	if projection == nil {
		return rendered
	}
	lines := strings.Split(rendered, "\n")
	for row := startRow; row <= endRow && row < len(lines); row++ {
		if row < 0 {
			continue
		}
		if row >= len(projection.rows) || projection.rows[row].kind != chatViewportRowTranscript {
			continue
		}
		descriptor := projection.rows[row]
		fromCell := 0
		toCell := selectionLineCells(a.renderEnvironment.profile, lines[row])
		if row == startRow {
			fromCell = startCell
		}
		if row == endRow {
			toCell = endCell
		}
		ranges := a.chat.selectionHighlightRanges(
			descriptor.itemIdx,
			descriptor.lineInItem,
			fromCell,
			toCell,
		)
		for _, cellRange := range ranges {
			lines[row] = selectionHighlightCells(
				a.renderEnvironment.profile,
				lines[row],
				cellRange[0],
				cellRange[1],
			)
		}
	}
	return strings.Join(lines, "\n")
}

func (a *App) expandMouseSelectionRow(msg tuiMouseMsg) (int, bool) {
	contentStartRow := 0
	if a.expandSearch.Visible() {
		contentStartRow = 1
	}
	contentRows := a.height - 1 - contentStartRow
	if contentRows <= 0 {
		return 0, false
	}
	row := msg.Y - contentStartRow
	if row >= 0 && row < contentRows {
		return a.expandOffset + row, true
	}
	// Chrome never starts a selection. Motion/release from an existing drag
	// clamps to the nearest content edge so leaving through search/status rows
	// cannot keep the drag open or select a non-content row.
	if !a.selection.isDragging ||
		(msg.Action != mouseActionMotion && msg.Action != mouseActionRelease) {
		return 0, false
	}
	if row < 0 {
		row = 0
	} else {
		row = contentRows - 1
	}
	return a.expandOffset + row, true
}

// applyExpandHighlight applies selection highlighting to the expand view.
func (a *App) applyExpandHighlight(rendered string) string {
	if !a.selection.HasExpandSelection() {
		return rendered
	}
	sr, startCell, er, endCell := a.selection.GetExpandBounds()
	// Translate to viewport-relative.
	sr -= a.expandOffset
	er -= a.expandOffset
	contentStartRow := 0
	if a.expandSearch.Visible() {
		contentStartRow = 1
	}
	viewH := a.height - 1 - contentStartRow
	if er < 0 || sr >= viewH {
		return rendered
	}
	if sr < 0 {
		sr = 0
		startCell = 0
	}
	if er >= viewH {
		er = viewH - 1
		endCell = 9999
	}
	lines := strings.Split(rendered, "\n")
	for row := sr; row <= er && row < len(lines); row++ {
		if row < 0 {
			continue
		}
		lineIndex := contentStartRow + row
		if lineIndex < 0 || lineIndex >= len(lines) {
			continue
		}
		fromCell := 0
		toCell := selectionLineCells(a.renderEnvironment.profile, lines[lineIndex])
		if row == sr {
			fromCell = startCell
		}
		if row == er {
			toCell = endCell
		}
		lines[lineIndex] = selectionHighlightCells(
			a.renderEnvironment.profile,
			lines[lineIndex],
			fromCell,
			toCell,
		)
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderHeader() string {
	// Single-line condensed header: title · model · mode · cwd
	title := a.styles.Header.Render(identity.ProductName)

	var parts []string
	parts = append(parts, title)
	parts = append(parts, a.styles.Dim.Render(a.activeThreadDisplayLabel()))

	if a.model != "" {
		modelStr := a.model
		effortSuffix := a.effortSuffix()
		if effortSuffix != "" {
			modelStr += " " + effortSuffix
		}
		parts = append(parts, a.styles.Dim.Render(modelStr))
	}

	// Mode indicator
	permMode := a.permissionMode()
	switch {
	case a.inputMode == InputShell:
		parts = append(parts, a.styles.Warning.Bold(true).Render("[SHELL]"))
	case permMode == permission.ModePlan:
		parts = append(parts, a.styles.AuroraSky.Bold(true).Render("[PLAN]"))
	case permMode == permission.ModeBypassPermissions:
		parts = append(parts, a.styles.Error.Bold(true).Render("[YOLO]"))
	}

	// CWD
	if dir := a.cwd(); dir != "" {
		home, _ := os.UserHomeDir()
		if home != "" && strings.HasPrefix(dir, home) {
			dir = "~" + dir[len(home):]
		}
		maxW := a.layout.width / 3
		profile := a.renderEnvironment.normalized().profile
		if profile.width(dir) > maxW && maxW > 5 {
			dir = modalTailEllipsize(profile, dir, maxW, 0, "...")
		}
		parts = append(parts, a.styles.Dim.Render(dir))
	}

	return strings.Join(parts, " · ")
}

func (a *App) renderEditor() string {
	multiline := NewMultilineState()
	multiline.Update(a.textarea.Value())

	content := a.textarea.View()
	inner := content

	// Multiline indicator: show line count when input spans multiple lines
	if lineInd := multiline.RenderLineIndicator(a.styles); lineInd != "" {
		inner += " " + lineInd
	}

	// Apply border - width adjusted for border chars. The compatibility
	// rendering boundary restores Lip Gloss v1's border-outside-Width
	// semantics. The border color carries the current mode.
	borderWidth := a.layout.width - 2 // -2 for left+right border chars
	if borderWidth < 20 {
		borderWidth = 20
	}
	editor := contentRenderStyleWidth(
		a.renderEnvironment.normalized().profile,
		a.styles.EditorBorder.BorderForeground(a.composerBorderColor()),
		borderWidth,
		inner,
	)

	return editor
}

func (a *App) composerBorderColor() tuiColor {
	permMode := a.permissionMode()
	switch {
	case a.inputMode == InputShell:
		return a.styles.Warning.GetForeground()
	case permMode == permission.ModePlan:
		return a.styles.AuroraSky.GetForeground()
	case permMode == permission.ModeBypassPermissions:
		return a.styles.Error.GetForeground()
	default:
		return a.styles.AssistantPrefix.GetForeground()
	}
}

func (a *App) renderStatus() string {
	var parts []string
	threadTiming, hasThreadTiming := a.activeThreadTiming()

	// Mode indicator (left side)
	switch {
	case a.externalEditorActive:
		parts = append(parts, a.styles.ToolRunning.Render("●")+" external editor")
	case hasThreadTiming && threadTiming.Status == engine.RuntimeThreadWaitingInput:
		parts = append(parts, a.styles.Warning.Render("●")+" waiting")
	case hasThreadTiming && threadTiming.Status == engine.RuntimeThreadPaused:
		parts = append(parts, a.styles.ToolRunning.Render("⏸")+" paused")
	case (hasThreadTiming && threadTiming.Status == engine.RuntimeThreadRunning) || a.running:
		parts = append(parts, a.styles.ToolSuccess.Render("●")+" running")
	default:
		switch a.permissionMode() {
		case permission.ModePlan:
			parts = append(parts, a.styles.ToolRunning.Render("⏸")+" plan")
		case permission.ModeBypassPermissions:
			parts = append(parts, a.styles.Error.Render("⏵⏵")+" yolo")
		default:
			// Show default mode indicator only when not running
			parts = append(parts, a.styles.Subtle.Render("●")+" default")
		}
	}
	parts = append(parts, a.styles.Subtle.Render(a.activeThreadDisplayLabel()))
	if goal := a.goalStatusProjection(); goal != "" {
		parts = append(parts, a.styles.AssistantPrefix.Render(goal))
	}
	if attention := a.threadAttentionStatus(); attention != "" {
		parts = append(parts, a.styles.Warning.Render(attention))
	}

	// Scroll position indicator (when user has scrolled up)
	if !a.chat.Following() && len(a.chat.items) > 0 {
		scrollInfo := a.chatScrollInfo()
		if scrollInfo != "" {
			parts = append(parts, a.styles.Subtle.Render(scrollInfo))
		}
	}

	// Key hints
	if a.running {
		submitLabel := "queue"
		if !a.isLeaderThreadView() {
			submitLabel = "send"
		}
		parts = append(parts, joinKeyHints(
			keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatSubmit, "enter"), submitLabel),
			keyHint(a.shortcut(keybindings.ContextGlobal, keybindings.ActionAppInterrupt, "ctrl+c"), "interrupt"),
		))
	} else {
		switch a.inputMode {
		case InputCommand:
			parts = append(parts, joinKeyHints(
				keyHint(a.shortcut(keybindings.ContextAutocomplete, keybindings.ActionAutocompletePrev, "up"), "previous"),
				keyHint(a.shortcut(keybindings.ContextAutocomplete, keybindings.ActionAutocompleteNext, "down"), "next"),
				keyHint(a.shortcut(keybindings.ContextAutocomplete, keybindings.ActionAutocompleteAccept, "enter"), "select"),
				keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatCancel, "escape"), "cancel"),
			))
		case InputShell:
			parts = append(parts, joinKeyHints(
				keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatSubmit, "enter"), "run"),
				keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatCancel, "escape"), "cancel"),
			))
		default:
			submit := keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatSubmit, "enter"), "send")
			newline := keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatNewline, "ctrl+j"), "newline")
			mode := keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatCycleMode, "shift+tab"), "mode")
			if strings.Contains(a.textarea.Value(), "\n") {
				parts = append(parts, joinKeyHints(submit, newline, mode))
			} else {
				parts = append(parts, joinKeyHints("/ cmd", "! shell", newline, mode))
			}
		}
	}

	left := "  " + strings.Join(parts, " · ")
	// Show toast notification if active (overrides left hints)
	if toast := a.activeToast(); toast != "" {
		left = "  " + toast
	}
	right := a.styles.AssistantPrefix.Render(assistantIdentityGlyph) + " " + a.model
	// Append CWD
	if dir := a.cwd(); dir != "" {
		home, _ := os.UserHomeDir()
		if home != "" && strings.HasPrefix(dir, home) {
			dir = "~" + dir[len(home):]
		}
		// Truncate long paths
		if len(dir) > 25 {
			dir = "..." + dir[len(dir)-22:]
		}
		right += " · " + a.styles.Dim.Render(dir)
	}
	// Append engine-owned cumulative active time for the selected thread.
	// Human wait, pause, idle, and UI-only work do not advance this clock.
	if hasThreadTiming {
		elapsed := threadTiming.Elapsed(time.Now())
		right += " · " + a.styles.Dim.Render("active "+formatActiveDuration(elapsed))
	} else if elapsed := a.sessionElapsed(); elapsed != "" {
		// Before the engine emits a runtime event, retain the startup/session
		// timer as a compatibility fallback.
		right += " · " + a.styles.Dim.Render(elapsed)
	}
	// Append tool count
	if toolCount := a.connectedToolCount(); toolCount > 0 {
		label := fmt.Sprintf("%d tools", toolCount)
		if toolCount == 1 {
			label = "1 tool"
		}
		right += " · " + a.styles.Subtle.Render(label)
	}
	// Append background task count
	if count := a.backgroundTaskCount(); count > 0 {
		label := fmt.Sprintf("%d tasks", count)
		if count == 1 {
			label = "1 task"
		}
		right += " · " + a.styles.Subtle.Render(label)
	}
	// Append context usage indicator
	if a.engine != nil {
		pct, tokens := a.engine.GetContextUsage()
		if tokens > 0 {
			label := humanTokens(tokens) + fmt.Sprintf(" (%d%%)", pct)
			switch {
			case pct >= 90:
				label = a.styles.Error.Render(label)
			case pct >= 75:
				label = a.styles.Warning.Render(label)
			}
			right += " · " + label
		}
	}
	// Allow external hook to customize the status line
	if a.statusLineHook != nil {
		if l, r := a.statusLineHook(left, right); l != "" || r != "" {
			if l != "" {
				left = l
			}
			if r != "" {
				right = r
			}
		}
	}
	// StatusBar contributes one column of left padding.
	return a.styles.StatusBar.Render(alignStatusLine(a.renderEnvironment.profile, left, right, max(1, a.layout.width-1)))
}

func (a *App) goalStatusProjection() string {
	if a == nil || a.engine == nil {
		return ""
	}
	snapshot := a.engine.RuntimeSnapshot()
	thread, ok := snapshot.Threads[snapshot.ActiveThreadID]
	if !ok || thread.Goal == nil || !thread.Goal.Available {
		return ""
	}
	goal := thread.Goal
	progress := humanGoalTokens(goal.TokensUsed)
	if goal.TokenBudget != nil {
		progress += "/" + humanGoalTokens(*goal.TokenBudget)
	}
	projection := "goal " + goal.Status + " " + progress +
		" active:" + formatActiveDuration(goalActiveDuration(goal.RootActiveTimeMillis))
	if coverage := strings.TrimSpace(goal.UsageCoverage); coverage != "" &&
		coverage != "complete" {
		projection += " coverage:" + coverage
	}
	if objective := boundedGoalStatusText(goal.Objective); objective != "" {
		projection += " [" + objective + "]"
	}
	if reason := boundedGoalStatusText(goal.StatusReason); reason != "" {
		projection += " reason:" + reason
	}
	return projection
}

func goalActiveDuration(millis int64) time.Duration {
	if millis <= 0 {
		return 0
	}
	if millis > math.MaxInt64/int64(time.Millisecond) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(millis) * time.Millisecond
}

func humanGoalTokens(value uint64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func boundedGoalStatusText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxRunes = 32
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

// renderSpinner renders the contextual spinner line between chat and editor.
// Matches the reference SpinnerWithVerb component: shimmer + verb + elapsed time.
func (a *App) renderSpinner() string {
	if !a.running {
		return ""
	}
	verb := a.spinnerState.Text()
	if a.reducedMotion {
		verb = a.spinnerState.StaticText()
	}
	if a.classifierChecking != "" {
		verb = "Auto classifier checking " + a.classifierChecking + "…"
	} else if a.permissionReview != "" {
		verb = "Advisory permission review checking " + a.permissionReview + "…"
	} else if a.hookStatus != "" {
		verb = a.hookStatus
	}
	dur := a.spinnerState.Duration()
	if elapsed, ok := a.activeThreadElapsedAt(time.Now()); ok {
		dur = ""
		if elapsed >= time.Second {
			dur = fmt.Sprintf("%.1fs", elapsed.Seconds())
		}
	}

	// Effort suffix: reference shows "[extended]" for high/max effort
	effortSuffix := a.effortSuffix()

	// Token count suffix: ↑Nk ↓Nk
	tokenSuffix := a.spinnerTokens()

	// Stall detection: early stall recolors to AuroraSky, full stall ramps to
	// the SpinnerStalled token.
	if intensity := a.spinnerState.StallIntensity(); intensity > 0 {
		var verbStyle lipgloss.Style
		if intensity > 0.5 {
			verbStyle = a.styles.SpinnerStalled
		} else {
			verbStyle = a.styles.AuroraSky
		}
		icon := a.spinnerPulseIcon(verbStyle, a.spinnerCount)
		line := icon + " " + verbStyle.Render(verb+" (waiting)")
		if effortSuffix != "" {
			line += " " + a.styles.Subtle.Render(effortSuffix)
		}
		if dur != "" {
			line += "  " + a.styles.Subtle.Render(dur)
		}
		if tokenSuffix != "" {
			line += " · " + a.styles.Subtle.Render(tokenSuffix)
		}
		return "  " + line
	}

	// Reduced motion renders a flat brand verb with no shimmer.
	if a.reducedMotion {
		icon := a.spinnerPulseIcon(a.styles.AssistantPrefix, a.spinnerCount)
		line := icon + " " + a.styles.AssistantPrefix.Render(verb)
		if effortSuffix != "" {
			line += " " + a.styles.Subtle.Render(effortSuffix)
		}
		if dur != "" {
			line += "  " + a.styles.Subtle.Render(dur)
		}
		if tokenSuffix != "" {
			line += " · " + a.styles.Subtle.Render(tokenSuffix)
		}
		return "  " + line
	}

	// Shimmer: per-character glimmer sweep matching reference ShimmerChar.tsx
	// The glimmer highlight sweeps from right to left across the verb text.
	// P19.2.2: the highlight runs the aurora three-stop gradient (brand teal
	// -> AuroraSky -> permission violet) on one 2.4s sine phase; ANSI themes
	// stay on the flat SpinnerShimmer semantic.
	icon := a.spinnerPulseIcon(a.styles.AssistantPrefix, a.spinnerCount)
	verbRendered := renderAuroraShimmerText(verb, a.spinnerCount, a.styles, a.spinnerState.ShimmerPhase())
	line := icon + " " + verbRendered
	if effortSuffix != "" {
		line += " " + a.styles.Subtle.Render(effortSuffix)
	}
	if dur != "" {
		line += "  " + a.styles.Subtle.Render(dur)
	}
	if tokenSuffix != "" {
		line += " · " + a.styles.Subtle.Render(tokenSuffix)
	}

	return "  " + line
}

func permissionReviewDisplayToken(value, fallback string) string {
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

// effortSuffix returns a bracket suffix like "[extended]" for high/max effort.
// Reference: SpinnerWithVerb shows effort level suffix when non-default.
func (a *App) effortSuffix() string {
	if a.engine == nil {
		return ""
	}
	tb := a.engine.GetTokenBudget()
	if tb == nil {
		return ""
	}
	level := tb.GetBudgetLevel()
	switch level {
	case "max":
		return "[extended]"
	case "high":
		return "[high]"
	case "low":
		return "[low]"
	default:
		return "" // medium is default — no suffix
	}
}

// spinnerTokens returns a "↑Nk ↓Nk" suffix showing token usage for the current session.
// Reference: SpinnerWithVerb shows "↑42k ↓1.2k tokens" next to duration.
func (a *App) spinnerTokens() string {
	if a.engine == nil {
		return ""
	}
	input, output := a.engine.GetTotalUsage()
	if input == 0 && output == 0 {
		return ""
	}
	return "↑" + formatCompactTokens(input) + " ↓" + formatCompactTokens(output)
}

// formatCompactTokens formats a token count compactly (e.g. "42k", "1.2M").
func formatCompactTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// backgroundTaskCount returns the number of non-done active tasks (sub-agents).
func (a *App) backgroundTaskCount() int {
	return summarizeTaskExplorer(a.taskExplorerSnapshot()).live
}

// connectedToolCount returns the number of registered tools in the engine.
func (a *App) connectedToolCount() int {
	if a.engine == nil {
		return 0
	}
	return len(a.engine.GetToolNames())
}

func (a *App) activeThreadTiming() (engine.RuntimeThreadTimingSnapshot, bool) {
	if a == nil || a.engine == nil {
		return engine.RuntimeThreadTimingSnapshot{}, false
	}
	threadID := a.activeThreadViewID()
	if threadID == "" {
		threadID = a.leaderThreadViewID()
	}
	return a.engine.RuntimeThreadTiming(threadID)
}

func (a *App) activeThreadElapsedAt(now time.Time) (time.Duration, bool) {
	timing, ok := a.activeThreadTiming()
	if !ok {
		return 0, false
	}
	return timing.Elapsed(now), true
}

func formatActiveDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// sessionElapsed returns a formatted string of the elapsed session time.
func (a *App) sessionElapsed() string {
	if a.sessionStart.IsZero() {
		return ""
	}
	d := time.Since(a.sessionStart)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// chatScrollInfo returns a scroll position description when the user has scrolled up.
// e.g. "viewing 3/12" indicating item 3 of 12 total messages.
func (a *App) chatScrollInfo() string {
	total := len(a.chat.items)
	if total == 0 {
		return ""
	}
	current := a.chat.offsetIdx + 1
	if current > total {
		current = total
	}
	return fmt.Sprintf("scroll %d/%d", current, total)
}

// taskEntry tracks the state of a running sub-agent/task for the task tree.
type taskEntry struct {
	taskID      string
	description string
	activeForm  string
	status      string
	phase       string
	owner       string
	toolUseID   string
	lastTool    string
	summary     string
	toolUses    int
	totalTokens int
	durationMS  int64
	done        bool
	local       bool
}

func (a *App) activeTaskEntriesForView() []*taskEntry {
	if a != nil && a.engine != nil {
		snapshot := a.taskExplorerSnapshot()
		entries := make(
			[]*taskEntry,
			0,
			len(snapshot.WorkItems)+len(snapshot.Executions),
		)
		for _, row := range snapshot.WorkItems {
			if isTerminalTaskPanelStatus(row.Status) {
				continue
			}
			entries = append(entries, &taskEntry{
				taskID: row.WorkItemID,
				description: firstNonEmptyTUIText(
					row.Title,
					row.Description,
					row.WorkItemID,
				),
				activeForm: row.ActiveForm,
				status:     row.Status,
				owner:      row.Owner,
				summary:    row.ResultSummary,
				local:      true,
			})
		}
		for _, row := range snapshot.Executions {
			switch row.Phase {
			case engine.TaskExplorerExecutionRunning,
				engine.TaskExplorerExecutionWaitingInput,
				engine.TaskExplorerExecutionPaused:
			default:
				continue
			}
			entries = append(entries, &taskEntry{
				taskID: fmt.Sprintf(
					"%s@g%d",
					row.Key.AgentID,
					row.Key.Generation,
				),
				description: firstNonEmptyTUIText(
					row.Task,
					row.Description,
					row.Name,
				),
				status:  firstNonEmptyTUIText(row.Status, string(row.Phase)),
				phase:   string(row.Phase),
				summary: row.Activity,
			})
		}
		return entries
	}

	entries := make([]*taskEntry, 0, len(a.activeTasks))
	for _, task := range a.activeTasks {
		if !task.done {
			entries = append(entries, task)
		}
	}
	return entries
}

func firstNonEmptyTUIText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// inlineToolEntry tracks an active tool call for the inline spinner tree.
type inlineToolEntry struct {
	toolCallID  string
	attemptID   string
	name        string
	description string // short description parsed from input (e.g. file path, command)
	status      ToolStatus
	startTime   time.Time
}

// trackToolStart records a new tool call in the inline tool tracker.
func (a *App) trackToolStart(
	toolCallID, name, input, attemptID string,
) {
	if toolCallID == "" {
		return // streaming deltas without ID are not tracked individually
	}
	// Avoid duplicates (streaming may emit tool call multiple times)
	if existing := a.activeTools[toolCallID]; existing != nil {
		if attemptID == "" || existing.attemptID == attemptID {
			return
		}
	}
	entry := &inlineToolEntry{
		toolCallID:  toolCallID,
		attemptID:   attemptID,
		name:        name,
		description: getToolDescription(name, input, ToolRunning),
		status:      ToolRunning,
		startTime:   time.Now(),
	}
	_, exists := a.activeTools[toolCallID]
	a.activeTools[toolCallID] = entry
	if !exists {
		a.activeToolsOrder = append(a.activeToolsOrder, toolCallID)
	}
	// Also track in tool progress display
	a.toolProgress.StartToolAttempt(toolCallID, name, input, attemptID)
	// End streaming when tools start (model output is done)
	if a.streamingCtx.IsStreaming() {
		a.streamingCtx.EndStreaming()
	}
}

// trackToolComplete marks a tool call as finished and removes it from the tree.
func (a *App) trackToolComplete(toolCallID string, status ToolStatus) {
	if toolCallID == "" {
		return
	}
	if entry, ok := a.activeTools[toolCallID]; ok {
		entry.status = status
	}
	// Remove from active tracking after completion
	delete(a.activeTools, toolCallID)
	for i, id := range a.activeToolsOrder {
		if id == toolCallID {
			a.activeToolsOrder = append(a.activeToolsOrder[:i], a.activeToolsOrder[i+1:]...)
			break
		}
	}
	// Also complete in tool progress display
	a.toolProgress.CompleteTool(toolCallID, status)
}

func (a *App) trackToolCompleteAttempt(
	toolCallID string,
	status ToolStatus,
	attemptID string,
) {
	if toolCallID == "" {
		return
	}
	entry, ok := a.activeTools[toolCallID]
	if ok && entry.attemptID == attemptID {
		entry.status = status
		delete(a.activeTools, toolCallID)
		for index, id := range a.activeToolsOrder {
			if id == toolCallID {
				a.activeToolsOrder = append(
					a.activeToolsOrder[:index],
					a.activeToolsOrder[index+1:]...,
				)
				break
			}
		}
	}
	a.toolProgress.CompleteToolAttempt(toolCallID, status, attemptID)
}

// maxInlineTreeItems is the maximum number of items shown in the inline tree
// before overflow is indicated with "+N more".
const maxInlineTreeItems = 5

// renderTaskTree renders the inline task/tool tree below the spinner.
// Shows active tool calls and sub-agent tasks in a compact tree format.
// Reference: TaskListV2 / TeammateSpinnerTree — compact inline tree.
func (a *App) renderTaskTree() string {
	// Collect inline tool entries (in insertion order)
	type treeItem struct {
		icon   string
		desc   string
		suffix string
	}
	var items []treeItem

	// Active tool calls first (in order they were dispatched)
	for _, id := range a.activeToolsOrder {
		entry, ok := a.activeTools[id]
		if !ok {
			continue
		}
		icon := a.spinnerPulseIcon(a.styles.ToolRunning, a.spinnerCount+len(items))
		desc := entry.name
		if entry.description != "" {
			desc = entry.name + " " + a.styles.Subtle.Render(entry.description)
		}
		// Show elapsed time for running tools
		elapsed := time.Since(entry.startTime)
		if elapsed >= time.Second {
			desc += " " + a.styles.Dim.Render(fmt.Sprintf("%ds", int(elapsed.Seconds())))
		}
		items = append(items, treeItem{icon: icon, desc: desc, suffix: a.styles.Subtle.Render(" [running]")})
	}

	// Sub-agent tasks (non-done)
	tasks := a.activeTaskEntriesForView()
	if len(tasks) > 0 {
		if a.engine == nil {
			sortTaskEntries(tasks)
		}
		for i, t := range tasks {
			icon := a.renderTaskEntryIcon(t, len(items)+i)
			desc := t.description
			if t.local && t.status == "in_progress" && t.activeForm != "" {
				desc = t.activeForm
			}
			if desc == "" {
				desc = "Task " + t.taskID[:min(8, len(t.taskID))]
			}
			var suffix string
			if t.local {
				suffix = a.styles.Subtle.Render(" [" + renderTaskEntryStatus(t) + "]")
				if t.owner != "" {
					suffix += a.styles.Subtle.Render(" " + t.owner)
				}
			} else if t.lastTool != "" {
				suffix = a.styles.Subtle.Render(" [" + renderTaskEntryStatus(t) + "] " + t.lastTool)
			} else if t.toolUses > 0 {
				suffix = a.styles.Subtle.Render(fmt.Sprintf(" [%s] (%d tools)", renderTaskEntryStatus(t), t.toolUses))
			} else {
				suffix = a.styles.Subtle.Render(" [" + renderTaskEntryStatus(t) + "]")
			}
			items = append(items, treeItem{icon: icon, desc: desc, suffix: suffix})
		}
	}

	if len(items) == 0 {
		return ""
	}

	// Cap visible items and compute overflow
	overflow := 0
	visible := items
	if len(items) > maxInlineTreeItems {
		visible = items[:maxInlineTreeItems]
		overflow = len(items) - maxInlineTreeItems
	}

	var lines []string
	lastIdx := len(visible) - 1
	if overflow > 0 {
		// All visible items use tree connector since the overflow line will be last
		lastIdx = -1
	}
	for i, item := range visible {
		prefix := "  ├─ "
		if i == lastIdx {
			prefix = "  └─ "
		}
		line := prefix + item.icon + " " + item.desc
		if item.suffix != "" {
			line += item.suffix
		}
		lines = append(lines, line)
	}
	if overflow > 0 {
		overflowLine := fmt.Sprintf("  └─ %s", a.styles.Subtle.Render(fmt.Sprintf("+%d more", overflow)))
		lines = append(lines, overflowLine)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderTaskEntryIcon(t *taskEntry, offset int) string {
	if !t.local {
		return a.spinnerPulseIcon(a.styles.ToolRunning, a.spinnerCount+offset)
	}
	switch t.status {
	case "completed":
		return a.styles.ToolSuccess.Render("✓")
	case "failed":
		return a.styles.ToolError.Render("✗")
	case "killed":
		return a.styles.ToolError.Render("■")
	case "in_progress", "running":
		return a.spinnerPulseIcon(a.styles.ToolRunning, a.spinnerCount+offset)
	case "pending", "":
		return a.styles.Subtle.Render("○")
	default:
		return a.styles.Subtle.Render("○")
	}
}

func renderTaskEntryStatus(t *taskEntry) string {
	if t.status != "" {
		return t.status
	}
	if t.phase != "" {
		return t.phase
	}
	return "pending"
}

func sortTaskEntries(tasks []*taskEntry) {
	// Simple insertion sort — typically < 5 items
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0 && tasks[j].taskID < tasks[j-1].taskID; j-- {
			tasks[j], tasks[j-1] = tasks[j-1], tasks[j]
		}
	}
}

func alignStatusLine(profile DisplayCellProfile, left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	if right == "" {
		return truncateStatusSegment(profile, left, width)
	}
	leftWidth := profile.width(left)
	rightWidth := profile.width(right)
	gap := width - leftWidth - rightWidth
	if gap < 1 {
		return truncateStatusSegment(profile, left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func truncateStatusSegment(profile DisplayCellProfile, value string, width int) string {
	if width <= 0 {
		return ""
	}
	return profile.truncate(value, width)
}

// Message types for tea.Cmd communication
type engineEventMsg struct {
	event   engine.QueryEvent
	queryID uint64
}

type asyncHookEventMsg struct {
	event engine.QueryEvent
	open  bool
}

type engineStartMsg struct {
	events  <-chan engine.QueryEvent
	queryID uint64
}

type eventsDoneMsg struct {
	queryID uint64
}

type permissionRequestMsg struct {
	requestID          string
	threadID           string
	agentID            string
	tool               string
	input              string
	sessionScope       string
	decisionConstraint engine.PermissionDecisionConstraint
	kind               threadAttentionKind
	attempt            int
	responseCh         chan<- PermissionResponse
}

type planApprovalMsg struct {
	requestID    string
	threadID     string
	sessionID    string
	agentID      string
	planApproval *engine.PlanApprovalRequest
	responseCh   chan<- PermissionResponse
}

type askUserQuestionMsg struct {
	requestID  string
	threadID   string
	agentID    string
	input      string
	responseCh chan<- PermissionResponse
}

type threadAttentionCanceledMsg struct {
	requestID string
}

type threadAttentionAnsweredMsg struct {
	requestID string
	response  PermissionResponse
}

type resumeSessionsLoadedMsg struct {
	page       *session.SessionPage
	generation uint64
	reset      bool
	err        error
}

type resumeSessionPageRequestMsg struct {
	query      session.SessionQuery
	generation uint64
	reset      bool
}

// engineBatchMsg carries multiple events drained from the channel in one go.
type engineBatchMsg struct {
	events  []engine.QueryEvent
	queryID uint64
	done    bool
}

func (a *App) syncInputModeFromText() {
	value := a.textarea.Value()
	if a.historySetText != "" && value != a.historySetText {
		// The first edit permanently leaves history-traversal suppression for
		// this recall, even if a later edit recreates the same text.
		a.historySetText = ""
	}
	if strings.TrimSpace(value) == "" {
		// Don't auto-exit shell mode — shell mode is exited explicitly
		// via esc or backspace-when-empty.
		if a.inputMode != InputShell {
			a.inputMode = InputNormal
			a.setEditorPrompt()
		}
		a.commandHints = nil
		a.commandHintIdx = -1
		a.fileHints = nil
		a.fileHintIdx = -1
		a.dismissMentionHints()
		return
	}
	if a.suppressingHistoryHints() {
		// Untouched history recall: keep mode transitions for dispatch, but
		// no autocomplete — the user is traversing history, not typing.
		if strings.HasPrefix(strings.TrimSpace(value), "/") {
			a.inputMode = InputCommand
		} else if a.inputMode == InputCommand {
			a.inputMode = InputNormal
			a.setEditorPrompt()
		}
		a.commandHints = nil
		a.commandHintIdx = -1
		a.fileHints = nil
		a.fileHintIdx = -1
		a.dismissMentionHints()
		a.updateLayout()
		return
	}
	if strings.HasPrefix(strings.TrimSpace(value), "/") {
		a.dismissMentionHints()
		a.inputMode = InputCommand
		a.updateCommandHints()
		return
	}
	if a.inputMode == InputCommand {
		// The text no longer starts with "/": leave command mode so a plain
		// message (including a plain-text history recall) is not dispatched as
		// a slash command.
		a.inputMode = InputNormal
		a.setEditorPrompt()
		a.commandHints = nil
		a.commandHintIdx = -1
		a.fileHints = nil
		a.fileHintIdx = -1
		a.updateLayout()
	}
	a.updateMentionHints()
}

// suppressingHistoryHints reports whether the composer still holds the
// untouched text placed by a history recall. While it does, autocomplete
// (command/file/mention hints) stays closed so up/down keep traversing
// history; any edit diverges the value and re-enables hints automatically.
func (a *App) suppressingHistoryHints() bool {
	return a.historySetText != "" && a.textarea.Value() == a.historySetText
}

func (a *App) updateCommandHints() {
	defer a.updateLayout()
	if a.commandRegistry == nil || a.inputMode != InputCommand {
		a.commandHints = nil
		a.commandHintIdx = -1
		a.fileHints = nil
		a.fileHintIdx = -1
		return
	}
	value := strings.TrimSpace(a.textarea.Value())
	query := strings.TrimPrefix(value, "/")
	fields := strings.Fields(query)

	// If we have a full command name + space → switch to file path hints
	if (len(fields) >= 1 && strings.HasSuffix(value, " ")) || len(fields) >= 2 {
		cmdName := strings.ToLower(fields[0])
		// Check if this is a valid command that accepts file args
		if a.commandRegistry.GetForContext(
			contextPkg.Background(),
			commands.EntrypointTUI,
			a.commandCapabilityContext(),
			cmdName,
		) != nil {
			// Extract the partial path (everything after command name)
			var partial string
			if len(fields) >= 2 {
				partial = fields[len(fields)-1]
			}
			a.commandHints = nil
			a.commandHintIdx = -1
			a.updateFileHints(partial)
			return
		}
	}

	// Standard command name matching
	var cmdQuery string
	if len(fields) > 0 {
		cmdQuery = strings.ToLower(fields[0])
	}
	var hints []*commands.Command
	for _, cmd := range a.commandRegistry.ListForContext(
		contextPkg.Background(),
		commands.EntrypointTUI,
		a.commandCapabilityContext(),
	) {
		if cmdQuery == "" || strings.HasPrefix(cmd.Name, cmdQuery) {
			hints = append(hints, cmd)
		}
	}
	a.commandHints = hints
	a.fileHints = nil
	a.fileHintIdx = -1
	// Clamp selection index
	if len(a.commandHints) == 0 {
		a.commandHintIdx = -1
	} else if a.commandHintIdx >= len(a.commandHints) {
		a.commandHintIdx = len(a.commandHints) - 1
	}
}

func (a *App) acceptCommandHint() {
	cmd := a.commandHints[a.commandHintIdx]
	before := a.captureComposerUndoEntry()
	a.textarea.SetValue("/" + cmd.Name + " ")
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.recordComposerUndo(before)
	a.textarea.CursorEnd()
	a.commandHintIdx = -1
	a.updateCommandHints()
}

// updateFileHints populates file path completions based on the partial path typed.
func (a *App) updateFileHints(partial string) {
	a.fileHints = nil
	a.fileHintIdx = -1

	// Determine directory to list and prefix to match
	dir := "."
	prefix := partial
	if partial != "" {
		if d := filepath.Dir(partial); d != "." && d != "" {
			dir = d
			prefix = filepath.Base(partial)
		}
	}

	// Resolve relative to CWD
	if !filepath.IsAbs(dir) {
		if cwd, err := os.Getwd(); err == nil {
			dir = filepath.Join(cwd, dir)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	lowerPrefix := strings.ToLower(prefix)
	var hints []string
	for _, e := range entries {
		name := e.Name()
		// Skip hidden files unless user typed a dot
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if lowerPrefix == "" || strings.HasPrefix(strings.ToLower(name), lowerPrefix) {
			if e.IsDir() {
				name += "/"
			}
			hints = append(hints, name)
		}
	}
	sort.Strings(hints)

	// Limit to reasonable count
	const maxFileHints = 50
	if len(hints) > maxFileHints {
		hints = hints[:maxFileHints]
	}
	a.fileHints = hints
}

// acceptFileHint fills the selected file path into the textarea.
func (a *App) acceptFileHint() {
	if a.fileHintIdx < 0 || a.fileHintIdx >= len(a.fileHints) {
		return
	}
	selected := a.fileHints[a.fileHintIdx]

	// Reconstruct: replace the partial path with the completed one
	value := a.textarea.Value()
	fields := strings.Fields(value)
	if len(fields) >= 2 {
		// Replace the last field (partial path) with completed path
		partial := fields[len(fields)-1]
		dir := filepath.Dir(partial)
		if dir != "." && dir != "" {
			selected = filepath.Join(dir, selected)
		}
		// Replace last occurrence of partial with selected
		idx := strings.LastIndex(value, partial)
		if idx >= 0 {
			value = value[:idx] + selected
		}
	} else {
		// No partial yet — just append
		value = strings.TrimRight(value, " ") + " " + selected
	}

	// If it's a directory, keep it for further navigation; otherwise add space
	if !strings.HasSuffix(selected, "/") {
		value += " "
	}
	before := a.captureComposerUndoEntry()
	a.textarea.SetValue(value)
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.recordComposerUndo(before)
	a.textarea.CursorEnd()
	a.fileHintIdx = -1
	a.updateCommandHints()
}

func (a *App) renderCommandHints() string {
	if a.inputMode != InputCommand || len(a.commandHints) == 0 || a.focus != FocusEditor {
		return ""
	}

	const maxVisible = 10

	// Compute visible window based on selection
	total := len(a.commandHints)
	winStart := 0
	winEnd := total
	if total > maxVisible {
		// Center the selection in the window
		sel := a.commandHintIdx
		if sel < 0 {
			sel = 0
		}
		winStart = sel - maxVisible/2
		if winStart < 0 {
			winStart = 0
		}
		winEnd = winStart + maxVisible
		if winEnd > total {
			winEnd = total
			winStart = winEnd - maxVisible
		}
	}

	// Find the longest command name in the visible window for alignment
	profile := a.renderEnvironment.normalized().profile
	const nameCol = 20
	maxName := nameCol
	type entry struct {
		label string
		desc  string
		idx   int // index in full commandHints list
	}
	entries := make([]entry, 0, winEnd-winStart)
	for i := winStart; i < winEnd; i++ {
		cmd := a.commandHints[i]
		label := "/" + cmd.Name
		if len(cmd.Aliases) > 0 {
			label += " (" + strings.Join(cmd.Aliases, ", ") + ")"
		}
		if labelWidth := profile.measure(label, 1); labelWidth > maxName {
			maxName = labelWidth
		}
		entries = append(entries, entry{label: label, desc: cmd.Description, idx: i})
	}

	var sb strings.Builder
	maxWidth := a.hintContentWidth()

	// Show scroll indicator at top if not at beginning
	if winStart > 0 {
		sb.WriteString(a.styles.Subtle.Render(fmt.Sprintf(" ↑ %d more", winStart)))
		sb.WriteString("\n")
	}

	for _, e := range entries {
		padding := maxName - profile.measure(e.label, 1) + 2
		if padding < 2 {
			padding = 2
		}
		line := " " + e.label + strings.Repeat(" ", padding) + e.desc
		line = contentEllipsize(profile, line, maxWidth, 2, "...")
		if e.idx == a.commandHintIdx {
			line = a.styles.Selected.Render(line)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Show scroll indicator at bottom if not at end
	if winEnd < total {
		sb.WriteString(a.styles.Subtle.Render(fmt.Sprintf(" ↓ %d more", total-winEnd)))
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// renderFileHints renders the file path completion overlay.
func (a *App) renderFileHints() string {
	if a.inputMode != InputCommand || len(a.fileHints) == 0 || a.focus != FocusEditor {
		return ""
	}

	const maxVisible = 10

	total := len(a.fileHints)
	winStart := 0
	winEnd := total
	if total > maxVisible {
		sel := a.fileHintIdx
		if sel < 0 {
			sel = 0
		}
		winStart = sel - maxVisible/2
		if winStart < 0 {
			winStart = 0
		}
		winEnd = winStart + maxVisible
		if winEnd > total {
			winEnd = total
			winStart = winEnd - maxVisible
		}
	}

	var sb strings.Builder
	profile := a.renderEnvironment.normalized().profile
	maxWidth := a.hintContentWidth()

	if winStart > 0 {
		sb.WriteString(a.styles.Subtle.Render(fmt.Sprintf(" ↑ %d more", winStart)))
		sb.WriteString("\n")
	}

	for i := winStart; i < winEnd; i++ {
		name := a.fileHints[i]
		line := " " + name
		line = contentEllipsize(profile, line, maxWidth, 2, "...")
		if i == a.fileHintIdx {
			line = a.styles.Selected.Render(line)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if winEnd < total {
		sb.WriteString(a.styles.Subtle.Render(fmt.Sprintf(" ↓ %d more", total-winEnd)))
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

func (a *App) hintContentWidth() int {
	width := a.layout.width
	if width <= 0 {
		width = a.width
	}
	return max(width-4, 1)
}

func (a *App) clearInputAfterSubmit(expanded string) {
	a.recordComposerHistory(expanded)
	a.historyIdx = len(a.history)
	a.draft = ""
	a.draftElements = nil
	a.composerUndo = nil
	persisted := strings.TrimSpace(expanded)
	_ = saveHistoryEntry(persisted)
	a.textarea.Reset()
	a.composerElements = nil
	a.setEditorPrompt()
	a.inputMode = InputNormal
	a.commandHints = nil
	a.commandHintIdx = -1
	a.fileHints = nil
	a.fileHintIdx = -1
	a.dismissMentionHints()
	a.updateLayout()
}

func (a *App) beginCommandPaletteSubmission(name string) {
	if a == nil {
		return
	}
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	if name == "" {
		a.commandPaletteSubmission = nil
		return
	}
	a.commandPaletteSubmissionSerial++
	if a.commandPaletteSubmissionSerial == 0 {
		a.commandPaletteSubmissionSerial++
	}
	a.commandPaletteSubmission = &commandPaletteSubmission{
		id:      a.commandPaletteSubmissionSerial,
		command: name,
	}
}

func (a *App) commandPaletteAdmissionMatches(value string) bool {
	if a == nil {
		return false
	}
	pending := a.commandPaletteSubmission
	if pending == nil ||
		pending.queryID != 0 ||
		pending.clipboardRequestID != 0 ||
		!commands.IsCommand(value) {
		return false
	}
	name, _ := commands.ParseCommandInput(value)
	if a.commandRegistry != nil {
		if cmd := a.commandRegistry.Get(name); cmd != nil {
			name = cmd.Name
		}
	}
	return strings.EqualFold(name, pending.command)
}

func (a *App) claimCommandPaletteSubmission(name string) *commandPaletteSubmission {
	if a == nil {
		return nil
	}
	pending := a.commandPaletteSubmission
	a.commandPaletteSubmission = nil
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	if pending == nil ||
		pending.queryID != 0 ||
		pending.clipboardRequestID != 0 ||
		pending.command != name ||
		pending.id != a.commandPaletteSubmissionSerial {
		return nil
	}
	return pending
}

func (a *App) bindCommandPaletteEngineSubmission(
	pending *commandPaletteSubmission,
	queryID uint64,
) {
	if a == nil ||
		pending == nil ||
		queryID == 0 ||
		pending.id != a.commandPaletteSubmissionSerial {
		return
	}
	bound := *pending
	bound.queryID = queryID
	a.commandPaletteSubmission = &bound
}

func (a *App) commitCommandPaletteLocalSubmission(
	pending *commandPaletteSubmission,
) {
	if a == nil ||
		pending == nil ||
		pending.queryID != 0 ||
		pending.clipboardRequestID != 0 ||
		pending.id != a.commandPaletteSubmissionSerial ||
		a.commandPalette == nil {
		return
	}
	a.commandPalette.RecordRecent(pending.command)
}

func (a *App) bindCommandPaletteClipboardSubmission(
	pending *commandPaletteSubmission,
	requestID uint64,
) {
	if a == nil ||
		pending == nil ||
		requestID == 0 ||
		pending.id != a.commandPaletteSubmissionSerial {
		return
	}
	bound := *pending
	bound.clipboardRequestID = requestID
	a.commandPaletteSubmission = &bound
}

func (a *App) settleCommandPaletteClipboardResult(
	result clipboardResultMsg,
	liveResult bool,
) {
	if a == nil || !liveResult {
		return
	}
	pending := a.commandPaletteSubmission
	if pending == nil ||
		pending.clipboardRequestID == 0 ||
		pending.clipboardRequestID != result.requestID ||
		result.caller != ClipboardCallerActionCopy ||
		pending.id != a.commandPaletteSubmissionSerial {
		return
	}
	a.commandPaletteSubmission = nil
	if result.failure != clipboardFailureNone ||
		result.terminal != clipboardTerminalSequenceWritten ||
		result.native != clipboardNativeSucceeded ||
		a.commandPalette == nil ||
		(a.clipboard != nil && a.clipboard.ctx.Err() != nil) {
		return
	}
	a.commandPalette.RecordRecent(pending.command)
}

func (a *App) settleCommandPaletteEngineEvent(
	queryID uint64,
	evt engine.QueryEvent,
) {
	if a == nil {
		return
	}
	pending := a.commandPaletteSubmission
	if pending == nil ||
		pending.queryID == 0 ||
		pending.queryID != queryID ||
		queryID != a.queryID {
		return
	}
	switch evt.Type {
	case engine.EventCommandResult:
		result := evt.CommandResult
		a.commandPaletteSubmission = nil
		if result == nil ||
			result.Status != engine.CommandResultSucceeded ||
			!strings.EqualFold(result.Command, pending.command) ||
			a.commandPalette == nil {
			return
		}
		a.commandPalette.RecordRecent(pending.command)
	case engine.EventUserInterruption, engine.EventTerminal, engine.EventMaxTurnsReached:
		a.commandPaletteSubmission = nil
	}
}

func (a *App) clearCommandPaletteEngineSubmission(queryID uint64) {
	if a == nil {
		return
	}
	pending := a.commandPaletteSubmission
	if pending != nil && pending.queryID == queryID {
		a.commandPaletteSubmission = nil
	}
}

func (a *App) dispatchCommandPaletteLocal(
	value string,
	cmdCtx *commands.CommandContext,
) (*commands.CommandResult, bool) {
	if a.commandRegistry == nil {
		return nil, false
	}
	result, err := a.commandRegistry.Dispatch(
		contextPkg.Background(),
		commands.EntrypointTUI,
		cmdCtx,
		value,
	)
	if err != nil {
		a.chat.AppendSystem(err.Error())
		return nil, false
	}
	if result == nil {
		a.chat.AppendSystem("Command returned no result.")
		return nil, false
	}
	if result.Availability == commands.AvailabilityDisabled ||
		result.Availability == commands.AvailabilityUnavailable {
		if result.Output != "" {
			a.chat.AppendSystem(result.Output)
		}
		return result, false
	}
	return result, true
}

func (a *App) sendSlashCommand(value string) tea.Cmd {
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	commandName, commandArgs := commands.ParseCommandInput(value)
	var registeredCommand *commands.Command
	cmdCtx := a.commandCapabilityContext()
	if a.commandRegistry != nil {
		registeredCommand = a.commandRegistry.Get(commandName)
	}
	canonicalName := commandName
	if registeredCommand != nil {
		canonicalName = registeredCommand.Name
	}
	paletteSubmission := a.claimCommandPaletteSubmission(canonicalName)
	tuiCommandAvailable := a.commandRegistry != nil &&
		a.commandRegistry.GetForContext(
			contextPkg.Background(),
			commands.EntrypointTUI,
			cmdCtx,
			commandName,
		) != nil
	palettePresentationPath := paletteSubmission != nil &&
		registeredCommand != nil &&
		(registeredCommand.ExecutionOwner == commands.ExecutionOwnerEntrypoint ||
			((registeredCommand.Name == "resume" ||
				registeredCommand.Name == "model") &&
				len(commandArgs) == 0))
	var paletteLocalResult *commands.CommandResult
	if palettePresentationPath {
		var ok bool
		paletteLocalResult, ok = a.dispatchCommandPaletteLocal(value, cmdCtx)
		if !ok {
			return nil
		}
	}
	if tuiCommandAvailable &&
		(commandName == "exit" || commandName == "quit") &&
		len(commandArgs) == 0 {
		a.quitting = true
		a.commitCommandPaletteLocalSubmission(paletteSubmission)
		return nil
	}
	if a.state == StateWelcome {
		a.state = StateChat
	}
	display := strings.TrimSpace(a.textarea.Value())
	if display == "" {
		display = value
	}
	a.chat.AppendUserWithComposer(display, a.composerDisplayElements())
	a.clearInputAfterSubmit(value)
	// Runtime thread selection and the read-only Agent monitor are always
	// available, including while the leader query is running.
	// Definition-management subcommands remain serialized.
	if tuiCommandAvailable && commandName == "agent" && len(commandArgs) == 0 {
		a.openAgentThreadPicker()
		a.commitCommandPaletteLocalSubmission(paletteSubmission)
		return nil
	}
	if tuiCommandAvailable &&
		(commandName == "team" || commandName == "teams") &&
		len(commandArgs) == 0 {
		a.enterTeams()
		a.commitCommandPaletteLocalSubmission(paletteSubmission)
		return teamsTickCmd()
	}
	if tuiCommandAvailable && commandName == "keybindings" && len(commandArgs) == 0 {
		a.chat.AppendSystem(a.keybindingSummary())
		a.commitCommandPaletteLocalSubmission(paletteSubmission)
		return nil
	}
	if tuiCommandAvailable && commandName == "queue" {
		a.handleQueueSlashCommand(value)
		a.commitCommandPaletteLocalSubmission(paletteSubmission)
		return nil
	}
	if a.running {
		if a.commandRegistry != nil && registeredCommand != nil {
			result, err := a.commandRegistry.Dispatch(
				contextPkg.Background(),
				commands.EntrypointTUI,
				cmdCtx,
				value,
			)
			if err == nil && result != nil && result.Output != "" {
				a.chat.AppendSystem(result.Output)
				return nil
			}
		}
		a.chat.AppendSystem("Cannot run command while a request is running.")
		return nil
	}
	// Handle /search locally — opens the search overlay
	if tuiCommandAvailable && commandName == "search" {
		a.openSearch()
		a.commitCommandPaletteLocalSubmission(paletteSubmission)
		return nil
	}
	if registeredCommand != nil &&
		registeredCommand.Entrypoints.Supports(commands.EntrypointTUI) &&
		registeredCommand.ExecutionOwner == commands.ExecutionOwnerEngine &&
		(registeredCommand.Availability == commands.AvailabilitySupported ||
			registeredCommand.Availability == commands.AvailabilityHidden) {
		if registeredCommand.Name == "resume" && len(commandArgs) == 0 {
			cmd := a.openResumeSelector()
			if cmd != nil {
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
			}
			return cmd
		}
		if registeredCommand.Name == "model" && len(commandArgs) == 0 {
			a.openModelPicker()
			a.commitCommandPaletteLocalSubmission(paletteSubmission)
			return nil
		}
		cmd := a.startEngineRequest(value)
		if cmd != nil {
			a.bindCommandPaletteEngineSubmission(paletteSubmission, a.queryID)
		}
		return cmd
	}
	// Try local command dispatch first
	if a.commandRegistry != nil {
		result := paletteLocalResult
		var err error
		if result == nil {
			result, err = a.commandRegistry.Dispatch(
				contextPkg.Background(),
				commands.EntrypointTUI,
				cmdCtx,
				value,
			)
		}
		if err == nil && result != nil {
			if result.Action == commands.ActionCopy {
				text, _ := result.Data["text"].(string)
				cmd := a.requestClipboardCopy(ClipboardCallerActionCopy, text)
				if cmd != nil && a.clipboardPending != nil {
					a.bindCommandPaletteClipboardSubmission(
						paletteSubmission,
						a.clipboardPending.id,
					)
				}
				return cmd
			}
			if result.Action == commands.ActionPrompt {
				// Inject as a prompt to the AI
				a.chat.AppendSystem("Analyzing codebase...")
				cmd := a.startEngineRequest(result.Output)
				if cmd != nil {
					a.commitCommandPaletteLocalSubmission(paletteSubmission)
				}
				return cmd
			}
			if result.Action == commands.ActionQuit {
				a.quitting = true
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			if result.Action == commands.ActionSuspend {
				switch {
				case !a.terminalCaps.SuspendResume:
					a.chat.AppendSystem("Suspend/resume is unavailable on this terminal or platform.")
				case a.hasActiveSuspensionWork():
					a.chat.AppendSystem("Cannot suspend while an Agent or task is active.")
				default:
					if result.Output != "" {
						a.chat.AppendSystem(result.Output)
					}
					a.commitCommandPaletteLocalSubmission(paletteSubmission)
					return tea.Suspend
				}
				return nil
			}
			if result.Action == commands.ActionAgentCreate {
				a.openAgentWizard()
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			if result.Action == commands.ActionAgentEdit {
				if name, ok := result.Data["name"].(string); ok && name != "" {
					a.openAgentWizardEdit(name)
					a.commitCommandPaletteLocalSubmission(paletteSubmission)
				}
				return nil
			}
			// Theme: apply new theme to the TUI.
			if result.Action == commands.ActionChangeTheme {
				if theme, ok := result.Data["theme"].(string); ok && theme != "" {
					if err := a.applyTheme(theme); err != nil {
						a.showNotification(err.Error(), NotifyError)
						a.chat.AppendSystem("Theme change failed: " + err.Error())
						return nil
					}
					a.showNotification("Theme changed to: "+theme, NotifySuccess)
				}
				if result.Output != "" {
					a.chat.AppendSystem(result.Output)
				}
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			// Vim toggle: switch input mode.
			if result.Action == commands.ActionToggleVim {
				if a.vimModel.IsEnabled() {
					a.vimModel.Disable()
					a.showNotification("Vim mode disabled", NotifyInfo)
				} else {
					a.vimModel.Enable()
					a.vimModel.SetValue(a.textarea.Value())
					a.vimModel.SetCursor(utf8.RuneCountInString(a.textarea.Value()))
					a.showNotification("Vim mode enabled (Normal mode)", NotifyInfo)
				}
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			// Styled help overlay rendering
			if strings.TrimPrefix(value, "/") == "help" {
				a.openHelpOverlay()
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			// Model picker overlay: /model without args opens the picker
			if strings.TrimPrefix(value, "/") == "model" {
				a.openModelPicker()
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			// MCP settings panel: /mcp without args opens the browser
			if strings.TrimPrefix(value, "/") == "mcp" {
				a.openMCPSettings()
				a.commitCommandPaletteLocalSubmission(paletteSubmission)
				return nil
			}
			if result.Output != "" {
				a.chat.AppendSystem(result.Output)
			}
			a.commitCommandPaletteLocalSubmission(paletteSubmission)
			return nil
		}
		// If command not found locally, fall through to engine
	}

	return a.startEngineRequest(value)
}

func (a *App) sendShellCommand(command string) tea.Cmd {
	if a.state == StateWelcome {
		a.state = StateChat
	}
	display := strings.TrimSpace(a.textarea.Value())
	if display == "" {
		display = command
	}
	a.chat.AppendUser("!" + display)
	a.clearInputAfterSubmit(command)
	toolID := fmt.Sprintf("shell_%d", time.Now().UnixNano())
	input := map[string]any{"command": command}
	inputJSON, _ := jsonPkg.Marshal(input)
	a.chat.AppendToolStart(toolID, "Bash", string(inputJSON))
	return func() tea.Msg {
		bash := tools.BashTool()
		ctx := contextPkg.Background()
		var result string
		var err error
		if bash.ExecuteCtx != nil {
			result, err = bash.ExecuteCtx(ctx, string(inputJSON))
		} else if bash.Execute != nil {
			result, err = bash.Execute(string(inputJSON))
		} else {
			err = fmt.Errorf("bash tool is unavailable")
		}
		if err != nil {
			return shellResultMsg{toolID: toolID, command: command, result: err.Error(), isError: true}
		}
		return shellResultMsg{toolID: toolID, command: command, result: result}
	}
}

func (a *App) startEngineRequest(prompt string) tea.Cmd {
	a.commandPaletteSubmission = nil
	if a.engine == nil {
		a.chat.AppendSystem("No engine configured.")
		return nil
	}
	a.running = true
	a.spinnerCount = 0
	// Reset streaming and progress state for new query
	a.streamingCtx.Reset()
	a.thinkingInd.Stop()
	a.toolProgress.Reset()
	a.spinnerState = SpinnerState{
		Mode:      SpinnerThinking,
		StartTime: time.Now(),
	}
	a.queryID++
	qid := a.queryID
	ctx, cancel := contextPkg.WithCancel(contextPkg.Background())
	a.cancelFn = cancel
	return func() tea.Msg {
		events, _ := a.engine.SubmitMessage(ctx, prompt)
		return engineStartMsg{events: events, queryID: qid}
	}
}

func (a *App) startEngineRuntimeItem(item engine.RuntimeItem) tea.Cmd {
	if a.engine == nil {
		a.chat.AppendSystem("No engine configured.")
		return nil
	}
	a.running = true
	a.spinnerCount = 0
	a.streamingCtx.Reset()
	a.thinkingInd.Stop()
	a.toolProgress.Reset()
	a.spinnerState = SpinnerState{
		Mode:      SpinnerThinking,
		StartTime: time.Now(),
	}
	a.queryID++
	qid := a.queryID
	ctx, cancel := contextPkg.WithCancel(contextPkg.Background())
	a.cancelFn = cancel
	return func() tea.Msg {
		events, _ := a.engine.SubmitRuntimeItem(ctx, item)
		return engineStartMsg{events: events, queryID: qid}
	}
}

func (a *App) startEngineGoalContinuation(item engine.RuntimeItem) tea.Cmd {
	if a.engine == nil {
		a.chat.AppendSystem("No engine configured.")
		return nil
	}
	a.running = true
	a.spinnerCount = 0
	a.streamingCtx.Reset()
	a.thinkingInd.Stop()
	a.toolProgress.Reset()
	a.spinnerState = SpinnerState{
		Mode:      SpinnerThinking,
		StartTime: time.Now(),
	}
	a.queryID++
	qid := a.queryID
	ctx, cancel := contextPkg.WithCancel(contextPkg.Background())
	a.cancelFn = cancel
	return func() tea.Msg {
		events, _ := a.engine.SubmitGoalContinuation(ctx, item)
		return engineStartMsg{events: events, queryID: qid}
	}
}

func (a *App) openResumeSelector() tea.Cmd {
	if a.engine == nil {
		a.chat.AppendSystem("No engine configured.")
		return nil
	}
	a.pushDialog(StateResume)
	a.resume.Show(a.engine.SessionID())
	query, generation := a.resume.beginPage(true)
	return a.loadResumePage(query, generation, true)
}

func (a *App) loadResumePage(query session.SessionQuery, generation uint64, reset bool) tea.Cmd {
	eng := a.engine
	return func() tea.Msg {
		if eng == nil || eng.SessionService() == nil {
			return resumeSessionsLoadedMsg{
				generation: generation,
				reset:      reset,
				err:        fmt.Errorf("session service is unavailable"),
			}
		}
		page, err := eng.SessionService().Query(contextPkg.Background(), query)
		return resumeSessionsLoadedMsg{page: page, generation: generation, reset: reset, err: err}
	}
}

func (a *App) applySessionPickerSelection(selection sessionPickerSelection) tea.Cmd {
	a.invalidateSessionViewSave()
	_ = a.persistSessionViewState()
	a.sessionRestorePending = true
	eng := a.engine
	return func() tea.Msg {
		if eng == nil {
			return resumeSessionActionFinishedMsg{selection: selection, err: fmt.Errorf("no engine configured")}
		}
		ctx := contextPkg.Background()
		if selection.Mode == sessionPickerFork {
			resumed, forked, err := eng.ForkSessionInfo(ctx, selection.Info)
			message := resumeSessionActionFinishedMsg{selection: selection, err: err}
			if resumed != nil {
				message.resumedID = resumed.SessionID
				message.count = len(resumed.Messages)
				message.warnings = append([]string(nil), resumed.Warnings...)
			}
			if forked != nil {
				message.forkedID = forked.NewSessionID
			}
			return message
		}
		var resumed *session.ResumedSession
		var err error
		if selection.ConfirmLegacyStopped {
			resumed, err = eng.SessionService().ImportLegacyAndResumeInfo(
				ctx,
				selection.Info,
				true,
			)
		} else {
			resumed, err = eng.SessionService().ResumeInfo(ctx, selection.Info)
		}
		message := resumeSessionActionFinishedMsg{selection: selection, err: err}
		if resumed != nil {
			message.resumedID = resumed.SessionID
			message.count = len(resumed.Messages)
			message.warnings = append([]string(nil), resumed.Warnings...)
		}
		return message
	}
}

// openHelpOverlay opens the help modal overlay showing keybindings and commands.
func (a *App) openHelpOverlay() {
	a.help.ShowFor(a.commandRegistry, a.commandCapabilityContext())
	a.pushDialog(StateHelp)
}

// openCommandPalette opens the searchable command palette overlay (Ctrl+K).
func (a *App) openCommandPalette() {
	a.commandPalette.ShowFor(a.commandRegistry, a.commandCapabilityContext())
	a.pushDialog(StateCommandPalette)
}

func (a *App) commandCapabilityContext() *commands.CommandContext {
	extra := map[string]any{
		"terminal_capabilities": a.terminalDiagnostics(),
		"terminal_clipboard":    a.terminalCaps.Interactive,
		"interactive_tui":       true,
	}
	if a.engine != nil {
		ctx := a.engine.CommandContext()
		ctx.Extra = extra
		ctx.Environment = commands.CommandEnvironment{Entrypoint: commands.EntrypointTUI, Phase: commandPhase(a.running)}
		ctx.Entrypoint = commands.EntrypointTUI
		return ctx
	}
	return &commands.CommandContext{
		CWD:                a.cwd(),
		Model:              a.model,
		Messages:           a.engineMessages(),
		WorkingDirectories: []string{a.cwd()},
		Extra:              extra,
		Entrypoint:         commands.EntrypointTUI,
		Environment:        commands.CommandEnvironment{Entrypoint: commands.EntrypointTUI, Phase: commandPhase(a.running)},
	}
}

func commandPhase(running bool) commands.CommandPhase {
	if running {
		return commands.CommandPhaseActiveTurn
	}
	return commands.CommandPhaseIdle
}

func (a *App) terminalDiagnostics() string {
	return a.terminalCaps.Summary(a.focusState.Status()) +
		"\n" + a.renderEnvironment.profile.diagnosticSummary()
}

// openMCPSettings opens the MCP servers settings/browser panel.
func (a *App) openMCPSettings() {
	a.mcpSettings.Show()
	a.pushDialog(StateMCPSettings)
}

// openModelPicker opens the model picker overlay.
func (a *App) openModelPicker() {
	if a.engine == nil {
		a.showNotification("No engine configured", NotifyWarning)
		return
	}
	inventory := a.engine.ModelInventory()
	currentModel := a.model
	if currentModel == "" && a.engine != nil {
		currentModel = a.engine.GetModelName()
	}
	a.modelPicker.Show(inventory, currentModel)
	a.pushDialog(StateModelPicker)
}

// applyModelSelection switches the active model after the user picks one from the overlay.
func (a *App) applyModelSelection(modelID string) {
	if a.engine != nil {
		state, err := a.engine.ChangeModel(contextPkg.Background(), modelID)
		if err != nil {
			a.showNotification("Model switch failed: "+err.Error(), NotifyError)
			a.chat.AppendSystem("model change failed: " + err.Error())
			return
		}
		a.model = state.Requested
		message := fmt.Sprintf("Model switched to %s:%s", state.Provider, state.Model)
		if !state.Durable {
			message += " (process-local)"
		}
		a.showNotification(message, NotifySuccess)
		a.chat.AppendSystem(message)
		for _, warning := range state.Warnings {
			if warning == "" {
				continue
			}
			a.showNotification(warning, NotifyWarning)
			a.chat.AppendSystem("model change warning: " + warning)
		}
		return
	}
	a.showNotification("No engine configured", NotifyWarning)
}

// applyTheme applies an explicit runtime choice. EINO_THEME and config only
// participate in startup resolution, so neither may override this choice.
func (a *App) applyTheme(name string) error {
	theme, err := ResolveExplicitTheme(name)
	if err != nil {
		return err
	}
	a.styles = StylesForTheme(theme)
	a.renderEnvironment = a.renderEnvironment.withStyles(a.styles)
	a.propagateStyles()
	return nil
}

// propagateStyles updates every component that captures Styles, including
// inactive thread views and factories for views created after the switch.
func (a *App) propagateStyles() {
	if a.threadViews != nil {
		a.threadViews.SetRenderEnvironment(a.renderEnvironment)
	} else {
		a.chat.SetRenderEnvironment(a.renderEnvironment)
	}
	a.dialog.SetRenderEnvironment(a.renderEnvironment)
	a.resume.SetRenderEnvironment(a.renderEnvironment)
	a.help.SetRenderEnvironment(a.renderEnvironment)
	a.search.SetRenderEnvironment(a.renderEnvironment)
	a.msgSelector.SetRenderEnvironment(a.renderEnvironment)
	a.mcpApproval.SetRenderEnvironment(a.renderEnvironment)
	a.modelPicker.SetRenderEnvironment(a.renderEnvironment)
	a.backgroundTasks.SetRenderEnvironment(a.renderEnvironment)
	a.agentWizard.SetRenderEnvironment(a.renderEnvironment)
	a.mcpSettings.SetRenderEnvironment(a.renderEnvironment)
	a.teamsPanel.SetRenderEnvironment(a.renderEnvironment)
	a.commandPalette.SetRenderEnvironment(a.renderEnvironment)
	a.planDialog.SetRenderEnvironment(a.renderEnvironment)
	a.questionDialog.SetRenderEnvironment(a.renderEnvironment)
	a.agentPicker.SetRenderEnvironment(a.renderEnvironment)
	a.expandSearch.SetRenderEnvironment(a.renderEnvironment)
	a.permQueue.SetStyles(a.styles)
}

func (a *App) projectModalRenderEnvironment() {
	a.dialog.SetRenderEnvironment(a.renderEnvironment)
	a.resume.SetRenderEnvironment(a.renderEnvironment)
	a.mcpApproval.SetRenderEnvironment(a.renderEnvironment)
	a.mcpSettings.SetRenderEnvironment(a.renderEnvironment)
	a.planDialog.SetRenderEnvironment(a.renderEnvironment)
	a.questionDialog.SetRenderEnvironment(a.renderEnvironment)
	a.backgroundTasks.SetRenderEnvironment(a.renderEnvironment)
	a.agentWizard.SetRenderEnvironment(a.renderEnvironment)
	a.teamsPanel.SetRenderEnvironment(a.renderEnvironment)
	a.taskExplorer.SetRenderEnvironment(a.renderEnvironment)
	a.search.SetRenderEnvironment(a.renderEnvironment)
	a.modelPicker.SetRenderEnvironment(a.renderEnvironment)
	a.commandPalette.SetRenderEnvironment(a.renderEnvironment)
	a.agentPicker.SetRenderEnvironment(a.renderEnvironment)
	a.expandSearch.SetRenderEnvironment(a.renderEnvironment)
	a.help.SetRenderEnvironment(a.renderEnvironment)
	a.msgSelector.SetRenderEnvironment(a.renderEnvironment)
}

// openAgentWizard opens the agent creation wizard.
func (a *App) openAgentWizard() {
	a.agentWizard.Show()
	a.pushDialog(StateAgentWizard)
}

// openAgentWizardEdit opens the agent wizard pre-filled with an existing definition.
func (a *App) openAgentWizardEdit(name string) {
	defs, _ := engine.LoadAgentDefinitions(a.cwd())
	def, ok := defs[name]
	if !ok {
		// Case-insensitive lookup
		for k, v := range defs {
			if strings.EqualFold(k, name) {
				def = v
				ok = true
				break
			}
		}
	}
	if !ok {
		a.showNotification("Agent not found: "+name, NotifyWarning)
		return
	}
	tools := strings.Join(def.Tools, ", ")
	a.agentWizard.ShowEdit(def.Name, def.WhenToUse, def.Model, def.SystemPrompt, tools)
	a.pushDialog(StateAgentWizard)
}

// saveAgentFromWizard persists the wizard result to disk.
func (a *App) saveAgentFromWizard(result *AgentWizardResult) {
	filePath, err := SaveAgentDefinition(result)
	if err != nil {
		a.showNotification("Error: "+err.Error(), NotifyError)
		return
	}
	mode := "Created"
	if a.agentWizard.mode == WizardModeEdit {
		mode = "Updated"
	}
	a.showNotification(fmt.Sprintf("%s agent %q (saved to %s)", mode, result.Name, filepath.Base(filePath)), NotifySuccess)
	a.chat.AppendSystem(fmt.Sprintf("%s agent definition: %s", mode, result.Name))
}

// openSearch opens the search overlay for finding text in conversation history.
func (a *App) openSearch() {
	a.search.Show()
	a.state = StateSearch
	// Run initial search with empty query (no results)
	a.search.UpdateMatches(a.chat.Items())
}

// handleSearchKey processes key events when the search overlay is active.
func (a *App) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	prevQuery := a.search.Query()
	scrollTo, dismissed, cmd := a.search.HandleKey(msg)

	if dismissed {
		a.state = StateChat
		return cmd
	}

	// If query changed, re-run search
	if a.search.Query() != prevQuery {
		a.search.UpdateMatches(a.chat.Items())
		// Auto-scroll to first match
		if m := a.search.CurrentMatch(); m != nil {
			a.chat.ScrollToItem(m.ItemIndex)
		}
	}

	// If navigation produced a scroll target, scroll to it
	if scrollTo >= 0 {
		a.chat.ScrollToItem(scrollTo)
	}

	return cmd
}

// openMessageSelector enters message selection mode for rewriting a prior message.
func (a *App) openMessageSelector() {
	if a.running {
		a.showNotification("Cannot rewrite while a request is running", NotifyWarning)
		return
	}
	items := a.chat.Items()
	// Check if there are any user messages to select
	hasUserMsg := false
	for _, item := range items {
		if _, ok := item.(*UserMessage); ok {
			hasUserMsg = true
			break
		}
	}
	if !hasUserMsg {
		a.showNotification("No messages to rewrite", NotifyWarning)
		return
	}
	a.msgSelector.Show(items)
	a.state = StateMessageSelect
	// Scroll to the selected message
	if idx := a.msgSelector.SelectedItemIndex(); idx >= 0 {
		a.chat.ScrollToItem(idx)
	}
}

// handleMessageSelectKey processes key events when in message selection mode.
func (a *App) handleMessageSelectKey(msg tea.KeyPressMsg) tea.Cmd {
	resolution := a.keybindResolver.ResolveEvent(msg, keybindings.ContextMessageSelector)
	if resolution.Kind == keybindings.ResolutionChordStarted || resolution.Kind == keybindings.ResolutionChordCancelled {
		return nil
	}
	if resolution.Kind != keybindings.ResolutionMatch {
		return nil
	}

	selected, dismissed := false, false
	switch resolution.Action {
	case keybindings.ActionMessageSelectorUp:
		if a.msgSelector.selectedPos > 0 {
			a.msgSelector.selectedPos--
		}
	case keybindings.ActionMessageSelectorDown:
		if a.msgSelector.selectedPos < len(a.msgSelector.userIndices)-1 {
			a.msgSelector.selectedPos++
		}
	case keybindings.ActionMessageSelectorTop:
		a.msgSelector.selectedPos = 0
	case keybindings.ActionMessageSelectorBottom:
		if len(a.msgSelector.userIndices) > 0 {
			a.msgSelector.selectedPos = len(a.msgSelector.userIndices) - 1
		}
	case keybindings.ActionMessageSelectorSelect:
		selected = len(a.msgSelector.userIndices) > 0
	case keybindings.ActionMessageSelectorCancel:
		a.msgSelector.Close()
		dismissed = true
	default:
		if handled, cmd := a.handleKeyAction(resolution.Action, msg); handled {
			return cmd
		}
	}

	if dismissed {
		a.state = StateChat
		a.chat.ResetFollow()
		return nil
	}

	if selected {
		// Get the selected message content
		content, elements := a.msgSelector.selectedComposer(a.chat.Items())
		selectedIdx := a.msgSelector.SelectedItemIndex()
		a.msgSelector.Close()
		a.state = StateChat

		if content != "" {
			// Truncate engine messages to the point before the selected message.
			// We need to find the corresponding engine message index.
			a.truncateAndLoadForRewrite(content, elements, selectedIdx)
		}
		return nil
	}

	// Navigation: scroll to keep selected item visible
	if idx := a.msgSelector.SelectedItemIndex(); idx >= 0 {
		a.chat.ScrollToItem(idx)
	}

	return nil
}

// truncateAndLoadForRewrite truncates the conversation at the given chat item
// index and loads the selected message text into the editor for rewriting.
func (a *App) truncateAndLoadForRewrite(
	content string,
	elements []threadComposerElement,
	chatItemIdx int,
) {
	for _, element := range elements {
		if element.Kind != composerElementKindImage ||
			element.Label == "" ||
			!strings.Contains(content, element.Label) {
			continue
		}
		replacement := element.Label + " (image content not restored)"
		if !strings.Contains(content, replacement) {
			content = strings.Replace(content, element.Label, replacement, 1)
		}
	}
	// Find the engine message index corresponding to this chat item.
	// We count user messages in the engine's message list to find the match.
	if a.engine != nil {
		msgs := a.engine.GetMessages()
		// Count user messages in chat items up to and including chatItemIdx
		userMsgOrdinal := 0
		for i, item := range a.chat.Items() {
			if _, ok := item.(*UserMessage); ok {
				userMsgOrdinal++
				if i == chatItemIdx {
					break
				}
			}
		}

		// Find the Nth user message in engine messages
		engineIdx := -1
		count := 0
		for i, msg := range msgs {
			if msg.Role == schema.User && (msg.Extra == nil || msg.Extra["is_meta"] != true) {
				count++
				if count == userMsgOrdinal {
					engineIdx = i
					break
				}
			}
		}

		// Truncate engine messages to just before this message
		if engineIdx >= 0 {
			if err := a.engine.TruncateToMessage(engineIdx); err == nil {
				// Also truncate the chat view to match
				a.truncateChatView(chatItemIdx)
			}
		}
	}

	// Load the message content into the editor
	a.textarea.SetValue(content)
	a.composerElements = nil
	a.markComposerChanged()
	a.gcDraftMedia()
	a.textarea.CursorEnd()
	a.chat.ResetFollow()
}

// truncateChatView removes all chat items from the given index onward.
func (a *App) truncateChatView(fromIdx int) {
	items := a.chat.Items()
	if fromIdx < 0 || fromIdx >= len(items) {
		return
	}
	a.chat.TruncateFrom(fromIdx)
}

func (a *App) reloadChatFromEngine() {
	if a.engine == nil {
		return
	}
	a.chat.Reset()
	a.chat.withHydrationIntent(func() {
		for _, msg := range a.engine.GetMessages() {
			if msg == nil {
				continue
			}
			switch msg.Role {
			case schema.User:
				if msg.Extra != nil && msg.Extra["is_meta"] == true {
					a.chat.AppendSystem(msg.Content)
				} else if msg.Content != "" {
					a.chat.AppendUser(msg.Content)
				}
			case schema.Assistant:
				if msg.ReasoningContent != "" {
					a.chat.StreamThinkingDelta(msg.ReasoningContent)
					a.chat.FinishThinking()
					a.thinkingInd.Stop()
				}
				if msg.Content != "" {
					a.chat.AppendOrUpdateAssistant(msg.Content)
					a.chat.FinishAssistant()
				}
				for _, tc := range msg.ToolCalls {
					a.chat.AppendOrUpdateTool(tc.ID, tc.Function.Name, tc.Function.Arguments)
				}
			case schema.Tool:
				isError := msg.Extra != nil && msg.Extra["is_error"] == true
				if isError {
					a.chat.UpdateToolError(msg.ToolCallID, msg.ToolName, msg.Content)
				} else {
					a.chat.UpdateToolResult(msg.ToolCallID, msg.ToolName, msg.Content)
				}
			case schema.System:
				if msg.Content != "" {
					a.chat.AppendSystem(msg.Content)
				}
			}
		}
	})
	a.syncAgentToolTraces()
	a.chat.ResetFollow()
}

func (a *App) togglePlanMode() {
	switch a.permissionMode() {
	case permission.ModePlan:
		// Entering bypass is a user execution-control action, distinct from the
		// model's typed ExitPlanMode flow, and always requires the risk dialog.
		a.showBypassConfirm()
		return
	case permission.ModeBypassPermissions:
		if err := a.setPermissionMode(permission.ModeDefault); err != nil {
			a.chat.AppendSystem("mode change failed: " + err.Error())
			return
		}
	default:
		if err := a.setPermissionMode(permission.ModePlan); err != nil {
			a.chat.AppendSystem("mode change failed: " + err.Error())
			return
		}
	}
	switch a.permissionMode() {
	case permission.ModePlan:
		a.chat.AppendSystem("plan mode on")
	case permission.ModeBypassPermissions:
		a.chat.AppendSystem("accept all tools on — bypassing permissions")
	default:
		a.chat.AppendSystem("default mode")
	}
}

func (a *App) permissionMode() permission.Mode {
	if a.engine != nil {
		return a.engine.PermissionMode()
	}
	return a.permMode
}

func (a *App) setPermissionMode(mode permission.Mode) error {
	if a.engine != nil {
		if err := a.engine.SetPermissionModeConfirmed(mode, false); err != nil {
			return err
		}
	}
	a.permMode = mode
	return nil
}

func (a *App) setConfirmedBypassMode() error {
	if a.engine != nil {
		if err := a.engine.SetPermissionModeConfirmed(
			permission.ModeBypassPermissions,
			true,
		); err != nil {
			return err
		}
	}
	a.permMode = permission.ModeBypassPermissions
	return nil
}

func (a *App) cwd() string {
	if a.engine != nil {
		if dir := a.engine.GetCWD(); dir != "" {
			return dir
		}
	}
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "."
}

// --- Expand View ---

func (a *App) enterExpandView() {
	content := a.chat.RenderAllExpanded(a.width)
	if strings.TrimSpace(content) == "" {
		return // nothing to expand
	}
	a.enterExpandContent(content, 0)
	a.expandConversation = true
	a.expandRaw = false
}

func (a *App) enterExpandContent(content string, returnDialog AppState) {
	a.expandContent = content
	a.expandLines = strings.Split(a.expandContent, "\n")
	a.expandOffset = 0
	a.expandSearch.Close()
	a.expandReturnDialog = returnDialog
	a.expandConversation = false
	a.expandRaw = false
	a.state = StateExpand
}

func (a *App) leaveExpandView() {
	returnDialog := a.expandReturnDialog
	a.expandReturnDialog = 0
	a.expandConversation = false
	a.expandRaw = false
	a.state = StateChat
	if returnDialog == StateResume {
		a.resume.visible = true
		a.pushDialog(StateResume)
	}
}

func (a *App) handleExpandKey(msg tea.KeyPressMsg) tea.Cmd {
	// If expand search is active, delegate to it first
	if a.expandSearch.Visible() {
		return a.handleExpandSearchKey(msg)
	}

	maxOffset := len(a.expandLines) - a.height + 2 // +2 for status bar
	if maxOffset < 0 {
		maxOffset = 0
	}
	resolution := a.keybindResolver.ResolveEvent(msg, keybindings.ContextTranscript, keybindings.ContextScroll)
	if resolution.Kind == keybindings.ResolutionChordStarted || resolution.Kind == keybindings.ResolutionChordCancelled {
		return nil
	}
	if resolution.Kind != keybindings.ResolutionMatch {
		return nil
	}
	switch resolution.Action {
	case keybindings.ActionTranscriptToggleRaw:
		a.toggleExpandRawProjection()
		return nil
	case keybindings.ActionTranscriptSearch:
		a.expandSearch.Show()
		a.expandSearch.UpdateMatches(a.expandLines)
		return nil
	case keybindings.ActionTranscriptExit:
		a.leaveExpandView()
		return nil
	case keybindings.ActionScrollLineUp:
		a.expandOffset--
		if a.expandOffset < 0 {
			a.expandOffset = 0
		}
		return nil
	case keybindings.ActionScrollLineDown:
		a.expandOffset++
		if a.expandOffset > maxOffset {
			a.expandOffset = maxOffset
		}
		return nil
	case keybindings.ActionScrollPageUp, keybindings.ActionScrollHalfUp:
		a.expandOffset -= a.height - 2
		if a.expandOffset < 0 {
			a.expandOffset = 0
		}
		return nil
	case keybindings.ActionScrollPageDown, keybindings.ActionScrollHalfDown:
		a.expandOffset += a.height - 2
		if a.expandOffset > maxOffset {
			a.expandOffset = maxOffset
		}
		return nil
	case keybindings.ActionScrollTop:
		a.expandOffset = 0
		return nil
	case keybindings.ActionScrollBottom:
		a.expandOffset = maxOffset
		return nil
	default:
		if handled, cmd := a.handleKeyAction(resolution.Action, msg); handled {
			return cmd
		}
	}
	return nil
}

func (a *App) toggleExpandRawProjection() {
	if !a.expandConversation {
		return
	}
	a.expandRaw = !a.expandRaw
	if a.expandRaw {
		a.expandContent = a.chat.RenderAllRaw(a.width)
	} else {
		a.expandContent = a.chat.RenderAllExpanded(a.width)
	}
	a.expandLines = strings.Split(a.expandContent, "\n")
	maxOffset := max(0, len(a.expandLines)-max(1, a.height-2))
	a.expandOffset = min(a.expandOffset, maxOffset)
	if a.expandSearch.Visible() {
		a.expandSearch.UpdateMatches(a.expandLines)
	}
}

// handleExpandSearchKey processes key events when the search bar within the
// expand view is active.
func (a *App) handleExpandSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	prevQuery := a.expandSearch.Query()
	scrollToLine, dismissed, cmd := a.expandSearch.HandleKey(msg)

	if dismissed {
		// Esc closes search bar, not the expand view
		return cmd
	}

	// If query changed, re-run search
	if a.expandSearch.Query() != prevQuery {
		a.expandSearch.UpdateMatches(a.expandLines)
		// Auto-scroll to first match
		if m := a.expandSearch.CurrentMatch(); m != nil {
			a.scrollExpandToLine(m.LineIndex)
		}
	}

	// If navigation produced a scroll target, scroll to it
	if scrollToLine >= 0 {
		a.scrollExpandToLine(scrollToLine)
	}

	return cmd
}

// scrollExpandToLine scrolls the expand view so that the given line is visible,
// preferably centered in the viewport.
func (a *App) scrollExpandToLine(lineIdx int) {
	viewHeight := a.height - 2 // account for status bar and search bar
	if viewHeight < 1 {
		viewHeight = 1
	}
	maxOffset := len(a.expandLines) - viewHeight
	if maxOffset < 0 {
		maxOffset = 0
	}

	// Center the target line in the viewport
	target := lineIdx - viewHeight/2
	if target < 0 {
		target = 0
	}
	if target > maxOffset {
		target = maxOffset
	}
	a.expandOffset = target
}

func (a *App) renderExpandView() string {
	// Reserve space for status bar (1 line) and search bar (1 line if visible)
	reservedLines := 1 // status bar
	searchBar := ""
	if a.expandSearch.Visible() {
		searchBar = a.expandSearch.Render(a.width)
		reservedLines++ // search bar takes 1 line
	}

	viewHeight := a.height - reservedLines
	if viewHeight < 1 {
		viewHeight = 1
	}

	// Slice visible lines
	end := a.expandOffset + viewHeight
	if end > len(a.expandLines) {
		end = len(a.expandLines)
	}
	start := a.expandOffset
	if start > len(a.expandLines) {
		start = len(a.expandLines)
	}

	visible := a.expandLines[start:end]

	// Apply search highlighting to visible lines
	highlighted := make([]string, viewHeight)
	for i, line := range visible {
		if a.expandSearch.Visible() && a.expandSearch.Query() != "" {
			line = a.expandSearch.HighlightLine(line, start+i)
		}
		highlighted[i] = contentProjectLine(
			a.renderEnvironment.profile,
			line,
			a.width,
			0,
		)
	}

	content := strings.Join(highlighted, "\n")

	// Status bar
	total := len(a.expandLines)
	pos := ""
	if total > viewHeight {
		maxOff := total - viewHeight
		if maxOff > 0 {
			pct := (a.expandOffset * 100) / maxOff
			pos = fmt.Sprintf(" %d%% ", pct)
		}
	}
	projection := "EXPANDED"
	projectionHint := ""
	if a.expandConversation {
		projectionHint = " • R raw/expanded"
		if a.expandRaw {
			projection = "RAW"
		}
	}
	statusHint := "  [" + projection + "] ↑/↓ scroll • PgUp/PgDn page • Ctrl+F search" + projectionHint + " • Esc exit"
	if a.expandSearch.Visible() {
		statusHint = "  ↑/↓ scroll • PgUp/PgDn page • Esc close search"
	}
	status := contentProjectLine(
		a.renderEnvironment.profile,
		a.styles.Subtle.Render(statusHint+pos),
		a.width,
		0,
	)

	// Compose: search bar (if visible) + content + status bar
	var parts []string
	if searchBar != "" {
		parts = append(parts, searchBar)
	}
	parts = append(parts, content)
	parts = append(parts, status)

	return strings.Join(parts, "\n")
}

// --- Task Panel ---

func (a *App) enterBackgroundTasks() tea.Cmd {
	if key, ok := a.chat.LatestAgentTraceTarget(); ok {
		found, pageCmd := a.backgroundTasks.ShowExecution(key)
		if found {
			a.backgroundTasks.detailTab = a.threadDetailTab
			a.backgroundTasks.rebuildAgentDetailLines()
			a.pushDialog(StateBackgroundTasks)
			return pageCmd
		}
	}
	a.backgroundTasks.Show()
	a.pushDialog(StateBackgroundTasks)
	return nil
}

func (a *App) openAgentDetail(key engine.RuntimeExecutionKey) tea.Cmd {
	found, pageCmd := a.backgroundTasks.ShowExecution(key)
	if found {
		a.backgroundTasks.detailTab = a.threadDetailTab
		a.backgroundTasks.rebuildAgentDetailLines()
		a.pushDialog(StateBackgroundTasks)
		return pageCmd
	}
	a.showToast("Agent detail is no longer available")
	return nil
}

func (a *App) syncAgentToolTraces() bool {
	if a == nil || a.agentTraceProvider == nil || a.chat == nil {
		return false
	}
	explorer := a.taskExplorerSnapshot()
	active := false
	for _, snapshot := range a.agentTraceProvider() {
		trace := agentToolTraceFromSnapshot(snapshot)
		if key, observed, resolved := a.chat.agentTraceIdentity(
			snapshot.ParentToolUseID,
			snapshot.AgentID,
			snapshot.ParentToolUseID,
		); observed {
			trace.ExecutionKey = key
			trace.IdentityObserved = true
			trace.IdentityResolved = resolved
		} else {
			trace = resolveAgentToolTraceIdentity(trace, explorer)
		}
		a.chat.UpdateAgentToolTrace(snapshot.ParentToolUseID, trace)
		active = active || trace.active()
	}
	return active
}

func (a *App) ensureSpinnerTick() tea.Cmd {
	if a == nil || a.spinnerTickScheduled {
		return nil
	}
	a.spinnerTickScheduled = true
	if a.reducedMotion {
		return spinnerTickAfter(500 * time.Millisecond)
	}
	return spinnerTick()
}

func (a *App) enterTeams() {
	a.teamsPanel.Show()
	a.pushDialog(StateTeams)
}

func (a *App) enterTaskPanel() tea.Cmd {
	a.taskExplorer.Show(taskExplorerMixed, false)
	a.state = StateTaskPanel
	return taskExplorerTickCmd()
}

func (a *App) buildTaskPanelLines() []string {
	lines := make([]string, 0)
	closeKey := a.shortcut(keybindings.ContextTask, keybindings.ActionTaskClose, "ctrl+t")
	closeHint := ""
	if closeKey != "" {
		closeHint = " " + a.styles.Subtle.Render("("+closeKey+" to close)")
	}
	lines = append(lines, a.styles.Bold.Render("  Task Panel")+closeHint)
	lines = append(lines, "")

	if a.taskExplorerSnapshotSource == nil {
		return append(
			lines,
			a.styles.Subtle.Render("  Task Explorer unavailable"),
		)
	}
	snapshot := a.taskExplorerSnapshotSource()
	if !snapshot.Available {
		return append(
			lines,
			a.styles.Subtle.Render(
				"  Task Explorer unavailable: "+
					snapshot.UnavailableReason,
			),
		)
	}
	if len(snapshot.WorkItems) == 0 &&
		len(snapshot.Executions) == 0 {
		lines = append(lines, a.styles.Subtle.Render("  No tasks"))
		return lines
	}
	for _, item := range snapshot.WorkItems {
		title := firstNonEmptyTUIText(
			item.ActiveForm,
			item.Title,
			item.Description,
			item.WorkItemID,
		)
		lines = append(lines, a.truncateTaskPanelText(
			fmt.Sprintf(
				"  [work %s] %s: %s",
				item.WorkItemID,
				item.Status,
				title,
			),
			a.taskPanelContentWidth(),
		))
	}
	for _, execution := range snapshot.Executions {
		lines = append(lines, a.truncateTaskPanelText(
			fmt.Sprintf(
				"  [agent %s/%d] %s: %s",
				execution.Key.AgentID,
				execution.Key.Generation,
				firstNonEmptyTUIText(
					execution.Status,
					string(execution.Phase),
				),
				firstNonEmptyTUIText(
					execution.Activity,
					execution.Task,
					execution.Description,
				),
			),
			a.taskPanelContentWidth(),
		))
	}
	return lines
}

func isActiveTaskPanelStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "running", "in_progress", "paused":
		return true
	default:
		return false
	}
}

func (a *App) hasActiveSuspensionWork() bool {
	if a == nil {
		return false
	}
	if a.running {
		return true
	}
	if a.taskExplorerSnapshotSource == nil {
		return false
	}
	snapshot := a.taskExplorerSnapshotSource()
	for _, item := range snapshot.WorkItems {
		if isActiveTaskPanelStatus(item.Status) {
			return true
		}
	}
	for _, execution := range snapshot.Executions {
		if isActiveTaskPanelStatus(execution.Status) {
			return true
		}
	}
	return false
}

func isTerminalTaskPanelStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "aborted", "killed":
		return true
	default:
		return false
	}
}

func (a *App) taskPanelContentWidth() int {
	width := a.layout.width
	if width <= 0 {
		width = a.width
	}
	if width <= 0 {
		return 20
	}
	return width
}

func (a *App) truncateTaskPanelText(text string, maxWidth int) string {
	return modalEllipsize(a.renderEnvironment.profile, text, maxWidth, 0, "...")
}

func (a *App) handleTaskPanelKey(msg tea.KeyPressMsg) tea.Cmd {
	if a.taskExplorer != nil && a.taskExplorer.provider != nil {
		dismissed, detailCommand := a.taskExplorer.HandleKeyWithCmd(
			msg,
			a.height,
		)
		if dismissed {
			a.state = StateChat
			return nil
		}
		if intent, ok := a.taskExplorer.takeSwitchTarget(); ok {
			cmd, err := a.activateTaskExplorerNavigationTarget(intent)
			if err != nil {
				a.showNotification(err.Error(), NotifyError)
				return nil
			}
			a.state = StateChat
			return cmd
		}
		return detailCommand
	}
	maxOffset := len(a.taskPanelLines) - a.height + 2
	if maxOffset < 0 {
		maxOffset = 0
	}
	resolution := a.keybindResolver.ResolveEvent(msg, keybindings.ContextTask, keybindings.ContextScroll)
	if resolution.Kind == keybindings.ResolutionChordStarted || resolution.Kind == keybindings.ResolutionChordCancelled {
		return nil
	}
	if resolution.Kind != keybindings.ResolutionMatch {
		return nil
	}
	switch resolution.Action {
	case keybindings.ActionTaskClose:
		a.state = StateChat
		return nil
	case keybindings.ActionScrollLineUp:
		a.taskPanelOffset--
		if a.taskPanelOffset < 0 {
			a.taskPanelOffset = 0
		}
		return nil
	case keybindings.ActionScrollLineDown:
		a.taskPanelOffset++
		if a.taskPanelOffset > maxOffset {
			a.taskPanelOffset = maxOffset
		}
		return nil
	case keybindings.ActionScrollPageUp, keybindings.ActionScrollHalfUp:
		a.taskPanelOffset -= a.height - 2
		if a.taskPanelOffset < 0 {
			a.taskPanelOffset = 0
		}
		return nil
	case keybindings.ActionScrollPageDown, keybindings.ActionScrollHalfDown:
		a.taskPanelOffset += a.height - 2
		if a.taskPanelOffset > maxOffset {
			a.taskPanelOffset = maxOffset
		}
		return nil
	case keybindings.ActionScrollTop:
		a.taskPanelOffset = 0
		return nil
	case keybindings.ActionScrollBottom:
		a.taskPanelOffset = maxOffset
		return nil
	case keybindings.ActionTaskRefresh:
		// Refresh task panel content
		a.taskPanelLines = a.buildTaskPanelLines()
		return nil
	default:
		if handled, cmd := a.handleKeyAction(resolution.Action, msg); handled {
			return cmd
		}
	}
	return nil
}

func (a *App) renderTaskPanel() string {
	if a.taskExplorer != nil && a.taskExplorer.provider != nil {
		return a.taskExplorer.Render(
			a.layout.overlayRect.Width,
			a.layout.overlayRect.Height,
		)
	}
	// Re-build lines on each render to reflect live progress
	a.taskPanelLines = a.buildTaskPanelLines()

	frameWidth := a.layout.overlayRect.Width
	if frameWidth <= 0 {
		frameWidth = a.width
	}
	frameHeight := a.layout.overlayRect.Height
	if frameHeight <= 0 {
		frameHeight = a.height
	}
	viewHeight := frameHeight - 1
	if viewHeight < 1 {
		viewHeight = 1
	}

	end := a.taskPanelOffset + viewHeight
	if end > len(a.taskPanelLines) {
		end = len(a.taskPanelLines)
	}
	start := a.taskPanelOffset
	if start > len(a.taskPanelLines) {
		start = len(a.taskPanelLines)
	}

	visible := a.taskPanelLines[start:end]
	padded := make([]string, viewHeight)
	copy(padded, visible)

	content := strings.Join(padded, "\n")

	// Status bar
	total := len(a.taskPanelLines)
	pos := ""
	if total > viewHeight {
		pct := (a.taskPanelOffset * 100) / (total - viewHeight)
		pos = fmt.Sprintf(" %d%% ", pct)
	}
	closeKey := a.shortcut(keybindings.ContextTask, keybindings.ActionTaskClose, "ctrl+t")
	status := a.styles.Subtle.Render("  " + joinKeyHints(
		keyHint(a.shortcut(keybindings.ContextTask, keybindings.ActionScrollLineUp, "up"), "up"),
		keyHint(a.shortcut(keybindings.ContextTask, keybindings.ActionScrollLineDown, "down"), "down"),
		keyHint(a.shortcut(keybindings.ContextTask, keybindings.ActionTaskRefresh, "r"), "refresh"),
		keyHint(closeKey, "close"),
	) + pos)

	frame, _ := modalTopFrame(
		a.renderEnvironment.profile,
		strings.Split(content+"\n"+status, "\n"),
		frameWidth,
		frameHeight,
	)
	return frame
}

func (a *App) engineMessages() []*schema.Message {
	if a.engine != nil {
		return a.engine.GetMessages()
	}
	return nil
}

type shellResultMsg struct {
	toolID  string
	command string
	result  string
	isError bool
}

// goodbyeMessages matches the reference exit flow.
var goodbyeMessages = []string{"Goodbye!", "See ya!", "Bye!", "Catch you later!"}

func (a *App) renderGoodbye() string {
	msg := goodbyeMessages[time.Now().UnixNano()%int64(len(goodbyeMessages))]
	var sb strings.Builder
	sb.WriteString(msg)
	sb.WriteByte('\n')
	sid := ""
	if a.engine != nil {
		sid = a.engine.SessionID()
	}
	if sid != "" {
		hint := fmt.Sprintf("\nResume this session with:\n  %s resume %s\n", identity.CommandName, sid)
		sb.WriteString(a.styles.Dim.Render(hint))
	}
	return sb.String()
}

type notificationExpiryTickMsg struct {
	generation uint64
}

func defaultNotificationAfter(
	delay time.Duration,
	message func(time.Time) tea.Msg,
) tea.Cmd {
	return tea.Tick(delay, message)
}

// showToast records Info feedback while Update owns the current transition.
func (a *App) showToast(msg string) {
	a.showNotification(msg, NotifyInfo)
}

// showNotification records feedback while Update owns the current transition.
func (a *App) showNotification(msg string, severity NotificationSeverity) {
	a.notifications.PushAt(a.notificationNow(), msg, severity)
}

// activeToast returns the current status-line notification without mutation.
func (a *App) activeToast() string {
	return a.notifications.RenderSingleLineWithEnvironment(
		a.renderEnvironment,
		a.width-10,
	)
}

func (a *App) acceptNotificationExpiryTick(generation uint64) bool {
	if !a.notificationExpiryScheduled ||
		generation != a.notificationExpiryGeneration {
		return false
	}
	a.notificationExpiryScheduled = false
	a.notificationScheduledDeadline = time.Time{}
	return true
}

func (a *App) reconcileNotificationExpiry() tea.Cmd {
	if a.quitting {
		if a.notificationExpiryScheduled {
			a.notificationExpiryGeneration++
			a.notificationExpiryScheduled = false
			a.notificationScheduledDeadline = time.Time{}
		}
		return nil
	}
	deadline, ok := a.notifications.EarliestDeadline()
	if !ok {
		if a.notificationExpiryScheduled {
			a.notificationExpiryGeneration++
			a.notificationExpiryScheduled = false
			a.notificationScheduledDeadline = time.Time{}
		}
		return nil
	}
	if a.notificationExpiryScheduled &&
		!deadline.Before(a.notificationScheduledDeadline) {
		return nil
	}

	a.notificationExpiryGeneration++
	generation := a.notificationExpiryGeneration
	a.notificationExpiryScheduled = true
	a.notificationScheduledDeadline = deadline
	delay := deadline.Sub(a.notificationNow())
	if delay < 0 {
		delay = 0
	}
	return a.notificationAfter(delay, func(time.Time) tea.Msg {
		return notificationExpiryTickMsg{generation: generation}
	})
}

func batchNotificationCmd(current, notification tea.Cmd) tea.Cmd {
	switch {
	case current == nil:
		return notification
	case notification == nil:
		return current
	default:
		return tea.Batch(current, notification)
	}
}

// pillClickHits reports whether a chat-relative left-click lands inside the
// published shared pill geometry.
func (a *App) pillClickHits(chatX, chatY int) bool {
	if a == nil || a.chat == nil {
		return false
	}
	projection := a.chat.currentViewportProjection()
	if projection == nil {
		return false
	}
	geometry := projection.pill
	if geometry.action != chatPillActionFollow {
		return false
	}
	return geometry.hits(chatX, chatY)
}
