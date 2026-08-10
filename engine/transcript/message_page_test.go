package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestLoadMessagePageBoundsModernPaginationAndFreezesAppend(t *testing.T) {
	recorder := NewRecorder("modern-page", t.TempDir())
	messages := []*schema.Message{
		{Role: schema.User, Content: "message-0"},
		{Role: schema.Assistant, Content: "message-1"},
		{Role: schema.User, Content: "same"},
		{Role: schema.User, Content: "same"},
		{Role: schema.Assistant, Content: "message-4"},
	}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	first, err := LoadMessagePage(MessagePageRequest{
		Path:     recorder.Path(),
		Limit:    2,
		MaxBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, first, "same", "message-4")
	if !first.HasMore || first.SnapshotSize == 0 || first.BytesRead > 64*1024 {
		t.Fatalf("first page = %#v", first)
	}
	if first.Entries[0].Identity.Key() == first.Entries[1].Identity.Key() ||
		first.Entries[0].Identity.Record.IsLegacy() {
		t.Fatalf("modern identities = %#v", first.Entries)
	}

	if err := recorder.RecordMessages([]*schema.Message{{Role: schema.Assistant, Content: "appended-later"}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	second, err := LoadMessagePage(MessagePageRequest{
		Path:         recorder.Path(),
		Limit:        2,
		MaxBytes:     64 * 1024,
		SnapshotSize: first.SnapshotSize,
		Boundary:     first.Next,
		ExpectedFile: first.FileInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, second, "message-1", "same")
	if !second.HasMore {
		t.Fatalf("second page should have an older record: %#v", second)
	}

	third, err := LoadMessagePage(MessagePageRequest{
		Path:         recorder.Path(),
		Limit:        2,
		MaxBytes:     64 * 1024,
		SnapshotSize: second.SnapshotSize,
		Boundary:     second.Next,
		ExpectedFile: second.FileInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, third, "message-0")
	if third.HasMore {
		t.Fatalf("third page unexpectedly has more: %#v", third)
	}
	for _, page := range []*MessagePageResult{first, second, third} {
		for _, entry := range page.Entries {
			if entry.Message.Content == "appended-later" {
				t.Fatal("append after the frozen snapshot leaked into pagination")
			}
		}
	}
}

func TestLoadMessagePageUsesNewestLifecycleAsActiveBoundary(t *testing.T) {
	recorder := NewRecorder("lifecycle-page", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "old-0"},
		{Role: schema.Assistant, Content: "old-1"},
	}); err != nil {
		t.Fatal(err)
	}
	active := []*schema.Message{
		{Role: schema.User, Content: "active-0"},
		{Role: schema.Assistant, Content: "active-1"},
		{Role: schema.Tool, Content: "active-2"},
	}
	if err := recorder.RecordLifecycleBoundary(LifecycleCheckpoint, active, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{{Role: schema.Assistant, Content: "after-boundary"}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	first, err := LoadMessagePage(MessagePageRequest{Path: recorder.Path(), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, first, "active-2", "after-boundary")
	if !first.HasMore || first.Next.LifecycleOffset < 0 {
		t.Fatalf("lifecycle continuation = %#v", first.Next)
	}
	second, err := LoadMessagePage(MessagePageRequest{
		Path:         recorder.Path(),
		Limit:        8,
		SnapshotSize: first.SnapshotSize,
		Boundary:     first.Next,
		ExpectedFile: first.FileInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, second, "active-0", "active-1")
	if second.HasMore {
		t.Fatalf("records before the lifecycle boundary became active: %#v", second)
	}
}

func TestLoadMessagePageLegacyIdentityAndRevisionValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeLegacyPageRecords(t, path, "legacy-0", "legacy-1", "legacy-2")

	first, err := LoadMessagePage(MessagePageRequest{Path: path, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, first, "legacy-2")
	if !first.Entries[0].Identity.Record.IsLegacy() ||
		first.Entries[0].Identity.Record.Revision == "" ||
		first.CompatibilityBytes != first.SnapshotSize {
		t.Fatalf("legacy page = %#v", first)
	}
	second, err := LoadMessagePage(MessagePageRequest{
		Path:         path,
		Limit:        1,
		SnapshotSize: first.SnapshotSize,
		Boundary:     first.Next,
		ExpectedFile: first.FileInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, second, "legacy-1")
	if first.Entries[0].Identity.Key() == second.Entries[0].Identity.Key() {
		t.Fatal("legacy records reused one logical identity")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(raw, []byte("legacy-0"), []byte("changed0"), 1)
	if len(changed) != len(raw) {
		t.Fatal("test mutation must preserve the snapshot size")
	}
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadMessagePage(MessagePageRequest{
		Path:         path,
		Limit:        1,
		SnapshotSize: second.SnapshotSize,
		Boundary:     second.Next,
		ExpectedFile: second.FileInfo,
	})
	if !errors.Is(err, ErrTranscriptRevisionChanged) {
		t.Fatalf("same-file legacy rewrite error = %v", err)
	}
}

func TestLoadMessagePageRejectsReplacementTruncationSymlinkAndDuplicateID(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		recorder := NewRecorder("replace", t.TempDir())
		if err := recorder.RecordMessages([]*schema.Message{
			{Role: schema.User, Content: "one"},
			{Role: schema.User, Content: "two"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Flush(); err != nil {
			t.Fatal(err)
		}
		first, err := LoadMessagePage(MessagePageRequest{Path: recorder.Path(), Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := recorder.Replace([]*schema.Message{{Role: schema.User, Content: "replacement"}}); err != nil {
			t.Fatal(err)
		}
		_, err = LoadMessagePage(MessagePageRequest{
			Path: recorder.Path(), Limit: 1, SnapshotSize: first.SnapshotSize,
			Boundary: first.Next, ExpectedFile: first.FileInfo,
		})
		if !errors.Is(err, ErrTranscriptRevisionChanged) {
			t.Fatalf("replacement error = %v", err)
		}
	})

	t.Run("truncate", func(t *testing.T) {
		recorder := NewRecorder("truncate", t.TempDir())
		if err := recorder.RecordMessages([]*schema.Message{
			{Role: schema.User, Content: "one"},
			{Role: schema.User, Content: "two"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Flush(); err != nil {
			t.Fatal(err)
		}
		first, err := LoadMessagePage(MessagePageRequest{Path: recorder.Path(), Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(recorder.Path(), first.SnapshotSize-1); err != nil {
			t.Fatal(err)
		}
		_, err = LoadMessagePage(MessagePageRequest{
			Path: recorder.Path(), Limit: 1, SnapshotSize: first.SnapshotSize,
			Boundary: first.Next, ExpectedFile: first.FileInfo,
		})
		if !errors.Is(err, ErrTranscriptRevisionChanged) {
			t.Fatalf("truncate error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink semantics require privileges on Windows")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "target.jsonl")
		writeLegacyPageRecords(t, target, "one")
		link := filepath.Join(dir, "link.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMessagePage(MessagePageRequest{Path: link, Limit: 1}); err == nil {
			t.Fatal("symlink transcript was accepted")
		}
	})

	t.Run("duplicate-persisted-id", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "duplicate.jsonl")
		identity := &EntryIdentity{Version: 1, ID: "duplicate"}
		var raw bytes.Buffer
		encoder := json.NewEncoder(&raw)
		for _, content := range []string{"one", "two"} {
			if err := encoder.Encode(recordEntry{
				Timestamp: time.Now().UTC(), EntryID: identity, Kind: "user",
				Message: &schema.Message{Role: schema.User, Content: content},
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadMessagePage(MessagePageRequest{Path: path, Limit: 2})
		if !errors.Is(err, ErrTranscriptEntryIdentityConflict) {
			t.Fatalf("duplicate identity error = %v", err)
		}
	})
}

func TestLoadMessagePageZeroLimitAndCorruptTailRemainBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.jsonl")
	var raw bytes.Buffer
	raw.WriteString("{bad json}\n")
	encoder := json.NewEncoder(&raw)
	if err := encoder.Encode(recordEntry{
		Timestamp: time.Now().UTC(),
		EntryID:   &EntryIdentity{Version: 1, ID: "valid"},
		Kind:      "assistant",
		Message:   &schema.Message{Role: schema.Assistant, Content: "valid"},
	}); err != nil {
		t.Fatal(err)
	}
	raw.WriteString("not-json\n")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := LoadMessagePage(MessagePageRequest{Path: path, Limit: 0, MaxBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 0 || !snapshot.HasMore || snapshot.BytesRead != 0 {
		t.Fatalf("zero-limit snapshot = %#v", snapshot)
	}
	page, err := LoadMessagePage(MessagePageRequest{
		Path: path, Limit: 1, MaxBytes: 1024,
		SnapshotSize: snapshot.SnapshotSize, Boundary: snapshot.Next, ExpectedFile: snapshot.FileInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPageContents(t, page, "valid")
	if page.Corruptions != 1 || page.BytesRead > 1024 {
		t.Fatalf("corruption projection = %#v", page)
	}
}

func TestLoadMessagePageRejectsRecordOutsideReadBudget(t *testing.T) {
	recorder := NewRecorder("oversized", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{{
		Role: schema.Assistant, Content: string(bytes.Repeat([]byte{'x'}, 4*1024)),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMessagePage(MessagePageRequest{
		Path: recorder.Path(), Limit: 1, MaxBytes: 128,
	})
	if !errors.Is(err, ErrTranscriptPageRecordTooLarge) {
		t.Fatalf("oversized record error = %v", err)
	}
}

func TestRecorderTracksLifecycleMessageSubidentity(t *testing.T) {
	recorder := NewRecorder("lifecycle-identity", t.TempDir())
	messages := []*schema.Message{
		{Role: schema.User, Content: "one"},
		{Role: schema.Assistant, Content: "two"},
	}
	if err := recorder.RecordLifecycleBoundary(LifecycleCheckpoint, messages, nil, nil); err != nil {
		t.Fatal(err)
	}
	identity, ok := recorder.LatestMessageEntryIdentity(messages[1])
	if !ok || identity.Index != 1 || identity.Record.ID == "" {
		t.Fatalf("lifecycle message identity = %#v, found=%v", identity, ok)
	}
	page, err := LoadMessagePage(MessagePageRequest{Path: recorder.Path(), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[1].Identity.Key() != identity.Key() {
		t.Fatalf("tracked/page identities differ: tracked=%#v page=%#v", identity, page)
	}
}

func assertPageContents(t *testing.T, page *MessagePageResult, contents ...string) {
	t.Helper()
	if len(page.Entries) != len(contents) {
		t.Fatalf("page entries = %#v, want %v", page.Entries, contents)
	}
	for index, content := range contents {
		if page.Entries[index].Message == nil || page.Entries[index].Message.Content != content {
			t.Fatalf("page[%d] = %#v, want %q", index, page.Entries[index], content)
		}
	}
}

func writeLegacyPageRecords(t *testing.T, path string, contents ...string) {
	t.Helper()
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	for _, content := range contents {
		if err := encoder.Encode(recordEntry{
			Timestamp: time.Now().UTC(),
			Kind:      "user",
			Message:   &schema.Message{Role: schema.User, Content: content},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
