package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

func TestP200PlanDigestIdentifiesExactBytes(t *testing.T) {
	withoutNewline := PlanBytesDigest([]byte("# Plan"))
	withNewline := PlanBytesDigest([]byte("# Plan\n"))
	if withoutNewline == withNewline {
		t.Fatal("Plan digest normalized a trailing newline")
	}
	if !strings.HasPrefix(withoutNewline, "sha256:") ||
		len(withoutNewline) != len("sha256:")+64 ||
		!validPlanDigest(withoutNewline) {
		t.Fatalf("canonical Plan digest = %q", withoutNewline)
	}
	for _, invalid := range []string{
		"",
		" " + withoutNewline,
		strings.ToUpper(withoutNewline),
		planDigestPrefix + strings.ToUpper(
			strings.TrimPrefix(withoutNewline, planDigestPrefix),
		),
		"sha256:00",
		strings.TrimPrefix(withoutNewline, "sha256:"),
	} {
		if validPlanDigest(invalid) {
			t.Fatalf("accepted invalid Plan digest %q", invalid)
		}
	}
}

func TestP200BeginApprovalCapturesExactInitialDigest(t *testing.T) {
	const content = "# Plan\n\n1. preserve exact bytes\n"
	path := p200PreparePlan(t, "digest-session", "", content)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "digest-session",
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
	})
	t.Cleanup(eng.Close)
	if _, err := eng.beginPlanTurn("digest-turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.endPlanTurn("digest-turn") })

	request, err := eng.beginPlanApproval("digest-request", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, digest, err := ReadPlanReviewSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	state := eng.PlanState()
	if string(data) != content ||
		request.InitialPlanDigest != PlanBytesDigest([]byte(content)) ||
		request.InitialPlanDigest != digest ||
		state.ApprovalInitialDigest != request.InitialPlanDigest ||
		state.ApprovalRequestID != request.RequestID ||
		state.Phase != PlanPhaseAwaitingApproval {
		t.Fatalf("captured Plan review identity request=%#v state=%#v", request, state)
	}
}

func TestP200ApprovalBindsReviewedBytesAtSettlement(t *testing.T) {
	t.Run("unchanged reviewed bytes approve", func(t *testing.T) {
		request, _ := p200ReviewRequest(t, "# Initial\n")
		raw := p200TypedApproval(
			request,
			request.InitialPlanDigest,
			permission.ModeDefault,
			false,
		)
		if planApprovalAllowsExit(raw.PlanApproval, request.RequestID) {
			t.Fatal("adapter-authored typed outcome bypassed engine settlement")
		}
		if _, ok := planModeTransition(
			"ExitPlanMode",
			request.RequestID,
			raw.PlanApproval,
		); ok {
			t.Fatal("adapter-authored typed outcome changed permission mode")
		}
		result := normalizePlanApprovalDecision(
			request,
			raw,
		)
		if !result.Allowed() ||
			result.PlanApproval == nil ||
			result.PlanApproval.Outcome != PlanApprovalApprove ||
			result.PlanApproval.Approved ||
			!planApprovalAllowsExit(result.PlanApproval, request.RequestID) {
			t.Fatalf("unchanged approval = %#v", result)
		}
		if mode, ok := planModeTransition(
			"ExitPlanMode",
			request.RequestID,
			result.PlanApproval,
		); !ok || mode != permission.ModeDefault {
			t.Fatalf("settled approval transition = (%q, %v)", mode, ok)
		}
		if planApprovalAllowsExit(result.PlanApproval, "another-request") {
			t.Fatal("settled approval crossed request identity")
		}
	})

	t.Run("changed after review fails closed", func(t *testing.T) {
		request, path := p200ReviewRequest(t, "# Initial\n")
		reviewedDigest := request.InitialPlanDigest
		if err := os.WriteFile(path, []byte("# Changed after review\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := normalizePlanApprovalDecision(
			request,
			p200TypedApproval(request, reviewedDigest, permission.ModeDefault, false),
		)
		if result.Allowed() ||
			result.PlanApproval == nil ||
			result.PlanApproval.Outcome != PlanApprovalCancel ||
			!strings.Contains(result.Message, "stale") {
			t.Fatalf("stale approval = %#v", result)
		}
	})

	t.Run("reloaded changed bytes can approve", func(t *testing.T) {
		request, path := p200ReviewRequest(t, "# Initial\n")
		changed := []byte("# Externally edited and reloaded\n")
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		result := normalizePlanApprovalDecision(
			request,
			p200TypedApproval(
				request,
				PlanBytesDigest(changed),
				permission.ModeAcceptEdits,
				false,
			),
		)
		if !result.Allowed() ||
			result.PlanApproval == nil ||
			result.PlanApproval.ReviewedPlanDigest != PlanBytesDigest(changed) ||
			result.PlanApproval.TargetMode != permission.ModeAcceptEdits {
			t.Fatalf("reloaded approval = %#v", result)
		}
	})
}

func TestP204SerializedTypedApproveCannotExecute(t *testing.T) {
	request, _ := p200ReviewRequest(t, "# Serialized intent\n")
	raw := p200TypedApproval(
		request,
		request.InitialPlanDigest,
		permission.ModeDefault,
		false,
	)
	payload, err := json.Marshal(raw.PlanApproval)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PlanApprovalDecision
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}

	executions := 0
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
		IsPlanModeTransition: true,
		Execute: func(string) (string, error) {
			executions++
			return "exited", nil
		},
	})
	outcome := executeToolCall(
		context.Background(),
		QueryParams{
			ToolRegistry: registry,
			CanUseTool: func(
				ctx context.Context,
				_ string,
				_ map[string]any,
				_ *ToolUseContext,
			) (bool, string) {
				SetPlanApprovalDecision(ctx, &decoded)
				return true, ""
			},
			ToolExecutor: func(
				context.Context,
				string,
				string,
			) (string, error) {
				executions++
				return "exited", nil
			},
		},
		nil,
		&ToolUseContext{
			PlanMode: true,
			Options: &ToolUseOptions{
				PermissionMode: permission.ModePlan,
			},
		},
		toolCall(request.RequestID, "ExitPlanMode"),
		nil,
	)
	if executions != 0 ||
		outcome == nil ||
		outcome.Result == nil ||
		!strings.Contains(
			outcome.Result.Content,
			"requires a structured Plan approval decision",
		) {
		t.Fatalf(
			"serialized typed approval executions=%d outcome=%#v",
			executions,
			outcome,
		)
	}
}

func TestP200TypedOutcomesAndLegacyCompatibility(t *testing.T) {
	t.Run("generic allow is not approval", func(t *testing.T) {
		request, _ := p200ReviewRequest(t, "# Plan\n")
		result := normalizePlanApprovalDecision(
			request,
			PermissionInteractionResult{Decision: PermissionAllowOnce},
		)
		if result.Allowed() ||
			result.PlanApproval == nil ||
			result.PlanApproval.Outcome != PlanApprovalCancel ||
			!strings.Contains(result.Message, "structured") {
			t.Fatalf("generic allow = %#v", result)
		}
	})

	t.Run("revise preserves feedback and stays non-approve", func(t *testing.T) {
		request, _ := p200ReviewRequest(t, "# Plan\n")
		result := normalizePlanApprovalDecision(
			request,
			PermissionInteractionResult{
				Decision: PermissionAllowOnce,
				PlanApproval: &PlanApprovalDecision{
					RequestID:    request.RequestID,
					PlanRevision: request.PlanRevision,
					Outcome:      PlanApprovalRevise,
					Feedback:     "cover rollback",
				},
			},
		)
		if result.Allowed() ||
			result.PlanApproval == nil ||
			result.PlanApproval.Outcome != PlanApprovalRevise ||
			result.PlanApproval.Feedback != "cover rollback" ||
			!strings.Contains(result.Message, "cover rollback") {
			t.Fatalf("revise result = %#v", result)
		}
	})

	t.Run("cancel and timeout remain non-approve", func(t *testing.T) {
		request, _ := p200ReviewRequest(t, "# Plan\n")
		result := normalizePlanApprovalDecision(
			request,
			PermissionInteractionResult{
				Decision: PermissionTimedOut,
				PlanApproval: &PlanApprovalDecision{
					RequestID:    request.RequestID,
					PlanRevision: request.PlanRevision,
					Outcome:      PlanApprovalCancel,
				},
			},
		)
		if result.Decision != PermissionTimedOut ||
			result.PlanApproval == nil ||
			result.PlanApproval.Outcome != PlanApprovalCancel ||
			result.PlanApproval.Approved {
			t.Fatalf("timeout result = %#v", result)
		}
	})

	t.Run("legacy approval only accepts unchanged initial bytes", func(t *testing.T) {
		request, path := p200ReviewRequest(t, "# Initial\n")
		legacy := func() PermissionInteractionResult {
			return PermissionInteractionResult{
				Decision: PermissionAllowOnce,
				PlanApproval: &PlanApprovalDecision{
					RequestID:    request.RequestID,
					PlanRevision: request.PlanRevision,
					Approved:     true,
					TargetMode:   permission.ModeDefault,
				},
			}
		}
		legacyJSON, err := json.Marshal(legacy())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(legacyJSON), `"Approved":true`) {
			t.Fatalf("legacy wire decision missing Approved: %s", legacyJSON)
		}
		var decoded PermissionInteractionResult
		if err := json.Unmarshal(legacyJSON, &decoded); err != nil {
			t.Fatal(err)
		}
		unchanged := normalizePlanApprovalDecision(request, decoded)
		if !unchanged.Allowed() ||
			unchanged.PlanApproval == nil ||
			unchanged.PlanApproval.Outcome != PlanApprovalApprove ||
			unchanged.PlanApproval.Approved {
			t.Fatalf("unchanged legacy approval = %#v", unchanged)
		}
		normalizedJSON, err := json.Marshal(unchanged)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(normalizedJSON), `"Approved"`) {
			t.Fatalf(
				"normalized decision re-emitted deprecated Approved: %s",
				normalizedJSON,
			)
		}
		if err := os.WriteFile(path, []byte("# Changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := normalizePlanApprovalDecision(request, legacy())
		if result.Allowed() || !strings.Contains(result.Message, "stale") {
			t.Fatalf("changed legacy approval = %#v", result)
		}
	})

	t.Run("bypass requires explicit confirmation", func(t *testing.T) {
		request, _ := p200ReviewRequest(t, "# Plan\n")
		result := normalizePlanApprovalDecision(
			request,
			p200TypedApproval(
				request,
				request.InitialPlanDigest,
				permission.ModeBypassPermissions,
				false,
			),
		)
		if result.Allowed() || !strings.Contains(result.Message, "confirmation") {
			t.Fatalf("unconfirmed bypass = %#v", result)
		}
	})
}

func TestP200ApprovalSupportsEveryNonPlanReturnMode(t *testing.T) {
	modes := []permission.Mode{
		permission.ModeDefault,
		permission.ModeAcceptEdits,
		permission.ModeBypassPermissions,
		permission.ModeDontAsk,
		permission.ModeAuto,
		permission.ModeBubble,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			request, _ := p200ReviewRequest(t, "# Plan\n")
			result := normalizePlanApprovalDecision(
				request,
				p200TypedApproval(
					request,
					request.InitialPlanDigest,
					mode,
					mode == permission.ModeBypassPermissions,
				),
			)
			if !result.Allowed() ||
				result.PlanApproval == nil ||
				result.PlanApproval.TargetMode != mode {
				t.Fatalf("target %q result = %#v", mode, result)
			}
		})
	}
}

func TestP200IdlePlanAbandonRestoresExactReturnMode(t *testing.T) {
	modes := []permission.Mode{
		permission.ModeDefault,
		permission.ModeAcceptEdits,
		permission.ModeBypassPermissions,
		permission.ModeDontAsk,
		permission.ModeAuto,
		permission.ModeBubble,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			eng := NewQueryEngine(QueryEngineConfig{
				SessionID:      "return-" + string(mode),
				CWD:            t.TempDir(),
				PermissionMode: mode,
			})
			t.Cleanup(eng.Close)
			if err := eng.SetPermissionMode(permission.ModePlan); err != nil {
				t.Fatal(err)
			}
			if state := eng.PlanState(); state.ReturnMode != mode ||
				state.Phase != PlanPhaseActive {
				t.Fatalf("active Plan state = %#v", state)
			}
			if err := eng.SetPermissionMode(permission.ModeDefault); err != nil {
				t.Fatal(err)
			}
			if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
				state.ReturnMode != mode ||
				eng.PermissionMode() != mode {
				t.Fatalf("abandon restored state=%#v mode=%q", state, eng.PermissionMode())
			}
		})
	}
}

func TestP200UserConfirmedBypassCanLeaveIdlePlan(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "confirmed-bypass",
		CWD:            t.TempDir(),
		PermissionMode: permission.ModeDefault,
	})
	t.Cleanup(eng.Close)

	if err := eng.SetPermissionMode(permission.ModePlan); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetPermissionModeConfirmed(
		permission.ModeBypassPermissions,
		false,
	); err == nil || !strings.Contains(err.Error(), "explicit risk confirmation") {
		t.Fatalf("unconfirmed bypass error = %v", err)
	}
	if state := eng.PlanState(); state.Phase != PlanPhaseActive ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("unconfirmed bypass mutated state=%#v mode=%q", state, eng.PermissionMode())
	}

	if err := eng.SetPermissionModeConfirmed(
		permission.ModeBypassPermissions,
		true,
	); err != nil {
		t.Fatal(err)
	}
	if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
		state.ReturnMode != permission.ModeDefault ||
		eng.PermissionMode() != permission.ModeBypassPermissions {
		t.Fatalf("confirmed bypass state=%#v mode=%q", state, eng.PermissionMode())
	}
}

func TestP200UserConfirmedTransitionCannotTargetAnotherMode(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "confirmed-target",
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
	})
	t.Cleanup(eng.Close)

	_, changed, err := eng.applyPlanTransition(planTransitionRequest{
		Source:    planTransitionUserConfirmed,
		RequestID: "confirmed-target-request",
		Mode:      permission.ModeDefault,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot target") {
		t.Fatalf("user-confirmed non-bypass error = %v", err)
	}
	if changed || eng.PermissionMode() != permission.ModePlan ||
		eng.PlanState().Phase != PlanPhaseActive {
		t.Fatalf("rejected target mutated mode=%q state=%#v", eng.PermissionMode(), eng.PlanState())
	}
}

func TestP200UserConfirmedBypassStillLosesOwnedPlanBoundaries(t *testing.T) {
	t.Run("active turn", func(t *testing.T) {
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID:      "confirmed-active",
			CWD:            t.TempDir(),
			PermissionMode: permission.ModePlan,
		})
		t.Cleanup(eng.Close)
		if _, err := eng.beginPlanTurn("active-turn"); err != nil {
			t.Fatal(err)
		}
		defer eng.endPlanTurn("active-turn")

		err := eng.SetPermissionModeConfirmed(
			permission.ModeBypassPermissions,
			true,
		)
		if !errors.Is(err, ErrPlanTransitionInFlight) {
			t.Fatalf("active turn confirmed bypass error = %v", err)
		}
	})

	t.Run("awaiting approval", func(t *testing.T) {
		p200PreparePlan(t, "confirmed-approval", "", "# Plan")
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID:      "confirmed-approval",
			CWD:            t.TempDir(),
			PermissionMode: permission.ModePlan,
		})
		t.Cleanup(eng.Close)
		if _, err := eng.beginPlanTurn("approval-turn"); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.beginPlanApproval("approval-request", nil); err != nil {
			t.Fatal(err)
		}
		eng.endPlanTurn("approval-turn")

		err := eng.SetPermissionModeConfirmed(
			permission.ModeBypassPermissions,
			true,
		)
		if !errors.Is(err, ErrPlanTransitionInFlight) {
			t.Fatalf("awaiting approval confirmed bypass error = %v", err)
		}
	})
}

func TestP200ExternalModeChangeCannotBypassActiveOrAwaitingBoundary(t *testing.T) {
	path := p200PreparePlan(t, "boundary-session", "", "# Plan")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "boundary-session",
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
	})
	t.Cleanup(eng.Close)
	if _, err := eng.beginPlanTurn("boundary-turn"); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetPermissionMode(permission.ModeDefault); !errors.Is(
		err,
		ErrPlanTransitionInFlight,
	) {
		t.Fatalf("active turn external change error = %v", err)
	}
	request, err := eng.beginPlanApproval("boundary-approval", nil)
	if err != nil {
		t.Fatal(err)
	}
	eng.endPlanTurn("boundary-turn")
	if err := eng.SetPermissionMode(permission.ModeDefault); !errors.Is(
		err,
		ErrPlanTransitionInFlight,
	) {
		t.Fatalf("awaiting approval external change error = %v", err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseAwaitingApproval ||
		state.ApprovalRequestID != request.RequestID ||
		state.ApprovalInitialDigest != PlanBytesDigest([]byte("# Plan")) ||
		state.PlanFileIdentity != path ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("external change mutated awaiting state=%#v mode=%q", state, eng.PermissionMode())
	}
}

func TestP200PersistedPlanReturnModeMatrix(t *testing.T) {
	modes := []permission.Mode{
		permission.ModeDefault,
		permission.ModeAcceptEdits,
		permission.ModeBypassPermissions,
		permission.ModeDontAsk,
		permission.ModeAuto,
		permission.ModeBubble,
	}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			state, restoredMode, warnings := restorePersistedPlanState(
				&session.PersistedPlanState{
					Version: session.PersistedPlanStateVersion,
					Phase:   string(PlanPhaseActive),
					PlanFileIdentity: filepath.Join(
						t.TempDir(),
						".claude",
						"plans",
						"persisted.md",
					),
					ReturnMode: string(mode),
					Revision:   3,
				},
				string(permission.ModePlan),
				"persisted",
				"",
				permission.ModeDefault,
				false,
			)
			if state.ReturnMode != mode ||
				state.Phase != PlanPhaseActive ||
				restoredMode != permission.ModePlan ||
				containsSessionWarning(warnings, "return mode") {
				t.Fatalf(
					"restored %q state=%#v mode=%q warnings=%#v",
					mode,
					state,
					restoredMode,
					warnings,
				)
			}
		})
	}
}

func p200ReviewRequest(
	t *testing.T,
	content string,
) (*PlanApprovalRequest, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	data := []byte(content)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &PlanApprovalRequest{
		RequestID:         "review-request",
		PlanRevision:      7,
		PlanFileIdentity:  path,
		InitialPlanDigest: PlanBytesDigest(data),
		ReturnMode:        permission.ModeDefault,
	}, path
}

func p200TypedApproval(
	request *PlanApprovalRequest,
	reviewedDigest string,
	target permission.Mode,
	confirmed bool,
) PermissionInteractionResult {
	return PermissionInteractionResult{
		Decision: PermissionAllowOnce,
		PlanApproval: &PlanApprovalDecision{
			RequestID:          request.RequestID,
			PlanRevision:       request.PlanRevision,
			Outcome:            PlanApprovalApprove,
			ReviewedPlanDigest: reviewedDigest,
			TargetMode:         target,
			Confirmed:          confirmed,
		},
	}
}
