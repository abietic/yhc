package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
)

func TestACPToolLifecycleLedgerProjectsCanonicalFactsExactlyOnce(t *testing.T) {
	ledger := newACPToolLifecycleLedger()
	var updates []acpsdk.SessionNotification
	send := func(
		_ context.Context,
		update acpsdk.SessionNotification,
	) error {
		updates = append(updates, update)
		return nil
	}
	sessionID := acpsdk.SessionId("lifecycle-session")
	callID := "call-one"
	events := []*engine.CanonicalProjectionEvent{
		canonicalACPToolEvent(
			engine.CanonicalProjectionToolStart,
			&engine.CanonicalToolPayload{
				ToolCallID: callID,
				ToolName:   "Read",
			},
		),
		canonicalACPToolEvent(
			engine.CanonicalProjectionToolStart,
			&engine.CanonicalToolPayload{
				ToolCallID: callID,
				ToolName:   "Read",
			},
		),
		canonicalACPToolEvent(
			engine.CanonicalProjectionToolInput,
			&engine.CanonicalToolPayload{
				ToolCallID: callID,
				EffectiveInput: json.RawMessage(
					`{"file_path":"/tmp/example.go","limit":2}`,
				),
			},
		),
		canonicalACPToolEvent(
			engine.CanonicalProjectionToolProgress,
			&engine.CanonicalToolPayload{
				ToolCallID: callID,
				Content:    "latest complete snapshot",
			},
		),
		canonicalACPToolEvent(
			engine.CanonicalProjectionToolTerminal,
			&engine.CanonicalToolPayload{
				ToolCallID: callID,
				Outcome:    engine.CanonicalToolOutcomeFailed,
				RawOutput:  json.RawMessage(`"permission denied"`),
			},
		),
		canonicalACPToolEvent(
			engine.CanonicalProjectionToolTerminal,
			&engine.CanonicalToolPayload{
				ToolCallID: callID,
				Outcome:    engine.CanonicalToolOutcomeFailed,
				RawOutput:  json.RawMessage(`"permission denied"`),
			},
		),
	}
	for _, event := range events {
		if err := ledger.project(
			t.Context(),
			sessionID,
			event,
			send,
		); err != nil {
			t.Fatal(err)
		}
	}
	if len(updates) != 4 {
		t.Fatalf("updates = %d, want 4", len(updates))
	}
	start := updates[0].Update.ToolCall
	if start == nil ||
		start.ToolCallId != acpsdk.ToolCallId(callID) ||
		start.Kind != acpsdk.ToolKindRead ||
		start.Status != acpsdk.ToolCallStatusPending {
		t.Fatalf("start = %#v", start)
	}
	input := updates[1].Update.ToolCallUpdate
	if input == nil ||
		input.Status == nil ||
		*input.Status != acpsdk.ToolCallStatusInProgress ||
		len(input.Locations) != 1 ||
		input.Locations[0].Path != "/tmp/example.go" {
		t.Fatalf("input update = %#v", input)
	}
	rawInput, ok := input.RawInput.(map[string]any)
	if !ok || rawInput["file_path"] != "/tmp/example.go" {
		t.Fatalf("raw input = %#v", input.RawInput)
	}
	progress := updates[2].Update.ToolCallUpdate
	if progress == nil ||
		progress.Status == nil ||
		*progress.Status != acpsdk.ToolCallStatusInProgress ||
		len(progress.Content) != 1 ||
		progress.Content[0].Content == nil ||
		progress.Content[0].Content.Content.Text == nil ||
		progress.Content[0].Content.Content.Text.Text !=
			"latest complete snapshot" {
		t.Fatalf("progress update = %#v", progress)
	}
	terminal := updates[3].Update.ToolCallUpdate
	if terminal == nil ||
		terminal.Status == nil ||
		*terminal.Status != acpsdk.ToolCallStatusFailed ||
		terminal.RawOutput != "permission denied" ||
		len(terminal.Content) != 1 ||
		terminal.Content[0].Content == nil ||
		terminal.Content[0].Content.Content.Text == nil ||
		terminal.Content[0].Content.Content.Text.Text !=
			"permission denied" {
		t.Fatalf("terminal update = %#v", terminal)
	}
	snapshot := ledger.snapshot(callID)
	if !snapshot.StartDelivered ||
		!snapshot.InputDelivered ||
		!snapshot.TerminalObserved ||
		!snapshot.TerminalDelivered ||
		!snapshot.LocallySettled ||
		snapshot.DeliveryFailed ||
		snapshot.Outcome != engine.CanonicalToolOutcomeFailed {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestACPToolLifecycleLedgerPermissionStartWinsRace(t *testing.T) {
	ledger := newACPToolLifecycleLedger()
	var mu sync.Mutex
	var updates []acpsdk.SessionNotification
	send := func(
		_ context.Context,
		update acpsdk.SessionNotification,
	) error {
		mu.Lock()
		updates = append(updates, update)
		mu.Unlock()
		return nil
	}
	start := canonicalACPToolEvent(
		engine.CanonicalProjectionToolStart,
		&engine.CanonicalToolPayload{
			ToolCallID: "permission-call",
			ToolName:   "Bash",
		},
	)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		if err := ledger.ensurePermissionVisible(
			t.Context(),
			"permission-session",
			"permission-call",
			"Bash",
			send,
		); err != nil {
			t.Errorf("permission start: %v", err)
		}
	}()
	go func() {
		defer wait.Done()
		if err := ledger.project(
			t.Context(),
			"permission-session",
			start,
			send,
		); err != nil {
			t.Errorf("canonical start: %v", err)
		}
	}()
	wait.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 1 || updates[0].Update.ToolCall == nil {
		t.Fatalf("updates = %#v", updates)
	}
}

func TestACPToolLifecycleLedgerDeduplicatesRequestedAndCanonicalToolNames(
	t *testing.T,
) {
	for _, test := range []struct {
		name            string
		permissionFirst bool
		wantTitle       string
	}{
		{
			name:            "permission canonical name first",
			permissionFirst: true,
			wantTitle:       "Count",
		},
		{
			name:      "committed requested alias first",
			wantTitle: "count_alias",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := newACPToolLifecycleLedger()
			var updates []acpsdk.SessionNotification
			send := func(
				_ context.Context,
				update acpsdk.SessionNotification,
			) error {
				updates = append(updates, update)
				return nil
			}
			project := func() error {
				return ledger.project(
					t.Context(),
					"alias-session",
					canonicalACPToolEvent(
						engine.CanonicalProjectionToolStart,
						&engine.CanonicalToolPayload{
							ToolCallID: "alias-call",
							ToolName:   "count_alias",
						},
					),
					send,
				)
			}
			permission := func() error {
				return ledger.ensurePermissionVisible(
					t.Context(),
					"alias-session",
					"alias-call",
					"Count",
					send,
				)
			}
			if test.permissionFirst {
				if err := permission(); err != nil {
					t.Fatal(err)
				}
				if err := project(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := project(); err != nil {
					t.Fatal(err)
				}
				if err := permission(); err != nil {
					t.Fatal(err)
				}
			}
			if len(updates) != 1 ||
				updates[0].Update.ToolCall == nil ||
				updates[0].Update.ToolCall.Title != test.wantTitle {
				t.Fatalf("updates = %#v", updates)
			}
		})
	}
}

func TestACPToolLifecycleLedgerDeliveryFailureSettlesLocally(t *testing.T) {
	ledger := newACPToolLifecycleLedger()
	deliveryErr := errors.New("client disconnected")
	send := func(
		context.Context,
		acpsdk.SessionNotification,
	) error {
		return deliveryErr
	}
	start := canonicalACPToolEvent(
		engine.CanonicalProjectionToolStart,
		&engine.CanonicalToolPayload{
			ToolCallID: "disconnect-call",
			ToolName:   "Read",
		},
	)
	err := ledger.project(
		t.Context(),
		"disconnect-session",
		start,
		send,
	)
	if !errors.Is(err, deliveryErr) {
		t.Fatalf("start error = %v", err)
	}
	terminal := canonicalACPToolEvent(
		engine.CanonicalProjectionToolTerminal,
		&engine.CanonicalToolPayload{
			ToolCallID: "disconnect-call",
			Outcome:    engine.CanonicalToolOutcomeFailed,
			RawOutput:  json.RawMessage(`"Interrupted by user"`),
		},
	)
	ledger.settleAfterDeliveryFailure(terminal, deliveryErr)
	snapshot := ledger.snapshot("disconnect-call")
	if snapshot.StartDelivered ||
		!snapshot.TerminalObserved ||
		snapshot.TerminalDelivered ||
		!snapshot.LocallySettled ||
		!snapshot.DeliveryFailed ||
		snapshot.Outcome != engine.CanonicalToolOutcomeFailed {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestACPToolLifecycleLedgerUnstartedSyntheticTerminalIsLocalOnly(t *testing.T) {
	ledger := newACPToolLifecycleLedger()
	sends := 0
	terminal := canonicalACPToolEvent(
		engine.CanonicalProjectionToolTerminal,
		&engine.CanonicalToolPayload{
			ToolCallID: "queued-cancel",
			Outcome:    engine.CanonicalToolOutcomeFailed,
			RawOutput:  json.RawMessage(`"Interrupted by user"`),
		},
	)
	if err := ledger.project(
		t.Context(),
		"synthetic-session",
		terminal,
		func(context.Context, acpsdk.SessionNotification) error {
			sends++
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if sends != 0 {
		t.Fatalf("client updates = %d, want 0", sends)
	}
	snapshot := ledger.snapshot("queued-cancel")
	if !snapshot.TerminalObserved ||
		!snapshot.LocallySettled ||
		snapshot.TerminalDelivered {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestACPToolLifecycleLedgerIsSessionIsolated(t *testing.T) {
	left := newACPToolLifecycleLedger()
	right := newACPToolLifecycleLedger()
	start := canonicalACPToolEvent(
		engine.CanonicalProjectionToolStart,
		&engine.CanonicalToolPayload{
			ToolCallID: "same-call",
			ToolName:   "Read",
		},
	)
	send := func(context.Context, acpsdk.SessionNotification) error {
		return nil
	}
	if err := left.project(t.Context(), "left", start, send); err != nil {
		t.Fatal(err)
	}
	if leftSnapshot := left.snapshot("same-call"); !leftSnapshot.StartDelivered {
		t.Fatalf("left snapshot = %#v", leftSnapshot)
	}
	if rightSnapshot := right.snapshot("same-call"); rightSnapshot.StartDelivered {
		t.Fatalf("right snapshot = %#v", rightSnapshot)
	}
}

func canonicalACPToolEvent(
	kind engine.CanonicalProjectionKind,
	tool *engine.CanonicalToolPayload,
) *engine.CanonicalProjectionEvent {
	return &engine.CanonicalProjectionEvent{
		Version: engine.CanonicalProjectionVersion,
		Kind:    kind,
		Tool:    tool,
	}
}
