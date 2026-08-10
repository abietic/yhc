package commands

import "testing"

func TestP245bHeadlessGoalEntrypointIsIsolatedFromOrdinaryHeadless(t *testing.T) {
	if !EntrypointsHeadlessGoal.Supports(EntrypointHeadlessGoal) {
		t.Fatal("dedicated headless Goal set does not support its entrypoint")
	}
	if EntrypointsHeadlessGoal.Supports(EntrypointHeadless) {
		t.Fatal("dedicated headless Goal set widened ordinary headless")
	}
	if EntrypointsHeadless.Supports(EntrypointHeadlessGoal) {
		t.Fatal("ordinary headless set widened dedicated headless Goal")
	}
}
