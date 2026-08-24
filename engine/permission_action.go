package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

// PermissionActionDescriptor is the QueryEngine-owned, host-local identity of
// one validated registered action. Raw input stays local and must never be
// projected directly to a model reviewer.
type PermissionActionDescriptor struct {
	RequestedToolName string
	CanonicalToolName string
	Input             map[string]any
	CanonicalInput    string

	Registered           bool
	Enabled              bool
	Selected             bool
	CapabilityGeneration uint64
	Origin               tools.ToolOrigin
	ActionKind           tools.ToolActionKind
	CapabilitiesDeclared bool

	ReadOnly                 bool
	Write                    bool
	Destructive              bool
	InternalStateDefaultSafe bool
	Network                  bool
	Child                    bool
	Dynamic                  bool
	RequiresUserInteraction  bool
	ShellComplete            bool
	SchemaValidated          bool
	CustomValidationComplete bool

	Path             permission.PathResolution
	PathWithinRoots  bool
	WorkingRoots     []string
	CWD              string
	RootSessionID    string
	SessionID        string
	ThreadID         string
	AgentID          string
	Entrypoint       string
	Mode             permission.Mode
	PlanActive       bool
	PlanPhase        PlanPhase
	PlanRevision     uint64
	PlanFileIdentity string
	PolicySnapshotID string

	ExecutionPolicyDigest         string
	ExecutionBindingDigest        string
	ExecutionProfile              containment.Profile
	ExecutionState                containment.State
	ExecutionAdapter              containment.AdapterFamily
	ExecutionNetwork              containment.NetworkMode
	CredentialMode                containment.CredentialMode
	ExecutionAvailability         containment.BindingAvailability
	ExecutionReasonCode           containment.ReasonCode
	AdapterAxes                   containment.EnforcementAxes
	RuntimeAxes                   containment.EnforcementAxes
	EnforcementAxes               containment.EnforcementAxes
	ExecutionCapabilityGeneration string
	GuestProcess                  bool
	WorkingDirIdentity            containment.RootIdentity
	admission                     permissionAdmissionKind
}

type permissionAdmissionKind uint8

const (
	permissionAdmissionNone permissionAdmissionKind = iota
	permissionAdmissionProofBoundBash
)

type preparedToolInput struct {
	resolution    tools.ToolResolution
	input         map[string]any
	canonicalJSON string
}

type settledPermissionActionPtrKey struct{}

type permissionDispatchActionKey struct{}

type permissionDispatchAction struct {
	action  PermissionActionDescriptor
	toolCtx *ToolUseContext
}

func withSettledPermissionActionPtr(
	ctx context.Context,
	ptr **PermissionActionDescriptor,
) context.Context {
	return context.WithValue(ctx, settledPermissionActionPtrKey{}, ptr)
}

func setSettledPermissionAction(
	ctx context.Context,
	action *PermissionActionDescriptor,
) {
	if ctx == nil || action == nil {
		return
	}
	ptr, ok := ctx.Value(
		settledPermissionActionPtrKey{},
	).(**PermissionActionDescriptor)
	if !ok || ptr == nil {
		return
	}
	cloned := clonePermissionActionDescriptor(*action)
	*ptr = &cloned
}

func withPermissionDispatchAction(
	ctx context.Context,
	action PermissionActionDescriptor,
	toolCtx *ToolUseContext,
) context.Context {
	return context.WithValue(ctx, permissionDispatchActionKey{}, permissionDispatchAction{
		action:  clonePermissionActionDescriptor(action),
		toolCtx: toolCtx,
	})
}

func permissionDispatchActionFromContext(
	ctx context.Context,
) (permissionDispatchAction, bool) {
	if ctx == nil {
		return permissionDispatchAction{}, false
	}
	binding, ok := ctx.Value(
		permissionDispatchActionKey{},
	).(permissionDispatchAction)
	return binding, ok
}

func clonePermissionActionDescriptor(
	action PermissionActionDescriptor,
) PermissionActionDescriptor {
	action.Input, _ = detachedJSONInput(action.Input)
	action.WorkingRoots = append([]string(nil), action.WorkingRoots...)
	action.Path.Paths = append([]string(nil), action.Path.Paths...)
	return action
}

func prepareToolInputForExecution(
	registry *tools.Registry,
	requestedToolName string,
	input map[string]any,
	policyInstalled bool,
) (preparedToolInput, error) {
	if registry != nil {
		return prepareRegisteredToolInput(registry, requestedToolName, input)
	}
	if policyInstalled {
		return preparedToolInput{}, fmt.Errorf(
			"permission action %q has no tool registry",
			strings.TrimSpace(requestedToolName),
		)
	}

	requestedToolName = strings.TrimSpace(requestedToolName)
	if requestedToolName == "" {
		return preparedToolInput{}, fmt.Errorf("tool execution has an empty tool name")
	}
	detached, err := detachedJSONInput(input)
	if err != nil {
		return preparedToolInput{}, fmt.Errorf(
			"tool execution %q input is not durable JSON: %w",
			requestedToolName,
			err,
		)
	}
	encoded, err := json.Marshal(detached)
	if err != nil {
		return preparedToolInput{}, fmt.Errorf(
			"tool execution %q input cannot be encoded: %w",
			requestedToolName,
			err,
		)
	}
	return preparedToolInput{
		resolution: tools.ToolResolution{
			RequestedName: requestedToolName,
			CanonicalName: requestedToolName,
		},
		input:         detached,
		canonicalJSON: string(encoded),
	}, nil
}

func prepareRegisteredToolInput(
	registry *tools.Registry,
	requestedToolName string,
	input map[string]any,
) (preparedToolInput, error) {
	requestedToolName = strings.TrimSpace(requestedToolName)
	if requestedToolName == "" {
		return preparedToolInput{}, fmt.Errorf("permission action has an empty tool name")
	}
	if reason, unavailable := tools.UnavailableBuiltinToolReason(requestedToolName); unavailable {
		return preparedToolInput{}, fmt.Errorf("permission action %q is unavailable: %s", requestedToolName, reason)
	}
	if registry == nil {
		return preparedToolInput{}, fmt.Errorf("permission action %q has no tool registry", requestedToolName)
	}
	resolution := registry.Resolve(requestedToolName)
	if !resolution.Registered {
		return preparedToolInput{}, fmt.Errorf("permission action %q is not registered", requestedToolName)
	}
	if !resolution.Enabled {
		return preparedToolInput{}, fmt.Errorf("permission action %q is disabled", requestedToolName)
	}
	if resolution.Implementation.Info == nil ||
		strings.TrimSpace(resolution.CanonicalName) == "" {
		return preparedToolInput{}, fmt.Errorf("permission action %q has no canonical registered identity", requestedToolName)
	}

	detached, err := detachedJSONInput(input)
	if err != nil {
		return preparedToolInput{}, fmt.Errorf("permission action %q input is not durable JSON: %w", requestedToolName, err)
	}
	detached = tools.CoerceToolInput(resolution.Implementation.Info, detached)
	if err := tools.ValidateToolInput(resolution.Implementation.Info, detached); err != nil {
		return preparedToolInput{}, err
	}
	if resolution.Implementation.ValidateInput != nil {
		validationInput, cloneErr := detachedJSONInput(detached)
		if cloneErr != nil {
			return preparedToolInput{}, fmt.Errorf("permission action %q input is not durable JSON: %w", requestedToolName, cloneErr)
		}
		if err := resolution.Implementation.ValidateInput(validationInput); err != nil {
			return preparedToolInput{}, fmt.Errorf(
				"input validation failed for %s: %w",
				requestedToolName,
				err,
			)
		}
	}
	encoded, err := json.Marshal(detached)
	if err != nil {
		return preparedToolInput{}, fmt.Errorf("permission action %q input cannot be encoded: %w", requestedToolName, err)
	}
	return preparedToolInput{
		resolution:    resolution,
		input:         detached,
		canonicalJSON: string(encoded),
	}, nil
}

func detachedJSONInput(input map[string]any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var detached map[string]any
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return nil, err
	}
	if detached == nil {
		detached = map[string]any{}
	}
	return detached, nil
}

func (e *QueryEngine) buildPermissionActionDescriptor(
	requestedToolName string,
	input map[string]any,
	toolCtx *ToolUseContext,
) (PermissionActionDescriptor, error) {
	if e == nil {
		return PermissionActionDescriptor{}, fmt.Errorf("permission action has no QueryEngine owner")
	}
	prepared, err := prepareRegisteredToolInput(e.toolRegistry, requestedToolName, input)
	if err != nil {
		return PermissionActionDescriptor{}, err
	}
	impl := prepared.resolution.Implementation
	capabilities := impl.Capabilities
	workingRoots := append([]string(nil), e.GetWorkingDirectories()...)
	rawPath := toolPath(prepared.resolution.CanonicalName, prepared.input, e.config.CWD)
	path := permission.ResolvePermissionPath(rawPath, e.config.CWD)
	pathWithinRoots := rawPath != "" &&
		permission.PermissionPathsWithinRoots(path, workingRoots)
	mode := e.activePermissionMode(toolCtx)
	plan := e.PlanState()
	sessionID := e.config.SessionID
	threadID := e.config.ThreadID
	agentID := e.config.AgentID
	if toolCtx != nil {
		if strings.TrimSpace(toolCtx.SessionID) != "" {
			sessionID = toolCtx.SessionID
		}
		if strings.TrimSpace(toolCtx.ThreadID) != "" {
			threadID = toolCtx.ThreadID
		}
		if strings.TrimSpace(toolCtx.AgentID) != "" {
			agentID = toolCtx.AgentID
		}
	}
	policySnapshot := e.effectivePolicySnapshot(toolCtx)
	var executionIdentity containment.ExecutionIdentity
	bindGuestIdentity := prepared.resolution.CanonicalName == "Bash" &&
		capabilities.Declared &&
		capabilities.Origin == tools.ToolOriginBuiltin &&
		capabilities.ActionKind == tools.ToolActionShell
	if bindGuestIdentity {
		hasShellOwner := e.shellManager != nil
		hasBindingOwner := e.executionBindings != nil &&
			e.executionBindings.Guest() != nil
		if hasShellOwner != hasBindingOwner {
			return PermissionActionDescriptor{}, fmt.Errorf(
				"permission action Guest execution identity is unavailable",
			)
		}
		if hasShellOwner {
			var executionErr error
			executionIdentity, executionErr = e.shellManager.GuestExecutionIdentity()
			if executionErr != nil {
				return PermissionActionDescriptor{}, fmt.Errorf(
					"permission action Guest execution identity: %w",
					executionErr,
				)
			}
			matrixIdentity, matrixErr := containment.ExecutionIdentityFor(
				e.executionBindings.Guest(),
				e.shellManager.GuestExecutionProof(),
			)
			if matrixErr != nil || executionIdentity != matrixIdentity {
				if matrixErr != nil {
					return PermissionActionDescriptor{}, fmt.Errorf(
						"permission action Guest execution binding: %w",
						matrixErr,
					)
				}
				return PermissionActionDescriptor{}, fmt.Errorf(
					"permission action Guest execution binding changed",
				)
			}
		}
	}
	return PermissionActionDescriptor{
		RequestedToolName:    strings.TrimSpace(requestedToolName),
		CanonicalToolName:    prepared.resolution.CanonicalName,
		Input:                prepared.input,
		CanonicalInput:       prepared.canonicalJSON,
		Registered:           prepared.resolution.Registered,
		Enabled:              prepared.resolution.Enabled,
		Selected:             e.toolAllowedBySelectionNames(requestedToolName, prepared.resolution.CanonicalName, capabilities.Origin),
		CapabilityGeneration: prepared.resolution.Generation,
		Origin:               capabilities.Origin,
		ActionKind:           capabilities.ActionKind,
		CapabilitiesDeclared: capabilities.Declared,
		ReadOnly:             impl.IsReadOnly,
		Write:                capabilities.ActionKind == tools.ToolActionWrite,
		Destructive:          impl.IsDestructive,
		InternalStateDefaultSafe: defaultSafeInternalState(
			impl,
			capabilities,
		),
		Network:                       capabilities.Network,
		Child:                         capabilities.Child,
		Dynamic:                       capabilities.Dynamic,
		RequiresUserInteraction:       capabilities.RequiresUserInteraction,
		ShellComplete:                 capabilities.ShellComplete,
		SchemaValidated:               true,
		CustomValidationComplete:      true,
		Path:                          path,
		PathWithinRoots:               pathWithinRoots,
		WorkingRoots:                  workingRoots,
		CWD:                           e.config.CWD,
		RootSessionID:                 e.permissionRootSessionID,
		SessionID:                     sessionID,
		ThreadID:                      threadID,
		AgentID:                       agentID,
		Entrypoint:                    string(e.config.CommandEntrypoint),
		Mode:                          mode,
		PlanActive:                    mode == permission.ModePlan || toolContextPlanActive(toolCtx),
		PlanPhase:                     plan.Phase,
		PlanRevision:                  plan.Revision,
		PlanFileIdentity:              plan.PlanFileIdentity,
		PolicySnapshotID:              policySnapshot.ID(),
		ExecutionPolicyDigest:         executionIdentity.PolicyDigest,
		ExecutionBindingDigest:        executionIdentity.BindingDigest,
		ExecutionProfile:              executionIdentity.Profile,
		ExecutionState:                executionIdentity.State,
		ExecutionAdapter:              executionIdentity.Adapter,
		ExecutionNetwork:              executionIdentity.Network,
		CredentialMode:                executionIdentity.CredentialMode,
		ExecutionAvailability:         executionIdentity.Availability,
		ExecutionReasonCode:           executionIdentity.ReasonCode,
		AdapterAxes:                   executionIdentity.AdapterAxes,
		RuntimeAxes:                   executionIdentity.RuntimeAxes,
		EnforcementAxes:               executionIdentity.Enforced,
		ExecutionCapabilityGeneration: executionIdentity.CapabilityGeneration,
		GuestProcess:                  executionIdentity.ProcessClass == containment.ProcessClassGuest,
		WorkingDirIdentity:            executionIdentity.Root,
	}, nil
}

func samePermissionActionBinding(
	initial PermissionActionDescriptor,
	current PermissionActionDescriptor,
) bool {
	return samePermissionActionAuthorityBinding(initial, current) &&
		initial.CanonicalInput == current.CanonicalInput &&
		initial.Path.Logical == current.Path.Logical &&
		initial.Path.Unsafe == current.Path.Unsafe &&
		slices.Equal(initial.Path.Paths, current.Path.Paths) &&
		initial.PathWithinRoots == current.PathWithinRoots
}

func samePermissionActionAuthorityBinding(
	initial PermissionActionDescriptor,
	current PermissionActionDescriptor,
) bool {
	return initial.RequestedToolName == current.RequestedToolName &&
		initial.CanonicalToolName == current.CanonicalToolName &&
		initial.Registered == current.Registered &&
		initial.Enabled == current.Enabled &&
		initial.Selected == current.Selected &&
		initial.CapabilityGeneration == current.CapabilityGeneration &&
		initial.Origin == current.Origin &&
		initial.ActionKind == current.ActionKind &&
		initial.CapabilitiesDeclared == current.CapabilitiesDeclared &&
		initial.ReadOnly == current.ReadOnly &&
		initial.Write == current.Write &&
		initial.Destructive == current.Destructive &&
		initial.InternalStateDefaultSafe == current.InternalStateDefaultSafe &&
		initial.Network == current.Network &&
		initial.Child == current.Child &&
		initial.Dynamic == current.Dynamic &&
		initial.RequiresUserInteraction == current.RequiresUserInteraction &&
		initial.ShellComplete == current.ShellComplete &&
		initial.SchemaValidated == current.SchemaValidated &&
		initial.CustomValidationComplete == current.CustomValidationComplete &&
		initial.CWD == current.CWD &&
		initial.RootSessionID == current.RootSessionID &&
		initial.SessionID == current.SessionID &&
		initial.ThreadID == current.ThreadID &&
		initial.AgentID == current.AgentID &&
		initial.Entrypoint == current.Entrypoint &&
		initial.Mode == current.Mode &&
		initial.PlanActive == current.PlanActive &&
		initial.PlanPhase == current.PlanPhase &&
		initial.PlanRevision == current.PlanRevision &&
		initial.PlanFileIdentity == current.PlanFileIdentity &&
		initial.PolicySnapshotID == current.PolicySnapshotID &&
		initial.ExecutionPolicyDigest == current.ExecutionPolicyDigest &&
		initial.ExecutionBindingDigest == current.ExecutionBindingDigest &&
		initial.ExecutionProfile == current.ExecutionProfile &&
		initial.ExecutionState == current.ExecutionState &&
		initial.ExecutionAdapter == current.ExecutionAdapter &&
		initial.ExecutionNetwork == current.ExecutionNetwork &&
		initial.CredentialMode == current.CredentialMode &&
		initial.ExecutionAvailability == current.ExecutionAvailability &&
		initial.ExecutionReasonCode == current.ExecutionReasonCode &&
		initial.AdapterAxes == current.AdapterAxes &&
		initial.RuntimeAxes == current.RuntimeAxes &&
		initial.EnforcementAxes == current.EnforcementAxes &&
		initial.ExecutionCapabilityGeneration == current.ExecutionCapabilityGeneration &&
		initial.GuestProcess == current.GuestProcess &&
		initial.WorkingDirIdentity == current.WorkingDirIdentity &&
		slices.Equal(initial.WorkingRoots, current.WorkingRoots)
}

const containedAutoBashAxes = containment.AxisFilesystemRead |
	containment.AxisFilesystemWrite |
	containment.AxisNetworkDenied |
	containment.AxisRootIdentity |
	containment.AxisDescendantConfinement |
	containment.AxisDescendantCleanup |
	containment.AxisWallTime |
	containment.AxisOutput

const containedAutoBashAdapterAxes = containment.AxisFilesystemRead |
	containment.AxisFilesystemWrite |
	containment.AxisNetworkDenied |
	containment.AxisRootIdentity |
	containment.AxisDescendantConfinement

const containedAutoBashRuntimeAxes = containment.AxisRootIdentity |
	containment.AxisDescendantCleanup |
	containment.AxisWallTime |
	containment.AxisOutput

func proofBoundBashMode(mode permission.Mode) bool {
	return mode == permission.ModeDefault || mode == permission.ModeAuto
}

func completeProofBoundBashAdmission(
	action PermissionActionDescriptor,
) (bool, string) {
	switch {
	case !proofBoundBashMode(action.Mode):
		return false, "contained Bash requires Default or Auto mode"
	case action.CanonicalToolName != "Bash" ||
		action.Origin != tools.ToolOriginBuiltin ||
		action.ActionKind != tools.ToolActionShell:
		return false, "contained Bash requires canonical built-in Bash"
	case !action.Registered || !action.Enabled || !action.Selected ||
		!action.CapabilitiesDeclared || !action.SchemaValidated ||
		!action.CustomValidationComplete:
		return false, "contained Bash action is incomplete"
	case !action.GuestProcess:
		return false, "contained Bash requires Guest execution"
	case action.ExecutionAvailability != containment.BindingAvailable ||
		action.ExecutionProfile != containment.ProfileWorkspaceWrite ||
		action.ExecutionState != containment.StateDegraded ||
		action.ExecutionAdapter != containment.AdapterDarwinSeatbelt ||
		action.ExecutionNetwork != containment.NetworkDenied ||
		action.CredentialMode != containment.CredentialAmbientEnvironment:
		return false, "contained Bash Guest binding is unavailable"
	case action.ExecutionPolicyDigest == "" ||
		action.ExecutionBindingDigest == "" ||
		action.ExecutionCapabilityGeneration == "":
		return false, "contained Bash Guest identity is incomplete"
	case action.AdapterAxes != containedAutoBashAdapterAxes ||
		action.RuntimeAxes != containedAutoBashRuntimeAxes ||
		action.EnforcementAxes != containedAutoBashAxes:
		return false, "contained Bash Guest proof axes are incomplete"
	case action.WorkingDirIdentity.Path == "" ||
		action.WorkingDirIdentity.Device == 0 ||
		action.WorkingDirIdentity.Inode == 0:
		return false, "contained Bash Guest root identity is incomplete"
	default:
		return true, ""
	}
}

func permissionActionWithExecutionIdentity(
	action PermissionActionDescriptor,
	identity containment.ExecutionIdentity,
) PermissionActionDescriptor {
	action.ExecutionPolicyDigest = identity.PolicyDigest
	action.ExecutionBindingDigest = identity.BindingDigest
	action.ExecutionProfile = identity.Profile
	action.ExecutionState = identity.State
	action.ExecutionAdapter = identity.Adapter
	action.ExecutionNetwork = identity.Network
	action.CredentialMode = identity.CredentialMode
	action.ExecutionAvailability = identity.Availability
	action.ExecutionReasonCode = identity.ReasonCode
	action.AdapterAxes = identity.AdapterAxes
	action.RuntimeAxes = identity.RuntimeAxes
	action.EnforcementAxes = identity.Enforced
	action.ExecutionCapabilityGeneration = identity.CapabilityGeneration
	action.GuestProcess = identity.ProcessClass == containment.ProcessClassGuest
	action.WorkingDirIdentity = identity.Root
	return action
}

func permissionActionMatchesPreparedInput(
	action PermissionActionDescriptor,
	prepared preparedToolInput,
) bool {
	impl := prepared.resolution.Implementation
	capabilities := impl.Capabilities
	return action.RequestedToolName == prepared.resolution.RequestedName &&
		action.CanonicalToolName == prepared.resolution.CanonicalName &&
		action.Registered == prepared.resolution.Registered &&
		action.Enabled == prepared.resolution.Enabled &&
		action.CapabilityGeneration == prepared.resolution.Generation &&
		action.Origin == capabilities.Origin &&
		action.ActionKind == capabilities.ActionKind &&
		action.CapabilitiesDeclared == capabilities.Declared &&
		action.ReadOnly == impl.IsReadOnly &&
		action.Write == (capabilities.ActionKind == tools.ToolActionWrite) &&
		action.Destructive == impl.IsDestructive &&
		action.InternalStateDefaultSafe ==
			defaultSafeInternalState(impl, capabilities) &&
		action.Network == capabilities.Network &&
		action.Child == capabilities.Child &&
		action.Dynamic == capabilities.Dynamic &&
		action.RequiresUserInteraction == capabilities.RequiresUserInteraction &&
		action.ShellComplete == capabilities.ShellComplete &&
		action.CanonicalInput == prepared.canonicalJSON
}

func defaultSafeInternalState(
	impl tools.ToolImpl,
	capabilities tools.ToolCapabilities,
) bool {
	if !impl.DefaultPermissionAllowed ||
		!capabilities.Declared ||
		capabilities.Origin != tools.ToolOriginBuiltin {
		return false
	}
	return capabilities.ActionKind == tools.ToolActionProcessLocal ||
		capabilities.ActionKind == tools.ToolActionRuntimeState
}

func (d PermissionActionDescriptor) requiresHumanCapabilityInAuto() (bool, string) {
	switch {
	case !d.CapabilitiesDeclared:
		return true, "registered tool capability metadata is incomplete"
	case d.Origin == tools.ToolOriginUnknown ||
		d.Origin == tools.ToolOriginMCP ||
		d.Origin == tools.ToolOriginApp ||
		d.Origin == tools.ToolOriginDynamic:
		return true, "dynamic or external tool origin requires human permission"
	case d.Dynamic:
		return true, "dynamic tool capability requires human permission"
	case d.Child || d.ActionKind == tools.ToolActionChild:
		return true, "child Agent capability requires human permission"
	case d.Network || d.ActionKind == tools.ToolActionNetwork:
		return true, "network capability requires human permission"
	case d.ActionKind == tools.ToolActionShell && !d.ShellComplete:
		return true, "shell action is not completely represented"
	case d.RequiresUserInteraction:
		return true, "tool requires direct user interaction"
	default:
		return false, ""
	}
}

func autoRuleAuthorizes(
	decision permission.RuleDecision,
) bool {
	if !decision.Matched ||
		decision.Action != permission.ActionAllow ||
		decision.Rule == nil ||
		!decision.ToolExact ||
		!decision.InputExact ||
		decision.Rule.InputPattern == "" {
		return false
	}
	switch decision.Rule.Source {
	case permission.SourceLocal, permission.SourceUser:
		return true
	default:
		return false
	}
}

func (e *QueryEngine) approvalAuthorizesAction(
	action PermissionActionDescriptor,
) bool {
	if e == nil || e.approvalTracker == nil {
		return false
	}
	command, scopedPath, fingerprint := permissionInvocation(
		e.config.PermissionProjectRoot,
		action.CanonicalToolName,
		action.Input,
	)
	for _, entry := range e.approvalTracker.List() {
		if entry.SessionScoped &&
			entry.RootSessionID != e.permissionRootSessionID {
			continue
		}
		matches := entry.Key.MatchesInvocation(
			action.CanonicalToolName,
			command,
			scopedPath,
			fingerprint,
		)
		if !matches && action.RequestedToolName != action.CanonicalToolName {
			matches = entry.Key.MatchesInvocation(
				action.RequestedToolName,
				command,
				scopedPath,
				fingerprint,
			)
		}
		if !matches {
			continue
		}
		if action.Mode != permission.ModeAuto {
			return true
		}
		if entry.Reason != "user" {
			continue
		}
		switch {
		case entry.Key.ExactCommand && entry.Key.CommandPattern != "":
			return true
		case entry.Key.ExactPath && entry.Key.PathPattern != "":
			return true
		case entry.Key.InputFingerprint != "":
			return true
		case entry.Key.RecursivePath &&
			action.PathWithinRoots &&
			isContainedReadAction(action.CanonicalToolName):
			return true
		}
	}
	return false
}

func isContainedReadAction(toolName string) bool {
	switch toolName {
	case "Read", "Grep", "Glob":
		return true
	default:
		return false
	}
}
