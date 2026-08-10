package transcript

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/internal/tui/attachments"
)

func TestRecorderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder("session-1", filepath.Join(dir, "sessions"))
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "world"},
	}
	if err := rec.Record(messages[:1], false); err != nil {
		t.Fatalf("record user: %v", err)
		return
	}
	if err := rec.Record(messages[1:], true); err != nil {
		t.Fatalf("record assistant: %v", err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
		return
	}

	loaded, err := rec.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
		return
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded))
	}
	if loaded[0].Content != "hello" || loaded[1].Content != "world" {
		t.Fatalf("unexpected transcript contents: %#v", loaded)
	}
}

func TestRecorderCreatesPrivateTranscript(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	recorder := NewRecorder("private-session", dir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: "private"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("transcript mode=%04o dir mode=%04o, want 0600/0700", fileInfo.Mode().Perm(), dirInfo.Mode().Perm())
	}
}

func TestAtomicReplaceTightensReusedTemporaryFileMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	recorder := NewRecorder("private-rewrite", dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recorder.Path()+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recorder.AtomicReplace([]*schema.Message{{Role: schema.User, Content: "private"}}); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("rewritten transcript mode=%04o, want 0600", fileInfo.Mode().Perm())
	}
}

func TestP300TUILegalImageTurnExceedsTranscriptScannerBudget(t *testing.T) {
	const scannerBudget = 8 * 1024 * 1024
	imageBytes := 3*1024*1024 + 256*1024
	if imageBytes >= attachments.MaxAttachmentBytes ||
		2*imageBytes >= 2*attachments.MaxAttachmentBytes {
		t.Fatal("fixture no longer fits the current TUI per-image and aggregate bounds")
	}
	encoded := base64.StdEncoding.EncodeToString(make([]byte, imageBytes))
	message := &schema.Message{
		Role:    schema.User,
		Content: "compare the images",
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "before"},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   "image/png",
					},
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: "between"},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   "image/png",
					},
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: "after"},
		},
	}

	recorder := NewRecorder("p30-inline-media", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= scannerBudget {
		t.Fatalf("encoded record size = %d, want > %d", info.Size(), scannerBudget)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 0 ||
		len(loaded.Corruptions) != 1 ||
		!strings.Contains(loaded.Corruptions[0].Err.Error(), "token too long") {
		t.Fatalf("load result messages=%d corruptions=%#v", len(loaded.Messages), loaded.Corruptions)
	}
}

func TestRecorderLoadsVersionedAgentCompletionReceiptsWithBound(t *testing.T) {
	dir := t.TempDir()
	recorder := NewRecorder("completion-receipts", dir)
	messages := make([]*schema.Message, 0, maxLoadedAgentCompletionReceipts+1)
	for index := 0; index < maxLoadedAgentCompletionReceipts+1; index++ {
		completionID := fmt.Sprintf("completion-%03d", index)
		messages = append(messages, &schema.Message{
			Role:    schema.User,
			Content: completionID,
			Extra: map[string]any{
				"runtime_item_id": completionID,
				AgentCompletionReceiptExtraKey(): AgentCompletionReceipt{
					Version:          AgentCompletionReceiptVersion,
					CompletionID:     completionID,
					AgentID:          fmt.Sprintf("agent-%03d", index),
					Generation:       1,
					TerminalStatus:   "completed",
					TerminalSequence: 1,
					ParentSessionID:  "completion-receipts",
					ParentThreadID:   "completion-receipts",
					DeliveredAt:      time.Unix(int64(index+1), 0).UTC(),
				},
			},
		})
	}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.AgentCompletionReceipts) != maxLoadedAgentCompletionReceipts {
		t.Fatalf(
			"loaded receipts = %d, want %d",
			len(loaded.AgentCompletionReceipts),
			maxLoadedAgentCompletionReceipts,
		)
	}
	if loaded.AgentCompletionReceipts[0].CompletionID != "completion-001" ||
		loaded.AgentCompletionReceipts[len(loaded.AgentCompletionReceipts)-1].
			CompletionID != "completion-256" {
		t.Fatalf("bounded receipts = %#v", loaded.AgentCompletionReceipts)
	}
	if receipt, ok := AgentCompletionReceiptFromMessage(loaded.Messages[0]); !ok ||
		receipt.CompletionID != "completion-000" {
		t.Fatalf("retained message receipt = %#v, ok=%v", receipt, ok)
	}
}

func TestRecorderPreservesUnknownAgentCompletionReceiptVersion(t *testing.T) {
	recorder := NewRecorder("unknown-completion-receipt", t.TempDir())
	message := &schema.Message{
		Role:    schema.User,
		Content: "unknown receipt",
		Extra: map[string]any{
			AgentCompletionReceiptExtraKey(): map[string]any{
				"version":           99,
				"completion_id":     "completion-unknown",
				"agent_id":          "agent-unknown",
				"generation":        4,
				"terminal_status":   "completed",
				"terminal_sequence": 4,
				"parent_session_id": "unknown-completion-receipt",
				"parent_thread_id":  "unknown-completion-receipt",
				"delivered_at":      time.Now().UTC(),
			},
		},
	}
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.AgentCompletionReceipts) != 1 ||
		loaded.AgentCompletionReceipts[0].Version != 99 ||
		loaded.AgentCompletionReceipts[0].CompletionID != "completion-unknown" {
		t.Fatalf("unknown receipt = %#v", loaded.AgentCompletionReceipts)
	}
	if len(loaded.Messages) != 1 ||
		loaded.Messages[0].Content != "unknown receipt" {
		t.Fatalf("message diagnostic was lost: %#v", loaded.Messages)
	}
}

func TestBranchWithStateCommitsCompleteChildWithoutMutatingSource(t *testing.T) {
	dir := t.TempDir()
	source := NewRecorder("branch-source", dir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}
	if err := source.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		messages,
		[]Replacement{{ToolUseID: "tool-1", Replacement: "preview"}},
		map[string]FileState{
			"/tmp/source.go": {Path: "/tmp/source.go", WasRead: true},
		},
	); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source.Path())
	if err != nil {
		t.Fatal(err)
	}

	child, err := source.BranchWithState(
		"branch-child",
		len(messages),
		BranchState{
			Replacements: []Replacement{{
				ToolUseID:   "tool-1",
				Replacement: "preview",
			}},
			FileSnapshots: []map[string]FileState{{
				"/tmp/source.go": {Path: "/tmp/source.go", WasRead: true},
			}},
			Metadata: []MetadataEntry{{
				Key:   "fork_operation_id",
				Value: "operation-1",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(source.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("branch mutated source transcript bytes")
	}
	loaded, err := child.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 ||
		len(loaded.Replacements) != 1 ||
		len(loaded.FileSnapshots) != 1 ||
		loaded.FileSnapshots[0]["/tmp/source.go"].Path != "/tmp/source.go" {
		t.Fatalf("child state = %#v", loaded)
	}
	metadata := make(map[string]string)
	for _, entry := range loaded.Metadata {
		metadata[entry.Key] = entry.Value
	}
	if metadata["parent_session_id"] != "branch-source" ||
		metadata["branch_point"] != "2" ||
		metadata["fork_operation_id"] != "operation-1" {
		t.Fatalf("child metadata = %#v", metadata)
	}
}

func TestBranchWithStateNeverClobbersExistingChild(t *testing.T) {
	dir := t.TempDir()
	source := NewRecorder("branch-no-clobber-source", dir)
	if err := source.RecordMessages([]*schema.Message{{
		Role: schema.User, Content: "source",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	target := NewRecorder("branch-no-clobber-child", dir)
	if err := target.RecordMessages([]*schema.Message{{
		Role: schema.User, Content: "existing",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target.Path())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := source.Branch(target.SessionID, 1); !errors.Is(err, os.ErrExist) {
		t.Fatalf("branch error = %v, want target-exists error", err)
	}
	after, err := os.ReadFile(target.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("branch replaced an existing child transcript")
	}
}

func TestBranchWithStateFailureBoundaries(t *testing.T) {
	t.Run("before commit", func(t *testing.T) {
		dir := t.TempDir()
		source := NewRecorder("branch-sync-source", dir)
		if err := source.RecordMessages([]*schema.Message{{
			Role: schema.User, Content: "source",
		}}); err != nil {
			t.Fatal(err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		source.syncFile = func(*os.File) error {
			return errors.New("injected fork sync failure")
		}
		_, err := source.Branch("branch-sync-child", 1)
		if err == nil || IsDurabilityUncertain(err) {
			t.Fatalf("pre-commit error = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "branch-sync-child.jsonl")); !os.IsNotExist(statErr) {
			t.Fatalf("pre-commit failure exposed child: %v", statErr)
		}
	})

	t.Run("after commit", func(t *testing.T) {
		dir := t.TempDir()
		source := NewRecorder("branch-dir-source", dir)
		if err := source.RecordMessages([]*schema.Message{{
			Role: schema.User, Content: "source",
		}}); err != nil {
			t.Fatal(err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		source.syncDir = func(string) error {
			return errors.New("injected fork directory sync failure")
		}
		_, err := source.Branch("branch-dir-child", 1)
		if !IsDurabilityUncertain(err) {
			t.Fatalf("post-commit error = %v, want durability uncertainty", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "branch-dir-child.jsonl")); statErr != nil {
			t.Fatalf("post-commit target is not inspectable: %v", statErr)
		}
	})
}

func TestRecorderReplaceRewritesTranscript(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder("session-2", filepath.Join(dir, "sessions"))
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "old user"}}, false); err != nil {
		t.Fatalf("record old user: %v", err)
		return
	}
	if err := rec.Record([]*schema.Message{{Role: schema.Assistant, Content: "old assistant"}}, true); err != nil {
		t.Fatalf("record old assistant: %v", err)
		return
	}
	replacement := []*schema.Message{
		{Role: schema.System, Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.System, Content: "summary", Extra: map[string]any{"subtype": "compact_summary"}},
		{Role: schema.User, Content: "latest question"},
	}
	if err := rec.Replace(replacement); err != nil {
		t.Fatalf("replace: %v", err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
		return
	}
	loaded, err := rec.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
		return
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 messages after replace, got %d", len(loaded))
	}
	if loaded[0].Extra == nil || loaded[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected compact boundary first after replace, got %#v", loaded[0])
		return
	}
	if loaded[1].Extra == nil || loaded[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected compact summary second after replace, got %#v", loaded[1])
		return
	}
	if loaded[2].Content != "latest question" {
		t.Fatalf("expected preserved tail after replace, got %#v", loaded[2])
	}
}

func TestLifecycleBoundariesReplayActiveContextWithoutDeletingAudit(t *testing.T) {
	rec := NewRecorder("lifecycle", t.TempDir())
	original := []*schema.Message{
		{Role: schema.User, Content: "original question"},
		{Role: schema.Assistant, Content: "original answer"},
	}
	if err := rec.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		original,
		[]Replacement{{ToolUseID: "tool-1", Replacement: "preview"}},
		map[string]FileState{
			"/tmp/original.go": {Path: "/tmp/original.go", WasRead: true},
		},
	); err != nil {
		t.Fatalf("record original checkpoint: %v", err)
	}
	if err := rec.RecordLifecycleBoundary(LifecycleReset, nil, nil, nil); err != nil {
		t.Fatalf("record reset: %v", err)
	}
	if err := rec.Record(
		[]*schema.Message{{Role: schema.User, Content: "after reset"}},
		false,
	); err != nil {
		t.Fatalf("record post-reset message: %v", err)
	}

	loaded, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("load lifecycle transcript: %v", err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "after reset" {
		t.Fatalf("active messages = %#v", loaded.Messages)
	}
	if len(loaded.Replacements) != 0 || len(loaded.FileSnapshots) != 0 {
		t.Fatalf(
			"reset retained active auxiliary state: replacements=%#v files=%#v",
			loaded.Replacements,
			loaded.FileSnapshots,
		)
	}
	if len(loaded.LifecycleBoundaries) != 2 {
		t.Fatalf("lifecycle boundaries = %#v", loaded.LifecycleBoundaries)
	}
	if got := loaded.LifecycleBoundaries[0].Messages[0].Content; got != "original question" {
		t.Fatalf("audited checkpoint message = %q", got)
	}
	raw, err := os.ReadFile(rec.Path())
	if err != nil {
		t.Fatalf("read transcript audit: %v", err)
	}
	if !strings.Contains(string(raw), "original question") ||
		!strings.Contains(string(raw), string(LifecycleReset)) {
		t.Fatalf("transcript audit lost prior state:\n%s", raw)
	}
}

func TestCompactBoundaryRestoresSnapshotAndAuxiliaryState(t *testing.T) {
	rec := NewRecorder("compact-lifecycle", t.TempDir())
	if err := rec.RecordLifecycleBoundary(
		LifecycleCompact,
		[]*schema.Message{{Role: schema.System, Content: "durable summary"}},
		[]Replacement{{ToolUseID: "tool-2", Replacement: "compacted preview"}},
		map[string]FileState{
			"/tmp/compact.go": {Path: "/tmp/compact.go", WasEdit: true},
		},
	); err != nil {
		t.Fatalf("record compact boundary: %v", err)
	}

	loaded, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("load compact boundary: %v", err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "durable summary" {
		t.Fatalf("messages = %#v", loaded.Messages)
	}
	if len(loaded.Replacements) != 1 ||
		loaded.Replacements[0].ToolUseID != "tool-2" {
		t.Fatalf("replacements = %#v", loaded.Replacements)
	}
	if len(loaded.FileSnapshots) != 1 ||
		!loaded.FileSnapshots[0]["/tmp/compact.go"].WasEdit {
		t.Fatalf("file snapshots = %#v", loaded.FileSnapshots)
	}
}

func TestLifecycleBoundarySyncFailureReportsIndeterminateReplayState(t *testing.T) {
	rec := NewRecorder("sync-failure", t.TempDir())
	rec.syncFile = func(*os.File) error {
		return errors.New("injected sync failure")
	}
	err := rec.RecordLifecycleBoundary(
		LifecycleReset,
		nil,
		nil,
		nil,
	)
	if !IsDurabilityUncertain(err) {
		t.Fatalf("sync error = %v, want durability uncertainty", err)
	}
	loaded, loadErr := rec.LoadFull()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.LifecycleBoundaries) != 1 ||
		loaded.LifecycleBoundaries[0].Kind != LifecycleReset {
		t.Fatalf(
			"sync-error replay must determine visible state: %#v",
			loaded.LifecycleBoundaries,
		)
	}
}

func TestLifecycleBoundarySyncsNewTranscriptParentDirectoryOnce(t *testing.T) {
	rec := NewRecorder("directory-sync", t.TempDir())
	var syncCalls int
	rec.syncDir = func(path string) error {
		syncCalls++
		if path != rec.Dir {
			t.Fatalf("synced directory = %q, want %q", path, rec.Dir)
		}
		return nil
	}
	if err := rec.RecordLifecycleBoundary(
		LifecycleSessionStart,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordLifecycleBoundary(
		LifecycleReset,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if syncCalls != 1 {
		t.Fatalf("parent directory sync calls = %d, want 1", syncCalls)
	}
}

func TestLifecycleBoundaryDirectorySyncFailureIsIndeterminate(t *testing.T) {
	rec := NewRecorder("directory-sync-failure", t.TempDir())
	rec.syncDir = func(string) error {
		return errors.New("injected directory sync failure")
	}
	err := rec.RecordLifecycleBoundary(
		LifecycleSessionStart,
		nil,
		nil,
		nil,
	)
	if !IsDurabilityUncertain(err) {
		t.Fatalf("directory sync error = %v, want durability uncertainty", err)
	}
}

func TestMessageEncodeFailureRepairsPartialJSONLBeforeCheckpoint(t *testing.T) {
	rec := NewRecorder("message-encode-repair", t.TempDir())
	if err := rec.RecordMessages([]*schema.Message{{
		Role:    schema.User,
		Content: "known complete line",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	err := rec.RecordMessages([]*schema.Message{{
		Role:    schema.User,
		Content: "cannot encode",
		Extra: map[string]any{
			"unsupported": make(chan struct{}),
		},
	}})
	if !IsDurabilityUncertain(err) {
		t.Fatalf("message encode error = %v, want durability uncertainty", err)
	}

	// json.Encoder currently buffers unsupported-value failures before Write.
	// Add the partial bytes that an underlying short write may leave so the
	// recovery path is exercised independently of that implementation detail.
	file, openErr := os.OpenFile(rec.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, writeErr := file.WriteString(`{"timestamp":"partial"`); writeErr != nil {
		_ = file.Close()
		t.Fatal(writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	checkpoint := []*schema.Message{{
		Role:    schema.User,
		Content: "repaired active state",
	}}
	if err := rec.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		checkpoint,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	loaded, loadErr := rec.LoadFull()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Corruptions) != 0 ||
		len(loaded.Messages) != 1 ||
		loaded.Messages[0].Content != "repaired active state" {
		t.Fatalf("repaired replay = %#v", loaded)
	}
	raw, readErr := os.ReadFile(rec.Path())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(raw), `"timestamp":"partial"`) {
		t.Fatalf("partial JSONL suffix survived repair:\n%s", raw)
	}
}

func TestFileSnapshotWriteFailureRepairsPartialJSONLBeforeCheckpoint(
	t *testing.T,
) {
	rec := NewRecorder("file-snapshot-repair", t.TempDir())
	if err := rec.RecordMessages([]*schema.Message{{
		Role:    schema.User,
		Content: "known complete line",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}

	rec.mu.Lock()
	if rec.file == nil {
		rec.mu.Unlock()
		t.Fatal("recorder did not retain an open transcript file")
	}
	if err := rec.file.Close(); err != nil {
		rec.mu.Unlock()
		t.Fatal(err)
	}
	rec.mu.Unlock()

	path := "/workspace/repaired.go"
	err := rec.RecordFileHistorySnapshot(map[string]FileState{
		path: {Path: path, WasRead: true},
	})
	if !IsDurabilityUncertain(err) {
		t.Fatalf(
			"file snapshot error = %v, want durability uncertainty",
			err,
		)
	}

	// Model an underlying short write independently of os.File's closed-file
	// error so the next lifecycle boundary must trim a partial JSONL suffix.
	file, openErr := os.OpenFile(rec.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, writeErr := file.WriteString(`{"timestamp":"partial-file-state"`); writeErr != nil {
		_ = file.Close()
		t.Fatal(writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	checkpoint := []*schema.Message{{
		Role:    schema.User,
		Content: "repaired active state",
	}}
	if err := rec.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		checkpoint,
		nil,
		map[string]FileState{
			path: {Path: path, WasRead: true},
		},
	); err != nil {
		t.Fatal(err)
	}
	loaded, loadErr := rec.LoadFull()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Corruptions) != 0 ||
		len(loaded.Messages) != 1 ||
		loaded.Messages[0].Content != "repaired active state" ||
		len(loaded.FileSnapshots) != 1 {
		t.Fatalf("repaired replay = %#v", loaded)
	}
	state := loaded.FileSnapshots[0][path]
	if !state.WasRead || state.WasEdit || state.WasWrite {
		t.Fatalf("repaired file state = %#v", state)
	}
	raw, readErr := os.ReadFile(rec.Path())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(raw), `"timestamp":"partial-file-state"`) {
		t.Fatalf("partial JSONL suffix survived repair:\n%s", raw)
	}
}

func TestRecorderReplaceIsAtomicForConcurrentReaders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	rec := NewRecorder("atomic-replace", dir)
	const messageCount = 256
	buildMessages := func(version string) []*schema.Message {
		messages := make([]*schema.Message, 0, messageCount)
		for i := 0; i < messageCount; i++ {
			messages = append(messages, &schema.Message{
				Role: schema.User, Content: fmt.Sprintf("%s-%03d-%s", version, i, strings.Repeat("x", 1024)),
			})
		}
		return messages
	}
	versions := [][]*schema.Message{buildMessages("alpha"), buildMessages("bravo")}
	if err := rec.Replace(versions[0]); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	errorsCh := make(chan error, 1)
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			reader := NewRecorder("atomic-replace", dir)
			for {
				select {
				case <-stop:
					return
				default:
				}
				loaded, err := reader.Load()
				if err != nil {
					select {
					case errorsCh <- err:
					default:
					}
					return
				}
				if len(loaded) != messageCount {
					select {
					case errorsCh <- fmt.Errorf("observed partial transcript with %d messages", len(loaded)):
					default:
					}
					return
				}
				prefix := strings.SplitN(loaded[0].Content, "-", 2)[0]
				for _, message := range loaded[1:] {
					if !strings.HasPrefix(message.Content, prefix+"-") {
						select {
						case errorsCh <- fmt.Errorf("observed mixed transcript versions"):
						default:
						}
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if err := rec.Replace(versions[i%len(versions)]); err != nil {
			close(stop)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case err := <-errorsCh:
		t.Fatal(err)
	default:
	}
}
