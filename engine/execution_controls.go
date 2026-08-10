package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/commands"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/transcript"
)

// CommandContext returns the live, detached command capability snapshot used
// by dispatch and entrypoint discovery.
func (e *QueryEngine) CommandContext() *commands.CommandContext {
	if e == nil {
		return &commands.CommandContext{}
	}
	return &commands.CommandContext{
		SessionID:          e.SessionID(),
		CWD:                e.GetCWD(),
		Model:              e.GetModelName(),
		Messages:           e.GetMessages(),
		WorkingDirectories: e.GetWorkingDirectories(),
		Engine:             e,
	}
}

func (e *QueryEngine) addWorkingDirectoryForCommandTurn(
	ctx context.Context,
	rawPath string,
	turnID string,
) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("workspace root validation canceled: %w", err)
	}
	canonical, err := canonicalWorkingDirectory(rawPath, e.GetCWD())
	if err != nil {
		return "", false, err
	}
	if err := e.lockExecutionControlMutation(strings.TrimSpace(turnID)); err != nil {
		return "", false, err
	}
	defer e.planMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	roots := make([]string, 0, 1+len(e.config.AdditionalDirs))
	roots = append(roots, e.config.CWD)
	roots = append(roots, e.config.AdditionalDirs...)
	resolution := permission.ResolvePermissionPath(canonical, "")
	if permission.PermissionPathsWithinRoots(resolution, roots) {
		return canonical, false, nil
	}
	e.config.AdditionalDirs = append(e.config.AdditionalDirs, canonical)
	return canonical, true, nil
}

func canonicalWorkingDirectory(rawPath, cwd string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("working directory path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	} else if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("user-specific home expansion is unsupported: %s", path)
	}

	resolution := permission.ResolvePermissionPath(path, cwd)
	if resolution.Unsafe || resolution.Logical == "" {
		return "", fmt.Errorf("working directory path is unsafe: %s", path)
	}
	canonical, err := filepath.EvalSymlinks(resolution.Logical)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %s: %w", resolution.Logical, err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize working directory %s: %w", canonical, err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect working directory %s: %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory is not a directory: %s", canonical)
	}
	canonicalResolution := permission.ResolvePermissionPath(canonical, "")
	if !permission.PermissionPathsWithinRoots(canonicalResolution, []string{canonical}) {
		return "", fmt.Errorf("working directory is outside the canonical permission root: %s", canonical)
	}
	return canonical, nil
}

// ModelResolver is the provider-owned, side-effect-free model resolution
// boundary used by execution controls. provider.Runtime satisfies it.
type ModelResolver interface {
	ResolveModel(modelSpec string) (provider.ResolvedConfig, error)
}

// ModelResolverFunc adapts deterministic fixtures and embedded runtimes to the
// provider resolution boundary without constructing a second router.
type ModelResolverFunc func(modelSpec string) (provider.ResolvedConfig, error)

func (f ModelResolverFunc) ResolveModel(modelSpec string) (provider.ResolvedConfig, error) {
	return f(modelSpec)
}

// ModelControlState is the effective provider/model capability returned after
// validation or mutation.
type ModelControlState struct {
	Requested               string
	Provider                provider.Provider
	Model                   string
	SupportsReasoningEffort bool
	ReasoningEffort         string
	Durable                 bool
	Warnings                []string
}

func (e *QueryEngine) resolveModelControl(
	ctx context.Context,
	modelSpec string,
) (ModelControlState, error) {
	if e == nil {
		return ModelControlState{}, fmt.Errorf("query engine is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ModelControlState{}, fmt.Errorf("model resolution canceled: %w", err)
	}
	requested := strings.TrimSpace(modelSpec)
	if requested == "" {
		requested = e.GetModelName()
	}
	if requested == "" {
		return ModelControlState{}, fmt.Errorf("model is required")
	}
	resolver := e.config.ModelResolver
	if resolver == nil {
		return ModelControlState{}, fmt.Errorf("provider model inventory is unavailable")
	}
	resolved, err := resolver.ResolveModel(requested)
	if err != nil {
		return ModelControlState{}, fmt.Errorf("resolve model %q: %w", requested, err)
	}
	if strings.TrimSpace(string(resolved.Provider)) == "" ||
		strings.TrimSpace(resolved.Model) == "" {
		return ModelControlState{}, fmt.Errorf(
			"provider model inventory returned an incomplete route for %q",
			requested,
		)
	}
	if err := ctx.Err(); err != nil {
		return ModelControlState{}, fmt.Errorf("model resolution canceled: %w", err)
	}
	capabilities := modelcaps.GetCapabilities(resolved.Model)
	return ModelControlState{
		Requested: requested,
		Provider:  resolved.Provider,
		Model:     resolved.Model,
		SupportsReasoningEffort: resolved.Provider == provider.ProviderAgenticClaude &&
			capabilities.SupportsThinking,
		ReasoningEffort: e.ReasoningEffort(),
	}, nil
}

// ChangeModel validates the provider route before one model mutation. The
// requested route spec is retained so provider-qualified custom models and
// configured aliases keep their routing identity.
func (e *QueryEngine) ChangeModel(
	ctx context.Context,
	modelSpec string,
) (ModelControlState, error) {
	return e.changeModel(ctx, modelSpec, "")
}

// SetModel preserves the pre-P16.5a source-compatible setter while reusing the
// validated execution-control boundary. Validation or active-turn failures are
// fail-closed because the legacy signature cannot return them.
//
// Deprecated: use ChangeModel and handle its returned state and error.
func (e *QueryEngine) SetModel(modelSpec string) {
	_, _ = e.ChangeModel(context.Background(), modelSpec)
}

func (e *QueryEngine) changeModelForCommandTurn(
	ctx context.Context,
	modelSpec string,
	turnID string,
) (ModelControlState, error) {
	return e.changeModel(ctx, modelSpec, strings.TrimSpace(turnID))
}

func (e *QueryEngine) changeModel(
	ctx context.Context,
	modelSpec string,
	ownerTurnID string,
) (ModelControlState, error) {
	e.mu.Lock()
	startGeneration := e.promptRouteGeneration
	currentReasoning := e.reasoningEffort
	currentBlock := cloneModelDispatchBlock(e.modelDispatchBlock)
	e.mu.Unlock()
	if currentBlock != nil &&
		currentBlock.Code == ModelDispatchBlockCheckpointUncertain {
		return ModelControlState{}, fmt.Errorf(
			"%s: reload the Session before changing the model",
			currentBlock.Code,
		)
	}
	state, binding, err := e.resolveModelBindingCandidate(
		ctx,
		modelSpec,
		currentReasoning,
	)
	if err != nil &&
		currentReasoning != "" &&
		errors.Is(err, errModelReasoningUnsupported) {
		state, binding, err = e.resolveModelBindingCandidate(
			ctx,
			modelSpec,
			"",
		)
		if err == nil {
			state.Warnings = append(
				state.Warnings,
				"model_reasoning_cleared: target route does not support the previous effort",
			)
		}
	}
	if err != nil {
		return ModelControlState{}, err
	}
	if err := e.lockExecutionControlMutation(ownerTurnID); err != nil {
		return ModelControlState{}, err
	}
	e.mu.Lock()
	if e.promptRouteGeneration != startGeneration ||
		e.reasoningEffort != currentReasoning {
		e.mu.Unlock()
		e.planMu.Unlock()
		return ModelControlState{}, fmt.Errorf(
			"model or reasoning changed while the candidate binding was being applied",
		)
	}
	e.mu.Unlock()
	persistErr := e.persistModelControlCheckpointLocked(
		binding,
		state.Model,
		string(state.Provider),
	)
	if persistErr != nil {
		if transcript.IsDurabilityUncertain(persistErr) {
			e.mu.Lock()
			e.modelDispatchBlock = newModelDispatchBlock(
				ModelDispatchBlockCheckpointUncertain,
				"",
				"reload the Session before any provider call",
				false,
			)
			e.mu.Unlock()
		}
		e.planMu.Unlock()
		return ModelControlState{}, fmt.Errorf(
			"persist model binding checkpoint: %w",
			persistErr,
		)
	}
	e.mu.Lock()
	e.promptRouteGeneration++
	e.config.Model = state.Requested
	e.modelBinding = binding.Clone()
	e.modelDispatchBlock = nil
	e.deprecationWarning = modelcaps.CheckDeprecation(state.Model)
	e.reasoningEffort = state.ReasoningEffort
	if e.subagentExecutor != nil {
		e.subagentExecutor.ParentModelName = state.Requested
	}
	state.ReasoningEffort = e.reasoningEffort
	state.Durable = e.transcript != nil && e.transcript.Path() != ""
	e.mu.Unlock()
	e.planMu.Unlock()
	return state, nil
}

// ReasoningEffortCapability returns the exact runtime capability used by
// command discovery and mutation admission.
func (e *QueryEngine) ReasoningEffortCapability(
	ctx context.Context,
) (bool, string, error) {
	state, _, err := e.resolveModelBindingCandidate(
		ctx,
		e.GetModelName(),
		e.ReasoningEffort(),
	)
	if err != nil {
		return false, "", err
	}
	if !state.SupportsReasoningEffort {
		return false, fmt.Sprintf(
			"%s model %q does not expose compatible reasoning effort",
			state.Provider,
			state.Model,
		), nil
	}
	return true, "", nil
}

// ReasoningEffort returns the effective provider reasoning effort. Empty means
// provider default.
func (e *QueryEngine) ReasoningEffort() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reasoningEffort
}

// ChangeReasoningEffort validates the current provider/model capability before
// changing the value supplied to subsequent model requests.
func (e *QueryEngine) ChangeReasoningEffort(
	ctx context.Context,
	level string,
) (string, error) {
	return e.changeReasoningEffort(ctx, level, "")
}

func (e *QueryEngine) changeReasoningEffortForCommandTurn(
	ctx context.Context,
	level string,
	turnID string,
) (string, error) {
	return e.changeReasoningEffort(ctx, level, strings.TrimSpace(turnID))
}

func (e *QueryEngine) changeReasoningEffort(
	ctx context.Context,
	level string,
	ownerTurnID string,
) (string, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "default", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return "", fmt.Errorf("unsupported reasoning effort %q", level)
	}
	requested := level
	if requested == "default" {
		requested = ""
	}
	e.mu.Lock()
	modelSpec := e.config.Model
	startGeneration := e.promptRouteGeneration
	block := cloneModelDispatchBlock(e.modelDispatchBlock)
	e.mu.Unlock()
	if block != nil && !block.ContextOnly {
		return "", fmt.Errorf(
			"%s: select a model explicitly before changing reasoning effort",
			block.Code,
		)
	}
	state, binding, err := e.resolveModelBindingCandidate(
		ctx,
		modelSpec,
		requested,
	)
	if err != nil {
		return "", err
	}
	if requested != "" && !state.SupportsReasoningEffort {
		return "", fmt.Errorf(
			"%s model %q does not support compatible reasoning effort",
			state.Provider,
			state.Model,
		)
	}
	if requested != "" && state.ReasoningEffort != requested {
		return "", fmt.Errorf(
			"%s model %q does not support reasoning effort %q",
			state.Provider,
			state.Model,
			requested,
		)
	}
	if err := e.lockExecutionControlMutation(ownerTurnID); err != nil {
		return "", err
	}
	e.mu.Lock()
	if e.promptRouteGeneration != startGeneration ||
		e.config.Model != modelSpec {
		e.mu.Unlock()
		e.planMu.Unlock()
		return "", fmt.Errorf("model changed while reasoning effort was being applied")
	}
	e.mu.Unlock()
	persistErr := e.persistModelControlCheckpointLocked(
		binding,
		state.Model,
		string(state.Provider),
	)
	if persistErr != nil {
		if transcript.IsDurabilityUncertain(persistErr) {
			e.mu.Lock()
			e.modelDispatchBlock = newModelDispatchBlock(
				ModelDispatchBlockCheckpointUncertain,
				"",
				"reload the Session before any provider call",
				false,
			)
			e.mu.Unlock()
		}
		e.planMu.Unlock()
		return "", fmt.Errorf(
			"persist reasoning effort checkpoint: %w",
			persistErr,
		)
	}
	e.mu.Lock()
	e.reasoningEffort = state.ReasoningEffort
	if binding != nil {
		e.modelBinding = binding.Clone()
	}
	e.mu.Unlock()
	e.planMu.Unlock()
	if requested == "" {
		return "default", nil
	}
	return requested, nil
}

// lockExecutionControlMutation serializes external execution controls with
// turn admission. A command may mutate only from the exact turn that owns the
// boundary; ACP/TUI direct controls must wait until no turn is active.
func (e *QueryEngine) lockExecutionControlMutation(ownerTurnID string) error {
	if e == nil {
		return fmt.Errorf("query engine is nil")
	}
	e.planMu.Lock()
	activeTurnID := strings.TrimSpace(e.planActiveTurnID)
	if activeTurnID == "" ||
		(ownerTurnID != "" && ownerTurnID == activeTurnID) {
		return nil
	}
	e.planMu.Unlock()
	return fmt.Errorf(
		"execution control cannot change while turn %s owns the runtime boundary",
		activeTurnID,
	)
}

// ResolveModelControl exposes side-effect-free provider/model resolution to
// entrypoint projections such as ACP configuration options.
func (e *QueryEngine) ResolveModelControl(
	ctx context.Context,
	modelSpec string,
) (ModelControlState, error) {
	return e.resolveModelControl(ctx, modelSpec)
}

// SetPermissionModeConfirmed is the external execution-control adapter. Bypass
// mode is fail-closed unless the caller carries an explicit confirmation from
// its own interaction contract.
func (e *QueryEngine) SetPermissionModeConfirmed(
	mode permission.Mode,
	bypassConfirmed bool,
) error {
	if !userSelectablePermissionMode(mode) {
		return fmt.Errorf("unsupported permission mode %q", mode)
	}
	if mode == permission.ModeBypassPermissions && !bypassConfirmed {
		return fmt.Errorf("bypassPermissions requires explicit risk confirmation")
	}
	source := planTransitionExternal
	if mode == permission.ModeBypassPermissions && bypassConfirmed {
		source = planTransitionUserConfirmed
	}
	return e.setPermissionMode(mode, source)
}

func userSelectablePermissionMode(mode permission.Mode) bool {
	switch mode {
	case permission.ModeDefault,
		permission.ModePlan,
		permission.ModeAcceptEdits,
		permission.ModeBypassPermissions,
		permission.ModeDontAsk,
		permission.ModeAuto:
		return true
	default:
		return false
	}
}
