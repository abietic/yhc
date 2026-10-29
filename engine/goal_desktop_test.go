package engine

import (
	"errors"
	"testing"

	"github.com/abietic/yhc/engine/commands"
)

func TestDesktopGoalUsesSavedRootCapabilityAndExactContinue(t *testing.T) {
	budget := uint64(9007199254740993)
	e := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointAppServer,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true, DefaultTokenBudget: &budget},
	})
	if available, reason := e.GoalCommandAvailability(); !available {
		t.Fatalf("saved AppServer Goal unavailable: %s", reason)
	}
	created, err := e.ApplyGoalControl(GoalControlRequest{Operation: GoalControlCreate, Objective: "finish desktop workflow", TokenBudget: e.GoalDefaultTokenBudget()})
	if err != nil {
		t.Fatal(err)
	}
	if created.Goal.TokenBudget == nil || *created.Goal.TokenBudget != budget || !created.RequiresPrompt {
		t.Fatalf("host default lost: %+v", created)
	}
	req := GoalControlRequest{Operation: GoalControlContinue, ExpectedGoalID: created.Goal.GoalID, ExpectedRevision: created.Goal.Revision}
	continued, err := e.ApplyGoalControl(req)
	if err != nil || !continued.RequiresPrompt || continued.Goal.Revision != created.Goal.Revision {
		t.Fatalf("exact continue = %+v, %v", continued, err)
	}
	paused, err := e.ApplyGoalControl(GoalControlRequest{Operation: GoalControlPause, ExpectedGoalID: req.ExpectedGoalID, ExpectedRevision: req.ExpectedRevision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.ApplyGoalControl(req)
	var conflict *GoalControlConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale continue = %v", err)
	}
	req.ExpectedRevision = paused.Goal.Revision
	if _, err := e.ApplyGoalControl(req); err == nil {
		t.Fatal("paused Goal authorized continue")
	}
	e.mu.Lock()
	e.config.AgentID = "child"
	e.mu.Unlock()
	if available, _ := e.GoalCommandAvailability(); available {
		t.Fatal("child inherited desktop Goal control")
	}
}
