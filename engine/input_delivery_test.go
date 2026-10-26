package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestNextTurnDeliveryNeverClaimsInsideActiveTurn(t *testing.T) {
	for _, boundary := range []struct {
		name                    string
		includeUser, allowLater bool
	}{
		{"prepare", false, false},
		{"after_tool", true, false},
		{"after_sleep", true, true},
		{"initial_prepare", false, true},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			c := newTestRuntimeInputCoordinator(t, "delivery-test", "")
			var item RuntimeItem
			if err := json.Unmarshal([]byte(`{"id":"next-turn","kind":"user_prompt","priority":"next","delivery":"next_turn","scope":{"session_id":"delivery-test"},"user_prompt":{"prompt":"only after this task"}}`), &item); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Enqueue(item); err != nil {
				t.Fatal(err)
			}
			claimed, err := c.ClaimSafePoint(item.Scope, boundary.includeUser, boundary.allowLater)
			if err != nil {
				t.Fatal(err)
			}
			if len(claimed) != 0 {
				t.Fatalf("next-turn input was claimed at %s: %#v", boundary.name, claimed)
			}
			idle, ok, err := c.ClaimNextIdle(item.Scope)
			if err != nil || !ok || idle.ID != item.ID {
				t.Fatalf("idle claim = %#v, %v, %v", idle, ok, err)
			}
		})
	}
}

type deliveryBoundaryModel struct {
	queueDrainModel
	inputs [][]*schema.Message
}

func (m *deliveryBoundaryModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	return m.queueDrainModel.Stream(ctx, input, opts...)
}

func TestPromptDeliveryRealToolRoundIsolation(t *testing.T) {
	c := newTestRuntimeInputCoordinator(t, "delivery-tool-round", "")
	mdl := &deliveryBoundaryModel{}
	maxTurns := 5
	params := queueTestParams("delivery-tool-round", mdl, c, &maxTurns)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	params.ToolExecutor = func(context.Context, string, string) (string, error) {
		close(entered)
		select {
		case <-release:
			return "finished existing tool", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	done := make(chan Terminal, 1)
	go func() { done <- Query(ctx, params, func(QueryEvent) {}) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("tool was not reached")
	}
	for _, input := range []struct {
		id   string
		mode PromptDeliveryMode
		text string
	}{
		{"correct", PromptDeliverySteer, "APPLY_CORRECTION"},
		{"followup", PromptDeliveryNextTurn, "ONLY_NEXT_INVOCATION"},
	} {
		if _, err := c.Enqueue(RuntimeItem{ID: input.id, Kind: RuntimeItemUserPrompt, Delivery: input.mode, UserPrompt: &RuntimeUserPrompt{Prompt: input.text}}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	terminal := <-done
	if terminal.Reason != TerminalCompleted || len(mdl.inputs) != 2 {
		t.Fatalf("terminal = %#v calls=%d", terminal, len(mdl.inputs))
	}
	var texts strings.Builder
	for _, msg := range mdl.inputs[1] {
		texts.WriteString(msg.Content)
	}
	if !strings.Contains(texts.String(), "APPLY_CORRECTION") || strings.Contains(texts.String(), "ONLY_NEXT_INVOCATION") {
		t.Fatalf("model saw incorrect delivery: %s", texts.String())
	}
	pending := c.Snapshot(c.scope)
	if len(pending) != 1 || pending[0].ID != "followup" {
		t.Fatalf("next-turn state = %#v", pending)
	}
}

func TestPromptDeliveryStatusesRequireTranscriptConfirmation(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	eng := newP302bTestEngine(t, "delivery-status", dir, cwd, &captureInputModel{}, DefaultPromptCapabilityResolver())
	queued, err := eng.EnqueuePromptInputWithDelivery(context.Background(), "delivery status", NewUntrustedPromptInput(NewPromptTextPart("delivery status")), PromptDeliveryNextTurn)
	if err != nil {
		t.Fatal(err)
	}
	assertState := func(want PromptDeliveryStatus) {
		t.Helper()
		states, err := eng.QueuedPromptDeliveryStatuses([]string{queued.ID, "absent"})
		if err != nil || states[queued.ID] != want || states["absent"] != PromptDeliveryUnknown {
			t.Fatalf("states=%#v err=%v want=%s", states, err, want)
		}
	}
	assertState(PromptDeliveryPending)
	item, ok, err := eng.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim=%v,%v", ok, err)
	}
	assertState(PromptDeliveryProcessing)
	events, _ := eng.SubmitRuntimeItem(context.Background(), item)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal=%#v", terminal)
	}
	assertState(PromptDeliveryDelivered)
	eng.Close()
	eng = newP302bTestEngine(t, "delivery-status", dir, cwd, &captureInputModel{}, DefaultPromptCapabilityResolver())
	assertState(PromptDeliveryDelivered)
	if pending := eng.RuntimeItems(); len(pending) != 0 {
		t.Fatal("replay requeued an already delivered input")
	}
}

func TestPromptDeliveryCompatibilityAndIdentity(t *testing.T) {
	c := newTestRuntimeInputCoordinator(t, "delivery-modes", "")
	for _, mode := range []PromptDeliveryMode{"", PromptDeliverySteer, PromptDeliveryNextTurn} {
		item := RuntimeItem{
			ID: "input-" + string(mode), Kind: RuntimeItemUserPrompt,
			Delivery: mode, UserPrompt: &RuntimeUserPrompt{Prompt: "instruction"},
		}
		accepted, err := c.Enqueue(item)
		if err != nil {
			t.Fatal(err)
		}
		if mode != "" && accepted.Version != runtimeItemDeliveryVersion {
			t.Fatal("explicit delivery lacks downgrade fence")
		}
	}
	prepare, err := c.ClaimSafePoint(c.scope, false, false)
	if err != nil || len(prepare) != 1 || prepare[0].Delivery != PromptDeliverySteer {
		t.Fatalf("prepare = %#v, %v", prepare, err)
	}
	afterTool, err := c.ClaimSafePoint(c.scope, true, true)
	if err != nil || len(afterTool) != 1 || afterTool[0].Delivery != "" {
		t.Fatalf("legacy claim = %#v, %v", afterTool, err)
	}
	idle, ok, err := c.ClaimNextIdle(c.scope)
	if err != nil || !ok || idle.Delivery != PromptDeliveryNextTurn {
		t.Fatalf("idle = %#v, %v, %v", idle, ok, err)
	}
	for _, item := range []RuntimeItem{
		{ID: "invalid", Kind: RuntimeItemUserPrompt, Delivery: "invalid", UserPrompt: &RuntimeUserPrompt{Prompt: "text"}},
		{ID: "wrong-kind", Kind: RuntimeItemSteering, Delivery: PromptDeliveryNextTurn, UserPrompt: &RuntimeUserPrompt{Prompt: "text"}},
	} {
		if _, err := c.Enqueue(item); err == nil {
			t.Fatalf("accepted invalid delivery: %#v", item)
		}
	}

	eng := newP302bTestEngine(t, "delivery-identity", t.TempDir(), t.TempDir(), &captureInputModel{}, DefaultPromptCapabilityResolver())
	input := UserTurnInput{Prompt: "same payload", Delivery: PromptDeliverySteer}
	first, err := eng.EnqueueUserInputWithID("retry-id", input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng.EnqueueUserInputWithID("retry-id", input)
	if err != nil || first.ID != second.ID {
		t.Fatalf("idempotent retry = %#v, %v", second, err)
	}
	input.Delivery = PromptDeliveryNextTurn
	_, err = eng.EnqueueUserInputWithID("retry-id", input)
	var conflict *RuntimeInputConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("delivery conflict = %v", err)
	}
}

func TestPromptDeliveryDurableRestartAndClaimEditRace(t *testing.T) {
	for _, rich := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "image"}[rich], func(t *testing.T) {
			dir, cwd := t.TempDir(), t.TempDir()
			eng := newP302bTestEngine(t, "delivery-restart", dir, cwd, &captureInputModel{}, DefaultPromptCapabilityResolver())
			parts := []UntrustedPromptPart{NewPromptTextPart("retained draft")}
			if rich {
				parts = append(parts, NewPromptImagePart(testUserImagePNGBase64, "image/png", PromptImageDetailAuto))
			}
			queued, err := eng.EnqueuePromptInputWithDelivery(context.Background(), "safe preview", NewUntrustedPromptInput(parts...), PromptDeliveryNextTurn)
			if err != nil {
				t.Fatal(err)
			}
			eng.Close()
			eng = newP302bTestEngine(t, "delivery-restart", dir, cwd, &captureInputModel{}, DefaultPromptCapabilityResolver())
			snapshots, err := eng.QueuedPromptInputs()
			if err != nil || len(snapshots) != 1 || snapshots[0].Delivery != PromptDeliveryNextTurn {
				t.Fatalf("restart = %#v, %v", snapshots, err)
			}
			if claimed, err := eng.inputCoordinator.ClaimSafePoint(eng.runtimeInputScope(), true, true); err != nil || len(claimed) != 0 {
				t.Fatalf("active after restart = %#v, %v", claimed, err)
			}
			var wg sync.WaitGroup
			var claimOK, editOK bool
			var claimErr, editErr error
			var draft QueuedPromptDraft
			start := make(chan struct{})
			wg.Add(2)
			go func() { defer wg.Done(); <-start; _, claimOK, claimErr = eng.ClaimNextRuntimeItem() }()
			go func() { defer wg.Done(); <-start; draft, editOK, editErr = eng.EditQueuedPrompt(queued.ID) }()
			close(start)
			wg.Wait()
			defer clearQueuedPromptDraft(&draft)
			if claimErr != nil || editErr != nil || claimOK == editOK {
				t.Fatalf("race: claimed=%v/%v edited=%v/%v", claimOK, claimErr, editOK, editErr)
			}
			if claimOK {
				if cancelled, err := eng.CancelQueuedPrompt(queued.ID); err != nil || cancelled {
					t.Fatalf("cancel claimed = %v, %v", cancelled, err)
				}
			} else if len(draft.Parts) != len(parts) || draft.Parts[0].Text != "retained draft" {
				t.Fatalf("edited draft = %#v", draft)
			}
		})
	}
}
