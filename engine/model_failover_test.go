package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
)

func TestP294CoordinatorSkipsCandidatesWithoutConsumingBudgets(t *testing.T) {
	chain := p294Chain()
	chain.MaxSwitches = 3
	chain.Alternates = []provider.FailoverCandidateSnapshot{
		{
			ProfileID: "primary",
			Call:      chain.Primary,
		},
		{
			ProfileID:     "no-image",
			AdmissionCode: "capability_image",
		},
		{
			ProfileID: "alternate",
			Call:      chain.Alternates[0].Call,
		},
		{
			ProfileID: "later",
			Call: provider.RoleCallSnapshot{
				Role:      "main",
				Selector:  "later",
				ProfileID: "later",
			},
		},
	}
	coordinator := newP294Coordinator(t, "tui", chain)
	var events []QueryEvent
	yield := func(event QueryEvent) { events = append(events, event) }
	first, ok := coordinator.next(yield)
	if !ok || first.candidate.profileID != "primary" {
		t.Fatalf("primary attempt = %#v, ok=%t", first, ok)
	}
	first.outputOffered = true
	candidate, canSwitch := coordinator.nextSwitchCandidate(
		context.Background(),
		first,
		execution.ModelFailureOverloaded,
		nil,
		yield,
	)
	if !canSwitch {
		t.Fatal("valid candidate after skipped entries was not admitted")
	}
	coordinator.discard(yield, first, execution.ModelFailureOverloaded)
	second := coordinator.startCandidate(yield, candidate, true)
	if second.candidate.profileID != "alternate" {
		t.Fatalf("alternate attempt = %#v", second)
	}
	if coordinator.switches != 1 || coordinator.budget.ProviderCalls() != 0 {
		t.Fatalf(
			"skips consumed budget: switches=%d calls=%d",
			coordinator.switches,
			coordinator.budget.ProviderCalls(),
		)
	}
	var codes []string
	tombstone := -1
	secondStart := -1
	for index, event := range events {
		if event.Type == EventModelAttempt &&
			event.ModelAttempt != nil &&
			event.ModelAttempt.Phase == ModelAttemptCandidateSkipped {
			codes = append(codes, event.ModelAttempt.AdmissionCode)
		}
		if event.Type == EventTombstone {
			tombstone = index
		}
		if event.Type == EventModelAttempt &&
			event.ModelAttempt != nil &&
			event.ModelAttempt.Phase == ModelAttemptStarted &&
			event.ModelAttempt.AttemptIndex == 1 {
			secondStart = index
		}
	}
	if !equalStrings(codes, []string{"duplicate", "capability_image"}) {
		t.Fatalf("candidate skip codes = %#v", codes)
	}
	if tombstone < 0 || secondStart < 0 || tombstone >= secondStart {
		t.Fatalf(
			"attempt order tombstone=%d second_start=%d events=%#v",
			tombstone,
			secondStart,
			events,
		)
	}
}

func TestP294CoordinatorKeepsOutputWhenNoCandidateCanStart(t *testing.T) {
	chain := p294Chain()
	chain.Alternates = []provider.FailoverCandidateSnapshot{{
		ProfileID:     "incompatible",
		AdmissionCode: "context_window",
	}}
	coordinator := newP294Coordinator(t, "tui", chain)
	var events []QueryEvent
	yield := func(event QueryEvent) { events = append(events, event) }
	attempt, ok := coordinator.next(yield)
	if !ok {
		t.Fatal("primary attempt was not admitted")
	}
	attempt.outputOffered = true
	_, canSwitch := coordinator.nextSwitchCandidate(
		context.Background(),
		attempt,
		execution.ModelFailureOverloaded,
		nil,
		yield,
	)
	if canSwitch {
		t.Fatal("incompatible-only chain opened a switch window")
	}
	for _, event := range events {
		if event.Type == EventTombstone {
			t.Fatal("visible output was retracted without a new admitted attempt")
		}
	}
	if got := coordinator.terminalDisposition(attempt); got !=
		ModelAttemptOutputCommitted {
		t.Fatalf("terminal disposition = %q", got)
	}
}

func TestP294OnlyOverloadCanSwitchProfiles(t *testing.T) {
	terminalClasses := []execution.ModelFailureClass{
		execution.ModelFailureRateLimited,
		execution.ModelFailureTimeout,
		execution.ModelFailureTransportUnavailable,
		execution.ModelFailureAuthentication,
		execution.ModelFailureAuthorization,
		execution.ModelFailureInvalidRequest,
		execution.ModelFailurePolicyRejected,
		execution.ModelFailureContextTooLong,
		execution.ModelFailureCancelled,
		execution.ModelFailureUsageAmbiguous,
		execution.ModelFailureBudgetExhausted,
		execution.ModelFailureUnknown,
	}
	for _, failure := range terminalClasses {
		t.Run(string(failure), func(t *testing.T) {
			coordinator := newP294Coordinator(t, "tui", p294Chain())
			attempt, ok := coordinator.next(func(QueryEvent) {})
			if !ok {
				t.Fatal("primary attempt was not admitted")
			}
			_, canSwitch := coordinator.nextSwitchCandidate(
				context.Background(),
				attempt,
				failure,
				nil,
				func(QueryEvent) {},
			)
			if canSwitch {
				t.Fatalf("failure class %q opened a switch window", failure)
			}
		})
	}
}

func TestP294NewRequestRestartsFromCurrentPrimary(t *testing.T) {
	for request := 0; request < 2; request++ {
		coordinator := newP294Coordinator(t, "headless", p294Chain())
		attempt, ok := coordinator.next(func(QueryEvent) {})
		if !ok ||
			attempt.index != 0 ||
			attempt.candidate.profileID != "primary" ||
			coordinator.switches != 0 {
			t.Fatalf(
				"request %d first attempt = %#v switches=%d",
				request,
				attempt,
				coordinator.switches,
			)
		}
	}
}

func TestP294AllRoutesTerminalSummaryIsBoundedAndRedacted(t *testing.T) {
	coordinator := newP294Coordinator(t, "headless", p294Chain())
	attempt, ok := coordinator.next(func(QueryEvent) {})
	if !ok {
		t.Fatal("primary attempt was not admitted")
	}
	err := coordinator.safeTerminalError(
		attempt,
		execution.ModelFailureOverloaded,
	)
	message := err.Error()
	if strings.Contains(message, "https://") ||
		strings.Contains(message, "secret") ||
		!strings.Contains(message, "provider calls") ||
		!strings.Contains(message, "switches") {
		t.Fatalf("unsafe or incomplete terminal summary: %q", message)
	}
}

func newP294Coordinator(
	t *testing.T,
	entrypoint string,
	chain provider.FailoverChainSnapshot,
) *modelAttemptCoordinator {
	t.Helper()
	params := p294FailoverParams(
		entrypoint,
		chain,
		func(
			context.Context,
			model.BaseChatModel,
			[]*schema.Message,
			*schema.Message,
			[]*schema.ToolInfo,
			execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			return nil, nil
		},
	)
	coordinator, err := newModelAttemptCoordinator(
		params,
		modelFailoverRequest{messages: params.Messages},
		"round",
		p294IDs(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}
