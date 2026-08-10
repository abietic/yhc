package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestTranscriptEntryIdentityCoversEveryPhysicalAppend(t *testing.T) {
	recorder := NewRecorder("entry-writers", t.TempDir())
	duplicate := []*schema.Message{
		{Role: schema.User, Content: "same"},
		{Role: schema.User, Content: "same"},
	}
	if err := recorder.RecordMessages(duplicate); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record([]*schema.Message{{
		Role: schema.Assistant, Content: "direct record path",
	}}, true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordContentReplacements([]Replacement{{
		ToolUseID: "tool-1", Replacement: "preview",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMetadata("cwd", "/workspace"); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordFileHistorySnapshot(map[string]FileState{
		"/workspace/main.go": {Path: "/workspace/main.go", WasRead: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		duplicate,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 7 {
		t.Fatalf("physical entries = %d, want 7", len(loaded.Entries))
	}
	seen := make(map[string]struct{}, len(loaded.Entries))
	for index, entry := range loaded.Entries {
		if entry.Ordinal != uint64(index) {
			t.Fatalf("entry ordinal = %d, want %d", entry.Ordinal, index)
		}
		if entry.Identity.IsLegacy() ||
			entry.Identity.Version != TranscriptEntryIdentityVersion ||
			entry.Identity.ID == "" {
			t.Fatalf("entry %d identity = %#v", index, entry.Identity)
		}
		if _, duplicateID := seen[entry.Identity.Key()]; duplicateID {
			t.Fatalf("duplicate physical entry identity %q", entry.Identity.Key())
		}
		seen[entry.Identity.Key()] = struct{}{}
	}
	if loaded.Entries[0].Identity.Key() == loaded.Entries[1].Identity.Key() {
		t.Fatal("identical messages shared one durable identity")
	}
	raw, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	wantRevision := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if string(loaded.Revision) != wantRevision {
		t.Fatalf("revision = %q, want %q", loaded.Revision, wantRevision)
	}

	// A reader compiled before P14.2a ignores entry_id while preserving the
	// original message payload.
	firstLine, _, _ := bytes.Cut(raw, []byte("\n"))
	var legacyReader struct {
		Timestamp time.Time       `json:"timestamp"`
		Kind      string          `json:"kind"`
		Message   *schema.Message `json:"message"`
	}
	if err := json.Unmarshal(firstLine, &legacyReader); err != nil {
		t.Fatal(err)
	}
	if legacyReader.Kind != "user" ||
		legacyReader.Message == nil ||
		legacyReader.Message.Content != "same" {
		t.Fatalf("legacy reader payload = %#v", legacyReader)
	}
}

func TestTranscriptEntryIdentitySurvivesSupportedRewrites(t *testing.T) {
	recorder := NewRecorder("entry-rewrite", t.TempDir())
	shared := &schema.Message{Role: schema.User, Content: "same"}
	messages := []*schema.Message{
		shared,
		shared,
		{Role: schema.Assistant, Content: "tail"},
	}
	replacements := []Replacement{{ToolUseID: "tool-1", Replacement: "preview"}}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordContentReplacements(replacements); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	before, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Entries) != 4 {
		t.Fatalf("entries before rewrite = %d", len(before.Entries))
	}
	beforeIDs := entryIdentityKeys(before.Entries)
	beforeTimes := entryTimestamps(before.Entries)

	if err := recorder.ReplaceWithReplacements(before.Messages, replacements); err != nil {
		t.Fatal(err)
	}
	after, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	afterIDs := entryIdentityKeys(after.Entries)
	if fmt.Sprint(afterIDs[:3]) != fmt.Sprint(beforeIDs[:3]) {
		t.Fatalf("rewrite message identities = %v, want %v", afterIDs[:3], beforeIDs[:3])
	}
	if afterIDs[3] == beforeIDs[3] {
		t.Fatal("newly synthesized replacement record reused its prior identity")
	}
	if got := entryTimestamps(after.Entries[:3]); fmt.Sprint(got) != fmt.Sprint(beforeTimes[:3]) {
		t.Fatalf("rewrite message timestamps = %v, want %v", got, beforeTimes[:3])
	}
	if after.Entries[0].Identity.Key() == after.Entries[1].Identity.Key() {
		t.Fatal("rewrite collapsed duplicate message identities")
	}

	oldKeys := make(map[string]struct{}, len(after.Entries))
	for _, key := range entryIdentityKeys(after.Entries) {
		oldKeys[key] = struct{}{}
	}
	tailIdentity := after.Entries[2].Identity.Key()
	summary := &schema.Message{
		Role:    schema.System,
		Content: "compacted summary",
		Extra:   map[string]any{"subtype": "compact_summary"},
	}
	if err := recorder.Replace([]*schema.Message{summary, after.Messages[2]}); err != nil {
		t.Fatal(err)
	}
	compacted, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted.Entries) != 2 {
		t.Fatalf("compacted entries = %d", len(compacted.Entries))
	}
	if _, reused := oldKeys[compacted.Entries[0].Identity.Key()]; reused {
		t.Fatal("newly synthesized compaction summary reused an old identity")
	}
	if compacted.Entries[1].Identity.Key() != tailIdentity {
		t.Fatalf(
			"retained tail identity = %q, want %q",
			compacted.Entries[1].Identity.Key(),
			tailIdentity,
		)
	}

	compactedIDs := entryIdentityKeys(compacted.Entries)
	if err := recorder.AtomicReplace(compacted.Messages); err != nil {
		t.Fatal(err)
	}
	atomic, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIdentityKeys(atomic.Entries); fmt.Sprint(got) != fmt.Sprint(compactedIDs) {
		t.Fatalf("atomic rewrite identities = %v, want %v", got, compactedIDs)
	}
}

func TestTranscriptEntryIdentityRepeatedPointerRewriteKeepsDistinctRecords(t *testing.T) {
	dir := t.TempDir()
	recorder := NewRecorder("shared-pointer", dir)
	shared := &schema.Message{Role: schema.User, Content: "same pointer"}
	if err := recorder.RecordMessages([]*schema.Message{shared, shared}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	observer, err := NewRecorder("shared-pointer", dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	before := entryIdentityKeys(observer.Entries)
	if before[0] == before[1] {
		t.Fatal("append collapsed a repeated message pointer")
	}
	if err := recorder.Replace([]*schema.Message{shared, shared}); err != nil {
		t.Fatal(err)
	}
	after, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIdentityKeys(after.Entries); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("shared-pointer rewrite identities = %v, want %v", got, before)
	}
}

func TestTranscriptEntryIdentitySynthesizedCompactionGetsNewIdentity(t *testing.T) {
	recorder := NewRecorder("synthetic-compaction", t.TempDir())
	stored := &schema.Message{
		Role:    schema.User,
		Content: "same summary",
		Extra:   map[string]any{"subtype": "compact_summary"},
	}
	if err := recorder.RecordMessages([]*schema.Message{stored}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	before, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	synthesized := &schema.Message{
		Role:    schema.User,
		Content: "same summary",
		Extra:   map[string]any{"subtype": "compact_summary"},
	}
	if err := recorder.Replace([]*schema.Message{synthesized}); err != nil {
		t.Fatal(err)
	}
	after, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if after.Entries[0].Identity.Key() == before.Entries[0].Identity.Key() {
		t.Fatal("newly synthesized compaction record reused an old identity")
	}
}

func TestLegacyTranscriptEntryIdentityIsRevisionScoped(t *testing.T) {
	recorder := NewRecorder("legacy-entry", t.TempDir())
	timestamp := time.Date(2026, time.July, 22, 1, 2, 3, 0, time.UTC)
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	for range 2 {
		if err := encoder.Encode(recordEntry{
			Timestamp: timestamp,
			Kind:      "user",
			Message:   &schema.Message{Role: schema.User, Content: "same legacy payload"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(recorder.Path(), raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeBytes := append([]byte(nil), raw.Bytes()...)

	legacy, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	afterLoadBytes, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterLoadBytes) {
		t.Fatal("legacy load rewrote the transcript")
	}
	if len(legacy.Entries) != 2 || legacy.Revision == "" {
		t.Fatalf("legacy result = %#v", legacy)
	}
	for _, entry := range legacy.Entries {
		if !entry.Identity.IsLegacy() || entry.Identity.Revision != legacy.Revision {
			t.Fatalf("legacy identity = %#v", entry.Identity)
		}
		if err := ValidateEntryCursorRevision(entry.Identity, legacy.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if legacy.Entries[0].Identity.Key() == legacy.Entries[1].Identity.Key() {
		t.Fatal("legacy logical ordinal did not distinguish duplicate records")
	}
	legacyCursor := legacy.Entries[0].Identity

	if err := recorder.Replace(legacy.Messages); err != nil {
		t.Fatal(err)
	}
	rewritten, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.Revision == legacy.Revision {
		t.Fatal("rewrite did not advance transcript revision")
	}
	for _, entry := range rewritten.Entries {
		if entry.Identity.IsLegacy() ||
			entry.Identity.Version != TranscriptEntryIdentityVersion {
			t.Fatalf("rewritten identity = %#v", entry.Identity)
		}
	}
	if err := ValidateEntryCursorRevision(legacyCursor, rewritten.Revision); !errors.Is(err, ErrTranscriptRevisionChanged) {
		t.Fatalf("legacy cursor validation error = %v", err)
	}
	if err := ValidateEntryCursorRevision(rewritten.Entries[0].Identity, legacy.Revision); err != nil {
		t.Fatalf("persisted identity was incorrectly revision-bound: %v", err)
	}
}

func TestTranscriptEntryIdentityBranchCreatesNewSourceRecords(t *testing.T) {
	dir := t.TempDir()
	source := NewRecorder("entry-source", dir)
	if err := source.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	sourceResult, err := source.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	child, err := source.Branch("entry-child", 2)
	if err != nil {
		t.Fatal(err)
	}
	childResult, err := child.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(childResult.Entries) != 4 {
		t.Fatalf("child physical entries = %d, want 4", len(childResult.Entries))
	}
	sourceIDs := strings.Join(entryIdentityKeys(sourceResult.Entries), ",")
	for _, entry := range childResult.Entries {
		if entry.Identity.IsLegacy() || entry.Identity.ID == "" {
			t.Fatalf("child identity = %#v", entry.Identity)
		}
		if strings.Contains(sourceIDs, entry.Identity.Key()) {
			t.Fatalf("branch copied source identity %q", entry.Identity.Key())
		}
	}
}

func TestTranscriptEntryIdentityPreservesUnknownPersistedVersion(t *testing.T) {
	recorder := NewRecorder("future-entry", t.TempDir())
	line, err := json.Marshal(recordEntry{
		Timestamp: time.Now().UTC(),
		EntryID:   &EntryIdentity{Version: 99, ID: "future-id"},
		Kind:      "user",
		Message:   &schema.Message{Role: schema.User, Content: "future"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recorder.Path(), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Entries[0].Identity.Version != 99 ||
		loaded.Entries[0].Identity.ID != "future-id" ||
		loaded.Entries[0].Identity.IsLegacy() {
		t.Fatalf("future identity = %#v", loaded.Entries[0].Identity)
	}
	if err := recorder.Replace(loaded.Messages); err != nil {
		t.Fatal(err)
	}
	rewritten, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.Entries[0].Identity.Version != 99 ||
		rewritten.Entries[0].Identity.ID != "future-id" {
		t.Fatalf("rewritten future identity = %#v", rewritten.Entries[0].Identity)
	}
}

func TestTranscriptEntryIdentityDuplicatePersistedIDFallsBackSafely(t *testing.T) {
	recorder := NewRecorder("duplicate-entry-id", t.TempDir())
	identity := &EntryIdentity{Version: TranscriptEntryIdentityVersion, ID: "duplicate-id"}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	for index := range 2 {
		if err := encoder.Encode(recordEntry{
			Timestamp: time.Now().UTC(),
			EntryID:   identity,
			Kind:      "user",
			Message: &schema.Message{
				Role: schema.User, Content: fmt.Sprintf("message-%d", index),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(recorder.Path(), raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 || len(loaded.Corruptions) != 1 {
		t.Fatalf("duplicate identity load = %#v", loaded)
	}
	if loaded.Entries[0].Identity.IsLegacy() ||
		!loaded.Entries[1].Identity.IsLegacy() ||
		loaded.Entries[0].Identity.Key() == loaded.Entries[1].Identity.Key() {
		t.Fatalf("duplicate identity projection = %#v", loaded.Entries)
	}
}

func entryIdentityKeys(entries []DurableEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Identity.Key())
	}
	return keys
}

func entryTimestamps(entries []DurableEntry) []time.Time {
	timestamps := make([]time.Time, 0, len(entries))
	for _, entry := range entries {
		timestamps = append(timestamps, entry.Timestamp)
	}
	return timestamps
}
