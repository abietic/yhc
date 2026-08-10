package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

// invocationPolicyDecision is the QueryEngine-owned projection of an
// invocation-policy branch. It is deliberately internal: callers still see
// the legacy bool/reason contract through wrapCanUseTool.
type invocationPolicyDecision string

const (
	invocationPolicyAllow        invocationPolicyDecision = "allow"
	invocationPolicyDeny         invocationPolicyDecision = "deny"
	invocationPolicyRequireHuman invocationPolicyDecision = "require_human"
	invocationPolicyReview       invocationPolicyDecision = "review"
)

// invocationPolicyBoundary distinguishes the absent-policy library boundary
// from an installed invocation policy. It is intentionally not a fifth
// decision state.
type invocationPolicyBoundary string

const (
	invocationPolicyInstalled         invocationPolicyBoundary = "installed"
	invocationPolicyNoPolicyInstalled invocationPolicyBoundary = "no_invocation_policy_installed"
)

func invocationPolicyBoundaryFor(inner CanUseToolFn, prompt PermissionPromptFn) invocationPolicyBoundary {
	if inner == nil && prompt == nil {
		return invocationPolicyNoPolicyInstalled
	}
	return invocationPolicyInstalled
}

type invocationPolicyOutcome struct {
	Decision invocationPolicyDecision
	Allowed  bool
	Reason   string
}

func allowInvocationPolicy() invocationPolicyOutcome {
	return invocationPolicyOutcome{Decision: invocationPolicyAllow, Allowed: true}
}

func denyInvocationPolicy(reason string) invocationPolicyOutcome {
	return invocationPolicyOutcome{Decision: invocationPolicyDeny, Reason: reason}
}

func requireHumanInvocationPolicy(allowed bool, reason string) invocationPolicyOutcome {
	return invocationPolicyOutcome{
		Decision: invocationPolicyRequireHuman,
		Allowed:  allowed,
		Reason:   reason,
	}
}

func reviewInvocationPolicy(allowed bool, reason string) invocationPolicyOutcome {
	return invocationPolicyOutcome{
		Decision: invocationPolicyReview,
		Allowed:  allowed,
		Reason:   reason,
	}
}

// effectivePolicySnapshot is immutable after construction: encoded and ID are
// derived from detached values and it retains no mutable config aliases.
type effectivePolicySnapshot struct {
	encoded string
	id      string
}

func (s effectivePolicySnapshot) ID() string { return s.id }

func (s effectivePolicySnapshot) document() (
	effectivePolicyDocument,
	bool,
) {
	var document effectivePolicyDocument
	if s.encoded == "" ||
		json.Unmarshal([]byte(s.encoded), &document) != nil {
		return effectivePolicyDocument{}, false
	}
	return document, true
}

type policyApproval struct {
	Key           permission.ApprovalKey `json:"key"`
	SessionScoped bool                   `json:"session_scoped"`
	RootSessionID string                 `json:"root_session_id,omitempty"`
}

type policyReservedFields struct {
	CapabilityGeneration  string `json:"capability_generation"`
	ReviewerPolicyVersion string `json:"reviewer_policy_version"`
	ChildScope            string `json:"child_scope"`
}

// effectivePolicyDocument retains the pre-P22.1a ProjectGraph JSON layout so
// an unchanged policy keeps the same revision across an upgrade. Reserved is
// fixed nil in this slice and therefore has one stable omitted representation.
type effectivePolicyDocument struct {
	Rules         []permission.PermissionRule `json:"rules"`
	Approvals     []policyApproval            `json:"approvals"`
	Mode          permission.Mode             `json:"mode"`
	PlanPhase     PlanPhase                   `json:"plan_phase"`
	PlanRevision  uint64                      `json:"plan_revision"`
	PlanFile      string                      `json:"plan_file,omitempty"`
	RootSessionID string                      `json:"root_session_id"`
	WorkingDirs   []string                    `json:"working_dirs"`
	ToolSelection any                         `json:"tool_selection,omitempty"`
	Reserved      *policyReservedFields       `json:"reserved,omitempty"`
}

func (e *QueryEngine) effectivePolicySnapshot(toolCtx *ToolUseContext) effectivePolicySnapshot {
	rules := e.permissionRulesSnapshot().Snapshot()
	approvals := []permission.ApprovalEntry(nil)
	if e.approvalTracker != nil {
		approvals = e.approvalTracker.List()
	}
	sort.Slice(approvals, func(i, j int) bool {
		left, _ := json.Marshal(approvals[i].Key)
		right, _ := json.Marshal(approvals[j].Key)
		if bytes.Equal(left, right) {
			return approvals[i].RootSessionID < approvals[j].RootSessionID
		}
		return string(left) < string(right)
	})
	canonicalApprovals := make([]policyApproval, 0, len(approvals))
	for _, entry := range approvals {
		canonicalApprovals = append(canonicalApprovals, policyApproval{
			Key:           entry.Key,
			SessionScoped: entry.SessionScoped,
			RootSessionID: entry.RootSessionID,
		})
	}

	plan := e.PlanState()
	e.mu.Lock()
	workingDirs := make([]string, 1, 1+len(e.config.AdditionalDirs))
	workingDirs[0] = e.config.CWD
	workingDirs = append(workingDirs, e.config.AdditionalDirs...)
	rootSessionID := e.permissionRootSessionID
	selection := clonePolicyToolSelection(e.config.ToolSelection)
	e.mu.Unlock()
	encoded, _ := json.Marshal(effectivePolicyDocument{
		Rules:         rules,
		Approvals:     canonicalApprovals,
		Mode:          e.activePermissionMode(toolCtx),
		PlanPhase:     plan.Phase,
		PlanRevision:  plan.Revision,
		PlanFile:      plan.PlanFileIdentity,
		RootSessionID: rootSessionID,
		WorkingDirs:   workingDirs,
		ToolSelection: selection,
		Reserved:      nil,
	})
	digest := sha256.Sum256(encoded)
	return effectivePolicySnapshot{encoded: string(encoded), id: hex.EncodeToString(digest[:])}
}

func clonePolicyToolSelection(selection *tools.ToolSelection) any {
	if selection == nil {
		return (*tools.ToolSelection)(nil)
	}
	var names []string
	if selection.Names != nil {
		names = append([]string{}, selection.Names...)
	}
	return &tools.ToolSelection{
		Preset: selection.Preset,
		Names:  names,
	}
}

// permissionCommitTransitionExpected distinguishes the one policy mutation
// owned by a session/always decision from unrelated concurrent drift. The
// caller still binds dispatch to the returned post-commit snapshot.
func (e *QueryEngine) permissionCommitTransitionExpected(
	before effectivePolicySnapshot,
	after effectivePolicySnapshot,
	action PermissionActionDescriptor,
	decision PermissionInteractionDecision,
) bool {
	beforeDocument, beforeOK := before.document()
	afterDocument, afterOK := after.document()
	if !beforeOK || !afterOK {
		return false
	}
	switch decision {
	case PermissionAllowOnce:
		return policyDocumentsEqual(beforeDocument, afterDocument)
	case PermissionAllowSession:
		key, _, err := sessionApprovalKey(
			e.config.PermissionProjectRoot,
			action.CanonicalToolName,
			action.Input,
		)
		if err != nil {
			return false
		}
		expected := policyApproval{
			Key:           key,
			SessionScoped: true,
			RootSessionID: e.permissionRootSessionID,
		}
		if policyDocumentsEqual(beforeDocument, afterDocument) {
			return policyDocumentHasApproval(beforeDocument, expected)
		}
		return policyDocumentDiffIsExpected(
			beforeDocument,
			afterDocument,
			func(document *effectivePolicyDocument) bool {
				return removeExpectedPolicyApproval(document, expected)
			},
		)
	case PermissionAllowAlways:
		exactRule, err := permission.BuildExactRuleFromInvocation(
			action.CanonicalToolName,
			action.Input,
			e.permissionProjectIdentity.Root,
		)
		if err != nil {
			return false
		}
		expected := exactRule.Rule
		expected.Source = permission.SourceLocal
		if policyDocumentsEqual(beforeDocument, afterDocument) {
			return policyDocumentHasRule(beforeDocument, expected)
		}
		return policyDocumentDiffIsExpected(
			beforeDocument,
			afterDocument,
			func(document *effectivePolicyDocument) bool {
				return removeExpectedPolicyRule(document, expected)
			},
		)
	default:
		return false
	}
}

func policyDocumentDiffIsExpected(
	before effectivePolicyDocument,
	after effectivePolicyDocument,
	removeExpected func(*effectivePolicyDocument) bool,
) bool {
	normalizePolicyDocument(&before)
	normalizePolicyDocument(&after)
	if removeExpected == nil || !removeExpected(&after) {
		return false
	}
	normalizePolicyDocument(&after)
	return reflect.DeepEqual(before, after)
}

func policyDocumentsEqual(
	left effectivePolicyDocument,
	right effectivePolicyDocument,
) bool {
	normalizePolicyDocument(&left)
	normalizePolicyDocument(&right)
	return reflect.DeepEqual(left, right)
}

func normalizePolicyDocument(document *effectivePolicyDocument) {
	if document == nil {
		return
	}
	if len(document.Rules) == 0 {
		document.Rules = nil
	}
	if len(document.Approvals) == 0 {
		document.Approvals = nil
	}
}

func removeExpectedPolicyApproval(
	document *effectivePolicyDocument,
	expected policyApproval,
) bool {
	if document == nil {
		return false
	}
	for index := range document.Approvals {
		if reflect.DeepEqual(document.Approvals[index], expected) {
			document.Approvals = append(
				document.Approvals[:index],
				document.Approvals[index+1:]...,
			)
			if len(document.Approvals) == 0 {
				document.Approvals = nil
			}
			return true
		}
	}
	return false
}

func policyDocumentHasApproval(
	document effectivePolicyDocument,
	expected policyApproval,
) bool {
	for index := range document.Approvals {
		if reflect.DeepEqual(document.Approvals[index], expected) {
			return true
		}
	}
	return false
}

func removeExpectedPolicyRule(
	document *effectivePolicyDocument,
	expected permission.PermissionRule,
) bool {
	if document == nil {
		return false
	}
	for index := range document.Rules {
		if reflect.DeepEqual(document.Rules[index], expected) {
			document.Rules = append(
				document.Rules[:index],
				document.Rules[index+1:]...,
			)
			if len(document.Rules) == 0 {
				document.Rules = nil
			}
			return true
		}
	}
	return false
}

func policyDocumentHasRule(
	document effectivePolicyDocument,
	expected permission.PermissionRule,
) bool {
	for index := range document.Rules {
		if reflect.DeepEqual(document.Rules[index], expected) {
			return true
		}
	}
	return false
}
