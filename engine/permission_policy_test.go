package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestP221aInvocationPolicyProjectionAndLegacyReview(t *testing.T) {
	if boundary := invocationPolicyBoundaryFor(nil, nil); boundary != invocationPolicyNoPolicyInstalled {
		t.Fatalf("nil/nil boundary = %q, want %q", boundary, invocationPolicyNoPolicyInstalled)
	}
	if got := p221aPolicyEngine().evaluateInvocationPolicy(context.Background(), nil, "Bash", nil, nil); got.Decision != invocationPolicyDeny {
		t.Fatalf("selection deny projection = %#v", got)
	}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	allowEngine := NewQueryEngine(QueryEngineConfig{
		CWD:            t.TempDir(),
		PermissionMode: permission.ModeBypassPermissions,
		ToolRegistry:   registry,
	})
	t.Cleanup(allowEngine.Close)
	if got := allowEngine.evaluateInvocationPolicy(
		context.Background(),
		nil,
		"Bash",
		map[string]any{"command": "true"},
		nil,
	); got.Decision != invocationPolicyAllow || !got.Allowed {
		t.Fatalf("bypass allow projection = %#v", got)
	}
	promptEngine := NewQueryEngine(QueryEngineConfig{
		CWD:          t.TempDir(),
		ToolRegistry: registry,
	})
	t.Cleanup(promptEngine.Close)
	if got := promptEngine.evaluateInvocationPolicy(context.Background(), func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
		return true, "prompted"
	}, "Bash", map[string]any{"command": "mkdir generated"}, nil); got.Decision != invocationPolicyRequireHuman || !got.Allowed {
		t.Fatalf("ordinary prompt projection = %#v", got)
	}

	for _, tt := range []struct {
		name    string
		model   *funcModel
		allowed bool
		prompts int
		cleared bool
	}{
		{name: "classifier allow", model: &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return &schema.Message{Role: schema.Assistant, Content: "<allow/>"}, nil
		}}, allowed: true},
		{name: "classifier deny", model: &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return &schema.Message{Role: schema.Assistant, Content: "<block/>"}, nil
		}}},
		{name: "classifier untagged remains deny", model: &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return &schema.Message{Role: schema.Assistant, Content: "uncertain"}, nil
		}}},
		{name: "classifier error prompts", model: &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return nil, errors.New("unavailable")
		}}, allowed: true, prompts: 1, cleared: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prompts := 0
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: t.TempDir(), ChatModel: tt.model,
				CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
					prompts++
					return true, "prompted"
				},
			})
			t.Cleanup(eng.Close)
			var events []QueryEvent
			ctx := withClassifierStatusEmitter(context.Background(), func(event QueryEvent) {
				events = append(events, event)
			})
			outcome := eng.evaluateInvocationPolicy(
				ctx,
				eng.config.CanUseTool,
				"TaskCreate",
				map[string]any{
					"subject":     "review state",
					"description": "review state",
				},
				&ToolUseContext{Options: &ToolUseOptions{
					PermissionMode: permission.ModeAuto,
				}},
			)
			if outcome.Decision != invocationPolicyReview || outcome.Allowed != tt.allowed || prompts != tt.prompts {
				t.Fatalf("outcome=%#v prompts=%d, want review/%v/%d", outcome, prompts, tt.allowed, tt.prompts)
			}
			if len(events) != 2 || (events[1].ClassifierStatus.Phase == ClassifierStatusCleared) != tt.cleared {
				t.Fatalf("classifier events = %#v, cleared=%v", events, tt.cleared)
			}
		})
	}
}

func TestP221aPolicySnapshotIdentityInputs(t *testing.T) {
	eng := p221aPolicyEngine()
	ctx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	base := eng.effectivePolicySnapshot(ctx)
	if base.ID() != eng.effectivePolicySnapshot(ctx).ID() {
		t.Fatal("identical effective input changed snapshot identity")
	}

	mutations := []string{
		"rule action", "rule source", "rule order", "approval key", "approval session",
		"mode", "plan phase", "plan revision", "plan file", "root session", "cwd",
		"additional root", "tool selection nil", "tool selection preset",
		"tool selection explicit empty", "tool selection named",
	}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			eng := p221aPolicyEngine()
			ctx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
			before := eng.effectivePolicySnapshot(ctx).ID()
			// Rebind closures to this subtest's engine where they mutate it.
			switch mutation {
			case "mode":
				ctx.Options.PermissionMode = permission.ModeAuto
			case "rule action":
				eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Bash", Action: permission.ActionAllow, Source: "one"}})
			case "rule source":
				eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Bash", Action: permission.ActionDeny, Source: "two"}})
			case "rule order":
				eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Read", Action: permission.ActionAllow, Source: "one"}, {ToolName: "Bash", Action: permission.ActionDeny, Source: "one"}})
			case "approval key":
				eng.approvalTracker.Approve(permission.ApprovalKey{ToolName: "Bash", CommandPattern: "git"}, "display-only", false)
			case "approval session":
				eng.approvalTracker.ApproveForRootSession(permission.ApprovalKey{ToolName: "Bash", CommandPattern: "git"}, "display-only", "other-root")
			case "plan phase":
				eng.planState.Phase = PlanPhaseActive
			case "plan revision":
				eng.planState.Revision++
			case "plan file":
				eng.planState.PlanFileIdentity = "/tmp/other-plan.md"
			case "root session":
				eng.permissionRootSessionID = "other-root"
			case "cwd":
				eng.config.CWD = "/other"
			case "additional root":
				eng.config.AdditionalDirs[0] = "/other-extra"
			case "tool selection nil":
				eng.config.ToolSelection = nil
			case "tool selection preset":
				eng.config.ToolSelection = &tools.ToolSelection{Preset: tools.PresetDefault}
			case "tool selection explicit empty":
				eng.config.ToolSelection = &tools.ToolSelection{Names: []string{}}
			case "tool selection named":
				eng.config.ToolSelection = &tools.ToolSelection{Names: []string{"Bash"}}
			}
			if after := eng.effectivePolicySnapshot(ctx).ID(); after == before {
				t.Fatal("mutation did not change snapshot identity")
			}
		})
	}
}

func TestP221aPolicySnapshotCanonicalAndImmutable(t *testing.T) {
	eng := p221aPolicyEngine()
	keyA := permission.ApprovalKey{ToolName: "Bash", CommandPattern: "git"}
	keyB := permission.ApprovalKey{ToolName: "Write", PathPattern: "/tmp/a", ExactPath: true}
	eng.approvalTracker.Approve(keyB, "first reason", false)
	eng.approvalTracker.ApproveForRootSession(keyA, "second reason", "root")
	first := eng.effectivePolicySnapshot(nil)
	if strings.Contains(first.encoded, `"reserved"`) ||
		strings.Contains(first.encoded, `"capability_generation"`) ||
		strings.Contains(first.encoded, `"reviewer_policy_version"`) ||
		strings.Contains(first.encoded, `"child_scope"`) {
		t.Fatalf("reserved fields did not use the fixed omitted representation: %s", first.encoded)
	}
	eng.approvalTracker.RevokeAll()
	eng.approvalTracker.ApproveForRootSession(keyA, "changed reason", "root")
	eng.approvalTracker.Approve(keyB, "another reason", false)
	if got := eng.effectivePolicySnapshot(nil); got.ID() != first.ID() {
		t.Fatalf("approval traversal or display-only reason changed identity: %s != %s", got.ID(), first.ID())
	}
	eng.config.AdditionalDirs[0] = "/mutated"
	eng.config.ToolSelection.Names[0] = "Read"
	eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Read", Action: permission.ActionAllow, Source: "mutated"}})
	if strings.Contains(first.encoded, "mutated") || first.ID() == "" {
		t.Fatalf("observed snapshot retained mutable alias: %#v", first)
	}
}

func TestP221aPolicySnapshotPreservesLegacyProjectGraphRevision(t *testing.T) {
	selections := []struct {
		name      string
		selection *tools.ToolSelection
	}{
		{name: "nil"},
		{name: "explicit empty names", selection: &tools.ToolSelection{Names: []string{}}},
		{name: "preset", selection: &tools.ToolSelection{Preset: tools.PresetDefault}},
		{name: "named", selection: &tools.ToolSelection{Names: []string{"Bash", "Read"}}},
	}
	for _, selection := range selections {
		for _, planFile := range []string{"", "/tmp/plan.md"} {
			name := selection.name + "/empty plan file"
			if planFile != "" {
				name = selection.name + "/populated plan file"
			}
			t.Run(name, func(t *testing.T) {
				eng := p221aPolicyEngine()
				eng.config.ToolSelection = selection.selection
				eng.planState.PlanFileIdentity = planFile
				eng.approvalTracker.Approve(
					permission.ApprovalKey{ToolName: "Write", PathPattern: "/tmp/a", ExactPath: true},
					"display-only",
					false,
				)
				eng.approvalTracker.ApproveForRootSession(
					permission.ApprovalKey{ToolName: "Bash", CommandPattern: "git"},
					"another display-only reason",
					"root",
				)
				ctx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
				if got, want := eng.effectivePolicySnapshot(ctx).ID(), p221aLegacyProjectGraphPolicyRevision(eng, ctx); got != want {
					t.Fatalf("snapshot revision = %s, pre-P22.1a revision = %s", got, want)
				}
			})
		}
	}

	nilSelection := p221aPolicyEngine()
	nilSelection.config.ToolSelection = nil
	emptySelection := p221aPolicyEngine()
	emptySelection.config.ToolSelection = &tools.ToolSelection{Names: []string{}}
	if nilSelection.effectivePolicySnapshot(nil).ID() == emptySelection.effectivePolicySnapshot(nil).ID() {
		t.Fatal("nil and explicitly empty tool selection lost their legacy identity distinction")
	}
}

func TestP221aProjectGraphRevisionUsesSharedSnapshot(t *testing.T) {
	eng := p221aPolicyEngine()
	ctx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	if got, want := eng.projectGraphPolicyRevision(ctx), eng.effectivePolicySnapshot(ctx).ID(); got != want {
		t.Fatalf("project graph revision = %s, want shared snapshot %s", got, want)
	}
	before := eng.projectGraphPolicyRevision(ctx)
	eng.config.AdditionalDirs[0] = "/drifted"
	if after := eng.projectGraphPolicyRevision(ctx); after == before {
		t.Fatal("project graph revision ignored policy input drift")
	}
}

// p221aLegacyProjectGraphPolicyRevision freezes the exact pre-P22.1a encoding
// as an upgrade-compatibility fixture for persisted ProjectGraph decisions.
func p221aLegacyProjectGraphPolicyRevision(
	e *QueryEngine,
	toolCtx *ToolUseContext,
) string {
	rules := e.permissionRulesSnapshot().Snapshot()
	approvals := e.approvalTracker.List()
	sort.Slice(approvals, func(i, j int) bool {
		left, _ := json.Marshal(approvals[i].Key)
		right, _ := json.Marshal(approvals[j].Key)
		if bytes.Equal(left, right) {
			return approvals[i].RootSessionID < approvals[j].RootSessionID
		}
		return string(left) < string(right)
	})
	type approvalPolicy struct {
		Key           permission.ApprovalKey `json:"key"`
		SessionScoped bool                   `json:"session_scoped"`
		RootSessionID string                 `json:"root_session_id,omitempty"`
	}
	canonicalApprovals := make([]approvalPolicy, 0, len(approvals))
	for _, entry := range approvals {
		canonicalApprovals = append(canonicalApprovals, approvalPolicy{
			Key:           entry.Key,
			SessionScoped: entry.SessionScoped,
			RootSessionID: entry.RootSessionID,
		})
	}
	plan := e.PlanState()
	encoded, _ := json.Marshal(struct {
		Rules         []permission.PermissionRule `json:"rules"`
		Approvals     []approvalPolicy            `json:"approvals"`
		Mode          permission.Mode             `json:"mode"`
		PlanPhase     PlanPhase                   `json:"plan_phase"`
		PlanRevision  uint64                      `json:"plan_revision"`
		PlanFile      string                      `json:"plan_file,omitempty"`
		RootSessionID string                      `json:"root_session_id"`
		WorkingDirs   []string                    `json:"working_dirs"`
		ToolSelection any                         `json:"tool_selection,omitempty"`
	}{
		Rules:         rules,
		Approvals:     canonicalApprovals,
		Mode:          e.activePermissionMode(toolCtx),
		PlanPhase:     plan.Phase,
		PlanRevision:  plan.Revision,
		PlanFile:      plan.PlanFileIdentity,
		RootSessionID: e.permissionRootSessionID,
		WorkingDirs:   e.GetWorkingDirectories(),
		ToolSelection: e.config.ToolSelection,
	})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func p221aPolicyEngine() *QueryEngine {
	return &QueryEngine{
		config:                  QueryEngineConfig{CWD: "/workspace", AdditionalDirs: []string{"/extra"}, ToolSelection: &tools.ToolSelection{Names: []string{"Write"}}},
		approvalTracker:         permission.NewApprovalTracker(),
		permissionRules:         permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Bash", Action: permission.ActionDeny, Source: "one"}, {ToolName: "Read", Action: permission.ActionAllow, Source: "one"}}),
		permissionRootSessionID: "root",
		planState:               PlanState{Phase: PlanPhaseInactive, PlanFileIdentity: "/tmp/plan.md", Revision: 4},
	}
}
