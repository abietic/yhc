package transcript

import (
	"encoding/json"
	"os"
	"testing"
)

func TestP242bGoalUsageRecordRoundTripPreservesSessionUsage(t *testing.T) {
	recorder := NewRecorder("goal-usage", t.TempDir())
	record := p242bTranscriptUsageRecord()
	if err := recorder.RecordGoalUsage(record); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordGoalUsage(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 2 ||
		loaded.GoalUsageRecords[0] != record ||
		loaded.GoalUsageRecords[1] != record {
		t.Fatalf("Goal usage records = %#v", loaded.GoalUsageRecords)
	}
	if loaded.Usage.ResponsesWithMetadata != 0 ||
		loaded.Usage.ResponsesWithoutMetadata != 0 ||
		loaded.Usage.PromptTokens != 0 ||
		loaded.Usage.CompletionTokens != 0 {
		t.Fatalf("Goal ledger changed Session usage diagnostics: %#v", loaded.Usage)
	}
	if len(loaded.Entries) != 2 ||
		loaded.Entries[0].GoalUsage == nil ||
		*loaded.Entries[0].GoalUsage != record {
		t.Fatalf("durable Goal usage entry = %#v", loaded.Entries)
	}
}

func TestP242bGoalUsageLoadPreservesUnknownVersionAndFlagsNegativeCorruption(
	t *testing.T,
) {
	dir := t.TempDir()
	recorder := NewRecorder("goal-usage-corrupt", dir)
	unknown := p242bTranscriptUsageRecord()
	unknown.Version++
	line := `{"timestamp":"2026-07-29T00:00:00Z","kind":"goal-provider-usage","goal_usage":` +
		mustGoalUsageJSON(t, unknown) + "}\n"
	line += `{"timestamp":"2026-07-29T00:00:01Z","kind":"goal-provider-usage","goal_usage":{"version":1,"ledger_revision":2,"goal_id":"goal","objective_revision":1,"root_session_id":"root","root_thread_id":"root","executing_session_id":"root","executing_thread_id":"root","goal_turn_id":"turn","logical_round_id":"round","provider_call_id":"call-2","prompt_tokens":-1}}\n`
	if err := os.WriteFile(recorder.Path(), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 1 ||
		loaded.GoalUsageRecords[0].Version != unknown.Version {
		t.Fatalf("unknown Goal usage version was not preserved: %#v", loaded.GoalUsageRecords)
	}
	if len(loaded.GoalUsageCorruptions) != 1 ||
		loaded.GoalUsageCorruptions[0].Err == nil {
		t.Fatalf("negative Goal usage corruption = %#v", loaded.GoalUsageCorruptions)
	}
}

func p242bTranscriptUsageRecord() GoalUsageRecord {
	return GoalUsageRecord{
		Version:            GoalUsageRecordVersion,
		LedgerRevision:     1,
		GoalID:             "goal",
		ObjectiveRevision:  1,
		RootSessionID:      "root-session",
		RootThreadID:       "root-thread",
		ExecutingSessionID: "root-session",
		ExecutingThreadID:  "root-thread",
		GoalTurnID:         "goal-turn",
		LogicalRoundID:     "logical-round",
		ProviderCallID:     "provider-call",
		PromptTokens:       100,
		CachedPromptTokens: 20,
		CompletionTokens:   30,
		ReasoningTokens:    10,
		TotalTokens:        130,
		BillableTokens:     110,
	}
}

func mustGoalUsageJSON(t *testing.T, record GoalUsageRecord) string {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
