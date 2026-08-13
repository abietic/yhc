package appserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/abietic/yhc/engine"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
)

const (
	maxExecutionSettingsModels   = 128
	maxExecutionSettingsSelector = 512
)

var errExecutionSettingsActiveTurn = errors.New("execution settings cannot change during an active turn")

var desktopPermissionModes = []permission.Mode{
	permission.ModeDefault,
	permission.ModePlan,
	permission.ModeAcceptEdits,
	permission.ModeDontAsk,
	permission.ModeAuto,
}

// sessionExecutionController is deliberately optional: the app-server cannot
// require every SessionEngine adapter to own these execution controls.
type sessionExecutionController interface {
	ModelInventory() provider.RuntimeInventorySnapshot
	GetModelName() string
	ReasoningEffort() string
	ReasoningEffortCapability(context.Context) (bool, string, error)
	ChangeModel(context.Context, string) (engine.ModelControlState, error)
	ChangeReasoningEffort(context.Context, string) (string, error)
	PermissionMode() permission.Mode
	SetPermissionModeConfirmed(permission.Mode, bool) error
	ModelDispatchBlockSnapshot() *engine.ModelDispatchBlock
}

func (input UpdateExecutionSettingsRequest) selectedField() (string, string, error) {
	count := 0
	field, value := "", ""
	for _, candidate := range []struct {
		name  string
		value *string
	}{
		{"model", input.Model},
		{"reasoning_effort", input.ReasoningEffort},
		{"permission_mode", input.PermissionMode},
	} {
		if candidate.value == nil {
			continue
		}
		count++
		field, value = candidate.name, strings.TrimSpace(*candidate.value)
	}
	if count != 1 {
		return "", "", fmt.Errorf("exactly one execution setting is required")
	}
	return field, value, nil
}

func (s *session) executionController() (sessionExecutionController, bool) {
	controller, ok := s.engine.(sessionExecutionController)
	return controller, ok
}

func (s *session) executionSettings(ctx context.Context) (ExecutionSettingsResponse, error) {
	controller, ok := s.executionController()
	if !ok {
		return ExecutionSettingsResponse{}, errExecutionSettingsUnavailable
	}
	model := strings.TrimSpace(controller.GetModelName())
	effort := strings.TrimSpace(controller.ReasoningEffort())
	if effort == "" {
		effort = "default"
	}
	supported, _, capabilityErr := controller.ReasoningEffortCapability(ctx)
	if capabilityErr != nil {
		supported = false
	}
	response := ExecutionSettingsResponse{
		Model:                    model,
		Models:                   executionModelOptions(controller.ModelInventory(), model),
		ReasoningEffort:          effort,
		ReasoningEffortSupported: supported,
		ReasoningEffortOptions:   []string{"default"},
		PermissionMode:           string(controller.PermissionMode()),
		PermissionModeOptions:    executionPermissionModeOptions(),
		DispatchBlock:            executionDispatchBlock(controller.ModelDispatchBlockSnapshot()),
	}
	if supported {
		response.ReasoningEffortOptions = executionReasoningOptions(
			controller.ModelInventory(), model,
		)
	}
	return response, nil
}

func executionModelOptions(
	inventory provider.RuntimeInventorySnapshot,
	current string,
) []ExecutionModelOption {
	entries := inventory.Entries
	currentIndex := -1
	for index, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Selector), current) {
			currentIndex = index
			break
		}
	}
	if len(entries) > maxExecutionSettingsModels && currentIndex >= maxExecutionSettingsModels {
		entries = append(append([]provider.RuntimeInventoryEntry(nil), entries[:maxExecutionSettingsModels-1]...), entries[currentIndex])
	} else if len(entries) > maxExecutionSettingsModels {
		entries = entries[:maxExecutionSettingsModels]
	}
	options := make([]ExecutionModelOption, 0, len(entries))
	for _, entry := range entries {
		displayName := strings.TrimSpace(entry.DisplayName)
		if displayName == "" {
			displayName = strings.TrimSpace(entry.Provider) + ":" + strings.TrimSpace(entry.APIModel)
		}
		options = append(options, ExecutionModelOption{
			Selector: strings.TrimSpace(entry.Selector), DisplayName: displayName,
			Provider: strings.TrimSpace(entry.Provider), APIModel: strings.TrimSpace(entry.APIModel),
		})
	}
	return options
}

func executionReasoningOptions(inventory provider.RuntimeInventorySnapshot, current string) []string {
	options := []string{"default"}
	seen := map[string]struct{}{"default": {}}
	for _, entry := range inventory.Entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Selector), current) {
			continue
		}
		efforts := entry.Metadata.SupportedReasoningEfforts.Value
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(entry.Selector)), "legacy:") &&
			strings.EqualFold(strings.TrimSpace(entry.Provider), "agenticclaude") &&
			modelcaps.GetCapabilities(entry.APIModel).SupportsThinking {
			efforts = []string{"low", "medium", "high", "xhigh", "max"}
		}
		for _, raw := range efforts {
			effort := strings.ToLower(strings.TrimSpace(raw))
			if effort == "" || !modelcaps.SupportsAdapterReasoningEffort(entry.Provider, effort) {
				continue
			}
			if _, exists := seen[effort]; exists {
				continue
			}
			seen[effort] = struct{}{}
			options = append(options, effort)
		}
		break
	}
	return options
}

func executionPermissionModeOptions() []string {
	options := make([]string, 0, len(desktopPermissionModes))
	for _, mode := range desktopPermissionModes {
		options = append(options, string(mode))
	}
	return options
}

func executionDispatchBlock(block *engine.ModelDispatchBlock) *ExecutionDispatchBlock {
	if block == nil {
		return nil
	}
	return &ExecutionDispatchBlock{Code: block.Code, Selector: block.Selector, Remediation: block.Remediation, ContextOnly: block.ContextOnly}
}

func (s *session) updateExecutionSettings(
	ctx context.Context,
	input UpdateExecutionSettingsRequest,
) (ExecutionSettingsResponse, error) {
	controller, ok := s.executionController()
	if !ok {
		return ExecutionSettingsResponse{}, errExecutionSettingsUnavailable
	}
	field, value, err := input.selectedField()
	if err != nil {
		return ExecutionSettingsResponse{}, err
	}
	if s.summary().ActiveTurnID != "" {
		return ExecutionSettingsResponse{}, errExecutionSettingsActiveTurn
	}
	switch field {
	case "model":
		if value == "" || len(value) > maxExecutionSettingsSelector {
			return ExecutionSettingsResponse{}, fmt.Errorf("model selector is invalid")
		}
		if !containsExecutionModelOption(
			executionModelOptions(controller.ModelInventory(), controller.GetModelName()), value,
		) {
			return ExecutionSettingsResponse{}, fmt.Errorf("model selector is unsupported")
		}
		_, err = controller.ChangeModel(ctx, value)
	case "reasoning_effort":
		settings, settingsErr := s.executionSettings(ctx)
		if settingsErr != nil {
			return ExecutionSettingsResponse{}, settingsErr
		}
		if !containsExecutionOption(settings.ReasoningEffortOptions, value) {
			return ExecutionSettingsResponse{}, fmt.Errorf("reasoning effort is unsupported")
		}
		_, err = controller.ChangeReasoningEffort(ctx, value)
	case "permission_mode":
		mode := permission.Mode(value)
		if !containsExecutionOption(executionPermissionModeOptions(), value) {
			return ExecutionSettingsResponse{}, fmt.Errorf("permission mode is unsupported")
		}
		err = controller.SetPermissionModeConfirmed(mode, false)
	}
	if err != nil {
		if isExecutionSettingsRace(err) {
			return ExecutionSettingsResponse{}, fmt.Errorf("%w: %w", errExecutionSettingsActiveTurn, err)
		}
		return ExecutionSettingsResponse{}, fmt.Errorf("execution setting rejected")
	}
	return s.executionSettings(ctx)
}

func containsExecutionOption(options []string, candidate string) bool {
	for _, option := range options {
		if option == candidate {
			return true
		}
	}
	return false
}

func containsExecutionModelOption(options []ExecutionModelOption, candidate string) bool {
	for _, option := range options {
		if option.Selector == candidate {
			return true
		}
	}
	return false
}

func isExecutionSettingsRace(err error) bool {
	if errors.Is(err, engine.ErrPlanTransitionInFlight) {
		return true
	}
	// Model and reasoning controls expose no typed/sentinel external-control
	// race; match only their stable, exact admission prefix.
	return strings.HasPrefix(
		err.Error(), "execution control cannot change while turn ",
	)
}

var errExecutionSettingsUnavailable = errors.New("execution settings are unavailable")

func (s *Server) handleGetExecutionSettings(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	settings, err := owned.executionSettings(r.Context())
	if errors.Is(err, errExecutionSettingsUnavailable) {
		writeError(w, http.StatusNotImplemented, "execution_settings_unavailable", "execution settings are unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "execution_settings_rejected", "execution settings could not be read")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateExecutionSettings(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	var input UpdateExecutionSettingsRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	settings, err := owned.updateExecutionSettings(r.Context(), input)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, settings)
	case errors.Is(err, errExecutionSettingsUnavailable):
		writeError(w, http.StatusNotImplemented, "execution_settings_unavailable", "execution settings are unavailable")
	case errors.Is(err, errExecutionSettingsActiveTurn):
		writeError(w, http.StatusConflict, "execution_settings_rejected", "execution settings cannot change during an active turn")
	case strings.Contains(err.Error(), "exactly one execution setting"):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusUnprocessableEntity, "execution_settings_rejected", "execution setting was rejected")
	}
}
