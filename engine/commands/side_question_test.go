package commands

import (
	"context"
	"testing"
)

type sideQuestionCapability struct{}

func (sideQuestionCapability) SideQuestionAvailability() (bool, string) { return true, "" }

func TestSideQuestionCommandIsReadOnlyActiveOperation(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	cmdCtx := &CommandContext{Engine: sideQuestionCapability{}, Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn}}
	operation, ok := r.DescribeOperation(context.Background(), EntrypointTUI, cmdCtx, "btw", nil)
	if !ok || operation.Command.Availability != AvailabilitySupported {
		t.Fatal("/btw must be available for an explicit side question during an ordinary active turn")
	}
	result, err := r.Dispatch(context.Background(), EntrypointTUI, cmdCtx, `/btw "why did the test fail?"`)
	if err != nil || result.Action != "side_question" || result.Data["question"] != "why did the test fail?" {
		t.Fatalf("side question dispatch = %#v %v", result, err)
	}
	if _, ok := r.DescribeOperation(context.Background(), EntrypointPlain, cmdCtx, "btw", nil); ok {
		t.Fatal("unwired entrypoint advertised /btw")
	}
}
