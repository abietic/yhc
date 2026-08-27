package transport

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
)

func TestLifecycleWriterProjectsCanonicalEventAndResult(t *testing.T) {
	timestamp := time.Date(2026, time.August, 24, 1, 2, 3, 4, time.FixedZone("test", 8*60*60))
	var output bytes.Buffer
	writer := NewLifecycleWriter(&output)

	written, err := writer.WriteEvent(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID:   "session-1",
			ThreadID:    "thread-1",
			TurnID:      "turn-1",
			Sequence:    7,
			Timestamp:   timestamp,
			CausationID: "message-1",
		},
		Type: engine.EventCanonicalProjection,
		CanonicalProjection: &engine.CanonicalProjectionEvent{
			Version: engine.CanonicalProjectionVersion,
			Kind:    engine.CanonicalProjectionAssistantDelta,
			Assistant: &engine.CanonicalAssistantPayload{
				MessageID: "message-1",
				Delta:     []byte("hello\n"),
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}
	if !written {
		t.Fatal("canonical assistant event was not written")
	}

	written, err = writer.WriteEvent(engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	})
	if err != nil {
		t.Fatalf("terminal WriteEvent failed: %v", err)
	}
	if written {
		t.Fatal("engine terminal must be owned by the final result record")
	}

	if err := writer.WriteResult(LifecycleResult{
		LifecycleIdentity: LifecycleIdentityFromEnvelope(engine.RuntimeEventEnvelope{
			SessionID: "session-1",
			ThreadID:  "thread-1",
			TurnID:    "turn-1",
			Sequence:  8,
			Timestamp: timestamp.Add(time.Second),
		}),
		Status:         "completed",
		Output:         "hello\n",
		TerminalReason: string(engine.TerminalCompleted),
		ExitCode:       0,
	}); err != nil {
		t.Fatalf("WriteResult failed: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var eventRecord LifecycleRecord
	if err := decoder.Decode(&eventRecord); err != nil {
		t.Fatalf("decode event record: %v", err)
	}
	if eventRecord.SchemaVersion != LifecycleSchemaVersion ||
		eventRecord.Type != LifecycleRecordEvent ||
		eventRecord.Event == nil ||
		eventRecord.Result != nil {
		t.Fatalf("event record = %#v", eventRecord)
	}
	if eventRecord.Event.Kind != string(engine.CanonicalProjectionAssistantDelta) ||
		eventRecord.Event.Family != engine.RuntimeFamilyTurnMessage ||
		eventRecord.Event.SessionID != "session-1" ||
		eventRecord.Event.Sequence != 7 ||
		eventRecord.Event.Timestamp != timestamp.UTC().Format(time.RFC3339Nano) ||
		eventRecord.Event.Assistant == nil ||
		eventRecord.Event.Assistant.MessageID != "message-1" ||
		eventRecord.Event.Assistant.Delta != "hello\n" {
		t.Fatalf("projected event = %#v", eventRecord.Event)
	}

	var resultRecord LifecycleRecord
	if err := decoder.Decode(&resultRecord); err != nil {
		t.Fatalf("decode result record: %v", err)
	}
	if resultRecord.SchemaVersion != LifecycleSchemaVersion ||
		resultRecord.Type != LifecycleRecordResult ||
		resultRecord.Event != nil ||
		resultRecord.Result == nil ||
		resultRecord.Result.Status != "completed" ||
		resultRecord.Result.Output != "hello\n" ||
		resultRecord.Result.Sequence != 8 {
		t.Fatalf("result record = %#v", resultRecord)
	}
	if decoder.More() {
		t.Fatal("unexpected third lifecycle record")
	}
}

func TestLifecycleWriterProjectsOnlyValidatedSafePayloads(t *testing.T) {
	var output bytes.Buffer
	writer := NewLifecycleWriter(&output)

	written, err := writer.WriteEvent(engine.QueryEvent{
		Type: engine.EventCanonicalProjection,
		CanonicalProjection: &engine.CanonicalProjectionEvent{
			Version: engine.CanonicalProjectionVersion,
			Kind:    engine.CanonicalProjectionToolInput,
			Tool: &engine.CanonicalToolPayload{
				ToolCallID:     "tool-1",
				EffectiveInput: json.RawMessage(`{"api_key":"[redacted]","path":"README.md"}`),
			},
		},
	})
	if err != nil || !written {
		t.Fatalf("valid tool event written=%v err=%v", written, err)
	}
	var record LifecycleRecord
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode tool record: %v", err)
	}
	if record.Event == nil || record.Event.Tool == nil ||
		string(record.Event.Tool.EffectiveInput) != `{"api_key":"[redacted]","path":"README.md"}` {
		t.Fatalf("tool record = %#v", record)
	}

	output.Reset()
	written, err = writer.WriteEvent(engine.QueryEvent{
		Type: engine.EventCanonicalProjection,
		CanonicalProjection: &engine.CanonicalProjectionEvent{
			Version: engine.CanonicalProjectionVersion,
			Kind:    engine.CanonicalProjectionToolInput,
			Tool: &engine.CanonicalToolPayload{
				ToolCallID:     "tool-1",
				EffectiveInput: json.RawMessage(`"not-an-object"`),
			},
		},
	})
	if err == nil || written || output.Len() != 0 {
		t.Fatalf("invalid projection written=%v err=%v output=%q", written, err, output.String())
	}

	written, err = writer.WriteEvent(engine.QueryEvent{
		Type: engine.EventCanonicalProjection,
		CanonicalProjection: &engine.CanonicalProjectionEvent{
			Version: engine.CanonicalProjectionVersion,
			Kind:    engine.CanonicalProjectionAssistantDelta,
			Assistant: &engine.CanonicalAssistantPayload{
				MessageID: "message-invalid-utf8",
				Delta:     []byte{0xff},
			},
		},
	})
	if err == nil || written || output.Len() != 0 {
		t.Fatalf("invalid UTF-8 written=%v err=%v output=%q", written, err, output.String())
	}
}
