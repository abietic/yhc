package engine

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

func restorePersistedPlanState(
	record *session.PersistedPlanState,
	persistedMode string,
	sessionID string,
	agentID string,
	fallbackMode permission.Mode,
	preserveLiveApproval bool,
) (PlanState, permission.Mode, []string) {
	mode, modeWarnings := restoredPermissionMode(persistedMode, fallbackMode)
	fallback := initialPlanState(QueryEngineConfig{
		SessionID:      sessionID,
		AgentID:        agentID,
		PermissionMode: mode,
	})
	if record == nil {
		return fallback, mode, modeWarnings
	}

	warnings := append([]string(nil), modeWarnings...)
	if record.Version != session.PersistedPlanStateVersion {
		warnings = append(warnings, fmt.Sprintf(
			"ignored unsupported persisted Plan state version %d; restored the safest phase from permission mode",
			record.Version,
		))
		return fallback, mode, warnings
	}

	phase := PlanPhase(strings.TrimSpace(record.Phase))
	switch phase {
	case PlanPhaseInactive, PlanPhaseActive, PlanPhaseAwaitingApproval:
	default:
		warnings = append(warnings, fmt.Sprintf(
			"ignored invalid persisted Plan phase %q; restored the safest phase from permission mode",
			record.Phase,
		))
		return fallback, mode, warnings
	}

	state := PlanState{
		Phase:                 phase,
		PlanFileIdentity:      record.PlanFileIdentity,
		ReturnMode:            permission.Mode(record.ReturnMode),
		ApprovalRequestID:     strings.TrimSpace(record.ApprovalRequestID),
		ApprovalInitialDigest: strings.TrimSpace(record.ApprovalInitialDigest),
		Revision:              record.Revision,
	}
	if !validPersistedPlanFileIdentity(
		state.PlanFileIdentity,
		sessionID,
		agentID,
	) {
		state.PlanFileIdentity = tools.GetPlanFilePath(sessionID, agentID)
		warnings = append(warnings,
			"replaced an unsafe persisted Plan file identity with the current session Plan path",
		)
	}
	if !knownPlanReturnMode(state.ReturnMode) {
		state.ReturnMode = permission.ModeDefault
		warnings = append(warnings, fmt.Sprintf(
			"replaced invalid persisted Plan return mode %q with default",
			record.ReturnMode,
		))
	}
	if state.Phase != PlanPhaseInactive && state.Revision == 0 {
		state.Revision = 1
		warnings = append(warnings,
			"repaired a zero persisted Plan revision before restoring containment",
		)
	}

	switch state.Phase {
	case PlanPhaseInactive:
		state.ApprovalInitialDigest = ""
		if mode == permission.ModePlan {
			fallback.Revision = maxPlanRevision(fallback.Revision, state.Revision)
			warnings = append(warnings,
				"persisted Plan phase conflicted with Plan permission mode; preserved Active containment",
			)
			return fallback, permission.ModePlan, warnings
		}
		if state.ApprovalRequestID != "" {
			state.ApprovalRequestID = ""
			warnings = append(warnings,
				"discarded a persisted approval reference from inactive Plan state",
			)
		}
		return state, mode, warnings
	case PlanPhaseActive:
		state.ApprovalInitialDigest = ""
		if state.ApprovalRequestID != "" {
			state.ApprovalRequestID = ""
			warnings = append(warnings,
				"discarded a persisted approval reference from active Plan state",
			)
		}
		if mode != permission.ModePlan {
			warnings = append(warnings,
				"restored Plan permission mode from the durable Active phase",
			)
		}
		return state, permission.ModePlan, warnings
	case PlanPhaseAwaitingApproval:
		if preserveLiveApproval {
			return state, permission.ModePlan, warnings
		}
		state.Phase = PlanPhaseActive
		state.ApprovalRequestID = ""
		state.ApprovalInitialDigest = ""
		if state.Revision < math.MaxUint64 {
			state.Revision++
		}
		warnings = append(warnings,
			"normalized persisted AwaitingApproval to Active; the previous approval request is no longer actionable",
		)
		return state, permission.ModePlan, warnings
	default:
		return fallback, mode, warnings
	}
}

func restoredPermissionMode(
	persisted string,
	fallback permission.Mode,
) (permission.Mode, []string) {
	persisted = strings.TrimSpace(persisted)
	if persisted == "" {
		if knownPermissionMode(fallback) {
			return fallback, nil
		}
		return permission.ModeDefault, nil
	}
	mode := permission.Mode(persisted)
	if knownPermissionMode(mode) {
		return mode, nil
	}
	return permission.ModeDefault, []string{fmt.Sprintf(
		"replaced unknown persisted permission mode %q with default",
		persisted,
	)}
}

func validPersistedPlanFileIdentity(
	value string,
	sessionID string,
	agentID string,
) bool {
	if strings.TrimSpace(value) == "" ||
		value != strings.TrimSpace(value) ||
		!filepath.IsAbs(value) ||
		value != filepath.Clean(value) {
		return false
	}
	expected := tools.GetPlanFilePath(sessionID, agentID)
	if filepath.Base(value) != filepath.Base(expected) {
		return false
	}
	plansDir := filepath.Dir(value)
	expectedPlansDir := tools.GetPlansDirPath()
	if filepath.Base(plansDir) != filepath.Base(expectedPlansDir) ||
		filepath.Base(filepath.Dir(plansDir)) !=
			filepath.Base(filepath.Dir(expectedPlansDir)) ||
		!pathHasNoSymlinkComponents(plansDir) {
		return false
	}
	info, err := os.Lstat(value)
	if err == nil {
		return info.Mode()&os.ModeSymlink == 0
	}
	return os.IsNotExist(err)
}

func validLivePersistedPlanApproval(
	record *session.PersistedPlanState,
	sessionID string,
	agentID string,
) bool {
	return record != nil &&
		record.Version == session.PersistedPlanStateVersion &&
		PlanPhase(strings.TrimSpace(record.Phase)) ==
			PlanPhaseAwaitingApproval &&
		strings.TrimSpace(record.ApprovalRequestID) != "" &&
		record.Revision > 0 &&
		validPlanDigest(record.ApprovalInitialDigest) &&
		knownPlanReturnMode(permission.Mode(record.ReturnMode)) &&
		validPersistedPlanFileIdentity(
			record.PlanFileIdentity,
			sessionID,
			agentID,
		)
}

func maxPlanRevision(left, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}
