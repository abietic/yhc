package engine

import (
	"errors"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/commands"
)

func TestP245cNegotiatedACPCapabilityIsExplicit(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability *GoalCapabilityConfig
		want       bool
	}{
		{
			name: "enabled and negotiated",
			capability: &GoalCapabilityConfig{
				Enabled:       true,
				ACPNegotiated: true,
			},
			want: true,
		},
		{
			name: "enabled but not negotiated",
			capability: &GoalCapabilityConfig{
				Enabled: true,
			},
		},
		{
			name: "negotiated but disabled",
			capability: &GoalCapabilityConfig{
				ACPNegotiated: true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			eng := newP241GoalEngine(t, QueryEngineConfig{
				CommandEntrypoint: commands.EntrypointACP,
				GoalCapability:    test.capability,
			})
			available, _ := eng.GoalCommandAvailability()
			if available != test.want {
				t.Fatalf("Goal availability = %v, want %v", available, test.want)
			}
		})
	}
}

func TestP245cGoalControlRequiresExactRevisionAndPersistsOnce(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointACP,
		GoalCapability: &GoalCapabilityConfig{
			Enabled:       true,
			ACPNegotiated: true,
		},
	})
	budget := uint64(10_000)
	created, err := eng.ApplyGoalControl(GoalControlRequest{
		Operation:        GoalControlCreate,
		ExpectedRevision: 0,
		Objective:        "finish the ACP Goal contract",
		TokenBudget:      &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Goal == nil ||
		created.Goal.Revision != 1 ||
		created.Goal.Status != string(goalStatusActive) ||
		!created.RequiresPrompt {
		t.Fatalf("created Goal = %#v", created)
	}

	_, err = eng.ApplyGoalControl(GoalControlRequest{
		Operation:        GoalControlCreate,
		ExpectedRevision: 0,
		Objective:        "duplicate",
		TokenBudget:      &budget,
	})
	var conflict *GoalControlConflictError
	if !errors.As(err, &conflict) ||
		conflict.Current == nil ||
		conflict.Current.Revision != created.Goal.Revision {
		t.Fatalf("duplicate create error = %#v", err)
	}

	paused, err := eng.ApplyGoalControl(GoalControlRequest{
		Operation:        GoalControlPause,
		ExpectedGoalID:   created.Goal.GoalID,
		ExpectedRevision: created.Goal.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Goal == nil ||
		paused.Goal.Status != string(goalStatusPaused) ||
		paused.Goal.Revision != created.Goal.Revision+1 {
		t.Fatalf("paused Goal = %#v", paused)
	}
	_, err = eng.ApplyGoalControl(GoalControlRequest{
		Operation:        GoalControlPause,
		ExpectedGoalID:   created.Goal.GoalID,
		ExpectedRevision: created.Goal.Revision,
	})
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate pause error = %#v", err)
	}
	current, _ := eng.GoalSnapshot()
	if current.Revision != paused.Goal.Revision {
		t.Fatalf("duplicate pause changed revision to %d", current.Revision)
	}
	_, err = eng.ApplyGoalControl(GoalControlRequest{
		Operation:        GoalControlPause,
		ExpectedGoalID:   paused.Goal.GoalID,
		ExpectedRevision: paused.Goal.Revision,
	})
	if !errors.As(err, &conflict) ||
		conflict.Reason != "operation would not change Goal state" {
		t.Fatalf("same-state pause error = %#v", err)
	}
}

func TestP245cConcurrentGoalControlsHaveOneRevisionWinner(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointACP,
		GoalCapability: &GoalCapabilityConfig{
			Enabled:       true,
			ACPNegotiated: true,
		},
	})
	budget := uint64(10_000)
	created, err := eng.ApplyGoalControl(GoalControlRequest{
		Operation:        GoalControlCreate,
		ExpectedRevision: 0,
		Objective:        "serialize concurrent controls",
		TokenBudget:      &budget,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, controlErr := eng.ApplyGoalControl(GoalControlRequest{
				Operation:        GoalControlPause,
				ExpectedGoalID:   created.Goal.GoalID,
				ExpectedRevision: created.Goal.Revision,
			})
			results <- controlErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, conflicts int
	for resultErr := range results {
		if resultErr == nil {
			successes++
			continue
		}
		var conflict *GoalControlConflictError
		if errors.As(resultErr, &conflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected control error = %v", resultErr)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("control results: successes=%d conflicts=%d", successes, conflicts)
	}
}
