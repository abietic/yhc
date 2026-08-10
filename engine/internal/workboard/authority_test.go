package workboard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAuthorityCodecStrictRoundTripAndClone(t *testing.T) {
	record := validAuthorityRecordFixture()
	data, err := EncodeAuthorityRecord(record)
	if err != nil {
		t.Fatalf("encode authority record: %v", err)
	}
	decoded, err := DecodeAuthorityRecord(data, record.SessionID)
	if err != nil {
		t.Fatalf("decode authority record: %v", err)
	}
	decoded.Board.Items[0].Title = "mutated"
	decoded.Compatibility.Tasks[0].Metadata[0] = '['
	decoded.Compatibility.TodoScopes[0].CurrentItemIDs[0] = "mutated"

	again, err := DecodeAuthorityRecord(data, record.SessionID)
	if err != nil {
		t.Fatalf("decode authority record again: %v", err)
	}
	if again.Board.Items[0].Title != "task" {
		t.Fatalf("decoded board alias leaked: %+v", again.Board.Items[0])
	}
	if string(again.Compatibility.Tasks[0].Metadata) != `{"key":"value"}` {
		t.Fatalf(
			"decoded metadata alias leaked: %s",
			again.Compatibility.Tasks[0].Metadata,
		)
	}
	if again.Compatibility.TodoScopes[0].CurrentItemIDs[0] != "todo:1" {
		t.Fatalf(
			"decoded Todo IDs alias leaked: %+v",
			again.Compatibility.TodoScopes[0],
		)
	}
}

func TestAuthorityCodecRejectsStrictReaderViolations(t *testing.T) {
	record := validAuthorityRecordFixture()
	data, err := EncodeAuthorityRecord(record)
	if err != nil {
		t.Fatalf("encode authority record: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	raw["unknown"] = true
	unknown, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal unknown fixture: %v", err)
	}

	tests := []struct {
		name      string
		data      []byte
		sessionID string
	}{
		{name: "unknown", data: unknown, sessionID: record.SessionID},
		{
			name:      "trailing",
			data:      append(append([]byte(nil), data...), []byte("{}")...),
			sessionID: record.SessionID,
		},
		{name: "session mismatch", data: data, sessionID: "other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeAuthorityRecord(
				test.data,
				test.sessionID,
			); err == nil {
				t.Fatal("expected strict authority decode failure")
			}
		})
	}

	record.Version = 99
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal version fixture: %v", err)
	}
	if _, err := DecodeAuthorityRecord(data, record.SessionID); err == nil {
		t.Fatal("expected unsupported record version")
	}
}

func TestAuthorityCodecRejectsCompatibilityAndCanonicalLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AuthorityRecord)
	}{
		{
			name: "invalid metadata",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.Tasks[0].Metadata = json.RawMessage("{")
			},
		},
		{
			name: "field limit",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.Tasks[0].Subject = strings.Repeat(
					"x",
					MaxFieldBytes+1,
				)
			},
		},
		{
			name: "unresolved dependency limit",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.Tasks[0].UnresolvedBlocks = make(
					[]string,
					MaxDependencyRefs+1,
				)
				for index := range record.Compatibility.Tasks[0].UnresolvedBlocks {
					record.Compatibility.Tasks[0].UnresolvedBlocks[index] = "missing"
				}
				record.Compatibility.Tasks[0].Blocks = append(
					[]string(nil),
					record.Compatibility.Tasks[0].UnresolvedBlocks...,
				)
			},
		},
		{
			name: "missing canonical dependency target",
			mutate: func(record *AuthorityRecord) {
				record.Board.Items[0].Blocks = []string{"missing"}
				record.Compatibility.Tasks[0].Blocks = []string{"missing"}
			},
		},
		{
			name: "duplicate Todo scope",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.TodoScopes = append(
					record.Compatibility.TodoScopes,
					record.Compatibility.TodoScopes[0],
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validAuthorityRecordFixture()
			test.mutate(&record)
			if _, err := EncodeAuthorityRecord(record); err == nil {
				t.Fatal("expected authority validation failure")
			}
		})
	}
}

func TestAuthorityMarkerAndBackupShapes(t *testing.T) {
	record := validAuthorityRecordFixture()
	marker := AuthorityMarker{
		Version:       AuthorityMarkerVersion,
		SessionID:     record.SessionID,
		MinimumReader: MinimumReaderV2,
	}
	data, err := EncodeAuthorityMarker(marker)
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	if bytes.Contains(data, []byte("board_id")) ||
		bytes.Contains(data, []byte("revision")) {
		t.Fatalf("marker pins mutable authority identity: %s", data)
	}
	decodedMarker, err := DecodeAuthorityMarker(data, record.SessionID)
	if err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	if decodedMarker != marker {
		t.Fatalf("marker mismatch: got %+v want %+v", decodedMarker, marker)
	}

	backup := LegacyBackup{
		Version:       LegacyBackupVersion,
		SessionID:     record.SessionID,
		BoardID:       record.BoardID,
		Board:         record.Board,
		Compatibility: record.Compatibility,
	}
	backupData, err := EncodeLegacyBackup(backup)
	if err != nil {
		t.Fatalf("encode backup: %v", err)
	}
	decodedBackup, err := DecodeLegacyBackup(backupData, record.SessionID)
	if err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	decodedBackup.Compatibility.Tasks[0].Subject = "mutated"
	if backup.Compatibility.Tasks[0].Subject != "task" {
		t.Fatal("backup decode aliases caller state")
	}
}

func TestAuthorityCodecRejectsSplitCompatibilityViews(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AuthorityRecord)
	}{
		{
			name: "task subject differs",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.Tasks[0].Subject = "other"
			},
		},
		{
			name: "unresolved dependency is not projected",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.Tasks[0].UnresolvedBlocks = []string{"missing"}
			},
		},
		{
			name: "next task ID collides",
			mutate: func(record *AuthorityRecord) {
				record.Compatibility.NextTaskID = 1
			},
		},
		{
			name: "metadata is not an object",
			mutate: func(record *AuthorityRecord) {
				raw := json.RawMessage(`["not","an","object"]`)
				record.Compatibility.Tasks[0].Metadata = raw
				record.Board.Items[0].Metadata = append(json.RawMessage(nil), raw...)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validAuthorityRecordFixture()
			test.mutate(&record)
			if _, err := EncodeAuthorityRecord(record); err == nil {
				t.Fatal("split compatibility record unexpectedly encoded")
			}
		})
	}
}

func validAuthorityRecordFixture() AuthorityRecord {
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	return AuthorityRecord{
		Version:   AuthorityRecordVersion,
		SessionID: "session",
		BoardID:   "board",
		Board: Board{
			Revision:   1,
			NextTodoID: 2,
			Items: []WorkItem{
				{
					ID:       "task:1",
					Revision: 1,
					Source: SourcePartition{
						Kind:     "task",
						LegacyID: "1",
					},
					Order:       0,
					Title:       "task",
					Description: "description",
					Status:      StatusPending,
					Metadata:    json.RawMessage(`{"key":"value"}`),
				},
				{
					ID:       "todo:1",
					Revision: 1,
					Source: SourcePartition{
						Kind:      "todo",
						SessionID: "session",
						AgentID:   "agent",
					},
					Order:      1,
					Title:      "todo",
					ActiveForm: "doing todo",
					Status:     StatusPending,
				},
			},
		},
		Compatibility: CompatibilityPayload{
			NextTaskID: 2,
			Tasks: []TaskCompatibility{
				{
					ID:           "1",
					Subject:      "task",
					Description:  "description",
					LegacyStatus: "pending",
					Metadata:     json.RawMessage(`{"key":"value"}`),
					CreatedAt:    now,
					UpdatedAt:    now,
				},
			},
			TodoScopes: []TodoScopeCompatibility{
				{
					SessionID:      "session",
					AgentID:        "agent",
					CurrentItemIDs: []string{"todo:1"},
				},
			},
		},
	}
}
